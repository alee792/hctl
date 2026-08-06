package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"hctl/internal/harness"
	"hctl/internal/session"
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
	mu       sync.Mutex
	next     int
	opened   int
	started  chan string
	releases map[string]chan struct{}
}

func newManagerDriver() *managerDriver {
	return &managerDriver{started: make(chan string, 16), releases: map[string]chan struct{}{}}
}

func (d *managerDriver) Name() string                 { return "claude" }
func (d *managerDriver) Executable() string           { return "/fake/claude" }
func (d *managerDriver) Verify(context.Context) error { return nil }
func (d *managerDriver) Open(_ context.Context, _ string, resumeID string) (harness.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opened++
	d.next++
	sessionID := resumeID
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

func (s *managerSession) Close() error { return nil }
func (s *managerSession) Abort()       {}

func TestManagerStatusValuesStayBounded(t *testing.T) {
	values := []Lifecycle{LifecycleInactive, LifecycleIdle, LifecycleQueued, LifecycleActive}
	if !reflect.DeepEqual(values, []Lifecycle{"inactive", "idle", "queued", "active"}) {
		t.Fatalf("lifecycle values = %v", values)
	}
}
