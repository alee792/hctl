package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/harness"
	"hctl/internal/interaction"
	"hctl/internal/project"
	"hctl/internal/session"
	"hctl/internal/worktree"
)

func TestManagerOwnsIndependentConversationLifecycles(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 32)
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	for _, input := range []struct {
		conversation string
		id           string
	}{
		{conversation: "discord-guild", id: "message-1"},
		{conversation: "discord-dm", id: "message-2"},
	} {
		result, err := manager.Submit(context.Background(), input.conversation, Submission{InputID: input.id, Text: input.id})
		if err != nil || result.Status != "queued" || result.Duplicate {
			t.Fatalf("submit %s = %+v, %v", input.id, result, err)
		}
	}

	driver.waitStarted(t, "message-1")
	driver.waitStarted(t, "message-2")
	waitManagedEvents(t, events, "turn.started", map[string]string{
		"discord-guild": "message-1",
		"discord-dm":    "message-2",
	})
	for _, conversation := range []string{"discord-guild", "discord-dm"} {
		status := manager.Status(conversation)
		if status.State != LifecycleActive || status.Pending != 1 {
			t.Fatalf("status %s = %+v", conversation, status)
		}
	}

	driver.release("message-1")
	driver.release("message-2")
	waitManagedEvents(t, events, "turn.completed", map[string]string{
		"discord-guild": "message-1",
		"discord-dm":    "message-2",
	})

	for _, conversation := range []string{"discord-guild", "discord-dm"} {
		status := manager.Status(conversation)
		if status.State != LifecycleIdle || status.Pending != 0 {
			t.Fatalf("idle status %s = %+v", conversation, status)
		}
	}
	if got := driver.openCount(); got != 2 {
		t.Fatalf("opened harness processes = %d, want one per conversation", got)
	}

	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		conversation string
		id           string
	}{
		{conversation: "discord-guild", id: "message-1"},
		{conversation: "discord-dm", id: "message-2"},
	} {
		conversation := findConversation(state, input.conversation)
		if conversation == nil || conversation.Outcomes[input.id] != "completed" {
			t.Fatalf("durable conversation %s = %#v", input.conversation, conversation)
		}
	}
}

func TestManagerConfiguresRequestInputBeforeRecoveredWorkersStart(t *testing.T) {
	p := testProject(t)
	driver := newNamedManagerDriver("codex")
	configured := false
	manager, err := NewManagerWithLimitsConfigured(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(string, Event) error { return nil }, func(manager *Manager) error {
		if driver.openCount() != 0 {
			t.Fatal("recovered worker opened before request-input configuration")
		}
		configured = true
		return manager.ConfigureRequestInput(func(string) RequestInputHandler { return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	if !configured {
		t.Fatal("request-input configuration was skipped")
	}
	if result, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "after configuration"}); err != nil || result.Status != "queued" {
		t.Fatalf("submit = %#v, %v", result, err)
	}
	driver.waitStarted(t, "message-1")
	driver.release("message-1")
}

func TestManagerParkingReleasesCapacityAndPreservesSuccessor(t *testing.T) {
	p := testProject(t)
	driver := newNamedManagerDriver("codex")
	waitingEvents := make(chan Event, 4)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(_ string, event Event) error {
		if event.Status == "waiting_for_input" {
			waitingEvents <- event
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	for _, id := range []string{"message-1", "message-2"} {
		if result, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: id, Text: id}); err != nil || result.Status != "queued" {
			t.Fatalf("submit %s = %+v, %v", id, result, err)
		}
	}
	driver.waitStarted(t, "message-1")

	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	coordinator, err := interaction.NewCoordinator(
		manager.store.interactionStoreWithWake(manager.reference("discord-guild"), func() error { return manager.wakeInteraction("discord-guild") }),
		dispatchRendererFunc(func(context.Context, interaction.RenderIntent) interaction.EffectOutcome {
			return interaction.EffectSucceeded
		}),
		dispatchContinuationFunc(func(context.Context, interaction.ContinuationIntent) interaction.ContinuationResult {
			return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed"}
		}),
		func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	request := interaction.Request{
		SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindConfirm, Prompt: "Proceed?",
		Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
		Field:  &interaction.Field{ID: "approved", Kind: interaction.KindConfirm, Label: "Proceed", Required: true},
	}
	open := interaction.OpenRequest{
		InteractionID: "interaction_1234567890", InputID: "message-1",
		Owner:   interaction.Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request: request, Resolution: interaction.Resolution{Mode: interaction.RenderNative}, Continuation: interaction.ContinuationTurn,
	}
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	driver.setStatus("message-1", "waiting_for_input")
	driver.release("message-1")
	deadline := time.Now().Add(2 * time.Second)
	for {
		capacity := manager.Capacity()
		if capacity.Active == 0 && capacity.Resident == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parked capacity = %+v", capacity)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status := manager.Status("discord-guild"); status.State != LifecycleWaiting {
		t.Fatalf("parked status = %+v", status)
	}
	if result, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-3", Text: "ordinary"}); err != nil || result.Status != "waiting_for_input" {
		t.Fatalf("ordinary waiting submission = %+v, %v", result, err)
	}
	select {
	case event := <-waitingEvents:
		if event.Type != "input.rejected" || event.Status != "waiting_for_input" || event.InputID != "" || event.Bytes != 0 {
			t.Fatalf("waiting event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing waiting rejection event")
	}
	select {
	case event := <-waitingEvents:
		t.Fatalf("waiting input emitted an extra event: %+v", event)
	default:
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	confirmed := true
	answer := interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}}
	if _, err := coordinator.AcceptAnswer(interaction.AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: answer}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-2")
	driver.release("message-2")
}

func TestManagerColdRestartDrainsSuccessorAfterTerminalInteraction(t *testing.T) {
	for _, test := range []struct {
		name    string
		phase   interaction.Phase
		outcome string
		prepare func(*interaction.Lifecycle) error
	}{
		{name: "completed", phase: interaction.PhaseCompleted, outcome: "completed", prepare: prepareResumingInteraction},
		{name: "cancelled", phase: interaction.PhaseCancelled, outcome: "failed", prepare: func(pending *interaction.Lifecycle) error {
			pending.Delivery = interaction.DeliveryIntended
			return nil
		}},
		{name: "expired", phase: interaction.PhaseExpired, outcome: "expired", prepare: func(*interaction.Lifecycle) error { return nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testProject(t)
			driver := newNamedManagerDriver("codex")
			store, err := openConversationStore(p.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			ref := conversationRef{agentID: p.AgentID, harness: driver.Name(), id: "discord-guild", fingerprint: p.SourceFingerprint}
			pending := storeTestLifecycle(test.name)
			if _, _, err := store.accept(ref, pending.InputID, "origin"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.accept(ref, "message-successor", "successor"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.startNext(ref); err != nil {
				t.Fatal(err)
			}
			if err := store.openInteraction(ref, pending); err != nil {
				t.Fatal(err)
			}
			if err := store.updateInteraction(ref, pending.ID, test.prepare); err != nil {
				t.Fatal(err)
			}
			finishedAt := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
			if err := store.finishInteraction(ref, interaction.FinishRequest{InteractionID: pending.ID, Phase: test.phase, OriginOutcome: test.outcome, FinishedAt: finishedAt}); err != nil {
				t.Fatal(err)
			}
			manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(string, Event) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			driver.waitStarted(t, "message-successor")
			driver.release("message-successor")
		})
	}
}

func TestManagerColdRestartAndShutdownPreserveWaitingLifecycle(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*interaction.Lifecycle) error
	}{
		{name: "requested", prepare: func(*interaction.Lifecycle) error { return nil }},
		{name: "rendered", prepare: func(pending *interaction.Lifecycle) error {
			pending.Phase = interaction.PhaseRendered
			pending.Delivery = interaction.DeliveryDelivered
			return nil
		}},
		{name: "answered", prepare: func(pending *interaction.Lifecycle) error {
			if err := prepareResumingInteraction(pending); err != nil {
				return err
			}
			pending.Phase = interaction.PhaseAnswered
			pending.Resume = interaction.ResumePending
			return nil
		}},
		{name: "resume-uncertain", prepare: func(pending *interaction.Lifecycle) error {
			if err := prepareResumingInteraction(pending); err != nil {
				return err
			}
			pending.Resume = interaction.ResumeUncertain
			return nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testProject(t)
			driver := newNamedManagerDriver("codex")
			store, err := openConversationStore(p.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			ref := conversationRef{agentID: p.AgentID, harness: driver.Name(), id: "discord-guild", fingerprint: p.SourceFingerprint}
			pending := storeTestLifecycle(test.name)
			pending.ExpiresAt = time.Now().UTC().Truncate(time.Second).Add(time.Hour)
			if _, _, err := store.accept(ref, pending.InputID, "origin"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.startNext(ref); err != nil {
				t.Fatal(err)
			}
			if err := store.openInteraction(ref, pending); err != nil {
				t.Fatal(err)
			}
			if err := store.updateInteraction(ref, pending.ID, test.prepare); err != nil {
				t.Fatal(err)
			}
			expected, err := store.loadInteraction(ref)
			if err != nil || expected.Pending == nil {
				t.Fatalf("expected state = %#v, %v", expected.Pending, err)
			}
			manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(string, Event) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if status := manager.Status("discord-guild"); status.State != LifecycleWaiting {
				t.Fatalf("status = %+v", status)
			}
			if capacity := manager.Capacity(); capacity.Active != 0 || capacity.Resident != 0 {
				t.Fatalf("capacity = %+v", capacity)
			}
			if driver.openCount() != 0 {
				t.Fatalf("opened processes = %d", driver.openCount())
			}
			manager.Close()
			reloaded, err := openConversationStore(p.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			state, err := reloaded.loadInteraction(ref)
			if err != nil || state.Pending == nil || state.Pending.Phase != expected.Pending.Phase || state.Pending.Resume != expected.Pending.Resume {
				t.Fatalf("preserved state = %#v, %v", state.Pending, err)
			}
		})
	}
}

func TestManagerQueuesOneConversationBehindOneResidentProcess(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 32)
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	for _, id := range []string{"message-1", "message-2"} {
		result, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: id, Text: id})
		if err != nil || result.Status != "queued" {
			t.Fatalf("submit %s = %+v, %v", id, result, err)
		}
	}
	driver.waitStarted(t, "message-1")
	status := manager.Status("discord-guild")
	if status.State != LifecycleActive || status.Pending != 2 {
		t.Fatalf("queued status = %+v", status)
	}

	driver.release("message-1")
	driver.waitStarted(t, "message-2")
	driver.release("message-2")
	waitManagedEvents(t, events, "turn.completed", map[string]string{
		"discord-guild": "message-1",
	})
	waitManagedEvents(t, events, "turn.completed", map[string]string{
		"discord-guild": "message-2",
	})
	if got := driver.openCount(); got != 1 {
		t.Fatalf("opened harness processes = %d, want one", got)
	}
}

func TestManagerBoundsActiveTurnsAndAdvancesConversationsFairly(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 64)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 2, 1, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "conversation-one", Submission{InputID: "one-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "one-1")
	if _, err := manager.Submit(context.Background(), "conversation-one", Submission{InputID: "one-2", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	duplicate, err := manager.Submit(context.Background(), "conversation-one", Submission{InputID: "one-1", Text: "first"})
	if err != nil || !duplicate.Duplicate || duplicate.Status != "active" {
		t.Fatalf("active duplicate = %+v, %v", duplicate, err)
	}
	if _, err := manager.Submit(context.Background(), "conversation-two", Submission{InputID: "two-1", Text: "other"}); err != nil {
		t.Fatal(err)
	}
	waitCapacityQueued(t, manager.capacity, "conversation-two")
	if status := manager.Capacity(); status.Active != 1 || status.ActiveLimit != 1 || status.Queued != 2 {
		t.Fatalf("saturated capacity = %+v", status)
	}

	driver.release("one-1")
	driver.waitStarted(t, "two-1")
	if status := manager.Capacity(); status.Active != 1 || status.Resident > status.ResidentLimit {
		t.Fatalf("capacity after fair handoff = %+v", status)
	}
	driver.release("two-1")
	driver.waitStarted(t, "one-2")
	driver.release("one-2")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"conversation-one": "one-2"})
}

func TestManagerShutdownDoesNotGrantWaitingCapacity(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 2, 1, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), "conversation-one", Submission{InputID: "one-1", Text: "active"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "one-1")
	if _, err := manager.Submit(context.Background(), "conversation-two", Submission{InputID: "two-1", Text: "waiting"}); err != nil {
		t.Fatal(err)
	}
	waitCapacityQueued(t, manager.capacity, "conversation-two")

	manager.Close()
	if got := driver.openCount(); got != 1 {
		t.Fatalf("shutdown opened %d processes, want only the active one", got)
	}
	if status := manager.Capacity(); status.Active != 0 || status.Resident != 0 {
		t.Fatalf("shutdown leaked capacity: %+v", status)
	}
}

func TestManagerRestartReusesDurableQueueWithoutDuplicateCapacity(t *testing.T) {
	p := testProject(t)
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		conversation string
		inputID      string
	}{{"conversation-one", "one-1"}, {"conversation-two", "two-1"}} {
		conversation, err := state.GetOrCreate(p.AgentID, "claude", item.conversation, p.SourceFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		if _, duplicate, err := conversation.Accept(item.inputID, "persisted"); err != nil || duplicate {
			t.Fatalf("seed durable input = duplicate %v, error %v", duplicate, err)
		}
	}
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}

	driver := newManagerDriver()
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 2, 1, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	if capacity := manager.Capacity(); capacity.Queued != 2 || capacity.Active != 0 || capacity.Resident != 0 {
		t.Fatalf("restart capacity = %+v", capacity)
	}
	for _, item := range []struct {
		conversation string
		inputID      string
	}{{"conversation-one", "one-1"}, {"conversation-two", "two-1"}} {
		result, err := manager.Submit(context.Background(), item.conversation, Submission{InputID: item.inputID, Text: "redelivered"})
		if err != nil || !result.Duplicate || result.Status != "queued" {
			t.Fatalf("restart duplicate %s = %+v, %v", item.inputID, result, err)
		}
	}
	first := driver.waitAnyStarted(t)
	if first != "one-1" && first != "two-1" {
		t.Fatalf("first restarted input = %s", first)
	}
	if capacity := manager.Capacity(); capacity.Active != 1 || capacity.Queued != 1 {
		t.Fatalf("restart saturation = %+v", capacity)
	}
	driver.release(first)
	second := driver.waitAnyStarted(t)
	if second == first || second != "one-1" && second != "two-1" {
		t.Fatalf("second restarted input = %s after %s", second, first)
	}
	driver.release(second)
}

func TestManagerHibernatesIdleResidentUnderCapacityPressure(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 64)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "conversation-one", Submission{InputID: "one-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "one-1")
	driver.release("one-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"conversation-one": "one-1"})
	if status := manager.Capacity(); status.Resident != 1 || status.Active != 0 {
		t.Fatalf("idle resident capacity = %+v", status)
	}

	if _, err := manager.Submit(context.Background(), "conversation-two", Submission{InputID: "two-1", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	hibernated := waitManagedEvent(t, events, "driver.process_hibernated")
	if hibernated.conversation != "conversation-one" || hibernated.event.Status != "capacity_pressure" {
		t.Fatalf("capacity hibernation = %+v", hibernated)
	}
	driver.waitStarted(t, "two-1")
	if status := manager.Capacity(); status.Resident != 1 || status.Active != 1 {
		t.Fatalf("replacement capacity = %+v", status)
	}
	driver.release("two-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"conversation-two": "two-1"})
}

func TestManagerRotatesResidentAfterTurnToPreventStarvation(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 64)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "conversation-one", Submission{InputID: "one-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "one-1")
	if _, err := manager.Submit(context.Background(), "conversation-one", Submission{InputID: "one-2", Text: "backlog"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), "conversation-two", Submission{InputID: "two-1", Text: "waiting"}); err != nil {
		t.Fatal(err)
	}
	waitCapacityQueued(t, manager.capacity, "conversation-two")
	driver.release("one-1")
	hibernated := waitManagedEvent(t, events, "driver.process_hibernated")
	if hibernated.conversation != "conversation-one" || hibernated.event.Status != "capacity_fairness" {
		t.Fatalf("fairness rotation = %+v", hibernated)
	}
	driver.waitStarted(t, "two-1")
	driver.release("two-1")
	driver.waitStarted(t, "one-2")
	driver.release("one-2")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"conversation-one": "one-2"})
}

func TestManagerConsumesSynchronousCapacityHandoffBeforeReopening(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 64)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	const conversation = "conversation-one"
	if _, err := manager.Submit(context.Background(), conversation, Submission{InputID: "one-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "one-1")
	driver.release("one-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{conversation: "one-1"})

	manager.capacity.mu.Lock()
	state := manager.capacity.states[conversation]
	if state == nil || !state.resident || state.active {
		manager.capacity.mu.Unlock()
		t.Fatalf("resident state before handoff = %#v", state)
	}
	submitted := make(chan error, 1)
	go func() {
		_, submitErr := manager.Submit(context.Background(), conversation, Submission{InputID: "one-2", Text: "second"})
		submitted <- submitErr
	}()
	if err := <-submitted; err != nil {
		manager.capacity.mu.Unlock()
		t.Fatal(err)
	}
	state.hibernating = true
	state.hibernate <- struct{}{}
	manager.capacity.mu.Unlock()

	driver.waitStarted(t, "one-2")
	manager.capacity.mu.Lock()
	pendingHandoff := len(state.hibernate)
	manager.capacity.mu.Unlock()
	if pendingHandoff != 0 {
		t.Fatalf("pending capacity handoff notices = %d, want 0", pendingHandoff)
	}
	if got := driver.closeCount(); got != 1 {
		t.Fatalf("closed harness processes = %d, want one synchronous handoff", got)
	}
	driver.release("one-2")
	waitManagedEvents(t, events, "turn.completed", map[string]string{conversation: "one-2"})
}

func TestManagerRunsConcurrentWritableSurfacesInIsolationForHarnesses(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			p := testProject(t)
			guildRoot, dmRoot := t.TempDir(), t.TempDir()
			provider := &multiWorkspaceProvider{
				base: p,
				assignments: map[string]worktree.Assignment{
					"discord-guild": {Root: guildRoot, Branch: "hctl/test/guild"},
					"discord-dm":    {Root: dmRoot, Branch: "hctl/test/dm"},
				},
			}
			driver := newNamedManagerDriver(harnessName)
			events := make(chan managedEvent, 128)
			manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, 2, 2, func(conversation string, event Event) error {
				events <- managedEvent{conversation: conversation, event: event}
				return nil
			}, newFakeClock().NewTimer, provider)
			if err != nil {
				t.Fatal(err)
			}

			for _, item := range []struct{ conversation, input string }{{"discord-guild", "guild-read"}, {"discord-dm", "dm-read"}} {
				if _, err := manager.Submit(context.Background(), item.conversation, Submission{InputID: item.input, Text: "prepare mutation"}); err != nil {
					t.Fatal(err)
				}
			}
			started := map[string]bool{driver.waitAnyStarted(t): true, driver.waitAnyStarted(t): true}
			if !started["guild-read"] || !started["dm-read"] {
				t.Fatalf("read-only turns started = %v", started)
			}
			driver.release("guild-read")
			driver.release("dm-read")
			waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "guild-read", "discord-dm": "dm-read"})

			for _, item := range []struct{ conversation, input string }{{"discord-guild", "guild-write"}, {"discord-dm", "dm-write"}} {
				result, err := manager.Elevate(context.Background(), item.conversation, Submission{InputID: item.input, Text: "continue with write access"})
				if err != nil || result.Status != "queued" {
					t.Fatalf("elevate %s = %+v, %v", item.conversation, result, err)
				}
			}
			started = map[string]bool{driver.waitAnyStarted(t): true, driver.waitAnyStarted(t): true}
			if !started["guild-write"] || !started["dm-write"] {
				t.Fatalf("writable turns started = %v", started)
			}
			if got := driver.rootForInput("guild-write"); got != guildRoot {
				t.Fatalf("guild writable root = %q, want %q", got, guildRoot)
			}
			if got := driver.rootForInput("dm-write"); got != dmRoot {
				t.Fatalf("DM writable root = %q, want %q", got, dmRoot)
			}
			if capacity := manager.Capacity(); capacity.Active != 2 || capacity.Resident != 2 {
				t.Fatalf("concurrent writable capacity = %+v", capacity)
			}

			// Complete out of order and require each terminal event to retain its surface.
			driver.release("dm-write")
			dmCompleted := waitManagedEvent(t, events, "turn.completed")
			if dmCompleted.conversation != "discord-dm" || dmCompleted.event.InputID != "dm-write" {
				t.Fatalf("first writable completion = %+v", dmCompleted)
			}
			driver.release("guild-write")
			waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "guild-write"})

			state, err := session.Load(p.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			guildState, dmState := findConversation(state, "discord-guild"), findConversation(state, "discord-dm")
			if guildState == nil || dmState == nil || guildState.WorkspaceRoot != guildRoot || dmState.WorkspaceRoot != dmRoot || guildState.WorktreeBranch == dmState.WorktreeBranch {
				t.Fatalf("durable isolated conversations: guild=%#v dm=%#v", guildState, dmState)
			}
			guildSession, dmSession := guildState.SessionID, dmState.SessionID
			manager.Close()

			restartedDriver := newNamedManagerDriver(harnessName)
			restarted, err := newManager(context.Background(), p, restartedDriver, time.Minute, time.Hour, 2, 2, func(string, Event) error { return nil }, newFakeClock().NewTimer, provider)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restarted.Close)
			for _, item := range []struct{ conversation, input string }{{"discord-guild", "guild-next"}, {"discord-dm", "dm-next"}} {
				if _, err := restarted.Submit(context.Background(), item.conversation, Submission{InputID: item.input, Text: "resume"}); err != nil {
					t.Fatal(err)
				}
			}
			started = map[string]bool{restartedDriver.waitAnyStarted(t): true, restartedDriver.waitAnyStarted(t): true}
			if !started["guild-next"] || !started["dm-next"] {
				t.Fatalf("restart turns started = %v", started)
			}
			if restartedDriver.rootForInput("guild-next") != guildRoot || restartedDriver.sessionForInput("guild-next") != guildSession {
				t.Fatalf("guild restart root/session = %q/%q", restartedDriver.rootForInput("guild-next"), restartedDriver.sessionForInput("guild-next"))
			}
			if restartedDriver.rootForInput("dm-next") != dmRoot || restartedDriver.sessionForInput("dm-next") != dmSession {
				t.Fatalf("DM restart root/session = %q/%q", restartedDriver.rootForInput("dm-next"), restartedDriver.sessionForInput("dm-next"))
			}
			restartedDriver.release("guild-next")
			restartedDriver.release("dm-next")
		})
	}
}

func TestManagerContainsHarnessFailureToOneWritableConversation(t *testing.T) {
	p := testProject(t)
	provider := &multiWorkspaceProvider{base: p, assignments: map[string]worktree.Assignment{
		"discord-guild": {Root: t.TempDir(), Branch: "hctl/test/guild"},
		"discord-dm":    {Root: t.TempDir(), Branch: "hctl/test/dm"},
	}}
	driver := newManagerDriver()
	driver.failures["guild-write"] = errors.New("guild harness failed")
	events := make(chan managedEvent, 128)
	manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, 2, 2, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	}, newFakeClock().NewTimer, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	for _, conversation := range []string{"discord-guild", "discord-dm"} {
		if _, err := manager.Submit(context.Background(), conversation, Submission{InputID: conversation + "-read", Text: "prepare"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		driver.release(driver.waitAnyStarted(t))
	}
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "discord-guild-read", "discord-dm": "discord-dm-read"})
	if _, err := manager.Elevate(context.Background(), "discord-guild", Submission{InputID: "guild-write", Text: "write"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Elevate(context.Background(), "discord-dm", Submission{InputID: "dm-write", Text: "write"}); err != nil {
		t.Fatal(err)
	}
	started := map[string]bool{driver.waitAnyStarted(t): true, driver.waitAnyStarted(t): true}
	if !started["guild-write"] || !started["dm-write"] {
		t.Fatalf("writable turns started = %v", started)
	}
	driver.release("guild-write")
	failure := waitManagedEvent(t, events, "driver.process_failed")
	if failure.conversation != "discord-guild" || failure.event.InputID != "guild-write" {
		t.Fatalf("isolated failure = %+v", failure)
	}
	select {
	case <-manager.Done():
		t.Fatal("guild harness failure stopped the DM conversation")
	default:
	}
	driver.release("dm-write")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-dm": "dm-write"})
	if status := manager.Status("discord-dm"); status.State != LifecycleIdle || status.Pending != 0 {
		t.Fatalf("DM state after guild failure = %+v", status)
	}
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	dm := findConversation(state, "discord-dm")
	if dm == nil || dm.Outcomes["dm-write"] != "completed" || dm.WorkspaceRoot != provider.assignments["discord-dm"].Root {
		t.Fatalf("DM durable state after guild failure = %#v", dm)
	}
}

func TestManagerReportsAndContainsWritableResolutionFailure(t *testing.T) {
	p := testProject(t)
	provider := &multiWorkspaceProvider{
		base: p,
		assignments: map[string]worktree.Assignment{
			"discord-guild": {Root: t.TempDir(), Branch: "hctl/test/guild"},
			"discord-dm":    {Root: t.TempDir(), Branch: "hctl/test/dm"},
		},
		resolveFailures: map[string]error{"discord-guild": errors.New("guild worktree is invalid")},
	}
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ conversation, input string }{{"discord-guild", "guild-write"}, {"discord-dm", "dm-write"}} {
		assignment := provider.assignments[item.conversation]
		ref := conversationRef{agentID: p.AgentID, harness: "claude", id: item.conversation, fingerprint: p.SourceFingerprint}
		if _, _, err := store.assignWorkspaceAndAccept(ref, assignment.Root, assignment.Branch, item.input, "persisted writable turn"); err != nil {
			t.Fatal(err)
		}
	}

	driver := newManagerDriver()
	events := make(chan managedEvent, 64)
	manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, 2, 2, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	}, newFakeClock().NewTimer, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	for _, item := range []struct{ conversation, input string }{{"discord-guild", "guild-write"}, {"discord-dm", "dm-write"}} {
		result, err := manager.Submit(context.Background(), item.conversation, Submission{InputID: item.input, Text: "redelivery"})
		if err != nil || !result.Duplicate || result.Status != "queued" {
			t.Fatalf("start %s = %+v, %v", item.conversation, result, err)
		}
	}
	failure := waitManagedEvent(t, events, "driver.process_failed")
	if failure.conversation != "discord-guild" || failure.event.InputID != "guild-write" || failure.event.Status != "workspace_failure" {
		t.Fatalf("workspace failure event = %+v", failure)
	}
	driver.waitStarted(t, "dm-write")
	select {
	case <-manager.Done():
		t.Fatal("guild worktree failure stopped DM work")
	default:
	}
	driver.release("dm-write")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-dm": "dm-write"})
	if status := manager.Status("discord-guild"); status.State != LifecycleQueued || status.Pending != 1 {
		t.Fatalf("failed guild durable status = %+v", status)
	}
}

func TestManagerHibernatesAndResumesWritableConversationsIndependently(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			p := testProject(t)
			provider := &multiWorkspaceProvider{base: p, assignments: map[string]worktree.Assignment{
				"discord-guild": {Root: t.TempDir(), Branch: "hctl/test/guild"},
				"discord-dm":    {Root: t.TempDir(), Branch: "hctl/test/dm"},
			}}
			store, err := openConversationStore(p.WorkspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range []struct{ conversation, input string }{{"discord-guild", "guild-one"}, {"discord-dm", "dm-one"}} {
				assignment := provider.assignments[item.conversation]
				ref := conversationRef{agentID: p.AgentID, harness: harnessName, id: item.conversation, fingerprint: p.SourceFingerprint}
				if _, _, err := store.assignWorkspaceAndAccept(ref, assignment.Root, assignment.Branch, item.input, "persisted writable turn"); err != nil {
					t.Fatal(err)
				}
			}

			driver := newNamedManagerDriver(harnessName)
			clock := newFakeClock()
			events := make(chan managedEvent, 128)
			manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, 2, 2, func(conversation string, event Event) error {
				events <- managedEvent{conversation: conversation, event: event}
				return nil
			}, clock.NewTimer, provider)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			for _, item := range []struct{ conversation, input string }{{"discord-guild", "guild-one"}, {"discord-dm", "dm-one"}} {
				result, err := manager.Submit(context.Background(), item.conversation, Submission{InputID: item.input, Text: "redelivery"})
				if err != nil || !result.Duplicate || result.Status != "queued" {
					t.Fatalf("start durable %s = %+v, %v", item.conversation, result, err)
				}
			}
			started := map[string]bool{driver.waitAnyStarted(t): true, driver.waitAnyStarted(t): true}
			if !started["guild-one"] || !started["dm-one"] {
				t.Fatalf("initial writable turns = %v", started)
			}
			driver.release("guild-one")
			waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "guild-one"})
			guildIdle := clock.waitTimer(t)
			driver.release("dm-one")
			waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-dm": "dm-one"})
			_ = clock.waitTimer(t)

			guildIdle.Fire()
			hibernated := waitManagedEvent(t, events, "driver.process_hibernated")
			if hibernated.conversation != "discord-guild" {
				t.Fatalf("hibernated conversation = %+v", hibernated)
			}
			if status := manager.Status("discord-dm"); status.State != LifecycleIdle {
				t.Fatalf("DM changed when guild hibernated: %+v", status)
			}
			if _, err := manager.Submit(context.Background(), "discord-dm", Submission{InputID: "dm-two", Text: "continue DM"}); err != nil {
				t.Fatal(err)
			}
			driver.waitStarted(t, "dm-two")
			if got := driver.openCount(); got != 2 {
				t.Fatalf("DM reopened after guild hibernation: opens=%d", got)
			}
			driver.release("dm-two")
			waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-dm": "dm-two"})
			if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "guild-two", Text: "resume guild"}); err != nil {
				t.Fatal(err)
			}
			driver.waitStarted(t, "guild-two")
			if got := driver.openCount(); got != 3 {
				t.Fatalf("guild did not reopen independently: opens=%d", got)
			}
			if driver.rootForInput("guild-two") != provider.assignments["discord-guild"].Root {
				t.Fatalf("guild resumed in %q", driver.rootForInput("guild-two"))
			}
			driver.release("guild-two")
		})
	}
}

func TestManagerStopsRuntimeWhenDispatchEventDeliveryFails(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	deliveryErr := errors.New("event transport failed")
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(string, Event) error { return deliveryErr })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	result, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "hello"})
	if err != nil || result.Status != "queued" {
		t.Fatalf("submission = %+v, %v", result, err)
	}
	select {
	case <-manager.Done():
		if !errors.Is(manager.Err(), errDispatchEventDelivery) {
			t.Fatalf("manager error = %v", manager.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch event delivery failure did not stop runtime")
	}
}

func TestManagerReconcilesPersistedWorktreesConservativelyAtStartup(t *testing.T) {
	p := testProject(t)
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		name       string
		queue      []session.Input
		outcomes   map[string]string
		order      []string
		retiring   bool
		inspection worktree.Inspection
		inspectErr error
	}
	fixtures := []fixture{
		{name: "clean", inspection: worktree.Inspection{Clean: true, Merged: true, Reason: "clean and merged"}},
		{name: "active", queue: []session.Input{{ID: "active-input", Text: "active content", Status: "active"}}, inspection: worktree.Inspection{Clean: true, Merged: true, Reason: "clean and merged"}},
		{name: "queued", queue: []session.Input{{ID: "queued-input", Text: "queued content", Status: "queued"}}, inspection: worktree.Inspection{Clean: true, Merged: true, Reason: "clean and merged"}},
		{name: "uncertain", outcomes: map[string]string{"prior-input": "uncertain"}, order: []string{"prior-input"}, inspection: worktree.Inspection{Clean: true, Merged: true, Reason: "clean and merged"}},
		{name: "dirty", inspection: worktree.Inspection{Merged: true, Reason: "dirty or untracked work"}},
		{name: "unmerged", inspection: worktree.Inspection{Clean: true, Reason: "unmerged commits"}},
		{name: "unverifiable", inspectErr: errors.New("worktree is missing")},
		{name: "stale-source", inspectErr: errors.New("worktree source fingerprint changed")},
		{name: "partial", retiring: true},
	}
	provider := &reconcilingWorkspaceProvider{
		base: p, assignments: map[string]worktree.Assignment{}, inspections: map[string]worktree.Inspection{}, inspectErrs: map[string]error{},
		retireErrs: map[string]error{"partial": errors.New("branch deletion interrupted")},
	}
	for _, item := range fixtures {
		fingerprint := p.SourceFingerprint
		if item.name == "stale-source" {
			fingerprint = "older-source-fingerprint"
		}
		conversation, err := state.GetOrCreate(p.AgentID, "claude", item.name, fingerprint)
		if err != nil {
			t.Fatal(err)
		}
		assignment := worktree.Assignment{Root: filepath.Join(t.TempDir(), item.name), Branch: "hctl/test/" + item.name}
		conversation.WorkspaceRoot = assignment.Root
		conversation.WorktreeBranch = assignment.Branch
		conversation.WorktreeRetiring = item.retiring
		conversation.Queue = item.queue
		if item.outcomes != nil {
			conversation.Outcomes = item.outcomes
			conversation.OutcomeOrder = item.order
		}
		provider.assignments[item.name] = assignment
		provider.inspections[item.name] = item.inspection
		provider.inspectErrs[item.name] = item.inspectErr
	}
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManagerWithWorkspace(context.Background(), p, newManagerDriver(), time.Minute, time.Hour, func(string, Event) error { return nil }, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), "partial", Submission{InputID: "blocked", Text: "must not run"}); err == nil || !strings.Contains(err.Error(), "local recovery") {
		t.Fatalf("submission during partial retirement = %v", err)
	}
	if err := manager.Reset("partial"); err == nil || !strings.Contains(err.Error(), "local recovery") {
		t.Fatalf("reset during partial retirement = %v", err)
	}
	manager.Close()
	provider.mu.Lock()
	inspected := append([]string(nil), provider.inspected...)
	retired := append([]string(nil), provider.retired...)
	provider.mu.Unlock()
	sort.Strings(inspected)
	wantInspected := []string{"active", "clean", "dirty", "queued", "stale-source", "uncertain", "unmerged", "unverifiable"}
	if !reflect.DeepEqual(inspected, wantInspected) {
		t.Fatalf("startup inspections = %v, want %v", inspected, wantInspected)
	}
	sort.Strings(retired)
	if !reflect.DeepEqual(retired, []string{"clean", "partial"}) {
		t.Fatalf("startup retirements = %v", retired)
	}
	diagnostics := strings.Join(manager.Diagnostics(), "\n")
	for _, reason := range []string{"active or queued durable work", "uncertain recovered work", "dirty or untracked work", "unmerged commits", "ownership could not be verified", "interrupted cleanup"} {
		if !strings.Contains(diagnostics, reason) {
			t.Fatalf("diagnostics missing %q:\n%s", reason, diagnostics)
		}
	}
	for _, forbidden := range []string{"active content", "queued content", "HCTL_DISCORD_TOKEN"} {
		if strings.Contains(diagnostics, forbidden) {
			t.Fatalf("diagnostics exposed %q: %s", forbidden, diagnostics)
		}
	}

	persisted, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if clean := findConversation(persisted, "clean"); clean == nil || clean.WorkspaceRoot != "" || clean.WorktreeRetiring {
		t.Fatalf("clean retirement state = %#v", clean)
	}
	partial := findConversation(persisted, "partial")
	if partial == nil || partial.WorkspaceRoot == "" || !partial.WorktreeRetiring {
		t.Fatalf("partial retirement evidence = %#v", partial)
	}
	for _, name := range []string{"active", "queued", "uncertain", "dirty", "unmerged", "unverifiable", "stale-source"} {
		conversation := findConversation(persisted, name)
		if conversation == nil || conversation.WorkspaceRoot == "" || conversation.WorktreeRetiring {
			t.Fatalf("preserved %s state = %#v", name, conversation)
		}
	}

	retryProvider := &reconcilingWorkspaceProvider{base: p, assignments: provider.assignments, inspections: provider.inspections, inspectErrs: provider.inspectErrs, retireErrs: map[string]error{}}
	retried, err := NewManagerWithWorkspace(context.Background(), p, newManagerDriver(), time.Minute, time.Hour, func(string, Event) error { return nil }, retryProvider)
	if err != nil {
		t.Fatal(err)
	}
	retried.Close()
	persisted, err = session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	partial = findConversation(persisted, "partial")
	if partial == nil || partial.WorkspaceRoot != "" || partial.WorktreeRetiring {
		t.Fatalf("retried retirement state = %#v", partial)
	}
}

func TestManagerPreservedAssignmentResumesWithoutProvisioning(t *testing.T) {
	p := testProject(t)
	assignment := worktree.Assignment{Root: t.TempDir(), Branch: "hctl/test/preserved"}
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := state.GetOrCreate(p.AgentID, "claude", "preserved", p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	conversation.WorkspaceRoot = assignment.Root
	conversation.WorktreeBranch = assignment.Branch
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}
	provider := &reconcilingWorkspaceProvider{
		base: p, assignments: map[string]worktree.Assignment{"preserved": assignment},
		inspections: map[string]worktree.Inspection{"preserved": {Merged: true, Reason: "dirty or untracked work"}}, inspectErrs: map[string]error{}, retireErrs: map[string]error{},
	}
	driver := newManagerDriver()
	manager, err := NewManagerWithWorkspace(context.Background(), p, driver, time.Minute, time.Hour, func(string, Event) error { return nil }, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	if _, err := manager.Submit(context.Background(), "preserved", Submission{InputID: "next", Text: "resume"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "next")
	provider.mu.Lock()
	provisions, resolves := provider.provisions, provider.resolves
	provider.mu.Unlock()
	if provisions != 0 || resolves != 1 || driver.rootForInput("next") != assignment.Root {
		t.Fatalf("preserved resume provisions=%d resolves=%d root=%q", provisions, resolves, driver.rootForInput("next"))
	}
	driver.release("next")
}

func TestManagerRefusesToSkipPersistedWorktreeReconciliation(t *testing.T) {
	p := testProject(t)
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := state.GetOrCreate(p.AgentID, "claude", "persisted", p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	conversation.WorkspaceRoot = t.TempDir()
	conversation.WorktreeBranch = "hctl/test/persisted"
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(context.Background(), p, newManagerDriver(), time.Minute, func(string, Event) error { return nil }); err == nil || !strings.Contains(err.Error(), "Git ownership recovery") {
		t.Fatalf("manager without reconciliation provider = %v", err)
	}
}

func TestManagerResetRequiresIdleConversation(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 16)
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	if err := manager.Reset("discord-guild"); err == nil {
		t.Fatal("active conversation reset succeeded")
	}
	driver.release("message-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{
		"discord-guild": "message-1",
	})
	if err := manager.Reset("discord-guild"); err != nil {
		t.Fatal(err)
	}
	if status := manager.Status("discord-guild"); status.State != LifecycleInactive || status.Pending != 0 {
		t.Fatalf("reset status = %+v", status)
	}

	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if conversation := findConversation(state, "discord-guild"); conversation != nil {
		t.Fatalf("reset durable conversation = %#v", conversation)
	}
}

func TestManagerRecoveryDoesNotConsumeNewPendingInput(t *testing.T) {
	p := testProject(t)
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := state.GetOrCreate(p.AgentID, "claude", "discord-guild", p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Accept("message-old", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.StartNext(); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}

	driver := newManagerDriver()
	events := make(chan managedEvent, 32)
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	result, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-new", Text: "new"})
	if err != nil || result.Status != "queued" {
		t.Fatalf("submit = %+v, %v", result, err)
	}
	driver.waitStarted(t, "message-new")
	waitManagedEvents(t, events, "turn.started", map[string]string{"discord-guild": "message-new"})
	status := manager.Status("discord-guild")
	if status.State != LifecycleActive || status.Pending != 1 {
		t.Fatalf("recovered status = %+v", status)
	}
	driver.release("message-new")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-new"})
}

func TestManagerColdDurableWorkIsBusyAndVisible(t *testing.T) {
	p := testProject(t)
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := state.GetOrCreate(p.AgentID, "claude", "discord-guild", p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Accept("message-old", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.StartNext(); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}

	manager, err := NewManager(context.Background(), p, newManagerDriver(), time.Minute, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if status := manager.Status("discord-guild"); status.State != LifecycleActive || status.Pending != 1 {
		t.Fatalf("cold durable status = %+v", status)
	}
	if err := manager.Reset("discord-guild"); !errors.Is(err, ErrConversationBusy) {
		t.Fatalf("cold durable reset = %v, want busy", err)
	}

	persisted, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	got := findConversation(persisted, "discord-guild")
	if got == nil || len(got.Queue) != 1 || got.Queue[0].ID != "message-old" || got.Queue[0].Status != "active" {
		t.Fatalf("cold durable work was changed: %#v", got)
	}
}

func TestManagerStatusDoesNotCreateUnknownConversation(t *testing.T) {
	p := testProject(t)
	manager, err := NewManager(context.Background(), p, newManagerDriver(), time.Minute, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	for check := 0; check < 2; check++ {
		if status := manager.Status("discord-never-used"); status.State != LifecycleInactive || status.Pending != 0 {
			t.Fatalf("unknown status check %d = %+v", check+1, status)
		}
	}
}

func TestManagerColdResetPreservesLegacyStateSemantics(t *testing.T) {
	for _, test := range []struct {
		name         string
		conversation string
		remaining    int
	}{
		{name: "matching conversation", conversation: "discord-guild", remaining: 0},
		{name: "unrelated conversation", conversation: "discord-dm", remaining: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := testProject(t)
			stateDir := filepath.Join(p.WorkspaceRoot, ".hctl")
			if err := os.MkdirAll(stateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			legacy := fmt.Sprintf(`{"schema_version":1,"conversations":{"claude:discord-guild":{"id":"discord-guild","harness":"claude","source_fingerprint":%q,"queue":[],"outcomes":{},"outcome_order":[]}}}`, p.SourceFingerprint)
			if err := os.WriteFile(filepath.Join(stateDir, "dispatch.json"), []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}

			manager, err := NewManager(context.Background(), p, newManagerDriver(), time.Minute, func(string, Event) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			if err := manager.Reset(test.conversation); err != nil {
				t.Fatal(err)
			}

			state, err := session.Load(p.WorkspaceRoot)
			if err != nil {
				t.Fatalf("load after cold legacy reset: %v", err)
			}
			if len(state.Conversations) != test.remaining {
				t.Fatalf("remaining conversations = %d, want %d", len(state.Conversations), test.remaining)
			}
		})
	}
}

func TestManagerHibernatesAndResumesIdleHarnesses(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			p := testProject(t)
			driver := newNamedManagerDriver(harnessName)
			clock := newFakeClock()
			events := make(chan managedEvent, 32)
			manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, DefaultMaxResidentSessions, DefaultMaxActiveTurns, func(conversation string, event Event) error {
				events <- managedEvent{conversation: conversation, event: event}
				return nil
			}, clock.NewTimer, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)

			if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"}); err != nil {
				t.Fatal(err)
			}
			driver.waitStarted(t, "message-1")
			driver.release("message-1")
			waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-1"})
			timer := clock.waitTimer(t)
			timer.Fire()
			waitManagedEvents(t, events, "driver.process_hibernated", map[string]string{"discord-guild": ""})

			if status := manager.Status("discord-guild"); status.State != LifecycleHibernated || status.Pending != 0 {
				t.Fatalf("hibernated status = %+v", status)
			}
			if got := driver.closeCount(); got != 1 {
				t.Fatalf("closed processes = %d, want 1", got)
			}
			duplicate, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"})
			if err != nil || !duplicate.Duplicate || duplicate.Status != "completed" {
				t.Fatalf("hibernated duplicate = %+v, %v", duplicate, err)
			}
			if got := driver.openCount(); got != 1 {
				t.Fatalf("duplicate reopened %d processes", got)
			}

			if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-2", Text: "second"}); err != nil {
				t.Fatal(err)
			}
			driver.waitStarted(t, "message-2")
			if got := driver.resumeIDs(); !reflect.DeepEqual(got, []string{"", "session-1"}) {
				t.Fatalf("resume IDs = %v", got)
			}
			if got := driver.executionPolicies(); !reflect.DeepEqual(got, []harness.ExecutionPolicy{harness.PolicyReadOnly, harness.PolicyReadOnly}) {
				t.Fatalf("execution policies = %v", got)
			}
			driver.release("message-2")
			waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-2"})
		})
	}
}

func TestManagerElevatesOnceAndReusesDurableWritableWorkspace(t *testing.T) {
	p := testProject(t)
	elevatedRoot := t.TempDir()
	provider := &fakeWorkspaceProvider{project: projectAtWorkspace(p, elevatedRoot), assignment: worktree.Assignment{Root: elevatedRoot, Branch: "hctl/test/conversation"}}
	driver := newManagerDriver()
	clock := newFakeClock()
	events := make(chan managedEvent, 64)
	manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, DefaultMaxResidentSessions, DefaultMaxActiveTurns, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	}, clock.NewTimer, provider)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "change it"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	driver.release("message-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-1"})
	result, err := manager.Elevate(context.Background(), "discord-guild", Submission{InputID: "message-1:write", Text: "continue"})
	if err != nil || result.Status != "queued" {
		t.Fatalf("elevation = %+v, %v", result, err)
	}
	atomicState, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	atomicConversation := findConversation(atomicState, "discord-guild")
	if atomicConversation == nil || atomicConversation.WorkspaceRoot != elevatedRoot || len(atomicConversation.Queue) != 1 || atomicConversation.Queue[0].ID != "message-1:write" {
		t.Fatalf("workspace and continuation were not persisted together: %#v", atomicConversation)
	}
	driver.waitStarted(t, "message-1:write")
	if provider.provisions != 1 {
		t.Fatalf("workspace provisions = %d", provider.provisions)
	}
	if got := driver.openRoots(); !reflect.DeepEqual(got, []string{p.WorkspaceRoot, elevatedRoot}) {
		t.Fatalf("opened roots = %v", got)
	}
	if got := driver.executionPolicies(); !reflect.DeepEqual(got, []harness.ExecutionPolicy{harness.PolicyReadOnly, harness.PolicyWorkspaceWrite}) {
		t.Fatalf("execution policies = %v", got)
	}
	if got := driver.resumeIDs(); !reflect.DeepEqual(got, []string{"", "session-1"}) {
		t.Fatalf("resume IDs = %v", got)
	}
	driver.release("message-1:write")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-1:write"})
	if _, err := manager.Submit(context.Background(), "discord-dm", Submission{InputID: "message-other", Text: "inspect"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-other")
	if got := driver.openRoots(); !reflect.DeepEqual(got, []string{p.WorkspaceRoot, elevatedRoot, p.WorkspaceRoot}) {
		t.Fatalf("roots after unrelated conversation = %v", got)
	}
	if got := driver.executionPolicies(); !reflect.DeepEqual(got, []harness.ExecutionPolicy{harness.PolicyReadOnly, harness.PolicyWorkspaceWrite, harness.PolicyReadOnly}) {
		t.Fatalf("policies after unrelated conversation = %v", got)
	}
	driver.release("message-other")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-dm": "message-other"})
	manager.Close()

	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation := findConversation(state, "discord-guild")
	if conversation == nil || conversation.WorkspaceRoot != elevatedRoot || conversation.WorktreeBranch != provider.assignment.Branch {
		t.Fatalf("durable writable assignment = %#v", conversation)
	}

	restartedDriver := newManagerDriver()
	restartedEvents := make(chan managedEvent, 16)
	restarted, err := newManager(context.Background(), p, restartedDriver, time.Minute, time.Hour, DefaultMaxResidentSessions, DefaultMaxActiveTurns, func(conversation string, event Event) error {
		restartedEvents <- managedEvent{conversation: conversation, event: event}
		return nil
	}, newFakeClock().NewTimer, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if _, err := restarted.Submit(context.Background(), "discord-guild", Submission{InputID: "message-2", Text: "next"}); err != nil {
		t.Fatal(err)
	}
	restartedDriver.waitStarted(t, "message-2")
	if got := restartedDriver.openRoots(); !reflect.DeepEqual(got, []string{elevatedRoot}) {
		t.Fatalf("restart roots = %v", got)
	}
	if got := restartedDriver.executionPolicies(); !reflect.DeepEqual(got, []harness.ExecutionPolicy{harness.PolicyWorkspaceWrite}) {
		t.Fatalf("restart policies = %v", got)
	}
	restartedDriver.release("message-2")
	waitManagedEvents(t, restartedEvents, "turn.completed", map[string]string{"discord-guild": "message-2"})
}

func TestManagerElevationFailurePreservesReadOnlyConversation(t *testing.T) {
	p := testProject(t)
	provider := &fakeWorkspaceProvider{provisionErr: errors.New("cannot prepare isolated workspace")}
	driver := newManagerDriver()
	events := make(chan managedEvent, 32)
	manager, err := NewManagerWithWorkspace(context.Background(), p, driver, time.Minute, time.Hour, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	}, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "change it"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	driver.release("message-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-1"})
	if _, err := manager.Elevate(context.Background(), "discord-guild", Submission{InputID: "message-1:write", Text: "continue"}); !errors.Is(err, provider.provisionErr) {
		t.Fatalf("elevation failure = %v", err)
	}

	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation := findConversation(state, "discord-guild")
	if conversation == nil || conversation.WorkspaceRoot != "" || conversation.WorktreeBranch != "" || conversation.Outcomes["message-1"] != "completed" {
		t.Fatalf("state after failed elevation = %#v", conversation)
	}
	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-2", Text: "inspect instead"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-2")
	if got := driver.executionPolicies(); !reflect.DeepEqual(got, []harness.ExecutionPolicy{harness.PolicyReadOnly, harness.PolicyReadOnly}) {
		t.Fatalf("policies after failed elevation = %v", got)
	}
	driver.release("message-2")
}

func TestManagerDoesNotHibernateActiveOrQueuedWork(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	clock := newFakeClock()
	manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, DefaultMaxResidentSessions, DefaultMaxActiveTurns, func(string, Event) error { return nil }, clock.NewTimer, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	for _, id := range []string{"message-1", "message-2"} {
		if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: id, Text: id}); err != nil {
			t.Fatal(err)
		}
	}
	driver.waitStarted(t, "message-1")
	clock.assertNoTimer(t)
	driver.release("message-1")
	driver.waitStarted(t, "message-2")
	clock.assertNoTimer(t)
	driver.release("message-2")
	clock.waitTimer(t)
}

func TestManagerClassifiesHibernateCloseFailureWithoutLosingConversation(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	driver.closeErr = errors.New("close failed")
	clock := newFakeClock()
	events := make(chan managedEvent, 32)
	manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, DefaultMaxResidentSessions, DefaultMaxActiveTurns, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	}, clock.NewTimer, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	driver.release("message-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-1"})
	clock.waitTimer(t).Fire()
	failure := waitManagedEvent(t, events, "driver.process_failed")
	if failure.event.Status != "hibernate_failure" || failure.event.InputID != "" {
		t.Fatalf("hibernate failure event = %+v", failure.event)
	}
	select {
	case <-manager.Done():
		t.Fatal("one conversation's hibernate failure stopped the manager")
	default:
	}
	if manager.Err() != nil {
		t.Fatalf("manager error = %v", manager.Err())
	}

	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation := findConversation(state, "discord-guild")
	if conversation == nil || conversation.SessionID != "session-1" || len(conversation.Queue) != 0 || conversation.Outcomes["message-1"] != "completed" {
		t.Fatalf("durable conversation after hibernate failure = %#v", conversation)
	}
}

func TestManagerClassifiesReadOnlyPolicyStartupFailure(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	driver.openErr = errors.New("read-only policy unsupported")
	events := make(chan managedEvent, 16)
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	result, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "change it"})
	if err != nil || result.Status != "queued" {
		t.Fatalf("submission = %+v, %v", result, err)
	}
	failure := waitManagedEvent(t, events, "driver.process_failed")
	if failure.event.Status != "startup_failure" || failure.event.InputID != "message-1" {
		t.Fatalf("policy failure event = %+v", failure.event)
	}
	if got := driver.executionPolicies(); !reflect.DeepEqual(got, []harness.ExecutionPolicy{harness.PolicyReadOnly}) {
		t.Fatalf("execution policies = %v", got)
	}
	select {
	case <-manager.Done():
		t.Fatal("one conversation's startup failure stopped the manager")
	default:
	}
	if capacity := manager.Capacity(); capacity.Active != 0 || capacity.Resident != 0 {
		t.Fatalf("startup failure leaked capacity: %+v", capacity)
	}
}

func TestManagerBoundsBlockedHibernateClose(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	driver.blockProcessClose()
	clock := newFakeClock()
	events := make(chan managedEvent, 32)
	manager, err := newManager(context.Background(), p, driver, time.Minute, time.Hour, DefaultMaxResidentSessions, DefaultMaxActiveTurns, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	}, clock.NewTimer, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	driver.release("message-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-1"})
	clock.waitTimer(t).Fire()
	clock.waitTimer(t).Fire()
	failure := waitManagedEvent(t, events, "driver.process_failed")
	if failure.event.Status != "hibernate_failure" {
		t.Fatalf("blocked close event = %+v", failure.event)
	}
	if got := driver.abortCount(); got != 1 {
		t.Fatalf("abort count = %d, want 1", got)
	}
}

func TestManagedRunBoundsBlockedCloseAfterAdmissionsStop(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	driver.blockProcessClose()
	clock := newFakeClock()
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	submissions := make(chan Submission, 1)
	events := make(chan Event, 32)
	done := make(chan error, 1)
	go func() {
		done <- runSubmissions(context.Background(), p, driver, "discord-guild", submissions, func(event Event) error {
			events <- event
			return nil
		}, runOptions{turnTimeout: time.Minute, idleTimeout: time.Hour, timers: clock.NewTimer, policy: harness.PolicyReadOnly, store: store})
	}()
	reply := make(chan SubmissionResult, 1)
	submissions <- Submission{InputID: "message-1", Text: "first", Reply: reply}
	if result := <-reply; result.Status != "queued" {
		t.Fatalf("submission = %+v", result)
	}
	driver.waitStarted(t, "message-1")
	driver.release("message-1")
	waitDispatchEvent(t, events, "turn.completed")
	close(submissions)
	clock.waitTimerAfter(t, harnessCloseTimeout).Fire()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "hibernation deadline") {
			t.Fatalf("managed close error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("managed run did not bound blocked close")
	}
	if got := driver.abortCount(); got != 1 {
		t.Fatalf("abort count = %d, want 1", got)
	}
}

func TestManagerDeduplicatesWithoutInflatingStatus(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 32)
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)

	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	duplicate, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"})
	if err != nil || !duplicate.Duplicate || duplicate.Status != "active" {
		t.Fatalf("active duplicate = %+v, %v", duplicate, err)
	}
	if status := manager.Status("discord-guild"); status.Pending != 1 || status.State != LifecycleActive {
		t.Fatalf("duplicate status = %+v", status)
	}
	driver.release("message-1")
	waitManagedEvents(t, events, "turn.completed", map[string]string{"discord-guild": "message-1"})

	duplicate, err = manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "first"})
	if err != nil || !duplicate.Duplicate || duplicate.Status != "completed" {
		t.Fatalf("completed duplicate = %+v, %v", duplicate, err)
	}
	if status := manager.Status("discord-guild"); status.Pending != 0 || status.State != LifecycleIdle {
		t.Fatalf("completed duplicate status = %+v", status)
	}
	if got := driver.openCount(); got != 1 {
		t.Fatalf("opened harness processes = %d, want one", got)
	}
}

func TestManagerAdmissionDoesNotBlockLifecycleStatusAtCapacity(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 48)
	for index := 0; index < cap(results); index++ {
		go func(index int) {
			_, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: fmt.Sprintf("message-%d", index), Text: "queued"})
			results <- err
		}(index)
	}
	driver.waitAnyStarted(t)

	statusDone := make(chan ConversationStatus, 1)
	go func() { statusDone <- manager.Status("discord-guild") }()
	select {
	case status := <-statusDone:
		if status.State != LifecycleActive && status.State != LifecycleQueued {
			t.Fatalf("saturated status = %+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("status blocked behind saturated admission")
	}

	manager.Close()
	for index := 0; index < cap(results); index++ {
		select {
		case <-results:
		case <-time.After(2 * time.Second):
			t.Fatal("saturated submission did not terminate during shutdown")
		}
	}
}

func TestManagerTurnTimeoutAndShutdownRemainBounded(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	events := make(chan managedEvent, 16)
	manager, err := NewManager(context.Background(), p, driver, 10*time.Millisecond, func(conversation string, event Event) error {
		events <- managedEvent{conversation: conversation, event: event}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "slow"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	failure := waitManagedEvent(t, events, "driver.process_failed")
	if failure.conversation != "discord-guild" || failure.event.InputID != "message-1" {
		t.Fatalf("timeout failure = %+v", failure)
	}
	select {
	case <-manager.Done():
		t.Fatal("one conversation's turn timeout stopped the manager")
	default:
	}
	manager.Close()
}

type managedEvent struct {
	conversation string
	event        Event
}

func TestManagerConfiguresManagedInputBeforeAdmission(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	manager, err := NewManager(context.Background(), p, driver, time.Minute, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	if err := manager.ConfigureRequestInput(func(string) RequestInputHandler {
		return testRequestInputHandler(&recordingInteractionRequester{})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "origin"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	if !driver.lastManagedRequestInput() {
		t.Fatal("configured channel worker did not enable managed input")
	}
	if err := manager.ConfigureRequestInput(func(string) RequestInputHandler { return nil }); err == nil {
		t.Fatal("managed input was reconfigured after admission")
	}
	driver.release("message-1")
}

func TestManagerOwnsCodexContinuationCapacityAndCommitsBeforeTerminalEvent(t *testing.T) {
	p := testProject(t)
	driver := newContinuationManagerDriver()
	events := make(chan Event, 32)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(_ string, event Event) error {
		events <- event
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	ref := manager.reference("discord-guild")
	if _, _, err := manager.store.accept(ref, "message-1", "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.store.startNext(ref); err != nil {
		t.Fatal(err)
	}
	if err := manager.store.setSessionID(ref, "thread-1"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	coordinator, err := manager.NewInteractionCoordinator("discord-guild", dispatchRendererFunc(func(context.Context, interaction.RenderIntent) interaction.EffectOutcome {
		return interaction.EffectSucceeded
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	pending := storeTestLifecycle("c")
	pending.InputID = "message-1"
	pending.ExpiresAt = now.Add(time.Hour)
	if err := coordinator.Request(interaction.OpenRequest{
		InteractionID: pending.ID, InputID: pending.InputID, Owner: pending.Owner,
		Request: pending.Request, Resolution: pending.Resolution, Continuation: interaction.ContinuationTurn,
	}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), pending.ID); err != nil {
		t.Fatal(err)
	}
	confirmed := true
	if _, err := coordinator.AcceptAnswer(interaction.AnswerAttempt{InteractionID: pending.ID, Owner: pending.Owner, Answer: interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ScheduleInteractionResume("discord-guild"); err != nil {
		t.Fatal(err)
	}
	driver.waitContinuation(t)
	if status := manager.Status("discord-guild"); status.State != LifecycleActive {
		t.Fatalf("status = %#v", status)
	}
	if capacity := manager.Capacity(); capacity.Active != 1 || capacity.Resident != 1 {
		t.Fatalf("capacity = %#v", capacity)
	}
	if err := coordinator.Resume(context.Background()); !errors.Is(err, interaction.ErrInteractionMissing) {
		t.Fatalf("duplicate continuation = %v", err)
	}
	driver.releaseContinuation()
	terminal := waitDispatchEvent(t, events, "turn.completed")
	state, err := manager.store.loadInteraction(ref)
	if err != nil || state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].Phase != interaction.PhaseCompleted {
		t.Fatalf("state = %#v, %v", state, err)
	}
	if terminal.Status != "continuation_turn" || terminal.SessionID != "thread-1" || terminal.TurnID != "answer-turn" {
		t.Fatalf("terminal = %#v", terminal)
	}
	if capacity := manager.Capacity(); capacity.Active != 0 || capacity.Resident != 0 {
		t.Fatalf("released capacity = %#v", capacity)
	}
}

func TestManagerRestartClaimsDurableAnsweredContinuationOnce(t *testing.T) {
	p := testProject(t)
	ref := conversationRef{agentID: p.AgentID, harness: "codex", id: "discord-guild", fingerprint: p.SourceFingerprint}
	store, pending := prepareDurableAnsweredInteraction(t, p, ref)
	_ = store
	driver := newContinuationManagerDriver()
	events := make(chan Event, 16)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(_ string, event Event) error {
		events <- event
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	driver.waitContinuation(t)
	continuation, err := manager.InteractionContinuation("discord-guild")
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := interaction.NewCoordinator(manager.store.interactionStore(ref), dispatchRendererFunc(func(context.Context, interaction.RenderIntent) interaction.EffectOutcome {
		return interaction.EffectFailed
	}), continuation, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Resume(context.Background()); !errors.Is(err, interaction.ErrInteractionMissing) {
		t.Fatalf("concurrent duplicate resume = %v", err)
	}
	driver.releaseContinuation()
	_ = waitDispatchEvent(t, events, "turn.completed")
	state, err := manager.store.loadInteraction(ref)
	if err != nil || state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].InteractionDigest != interaction.Digest(pending.ID) {
		t.Fatalf("state = %#v, %v", state, err)
	}
	select {
	case <-driver.started:
		t.Fatal("durable continuation was started twice")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestManagerRestartClaimsDurableNativeDeferredContinuationOnce(t *testing.T) {
	p := testProject(t)
	ref := conversationRef{agentID: p.AgentID, harness: "claude", id: "discord-guild", fingerprint: p.SourceFingerprint}
	store, pending := prepareDurableAnsweredInteraction(t, p, ref)
	if err := store.updateInteraction(ref, pending.ID, func(current *interaction.Lifecycle) error {
		current.Continuation = interaction.ContinuationNativeDeferredTool
		current.ContinuationKey = "toolu_restart_exact"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := newNativeDeferredManagerDriver()
	events := make(chan Event, 16)
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(_ string, event Event) error {
		events <- event
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	driver.waitContinuation(t)
	if capacity := manager.Capacity(); capacity.Active != 1 || capacity.Resident != 1 {
		t.Fatalf("native deferred capacity = %#v", capacity)
	}
	driver.releaseContinuation()
	terminal := waitDispatchEvent(t, events, "turn.completed")
	if terminal.Status != string(interaction.ContinuationNativeDeferredTool) || terminal.SessionID != "thread-1" || terminal.TurnID != "message-1" {
		t.Fatalf("terminal = %#v", terminal)
	}
	state, err := manager.store.loadInteraction(ref)
	if err != nil || state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].InteractionDigest != interaction.Digest(pending.ID) {
		t.Fatalf("state = %#v, %v", state, err)
	}
	select {
	case <-driver.started:
		t.Fatal("durable native deferred continuation was started twice")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestContinuationDeltaDeliveryFailureStopsRuntime(t *testing.T) {
	p := testProject(t)
	ref := conversationRef{agentID: p.AgentID, harness: "codex", id: "discord-guild", fingerprint: p.SourceFingerprint}
	_, pending := prepareDurableAnsweredInteraction(t, p, ref)
	driver := newContinuationManagerDriver()
	deliveryErr := errors.New("continuation delta transport failed")
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(_ string, event Event) error {
		if event.Type == "agent.output.delta" {
			return deliveryErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	driver.waitContinuation(t)
	driver.releaseContinuation()
	assertContinuationDeliveryFatal(t, manager, deliveryErr)
	deadline := time.Now().Add(2 * time.Second)
	for {
		state, loadErr := manager.store.loadInteraction(ref)
		if loadErr == nil && state.Pending != nil && state.Pending.ID == pending.ID && state.Pending.Resume == interaction.ResumeUncertain {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delta failure lifecycle = %#v, %v", state, loadErr)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestContinuationDeltaDeliveryFailureCancelsBeforeDriverReturns(t *testing.T) {
	p := testProject(t)
	ref := conversationRef{agentID: p.AgentID, harness: "codex", id: "discord-guild", fingerprint: p.SourceFingerprint}
	_, _ = prepareDurableAnsweredInteraction(t, p, ref)
	driver := newBlockedAfterEmitContinuationDriver()
	deliveryErr := errors.New("continuation callback transport failed")
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(_ string, event Event) error {
		if event.Type == "agent.output.delta" {
			return deliveryErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		driver.releaseReturn()
		manager.Close()
	})
	select {
	case <-driver.callbackReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("continuation did not emit a delta")
	}
	select {
	case <-driver.cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("manager-fatal transition did not cancel the active continuation")
	}
	select {
	case <-manager.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not stop before continuation driver return")
	}
	select {
	case <-driver.returned:
		t.Fatal("continuation driver returned before immediate fatal state was observed")
	default:
	}
	if !errors.Is(manager.Err(), errDispatchEventDelivery) || !errors.Is(manager.Err(), deliveryErr) {
		t.Fatalf("manager error = %v", manager.Err())
	}
	if _, err := manager.Submit(context.Background(), "discord-dm", Submission{InputID: "message-other", Text: "must reject"}); !errors.Is(err, errDispatchEventDelivery) {
		t.Fatalf("post-failure admission = %v", err)
	}
	driver.releaseReturn()
	select {
	case <-driver.returned:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled continuation did not unwind")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		capacity := manager.Capacity()
		if capacity.Active == 0 && capacity.Resident == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled continuation capacity = %#v", capacity)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestContinuationTerminalDeliveryFailureStopsRuntimeAfterCommit(t *testing.T) {
	p := testProject(t)
	ref := conversationRef{agentID: p.AgentID, harness: "codex", id: "discord-guild", fingerprint: p.SourceFingerprint}
	_, pending := prepareDurableAnsweredInteraction(t, p, ref)
	driver := newContinuationManagerDriver()
	deliveryErr := errors.New("continuation terminal transport failed")
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(_ string, event Event) error {
		if event.Type == "turn.completed" {
			return deliveryErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	driver.waitContinuation(t)
	driver.releaseContinuation()
	assertContinuationDeliveryFatal(t, manager, deliveryErr)
	state, err := manager.store.loadInteraction(ref)
	if err != nil || state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].InteractionDigest != interaction.Digest(pending.ID) || state.Tombstones[0].Phase != interaction.PhaseCompleted {
		t.Fatalf("terminal failure commit = %#v, %v", state, err)
	}
}

func assertContinuationDeliveryFatal(t *testing.T, manager *Manager, deliveryErr error) {
	t.Helper()
	select {
	case <-manager.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("continuation delivery failure did not stop runtime")
	}
	if !errors.Is(manager.Err(), errDispatchEventDelivery) || !errors.Is(manager.Err(), deliveryErr) {
		t.Fatalf("manager error = %v", manager.Err())
	}
	if _, err := manager.Submit(context.Background(), "discord-dm", Submission{InputID: "message-other", Text: "must reject"}); !errors.Is(err, errDispatchEventDelivery) {
		t.Fatalf("post-failure admission = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		capacity := manager.Capacity()
		if capacity.Active == 0 && capacity.Resident == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("post-failure capacity = %#v", capacity)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerRestartMarksClaimedContinuationUncertainWithoutRetry(t *testing.T) {
	p := testProject(t)
	ref := conversationRef{agentID: p.AgentID, harness: "codex", id: "discord-guild", fingerprint: p.SourceFingerprint}
	store, pending := prepareDurableAnsweredInteraction(t, p, ref)
	if err := store.updateInteraction(ref, pending.ID, func(current *interaction.Lifecycle) error {
		current.Phase = interaction.PhaseResuming
		current.Resume = interaction.ResumeIntended
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	driver := newContinuationManagerDriver()
	manager, err := NewManagerWithLimits(context.Background(), p, driver, time.Minute, time.Hour, 1, 1, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	select {
	case <-driver.started:
		t.Fatal("ambiguous continuation was retried")
	case <-time.After(25 * time.Millisecond):
	}
	state, err := manager.store.loadInteraction(ref)
	if err != nil || state.Pending == nil || state.Pending.Phase != interaction.PhaseResuming || state.Pending.Resume != interaction.ResumeUncertain {
		t.Fatalf("state = %#v, %v", state, err)
	}
}

func prepareDurableAnsweredInteraction(t *testing.T, p *project.Project, ref conversationRef) (*conversationStore, *interaction.Lifecycle) {
	t.Helper()
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.accept(ref, "message-1", "origin"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.startNext(ref); err != nil {
		t.Fatal(err)
	}
	if err := store.setSessionID(ref, "thread-1"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	pending := storeTestLifecycle("r")
	pending.InputID = "message-1"
	pending.ExpiresAt = now.Add(time.Hour)
	coordinator, err := interaction.NewCoordinator(store.interactionStore(ref), dispatchRendererFunc(func(context.Context, interaction.RenderIntent) interaction.EffectOutcome {
		return interaction.EffectSucceeded
	}), dispatchContinuationFunc(func(context.Context, interaction.ContinuationIntent) interaction.ContinuationResult {
		return interaction.ContinuationResult{Effect: interaction.EffectFailed}
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Request(interaction.OpenRequest{InteractionID: pending.ID, InputID: pending.InputID, Owner: pending.Owner, Request: pending.Request, Resolution: pending.Resolution, Continuation: interaction.ContinuationTurn}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), pending.ID); err != nil {
		t.Fatal(err)
	}
	confirmed := true
	if _, err := coordinator.AcceptAnswer(interaction.AnswerAttempt{InteractionID: pending.ID, Owner: pending.Owner, Answer: interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}}}); err != nil {
		t.Fatal(err)
	}
	return store, pending
}

type continuationManagerDriver struct {
	*managerDriver
	started chan struct{}
	release chan struct{}
}

func newContinuationManagerDriver() *continuationManagerDriver {
	return &continuationManagerDriver{managerDriver: newNamedManagerDriver("codex"), started: make(chan struct{}, 1), release: make(chan struct{})}
}

func (d *continuationManagerDriver) ContinueTurn(ctx context.Context, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	if request.Policy != harness.PolicyReadOnly || sessionID != "thread-1" || intent.InputID != "message-1" {
		return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
	}
	d.started <- struct{}{}
	select {
	case <-d.release:
	case <-ctx.Done():
		return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain"}
	}
	emit(harness.Event{Type: "agent.output.delta", SessionID: sessionID, TurnID: "answer-turn", ItemID: "answer", Delta: "done"})
	return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed", ResultSessionID: sessionID, ResultTurnID: "answer-turn"}
}

func (d *continuationManagerDriver) waitContinuation(t *testing.T) {
	t.Helper()
	select {
	case <-d.started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for continuation")
	}
}

func (d *continuationManagerDriver) releaseContinuation() { close(d.release) }

type nativeDeferredManagerDriver struct {
	*continuationManagerDriver
}

type blockedAfterEmitContinuationDriver struct {
	*managerDriver
	callbackReturned chan struct{}
	cancelObserved   chan struct{}
	allowReturn      chan struct{}
	returned         chan struct{}
	releaseOnce      sync.Once
}

func newBlockedAfterEmitContinuationDriver() *blockedAfterEmitContinuationDriver {
	return &blockedAfterEmitContinuationDriver{
		managerDriver: newNamedManagerDriver("codex"), callbackReturned: make(chan struct{}),
		cancelObserved: make(chan struct{}), allowReturn: make(chan struct{}), returned: make(chan struct{}),
	}
}

func (d *blockedAfterEmitContinuationDriver) ContinueTurn(ctx context.Context, _ harness.OpenRequest, _ string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	emit(harness.Event{Type: "agent.output.delta", TurnID: "blocked-answer", ItemID: "answer", Delta: "cannot deliver"})
	close(d.callbackReturned)
	<-ctx.Done()
	close(d.cancelObserved)
	<-d.allowReturn
	close(d.returned)
	return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain", ResultTurnID: intent.InputID}
}

func (d *blockedAfterEmitContinuationDriver) releaseReturn() {
	d.releaseOnce.Do(func() { close(d.allowReturn) })
}

func newNativeDeferredManagerDriver() *nativeDeferredManagerDriver {
	base := newContinuationManagerDriver()
	base.name = "claude"
	return &nativeDeferredManagerDriver{continuationManagerDriver: base}
}

func (d *nativeDeferredManagerDriver) ResumeDeferredTool(ctx context.Context, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	if request.Policy != harness.PolicyReadOnly || sessionID != "thread-1" || intent.InputID != "message-1" || intent.ContinuationKey != "toolu_restart_exact" {
		return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
	}
	d.started <- struct{}{}
	select {
	case <-d.release:
	case <-ctx.Done():
		return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain"}
	}
	emit(harness.Event{Type: "agent.output.delta", SessionID: sessionID, TurnID: intent.InputID, ItemID: "answer", Delta: "done"})
	return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed", ResultSessionID: sessionID, ResultTurnID: intent.InputID}
}

func waitDispatchEvent(t *testing.T, events <-chan Event, eventType string) Event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == eventType {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", eventType)
		}
	}
}

func waitManagedEvent(t *testing.T, events <-chan managedEvent, eventType string) managedEvent {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.event.Type == eventType {
				return event
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", eventType)
		}
	}
}

func waitManagedEvents(t *testing.T, events <-chan managedEvent, eventType string, expected map[string]string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	remaining := make(map[string]string, len(expected))
	for conversation, inputID := range expected {
		remaining[conversation] = inputID
	}
	for len(remaining) > 0 {
		select {
		case got := <-events:
			if inputID := remaining[got.conversation]; got.event.Type == eventType && got.event.InputID == inputID {
				delete(remaining, got.conversation)
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s events: %v", eventType, remaining)
		}
	}
}

type managerDriver struct {
	mu            sync.Mutex
	name          string
	next          int
	opened        int
	closed        int
	resumed       []string
	policies      []harness.ExecutionPolicy
	roots         []string
	managedInputs []bool
	inputRoots    map[string]string
	inputSessions map[string]string
	failures      map[string]error
	statuses      map[string]string
	openErr       error
	closeErr      error
	closeWait     chan struct{}
	abortOnce     sync.Once
	aborted       int
	started       chan string
	releases      map[string]chan struct{}
}

func newManagerDriver() *managerDriver {
	return newNamedManagerDriver("claude")
}

func newNamedManagerDriver(name string) *managerDriver {
	return &managerDriver{name: name, started: make(chan string, 16), releases: map[string]chan struct{}{}, inputRoots: map[string]string{}, inputSessions: map[string]string{}, failures: map[string]error{}, statuses: map[string]string{}}
}

func (d *managerDriver) Name() string                 { return d.name }
func (d *managerDriver) Executable() string           { return "/fake/claude" }
func (d *managerDriver) Verify(context.Context) error { return nil }
func (d *managerDriver) Open(_ context.Context, request harness.OpenRequest) (harness.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opened++
	d.next++
	d.resumed = append(d.resumed, request.ResumeID)
	d.policies = append(d.policies, request.Policy)
	d.roots = append(d.roots, request.Root)
	d.managedInputs = append(d.managedInputs, request.ManagedRequestInput)
	if d.openErr != nil {
		return nil, d.openErr
	}
	sessionID := request.ResumeID
	if sessionID == "" {
		sessionID = fmt.Sprintf("session-%d", d.next)
	}
	return &managerSession{driver: d, sessionID: sessionID, root: request.Root}, nil
}

func (d *managerDriver) lastManagedRequestInput() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.managedInputs) != 0 && d.managedInputs[len(d.managedInputs)-1]
}

func (d *managerDriver) waitStarted(t *testing.T, id string) {
	t.Helper()
	select {
	case got := <-d.started:
		if got != id {
			t.Fatalf("started input = %s, want %s", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", id)
	}
}

func (d *managerDriver) waitAnyStarted(t *testing.T) string {
	t.Helper()
	select {
	case got := <-d.started:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a managed turn")
		return ""
	}
}

func (d *managerDriver) release(id string) {
	d.mu.Lock()
	release := d.releases[id]
	d.mu.Unlock()
	close(release)
}

func (d *managerDriver) setStatus(id, status string) {
	d.mu.Lock()
	d.statuses[id] = status
	d.mu.Unlock()
}

func (d *managerDriver) openCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opened
}

func (d *managerDriver) closeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

func (d *managerDriver) resumeIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.resumed...)
}

func (d *managerDriver) executionPolicies() []harness.ExecutionPolicy {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]harness.ExecutionPolicy(nil), d.policies...)
}

func (d *managerDriver) openRoots() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.roots...)
}

func (d *managerDriver) rootForInput(inputID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inputRoots[inputID]
}

func (d *managerDriver) sessionForInput(inputID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inputSessions[inputID]
}

type fakeWorkspaceProvider struct {
	project      *project.Project
	assignment   worktree.Assignment
	provisionErr error
	provisions   int
	resolves     int
	removed      int
}

type multiWorkspaceProvider struct {
	mu              sync.Mutex
	base            *project.Project
	assignments     map[string]worktree.Assignment
	resolveFailures map[string]error
}

type reconcilingWorkspaceProvider struct {
	mu          sync.Mutex
	base        *project.Project
	assignments map[string]worktree.Assignment
	inspections map[string]worktree.Inspection
	inspectErrs map[string]error
	retireErrs  map[string]error
	inspected   []string
	retired     []string
	provisions  int
	resolves    int
}

func (p *reconcilingWorkspaceProvider) Provision(_ context.Context, conversation string) (*project.Project, worktree.Assignment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.provisions++
	assignment := p.assignments[conversation]
	return projectAtWorkspace(p.base, assignment.Root), assignment, nil
}

func (p *reconcilingWorkspaceProvider) Resolve(_ context.Context, conversation string, assignment worktree.Assignment) (*project.Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolves++
	if p.assignments[conversation] != assignment {
		return nil, errors.New("unexpected reconciled assignment")
	}
	return projectAtWorkspace(p.base, assignment.Root), nil
}

func (*reconcilingWorkspaceProvider) Remove(context.Context, worktree.Assignment) {}

func (p *reconcilingWorkspaceProvider) Inspect(_ context.Context, conversation string, assignment worktree.Assignment) (worktree.Inspection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inspected = append(p.inspected, conversation)
	if p.assignments[conversation] != assignment {
		return worktree.Inspection{}, errors.New("unexpected inspection assignment")
	}
	return p.inspections[conversation], p.inspectErrs[conversation]
}

func (p *reconcilingWorkspaceProvider) Retire(_ context.Context, conversation string, assignment worktree.Assignment) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.retired = append(p.retired, conversation)
	if p.assignments[conversation] != assignment {
		return errors.New("unexpected retirement assignment")
	}
	return p.retireErrs[conversation]
}

func (p *multiWorkspaceProvider) Provision(_ context.Context, conversation string) (*project.Project, worktree.Assignment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	assignment, ok := p.assignments[conversation]
	if !ok {
		return nil, worktree.Assignment{}, errors.New("missing test workspace assignment")
	}
	return projectAtWorkspace(p.base, assignment.Root), assignment, nil
}

func (p *multiWorkspaceProvider) Resolve(_ context.Context, conversation string, assignment worktree.Assignment) (*project.Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.resolveFailures[conversation]; err != nil {
		return nil, err
	}
	if p.assignments[conversation] != assignment {
		return nil, errors.New("unexpected test workspace assignment")
	}
	return projectAtWorkspace(p.base, assignment.Root), nil
}

func (*multiWorkspaceProvider) Remove(context.Context, worktree.Assignment) {}

func (p *multiWorkspaceProvider) Inspect(_ context.Context, conversation string, assignment worktree.Assignment) (worktree.Inspection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.assignments[conversation] != assignment {
		return worktree.Inspection{}, errors.New("unexpected test workspace assignment")
	}
	return worktree.Inspection{Reason: "test worktree preserved"}, nil
}

func (*multiWorkspaceProvider) Retire(context.Context, string, worktree.Assignment) error {
	return errors.New("test worktree is not disposable")
}

func (p *fakeWorkspaceProvider) Provision(context.Context, string) (*project.Project, worktree.Assignment, error) {
	p.provisions++
	if p.provisionErr != nil {
		return nil, worktree.Assignment{}, p.provisionErr
	}
	return p.project, p.assignment, nil
}

func (p *fakeWorkspaceProvider) Resolve(_ context.Context, _ string, assignment worktree.Assignment) (*project.Project, error) {
	p.resolves++
	if assignment != p.assignment {
		return nil, errors.New("unexpected assignment")
	}
	return p.project, nil
}

func (p *fakeWorkspaceProvider) Remove(context.Context, worktree.Assignment) { p.removed++ }

func (p *fakeWorkspaceProvider) Inspect(_ context.Context, _ string, assignment worktree.Assignment) (worktree.Inspection, error) {
	if assignment != p.assignment {
		return worktree.Inspection{}, errors.New("unexpected assignment")
	}
	return worktree.Inspection{Reason: "test worktree preserved"}, nil
}

func (*fakeWorkspaceProvider) Retire(context.Context, string, worktree.Assignment) error {
	return errors.New("test worktree is not disposable")
}

func projectAtWorkspace(base *project.Project, root string) *project.Project {
	copy := *base
	copy.WorkspaceRoot = root
	return &copy
}

func (d *managerDriver) blockProcessClose() {
	d.mu.Lock()
	d.closeWait = make(chan struct{})
	d.mu.Unlock()
}

func (d *managerDriver) abortCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.aborted
}

type managerSession struct {
	driver    *managerDriver
	sessionID string
	root      string
}

func (s *managerSession) InitialEvents() []harness.Event {
	return []harness.Event{{Type: "session.started", SessionID: s.sessionID}}
}

func (s *managerSession) RunTurn(ctx context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	release := make(chan struct{})
	s.driver.mu.Lock()
	s.driver.releases[input.ID] = release
	s.driver.inputRoots[input.ID] = s.root
	s.driver.inputSessions[input.ID] = s.sessionID
	failure := s.driver.failures[input.ID]
	status := s.driver.statuses[input.ID]
	s.driver.mu.Unlock()
	emit(harness.Event{Type: "turn.started", SessionID: s.sessionID, TurnID: input.ID})
	s.driver.started <- input.ID
	select {
	case <-release:
	case <-ctx.Done():
		return harness.TurnResult{}, ctx.Err()
	}
	if failure != nil {
		return harness.TurnResult{}, failure
	}
	emit(harness.Event{Type: "agent.output.delta", SessionID: s.sessionID, TurnID: input.ID, Delta: "ok"})
	if status == "" {
		status = "completed"
	}
	return harness.TurnResult{SessionID: s.sessionID, TurnID: input.ID, Status: status}, nil
}

func (s *managerSession) Close() error {
	s.driver.mu.Lock()
	s.driver.closed++
	err := s.driver.closeErr
	wait := s.driver.closeWait
	s.driver.mu.Unlock()
	if wait != nil {
		<-wait
	}
	return err
}
func (s *managerSession) Abort() {
	s.driver.abortOnce.Do(func() {
		s.driver.mu.Lock()
		s.driver.aborted++
		wait := s.driver.closeWait
		s.driver.mu.Unlock()
		if wait != nil {
			close(wait)
		}
	})
}

type fakeClock struct {
	created chan *fakeTimer
}

func newFakeClock() *fakeClock {
	return &fakeClock{created: make(chan *fakeTimer, 16)}
}

func (c *fakeClock) NewTimer(after time.Duration) dispatchTimer {
	timer := &fakeTimer{after: after, fired: make(chan time.Time, 1)}
	c.created <- timer
	return timer
}

func (c *fakeClock) waitTimerAfter(t *testing.T, after time.Duration) *fakeTimer {
	t.Helper()
	for {
		timer := c.waitTimer(t)
		if timer.after == after {
			return timer
		}
	}
}

func (c *fakeClock) waitTimer(t *testing.T) *fakeTimer {
	t.Helper()
	select {
	case timer := <-c.created:
		return timer
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for idle timer")
		return nil
	}
}

func (c *fakeClock) assertNoTimer(t *testing.T) {
	t.Helper()
	select {
	case <-c.created:
		t.Fatal("idle timer started while work remained")
	default:
	}
}

type fakeTimer struct {
	after time.Duration
	fired chan time.Time
	once  sync.Once
}

func (t *fakeTimer) C() <-chan time.Time { return t.fired }
func (t *fakeTimer) Stop() bool          { return true }
func (t *fakeTimer) Fire() {
	t.once.Do(func() { t.fired <- time.Time{} })
}

func TestManagerStatusValuesStayBounded(t *testing.T) {
	values := []Lifecycle{LifecycleInactive, LifecycleIdle, LifecycleQueued, LifecycleActive, LifecycleWaiting, LifecycleHibernated}
	if !reflect.DeepEqual(values, []Lifecycle{"inactive", "idle", "queued", "active", "waiting_for_input", "hibernated"}) {
		t.Fatalf("lifecycle values = %v", values)
	}
}

func TestManagerStatusRedactsPendingInteraction(t *testing.T) {
	p := testProject(t)
	manager, err := NewManager(context.Background(), p, newManagerDriver(), time.Minute, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	ref := manager.reference("discord-guild")
	pending := storeTestLifecycle("status")
	pending.InputID = "message-status"
	openStoreTestInteraction(t, manager.store, ref, pending)
	status := manager.Status("discord-guild")
	if status.State != LifecycleWaiting {
		t.Fatalf("status = %+v", status)
	}
	encoded := fmt.Sprintf("%+v", status)
	for _, secret := range []string{"Proceed?", "interaction_", "message-status", "aaaa"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("status leaked interaction state: %s", encoded)
		}
	}
}
