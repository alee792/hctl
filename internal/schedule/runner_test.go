package schedule

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/project"
)

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeClockTimer
}

type fakeClockTimer struct {
	c       chan time.Time
	stopped atomic.Bool
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) NewTimer(time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeClockTimer{c: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	return timer
}
func (t *fakeClockTimer) C() <-chan time.Time { return t.c }
func (t *fakeClockTimer) Stop() bool          { return !t.stopped.Swap(true) }
func (c *fakeClock) wake(at time.Time) {
	c.mu.Lock()
	c.now = at
	var timer *fakeClockTimer
	for i := len(c.timers) - 1; i >= 0; i-- {
		if !c.timers[i].stopped.Load() {
			timer = c.timers[i]
			break
		}
	}
	c.mu.Unlock()
	if timer != nil {
		timer.c <- at
	}
}

type fakeTaskCoordinator struct {
	mu         sync.Mutex
	recovered  map[string][]string
	calls      []dispatch.Submission
	release    chan struct{}
	started    chan struct{}
	stopped    bool
	result     string
	err        error
	recoverErr error
}

type integratedTaskDriver struct {
	mu      sync.Mutex
	active  int
	maximum int
	opened  int
	resumes []string
	started chan string
	release chan struct{}
}

func (*integratedTaskDriver) Name() string                 { return "claude" }
func (*integratedTaskDriver) Executable() string           { return "fake" }
func (*integratedTaskDriver) Verify(context.Context) error { return nil }
func (d *integratedTaskDriver) Open(_ context.Context, request harness.OpenRequest) (harness.Session, error) {
	d.mu.Lock()
	d.opened++
	d.resumes = append(d.resumes, request.ResumeID)
	d.mu.Unlock()
	return &integratedTaskSession{driver: d}, nil
}

type integratedTaskSession struct{ driver *integratedTaskDriver }

func (*integratedTaskSession) InitialEvents() []harness.Event { return nil }
func (s *integratedTaskSession) RunTurn(_ context.Context, input harness.Input, _ func(harness.Event)) (harness.TurnResult, error) {
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
func (*integratedTaskSession) Close() error { return nil }
func (*integratedTaskSession) Abort()       {}

func (f *fakeTaskCoordinator) Recover(conversation string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recoverErr != nil {
		return nil, f.recoverErr
	}
	return append([]string(nil), f.recovered[conversation]...), nil
}
func (f *fakeTaskCoordinator) Run(_ context.Context, _ string, submission dispatch.Submission, emit func(dispatch.Event) error) error {
	f.mu.Lock()
	f.calls = append(f.calls, submission)
	started := f.started
	release := f.release
	status := f.result
	err := f.err
	f.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	if status == "" {
		status = "completed"
	}
	if status == "uncertain" {
		_ = emit(dispatch.Event{Type: "turn.uncertain", InputID: submission.InputID, Status: "deadline_exceeded", Reason: "deadline_exceeded"})
	} else {
		_ = emit(dispatch.Event{Type: "turn." + status, InputID: submission.InputID})
	}
	return err
}
func (f *fakeTaskCoordinator) Start(ctx context.Context, conversation string, submission dispatch.Submission, emit func(dispatch.Event) error) (<-chan error, error) {
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx, conversation, submission, emit); close(done) }()
	return done, nil
}
func (f *fakeTaskCoordinator) StopAdmission() { f.mu.Lock(); f.stopped = true; f.mu.Unlock() }
func (*fakeTaskCoordinator) Wait()            {}
func (f *fakeTaskCoordinator) count() int     { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func TestRunnerUsesOnlyCurrentUTCMinuteAcrossClockJumps(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 30, 0, time.FixedZone("local", -7*60*60))
	clock := &fakeClock{now: start}
	runtime := &fakeTaskCoordinator{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunForeground(ctx, []project.Schedule{{Name: "every", Cron: "* * * * *", Prompt: []byte("run")}, {Name: "hourly", Cron: "0 * * * *", Prompt: []byte("run")}}, runtime, RunnerOptions{Clock: clock, Emit: func(Diagnostic) error { return nil }})
	}()
	waitForTimers(t, clock, 1)
	clock.wake(time.Date(2026, 8, 7, 19, 5, 20, 0, time.UTC))
	waitForCalls(t, runtime, 1)
	if runtime.calls[0].Text != "run" {
		t.Fatalf("unexpected task: %#v", runtime.calls)
	}
	time.Sleep(10 * time.Millisecond)
	if runtime.count() != 1 {
		t.Fatalf("forward jump backfilled tasks: %d", runtime.count())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerExactBoundaryAndBackwardWakeDoNotDuplicate(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	runtime := &fakeTaskCoordinator{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunForeground(ctx, []project.Schedule{{Name: "task", Cron: "* * * * *", Prompt: []byte("run")}}, runtime, RunnerOptions{Clock: clock, Emit: func(Diagnostic) error { return nil }})
	}()
	waitForTimers(t, clock, 1)
	if runtime.count() != 0 {
		t.Fatal("startup boundary was admitted")
	}
	clock.wake(start.Add(time.Minute))
	waitForCalls(t, runtime, 1)
	waitForTimers(t, clock, 2)
	clock.wake(start.Add(time.Minute))
	waitForTimers(t, clock, 3)
	clock.wake(start.Add(-time.Minute))
	waitForTimers(t, clock, 4)
	if runtime.count() != 1 {
		t.Fatalf("duplicate after repeated/backward wake: %d", runtime.count())
	}
	clock.wake(start.Add(2 * time.Minute))
	waitForCalls(t, runtime, 2)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStopsBeforeStartWhenRecoveryFails(t *testing.T) {
	runtime := &fakeTaskCoordinator{recoverErr: errors.New("persist failed")}
	err := RunForeground(context.Background(), []project.Schedule{{Name: "task", Cron: "* * * * *", Prompt: []byte("run")}}, runtime, RunnerOptions{Clock: &fakeClock{now: time.Now()}, Emit: func(Diagnostic) error { return nil }})
	if err == nil || runtime.count() != 0 {
		t.Fatalf("recovery failure err=%v calls=%d", err, runtime.count())
	}
}

func TestRunnerRejectsNoSchedules(t *testing.T) {
	err := RunForeground(context.Background(), nil, &fakeTaskCoordinator{}, RunnerOptions{Clock: &fakeClock{now: time.Now()}, Emit: func(Diagnostic) error { return nil }})
	if err == nil || err.Error() != "agent project defines no schedules" {
		t.Fatalf("error=%v", err)
	}
}

func TestRunnerSkipsOverlapAndDrainsOnShutdown(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	runtime := &fakeTaskCoordinator{release: make(chan struct{}), started: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var diagnostics []Diagnostic
	done := make(chan error, 1)
	go func() {
		done <- RunForeground(ctx, []project.Schedule{{Name: "nested/日本語", Cron: "* * * * *", Prompt: []byte("run")}}, runtime, RunnerOptions{Clock: clock, Emit: func(d Diagnostic) error { mu.Lock(); diagnostics = append(diagnostics, d); mu.Unlock(); return nil }})
	}()
	waitForTimers(t, clock, 1)
	clock.wake(start.Add(time.Minute))
	select {
	case <-runtime.started:
	case <-time.After(time.Second):
		t.Fatal("occurrence did not start")
	}
	waitForTimers(t, clock, 2)
	clock.wake(start.Add(2 * time.Minute))
	waitForDiagnostic(t, &mu, &diagnostics, "skipped", "overlap")
	cancel()
	select {
	case err := <-done:
		t.Fatalf("shutdown did not drain: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(runtime.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitForDiagnostic(t, &mu, &diagnostics, "completed", "")
}

func TestRunnerSharedRuntimeCountsCapacityWaitersAsOverlappingAndDrains(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	p := &project.Project{AgentID: "agent@one", Harness: "claude", WorkspaceRoot: t.TempDir(), SourceFingerprint: "source-one"}
	driver := &integratedTaskDriver{started: make(chan string, 3), release: make(chan struct{})}
	runtime, err := dispatch.NewTaskRuntime(p, driver, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	schedules := []project.Schedule{
		{Name: "alpha", Cron: "* * * * *", Prompt: []byte("alpha")},
		{Name: "beta", Cron: "* * * * *", Prompt: []byte("beta")},
		{Name: "gamma", Cron: "* * * * *", Prompt: []byte("gamma")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var diagnostics []Diagnostic
	done := make(chan error, 1)
	go func() {
		done <- RunForeground(ctx, schedules, runtime, RunnerOptions{Clock: clock, Emit: func(d Diagnostic) error {
			mu.Lock()
			diagnostics = append(diagnostics, d)
			mu.Unlock()
			return nil
		}})
	}()
	waitForTimers(t, clock, 1)
	clock.wake(start.Add(time.Minute))
	waitForDiagnosticCount(t, &mu, &diagnostics, "started", 4) // runtime plus three occurrences
	select {
	case <-driver.started:
	case <-time.After(time.Second):
		t.Fatal("first occurrence did not enter its turn")
	}
	select {
	case id := <-driver.started:
		t.Fatalf("capacity admitted a second active turn: %s", id)
	case <-time.After(20 * time.Millisecond):
	}
	waitForTimers(t, clock, 2)
	clock.wake(start.Add(2 * time.Minute))
	waitForDiagnosticCount(t, &mu, &diagnostics, "skipped", 3)
	cancel()
	select {
	case err := <-done:
		t.Fatalf("signal did not drain capacity waiters: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(driver.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.maximum != 1 || driver.opened != 3 {
		t.Fatalf("maximum=%d opened=%d", driver.maximum, driver.opened)
	}
	for _, resume := range driver.resumes {
		if resume != "" {
			t.Fatalf("scheduled task resumed %q", resume)
		}
	}
}

func TestRunnerRecoversDurableTaskWithoutExecutingIt(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
	name := "billing/sweep"
	runtime := &fakeTaskCoordinator{recovered: map[string][]string{conversationID(name): {"prior-occurrence"}}}
	ctx, cancel := context.WithCancel(context.Background())
	var diagnostics []Diagnostic
	done := make(chan error, 1)
	go func() {
		done <- RunForeground(ctx, []project.Schedule{{Name: name, Cron: "0 * * * *", Prompt: []byte("secret prompt")}}, runtime, RunnerOptions{Clock: clock, Emit: func(d Diagnostic) error { diagnostics = append(diagnostics, d); return nil }})
	}()
	waitForTimers(t, clock, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if runtime.count() != 0 {
		t.Fatal("recovered occurrence was executed")
	}
	if len(diagnostics) < 2 || diagnostics[0].Status != "uncertain" || diagnostics[0].Reason != "dispatcher_restarted" {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
}

func TestRunnerReturnsTerminalOutputFailureDuringShutdown(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	runtime := &fakeTaskCoordinator{release: make(chan struct{}), started: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	var terminal atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- RunForeground(ctx, []project.Schedule{{Name: "task", Cron: "* * * * *", Prompt: []byte("run")}}, runtime, RunnerOptions{Clock: clock, Emit: func(d Diagnostic) error {
			if terminal.Load() && d.Kind == "occurrence" {
				return errors.New("sink broke")
			}
			return nil
		}})
	}()
	waitForTimers(t, clock, 1)
	clock.wake(start.Add(time.Minute))
	select {
	case <-runtime.started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	cancel()
	terminal.Store(true)
	close(runtime.release)
	if err := <-done; err == nil {
		t.Fatal("output failure was hidden")
	}
}

func TestOccurrenceIDUsesExactNameAndUTCMinute(t *testing.T) {
	instant := time.Date(2026, 8, 7, 19, 5, 0, 0, time.UTC)
	local := instant.In(time.FixedZone("local", -7*60*60))
	name := "nested/" + string(make([]rune, 0)) + "日本語:a\x00b"
	id := OccurrenceID(name, instant)
	if id != OccurrenceID(name, local) || len(id) != 68 {
		t.Fatalf("unstable occurrence id %q", id)
	}
	if id == OccurrenceID(name+"x", instant) || id == OccurrenceID(name, instant.Add(time.Minute)) {
		t.Fatal("occurrence ids collided")
	}
	if err := dispatch.ValidateInputID(id); err != nil {
		t.Fatalf("invalid input id: %v", err)
	}
}

func waitForTimers(t *testing.T, clock *fakeClock, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		clock.mu.Lock()
		n := len(clock.timers)
		clock.mu.Unlock()
		if n >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timer count did not reach %d", count)
}
func waitForCalls(t *testing.T, runtime *fakeTaskCoordinator, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.count() >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("call count did not reach %d", count)
}
func waitForDiagnostic(t *testing.T, mu *sync.Mutex, diagnostics *[]Diagnostic, status, reason string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, d := range *diagnostics {
			if d.Status == status && d.Reason == reason {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("missing diagnostic %s/%s", status, reason)
}

func waitForDiagnosticCount(t *testing.T, mu *sync.Mutex, diagnostics *[]Diagnostic, status string, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := 0
		for _, d := range *diagnostics {
			if d.Status == status {
				found++
			}
		}
		mu.Unlock()
		if found >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("diagnostic %s count did not reach %d", status, count)
}
