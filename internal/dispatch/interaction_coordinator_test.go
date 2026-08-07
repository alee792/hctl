package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"hctl/internal/interaction"
)

func TestCoordinatorAtomicallyParksAndCompletesDispatcherOrigin(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "codex", id: "discord-guild", fingerprint: "source-1"}
	if _, _, err := store.accept(ref, "message-1", "origin"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.accept(ref, "message-2", "successor"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.startNext(ref); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	coordinator, err := interaction.NewCoordinator(
		store.interactionStore(ref),
		dispatchRendererFunc(func(context.Context, interaction.RenderIntent) interaction.EffectOutcome {
			return interaction.EffectSucceeded
		}),
		dispatchContinuationFunc(func(context.Context, interaction.ContinuationIntent) interaction.ContinuationResult {
			return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed", ResultSessionID: "session-2"}
		}),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	request := interaction.Request{
		SchemaVersion: interaction.SchemaVersion,
		Kind:          interaction.KindConfirm,
		Prompt:        "Proceed?",
		Policy:        interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
		Field:         &interaction.Field{ID: "approved", Kind: interaction.KindConfirm, Label: "Proceed", Required: true},
	}
	open := interaction.OpenRequest{
		InteractionID: "interaction_1234567890", InputID: "message-1",
		Owner:   interaction.Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request: request, Resolution: interaction.Resolution{Mode: interaction.RenderNative},
		Continuation: interaction.ContinuationTurn,
	}
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.snapshot(ref)
	if err != nil || !snapshot.waitingForInput || snapshot.active || snapshot.firstID != "message-1" {
		t.Fatalf("parked snapshot = %#v, %v", snapshot, err)
	}
	if _, err := store.startNext(ref); !errors.Is(err, ErrWaitingForInput) {
		t.Fatalf("parked successor started: %v", err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	confirmed := true
	answer := interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}}
	if disposition, err := coordinator.AcceptAnswer(interaction.AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: answer}); err != nil || disposition != interaction.AnswerAccepted {
		t.Fatalf("answer = %q, %v", disposition, err)
	}
	if err := coordinator.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.snapshot(ref)
	if err != nil || snapshot.waitingForInput || snapshot.queueLen != 1 || snapshot.firstID != "message-2" || snapshot.sessionID != "session-2" {
		t.Fatalf("completed snapshot = %#v, %v", snapshot, err)
	}
	if status, duplicate, err := store.inputStatus(ref, "message-1"); err != nil || !duplicate || status != "completed" {
		t.Fatalf("origin outcome = %q, %v, %v", status, duplicate, err)
	}
	if next, err := store.startNext(ref); err != nil || next.ID != "message-2" {
		t.Fatalf("successor = %#v, %v", next, err)
	}
}

type dispatchRendererFunc func(context.Context, interaction.RenderIntent) interaction.EffectOutcome

func (f dispatchRendererFunc) Render(ctx context.Context, intent interaction.RenderIntent) interaction.EffectOutcome {
	return f(ctx, intent)
}

type dispatchContinuationFunc func(context.Context, interaction.ContinuationIntent) interaction.ContinuationResult

func (f dispatchContinuationFunc) Resume(ctx context.Context, intent interaction.ContinuationIntent) interaction.ContinuationResult {
	return f(ctx, intent)
}
