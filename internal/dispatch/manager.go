package dispatch

import (
	"context"
	"errors"
	"sync"
	"time"

	"hctl/internal/harness"
	"hctl/internal/project"
)

var (
	ErrManagerClosed    = errors.New("managed sessions are closed")
	ErrConversationBusy = errors.New("conversation is busy")
)

const DefaultIdleTimeout = 15 * time.Minute

type Lifecycle string

const (
	LifecycleInactive   Lifecycle = "inactive"
	LifecycleIdle       Lifecycle = "idle"
	LifecycleQueued     Lifecycle = "queued"
	LifecycleActive     Lifecycle = "active"
	LifecycleHibernated Lifecycle = "hibernated"
)

type ConversationStatus struct {
	State   Lifecycle
	Pending int
}

// Manager owns the lifecycle of every long-lived conversation in one runtime.
// It keeps transport concerns outside the dispatcher while sharing one durable
// state store across all conversation workers.
type Manager struct {
	project     *project.Project
	driver      harness.Driver
	turnTimeout time.Duration
	idleTimeout time.Duration
	timers      idleTimerFactory
	emit        func(string, Event) error
	store       *conversationStore
	ctx         context.Context
	cancel      context.CancelFunc

	mu        sync.Mutex
	workers   map[string]*managedConversation
	closed    bool
	err       error
	done      chan struct{}
	doneOnce  sync.Once
	stopped   chan struct{}
	closeOnce sync.Once
}

type managedConversation struct {
	conversation string
	submissions  chan Submission
	done         chan struct{}
	submitMu     sync.Mutex
	admissions   int
	closing      bool
	resident     bool
	err          error
}

func NewManager(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout time.Duration, emit func(string, Event) error) (*Manager, error) {
	return NewManagerWithIdleTimeout(ctx, p, driver, turnTimeout, DefaultIdleTimeout, emit)
}

func NewManagerWithIdleTimeout(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout, idleTimeout time.Duration, emit func(string, Event) error) (*Manager, error) {
	return newManager(ctx, p, driver, turnTimeout, idleTimeout, emit, newIdleTimer)
}

func newManager(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout, idleTimeout time.Duration, emit func(string, Event) error, timers idleTimerFactory) (*Manager, error) {
	if p == nil || driver == nil {
		return nil, errors.New("managed sessions require a project and harness driver")
	}
	if turnTimeout <= 0 {
		return nil, errors.New("managed session turn timeout must be positive")
	}
	if idleTimeout <= 0 || timers == nil {
		return nil, errors.New("managed session idle timeout must be positive")
	}
	if emit == nil {
		return nil, errors.New("managed session event receiver is required")
	}
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	managerCtx, cancel := context.WithCancel(ctx)
	return &Manager{
		project: p, driver: driver, turnTimeout: turnTimeout, idleTimeout: idleTimeout, timers: timers, emit: emit,
		store: store, ctx: managerCtx, cancel: cancel,
		workers: map[string]*managedConversation{}, done: make(chan struct{}), stopped: make(chan struct{}),
	}, nil
}

func (m *Manager) Submit(ctx context.Context, conversation string, submission Submission) (SubmissionResult, error) {
	if err := ValidateConversation(conversation); err != nil {
		return SubmissionResult{}, err
	}
	reply := make(chan SubmissionResult, 1)
	submission.Reply = reply

	m.mu.Lock()
	if m.closed {
		err := m.err
		m.mu.Unlock()
		if err != nil {
			return SubmissionResult{}, err
		}
		return SubmissionResult{}, ErrManagerClosed
	}
	worker := m.workers[conversation]
	if worker == nil {
		worker = &managedConversation{conversation: conversation, submissions: make(chan Submission, 32), done: make(chan struct{})}
		m.workers[conversation] = worker
		go m.run(worker)
	}
	if worker.closing {
		m.mu.Unlock()
		return SubmissionResult{}, ErrConversationBusy
	}
	worker.admissions++
	m.mu.Unlock()

	worker.submitMu.Lock()
	m.mu.Lock()
	if m.closed || worker.closing {
		closed := m.closed
		worker.admissions--
		err := m.err
		m.mu.Unlock()
		worker.submitMu.Unlock()
		if err != nil {
			return SubmissionResult{}, err
		}
		if closed {
			return SubmissionResult{}, ErrManagerClosed
		}
		return SubmissionResult{}, ErrConversationBusy
	}
	m.mu.Unlock()

	select {
	case worker.submissions <- submission:
		worker.submitMu.Unlock()
	case <-ctx.Done():
		worker.submitMu.Unlock()
		if err := m.finishAdmission(worker); err != nil {
			return SubmissionResult{}, err
		}
		return SubmissionResult{}, ctx.Err()
	case <-m.ctx.Done():
		worker.submitMu.Unlock()
		if err := m.finishAdmission(worker); err != nil {
			return SubmissionResult{}, err
		}
		m.mu.Lock()
		err := m.err
		m.mu.Unlock()
		if err != nil {
			return SubmissionResult{}, err
		}
		return SubmissionResult{}, ErrManagerClosed
	}

	select {
	case result := <-reply:
		if err := m.finishAdmission(worker); err != nil {
			return SubmissionResult{}, err
		}
		return result, nil
	case <-ctx.Done():
		m.finishAdmissionAsync(worker, reply)
		return SubmissionResult{}, ctx.Err()
	case <-m.ctx.Done():
		m.finishAdmissionAsync(worker, reply)
		m.mu.Lock()
		err := m.err
		m.mu.Unlock()
		if err != nil {
			return SubmissionResult{}, err
		}
		return SubmissionResult{}, ErrManagerClosed
	}
}

func (m *Manager) Status(conversation string) ConversationStatus {
	m.mu.Lock()
	worker := m.workers[conversation]
	if worker == nil {
		snapshot, err := m.store.snapshot(m.reference(conversation))
		m.mu.Unlock()
		if err != nil || !snapshot.exists {
			return ConversationStatus{State: LifecycleInactive}
		}
		return statusFromSnapshot(snapshot, 0, false)
	}
	if worker.closing {
		m.mu.Unlock()
		return ConversationStatus{State: LifecycleInactive}
	}
	m.mu.Unlock()

	snapshot, err := m.store.snapshot(m.reference(conversation))
	if err != nil {
		return ConversationStatus{State: LifecycleInactive}
	}
	m.mu.Lock()
	if m.workers[conversation] != worker || worker.closing {
		m.mu.Unlock()
		return ConversationStatus{State: LifecycleInactive}
	}
	admissions := worker.admissions
	resident := worker.resident
	m.mu.Unlock()

	return statusFromSnapshot(snapshot, admissions, resident)
}

func statusFromSnapshot(snapshot conversationSnapshot, admissions int, resident bool) ConversationStatus {
	pending := max(snapshot.queueLen, admissions)
	status := ConversationStatus{State: LifecycleIdle, Pending: pending}
	if snapshot.active {
		status.State = LifecycleActive
	} else if pending > 0 {
		status.State = LifecycleQueued
	} else if !resident && snapshot.sessionID != "" {
		status.State = LifecycleHibernated
	} else if !resident {
		status.State = LifecycleInactive
	}
	return status
}

func (m *Manager) Reset(conversation string) error {
	if err := ValidateConversation(conversation); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrManagerClosed
	}
	worker := m.workers[conversation]
	if worker == nil {
		snapshot, err := m.store.snapshot(m.reference(conversation))
		if err != nil {
			m.mu.Unlock()
			return err
		}
		if !snapshot.exists {
			m.mu.Unlock()
			return nil
		}
		if snapshot.queueLen != 0 {
			m.mu.Unlock()
			return ErrConversationBusy
		}
		if err := m.store.reset(m.reference(conversation)); err != nil {
			m.mu.Unlock()
			return err
		}
		m.mu.Unlock()
		return nil
	}
	if worker != nil {
		if worker.admissions != 0 || worker.closing {
			m.mu.Unlock()
			return ErrConversationBusy
		}
		worker.closing = true
	}
	m.mu.Unlock()
	if worker != nil {
		snapshot, err := m.store.snapshot(m.reference(conversation))
		if err != nil || snapshot.queueLen != 0 {
			m.mu.Lock()
			if m.workers[conversation] == worker && !m.closed {
				worker.closing = false
			}
			m.mu.Unlock()
			if err != nil {
				return err
			}
			return ErrConversationBusy
		}
		worker.submitMu.Lock()
		close(worker.submissions)
		worker.submitMu.Unlock()
		<-worker.done
		if worker.err != nil {
			return worker.err
		}
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrManagerClosed
	}
	if err := m.store.reset(m.reference(conversation)); err != nil {
		return err
	}
	m.mu.Lock()
	if m.workers[conversation] == worker {
		delete(m.workers, conversation)
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) Done() <-chan struct{} { return m.done }

func (m *Manager) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		workers := make([]*managedConversation, 0, len(m.workers))
		toClose := make([]*managedConversation, 0, len(m.workers))
		for _, worker := range m.workers {
			if !worker.closing {
				worker.closing = true
				toClose = append(toClose, worker)
			}
			workers = append(workers, worker)
		}
		m.cancel()
		m.mu.Unlock()
		for _, worker := range toClose {
			worker.submitMu.Lock()
			close(worker.submissions)
			worker.submitMu.Unlock()
		}
		for _, worker := range workers {
			<-worker.done
		}
		m.doneOnce.Do(func() { close(m.done) })
		close(m.stopped)
	})
	<-m.stopped
}

func (m *Manager) run(worker *managedConversation) {
	err := runSubmissions(m.ctx, m.project, m.driver, worker.conversation, worker.submissions, func(event Event) error {
		m.mu.Lock()
		switch event.Type {
		case "driver.process_opened":
			worker.resident = true
		case "driver.process_hibernated":
			worker.resident = false
		}
		m.mu.Unlock()
		return m.emit(worker.conversation, event)
	}, false, m.turnTimeout, m.idleTimeout, m.timers, m.store)
	m.mu.Lock()
	worker.err = err
	close(worker.done)
	closing := worker.closing || m.closed || errors.Is(err, context.Canceled)
	if err != nil && !closing && m.err == nil {
		m.err = err
		m.closed = true
		m.cancel()
		m.doneOnce.Do(func() { close(m.done) })
	}
	m.mu.Unlock()
}

func (m *Manager) finishAdmission(worker *managedConversation) error {
	m.mu.Lock()
	if worker.admissions > 0 {
		worker.admissions--
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) finishAdmissionAsync(worker *managedConversation, reply <-chan SubmissionResult) {
	go func() {
		select {
		case <-reply:
			_ = m.finishAdmission(worker)
		case <-worker.done:
			_ = m.finishAdmission(worker)
		}
	}()
}

func (m *Manager) reference(conversation string) conversationRef {
	return conversationRef{agentID: m.project.AgentID, harness: m.driver.Name(), id: conversation, fingerprint: m.project.SourceFingerprint}
}
