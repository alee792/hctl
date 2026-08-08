package discordadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hctl/channeladapter"

	"github.com/bwmarrin/discordgo"
)

var discordFeatures = []channeladapter.Feature{
	channeladapter.FeatureTyping,
	channeladapter.FeatureReplies,
	channeladapter.FeatureEdits,
	channeladapter.FeatureReactions,
	channeladapter.FeatureAttachments,
	channeladapter.FeatureInteractiveComponents,
	channeladapter.FeatureTextFallback,
}

var discordLimits = channeladapter.Limits{
	MaxFrameBytes:      channeladapter.MaxFrameBytes,
	MaxTextBytes:       channeladapter.MaxTextBytes,
	MaxAttachments:     channeladapter.MaxAttachments,
	MaxAttachmentBytes: channeladapter.MaxAttachmentBytes,
	MaxOutstanding:     channeladapter.MaxOutstanding,
}

type protocolWriter struct {
	mu              sync.Mutex
	output          io.Writer
	after           func(time.Duration) <-chan time.Time
	fatal           chan<- error
	queue           chan queuedProtocolFrame
	capacityChanged chan struct{}
	queueCount      int
	queueBytes      int
	maxFrameBytes   int
	maxOutstanding  int
	failed          error
	next            atomic.Uint64
	pending         map[string]channeladapter.Envelope
	eventKeys       map[string]string
}

type queuedProtocolFrame struct {
	data []byte
}

func newProtocolWriter(output io.Writer, after func(time.Duration) <-chan time.Time, fatal chan<- error) *protocolWriter {
	writer := &protocolWriter{
		output: output, after: after, fatal: fatal,
		queue: make(chan queuedProtocolFrame, channeladapter.MaxQueuedFrames), capacityChanged: make(chan struct{}),
		maxFrameBytes: channeladapter.MaxFrameBytes, maxOutstanding: channeladapter.MaxOutstanding,
		pending: map[string]channeladapter.Envelope{}, eventKeys: map[string]string{},
	}
	go writer.run()
	return writer
}

func (writer *protocolWriter) run() {
	for item := range writer.queue {
		completed := make(chan error, 1)
		progress := make(chan struct{}, 1)
		go func(data []byte) { completed <- writeAll(writer.output, data, progress) }(item.data)
		var err error
	write:
		for {
			select {
			case err = <-completed:
				break write
			case <-progress:
				// Start a fresh no-progress window after every successful partial write.
			case <-writer.after(channeladapter.CommandTimeout):
				err = errors.New("Discord adapter protocol output made no progress")
				break write
			}
		}
		writer.mu.Lock()
		writer.queueCount--
		writer.queueBytes -= len(item.data)
		if err != nil {
			writer.failed = err
		}
		terminal := writer.failed
		writer.notifyCapacityLocked()
		writer.mu.Unlock()
		if terminal != nil {
			if err != nil {
				writer.fail(err)
			}
			return
		}
	}
}

func writeAll(output io.Writer, data []byte, progress chan<- struct{}) error {
	for len(data) > 0 {
		written, err := output.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
		select {
		case progress <- struct{}{}:
		default:
		}
	}
	return nil
}

func (writer *protocolWriter) fail(err error) {
	select {
	case writer.fatal <- err:
	default:
	}
}

func (writer *protocolWriter) notifyCapacityLocked() {
	close(writer.capacityChanged)
	writer.capacityChanged = make(chan struct{})
}

func (writer *protocolWriter) send(payload channeladapter.Payload, correlation string) error {
	return writer.sendContext(context.Background(), payload, correlation)
}

func (writer *protocolWriter) sendContext(ctx context.Context, payload channeladapter.Payload, correlation string) error {
	_, err := writer.sendIDContext(ctx, payload, correlation)
	return err
}

func (writer *protocolWriter) sendID(payload channeladapter.Payload, correlation string) (string, error) {
	return writer.sendIDContext(context.Background(), payload, correlation)
}

func (writer *protocolWriter) sendIDContext(ctx context.Context, payload channeladapter.Payload, correlation string) (string, error) {
	id := fmt.Sprintf("adapter.%08x", writer.next.Add(1))
	envelope := channeladapter.Envelope{ProtocolVersion: channeladapter.ProtocolVersion, ID: id, CorrelationID: correlation, Payload: payload}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	err := writer.enqueueLockedContext(ctx, envelope)
	return id, err
}

func (writer *protocolWriter) sendEvent(key string, payload channeladapter.Payload, correlation string) (string, error) {
	return writer.sendEventContext(context.Background(), key, payload, correlation)
}

func (writer *protocolWriter) sendEventContext(ctx context.Context, key string, payload channeladapter.Payload, correlation string) (string, error) {
	return writer.sendEventRegisteredContext(ctx, key, payload, correlation, nil)
}

func (writer *protocolWriter) sendEventRegisteredContext(ctx context.Context, key string, payload channeladapter.Payload, correlation string, register func(string, bool)) (string, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if id := writer.eventKeys[key]; id != "" {
		stored := writer.pending[id]
		candidate := stored
		candidate.Payload = payload
		oldBytes, oldErr := channeladapter.MarshalFrame(stored, channeladapter.FromAdapter)
		newBytes, newErr := channeladapter.MarshalFrame(candidate, channeladapter.FromAdapter)
		if oldErr != nil || newErr != nil || !bytes.Equal(oldBytes, newBytes) {
			return "", errors.New("Discord replay changed content under a stable source id")
		}
		return id, writer.enqueueLockedContext(ctx, stored)
	}
	if len(writer.pending) >= writer.maxOutstanding {
		err := errors.New("Discord adapter has too many unacknowledged events")
		writer.failed = err
		writer.notifyCapacityLocked()
		writer.fail(err)
		return "", err
	}
	id := fmt.Sprintf("adapter.%08x", writer.next.Add(1))
	envelope := channeladapter.Envelope{ProtocolVersion: channeladapter.ProtocolVersion, ID: id, CorrelationID: correlation, Payload: payload}
	if register != nil {
		register(id, true)
	}
	if err := writer.enqueueLockedContext(ctx, envelope); err != nil {
		if register != nil {
			register(id, false)
		}
		return "", err
	}
	writer.pending[id], writer.eventKeys[key] = envelope, id
	return id, nil
}

func (writer *protocolWriter) enqueueLockedContext(ctx context.Context, envelope channeladapter.Envelope) error {
	if writer.failed != nil {
		return writer.failed
	}
	select {
	case <-ctx.Done():
		return errors.New("Discord adapter protocol output admission was cancelled")
	default:
	}
	data, err := channeladapter.MarshalFrame(envelope, channeladapter.FromAdapter)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data)-1 > writer.maxFrameBytes {
		err := errors.New("Discord adapter frame exceeds the negotiated frame limit")
		writer.failed = err
		writer.notifyCapacityLocked()
		writer.fail(err)
		return err
	}
	for writer.queueCount >= channeladapter.MaxQueuedFrames || writer.queueBytes+len(data) > channeladapter.MaxQueuedBytes {
		changed := writer.capacityChanged
		writer.mu.Unlock()
		select {
		case <-changed:
			writer.mu.Lock()
			if writer.failed != nil {
				return writer.failed
			}
			continue
		case <-ctx.Done():
			writer.mu.Lock()
			return errors.New("Discord adapter protocol output admission deadline elapsed")
		case <-writer.after(channeladapter.CommandTimeout):
			writer.mu.Lock()
			err := errors.New("Discord adapter protocol output queue made no progress")
			writer.failed = err
			writer.notifyCapacityLocked()
			writer.fail(err)
			return err
		}
	}
	item := queuedProtocolFrame{data: data}
	select {
	case writer.queue <- item:
		writer.queueCount++
		writer.queueBytes += len(data)
		return nil
	default:
		err := errors.New("Discord adapter protocol output queue invariant failed")
		writer.failed = err
		writer.notifyCapacityLocked()
		writer.fail(err)
		return err
	}
}

func (writer *protocolWriter) setBounds(maxFrameBytes, maxOutstanding int) {
	writer.mu.Lock()
	writer.maxFrameBytes, writer.maxOutstanding = maxFrameBytes, maxOutstanding
	writer.mu.Unlock()
}

func (writer *protocolWriter) acknowledge(id string) bool {
	writer.mu.Lock()
	found := false
	if _, ok := writer.pending[id]; ok {
		found = true
		delete(writer.pending, id)
		for key, eventID := range writer.eventKeys {
			if eventID == id {
				delete(writer.eventKeys, key)
				break
			}
		}
	}
	writer.mu.Unlock()
	return found
}

func (writer *protocolWriter) replayEventsContext(ctx context.Context) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	ids := make([]string, 0, len(writer.pending))
	for id := range writer.pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := writer.enqueueLockedContext(ctx, writer.pending[id]); err != nil {
			return err
		}
	}
	return nil
}

func (writer *protocolWriter) drain(timeout time.Duration) error {
	deadline := writer.after(timeout)
	for {
		writer.mu.Lock()
		count := writer.queueCount
		if count == 0 {
			writer.mu.Unlock()
			return nil
		}
		changed := writer.capacityChanged
		writer.mu.Unlock()
		select {
		case <-changed:
		case <-deadline:
			return errors.New("Discord adapter protocol output did not drain")
		}
	}
}

func (writer *protocolWriter) drainContext(ctx context.Context) error {
	for {
		writer.mu.Lock()
		count := writer.queueCount
		if count == 0 {
			writer.mu.Unlock()
			return nil
		}
		changed := writer.capacityChanged
		writer.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return errors.New("Discord adapter protocol output did not drain before shutdown")
		}
	}
}

type pendingControl struct {
	interaction *discordgo.Interaction
	action      channeladapter.ControlAction
}

type pendingInteraction struct {
	hostFrameID  string
	request      channeladapter.InteractionRequest
	handle       string
	expiryCancel context.CancelFunc
}

type pendingCallback struct {
	interaction *discordgo.Interaction
	route       string
	message     string
	cancelled   bool
}

type attachmentSource struct {
	url          string
	size         int64
	generation   uint64
	expiryCancel context.CancelFunc
}

type outboundTransfer struct {
	content      bytes.Buffer
	next         int
	name         string
	mediaType    string
	complete     bool
	generation   uint64
	expiryCancel context.CancelFunc
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Runtime struct {
	profiles    ProfileStore
	credentials CredentialStore
	factory     DiscordFactory
	locks       LockFactory
	writer      *protocolWriter
	http        HTTPClient
	after       func(time.Duration) <-chan time.Time

	mu               sync.Mutex
	activeAdmissions int
	admissionIdle    chan struct{}
	profile          Profile
	client           Discord
	controls         map[string]pendingControl
	interactions     map[string]pendingInteraction
	callbacks        map[string]pendingCallback
	callbackExpiry   map[string]context.CancelFunc
	handles          map[string]string
	attachments      map[string]attachmentSource
	attachmentSeq    atomic.Uint64
	transferSeq      atomic.Uint64
	fetches          map[string]context.CancelFunc
	outbound         map[string]*outboundTransfer
	lock             ApplicationLock
	closed           bool
	shuttingDown     bool
	runCancel        context.CancelFunc
	runContext       context.Context
	connectionTry    int
	fatal            chan error
	features         map[channeladapter.Feature]bool
	limits           channeladapter.Limits
	policy           channeladapter.RuntimePolicy
}

func (runtime *Runtime) setNegotiated(initialize channeladapter.Initialize) {
	features := make(map[channeladapter.Feature]bool, len(initialize.Features))
	for _, feature := range initialize.Features {
		features[feature] = true
	}
	runtime.mu.Lock()
	runtime.features, runtime.limits, runtime.policy = features, initialize.Limits, initialize.Policy
	runtime.mu.Unlock()
}

func (runtime *Runtime) supports(feature channeladapter.Feature) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.features[feature]
}

func (runtime *Runtime) inboundTextLimit() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return min(runtime.limits.MaxTextBytes, runtime.policy.MaxInboundTextBytes)
}

func (runtime *Runtime) deliveryTextLimit() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return min(runtime.limits.MaxTextBytes, runtime.policy.MaxDeliveryTextBytes)
}

func (runtime *Runtime) attachmentLimits() (count, bytes int) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.limits.MaxAttachments, min(runtime.limits.MaxAttachmentBytes, runtime.policy.MaxAttachmentBytes)
}

func NewRuntime(output io.Writer, dependencies Dependencies) (*Runtime, error) {
	if err := dependencies.validate(); err != nil {
		return nil, err
	}
	after := dependencies.After
	if after == nil {
		after = time.After
	}
	writeAfter := dependencies.WriteAfter
	if writeAfter == nil {
		writeAfter = time.After
	}
	client := dependencies.HTTP
	if client == nil {
		client = &http.Client{}
	}
	fatal := make(chan error, 1)
	writer := newProtocolWriter(output, writeAfter, fatal)
	runContext, runCancel := context.WithCancel(context.Background())
	return &Runtime{
		profiles: dependencies.Profiles, credentials: dependencies.Credentials, factory: dependencies.Discord, locks: dependencies.Locks,
		writer: writer, http: client, after: after,
		controls: map[string]pendingControl{}, interactions: map[string]pendingInteraction{}, callbacks: map[string]pendingCallback{}, callbackExpiry: map[string]context.CancelFunc{}, handles: map[string]string{}, attachments: map[string]attachmentSource{}, fetches: map[string]context.CancelFunc{}, outbound: map[string]*outboundTransfer{},
		fatal: fatal, runContext: runContext, runCancel: runCancel,
	}, nil
}

func (runtime *Runtime) Run(ctx context.Context, input io.Reader) error {
	runContext, cancel := context.WithCancel(ctx)
	runtime.mu.Lock()
	runtime.runCancel()
	runtime.runContext = runContext
	runtime.runCancel = cancel
	runtime.mu.Unlock()
	defer cancel()
	hello := channeladapter.Hello{ChannelKind: "discord", Protocol: channeladapter.ProtocolRange{Minimum: 1, Before: 2}, Features: append([]channeladapter.Feature(nil), discordFeatures...), Limits: discordLimits}
	helloID, err := runtime.writer.sendID(hello, "")
	if err != nil {
		return err
	}
	defer func() { _ = runtime.writer.drain(channeladapter.ForcedExitTimeout) }()
	decoder := channeladapter.NewDecoder(input)
	type readResult struct {
		frame channeladapter.Envelope
		err   error
	}
	initializationResult := make(chan readResult, 1)
	go func() {
		frame, readErr := decoder.Read(channeladapter.FromHost)
		initializationResult <- readResult{frame: frame, err: readErr}
	}()
	var initialization channeladapter.Envelope
	select {
	case <-runContext.Done():
		return nil
	case <-runtime.after(channeladapter.HandshakeTimeout):
		return errors.New("Discord adapter initialization timed out")
	case result := <-initializationResult:
		if result.err != nil {
			return result.err
		}
		initialization = result.frame
	}
	initialize, ok := initialization.Payload.(*channeladapter.Initialize)
	if !ok {
		return errors.New("Discord adapter expected initialize after hello")
	}
	if initialization.CorrelationID != helloID {
		return errors.New("Discord adapter initialize did not correlate to hello")
	}
	ready := channeladapter.Ready{ChannelKind: "discord", Features: append([]channeladapter.Feature(nil), initialize.Features...), Limits: initialize.Limits}
	if err := channeladapter.ValidateNegotiation(hello, *initialize, ready); err != nil {
		return err
	}
	if err := decoder.SetMaxFrameBytes(initialize.Limits.MaxFrameBytes); err != nil {
		return err
	}
	runtime.setNegotiated(*initialize)
	runtime.writer.setBounds(initialize.Limits.MaxFrameBytes, initialize.Limits.MaxOutstanding)
	if err := runtime.writer.send(ready, initialization.ID); err != nil {
		return err
	}
	if err := runtime.connect(runContext, initialize.ProfileID); err != nil {
		_ = runtime.writer.send(channeladapter.Diagnostic{Class: diagnosticClass(err), Severity: channeladapter.SeverityError, Code: "startup_failed", Message: safeRuntimeMessage(err)}, "")
		return err
	}
	defer runtime.close()
	readContext, stopReading := context.WithCancel(runContext)
	defer stopReading()
	frames := make(chan readResult, 1)
	go func() {
		for {
			frame, readErr := decoder.Read(channeladapter.FromHost)
			select {
			case frames <- readResult{frame: frame, err: readErr}:
			case <-readContext.Done():
				return
			}
			if readErr != nil {
				return
			}
		}
	}()
	for {
		var frame channeladapter.Envelope
		select {
		case <-runContext.Done():
			return nil
		case err := <-runtime.fatal:
			return err
		case result := <-frames:
			if result.err != nil {
				if runContext.Err() != nil || errors.Is(result.err, io.EOF) {
					return nil
				}
				return result.err
			}
			frame = result.frame
		}
		stop, err := runtime.handleHostFrame(runContext, frame)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
}

func (runtime *Runtime) connect(ctx context.Context, profileID string) error {
	profile, err := runtime.profiles.Get(profileID)
	if err != nil {
		return err
	}
	token, err := resolveCredential(runtime.credentials, profileID)
	if err != nil {
		return err
	}
	client, err := runtime.factory(token)
	if err != nil {
		return err
	}
	identity, err := validateIdentity(ctx, client)
	if err != nil {
		_ = client.Close()
		return err
	}
	if err := validateIdentityMatches(identity, profile); err != nil {
		_ = client.Close()
		return err
	}
	lock, err := runtime.locks(profile.ApplicationID)
	if err != nil {
		_ = client.Close()
		return err
	}
	locked, err := lock.TryLock()
	if err != nil || !locked {
		_ = client.Close()
		return errors.New("this Discord application is already active in another adapter process")
	}
	keep := false
	defer func() {
		if !keep {
			_ = client.Close()
			_ = lock.Unlock()
		}
	}()
	runtime.mu.Lock()
	runtime.profile, runtime.client, runtime.lock = profile, client, lock
	runtime.mu.Unlock()
	runtime.installHandlers(client)
	commands := []*discordgo.ApplicationCommand{{Name: "new", Description: "Start a fresh hctl conversation"}, {Name: "status", Description: "Show hctl runtime status"}}
	if _, err := client.ApplicationCommandBulkOverwrite(profile.ApplicationID, "", commands); err != nil {
		return errors.New("cannot reconcile Discord slash commands")
	}
	runtime.connectionTry = 1
	_ = runtime.writer.send(channeladapter.Connection{State: channeladapter.ConnectionConnecting, Attempt: runtime.connectionTry}, "")
	if err := client.Open(); err != nil {
		return errors.New("cannot connect to the Discord Gateway")
	}
	keep = true
	return nil
}

func (runtime *Runtime) installHandlers(client Discord) {
	client.AddHandler(func(_ *discordgo.Session, event *discordgo.MessageCreate) { runtime.handleMessage(event) })
	client.AddHandler(func(_ *discordgo.Session, event *discordgo.InteractionCreate) { runtime.handleInteraction(event) })
	client.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		if !runtime.beginVendorAdmission() {
			return
		}
		defer runtime.endVendorAdmission()
		runtime.mu.Lock()
		runtime.connectionTry++
		attempt := runtime.connectionTry
		runtime.mu.Unlock()
		_ = runtime.writer.sendContext(runtime.admissionContext(), channeladapter.Connection{State: channeladapter.ConnectionReconnecting, Attempt: attempt}, "")
	})
	ready := func() {
		if !runtime.beginVendorAdmission() {
			return
		}
		defer runtime.endVendorAdmission()
		runtime.mu.Lock()
		attempt := runtime.connectionTry
		runtime.mu.Unlock()
		ctx := runtime.admissionContext()
		_ = runtime.writer.sendContext(ctx, channeladapter.Connection{State: channeladapter.ConnectionReady, Attempt: attempt}, "")
		_ = runtime.writer.replayEventsContext(ctx)
	}
	client.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) { ready() })
	client.AddHandler(func(_ *discordgo.Session, _ *discordgo.Ready) { ready() })
}

func (runtime *Runtime) admissionContext() context.Context {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.runContext
}

func (runtime *Runtime) beginVendorAdmission() bool {
	runtime.mu.Lock()
	if runtime.shuttingDown {
		runtime.mu.Unlock()
		return false
	}
	runtime.activeAdmissions++
	runtime.mu.Unlock()
	return true
}

func (runtime *Runtime) endVendorAdmission() {
	runtime.mu.Lock()
	runtime.activeAdmissions--
	if runtime.activeAdmissions == 0 && runtime.admissionIdle != nil {
		close(runtime.admissionIdle)
		runtime.admissionIdle = nil
	}
	runtime.mu.Unlock()
}

func (runtime *Runtime) handleMessage(incoming *discordgo.MessageCreate) {
	if !runtime.beginVendorAdmission() {
		return
	}
	defer runtime.endVendorAdmission()
	if !eligibleMessage(runtime.profile, incoming) || runtime.handleFallback(incoming) {
		return
	}
	if len([]byte(incoming.Content)) > runtime.inboundTextLimit() {
		runtime.vendorDiagnostic("inbound_text_too_large", "Discord message exceeds the negotiated inbound text limit.")
		return
	}
	attachmentCount, attachmentBytes := runtime.attachmentLimits()
	if len(incoming.Attachments) > 0 && !runtime.supports(channeladapter.FeatureAttachments) {
		runtime.vendorDiagnostic("attachments_disabled", "Discord attachment input is not enabled for this runtime.")
		return
	}
	if len(incoming.Attachments) > attachmentCount {
		runtime.vendorDiagnostic("attachments_too_many", "Discord message exceeds the negotiated attachment count.")
		return
	}
	for _, attachment := range incoming.Attachments {
		if attachment == nil || attachment.Size < 0 || attachment.Size > attachmentBytes {
			runtime.vendorDiagnostic("attachment_too_large", "Discord attachment exceeds the negotiated size limit.")
			return
		}
	}
	attachments := make([]channeladapter.AttachmentDescriptor, 0, len(incoming.Attachments))
	type expiringHandle struct {
		handle     string
		generation uint64
		ctx        context.Context
	}
	expiring := make([]expiringHandle, 0, len(incoming.Attachments))
	runtime.mu.Lock()
	maximumHandles := runtime.limits.MaxOutstanding * attachmentCount
	if len(runtime.attachments)+len(incoming.Attachments) > maximumHandles {
		runtime.mu.Unlock()
		runtime.vendorDiagnostic("attachment_handles_full", "Discord attachment input is temporarily at capacity.")
		return
	}
	seenHandles := make(map[string]bool, len(incoming.Attachments))
	for _, attachment := range incoming.Attachments {
		handle := opaqueHandle("attachment", attachment.ID)
		if seenHandles[handle] {
			runtime.mu.Unlock()
			runtime.vendorDiagnostic("attachment_handle_conflict", "Discord attachment identifiers were duplicated.")
			return
		}
		if _, exists := runtime.attachments[handle]; exists {
			runtime.mu.Unlock()
			runtime.vendorDiagnostic("attachment_handle_conflict", "Discord attachment identifier is already pending.")
			return
		}
		seenHandles[handle] = true
	}
	for _, attachment := range incoming.Attachments {
		handle := opaqueHandle("attachment", attachment.ID)
		generation := runtime.attachmentSeq.Add(1)
		expiryContext, cancelExpiry := context.WithCancel(context.Background())
		runtime.attachments[handle] = attachmentSource{url: attachment.URL, size: int64(attachment.Size), generation: generation, expiryCancel: cancelExpiry}
		expiring = append(expiring, expiringHandle{handle: handle, generation: generation, ctx: expiryContext})
		attachments = append(attachments, channeladapter.AttachmentDescriptor{Handle: handle, Name: safeAttachmentName(attachment.Filename), MediaType: attachment.ContentType, Size: int64(attachment.Size)})
	}
	runtime.mu.Unlock()
	for _, source := range expiring {
		go runtime.expireAttachment(source.ctx, source.handle, source.generation)
	}
	message := channeladapter.InboundMessage{
		SourceID: incoming.ID, Route: channeladapter.Route{Handle: incoming.ChannelID}, Message: channeladapter.MessageRef{Handle: incoming.ID},
		Author: channeladapter.Author{Handle: incoming.Author.ID, Label: safeLabel(incoming.Author.Username)}, Text: incoming.Content, Attachments: attachments,
	}
	ctx := runtime.admissionContext()
	if _, err := runtime.writer.sendEventContext(ctx, "inbound:"+incoming.ID, message, ""); err != nil {
		_ = runtime.writer.sendContext(ctx, channeladapter.Diagnostic{Class: channeladapter.DiagnosticProtocol, Severity: channeladapter.SeverityError, Code: "event_replay_conflict", Message: "Discord replay changed a pending event."}, "")
		select {
		case runtime.fatal <- err:
		default:
		}
	}
}

func (runtime *Runtime) expireAttachment(ctx context.Context, handle string, generation uint64) {
	select {
	case <-runtime.after(channeladapter.AttachmentTimeout):
	case <-ctx.Done():
		return
	}
	runtime.mu.Lock()
	if source, ok := runtime.attachments[handle]; ok && source.generation == generation {
		delete(runtime.attachments, handle)
	}
	runtime.mu.Unlock()
}

func (runtime *Runtime) vendorDiagnostic(code, message string) {
	_ = runtime.writer.sendContext(runtime.admissionContext(), channeladapter.Diagnostic{Class: channeladapter.DiagnosticConfiguration, Severity: channeladapter.SeverityWarning, Code: code, Message: message}, "")
}

func eligibleMessage(profile Profile, incoming *discordgo.MessageCreate) bool {
	if incoming == nil || incoming.Message == nil || incoming.Author == nil || incoming.Author.Bot || incoming.WebhookID != "" || incoming.Author.ID != profile.AllowedUserID || strings.TrimSpace(incoming.Content) == "" && len(incoming.Attachments) == 0 {
		return false
	}
	return incoming.GuildID == "" || incoming.GuildID == profile.AllowedGuildID && incoming.ChannelID == profile.AllowedChannelID
}

func (runtime *Runtime) handleHostFrame(ctx context.Context, frame channeladapter.Envelope) (bool, error) {
	switch payload := frame.Payload.(type) {
	case *channeladapter.EventAck:
		if !runtime.writer.acknowledge(frame.CorrelationID) {
			return false, errors.New("Discord adapter received an unknown event acknowledgement correlation")
		}
		runtime.acknowledgeInteraction(frame.CorrelationID, payload.Disposition)
		return false, nil
	case *channeladapter.Activity:
		if payload.Kind == channeladapter.ActivityTyping {
			if !runtime.supports(channeladapter.FeatureTyping) {
				return false, errors.New("Discord adapter received typing without a negotiated feature")
			}
			_ = runtime.client.ChannelTyping(payload.Route.Handle)
		}
	case *channeladapter.Delivery:
		runtime.delivery(frame.ID, *payload)
	case *channeladapter.ControlResult:
		if err := runtime.controlResult(frame.CorrelationID, *payload); err != nil {
			return false, err
		}
	case *channeladapter.InteractionRequest:
		if !runtime.supports(channeladapter.FeatureInteractiveComponents) && !runtime.supports(channeladapter.FeatureTextFallback) {
			return false, errors.New("Discord adapter received an interaction without a negotiated renderer")
		}
		if err := runtime.renderInteraction(frame.ID, *payload); err != nil {
			return false, err
		}
	case *channeladapter.InteractionCancel:
		runtime.cancelInteraction(frame.ID, *payload)
	case *channeladapter.AttachmentFetch:
		if !runtime.supports(channeladapter.FeatureAttachments) {
			return false, errors.New("Discord adapter received attachment fetch without a negotiated feature")
		}
		runtime.startAttachmentFetch(ctx, frame.ID, *payload)
	case *channeladapter.AttachmentDeliver:
		if !runtime.supports(channeladapter.FeatureAttachments) {
			return false, errors.New("Discord adapter received attachment delivery without a negotiated feature")
		}
		runtime.receiveAttachment(frame.ID, *payload)
	case *channeladapter.Shutdown:
		shutdownContext, cancel := context.WithTimeout(context.Background(), channeladapter.ShutdownTimeout)
		defer cancel()
		if err := runtime.closeWithContext(shutdownContext); err != nil {
			return false, err
		}
		if err := runtime.writer.drainContext(shutdownContext); err != nil {
			return false, err
		}
		if err := runtime.writer.sendContext(shutdownContext, channeladapter.ShutdownComplete{}, frame.ID); err != nil {
			return false, err
		}
		if err := runtime.writer.drainContext(shutdownContext); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, errors.New("Discord adapter received an unsupported host command")
	}
	return false, nil
}

func (runtime *Runtime) startAttachmentFetch(ctx context.Context, correlation string, fetch channeladapter.AttachmentFetch) {
	runtime.mu.Lock()
	if runtime.shuttingDown {
		runtime.mu.Unlock()
		return
	}
	_, duplicateFetch := runtime.fetches[fetch.TransferID]
	_, duplicateOutbound := runtime.outbound[fetch.TransferID]
	duplicate := duplicateFetch || duplicateOutbound
	if duplicate || len(runtime.fetches)+len(runtime.outbound) >= channeladapter.MaxTransfers {
		runtime.mu.Unlock()
		_ = runtime.writer.send(channeladapter.AttachmentResult{TransferID: fetch.TransferID, Disposition: channeladapter.EffectFailed, Failure: channeladapter.Failure{Class: channeladapter.DiagnosticProtocol, Code: "attachment_capacity"}}, correlation)
		return
	}
	fetchContext, cancel := context.WithCancel(ctx)
	runtime.fetches[fetch.TransferID] = cancel
	runtime.mu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			runtime.mu.Lock()
			delete(runtime.fetches, fetch.TransferID)
			runtime.mu.Unlock()
		}()
		runtime.fetchAttachment(fetchContext, correlation, fetch)
	}()
}

func (runtime *Runtime) delivery(correlation string, delivery channeladapter.Delivery) {
	result := channeladapter.DeliveryResult{Disposition: channeladapter.EffectExact}
	fail := func(code string) {
		result.Disposition = channeladapter.EffectFailed
		result.Failure = channeladapter.Failure{Class: channeladapter.DiagnosticConfiguration, Code: code}
		_ = runtime.writer.send(result, correlation)
	}
	if len([]byte(delivery.Text)) > runtime.deliveryTextLimit() {
		fail("delivery_text_too_large")
		return
	}
	if delivery.ReplyTo != nil && !runtime.supports(channeladapter.FeatureReplies) {
		fail("replies_disabled")
		return
	}
	if len(delivery.AttachmentTransfers) > 0 && !runtime.supports(channeladapter.FeatureAttachments) {
		fail("attachments_disabled")
		return
	}
	attachmentCount, _ := runtime.attachmentLimits()
	if len(delivery.AttachmentTransfers) > attachmentCount {
		fail("attachments_too_many")
		return
	}
	if delivery.Action == channeladapter.DeliveryEdit && !runtime.supports(channeladapter.FeatureEdits) {
		fail("edits_disabled")
		return
	}
	if delivery.Action == channeladapter.DeliveryReaction && !runtime.supports(channeladapter.FeatureReactions) {
		fail("reactions_disabled")
		return
	}
	var err error
	attempted := false
	switch delivery.Action {
	case channeladapter.DeliverySend:
		files := make([]*discordgo.File, 0, len(delivery.AttachmentTransfers))
		for _, transferID := range delivery.AttachmentTransfers {
			runtime.mu.Lock()
			transfer := runtime.outbound[transferID]
			delete(runtime.outbound, transferID)
			runtime.mu.Unlock()
			if transfer != nil && transfer.expiryCancel != nil {
				transfer.expiryCancel()
			}
			if transfer == nil || !transfer.complete {
				err = errors.New("outbound attachment is unavailable")
				break
			}
			files = append(files, &discordgo.File{Name: transfer.name, ContentType: transfer.mediaType, Reader: bytes.NewReader(transfer.content.Bytes())})
		}
		if err == nil {
			chunks := discordChunks(delivery.Text)
			if len(chunks) == 0 && len(files) > 0 {
				chunks = []string{""}
			}
			for index, chunk := range chunks {
				message := &discordgo.MessageSend{Content: chunk, AllowedMentions: disabledMentions()}
				if index == 0 {
					message.Files = files
					if delivery.ReplyTo != nil {
						message.Reference = &discordgo.MessageReference{MessageID: delivery.ReplyTo.Handle, ChannelID: delivery.Route.Handle, FailIfNotExists: boolPointer(false)}
					}
				}
				attempted = true
				sent, sendErr := runtime.client.ChannelMessageSendComplex(delivery.Route.Handle, message)
				if sendErr != nil {
					err = sendErr
					break
				}
				if result.Message == nil && sent != nil && snowflake(sent.ID) {
					result.Message = &channeladapter.MessageRef{Handle: sent.ID}
				}
			}
		}
	case channeladapter.DeliveryEdit:
		if len([]rune(delivery.Text)) > 2000 {
			err = errors.New("Discord edit exceeds the message limit")
		} else {
			attempted = true
			edit := discordgo.NewMessageEdit(delivery.Route.Handle, delivery.Message.Handle).SetContent(delivery.Text)
			_, err = runtime.client.ChannelMessageEditComplex(edit)
		}
	case channeladapter.DeliveryReaction:
		attempted = true
		err = runtime.client.MessageReactionAdd(delivery.Route.Handle, delivery.Message.Handle, delivery.Reaction)
	}
	if err != nil {
		if attempted {
			result.Disposition = channeladapter.EffectAmbiguous
		} else {
			result.Disposition = channeladapter.EffectFailed
			result.Failure = channeladapter.Failure{Class: channeladapter.DiagnosticConfiguration, Code: "discord_delivery_invalid"}
		}
	}
	_ = runtime.writer.send(result, correlation)
}

func discordChunks(content string) []string {
	if content == "" {
		return nil
	}
	const maximumChunks = 6
	const maximumRunes = 2000
	runes := []rune(content)
	chunks := make([]string, 0, min(maximumChunks, (len(runes)+maximumRunes-1)/maximumRunes))
	for len(runes) > 0 && len(chunks) < maximumChunks {
		count := min(len(runes), maximumRunes)
		chunks = append(chunks, string(runes[:count]))
		runes = runes[count:]
	}
	if len(runes) > 0 {
		const marker = "\n\n[output truncated]"
		last := []rune(chunks[len(chunks)-1])
		last = last[:maximumRunes-len([]rune(marker))]
		chunks[len(chunks)-1] = string(last) + marker
	}
	return chunks
}

func (runtime *Runtime) fetchAttachment(ctx context.Context, correlation string, fetch channeladapter.AttachmentFetch) {
	runtime.mu.Lock()
	source, ok := runtime.attachments[fetch.AttachmentHandle]
	delete(runtime.attachments, fetch.AttachmentHandle)
	runtime.mu.Unlock()
	if source.expiryCancel != nil {
		source.expiryCancel()
	}
	_, negotiatedBytes := runtime.attachmentLimits()
	maximum := min(fetch.MaximumBytes, negotiatedBytes)
	if !ok || maximum < 1 || source.size > int64(maximum) {
		_ = runtime.sendConcurrentFrame(channeladapter.AttachmentResult{TransferID: fetch.TransferID, Disposition: channeladapter.EffectFailed, Failure: channeladapter.Failure{Class: channeladapter.DiagnosticConfiguration, Code: "attachment_unavailable"}}, correlation)
		return
	}
	fetchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	timedOut := make(chan struct{})
	go func() {
		select {
		case <-runtime.after(channeladapter.AttachmentTimeout):
			close(timedOut)
			cancel()
		case <-fetchContext.Done():
		}
	}()
	request, err := http.NewRequestWithContext(fetchContext, http.MethodGet, source.url, nil)
	if err != nil {
		_ = runtime.sendConcurrentFrame(channeladapter.AttachmentResult{TransferID: fetch.TransferID, Disposition: channeladapter.EffectFailed, Failure: channeladapter.Failure{Class: channeladapter.DiagnosticConfiguration, Code: "attachment_request_invalid"}}, correlation)
		return
	}
	response, err := runtime.http.Do(request)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil || response == nil || response.Body == nil || response.StatusCode != http.StatusOK {
		code := "attachment_fetch_failed"
		select {
		case <-timedOut:
			code = "attachment_fetch_timeout"
		default:
		}
		_ = runtime.sendConcurrentFrame(channeladapter.AttachmentResult{TransferID: fetch.TransferID, Disposition: channeladapter.EffectFailed, Failure: channeladapter.Failure{Class: channeladapter.DiagnosticConnection, Code: code}}, correlation)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(maximum)+1))
	select {
	case <-timedOut:
		_ = runtime.sendConcurrentFrame(channeladapter.AttachmentResult{TransferID: fetch.TransferID, Disposition: channeladapter.EffectFailed, Failure: channeladapter.Failure{Class: channeladapter.DiagnosticConnection, Code: "attachment_fetch_timeout"}}, correlation)
		return
	default:
	}
	if err != nil || len(data) > maximum {
		code := "attachment_fetch_failed"
		if len(data) > maximum {
			code = "attachment_fetch_oversized"
		}
		_ = runtime.sendConcurrentFrame(channeladapter.AttachmentResult{TransferID: fetch.TransferID, Disposition: channeladapter.EffectFailed, Failure: channeladapter.Failure{Class: channeladapter.DiagnosticConnection, Code: code}}, correlation)
		return
	}
	sequence := 0
	for len(data) > 0 {
		count := min(len(data), channeladapter.MaxAttachmentChunkBytes)
		chunk := channeladapter.AttachmentChunk{TransferID: fetch.TransferID, Sequence: sequence, Data: base64.StdEncoding.EncodeToString(data[:count]), Final: count == len(data)}
		if err := runtime.sendConcurrentFrame(chunk, correlation); err != nil {
			return
		}
		data = data[count:]
		sequence++
	}
	if sequence == 0 {
		_ = runtime.sendConcurrentFrame(channeladapter.AttachmentChunk{TransferID: fetch.TransferID, Sequence: 0, Data: "", Final: true}, correlation)
	}
}

func (runtime *Runtime) sendConcurrentFrame(payload channeladapter.Payload, correlation string) error {
	if !runtime.beginVendorAdmission() {
		return context.Canceled
	}
	defer runtime.endVendorAdmission()
	return runtime.writer.sendContext(runtime.admissionContext(), payload, correlation)
}

func (runtime *Runtime) receiveAttachment(correlation string, chunk channeladapter.AttachmentDeliver) {
	data, err := base64.StdEncoding.DecodeString(chunk.Data)
	result := channeladapter.AttachmentResult{TransferID: chunk.TransferID, Disposition: channeladapter.EffectExact}
	_, maximum := runtime.attachmentLimits()
	runtime.mu.Lock()
	if runtime.shuttingDown {
		runtime.mu.Unlock()
		return
	}
	transfer := runtime.outbound[chunk.TransferID]
	_, fetching := runtime.fetches[chunk.TransferID]
	created := false
	var expiryContext context.Context
	if transfer == nil && !fetching && len(runtime.outbound)+len(runtime.fetches) < channeladapter.MaxTransfers && chunk.Sequence == 0 {
		var cancelExpiry context.CancelFunc
		expiryContext, cancelExpiry = context.WithCancel(context.Background())
		transfer = &outboundTransfer{name: chunk.Name, mediaType: chunk.MediaType, generation: runtime.transferSeq.Add(1), expiryCancel: cancelExpiry}
		runtime.outbound[chunk.TransferID] = transfer
		created = true
	}
	if transfer == nil || transfer.complete || chunk.Sequence != transfer.next {
		err = errors.New("attachment sequence is invalid")
	}
	if err == nil && transfer.content.Len()+len(data) > maximum {
		err = errors.New("attachment exceeds the negotiated size limit")
	}
	if err == nil {
		_, err = transfer.content.Write(data)
		transfer.next++
		transfer.complete = chunk.Final
	}
	runtime.mu.Unlock()
	if err != nil {
		runtime.mu.Lock()
		delete(runtime.outbound, chunk.TransferID)
		runtime.mu.Unlock()
		if transfer != nil && transfer.expiryCancel != nil {
			transfer.expiryCancel()
		}
		result.Disposition = channeladapter.EffectFailed
		result.Failure = channeladapter.Failure{Class: channeladapter.DiagnosticProtocol, Code: "attachment_invalid"}
	} else if created {
		go runtime.expireOutboundTransfer(expiryContext, chunk.TransferID, transfer.generation)
	}
	_ = runtime.writer.send(result, correlation)
}

func (runtime *Runtime) expireOutboundTransfer(ctx context.Context, transferID string, generation uint64) {
	select {
	case <-runtime.after(channeladapter.AttachmentTimeout):
	case <-ctx.Done():
		return
	}
	runtime.mu.Lock()
	if transfer := runtime.outbound[transferID]; transfer != nil && transfer.generation == generation {
		delete(runtime.outbound, transferID)
	}
	runtime.mu.Unlock()
}

func (runtime *Runtime) close() {
	ctx, cancel := context.WithTimeout(context.Background(), channeladapter.ShutdownTimeout)
	defer cancel()
	_ = runtime.closeWithContext(ctx)
}

func (runtime *Runtime) closeWithContext(ctx context.Context) error {
	wait := runtime.prepareShutdown()
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		return nil
	}
	runtime.closed = true
	client, lock := runtime.client, runtime.lock
	runtime.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			if lock != nil {
				_ = lock.Unlock()
			}
			return errors.New("Discord adapter vendor admission did not stop before shutdown")
		}
	}
	if lock != nil {
		_ = lock.Unlock()
	}
	return runtime.writer.sendContext(ctx, channeladapter.Connection{State: channeladapter.ConnectionClosed, Attempt: max(1, runtime.connectionTry)}, "")
}

func (runtime *Runtime) prepareShutdown() <-chan struct{} {
	runtime.mu.Lock()
	if runtime.shuttingDown {
		wait := runtime.admissionIdle
		runtime.mu.Unlock()
		return wait
	}
	runtime.shuttingDown = true
	runtime.runCancel()
	for _, cancel := range runtime.fetches {
		cancel()
	}
	for _, source := range runtime.attachments {
		if source.expiryCancel != nil {
			source.expiryCancel()
		}
	}
	for _, transfer := range runtime.outbound {
		if transfer.expiryCancel != nil {
			transfer.expiryCancel()
		}
	}
	for _, interaction := range runtime.interactions {
		if interaction.expiryCancel != nil {
			interaction.expiryCancel()
		}
	}
	for _, cancel := range runtime.callbackExpiry {
		cancel()
	}
	runtime.fetches = map[string]context.CancelFunc{}
	runtime.outbound = map[string]*outboundTransfer{}
	runtime.attachments = map[string]attachmentSource{}
	runtime.interactions = map[string]pendingInteraction{}
	runtime.handles = map[string]string{}
	runtime.controls = map[string]pendingControl{}
	runtime.callbacks = map[string]pendingCallback{}
	runtime.callbackExpiry = map[string]context.CancelFunc{}
	if runtime.activeAdmissions > 0 {
		runtime.admissionIdle = make(chan struct{})
	}
	wait := runtime.admissionIdle
	runtime.mu.Unlock()
	return wait
}

func opaqueHandle(namespace, value string) string {
	digest := sha256.Sum256([]byte("hctl-discord-adapter-v1\x00" + namespace + "\x00" + value))
	return hex.EncodeToString(digest[:16])
}

func safeAttachmentName(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(name, "/", "_"), "\\", "_"))
	if name == "" || name == "." || name == ".." || len(name) > 255 {
		return "attachment"
	}
	return name
}

func safeLabel(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " "))
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func safeRuntimeMessage(err error) string {
	message := err.Error()
	if strings.Contains(strings.ToLower(message), "token") || strings.Contains(strings.ToLower(message), "credential") {
		return "Discord authentication failed. Run status or setup for this profile."
	}
	return message
}

func diagnosticClass(err error) channeladapter.DiagnosticClass {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "credential") || strings.Contains(message, "identity") || strings.Contains(message, "application") {
		return channeladapter.DiagnosticAuthentication
	}
	if strings.Contains(message, "gateway") || strings.Contains(message, "connect") {
		return channeladapter.DiagnosticConnection
	}
	return channeladapter.DiagnosticConfiguration
}
