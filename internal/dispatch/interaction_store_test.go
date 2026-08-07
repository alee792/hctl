package dispatch

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/interaction"
	"hctl/internal/session"
)

func TestConversationStorePersistsInteractionMutationsAcrossConversations(t *testing.T) {
	root := t.TempDir()
	store, err := openConversationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	refs := []conversationRef{
		{agentID: "agent@one", harness: "claude", id: "conversation-one", fingerprint: "source-1"},
		{agentID: "agent@one", harness: "claude", id: "conversation-two", fingerprint: "source-1"},
	}
	var group sync.WaitGroup
	for index, ref := range refs {
		group.Add(1)
		go func(index int, ref conversationRef) {
			defer group.Done()
			pending := storeTestLifecycle(string(rune('a' + index)))
			_, _, err := store.accept(ref, pending.InputID, "origin")
			if err == nil {
				_, err = store.startNext(ref)
			}
			if err == nil {
				err = store.openInteraction(ref, pending)
			}
			if err != nil {
				t.Errorf("conversation %d: %v", index, err)
			}
		}(index, ref)
	}
	group.Wait()

	reloaded, err := openConversationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for index, ref := range refs {
		state, err := reloaded.loadInteraction(ref)
		if err != nil || state.Pending == nil || state.Pending.ID != storeTestLifecycle(string(rune('a'+index))).ID {
			t.Fatalf("conversation %d state = %#v, %v", index, state, err)
		}
	}
}

func TestConversationStoreRollsBackInteractionWhenSaveFails(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "codex", id: "conversation", fingerprint: "source-1"}
	openStoreTestInteraction(t, store, ref, storeTestLifecycle("a"))
	store.save = func(string, *session.State) error { return errors.New("disk full") }
	err = store.updateInteraction(ref, storeTestLifecycle("a").ID, func(pending *interaction.Lifecycle) error {
		pending.Delivery = interaction.DeliveryUncertain
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("save error = %v", err)
	}
	state, err := store.loadInteraction(ref)
	if err != nil || state.Pending == nil || state.Pending.Delivery != interaction.DeliveryPending {
		t.Fatalf("rolled back state = %#v, %v", state, err)
	}
}

func TestConversationStoreRejectsInvalidInteractionMutationWithoutChangingState(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
	openStoreTestInteraction(t, store, ref, storeTestLifecycle("a"))
	if err := store.updateInteraction(ref, storeTestLifecycle("a").ID, func(pending *interaction.Lifecycle) error {
		pending.Owner.SurfaceKey = "raw-vendor-id"
		return nil
	}); err == nil {
		t.Fatal("invalid mutation was accepted")
	}
	state, err := store.loadInteraction(ref)
	if err != nil || state.Pending.Owner.SurfaceKey != strings.Repeat("a", 64) {
		t.Fatalf("state changed after rejected mutation: %#v, %v", state, err)
	}
}

func TestConversationStoreRollsBackEveryFailedInteractionMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*interaction.Lifecycle) error
	}{
		{name: "callback", mutate: func(*interaction.Lifecycle) error { return errors.New("rejected") }},
		{name: "validation", mutate: func(pending *interaction.Lifecycle) error {
			pending.Owner.SurfaceKey = "raw-vendor-id"
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := openConversationStore(root)
			if err != nil {
				t.Fatal(err)
			}
			failedRef := conversationRef{agentID: "agent@one", harness: "claude", id: "failed", fingerprint: "source-1"}
			openStoreTestInteraction(t, store, failedRef, storeTestLifecycle("a"))
			if err := store.updateInteraction(failedRef, storeTestLifecycle("a").ID, test.mutate); err == nil {
				t.Fatal("failed interaction mutation succeeded")
			}
			if state, err := store.loadInteraction(failedRef); err != nil || state.Pending == nil || len(state.Tombstones) != 0 {
				t.Fatalf("failed mutation remained visible: %#v, %v", state, err)
			}

			otherRef := conversationRef{agentID: "agent@one", harness: "claude", id: "other", fingerprint: "source-1"}
			openStoreTestInteraction(t, store, otherRef, storeTestLifecycle("b"))
			reloaded, err := openConversationStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if state, err := reloaded.loadInteraction(failedRef); err != nil || state.Pending == nil || state.Pending.Owner.SurfaceKey != strings.Repeat("a", 64) {
				t.Fatalf("failed mutation was later persisted: %#v, %v", state, err)
			}
		})
	}
}

func TestConversationStoreReturnsDeepInteractionCopies(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "codex", id: "conversation", fingerprint: "source-1"}
	openStoreTestInteraction(t, store, ref, storeTestLifecycle("a"))
	loaded, err := store.loadInteraction(ref)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Pending.Request.Prompt = "changed"
	loaded.Pending.Owner.SurfaceKey = strings.Repeat("c", 64)
	again, err := store.loadInteraction(ref)
	if err != nil {
		t.Fatal(err)
	}
	if again.Pending.Request.Prompt != "Proceed?" || again.Pending.Owner.SurfaceKey != strings.Repeat("a", 64) {
		t.Fatalf("caller mutated store-owned interaction state: %#v", again.Pending)
	}
}

func TestConversationStoreSerializesConcurrentInteractionReadsAndWrites(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
	openStoreTestInteraction(t, store, ref, storeTestLifecycle("a"))
	var group sync.WaitGroup
	for index := range 32 {
		group.Add(2)
		go func(index int) {
			defer group.Done()
			if err := store.updateInteraction(ref, storeTestLifecycle("a").ID, func(pending *interaction.Lifecycle) error {
				if index%2 == 0 {
					pending.Delivery = interaction.DeliveryIntended
				} else {
					pending.Delivery = interaction.DeliveryUncertain
				}
				return nil
			}); err != nil {
				t.Errorf("write %d: %v", index, err)
			}
		}(index)
		go func() {
			defer group.Done()
			state, err := store.loadInteraction(ref)
			if err != nil || state.Pending == nil {
				t.Errorf("read state = %#v, %v", state, err)
			}
		}()
	}
	group.Wait()
}

func TestConversationStoreGatesInputAndQueuedWorkWhileWaiting(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
	if status, duplicate, err := store.accept(ref, "queued-before", "first"); err != nil || duplicate || status != "queued" {
		t.Fatalf("initial accept = %q, %t, %v", status, duplicate, err)
	}
	if _, err := store.startNext(ref); err != nil {
		t.Fatal(err)
	}
	pending := storeTestLifecycle("a")
	pending.InputID = "queued-before"
	if err := store.openInteraction(ref, pending); err != nil {
		t.Fatal(err)
	}
	if status, duplicate, err := store.accept(ref, "queued-before", "first"); err != nil || !duplicate || status != "parked" {
		t.Fatalf("duplicate while waiting = %q, %t, %v", status, duplicate, err)
	}
	if status, duplicate, err := store.accept(ref, "ordinary", "second"); err != nil || duplicate || status != "waiting_for_input" {
		t.Fatalf("ordinary input while waiting = %q, %t, %v", status, duplicate, err)
	}
	if _, err := store.startNext(ref); !errors.Is(err, ErrWaitingForInput) {
		t.Fatalf("queued input started while waiting: %v", err)
	}
	snapshot, err := store.snapshot(ref)
	if err != nil || !snapshot.waitingForInput || snapshot.queueLen != 1 || snapshot.active {
		t.Fatalf("redacted waiting snapshot = %#v, %v", snapshot, err)
	}
}

func TestConversationStoreGatesWorkspaceAssignmentAndResetWhileWaiting(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "codex", id: "conversation", fingerprint: "source-1"}
	openStoreTestInteraction(t, store, ref, storeTestLifecycle("a"))
	if status, duplicate, err := store.assignWorkspaceAndAccept(ref, "/tmp/should-not-stick", "hctl/test/nope", "write", "write"); err != nil || duplicate || status != "waiting_for_input" {
		t.Fatalf("workspace input while waiting = %q, %t, %v", status, duplicate, err)
	}
	if err := store.reset(ref); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("reset while waiting = %v", err)
	}
	snapshot, err := store.snapshot(ref)
	if err != nil || snapshot.workspace != "" || !snapshot.waitingForInput {
		t.Fatalf("waiting state changed by rejected operations: %#v, %v", snapshot, err)
	}
}

func TestConversationStorePreservesWaitingWorktrees(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
	pending := storeTestLifecycle("a")
	openStoreTestInteraction(t, store, ref, pending)
	conversation, err := store.lookup(ref)
	if err != nil || conversation == nil {
		t.Fatalf("conversation = %#v, %v", conversation, err)
	}
	conversation.WorkspaceRoot = "/tmp/worktree"
	conversation.WorktreeBranch = "hctl/test/one"
	conversation.Interaction.Phase = interaction.PhaseResuming
	conversation.Interaction.Delivery = interaction.DeliveryDelivered
	answer, err := interaction.NormalizeAnswer(conversation.Interaction.Request, interaction.Answer{
		SchemaVersion: interaction.SchemaVersion,
		Action:        interaction.ActionSubmit,
		Fields:        []interaction.FieldAnswer{{FieldID: "approved", Confirmed: storeBoolPointer(true)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	conversation.Interaction.Answer = &answer
	conversation.Interaction.AnswerDigest, err = interaction.DigestAnswer(answer)
	if err != nil {
		t.Fatal(err)
	}
	conversation.Interaction.Resume = interaction.ResumeUncertain
	records, err := store.workspaceRecords(ref)
	if err != nil || len(records) != 1 || !records[0].busy || !records[0].uncertain {
		t.Fatalf("waiting worktree record = %#v, %v", records, err)
	}
}

func TestConversationStoreOpenAtomicallyParksOrigin(t *testing.T) {
	root := t.TempDir()
	store, err := openConversationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
	pending := storeTestLifecycle("a")
	if _, _, err := store.accept(ref, pending.InputID, "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.startNext(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.interactionStore(ref).Open(pending); err != nil {
		t.Fatal(err)
	}
	reloaded, err := openConversationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := reloaded.lookup(ref)
	if err != nil || conversation == nil || len(conversation.Queue) != 1 || conversation.Queue[0].Status != "parked" || conversation.Interaction == nil || conversation.Interaction.Delivery != interaction.DeliveryPending {
		t.Fatalf("parked state = %#v, %v", conversation, err)
	}
}

func TestConversationStoreOpenRejectsMismatchedAndRollsBackFailedSave(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "codex", id: "conversation", fingerprint: "source-1"}
	pending := storeTestLifecycle("a")
	if _, _, err := store.accept(ref, pending.InputID, "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.startNext(ref); err != nil {
		t.Fatal(err)
	}
	mismatch := *pending
	mismatch.InputID = "different"
	if err := store.openInteraction(ref, &mismatch); err == nil {
		t.Fatal("mismatched interaction origin was accepted")
	}
	if status, _, err := store.inputStatus(ref, pending.InputID); err != nil || status != "active" {
		t.Fatalf("mismatch changed queue: %q, %v", status, err)
	}
	store.save = func(string, *session.State) error { return errors.New("disk full") }
	if err := store.openInteraction(ref, pending); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("open save error = %v", err)
	}
	state, err := store.loadInteraction(ref)
	if err != nil || state.Pending != nil {
		t.Fatalf("failed open persisted interaction: %#v, %v", state, err)
	}
	if status, _, err := store.inputStatus(ref, pending.InputID); err != nil || status != "active" {
		t.Fatalf("failed open left parked queue: %q, %v", status, err)
	}
}

func TestConversationStoreFinishAtomicallyCompletesParkedOrigin(t *testing.T) {
	finishedAt := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name    string
		phase   interaction.Phase
		outcome string
		prepare func(*interaction.Lifecycle) error
	}{
		{name: "completed", phase: interaction.PhaseCompleted, outcome: "completed", prepare: prepareResumingInteraction},
		{name: "cancelled render", phase: interaction.PhaseCancelled, outcome: "failed", prepare: func(pending *interaction.Lifecycle) error {
			pending.Delivery = interaction.DeliveryIntended
			return nil
		}},
		{name: "cancelled answer", phase: interaction.PhaseCancelled, outcome: "cancelled", prepare: prepareCancelledInteraction},
		{name: "expired", phase: interaction.PhaseExpired, outcome: "expired", prepare: func(*interaction.Lifecycle) error { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := openConversationStore(root)
			if err != nil {
				t.Fatal(err)
			}
			ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
			pending := storeTestLifecycle("a")
			openStoreTestInteraction(t, store, ref, pending)
			if err := store.updateInteraction(ref, pending.ID, test.prepare); err != nil {
				t.Fatal(err)
			}
			finish := interaction.FinishRequest{InteractionID: pending.ID, Phase: test.phase, OriginOutcome: test.outcome, FinishedAt: finishedAt}
			if test.phase == interaction.PhaseCompleted {
				finish.ResultSessionID = "resumed-session"
			}
			if err := store.interactionStore(ref).Finish(finish); err != nil {
				t.Fatal(err)
			}
			reloaded, err := openConversationStore(root)
			if err != nil {
				t.Fatal(err)
			}
			conversation, err := reloaded.lookup(ref)
			if err != nil || conversation == nil || conversation.Interaction != nil || len(conversation.Queue) != 0 || conversation.Outcomes[pending.InputID] != test.outcome || len(conversation.InteractionTombstones) != 1 || conversation.InteractionTombstones[0].Phase != test.phase {
				t.Fatalf("terminal state = %#v, %v", conversation, err)
			}
			if test.phase == interaction.PhaseCompleted && conversation.SessionID != "resumed-session" {
				t.Fatalf("result session = %q", conversation.SessionID)
			}
			if err := store.finishInteraction(ref, finish); err != nil {
				t.Fatalf("duplicate finish = %v", err)
			}
		})
	}
}

func TestConversationStoreFinishRejectsInvalidPhaseAndRollsBackFailedSave(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
	pending := storeTestLifecycle("a")
	openStoreTestInteraction(t, store, ref, pending)
	finish := interaction.FinishRequest{InteractionID: pending.ID, Phase: interaction.PhaseCompleted, OriginOutcome: "completed", FinishedAt: time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)}
	if err := store.finishInteraction(ref, finish); !errors.Is(err, interaction.ErrInteractionLate) {
		t.Fatalf("completed before resume = %v", err)
	}
	if err := store.updateInteraction(ref, pending.ID, prepareResumingInteraction); err != nil {
		t.Fatal(err)
	}
	store.save = func(string, *session.State) error { return errors.New("disk full") }
	if err := store.finishInteraction(ref, finish); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("finish save error = %v", err)
	}
	state, err := store.loadInteraction(ref)
	if err != nil || state.Pending == nil || state.Pending.Phase != interaction.PhaseResuming || len(state.Tombstones) != 0 {
		t.Fatalf("failed finish changed interaction: %#v, %v", state, err)
	}
	if status, _, err := store.inputStatus(ref, pending.InputID); err != nil || status != "parked" {
		t.Fatalf("failed finish changed origin: %q, %v", status, err)
	}
}

func TestConversationStoreRejectsReusingTerminalInteractionID(t *testing.T) {
	store, err := openConversationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ref := conversationRef{agentID: "agent@one", harness: "claude", id: "conversation", fingerprint: "source-1"}
	pending := storeTestLifecycle("a")
	openStoreTestInteraction(t, store, ref, pending)
	if err := store.updateInteraction(ref, pending.ID, func(current *interaction.Lifecycle) error {
		current.Delivery = interaction.DeliveryIntended
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.finishInteraction(ref, interaction.FinishRequest{
		InteractionID: pending.ID, Phase: interaction.PhaseCancelled, OriginOutcome: "failed",
		FinishedAt: time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	reused := storeTestLifecycle("b")
	reused.ID = pending.ID
	if _, _, err := store.accept(ref, reused.InputID, "new origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.startNext(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.openInteraction(ref, reused); err == nil {
		t.Fatal("terminal interaction ID was reused")
	}
	if status, _, err := store.inputStatus(ref, reused.InputID); err != nil || status != "active" {
		t.Fatalf("rejected reuse changed origin: %q, %v", status, err)
	}
}

func prepareResumingInteraction(pending *interaction.Lifecycle) error {
	confirmed := true
	answer, err := interaction.NormalizeAnswer(pending.Request, interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}})
	if err != nil {
		return err
	}
	pending.Delivery = interaction.DeliveryDelivered
	pending.Answer = &answer
	pending.AnswerDigest, err = interaction.DigestAnswer(answer)
	pending.Phase = interaction.PhaseResuming
	pending.Resume = interaction.ResumeIntended
	return err
}

func prepareCancelledInteraction(pending *interaction.Lifecycle) error {
	answer := interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionCancel}
	pending.Delivery = interaction.DeliveryDelivered
	pending.Answer = &answer
	var err error
	pending.AnswerDigest, err = interaction.DigestAnswer(answer)
	pending.Phase = interaction.PhaseAnswered
	pending.Resume = interaction.ResumePending
	return err
}

func storeTestLifecycle(suffix string) *interaction.Lifecycle {
	request := interaction.Request{
		SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindConfirm, Prompt: "Proceed?",
		Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
		Field:  &interaction.Field{ID: "approved", Kind: interaction.KindConfirm, Label: "Proceed", Required: true},
	}
	return &interaction.Lifecycle{
		ID: "interaction_123456789" + suffix, InputID: "message-" + suffix,
		Owner:   interaction.Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request: request, Resolution: interaction.Resolution{Mode: interaction.RenderNative},
		ExpiresAt: time.Date(2026, 8, 7, 2, 0, 0, 0, time.UTC), Continuation: interaction.ContinuationTurn,
		Phase: interaction.PhaseRequested, Delivery: interaction.DeliveryPending,
	}
}

func openStoreTestInteraction(t *testing.T, store *conversationStore, ref conversationRef, pending *interaction.Lifecycle) {
	t.Helper()
	if status, duplicate, err := store.accept(ref, pending.InputID, "origin"); err != nil || duplicate || status != "queued" {
		t.Fatalf("accept origin = %q, %t, %v", status, duplicate, err)
	}
	if next, err := store.startNext(ref); err != nil || next.ID != pending.InputID {
		t.Fatalf("start origin = %#v, %v", next, err)
	}
	if err := store.openInteraction(ref, pending); err != nil {
		t.Fatalf("open interaction = %v", err)
	}
}

func storeBoolPointer(value bool) *bool { return &value }
