package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"

	"hctl/internal/harness"
	"hctl/internal/project"
)

// TaskRuntime is the shared durable-state and admission coordinator for fresh-
// session tasks. Callers may run different conversations concurrently. A
// conversation may have only one admitted task at a time.
type TaskRuntime struct {
	project     *project.Project
	driver      harness.Driver
	store       *conversationStore
	capacity    *capacityCoordinator
	turnTimeout time.Duration
	timers      timerFactory

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// Recover classifies every occurrence retained across a prior task-runtime
// shutdown as uncertain. It never starts a harness process.
func (r *TaskRuntime) Recover(conversationID string) ([]string, error) {
	if err := validateDispatch(conversationID, func(Event) error { return nil }); err != nil {
		return nil, err
	}
	ref := conversationRef{agentID: r.project.AgentID, harness: r.driver.Name(), id: conversationID, fingerprint: r.project.SourceFingerprint}
	return r.store.recoverTask(ref)
}

func NewTaskRuntime(p *project.Project, driver harness.Driver, turnTimeout time.Duration, maxActive int) (*TaskRuntime, error) {
	return newTaskRuntime(p, driver, turnTimeout, maxActive, newTimer)
}

func newTaskRuntime(p *project.Project, driver harness.Driver, turnTimeout time.Duration, maxActive int, timers timerFactory) (*TaskRuntime, error) {
	if p == nil || driver == nil {
		return nil, errors.New("task runtime requires a project and harness driver")
	}
	if turnTimeout <= 0 || turnTimeout > maxTaskTurnTimeout {
		return nil, errors.New("task turn timeout must be positive and at most 30m")
	}
	if maxActive <= 0 || maxActive > 64 {
		return nil, errors.New("task active-turn limit must be between 1 and 64")
	}
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	capacity, err := newCapacityCoordinator(maxActive, maxActive)
	if err != nil {
		return nil, err
	}
	if timers == nil {
		return nil, errors.New("task timer factory is required")
	}
	return &TaskRuntime{project: p, driver: driver, store: store, capacity: capacity, turnTimeout: turnTimeout, timers: timers}, nil
}

// Run executes one occurrence. Capacity waiting is part of the occurrence's
// in-flight lifetime and the deadline begins only when the native turn starts.
func (r *TaskRuntime) Run(ctx context.Context, conversationID string, submission Submission, emit func(Event) error) error {
	done, err := r.Start(ctx, conversationID, submission, emit)
	if err != nil {
		return err
	}
	return <-done
}

// Start synchronously admits an occurrence before launching its worker. Once
// Start returns successfully, StopAdmission cannot prevent that work from
// draining.
func (r *TaskRuntime) Start(ctx context.Context, conversationID string, submission Submission, emit func(Event) error) (<-chan error, error) {
	if err := validateDispatch(conversationID, emit); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrManagerClosed
	}
	r.wg.Add(1)
	hibernate := make(chan struct{}, 1)
	if err := r.capacity.register(conversationID, hibernate); err != nil {
		r.wg.Done()
		r.mu.Unlock()
		return nil, err
	}
	r.mu.Unlock()

	submissions := make(chan Submission, 1)
	submissions <- submission
	close(submissions)
	done := make(chan error, 1)
	go func() {
		defer r.wg.Done()
		defer r.capacity.unregister(conversationID)
		done <- runSubmissions(ctx, r.project, r.driver, conversationID, submissions, emit, runOptions{
			freshSessions:  true,
			turnTimeout:    r.turnTimeout,
			timers:         r.timers,
			taskDeadline:   true,
			policy:         harness.PolicyDefault,
			store:          r.store,
			capacity:       r.capacity,
			forceHibernate: hibernate,
		})
		close(done)
	}()
	return done, nil
}

// StopAdmission rejects new work. Already admitted work continues until its
// turn completes, fails, or reaches its configured deadline.
func (r *TaskRuntime) StopAdmission() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *TaskRuntime) Wait() { r.wg.Wait() }

func (r *TaskRuntime) Close() {
	r.StopAdmission()
	r.Wait()
	r.capacity.shutdown()
}
