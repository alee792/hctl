package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/worktree"
)

var (
	ErrManagerClosed    = errors.New("managed sessions are closed")
	ErrConversationBusy = errors.New("conversation is busy")
	ErrWaitingForInput  = errors.New("waiting for input")
)

const DefaultIdleTimeout = 15 * time.Minute

type Lifecycle string

const (
	LifecycleInactive   Lifecycle = "inactive"
	LifecycleIdle       Lifecycle = "idle"
	LifecycleQueued     Lifecycle = "queued"
	LifecycleActive     Lifecycle = "active"
	LifecycleWaiting    Lifecycle = "waiting_for_input"
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
	capacity    *capacityCoordinator
	workspaces  WorkspaceProvider
	emit        func(string, Event) error
	store       *conversationStore
	ctx         context.Context
	cancel      context.CancelFunc

	mu          sync.Mutex
	workers     map[string]*managedConversation
	elevating   map[string]bool
	closed      bool
	err         error
	done        chan struct{}
	doneOnce    sync.Once
	stopped     chan struct{}
	closeOnce   sync.Once
	diagnostics []string
}

type WorkspaceProvider interface {
	Provision(context.Context, string) (*project.Project, worktree.Assignment, error)
	Resolve(context.Context, string, worktree.Assignment) (*project.Project, error)
	Remove(context.Context, worktree.Assignment)
}

type WorkspaceReconciler interface {
	Inspect(context.Context, string, worktree.Assignment) (worktree.Inspection, error)
	Retire(context.Context, string, worktree.Assignment) error
}

type managedConversation struct {
	conversation string
	submissions  chan Submission
	done         chan struct{}
	submitMu     sync.Mutex
	admissions   int
	closing      bool
	resident     bool
	hibernate    chan struct{}
	wake         chan struct{}
	err          error
}

func NewManager(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout time.Duration, emit func(string, Event) error) (*Manager, error) {
	return NewManagerWithIdleTimeout(ctx, p, driver, turnTimeout, DefaultIdleTimeout, emit)
}

func NewManagerWithIdleTimeout(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout, idleTimeout time.Duration, emit func(string, Event) error) (*Manager, error) {
	return newManager(ctx, p, driver, turnTimeout, idleTimeout, DefaultMaxResidentSessions, DefaultMaxActiveTurns, emit, newIdleTimer, nil)
}

func NewManagerWithWorkspace(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout, idleTimeout time.Duration, emit func(string, Event) error, workspaces WorkspaceProvider) (*Manager, error) {
	return NewManagerWithWorkspaceAndLimits(ctx, p, driver, turnTimeout, idleTimeout, DefaultMaxResidentSessions, DefaultMaxActiveTurns, emit, workspaces)
}

func NewManagerWithWorkspaceAndLimits(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout, idleTimeout time.Duration, maxResident, maxActive int, emit func(string, Event) error, workspaces WorkspaceProvider) (*Manager, error) {
	if workspaces == nil {
		return nil, errors.New("managed writable workspace provider is required")
	}
	return newManager(ctx, p, driver, turnTimeout, idleTimeout, maxResident, maxActive, emit, newIdleTimer, workspaces)
}

func NewManagerWithLimits(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout, idleTimeout time.Duration, maxResident, maxActive int, emit func(string, Event) error) (*Manager, error) {
	return newManager(ctx, p, driver, turnTimeout, idleTimeout, maxResident, maxActive, emit, newIdleTimer, nil)
}

func newManager(ctx context.Context, p *project.Project, driver harness.Driver, turnTimeout, idleTimeout time.Duration, maxResident, maxActive int, emit func(string, Event) error, timers idleTimerFactory, workspaces WorkspaceProvider) (*Manager, error) {
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
	capacity, err := newCapacityCoordinator(maxResident, maxActive)
	if err != nil {
		return nil, err
	}
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	records, recordErr := store.workspaceRecords(conversationRef{agentID: p.AgentID, harness: driver.Name()})
	if recordErr != nil {
		return nil, recordErr
	}
	if len(records) != 0 {
		if _, ok := workspaces.(WorkspaceReconciler); !ok {
			return nil, errors.New("persisted conversation worktrees require local Git ownership recovery before hctl can run")
		}
	}
	managerCtx, cancel := context.WithCancel(ctx)
	manager := &Manager{
		project: p, driver: driver, turnTimeout: turnTimeout, idleTimeout: idleTimeout, timers: timers, capacity: capacity, workspaces: workspaces, emit: emit,
		store: store, ctx: managerCtx, cancel: cancel,
		workers: map[string]*managedConversation{}, elevating: map[string]bool{}, done: make(chan struct{}), stopped: make(chan struct{}),
	}
	if err := manager.reconcileWorkspaces(managerCtx); err != nil {
		cancel()
		return nil, err
	}
	for _, conversation := range store.runnable(manager.reference("")) {
		if err := manager.startDurableWorker(conversation); err != nil {
			manager.Close()
			return nil, err
		}
	}
	return manager, nil
}

func (m *Manager) Elevate(ctx context.Context, conversation string, continuation Submission) (SubmissionResult, error) {
	if err := ValidateConversation(conversation); err != nil {
		return SubmissionResult{}, err
	}
	if status := validateInput(continuation); status != "" {
		return SubmissionResult{Status: status}, nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return SubmissionResult{}, ErrManagerClosed
	}
	if m.workspaces == nil {
		m.mu.Unlock()
		return SubmissionResult{}, errors.New("writable conversation workspaces are unavailable")
	}
	if m.elevating[conversation] {
		m.mu.Unlock()
		return SubmissionResult{}, ErrConversationBusy
	}
	m.elevating[conversation] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.elevating, conversation)
		m.mu.Unlock()
	}()

	if err := m.stopIdleWorker(conversation); err != nil {
		return SubmissionResult{}, err
	}
	snapshot, err := m.store.snapshot(m.reference(conversation))
	if err != nil {
		return SubmissionResult{}, err
	}
	if snapshot.retiring {
		return SubmissionResult{}, errors.New("conversation worktree retirement requires local recovery")
	}
	assignment := worktree.Assignment{Root: snapshot.workspace, Branch: snapshot.branch}
	created := false
	if assignment.Root == "" {
		_, provisioned, provisionErr := m.workspaces.Provision(ctx, conversation)
		assignment, err = provisioned, provisionErr
		if err != nil {
			return SubmissionResult{}, err
		}
		created = true
	}
	status, duplicate, err := m.store.assignWorkspaceAndAccept(m.reference(conversation), assignment.Root, assignment.Branch, continuation.InputID, continuation.Text)
	if err != nil {
		if created {
			m.workspaces.Remove(ctx, assignment)
		}
		return SubmissionResult{}, err
	}
	if status != "queued" && status != "active" {
		return SubmissionResult{Status: status, Duplicate: duplicate}, nil
	}
	if err := m.startDurableWorker(conversation); err != nil {
		return SubmissionResult{}, err
	}
	return SubmissionResult{Status: status, Duplicate: duplicate}, nil
}

func (m *Manager) startDurableWorker(conversation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		if m.err != nil {
			return m.err
		}
		return ErrManagerClosed
	}
	if m.workers[conversation] != nil {
		return nil
	}
	worker, err := m.newWorkerLocked(conversation)
	if err != nil {
		return err
	}
	m.workers[conversation] = worker
	go m.run(worker)
	return nil
}

func (m *Manager) Submit(ctx context.Context, conversation string, submission Submission) (SubmissionResult, error) {
	if err := ValidateConversation(conversation); err != nil {
		return SubmissionResult{}, err
	}
	m.mu.Lock()
	closed, managerErr, elevating := m.closed, m.err, m.elevating[conversation]
	m.mu.Unlock()
	if closed {
		if managerErr != nil {
			return SubmissionResult{}, managerErr
		}
		return SubmissionResult{}, ErrManagerClosed
	}
	if elevating {
		return SubmissionResult{}, ErrConversationBusy
	}
	snapshot, err := m.store.snapshot(m.reference(conversation))
	if err != nil {
		return SubmissionResult{}, err
	}
	if snapshot.retiring {
		return SubmissionResult{}, errors.New("conversation worktree retirement requires local recovery")
	}
	status, duplicate, err := m.store.inputStatus(m.reference(conversation), submission.InputID)
	if err != nil {
		return SubmissionResult{}, err
	}
	if duplicate {
		if status == "queued" || status == "active" {
			if err := m.startDurableWorker(conversation); err != nil {
				return SubmissionResult{}, err
			}
		}
		return SubmissionResult{Status: status, Duplicate: true}, nil
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
	if m.elevating[conversation] {
		m.mu.Unlock()
		return SubmissionResult{}, ErrConversationBusy
	}
	worker := m.workers[conversation]
	if worker == nil {
		var err error
		worker, err = m.newWorkerLocked(conversation)
		if err != nil {
			m.mu.Unlock()
			return SubmissionResult{}, err
		}
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
	case <-worker.done:
		worker.submitMu.Unlock()
		if err := m.finishAdmission(worker); err != nil {
			return SubmissionResult{}, err
		}
		return SubmissionResult{}, m.workerError(worker)
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
	case <-worker.done:
		if err := m.finishAdmission(worker); err != nil {
			return SubmissionResult{}, err
		}
		return SubmissionResult{}, m.workerError(worker)
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

func (m *Manager) Capacity() CapacityStatus {
	queued := m.store.queued(m.reference(""))
	return m.capacity.snapshot(queued)
}

// wakeInteraction coalesces a durable lifecycle notification for one worker.
// If restart left no resident worker, it reconstructs one from dispatcher
// state. Callers notify only after the corresponding store commit succeeds.
func (m *Manager) wakeInteraction(conversation string) error {
	if err := ValidateConversation(conversation); err != nil {
		return err
	}
	m.mu.Lock()
	worker := m.workers[conversation]
	if worker != nil && !worker.closing {
		select {
		case worker.wake <- struct{}{}:
		default:
		}
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	snapshot, err := m.store.snapshot(m.reference(conversation))
	if err != nil || snapshot.queueLen == 0 || snapshot.waitingForInput {
		return err
	}
	return m.startDurableWorker(conversation)
}

func (m *Manager) Diagnostics() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.diagnostics...)
}

func (m *Manager) reconcileWorkspaces(ctx context.Context) error {
	reconciler, ok := m.workspaces.(WorkspaceReconciler)
	if !ok {
		return nil
	}
	records, err := m.store.workspaceRecords(m.reference(""))
	if err != nil {
		return err
	}
	claims := make(map[worktree.Assignment]int, len(records))
	for _, record := range records {
		claims[record.assignment]++
	}
	for _, record := range records {
		ref := conversationRef{agentID: m.project.AgentID, harness: m.driver.Name(), id: record.conversation, fingerprint: record.fingerprint}
		if claims[record.assignment] != 1 {
			_, inspectErr := reconciler.Inspect(ctx, record.conversation, record.assignment)
			detail := "multiple durable conversations claim this assignment"
			if inspectErr != nil {
				detail += "; ownership also could not be verified: " + inspectErr.Error()
			}
			m.diagnostics = append(m.diagnostics, fmt.Sprintf("worktree %s preserved: %s; repair durable ownership locally", record.assignment.Root, detail))
			continue
		}
		if record.retiring {
			if err := reconciler.Retire(ctx, record.conversation, record.assignment); err != nil {
				m.diagnostics = append(m.diagnostics, fmt.Sprintf("worktree %s preserved after interrupted cleanup: %v; repair the exact target and restart hctl", record.assignment.Root, err))
				continue
			}
			if err := m.store.clearRetiredWorkspace(ref, record.assignment); err != nil {
				return err
			}
			m.diagnostics = append(m.diagnostics, fmt.Sprintf("worktree %s retirement completed after interrupted cleanup", record.assignment.Root))
			continue
		}
		inspection, err := reconciler.Inspect(ctx, record.conversation, record.assignment)
		if err != nil {
			m.diagnostics = append(m.diagnostics, fmt.Sprintf("worktree %s preserved because ownership could not be verified: %v; repair it locally before this conversation can resume", record.assignment.Root, err))
			continue
		}
		reason := inspection.Reason
		switch {
		case record.busy:
			reason = "active or queued durable work"
		case record.uncertain:
			reason = "uncertain recovered work"
		}
		if record.busy || record.uncertain || !inspection.Clean || !inspection.Merged {
			m.diagnostics = append(m.diagnostics, fmt.Sprintf("worktree %s preserved: %s", record.assignment.Root, reason))
			continue
		}
		if err := m.store.markWorkspaceRetiring(ref); err != nil {
			return err
		}
		if err := reconciler.Retire(ctx, record.conversation, record.assignment); err != nil {
			m.diagnostics = append(m.diagnostics, fmt.Sprintf("worktree %s cleanup was interrupted: %v; durable ownership was preserved for retry", record.assignment.Root, err))
			continue
		}
		if err := m.store.clearRetiredWorkspace(ref, record.assignment); err != nil {
			return err
		}
		m.diagnostics = append(m.diagnostics, fmt.Sprintf("worktree %s retired after verifying it was inactive, clean, and merged", record.assignment.Root))
	}
	return nil
}

func statusFromSnapshot(snapshot conversationSnapshot, admissions int, resident bool) ConversationStatus {
	pending := max(snapshot.queueLen, admissions)
	status := ConversationStatus{State: LifecycleIdle, Pending: pending}
	if snapshot.waitingForInput {
		status.State = LifecycleWaiting
	} else if snapshot.active {
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

func (m *Manager) stopIdleWorker(conversation string) error {
	m.mu.Lock()
	worker := m.workers[conversation]
	if worker == nil {
		m.mu.Unlock()
		snapshot, err := m.store.snapshot(m.reference(conversation))
		if err != nil {
			return err
		}
		if snapshot.queueLen != 0 {
			return ErrConversationBusy
		}
		return nil
	}
	if worker.admissions != 0 || worker.closing {
		m.mu.Unlock()
		return ErrConversationBusy
	}
	worker.closing = true
	m.mu.Unlock()
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
	m.mu.Lock()
	if m.workers[conversation] == worker {
		delete(m.workers, conversation)
	}
	m.mu.Unlock()
	return nil
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
		if snapshot.retiring {
			m.mu.Unlock()
			return errors.New("conversation worktree retirement requires local recovery")
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
		m.capacity.shutdown()
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
	p := m.project
	policy := harness.PolicyReadOnly
	snapshot, err := m.store.snapshot(m.reference(worker.conversation))
	if err == nil && snapshot.workspace != "" {
		if m.workspaces == nil {
			err = errors.New("durable writable workspace cannot be resolved")
		} else {
			p, err = m.workspaces.Resolve(m.ctx, worker.conversation, worktree.Assignment{Root: snapshot.workspace, Branch: snapshot.branch})
			policy = harness.PolicyWorkspaceWrite
		}
	}
	if err != nil && snapshot.firstID != "" {
		event := Event{SchemaVersion: 1, Sequence: 1, Type: "driver.process_failed", Harness: m.driver.Name(), Conversation: worker.conversation, InputID: snapshot.firstID, SessionID: snapshot.sessionID, Status: "workspace_failure"}
		if emitErr := m.emit(worker.conversation, event); emitErr != nil {
			err = errors.Join(errDispatchEventDelivery, emitErr)
		}
	}
	if err == nil {
		err = runSubmissions(m.ctx, p, m.driver, worker.conversation, worker.submissions, func(event Event) error {
			m.mu.Lock()
			switch event.Type {
			case "driver.process_opened":
				worker.resident = true
			case "driver.process_hibernated":
				worker.resident = false
			}
			m.mu.Unlock()
			return m.emit(worker.conversation, event)
		}, runOptions{
			turnTimeout: m.turnTimeout, idleTimeout: m.idleTimeout, timers: m.timers,
			policy: policy, store: m.store, capacity: m.capacity,
			forceHibernate: worker.hibernate, wake: worker.wake,
		})
	}
	m.capacity.unregister(worker.conversation)
	m.mu.Lock()
	worker.err = err
	closing := worker.closing || m.closed || errors.Is(err, context.Canceled)
	if m.workers[worker.conversation] == worker {
		delete(m.workers, worker.conversation)
	}
	close(worker.done)
	if err != nil && !closing && errors.Is(err, errDispatchEventDelivery) && m.err == nil {
		m.err = err
		m.closed = true
		m.capacity.shutdown()
		m.cancel()
		m.doneOnce.Do(func() { close(m.done) })
	}
	m.mu.Unlock()
}

func (m *Manager) workerError(worker *managedConversation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if worker.err != nil {
		return worker.err
	}
	if m.err != nil {
		return m.err
	}
	if m.closed {
		return ErrManagerClosed
	}
	return ErrConversationBusy
}

func (m *Manager) newWorkerLocked(conversation string) (*managedConversation, error) {
	hibernate := make(chan struct{}, 1)
	if err := m.capacity.register(conversation, hibernate); err != nil {
		return nil, err
	}
	return &managedConversation{conversation: conversation, submissions: make(chan Submission, 32), done: make(chan struct{}), hibernate: hibernate, wake: make(chan struct{}, 1)}, nil
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
