package dispatch

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestCapacityCoordinatorAdmitsTurnsFIFO(t *testing.T) {
	capacity, err := newCapacityCoordinator(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []string{"one", "two"} {
		if err := capacity.register(conversation, make(chan struct{}, 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := capacity.acquireTurn(context.Background(), "one", true); err != nil {
		t.Fatal(err)
	}
	granted := make(chan string, 2)
	go func() {
		if capacity.acquireTurn(context.Background(), "two", true) == nil {
			granted <- "two"
		}
	}()
	waitCapacityQueued(t, capacity, "two")
	capacity.releaseTurn("one", true)
	go func() {
		if capacity.acquireTurn(context.Background(), "one", false) == nil {
			granted <- "one"
		}
	}()
	if got := waitCapacityGrant(t, granted); got != "two" {
		t.Fatalf("first grant = %s, want two", got)
	}
	capacity.releaseTurn("two", false)
	if got := waitCapacityGrant(t, granted); got != "one" {
		t.Fatalf("second grant = %s, want one", got)
	}
}

func waitCapacityQueued(t *testing.T, capacity *capacityCoordinator, conversation string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		capacity.mu.Lock()
		waiting := capacity.waitingLocked(conversation)
		capacity.mu.Unlock()
		if waiting {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s to queue", conversation)
		default:
			runtime.Gosched()
		}
	}
}

func TestCapacityCoordinatorRequestsOldestIdleHibernation(t *testing.T) {
	capacity, err := newCapacityCoordinator(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	hibernateOne := make(chan struct{}, 1)
	if err := capacity.register("one", hibernateOne); err != nil {
		t.Fatal(err)
	}
	if err := capacity.register("two", make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if err := capacity.acquireTurn(context.Background(), "one", true); err != nil {
		t.Fatal(err)
	}
	capacity.releaseTurn("one", false)
	granted := make(chan struct{}, 1)
	go func() {
		if capacity.acquireTurn(context.Background(), "two", true) == nil {
			granted <- struct{}{}
		}
	}()
	select {
	case <-hibernateOne:
	case <-time.After(time.Second):
		t.Fatal("idle resident was not selected for hibernation")
	}
	select {
	case <-granted:
		t.Fatal("replacement was granted before resident capacity was released")
	default:
	}
	capacity.releaseResident("one")
	select {
	case <-granted:
	case <-time.After(time.Second):
		t.Fatal("replacement did not receive released resident capacity")
	}
}

func TestCapacityCoordinatorSelectsLeastRecentlyIdleResident(t *testing.T) {
	capacity, err := newCapacityCoordinator(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	hibernateOne := make(chan struct{}, 1)
	hibernateTwo := make(chan struct{}, 1)
	for conversation, hibernate := range map[string]chan struct{}{"one": hibernateOne, "two": hibernateTwo, "three": make(chan struct{}, 1)} {
		if err := capacity.register(conversation, hibernate); err != nil {
			t.Fatal(err)
		}
	}
	if err := capacity.acquireTurn(context.Background(), "one", true); err != nil {
		t.Fatal(err)
	}
	capacity.releaseTurn("one", false)
	if err := capacity.acquireTurn(context.Background(), "two", true); err != nil {
		t.Fatal(err)
	}
	capacity.releaseTurn("two", false)
	done := make(chan error, 1)
	go func() { done <- capacity.acquireTurn(context.Background(), "three", true) }()
	select {
	case <-hibernateOne:
	case <-hibernateTwo:
		t.Fatal("more recently idle resident was selected")
	case <-time.After(time.Second):
		t.Fatal("no idle resident was selected")
	}
	capacity.releaseResident("one")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	capacity.releaseTurn("three", false)
	capacity.releaseResident("three")
}

func TestCapacityCoordinatorCancellationDoesNotLeakCapacity(t *testing.T) {
	capacity, err := newCapacityCoordinator(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := capacity.register("one", make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if err := capacity.register("two", make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if err := capacity.acquireTurn(context.Background(), "one", true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- capacity.acquireTurn(ctx, "two", true) }()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled capacity request succeeded")
	}
	status := capacity.snapshot(0)
	if status.Active != 1 || status.Resident != 1 {
		t.Fatalf("capacity leaked after cancellation: %+v", status)
	}
}

func TestCapacityCoordinatorShutdownStopsWaitingAdmission(t *testing.T) {
	capacity, err := newCapacityCoordinator(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := capacity.register("one", make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if err := capacity.register("two", make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if err := capacity.acquireTurn(context.Background(), "one", true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- capacity.acquireTurn(context.Background(), "two", true) }()
	waitCapacityQueued(t, capacity, "two")
	capacity.shutdown()
	if err := <-done; !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("shutdown admission = %v", err)
	}
	capacity.releaseTurn("one", false)
	capacity.releaseResident("one")
	if status := capacity.snapshot(0); status.Active != 0 || status.Resident != 0 {
		t.Fatalf("shutdown leaked capacity: %+v", status)
	}
}

func TestCapacityCoordinatorHandsOffQueuedResidentWhenWaiterArrivesBetweenTurns(t *testing.T) {
	capacity, err := newCapacityCoordinator(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	hibernateOne := make(chan struct{}, 1)
	if err := capacity.register("one", hibernateOne); err != nil {
		t.Fatal(err)
	}
	if err := capacity.register("two", make(chan struct{}, 1)); err != nil {
		t.Fatal(err)
	}
	if err := capacity.acquireTurn(context.Background(), "one", true); err != nil {
		t.Fatal(err)
	}
	capacity.releaseTurn("one", true)
	twoGranted := make(chan error, 1)
	go func() { twoGranted <- capacity.acquireTurn(context.Background(), "two", true) }()
	select {
	case <-hibernateOne:
	case <-time.After(time.Second):
		t.Fatal("queued resident was not selected for between-turn handoff")
	}
	if err := capacity.acquireTurn(context.Background(), "one", false); !errors.Is(err, errCapacityHibernation) {
		t.Fatalf("queued resident reacquire = %v", err)
	}
	capacity.releaseResident("one")
	if err := <-twoGranted; err != nil {
		t.Fatal(err)
	}
	capacity.releaseTurn("two", false)
	capacity.releaseResident("two")
}

func waitCapacityGrant(t *testing.T, granted <-chan string) string {
	t.Helper()
	select {
	case value := <-granted:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for capacity grant")
		return ""
	}
}
