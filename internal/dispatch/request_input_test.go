package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hctl/internal/harness"
	"hctl/internal/interaction"
)

func TestRequestInputRejectsUnprovenNativeSubagentBeforePersistence(t *testing.T) {
	requester := &recordingInteractionRequester{}
	handler := testRequestInputHandler(requester)
	reply := make(chan harness.RequestInputAcknowledgement, 1)
	err := handleRequestInput(context.Background(), handler, "discord-guild", "message-1", &harness.RequestInputEvent{CorrelationID: "tool-call-1", Request: testSemanticRequest(), Reply: reply})
	if !errors.Is(err, ErrRequestInputUnavailable) || requester.calls != 0 {
		t.Fatalf("unproven call err=%v calls=%d", err, requester.calls)
	}
	if result := <-reply; result.Accepted || result.Status != "unavailable" {
		t.Fatalf("reply = %#v", result)
	}
}

func TestRequestInputRecomputesFallbackAndHandsOffOnce(t *testing.T) {
	requester := &recordingInteractionRequester{}
	handler := testRequestInputHandler(requester)
	handler.Capabilities.Kinds = []interaction.Kind{interaction.KindText}
	reply := make(chan harness.RequestInputAcknowledgement, 1)
	event := harness.NewRootRequestInputEvent("tool-call-1", testSemanticRequest(), reply)
	if err := handleRequestInput(context.Background(), handler, "discord-guild", "message-1", event); err != nil {
		t.Fatal(err)
	}
	if result := <-reply; !result.Accepted || result.Status != "accepted" {
		t.Fatalf("reply = %#v", result)
	}
	if requester.calls != 1 || requester.open.InputID != "message-1" || requester.open.Resolution.Mode != interaction.RenderTextFallback || requester.open.Resolution.FallbackText == "" {
		t.Fatalf("handoff = %#v", requester.open)
	}
	if requester.open.Owner.SurfaceKey != strings.Repeat("a", 64) || requester.open.Continuation != interaction.ContinuationTurn {
		t.Fatalf("trusted runtime fields = %#v", requester.open)
	}
}

func TestRequestInputUnavailableFallbackNeverReachesCoordinator(t *testing.T) {
	requester := &recordingInteractionRequester{}
	handler := testRequestInputHandler(requester)
	handler.Capabilities.Kinds = []interaction.Kind{interaction.KindText}
	request := testSemanticRequest()
	request.FallbackText = ""
	err := handler.Handle(context.Background(), RequestInputContext{ConversationID: "discord-guild", InputID: "message-1", CorrelationID: "tool-1", Request: request})
	if err == nil || requester.calls != 0 {
		t.Fatalf("unsupported request err=%v calls=%d", err, requester.calls)
	}
}

func TestFakeHarnessEventParksThroughDurableCoordinatorBeforeAcknowledgement(t *testing.T) {
	p := testProject(t)
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: p.AgentID, harness: "claude", id: "discord-guild", fingerprint: p.SourceFingerprint}
	coordinator, err := interaction.NewCoordinator(
		store.interactionStore(ref),
		dispatchRendererFunc(func(context.Context, interaction.RenderIntent) interaction.EffectOutcome {
			return interaction.EffectSucceeded
		}),
		dispatchContinuationFunc(func(context.Context, interaction.ContinuationIntent) interaction.ContinuationResult {
			return interaction.ContinuationResult{}
		}),
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := testRequestInputHandler(coordinator)
	driver := &requestInputDriver{request: testSemanticRequest(), replies: make(chan harness.RequestInputAcknowledgement, 2)}
	submissions := make(chan Submission, 1)
	submissions <- Submission{InputID: "message-1", Text: "deploy"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	events := make(chan Event, 32)
	go func() {
		done <- runSubmissions(ctx, p, driver, "discord-guild", submissions, func(event Event) error { events <- event; return nil }, runOptions{
			turnTimeout: time.Minute, idleTimeout: time.Hour, timers: newTimer,
			policy: harness.PolicyReadOnly, store: store, requestInputs: handler,
		})
	}()

	first := <-driver.replies
	second := <-driver.replies
	if !first.Accepted || first.Status != "accepted" || second.Accepted || second.Status != "rejected" {
		t.Fatalf("harness acknowledgements = %#v, %#v", first, second)
	}
	seenHibernated := false
	for !seenHibernated {
		select {
		case event := <-events:
			if event.Type == "driver.process_hibernated" {
				seenHibernated = true
			}
		case <-time.After(time.Second):
			t.Fatal("dispatcher did not hibernate before render notification")
		}
	}
	select {
	case event := <-events:
		if event.Type != "interaction.parked" || event.Status != string(harness.RequestInputContinuationTurn) || event.InputID != "message-1" {
			t.Fatalf("parked event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not emit render notification after hibernation")
	}
	snapshot, err := store.snapshot(ref)
	if err != nil || !snapshot.waitingForInput || snapshot.firstID != "message-1" {
		t.Fatalf("durable handoff = %#v, %v", snapshot, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("dispatcher stop = %v", err)
	}
}

type recordingInteractionRequester struct {
	calls int
	open  interaction.OpenRequest
	err   error
}

func (r *recordingInteractionRequester) Request(open interaction.OpenRequest) error {
	r.calls++
	r.open = open
	return r.err
}

func testRequestInputHandler(requester interactionRequester) CoordinatorRequestInputHandler {
	return CoordinatorRequestInputHandler{
		Coordinator: requester, Owner: interaction.Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Continuation: interaction.ContinuationTurn,
		Capabilities: interaction.Capabilities{
			Kinds: []interaction.Kind{interaction.KindChooseOne}, MaxRequestBytes: interaction.MaxRequestBytes,
			MaxPromptBytes: interaction.MaxPromptBytes, MaxFields: interaction.MaxFields,
			MaxOptionsPerField: interaction.MaxOptionsPerField, MaxSelections: interaction.MaxSelections,
			MaxTotalOptions: interaction.MaxTotalOptions, MaxLabelBytes: interaction.MaxLabelBytes,
			MaxDescriptionBytes: interaction.MaxDescriptionBytes, MaxValueBytes: interaction.MaxValueBytes,
			MaxTextRunes: interaction.MaxTextRunes,
		},
	}
}

func testSemanticRequest() interaction.Request {
	return interaction.Request{
		SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindChooseOne,
		Prompt: "Choose target", FallbackText: "Reply staging or production.",
		Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
		Field: &interaction.Field{ID: "target", Kind: interaction.KindChooseOne, Label: "Target", Required: true, MinSelections: 1, MaxSelections: 1, Options: []interaction.Option{
			{ID: "staging", Label: "Staging", Value: "staging"}, {ID: "production", Label: "Production", Value: "production"},
		}},
	}
}

type requestInputDriver struct {
	request interaction.Request
	replies chan harness.RequestInputAcknowledgement
}

func (d *requestInputDriver) Name() string                 { return "claude" }
func (d *requestInputDriver) Executable() string           { return "/fake/claude" }
func (d *requestInputDriver) Verify(context.Context) error { return nil }
func (d *requestInputDriver) Open(context.Context, harness.OpenRequest) (harness.Session, error) {
	return &requestInputSession{driver: d}, nil
}

type requestInputSession struct{ driver *requestInputDriver }

func (s *requestInputSession) InitialEvents() []harness.Event { return nil }
func (s *requestInputSession) RunTurn(_ context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	for _, correlation := range []string{"tool-call-1", "tool-call-2"} {
		reply := make(chan harness.RequestInputAcknowledgement, 1)
		emit(harness.Event{RequestInput: harness.NewRootRequestInputEvent(correlation, s.driver.request, reply)})
		result := <-reply
		s.driver.replies <- result
	}
	return harness.TurnResult{SessionID: "session-1", TurnID: input.ID, Status: string(LifecycleWaiting)}, nil
}
func (s *requestInputSession) Close() error { return nil }
func (s *requestInputSession) Abort()       {}
