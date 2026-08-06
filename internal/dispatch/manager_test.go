package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/harness"
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
			clock := newFakeIdleClock()
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
	clock := newFakeIdleClock()
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
	}, newFakeIdleClock().NewTimer, provider)
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
	clock := newFakeIdleClock()
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
	clock := newFakeIdleClock()
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
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not stop after hibernate close failure")
	}
	if !errors.Is(manager.Err(), driver.closeErr) {
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
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not finish after startup failure")
	}
	if capacity := manager.Capacity(); capacity.Active != 0 || capacity.Resident != 0 {
		t.Fatalf("startup failure leaked capacity: %+v", capacity)
	}
}

func TestManagerBoundsBlockedHibernateClose(t *testing.T) {
	p := testProject(t)
	driver := newManagerDriver()
	driver.blockProcessClose()
	clock := newFakeIdleClock()
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
	clock := newFakeIdleClock()
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
		}, false, time.Minute, time.Hour, clock.NewTimer, harness.PolicyReadOnly, store, nil, nil)
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
	manager, err := NewManager(context.Background(), p, driver, 10*time.Millisecond, func(string, Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(context.Background(), "discord-guild", Submission{InputID: "message-1", Text: "slow"}); err != nil {
		t.Fatal(err)
	}
	driver.waitStarted(t, "message-1")
	select {
	case <-manager.Done():
		if !errors.Is(manager.Err(), context.DeadlineExceeded) {
			t.Fatalf("timeout error = %v", manager.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("turn timeout did not stop managed runtime")
	}
	manager.Close()
}

type managedEvent struct {
	conversation string
	event        Event
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
	mu        sync.Mutex
	name      string
	next      int
	opened    int
	closed    int
	resumed   []string
	policies  []harness.ExecutionPolicy
	roots     []string
	openErr   error
	closeErr  error
	closeWait chan struct{}
	abortOnce sync.Once
	aborted   int
	started   chan string
	releases  map[string]chan struct{}
}

func newManagerDriver() *managerDriver {
	return newNamedManagerDriver("claude")
}

func newNamedManagerDriver(name string) *managerDriver {
	return &managerDriver{name: name, started: make(chan string, 16), releases: map[string]chan struct{}{}}
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
	if d.openErr != nil {
		return nil, d.openErr
	}
	sessionID := request.ResumeID
	if sessionID == "" {
		sessionID = fmt.Sprintf("session-%d", d.next)
	}
	return &managerSession{driver: d, sessionID: sessionID}, nil
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

type fakeWorkspaceProvider struct {
	project      *project.Project
	assignment   worktree.Assignment
	provisionErr error
	provisions   int
	resolves     int
	removed      int
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
}

func (s *managerSession) InitialEvents() []harness.Event {
	return []harness.Event{{Type: "session.started", SessionID: s.sessionID}}
}

func (s *managerSession) RunTurn(ctx context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	release := make(chan struct{})
	s.driver.mu.Lock()
	s.driver.releases[input.ID] = release
	s.driver.mu.Unlock()
	emit(harness.Event{Type: "turn.started", SessionID: s.sessionID, TurnID: input.ID})
	s.driver.started <- input.ID
	select {
	case <-release:
	case <-ctx.Done():
		return harness.TurnResult{}, ctx.Err()
	}
	emit(harness.Event{Type: "agent.output.delta", SessionID: s.sessionID, TurnID: input.ID, Delta: "ok"})
	return harness.TurnResult{SessionID: s.sessionID, TurnID: input.ID, Status: "completed"}, nil
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

type fakeIdleClock struct {
	created chan *fakeIdleTimer
}

func newFakeIdleClock() *fakeIdleClock {
	return &fakeIdleClock{created: make(chan *fakeIdleTimer, 16)}
}

func (c *fakeIdleClock) NewTimer(after time.Duration) idleTimer {
	timer := &fakeIdleTimer{after: after, fired: make(chan time.Time, 1)}
	c.created <- timer
	return timer
}

func (c *fakeIdleClock) waitTimerAfter(t *testing.T, after time.Duration) *fakeIdleTimer {
	t.Helper()
	for {
		timer := c.waitTimer(t)
		if timer.after == after {
			return timer
		}
	}
}

func (c *fakeIdleClock) waitTimer(t *testing.T) *fakeIdleTimer {
	t.Helper()
	select {
	case timer := <-c.created:
		return timer
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for idle timer")
		return nil
	}
}

func (c *fakeIdleClock) assertNoTimer(t *testing.T) {
	t.Helper()
	select {
	case <-c.created:
		t.Fatal("idle timer started while work remained")
	default:
	}
}

type fakeIdleTimer struct {
	after time.Duration
	fired chan time.Time
	once  sync.Once
}

func (t *fakeIdleTimer) C() <-chan time.Time { return t.fired }
func (t *fakeIdleTimer) Stop() bool          { return true }
func (t *fakeIdleTimer) Fire() {
	t.once.Do(func() { t.fired <- time.Time{} })
}

func TestManagerStatusValuesStayBounded(t *testing.T) {
	values := []Lifecycle{LifecycleInactive, LifecycleIdle, LifecycleQueued, LifecycleActive, LifecycleHibernated}
	if !reflect.DeepEqual(values, []Lifecycle{"inactive", "idle", "queued", "active", "hibernated"}) {
		t.Fatalf("lifecycle values = %v", values)
	}
}
