package dispatch

import (
	"context"
	"errors"
	"sync"
)

var errCapacityHibernation = errors.New("resident session must hibernate for capacity")

const (
	DefaultMaxResidentSessions = 4
	DefaultMaxActiveTurns      = 2
)

type CapacityStatus struct {
	Resident      int
	ResidentLimit int
	Active        int
	ActiveLimit   int
	Queued        int
}

type capacityCoordinator struct {
	mu            sync.Mutex
	residentLimit int
	activeLimit   int
	resident      int
	active        int
	sequence      uint64
	states        map[string]*capacityState
	waiters       []*capacityWaiter
	closed        bool
	done          chan struct{}
}

type capacityState struct {
	resident    bool
	active      bool
	queued      bool
	hibernating bool
	idleSince   uint64
	hibernate   chan<- struct{}
}

type capacityWaiter struct {
	conversation  string
	needsResident bool
	grant         chan struct{}
	granted       bool
}

func newCapacityCoordinator(residentLimit, activeLimit int) (*capacityCoordinator, error) {
	if residentLimit <= 0 || activeLimit <= 0 || activeLimit > residentLimit {
		return nil, errors.New("managed session capacity limits are invalid")
	}
	return &capacityCoordinator{residentLimit: residentLimit, activeLimit: activeLimit, states: map[string]*capacityState{}, done: make(chan struct{})}, nil
}

func (c *capacityCoordinator) register(conversation string, hibernate chan<- struct{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.states[conversation]; exists {
		return errors.New("managed conversation is already registered for capacity")
	}
	c.states[conversation] = &capacityState{hibernate: hibernate}
	return nil
}

func (c *capacityCoordinator) unregister(conversation string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[conversation]
	if state == nil || state.active || state.resident {
		return
	}
	delete(c.states, conversation)
}

func (c *capacityCoordinator) acquireTurn(ctx context.Context, conversation string, needsResident bool) error {
	waiter := &capacityWaiter{conversation: conversation, needsResident: needsResident, grant: make(chan struct{}, 1)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrManagerClosed
	}
	state := c.states[conversation]
	if state == nil {
		c.mu.Unlock()
		return errors.New("managed conversation is not registered for capacity")
	}
	if state.hibernating && !needsResident {
		c.mu.Unlock()
		return errCapacityHibernation
	}
	if state.active || c.waitingLocked(conversation) || (!needsResident && !state.resident) {
		c.mu.Unlock()
		return errors.New("managed conversation capacity state is inconsistent")
	}
	state.queued = true
	c.waiters = append(c.waiters, waiter)
	c.scheduleLocked()
	c.mu.Unlock()

	select {
	case <-waiter.grant:
		return nil
	case <-c.done:
		if c.cancelWaiter(waiter) {
			return nil
		}
		return ErrManagerClosed
	case <-ctx.Done():
		if c.cancelWaiter(waiter) {
			return nil
		}
		return ctx.Err()
	}
}

func (c *capacityCoordinator) cancelWaiter(waiter *capacityWaiter) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if waiter.granted {
		return true
	}
	for index, candidate := range c.waiters {
		if candidate == waiter {
			c.waiters = append(c.waiters[:index], c.waiters[index+1:]...)
			break
		}
	}
	if state := c.states[waiter.conversation]; state != nil {
		state.queued = false
	}
	c.scheduleLocked()
	return false
}

func (c *capacityCoordinator) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	close(c.done)
}

func (c *capacityCoordinator) releaseTurn(conversation string, queued bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[conversation]
	if state == nil || !state.active {
		return false
	}
	state.active = false
	state.queued = queued
	c.active--
	c.sequence++
	state.idleSince = c.sequence
	if queued && c.waitingForResidentLocked() {
		state.hibernating = true
		return true
	}
	c.scheduleLocked()
	return false
}

func (c *capacityCoordinator) releaseResident(conversation string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.states[conversation]
	if state == nil || !state.resident {
		return
	}
	state.resident = false
	state.hibernating = false
	c.resident--
	c.scheduleLocked()
}

func (c *capacityCoordinator) snapshot(queued int) CapacityStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CapacityStatus{Resident: c.resident, ResidentLimit: c.residentLimit, Active: c.active, ActiveLimit: c.activeLimit, Queued: queued}
}

func (c *capacityCoordinator) scheduleLocked() {
	if c.closed {
		return
	}
	for len(c.waiters) > 0 && c.active < c.activeLimit {
		waiter := c.waiters[0]
		state := c.states[waiter.conversation]
		if state == nil {
			c.waiters = c.waiters[1:]
			continue
		}
		if waiter.needsResident && !state.resident && c.resident >= c.residentLimit {
			victim := c.oldestIdleLocked(waiter.conversation)
			if victim == nil {
				victim = c.oldestQueuedResidentLocked(waiter.conversation)
			}
			if victim != nil && !victim.hibernating {
				victim.hibernating = true
				select {
				case victim.hibernate <- struct{}{}:
				default:
				}
			}
			return
		}
		if waiter.needsResident && !state.resident {
			state.resident = true
			c.resident++
		}
		state.active = true
		state.queued = false
		c.active++
		waiter.granted = true
		c.waiters = c.waiters[1:]
		waiter.grant <- struct{}{}
	}
}

func (c *capacityCoordinator) oldestQueuedResidentLocked(exclude string) *capacityState {
	var selected *capacityState
	for conversation, state := range c.states {
		if conversation == exclude || !state.resident || state.active || !state.queued || state.hibernating || c.waitingLocked(conversation) {
			continue
		}
		if selected == nil || state.idleSince < selected.idleSince {
			selected = state
		}
	}
	return selected
}

func (c *capacityCoordinator) oldestIdleLocked(exclude string) *capacityState {
	var selected *capacityState
	for conversation, state := range c.states {
		if conversation == exclude || !state.resident || state.active || state.queued || state.hibernating {
			continue
		}
		if selected == nil || state.idleSince < selected.idleSince {
			selected = state
		}
	}
	return selected
}

func (c *capacityCoordinator) waitingLocked(conversation string) bool {
	for _, waiter := range c.waiters {
		if waiter.conversation == conversation {
			return true
		}
	}
	return false
}

func (c *capacityCoordinator) waitingForResidentLocked() bool {
	if len(c.waiters) == 0 || c.resident < c.residentLimit {
		return false
	}
	waiter := c.waiters[0]
	state := c.states[waiter.conversation]
	return waiter.needsResident && state != nil && !state.resident
}
