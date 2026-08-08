// Package adapterhost supervises one exact external channel-adapter process
// and translates its bounded semantic protocol into the existing channel
// controller. It owns no vendor SDK, credential store, or arbitrary plugin
// lifecycle.
package adapterhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hctl/channeladapter"
	"hctl/internal/channel/controller"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/integration"
	"hctl/internal/interaction"
	"hctl/internal/project"
)

type Launch = integration.ChannelAdapterLaunchDescriptor

type Config struct {
	Project       *project.Project
	Driver        harness.Driver
	Launch        Launch
	ProfileID     string
	Environment   []string
	TurnTimeout   time.Duration
	IdleTimeout   time.Duration
	MaxResident   int
	MaxActive     int
	Executable    string
	Audit         io.Writer
	launchProcess func(Launch, []string, io.Writer) (processHandle, error)
	openTransport func(io.Reader, io.Writer) (frameDecoder, frameEncoder)
	after         func(time.Duration) <-chan time.Time
	newController func(context.Context, controller.Config, controller.Delivery) (channelController, error)
}

// These responsibility-specific seams keep tests independent without a
// service container, reflection registry, or vendor-shaped interface.
type processHandle interface {
	Input() io.WriteCloser
	Output() io.ReadCloser
	Done() <-chan error
	KillTree()
}
type frameDecoder interface {
	Read(channeladapter.Direction) (channeladapter.Envelope, error)
	SetMaxFrameBytes(int) error
}
type frameEncoder interface {
	Write(channeladapter.Envelope, channeladapter.Direction) error
}
type channelController interface {
	Submit(context.Context, controller.Inbound) (dispatch.SubmissionResult, error)
	Status(string) controller.Status
	Reset(string, string) error
	AcceptInteraction(string, string, interaction.AnswerAttempt) (interaction.AnswerDisposition, error)
	ContinueInteraction(string) error
	Done() <-chan struct{}
	Err() error
	Close()
}

type Runtime struct {
	config     Config
	process    processHandle
	decoder    frameDecoder
	encoder    frameEncoder
	controller channelController
	features   []channeladapter.Feature

	writeMu      sync.Mutex
	next         atomic.Uint64
	mu           sync.Mutex
	pending      map[string]chan channeladapter.Envelope
	interactions map[string]interactionTarget
	targets      map[string]target
	targetOrder  []string
	events       map[string]eventReceipt
	eventOrder   []string
	err          error
	done         chan struct{}
	closeOnce    sync.Once
}

type target struct {
	route   channeladapter.Route
	message channeladapter.MessageRef
}
type interactionTarget struct{ surface, conversation string }
type eventReceipt struct{ digest string }
type synchronizedWriter struct {
	mu     sync.Mutex
	target io.Writer
}

func (writer *synchronizedWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.target.Write(data)
}

func New(config Config) (*Runtime, error) {
	if config.Project == nil || config.Driver == nil {
		return nil, errors.New("channel-adapter host requires a configured channel project and harness driver")
	}
	if config.Launch.Command == "" || config.Launch.ChannelKind == "" || config.ProfileID == "" {
		return nil, errors.New("channel-adapter host launch is incomplete")
	}
	if err := integration.ValidateChannelProfileID(config.ProfileID); err != nil {
		return nil, err
	}
	if config.Launch.ProtocolMinimum > channeladapter.ProtocolVersion || config.Launch.ProtocolBefore <= channeladapter.ProtocolVersion {
		return nil, errors.New("channel-adapter package does not support protocol version 1")
	}
	if config.TurnTimeout <= 0 || config.TurnTimeout > 30*time.Minute || config.IdleTimeout <= 0 || config.IdleTimeout > 24*time.Hour || config.MaxResident <= 0 || config.MaxResident > 64 || config.MaxActive <= 0 || config.MaxActive > config.MaxResident {
		return nil, errors.New("channel-adapter execution policy is invalid")
	}
	workingDirectory, err := filepath.Abs(config.Launch.WorkingDirectory)
	if err != nil || filepath.Clean(workingDirectory) != workingDirectory {
		return nil, errors.New("channel-adapter working directory is invalid")
	}
	executable, err := filepath.Abs(config.Launch.Command)
	info, statErr := os.Lstat(executable)
	relative, relativeErr := filepath.Rel(workingDirectory, executable)
	if err != nil || statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("channel-adapter executable is outside its verified package root or unsafe")
	}
	if config.Audit == nil {
		config.Audit = io.Discard
	}
	config.Audit = &synchronizedWriter{target: config.Audit}
	if config.launchProcess == nil {
		config.launchProcess = func(launch Launch, environment []string, stderr io.Writer) (processHandle, error) {
			return startChild(launch, environment, stderr)
		}
	}
	if config.openTransport == nil {
		config.openTransport = func(reader io.Reader, writer io.Writer) (frameDecoder, frameEncoder) {
			return channeladapter.NewDecoder(reader), channeladapter.NewEncoder(writer)
		}
	}
	if config.after == nil {
		config.after = time.After
	}
	if config.newController == nil {
		config.newController = func(ctx context.Context, config controller.Config, delivery controller.Delivery) (channelController, error) {
			return controller.New(ctx, config, delivery)
		}
	}
	return &Runtime{config: config, pending: map[string]chan channeladapter.Envelope{}, interactions: map[string]interactionTarget{}, targets: map[string]target{}, events: map[string]eventReceipt{}, done: make(chan struct{})}, nil
}

func (runtime *Runtime) Run(ctx context.Context) error {
	stderr := &boundedWriter{remaining: channeladapter.MaxStderrBytes, writer: io.Discard}
	process, err := runtime.config.launchProcess(runtime.config.Launch, runtime.config.Environment, stderr)
	if err != nil {
		return err
	}
	runtime.process = process
	runtime.decoder, runtime.encoder = runtime.config.openTransport(process.Output(), process.Input())
	if err := runtime.handshake(ctx); err != nil {
		runtime.forceClose()
		return err
	}
	controlled, err := runtime.config.newController(ctx, controller.Config{
		Project: runtime.config.Project, Driver: runtime.config.Driver,
		TurnTimeout: runtime.config.TurnTimeout, IdleTimeout: runtime.config.IdleTimeout,
		MaxResident: runtime.config.MaxResident, MaxActive: runtime.config.MaxActive,
		Executable: runtime.config.Executable, Audit: runtime.config.Audit,
		AuditPrefix: "Channel adapter", Interactions: runtime,
	}, runtime)
	if err != nil {
		runtime.forceClose()
		return err
	}
	runtime.controller = controlled
	go runtime.readLoop()
	_, _ = fmt.Fprintf(runtime.config.Audit, "Channel adapter connected kind=%s profile=%s package=%s\n", runtime.config.Launch.ChannelKind, runtime.config.ProfileID, runtime.config.Launch.PackageID)
	select {
	case <-ctx.Done():
		runtime.Close()
		return nil
	case <-controlled.Done():
		err := controlled.Err()
		runtime.Close()
		return err
	case <-runtime.done:
		runtime.mu.Lock()
		err := runtime.err
		runtime.mu.Unlock()
		runtime.Close()
		return err
	}
}

func (runtime *Runtime) handshake(ctx context.Context) error {
	helloEnvelope, err := runtime.readWithTimeout(ctx, channeladapter.HandshakeTimeout)
	if err != nil {
		return errors.New("channel-adapter handshake failed before hello")
	}
	hello, ok := helloEnvelope.Payload.(*channeladapter.Hello)
	if !ok || hello.ChannelKind != runtime.config.Launch.ChannelKind || hello.Protocol.Minimum > channeladapter.ProtocolVersion || hello.Protocol.Before <= channeladapter.ProtocolVersion || runtime.config.Launch.ProtocolMinimum > channeladapter.ProtocolVersion || runtime.config.Launch.ProtocolBefore <= channeladapter.ProtocolVersion {
		return errors.New("channel-adapter handshake is incompatible; install a package supporting protocol version 1")
	}
	declared := map[channeladapter.Feature]bool{}
	for _, feature := range runtime.config.Launch.Features {
		declared[channeladapter.Feature(feature)] = true
	}
	for _, feature := range hello.Features {
		if !declared[feature] {
			return errors.New("channel-adapter handshake widened installed features")
		}
	}
	features := append([]channeladapter.Feature(nil), hello.Features...)
	limits := narrowLimits(hello.Limits)
	initialize := channeladapter.Initialize{
		SelectedVersion: channeladapter.ProtocolVersion, ProfileID: runtime.config.ProfileID,
		Features: features, Limits: limits,
		Policy: channeladapter.RuntimePolicy{Participation: channeladapter.ParticipationAmbient, MaxInboundTextBytes: limits.MaxTextBytes, MaxDeliveryTextBytes: limits.MaxTextBytes, MaxAttachmentBytes: limits.MaxAttachmentBytes},
	}
	initializeID, err := runtime.write(ctx, initialize, helloEnvelope.ID, channeladapter.HandshakeTimeout)
	if err != nil {
		return errors.New("channel-adapter initialization failed")
	}
	readyEnvelope, err := runtime.readWithTimeout(ctx, channeladapter.HandshakeTimeout)
	if err != nil {
		return errors.New("channel-adapter handshake failed before ready")
	}
	ready, ok := readyEnvelope.Payload.(*channeladapter.Ready)
	if !ok || readyEnvelope.CorrelationID != initializeID || ready.ChannelKind != hello.ChannelKind || !subsetFeatures(ready.Features, features) || !narrowerLimits(ready.Limits, limits) {
		return errors.New("channel-adapter ready response is incompatible with initialization")
	}
	if err := runtime.decoder.SetMaxFrameBytes(ready.Limits.MaxFrameBytes); err != nil {
		return err
	}
	runtime.features = append([]channeladapter.Feature(nil), ready.Features...)
	return nil
}

func narrowLimits(value channeladapter.Limits) channeladapter.Limits {
	return channeladapter.Limits{
		MaxFrameBytes: min(value.MaxFrameBytes, channeladapter.MaxFrameBytes), MaxTextBytes: min(value.MaxTextBytes, channeladapter.MaxTextBytes),
		MaxAttachments: min(value.MaxAttachments, channeladapter.MaxAttachments), MaxAttachmentBytes: min(value.MaxAttachmentBytes, channeladapter.MaxAttachmentBytes),
		MaxOutstanding: min(value.MaxOutstanding, channeladapter.MaxOutstanding),
	}
}
func narrowerLimits(value, maximum channeladapter.Limits) bool {
	return value.MaxFrameBytes > 0 && value.MaxFrameBytes <= maximum.MaxFrameBytes && value.MaxTextBytes > 0 && value.MaxTextBytes <= maximum.MaxTextBytes && value.MaxAttachments >= 0 && value.MaxAttachments <= maximum.MaxAttachments && value.MaxAttachmentBytes > 0 && value.MaxAttachmentBytes <= maximum.MaxAttachmentBytes && value.MaxOutstanding > 0 && value.MaxOutstanding <= maximum.MaxOutstanding
}
func subsetFeatures(values, maximum []channeladapter.Feature) bool {
	for _, value := range values {
		if !slices.Contains(maximum, value) {
			return false
		}
	}
	return true
}

func (runtime *Runtime) readWithTimeout(ctx context.Context, timeout time.Duration) (channeladapter.Envelope, error) {
	type result struct {
		frame channeladapter.Envelope
		err   error
	}
	resultCh := make(chan result, 1)
	go func() { frame, err := runtime.decoder.Read(channeladapter.FromAdapter); resultCh <- result{frame, err} }()
	select {
	case result := <-resultCh:
		return result.frame, result.err
	case <-ctx.Done():
		return channeladapter.Envelope{}, ctx.Err()
	case <-runtime.config.after(timeout):
		return channeladapter.Envelope{}, context.DeadlineExceeded
	}
}

func (runtime *Runtime) readLoop() {
	for {
		frame, err := runtime.decoder.Read(channeladapter.FromAdapter)
		if err != nil {
			runtime.fail(errors.New("channel-adapter protocol failed"))
			return
		}
		if runtime.routeResponse(frame) {
			continue
		}
		if err := runtime.handle(frame); err != nil {
			runtime.fail(err)
			return
		}
	}
}

func (runtime *Runtime) routeResponse(frame channeladapter.Envelope) bool {
	switch frame.Payload.(type) {
	case *channeladapter.DeliveryResult, *channeladapter.AttachmentChunk, *channeladapter.AttachmentResult, *channeladapter.ShutdownComplete:
	default:
		return false
	}
	runtime.mu.Lock()
	response := runtime.pending[frame.CorrelationID]
	if response != nil {
		delete(runtime.pending, frame.CorrelationID)
	}
	runtime.mu.Unlock()
	if response == nil {
		runtime.fail(errors.New("channel-adapter response has unknown correlation"))
		return true
	}
	response <- frame
	return true
}

func (runtime *Runtime) handle(frame channeladapter.Envelope) error {
	switch payload := frame.Payload.(type) {
	case *channeladapter.InboundMessage:
		return runtime.inbound(frame, *payload)
	case *channeladapter.ControlRequest:
		return runtime.control(frame, *payload)
	case *channeladapter.InteractionResult:
		return runtime.interactionResult(frame, *payload)
	case *channeladapter.Connection:
		_, _ = fmt.Fprintf(runtime.config.Audit, "Channel adapter connection state=%s attempt=%d\n", payload.State, payload.Attempt)
		return nil
	case *channeladapter.Diagnostic:
		_, _ = fmt.Fprintf(runtime.config.Audit, "Channel adapter diagnostic class=%s severity=%s code=%s\n", payload.Class, payload.Severity, payload.Code)
		return nil
	default:
		return errors.New("channel-adapter emitted an unexpected protocol frame")
	}
}

func (runtime *Runtime) inbound(frame channeladapter.Envelope, incoming channeladapter.InboundMessage) error {
	duplicate, err := runtime.event(frame)
	if err != nil {
		return err
	}
	if duplicate {
		_, err = runtime.write(context.Background(), channeladapter.EventAck{Disposition: "duplicate"}, frame.ID, channeladapter.CommandTimeout)
		return err
	}
	surface, conversation := incoming.Route.Handle, runtime.conversation(incoming.Route.Handle)
	target := target{route: incoming.Route, message: incoming.Message}
	runtime.remember(incoming.SourceID, target)
	attachments := make([]map[string]any, 0, len(incoming.Attachments))
	for _, attachment := range incoming.Attachments {
		attachments = append(attachments, map[string]any{"name": attachment.Name, "media_type": attachment.MediaType, "size": attachment.Size})
	}
	semantic, _ := json.Marshal(map[string]any{"channel": runtime.config.Launch.ChannelKind, "author": incoming.Author.Label, "text": incoming.Text, "attachments": attachments})
	result, submitErr := runtime.controller.Submit(context.Background(), controller.Inbound{SurfaceID: surface, ConversationID: conversation, InputID: incoming.SourceID, Text: "Channel message (JSON):\n" + string(semantic), Target: target})
	if submitErr != nil {
		_, _ = fmt.Fprintln(runtime.config.Audit, "Channel adapter admission status=rejected")
	} else {
		_, _ = fmt.Fprintf(runtime.config.Audit, "Channel adapter admission status=%s duplicate=%t\n", result.Status, result.Duplicate)
	}
	disposition := "accepted"
	if result.Duplicate {
		disposition = "duplicate"
	}
	if submitErr != nil {
		disposition = "rejected"
		runtime.forget(incoming.SourceID)
	}
	runtime.rememberEvent(frame)
	_, writeErr := runtime.write(context.Background(), channeladapter.EventAck{Disposition: disposition}, frame.ID, channeladapter.CommandTimeout)
	return writeErr
}

func (runtime *Runtime) control(frame channeladapter.Envelope, request channeladapter.ControlRequest) error {
	duplicate, err := runtime.event(frame)
	if err != nil {
		return err
	}
	if duplicate {
		_, err = runtime.write(context.Background(), channeladapter.EventAck{Disposition: "duplicate"}, frame.ID, channeladapter.CommandTimeout)
		return err
	}
	conversation := runtime.conversation(request.Route.Handle)
	result := channeladapter.ControlResult{Action: request.Action, Disposition: channeladapter.ControlExact}
	if request.Action == channeladapter.ControlStatus {
		status := runtime.controller.Status(conversation)
		result.Status = &channeladapter.RuntimeStatus{Agent: runtime.config.Project.Name, Harness: runtime.config.Driver.Name(), State: channeladapter.LifecycleState(status.Conversation.State), Pending: status.Conversation.Pending, Active: status.Capacity.Active, ActiveLimit: status.Capacity.ActiveLimit, Resident: status.Capacity.Resident, ResidentLimit: status.Capacity.ResidentLimit, Queued: status.Capacity.Queued}
	} else if err := runtime.controller.Reset(request.Route.Handle, conversation); err != nil {
		if errors.Is(err, dispatch.ErrConversationBusy) {
			result.Disposition = channeladapter.ControlBusy
		} else {
			result.Disposition = channeladapter.ControlFailed
			result.Failure = channeladapter.Failure{Class: channeladapter.DiagnosticInternal, Code: "reset_failed"}
		}
	}
	runtime.rememberEvent(frame)
	if _, err := runtime.write(context.Background(), result, frame.ID, channeladapter.CommandTimeout); err != nil {
		return err
	}
	_, err = runtime.write(context.Background(), channeladapter.EventAck{Disposition: "accepted"}, frame.ID, channeladapter.CommandTimeout)
	return err
}

func (runtime *Runtime) interactionResult(frame channeladapter.Envelope, result channeladapter.InteractionResult) error {
	duplicate, err := runtime.event(frame)
	if err != nil {
		return err
	}
	if duplicate {
		_, err := runtime.write(context.Background(), channeladapter.EventAck{Disposition: "duplicate"}, frame.ID, channeladapter.CommandTimeout)
		return err
	}
	runtime.mu.Lock()
	target, ok := runtime.interactions[frame.CorrelationID]
	runtime.mu.Unlock()
	if !ok {
		return errors.New("channel-adapter interaction result has unknown correlation")
	}
	answer := fromProtocolAnswer(result.Answer)
	disposition, acceptErr := runtime.controller.AcceptInteraction(target.surface, target.conversation, interaction.AnswerAttempt{InteractionID: result.InteractionID, Answer: answer})
	ack := "accepted"
	if disposition == interaction.AnswerDuplicate {
		ack = "duplicate"
	}
	if acceptErr != nil {
		ack = "rejected"
	} else {
		runtime.rememberEvent(frame)
		runtime.mu.Lock()
		delete(runtime.interactions, frame.CorrelationID)
		runtime.mu.Unlock()
	}
	if _, err := runtime.write(context.Background(), channeladapter.EventAck{Disposition: ack}, frame.ID, channeladapter.CommandTimeout); err != nil {
		return err
	}
	if acceptErr == nil && (disposition == interaction.AnswerAccepted || disposition == interaction.AnswerDuplicate) {
		return runtime.controller.ContinueInteraction(target.conversation)
	}
	return nil
}

func (runtime *Runtime) event(frame channeladapter.Envelope) (bool, error) {
	data, err := channeladapter.MarshalFrame(frame, channeladapter.FromAdapter)
	if err != nil {
		return false, err
	}
	digestBytes := sha256.Sum256(data)
	digest := hex.EncodeToString(digestBytes[:])
	runtime.mu.Lock()
	receipt, found := runtime.events[frame.ID]
	runtime.mu.Unlock()
	if found && receipt.digest != digest {
		return false, errors.New("channel-adapter replay changed content under a stable frame id")
	}
	return found, nil
}
func (runtime *Runtime) rememberEvent(frame channeladapter.Envelope) {
	data, _ := channeladapter.MarshalFrame(frame, channeladapter.FromAdapter)
	digestBytes := sha256.Sum256(data)
	runtime.mu.Lock()
	if _, found := runtime.events[frame.ID]; !found {
		runtime.events[frame.ID] = eventReceipt{digest: hex.EncodeToString(digestBytes[:])}
		runtime.eventOrder = append(runtime.eventOrder, frame.ID)
	}
	for len(runtime.eventOrder) > channeladapter.MaxOutstanding {
		oldest := runtime.eventOrder[0]
		runtime.eventOrder = runtime.eventOrder[1:]
		delete(runtime.events, oldest)
	}
	runtime.mu.Unlock()
}

func (runtime *Runtime) write(ctx context.Context, payload channeladapter.Payload, correlation string, timeout time.Duration) (string, error) {
	id := fmt.Sprintf("host.%08x", runtime.next.Add(1))
	envelope := channeladapter.Envelope{ProtocolVersion: channeladapter.ProtocolVersion, ID: id, CorrelationID: correlation, Payload: payload}
	runtime.writeMu.Lock()
	defer runtime.writeMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- runtime.encoder.Write(envelope, channeladapter.FromHost) }()
	select {
	case err := <-done:
		return id, err
	case <-ctx.Done():
		return "", ctx.Err()
	case <-runtime.config.after(timeout):
		if runtime.process != nil {
			runtime.process.KillTree()
		}
		return "", errors.New("channel-adapter protocol write timed out")
	}
}

func (runtime *Runtime) command(ctx context.Context, payload channeladapter.Payload, timeout time.Duration) (channeladapter.Envelope, error) {
	id := fmt.Sprintf("host.%08x", runtime.next.Add(1))
	response := make(chan channeladapter.Envelope, 1)
	runtime.mu.Lock()
	runtime.pending[id] = response
	runtime.mu.Unlock()
	defer func() { runtime.mu.Lock(); delete(runtime.pending, id); runtime.mu.Unlock() }()
	envelope := channeladapter.Envelope{ProtocolVersion: channeladapter.ProtocolVersion, ID: id, Payload: payload}
	runtime.writeMu.Lock()
	done := make(chan error, 1)
	go func() { done <- runtime.encoder.Write(envelope, channeladapter.FromHost) }()
	select {
	case err := <-done:
		runtime.writeMu.Unlock()
		if err != nil {
			return channeladapter.Envelope{}, err
		}
	case <-ctx.Done():
		runtime.writeMu.Unlock()
		return channeladapter.Envelope{}, ctx.Err()
	case <-runtime.config.after(timeout):
		runtime.writeMu.Unlock()
		if runtime.process != nil {
			runtime.process.KillTree()
		}
		return channeladapter.Envelope{}, errors.New("channel-adapter protocol write timed out")
	}
	select {
	case frame := <-response:
		return frame, nil
	case <-ctx.Done():
		return channeladapter.Envelope{}, ctx.Err()
	case <-runtime.config.after(timeout):
		if runtime.process != nil {
			runtime.process.KillTree()
		}
		return channeladapter.Envelope{}, errors.New("channel-adapter command timed out")
	}
}

func (runtime *Runtime) Typing(value any) error {
	target, ok := value.(target)
	if !ok || !slices.Contains(runtime.features, channeladapter.FeatureTyping) {
		return nil
	}
	_, err := runtime.write(context.Background(), channeladapter.Activity{Route: target.route, Kind: channeladapter.ActivityTyping}, "", channeladapter.CommandTimeout)
	return err
}

func (runtime *Runtime) Deliver(outcome controller.Outcome) error {
	target, ok := outcome.Target.(target)
	if !ok {
		return errors.New("channel reply target is invalid")
	}
	runtime.forget(outcome.InputID)
	parts := outcome.Parts
	if outcome.Failure != controller.FailureNone {
		parts = []string{failureMessage(outcome.Failure)}
	}
	if outcome.Truncated && len(parts) > 0 {
		parts[len(parts)-1] += "\n\n[output truncated]"
	}
	for index, text := range parts {
		delivery := channeladapter.Delivery{Action: channeladapter.DeliverySend, Route: target.route, Text: text}
		if index == 0 {
			delivery.ReplyTo = &target.message
		}
		frame, err := runtime.command(context.Background(), delivery, channeladapter.DeliveryTimeout)
		if err != nil {
			return err
		}
		result, ok := frame.Payload.(*channeladapter.DeliveryResult)
		if !ok {
			return errors.New("channel-adapter returned an invalid delivery result")
		}
		if result.Disposition == channeladapter.EffectAmbiguous {
			return errors.New("channel-adapter delivery is uncertain")
		}
		if result.Disposition == channeladapter.EffectFailed {
			return errors.New("channel-adapter delivery failed")
		}
	}
	return nil
}

func failureMessage(failure controller.Failure) string {
	switch failure {
	case controller.FailureAdmission:
		return "I couldn't handle that request in this conversation. Please try again."
	case controller.FailureProcess:
		return "I hit an error while handling that. Please try again."
	case controller.FailureCancelled:
		return "That request was cancelled."
	case controller.FailureUncertain:
		return "I lost track of that response during recovery. Please try again."
	case controller.FailureWriteAccess:
		return "I couldn't continue that request with write access."
	case controller.FailureWorkspace:
		return "I couldn't safely create a writable workspace for that request."
	default:
		return "I couldn't produce a response. Please try again."
	}
}

func (runtime *Runtime) Capabilities() interaction.Capabilities {
	if !slices.Contains(runtime.features, channeladapter.FeatureInteractiveComponents) && !slices.Contains(runtime.features, channeladapter.FeatureTextFallback) {
		return interaction.Capabilities{}
	}
	return interaction.Capabilities{Kinds: []interaction.Kind{interaction.KindConfirm, interaction.KindChooseOne, interaction.KindChooseMany, interaction.KindText, interaction.KindForm}, FormFieldKinds: []interaction.Kind{interaction.KindText}, MaxRequestBytes: interaction.MaxRequestBytes, MaxPromptBytes: 800, MaxFields: 5, MaxOptionsPerField: 25, MaxSelections: 25, MaxTotalOptions: 25, MaxLabelBytes: 45, MaxDescriptionBytes: 100, MaxValueBytes: interaction.MaxValueBytes, MaxTextRunes: interaction.MaxTextRunes, SupportsFreeform: false}
}
func (runtime *Runtime) Owner(surfaceID string) interaction.Owner {
	return interaction.Owner{SurfaceKey: interaction.Digest("channel-surface\x00" + runtime.config.Launch.ChannelKind + "\x00" + runtime.config.ProfileID + "\x00" + surfaceID), PrincipalKey: interaction.Digest("channel-principal\x00" + runtime.config.Launch.ChannelKind + "\x00" + runtime.config.ProfileID)}
}
func (runtime *Runtime) RecoverTarget(surfaceID, inputID string) (any, bool) {
	if surfaceID == "" || inputID == "" {
		return nil, false
	}
	runtime.mu.Lock()
	recovered, ok := runtime.targets[inputID]
	runtime.mu.Unlock()
	if !ok || recovered.route.Handle != surfaceID {
		return nil, false
	}
	return recovered, true
}
func (runtime *Runtime) Render(ctx context.Context, intent interaction.RenderIntent) interaction.EffectOutcome {
	runtime.mu.Lock()
	delivery, ok := runtime.targets[intent.InputID]
	runtime.mu.Unlock()
	if !ok {
		return interaction.EffectFailed
	}
	request := toProtocolRequest(intent.Request)
	id := fmt.Sprintf("host.%08x", runtime.next.Add(1))
	runtime.mu.Lock()
	runtime.interactions[id] = interactionTarget{surface: delivery.route.Handle, conversation: runtime.conversation(delivery.route.Handle)}
	runtime.mu.Unlock()
	envelope := channeladapter.Envelope{ProtocolVersion: channeladapter.ProtocolVersion, ID: id, Payload: channeladapter.InteractionRequest{InteractionID: intent.InteractionID, Route: delivery.route, ReplyTo: delivery.message, Request: request}}
	runtime.writeMu.Lock()
	done := make(chan error, 1)
	go func() { done <- runtime.encoder.Write(envelope, channeladapter.FromHost) }()
	select {
	case err := <-done:
		runtime.writeMu.Unlock()
		if err != nil {
			return interaction.EffectUncertain
		}
		return interaction.EffectSucceeded
	case <-ctx.Done():
		runtime.writeMu.Unlock()
		return interaction.EffectUncertain
	case <-runtime.config.after(channeladapter.DeliveryTimeout):
		runtime.writeMu.Unlock()
		if runtime.process != nil {
			runtime.process.KillTree()
		}
		return interaction.EffectUncertain
	}
}

func (runtime *Runtime) conversation(route string) string {
	sum := sha256.Sum256([]byte(runtime.config.Launch.ChannelKind + "\x00" + runtime.config.ProfileID + "\x00" + route))
	return "channel-" + hex.EncodeToString(sum[:12])
}
func (runtime *Runtime) remember(input string, value target) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if _, ok := runtime.targets[input]; !ok {
		runtime.targetOrder = append(runtime.targetOrder, input)
	}
	runtime.targets[input] = value
	for len(runtime.targetOrder) > channeladapter.MaxOutstanding {
		oldest := runtime.targetOrder[0]
		runtime.targetOrder = runtime.targetOrder[1:]
		delete(runtime.targets, oldest)
	}
}
func (runtime *Runtime) forget(input string) {
	runtime.mu.Lock()
	delete(runtime.targets, input)
	runtime.mu.Unlock()
}

func (runtime *Runtime) fail(err error) {
	runtime.mu.Lock()
	if runtime.err == nil {
		runtime.err = err
		close(runtime.done)
	}
	runtime.mu.Unlock()
}
func (runtime *Runtime) Close() {
	runtime.closeOnce.Do(func() {
		if runtime.controller != nil {
			runtime.controller.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), channeladapter.ShutdownTimeout)
		defer cancel()
		if runtime.process != nil {
			_, _ = runtime.command(ctx, channeladapter.Shutdown{Reason: "host_shutdown"}, channeladapter.ShutdownTimeout)
			runtime.forceClose()
		}
	})
}
func (runtime *Runtime) forceClose() {
	if runtime.process == nil {
		return
	}
	_ = runtime.process.Input().Close()
	select {
	case <-runtime.process.Done():
		runtime.process.KillTree()
		return
	case <-runtime.config.after(channeladapter.ForcedExitTimeout):
		runtime.process.KillTree()
		<-runtime.process.Done()
	}
}

func toProtocolRequest(value interaction.Request) channeladapter.SemanticInteractionRequest {
	result := channeladapter.SemanticInteractionRequest{SchemaVersion: value.SchemaVersion, Kind: channeladapter.InteractionKind(value.Kind), Prompt: value.Prompt, FallbackText: value.FallbackText, Policy: channeladapter.InteractionPolicy{ExpiresAfterSeconds: value.Policy.ExpiresAfterSeconds, Cancellation: channeladapter.Cancellation(value.Policy.Cancellation)}}
	if value.Field != nil {
		field := toProtocolField(*value.Field)
		result.Field = &field
	}
	for _, field := range value.Fields {
		result.Fields = append(result.Fields, toProtocolField(field))
	}
	return result
}
func toProtocolField(value interaction.Field) channeladapter.Field {
	result := channeladapter.Field{ID: value.ID, Kind: channeladapter.InteractionKind(value.Kind), Label: value.Label, Description: value.Description, Required: value.Required, AllowFreeform: value.AllowFreeform, MinSelections: value.MinSelections, MaxSelections: value.MaxSelections, MinLength: value.MinLength, MaxLength: value.MaxLength, DateTimeRepresentation: channeladapter.DateTimeRepresentation(value.DateTimeRepresentation)}
	for _, option := range value.Options {
		result.Options = append(result.Options, channeladapter.Option{ID: option.ID, Label: option.Label, Description: option.Description, Value: option.Value})
	}
	return result
}
func fromProtocolAnswer(value channeladapter.SemanticInteractionAnswer) interaction.Answer {
	result := interaction.Answer{SchemaVersion: value.SchemaVersion, Action: interaction.AnswerAction(value.Action)}
	for _, field := range value.Fields {
		result.Fields = append(result.Fields, interaction.FieldAnswer{FieldID: field.FieldID, Confirmed: field.Confirmed, OptionIDs: append([]string(nil), field.OptionIDs...), Freeform: field.Freeform, Text: field.Text, DateTime: field.DateTime})
	}
	return result
}
