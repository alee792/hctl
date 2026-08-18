package dispatch

import (
	"context"
	"sync"
	"testing"
	"time"

	"hctl/internal/dispatchstate"
	"hctl/internal/harness"
)

type runtimeDriver struct {
	mu      sync.Mutex
	active  int
	maximum int
	opened  int
	resumed []string
	started chan string
	release chan struct{}
}

func (*runtimeDriver) Name() string                 { return "claude" }
func (*runtimeDriver) Executable() string           { return "fake" }
func (*runtimeDriver) Verify(context.Context) error { return nil }
func (d *runtimeDriver) Open(_ context.Context, request harness.OpenRequest) (harness.Session, error) {
	d.mu.Lock()
	d.opened++
	d.resumed = append(d.resumed, request.ResumeID)
	d.mu.Unlock()
	return &runtimeSession{driver: d}, nil
}

type runtimeSession struct{ driver *runtimeDriver }

func (*runtimeSession) InitialEvents() []harness.Event { return nil }
func (s *runtimeSession) RunTurn(_ context.Context, input harness.Input, _ func(harness.Event)) (harness.TurnResult, error) {
	s.driver.mu.Lock()
	s.driver.active++
	if s.driver.active > s.driver.maximum {
		s.driver.maximum = s.driver.active
	}
	s.driver.mu.Unlock()
	s.driver.started <- input.ID
	<-s.driver.release
	s.driver.mu.Lock()
	s.driver.active--
	s.driver.mu.Unlock()
	return harness.TurnResult{SessionID: "session-" + input.ID, TurnID: input.ID, Status: "completed"}, nil
}
func (*runtimeSession) Close() error { return nil }
func (*runtimeSession) Abort()       {}

func TestTaskRuntimeSharesStoreAndBoundsConcurrentFreshSessions(t *testing.T) {
	p := testProject(t)
	driver := &runtimeDriver{started: make(chan string, 3), release: make(chan struct{})}
	runtime, err := NewTaskRuntime(p, driver, time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i, conversation := range []string{"schedule-a", "schedule-b", "schedule-c"} {
		wg.Add(1)
		go func(conversation, id string) {
			defer wg.Done()
			errs <- runtime.Run(context.Background(), conversation, Submission{InputID: id, Text: "run"}, func(Event) error { return nil })
		}(conversation, "occurrence-"+string(rune('a'+i)))
	}
	for range 2 {
		select {
		case <-driver.started:
		case <-time.After(time.Second):
			t.Fatal("capacity did not admit two tasks")
		}
	}
	select {
	case id := <-driver.started:
		t.Fatalf("third task exceeded capacity: %s", id)
	case <-time.After(20 * time.Millisecond):
	}
	close(driver.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	driver.mu.Lock()
	if driver.maximum != 2 || driver.opened != 3 {
		t.Fatalf("maximum=%d opened=%d", driver.maximum, driver.opened)
	}
	for _, resume := range driver.resumed {
		if resume != "" {
			t.Fatalf("task resumed session %q", resume)
		}
	}
	driver.mu.Unlock()
	state, err := dispatchstate.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Conversations) != 3 {
		t.Fatalf("shared durable state lost conversations: %d", len(state.Conversations))
	}
}

func TestTaskRuntimeRecoversQueuedAndActiveAsUncertain(t *testing.T) {
	p := testProject(t)
	state, err := dispatchstate.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := state.GetOrCreate(p.AgentID, "claude", "schedule-a", p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Accept("queued", "run"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Accept("active", "run"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.StartNext(); err != nil {
		t.Fatal(err)
	}
	if err := dispatchstate.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewTaskRuntime(p, &runtimeDriver{}, time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	recovered, err := runtime.Recover("schedule-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 2 || recovered[0] != "queued" || recovered[1] != "active" {
		t.Fatalf("recovered=%v", recovered)
	}
	loaded, err := dispatchstate.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation = findConversation(loaded, "schedule-a")
	if len(conversation.Queue) != 0 || conversation.Outcomes["queued"] != "uncertain" || conversation.Outcomes["active"] != "uncertain" {
		t.Fatalf("state=%#v", conversation)
	}
}

func TestTaskRuntimeDeadlineIsolatedAndDurable(t *testing.T) {
	p := testProject(t)
	driver := &runtimeDriver{started: make(chan string, 1), release: make(chan struct{})}
	clock := newFakeClock()
	runtime, err := newTaskRuntime(p, driver, time.Minute, 1, clock.NewTimer)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- runtime.Run(context.Background(), "schedule-a", Submission{InputID: "deadline", Text: "run"}, func(Event) error { return nil })
	}()
	select {
	case <-driver.started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	clock.waitTimerAfter(t, time.Minute).Fire()
	err = <-done
	if err != ErrTurnDeadlineExceeded {
		t.Fatalf("deadline error=%v", err)
	}
	state, err := dispatchstate.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation := findConversation(state, "schedule-a")
	if conversation.Outcomes["deadline"] != "uncertain" || conversation.OutcomeReason("deadline") != dispatchstate.OutcomeReasonDeadlineExceeded {
		t.Fatalf("state=%#v", conversation)
	}
	close(driver.release)
}

func TestTaskRuntimeStopAdmissionDrainsSynchronouslyStartedTask(t *testing.T) {
	p := testProject(t)
	driver := &runtimeDriver{started: make(chan string, 1), release: make(chan struct{})}
	runtime, err := NewTaskRuntime(p, driver, time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	done, err := runtime.Start(context.Background(), "schedule-a", Submission{InputID: "admitted", Text: "run"}, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	runtime.StopAdmission()
	if _, err := runtime.Start(context.Background(), "schedule-b", Submission{InputID: "late", Text: "run"}, func(Event) error { return nil }); err != ErrManagerClosed {
		t.Fatalf("late admission error=%v", err)
	}
	select {
	case <-driver.started:
	case <-time.After(time.Second):
		t.Fatal("admitted task did not drain")
	}
	close(driver.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	runtime.Close()
}
