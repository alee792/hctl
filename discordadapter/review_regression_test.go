package discordadapter

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/channeladapter"

	"github.com/bwmarrin/discordgo"
)

func TestNegotiatedFeatureAndSemanticLimitsAreEnforced(t *testing.T) {
	discord := &fakeDiscord{}
	var output bytes.Buffer
	runtime, err := NewRuntime(&output, fixtureDependencies(discord))
	if err != nil {
		t.Fatal(err)
	}
	initialize := channeladapter.Initialize{
		SelectedVersion: 1, ProfileID: "default", Features: []channeladapter.Feature{channeladapter.FeatureReplies},
		Limits: channeladapter.Limits{MaxFrameBytes: 4096, MaxTextBytes: 8, MaxAttachments: 0, MaxAttachmentBytes: 0, MaxOutstanding: 8},
		Policy: channeladapter.RuntimePolicy{Participation: channeladapter.ParticipationAmbient, MaxInboundTextBytes: 4, MaxDeliveryTextBytes: 5, MaxAttachmentBytes: 0},
	}
	runtime.setNegotiated(initialize)
	runtime.writer.setBounds(initialize.Limits.MaxFrameBytes, initialize.Limits.MaxOutstanding)
	runtime.profile, runtime.client = fixtureProfile(), discord
	runtime.handleMessage(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "601", ChannelID: "555", GuildID: "444", Content: "12345", Author: &discordgo.User{ID: "333"}}})
	runtime.handleMessage(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "602", ChannelID: "555", GuildID: "444", Content: "ok", Author: &discordgo.User{ID: "333"}, Attachments: []*discordgo.MessageAttachment{{ID: "1", Filename: "a", Size: 1}}}})
	runtime.delivery("host.delivery.limit", channeladapter.Delivery{Action: channeladapter.DeliverySend, Route: channeladapter.Route{Handle: "555"}, Text: "123456"})
	if err := runtime.writer.drain(time.Second); err != nil {
		t.Fatal(err)
	}
	frames := decodeAdapterOutput(t, output.Bytes())
	if len(frames) != 3 {
		t.Fatalf("frames = %#v", frames)
	}
	if first := frames[0].Payload.(*channeladapter.Diagnostic); first.Code != "inbound_text_too_large" {
		t.Fatalf("text diagnostic = %#v", first)
	}
	if second := frames[1].Payload.(*channeladapter.Diagnostic); second.Code != "attachments_disabled" {
		t.Fatalf("attachment diagnostic = %#v", second)
	}
	if result := frames[2].Payload.(*channeladapter.DeliveryResult); result.Disposition != channeladapter.EffectFailed || result.Failure.Code != "delivery_text_too_large" {
		t.Fatalf("delivery limit = %#v", result)
	}
	if sent, _, _, _ := discord.snapshot(); len(sent) != 0 {
		t.Fatal("negotiated rejection reached Discord")
	}
}

func TestNegotiatedAttachmentCountSizeAndCumulativeOverflow(t *testing.T) {
	discord := &fakeDiscord{}
	var output bytes.Buffer
	runtime, _ := NewRuntime(&output, fixtureDependencies(discord))
	initialize := channeladapter.Initialize{
		SelectedVersion: 1, ProfileID: "default", Features: []channeladapter.Feature{channeladapter.FeatureAttachments},
		Limits: channeladapter.Limits{MaxFrameBytes: 4096, MaxTextBytes: 32, MaxAttachments: 1, MaxAttachmentBytes: 2, MaxOutstanding: 8},
		Policy: channeladapter.RuntimePolicy{Participation: channeladapter.ParticipationAmbient, MaxInboundTextBytes: 32, MaxDeliveryTextBytes: 32, MaxAttachmentBytes: 2},
	}
	runtime.setNegotiated(initialize)
	runtime.writer.setBounds(4096, 8)
	runtime.profile, runtime.client = fixtureProfile(), discord
	runtime.handleMessage(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "603", ChannelID: "555", GuildID: "444", Content: "x", Author: &discordgo.User{ID: "333"}, Attachments: []*discordgo.MessageAttachment{{ID: "1", Filename: "a", Size: 1}, {ID: "2", Filename: "b", Size: 1}}}})
	runtime.handleMessage(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "604", ChannelID: "555", GuildID: "444", Content: "x", Author: &discordgo.User{ID: "333"}, Attachments: []*discordgo.MessageAttachment{{ID: "3", Filename: "a", Size: 3}}}})
	runtime.receiveAttachment("host.chunk.1", channeladapter.AttachmentDeliver{TransferID: "transfer.1", Sequence: 0, Name: "a", Data: "YWI=", Final: false})
	runtime.receiveAttachment("host.chunk.2", channeladapter.AttachmentDeliver{TransferID: "transfer.1", Sequence: 1, Data: "Yw==", Final: true})
	runtime.receiveAttachment("host.chunk.3", channeladapter.AttachmentDeliver{TransferID: "transfer.1", Sequence: 0, Name: "a", Data: "YQ==", Final: true})
	if err := runtime.writer.drain(time.Second); err != nil {
		t.Fatal(err)
	}
	frames := decodeAdapterOutput(t, output.Bytes())
	if frames[0].Payload.(*channeladapter.Diagnostic).Code != "attachments_too_many" || frames[1].Payload.(*channeladapter.Diagnostic).Code != "attachment_too_large" {
		t.Fatalf("attachment diagnostics = %#v", frames[:2])
	}
	if result := frames[3].Payload.(*channeladapter.AttachmentResult); result.Disposition != channeladapter.EffectFailed {
		t.Fatalf("cumulative overflow = %#v", result)
	}
	if result := frames[4].Payload.(*channeladapter.AttachmentResult); result.Disposition != channeladapter.EffectExact {
		t.Fatalf("invalid transfer was not retired = %#v", result)
	}
}

type blockingWriter struct {
	release chan struct{}
	once    sync.Once
}

func (writer *blockingWriter) Write(data []byte) (int, error) {
	<-writer.release
	return len(data), nil
}

func (writer *blockingWriter) unblock() { writer.once.Do(func() { close(writer.release) }) }

type partialBlockingWriter struct {
	firstWrite chan struct{}
	release    chan struct{}
	writes     int
}

func (writer *partialBlockingWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == 1 {
		close(writer.firstWrite)
		return 1, nil
	}
	<-writer.release
	return len(data), nil
}

func TestProtocolWriterQueueIsBoundedAndTimesOutWithoutBlockingAdmission(t *testing.T) {
	blocked := &blockingWriter{release: make(chan struct{})}
	fatal := make(chan error, 1)
	never := func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	writer := newProtocolWriter(blocked, never, fatal)
	var full error
	for index := 0; index < channeladapter.MaxQueuedFrames+1; index++ {
		_, full = writer.sendID(channeladapter.Connection{State: channeladapter.ConnectionReady, Attempt: 1}, "")
	}
	if full == nil || !strings.Contains(full.Error(), "queue is full") {
		t.Fatalf("bounded admission error = %v", full)
	}
	writer.mu.Lock()
	count, size := writer.queueCount, writer.queueBytes
	writer.mu.Unlock()
	if count > channeladapter.MaxQueuedFrames || size > channeladapter.MaxQueuedBytes {
		t.Fatalf("queue retained count=%d bytes=%d", count, size)
	}
	blocked.unblock()

	timeout := make(chan time.Time, 1)
	blocked = &blockingWriter{release: make(chan struct{})}
	fatal = make(chan error, 1)
	writer = newProtocolWriter(blocked, func(time.Duration) <-chan time.Time { return timeout }, fatal)
	if _, err := writer.sendID(channeladapter.Connection{State: channeladapter.ConnectionReady, Attempt: 1}, ""); err != nil {
		t.Fatal(err)
	}
	timeout <- time.Unix(1, 0)
	select {
	case err := <-fatal:
		if !strings.Contains(err.Error(), "no progress") {
			t.Fatalf("progress error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked protocol write did not time out")
	}
	blocked.unblock()
}

func TestProtocolWriterResetsDeadlineAfterProgress(t *testing.T) {
	output := &partialBlockingWriter{firstWrite: make(chan struct{}), release: make(chan struct{})}
	fatal := make(chan error, 1)
	deadlines := make(chan chan time.Time, 2)
	after := func(time.Duration) <-chan time.Time {
		deadline := make(chan time.Time, 1)
		deadlines <- deadline
		return deadline
	}
	writer := newProtocolWriter(output, after, fatal)
	if _, err := writer.sendID(channeladapter.Connection{State: channeladapter.ConnectionReady, Attempt: 1}, ""); err != nil {
		t.Fatal(err)
	}
	<-output.firstWrite
	first := <-deadlines
	second := <-deadlines
	first <- time.Unix(1, 0)
	close(output.release)
	if err := writer.drain(time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-fatal:
		t.Fatalf("retired progress deadline remained active: %v", err)
	default:
	}
	_ = second
}

func TestHandshakeAndRuntimeCorrelationsFailClosed(t *testing.T) {
	initialize := canonicalInitialize()
	for _, test := range []struct {
		name   string
		frames []channeladapter.Envelope
		want   string
	}{
		{name: "initialize", frames: []channeladapter.Envelope{{ProtocolVersion: 1, ID: "host.initialize.1", CorrelationID: "adapter.wrong", Payload: initialize}}, want: "did not correlate"},
		{name: "event ack", frames: []channeladapter.Envelope{{ProtocolVersion: 1, ID: "host.initialize.1", CorrelationID: "adapter.00000001", Payload: initialize}, {ProtocolVersion: 1, ID: "host.ack.1", CorrelationID: "adapter.unknown", Payload: channeladapter.EventAck{Disposition: "accepted"}}}, want: "unknown event"},
		{name: "control result", frames: []channeladapter.Envelope{{ProtocolVersion: 1, ID: "host.initialize.1", CorrelationID: "adapter.00000001", Payload: initialize}, {ProtocolVersion: 1, ID: "host.control.1", CorrelationID: "adapter.unknown", Payload: channeladapter.ControlResult{Action: channeladapter.ControlReset, Disposition: channeladapter.ControlFailed, Failure: channeladapter.Failure{Class: channeladapter.DiagnosticInternal, Code: "failed"}}}}, want: "unknown control-result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := encodeHostFrames(t, test.frames...)
			var output bytes.Buffer
			runtime, _ := NewRuntime(&output, fixtureDependencies(&fakeDiscord{}))
			err := runtime.Run(context.Background(), bytes.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("correlation error = %v", err)
			}
		})
	}
}

func TestInteractionAcknowledgesDiscordOnlyAfterDurableEventAck(t *testing.T) {
	discord := &fakeDiscord{}
	var output bytes.Buffer
	runtime, _ := NewRuntime(&output, fixtureDependencies(discord))
	runtime.client = discord
	pending := pendingInteraction{hostFrameID: "host.interaction.1", request: confirmRequest(), handle: "handle"}
	answer := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerSubmit, Fields: []channeladapter.FieldAnswer{{FieldID: "confirmation", Confirmed: boolPointer(true)}}}
	callback := pendingCallback{interaction: componentInteraction("h1.00000000000000000000000000000000.y").Interaction}
	if err := runtime.finishInteraction(pending, answer, callback); err != nil {
		t.Fatal(err)
	}
	if err := runtime.writer.drain(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, _, responses, _ := discord.snapshot(); len(responses) != 0 {
		t.Fatal("Discord was acknowledged before durable host acceptance")
	}
	eventID := onlyCallbackID(t, runtime)
	if _, err := runtime.handleHostFrame(context.Background(), channeladapter.Envelope{ProtocolVersion: 1, ID: "host.ack.1", CorrelationID: eventID, Payload: &channeladapter.EventAck{Disposition: "rejected"}}); err != nil {
		t.Fatal(err)
	}
	_, _, responses, _ := discord.snapshot()
	if len(responses) != 1 || !strings.Contains(responses[0].Data.Content, "could not be accepted") {
		t.Fatalf("rejected acknowledgement = %#v", responses)
	}
}

func TestInteractionAckTimeoutNeverClaimsSuccess(t *testing.T) {
	discord := &fakeDiscord{}
	timeout := make(chan time.Time, 1)
	dependencies := fixtureDependencies(discord)
	dependencies.After = func(time.Duration) <-chan time.Time { return timeout }
	var output bytes.Buffer
	runtime, _ := NewRuntime(&output, dependencies)
	runtime.client = discord
	pending := pendingInteraction{hostFrameID: "host.interaction.1", request: confirmRequest(), handle: "handle"}
	answer := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: channeladapter.AnswerCancel}
	if err := runtime.finishInteraction(pending, answer, pendingCallback{interaction: commandInteraction("status").Interaction, cancelled: true}); err != nil {
		t.Fatal(err)
	}
	timeout <- time.Unix(1, 0)
	select {
	case <-runtime.fatal:
	case <-time.After(time.Second):
		t.Fatal("interaction acknowledgement timeout was not terminal")
	}
	if _, _, responses, _ := discord.snapshot(); len(responses) != 0 {
		t.Fatal("timed-out interaction claimed success")
	}
}

func TestCompleteTextFallbackGrammar(t *testing.T) {
	freeform := channeladapter.Field{ID: "choices", Kind: channeladapter.InteractionChooseMany, Label: "Choices", Required: true, Options: []channeladapter.Option{{ID: "one", Label: "One", Value: "one"}, {ID: "two", Label: "Two", Value: "two"}}, AllowFreeform: true, MinSelections: 1, MaxSelections: 3, MinLength: 0, MaxLength: 20}
	request := channeladapter.SemanticInteractionRequest{SchemaVersion: 1, Kind: channeladapter.InteractionChooseMany, Prompt: "Choose", FallbackText: "Reply", Policy: channeladapter.InteractionPolicy{ExpiresAfterSeconds: 60, Cancellation: channeladapter.CancellationAllowed}, Field: &freeform}
	answer, err := fallbackAnswer(request, "1,2;other=custom")
	if err != nil || len(answer.Fields) != 1 || len(answer.Fields[0].OptionIDs) != 2 || answer.Fields[0].Freeform == nil || *answer.Fields[0].Freeform != "custom" {
		t.Fatalf("choose-many fallback = %#v, %v", answer, err)
	}
	form := channeladapter.SemanticInteractionRequest{SchemaVersion: 1, Kind: channeladapter.InteractionForm, Prompt: "Details", FallbackText: "Reply", Policy: request.Policy, Fields: []channeladapter.Field{
		{ID: "when", Kind: channeladapter.InteractionDateTime, Label: "When", Required: true, DateTimeRepresentation: channeladapter.DateTime},
		{ID: "note", Kind: channeladapter.InteractionText, Label: "Note", Required: true, MinLength: 1, MaxLength: 20},
		freeform,
	}}
	answer, err = fallbackAnswer(form, "when: 2026-08-08T10:00:00-07:00\nnote: ready\nchoices: other=x")
	if err != nil || *answer.Fields[0].DateTime != "2026-08-08T17:00:00Z" || *answer.Fields[2].Freeform != "x" {
		t.Fatalf("form fallback = %#v, %v", answer, err)
	}
	if grammar := textInstructions(form); !strings.Contains(grammar, "when:") || !strings.Contains(grammar, "other=TEXT") {
		t.Fatalf("fallback grammar = %q", grammar)
	}
	for _, test := range []struct {
		representation channeladapter.DateTimeRepresentation
		input          string
		want           string
	}{
		{representation: channeladapter.DateOnly, input: "2026-08-08", want: "2026-08-08"},
		{representation: channeladapter.TimeOnly, input: "09:05", want: "09:05"},
	} {
		field := channeladapter.Field{ID: "when", Kind: channeladapter.InteractionDateTime, Required: true, DateTimeRepresentation: test.representation}
		answer, err := fallbackAnswer(channeladapter.SemanticInteractionRequest{SchemaVersion: 1, Kind: channeladapter.InteractionDateTime, Field: &field}, test.input)
		if err != nil || answer.Fields[0].DateTime == nil || *answer.Fields[0].DateTime != test.want {
			t.Fatalf("%s fallback = %#v, %v", test.representation, answer, err)
		}
	}
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (body *closeTrackingBody) Close() error { body.closed = true; return nil }

func TestAttachmentFetchBoundsClosesAndRetires(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader("abc")}
	dependencies := fixtureDependencies(&fakeDiscord{})
	dependencies.HTTP = httpClientFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	var output bytes.Buffer
	runtime, _ := NewRuntime(&output, dependencies)
	runtime.setNegotiated(canonicalInitialize())
	runtime.attachments["attachment"] = attachmentSource{url: "https://example.invalid/a", size: 1}
	runtime.fetchAttachment(context.Background(), "host.fetch.1", channeladapter.AttachmentFetch{TransferID: "transfer.1", AttachmentHandle: "attachment", MaximumBytes: 2})
	_ = runtime.writer.drain(time.Second)
	frames := decodeAdapterOutput(t, output.Bytes())
	result := frames[len(frames)-1].Payload.(*channeladapter.AttachmentResult)
	if result.Failure.Code != "attachment_fetch_oversized" || !body.closed {
		t.Fatalf("oversized fetch = %#v, closed=%t", result, body.closed)
	}
	if _, exists := runtime.attachments["attachment"]; exists {
		t.Fatal("attachment handle was not retired")
	}
}

func TestAttachmentFetchTimeoutCancelsAndClosesResponse(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader("late")}
	deadline := make(chan time.Time, 1)
	dependencies := fixtureDependencies(&fakeDiscord{})
	dependencies.After = func(duration time.Duration) <-chan time.Time {
		if duration == channeladapter.AttachmentTimeout {
			return deadline
		}
		return time.After(duration)
	}
	dependencies.HTTP = httpClientFunc(func(request *http.Request) (*http.Response, error) {
		deadline <- time.Unix(1, 0)
		<-request.Context().Done()
		// A misbehaving transport may ignore cancellation and still return bytes.
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	var output bytes.Buffer
	runtime, _ := NewRuntime(&output, dependencies)
	runtime.setNegotiated(canonicalInitialize())
	runtime.attachments["attachment"] = attachmentSource{url: "https://example.invalid/a", size: 1}
	runtime.fetchAttachment(context.Background(), "host.fetch.1", channeladapter.AttachmentFetch{TransferID: "transfer.1", AttachmentHandle: "attachment", MaximumBytes: 2})
	_ = runtime.writer.drain(time.Second)
	result := decodeAdapterOutput(t, output.Bytes())[0].Payload.(*channeladapter.AttachmentResult)
	if result.Failure.Code != "attachment_fetch_timeout" || !body.closed {
		t.Fatalf("timeout fetch = %#v, closed=%t", result, body.closed)
	}
}

func canonicalInitialize() channeladapter.Initialize {
	return channeladapter.Initialize{SelectedVersion: 1, ProfileID: "default", Features: append([]channeladapter.Feature(nil), discordFeatures...), Limits: discordLimits, Policy: channeladapter.RuntimePolicy{Participation: channeladapter.ParticipationAmbient, MaxInboundTextBytes: channeladapter.MaxTextBytes, MaxDeliveryTextBytes: channeladapter.MaxTextBytes, MaxAttachmentBytes: channeladapter.MaxAttachmentBytes}}
}

func decodeAdapterOutput(t *testing.T, data []byte) []channeladapter.Envelope {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	frames := make([]channeladapter.Envelope, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		frame, err := channeladapter.DecodeFrame(line, channeladapter.FromAdapter)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	return frames
}

func encodeHostFrames(t *testing.T, frames ...channeladapter.Envelope) []byte {
	t.Helper()
	var buffer bytes.Buffer
	encoder := channeladapter.NewEncoder(&buffer)
	for _, frame := range frames {
		if err := encoder.Write(frame, channeladapter.FromHost); err != nil {
			t.Fatal(err)
		}
	}
	return buffer.Bytes()
}

func onlyCallbackID(t *testing.T, runtime *Runtime) string {
	t.Helper()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.callbacks) != 1 {
		t.Fatalf("callbacks = %#v", runtime.callbacks)
	}
	for id := range runtime.callbacks {
		return id
	}
	return ""
}
