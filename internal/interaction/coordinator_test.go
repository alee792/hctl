package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCoordinatorCommitsBeforeRenderAndResume(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	renderer := rendererFunc(func(_ context.Context, _ RenderIntent) EffectOutcome {
		state, _ := store.Load()
		if state.Pending == nil || state.Pending.Delivery != DeliveryIntended {
			t.Fatalf("state at render = %#v", state.Pending)
		}
		return EffectSucceeded
	})
	continuation := continuationFunc(func(_ context.Context, _ ContinuationIntent) ContinuationResult {
		state, _ := store.Load()
		if state.Pending == nil || state.Pending.Phase != PhaseResuming || state.Pending.Resume != ResumeIntended {
			t.Fatalf("state at resume = %#v", state.Pending)
		}
		return ContinuationResult{Effect: EffectSucceeded, OriginOutcome: "completed"}
	})
	coordinator, err := NewCoordinator(store, renderer, continuation, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load()
	if state.Pending == nil || state.Pending.Phase != PhaseRendered || state.Pending.Delivery != DeliveryDelivered {
		t.Fatalf("rendered state = %#v", state.Pending)
	}

	answer := coordinatorTestAnswer(true)
	got, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: answer})
	if err != nil || got != AnswerAccepted {
		t.Fatalf("accept = %q, %v", got, err)
	}
	if err := coordinator.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load()
	if state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].Phase != PhaseCompleted {
		t.Fatalf("completed state = %#v", state)
	}
}

func TestCoordinatorNeverRetriesAmbiguousEffects(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	renders := 0
	coordinator, err := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome {
		renders++
		return EffectUncertain
	}), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectUncertain}
	}), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); !errors.Is(err, ErrRenderFailed) {
		t.Fatalf("open error = %v", err)
	}
	if err := coordinator.Recover(); err != nil {
		t.Fatal(err)
	}
	if renders != 1 {
		t.Fatalf("render calls = %d", renders)
	}
	state, _ := store.Load()
	if state.Pending == nil || state.Pending.Delivery != DeliveryUncertain {
		t.Fatalf("uncertain delivery = %#v", state.Pending)
	}

	if got, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: coordinatorTestAnswer(true)}); err != nil || got != AnswerAccepted {
		t.Fatalf("answer proving delivery = %q, %v", got, err)
	}
	if err := coordinator.Resume(context.Background()); !errors.Is(err, ErrResumeFailed) {
		t.Fatalf("resume error = %v", err)
	}
	if err := coordinator.Recover(); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load()
	if state.Pending == nil || state.Pending.Resume != ResumeUncertain {
		t.Fatalf("uncertain resume = %#v", state.Pending)
	}
	if err := coordinator.Resume(context.Background()); !errors.Is(err, ErrInteractionMissing) {
		t.Fatalf("uncertain resume was retried: %v", err)
	}
	if err := coordinator.ResolveResume(open.InteractionID, ResolveResumeFailed); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load()
	if state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].Phase != PhaseCancelled {
		t.Fatalf("explicit resume recovery = %#v", state)
	}
}

func TestCoordinatorDefiniteResumeFailureRequiresExplicitRecovery(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome { return EffectSucceeded }), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectFailed}
	}), func() time.Time { return now })
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: coordinatorTestAnswer(true)}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Resume(context.Background()); !errors.Is(err, ErrResumeFailed) {
		t.Fatalf("resume failure = %v", err)
	}
	state, _ := store.Load()
	if state.Pending == nil || state.Pending.Resume != ResumeFailed {
		t.Fatalf("failed resume state = %#v", state.Pending)
	}
	if err := coordinator.ResolveResume(open.InteractionID, ResolveResumeFailed); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRenderClaimIsAtMostOnce(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome {
		entered <- struct{}{}
		<-release
		return EffectSucceeded
	}), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return now })
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- coordinator.Render(context.Background(), open.InteractionID) }()
	<-entered
	if err := coordinator.Render(context.Background(), open.InteractionID); !errors.Is(err, ErrInteractionLate) {
		t.Fatalf("second render = %v", err)
	}
	select {
	case <-entered:
		t.Fatal("renderer was invoked twice")
	default:
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorDoesNotRenderExpiredRequest(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	renders := 0
	coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome {
		renders++
		return EffectSucceeded
	}), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return now })
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Duration(open.Request.Policy.ExpiresAfterSeconds) * time.Second)
	if err := coordinator.Render(context.Background(), open.InteractionID); !errors.Is(err, ErrInteractionLate) {
		t.Fatalf("expired render = %v", err)
	}
	if renders != 0 {
		t.Fatalf("renderer calls = %d", renders)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].Phase != PhaseExpired {
		t.Fatalf("expired state = %#v", state)
	}
}

func TestCoordinatorAnswerOwnershipIdempotencyAndConflict(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome { return EffectSucceeded }), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return now })
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	wrongOwner := open.Owner
	wrongOwner.PrincipalKey = strings.Repeat("c", 64)
	if _, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: wrongOwner, Answer: coordinatorTestAnswer(true)}); !errors.Is(err, ErrInteractionOwner) {
		t.Fatalf("wrong owner error = %v", err)
	}
	attempt := AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: coordinatorTestAnswer(true)}
	if got, err := coordinator.AcceptAnswer(attempt); err != nil || got != AnswerAccepted {
		t.Fatalf("first answer = %q, %v", got, err)
	}
	if got, err := coordinator.AcceptAnswer(attempt); err != nil || got != AnswerDuplicate {
		t.Fatalf("duplicate pending answer = %q, %v", got, err)
	}
	if _, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: coordinatorTestAnswer(false)}); !errors.Is(err, ErrInteractionConflict) {
		t.Fatalf("conflicting answer error = %v", err)
	}
	if err := coordinator.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := coordinator.AcceptAnswer(attempt); err != nil || got != AnswerDuplicate {
		t.Fatalf("duplicate terminal answer = %q, %v", got, err)
	}
	if _, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: coordinatorTestAnswer(false)}); !errors.Is(err, ErrInteractionLate) {
		t.Fatalf("late conflicting answer error = %v", err)
	}
}

func TestCoordinatorTerminalDuplicateUsesCanonicalAnswerDigest(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome { return EffectSucceeded }), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return now })
	request := Request{
		SchemaVersion: SchemaVersion, Kind: KindForm, Prompt: "Details",
		Policy: Policy{ExpiresAfterSeconds: MinExpirySeconds, Cancellation: CancellationAllowed},
		Fields: []Field{
			{ID: "alpha", Kind: KindText, Label: "Alpha", Required: true, MinLength: 1, MaxLength: 100},
			{ID: "beta", Kind: KindText, Label: "Beta", Required: true, MinLength: 1, MaxLength: 100},
		},
	}
	open := coordinatorTestOpen(now)
	open.Request = request
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	alpha := "one"
	betaCRLF := "two\r\nlines"
	first := Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit, Fields: []FieldAnswer{{FieldID: "beta", Text: &betaCRLF}, {FieldID: "alpha", Text: &alpha}}}
	if _, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: first}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	betaLF := "two\nlines"
	reordered := Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit, Fields: []FieldAnswer{{FieldID: "alpha", Text: &alpha}, {FieldID: "beta", Text: &betaLF}}}
	if disposition, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: reordered}); err != nil || disposition != AnswerDuplicate {
		t.Fatalf("terminal semantic duplicate = %q, %v", disposition, err)
	}
}

func TestCoordinatorTerminalDuplicateCanonicalizesChoiceFreeform(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome { return EffectSucceeded }), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return now })
	open := coordinatorTestOpen(now)
	open.Request = Request{
		SchemaVersion: SchemaVersion, Kind: KindChooseOne, Prompt: "Choose",
		Policy: Policy{ExpiresAfterSeconds: MinExpirySeconds, Cancellation: CancellationAllowed},
		Field:  &Field{ID: "choice", Kind: KindChooseOne, Label: "Choice", Required: true, Options: []Option{{ID: "known", Label: "Known", Value: "known"}}, AllowFreeform: true, MinSelections: 1, MaxSelections: 1, MinLength: 1, MaxLength: 100},
	}
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	value := "  custom value  "
	attempt := AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit, Fields: []FieldAnswer{{FieldID: "choice", Freeform: &value}}}}
	if _, err := coordinator.AcceptAnswer(attempt); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if disposition, err := coordinator.AcceptAnswer(attempt); err != nil || disposition != AnswerDuplicate {
		t.Fatalf("terminal freeform duplicate = %q, %v", disposition, err)
	}
}

func TestCoordinatorExpiryAndCancellationAreTerminal(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	store := &memoryLifecycleStore{}
	coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome { return EffectSucceeded }), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return now })
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	cancel := Answer{SchemaVersion: SchemaVersion, Action: ActionCancel}
	if got, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: cancel}); err != nil || got != AnswerCancelled {
		t.Fatalf("cancel = %q, %v", got, err)
	}
	state, _ := store.Load()
	if state.Pending != nil || state.Tombstones[0].Phase != PhaseCancelled {
		t.Fatalf("cancelled state = %#v", state)
	}

	store = &memoryLifecycleStore{}
	current := now
	coordinator, _ = NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome { return EffectSucceeded }), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return current })
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
		t.Fatal(err)
	}
	current = now.Add(2 * time.Minute)
	if err := coordinator.Expire(); err != nil {
		t.Fatal(err)
	}
	state, _ = store.Load()
	if state.Pending != nil || state.Tombstones[0].Phase != PhaseExpired {
		t.Fatalf("expired state = %#v", state)
	}
}

func TestCoordinatorConcurrentCancellationRecoveryIsIdempotent(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
		store := &memoryLifecycleStore{}
		coordinator, _ := NewCoordinator(store, rendererFunc(func(context.Context, RenderIntent) EffectOutcome { return EffectSucceeded }), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
			return ContinuationResult{Effect: EffectSucceeded}
		}), func() time.Time { return now })
		open := coordinatorTestOpen(now)
		if err := coordinator.Request(open); err != nil {
			t.Fatal(err)
		}
		if err := coordinator.Render(context.Background(), open.InteractionID); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			_, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: Answer{SchemaVersion: SchemaVersion, Action: ActionCancel}})
			results <- err
		}()
		go func() {
			<-start
			results <- coordinator.Recover()
		}()
		close(start)
		for count := 0; count < 2; count++ {
			if err := <-results; err != nil {
				t.Fatalf("iteration %d: %v", iteration, err)
			}
		}
		state, _ := store.Load()
		if state.Pending != nil || len(state.Tombstones) != 1 || state.Tombstones[0].Phase != PhaseCancelled {
			t.Fatalf("iteration %d state = %#v", iteration, state)
		}
	}
}

func TestCoordinatorCrashBoundariesDoNotRepeatSideEffects(t *testing.T) {
	now := time.Date(2026, 8, 7, 3, 0, 0, 0, time.UTC)
	base := &memoryLifecycleStore{}
	faults := &faultLifecycleStore{Store: base, failOpen: errors.New("persist request")}
	renders := 0
	resumes := 0
	coordinator, _ := NewCoordinator(faults, rendererFunc(func(context.Context, RenderIntent) EffectOutcome {
		renders++
		return EffectSucceeded
	}), continuationFunc(func(context.Context, ContinuationIntent) ContinuationResult {
		resumes++
		return ContinuationResult{Effect: EffectSucceeded}
	}), func() time.Time { return now })
	open := coordinatorTestOpen(now)
	if err := coordinator.Request(open); err == nil {
		t.Fatal("request persistence failure was hidden")
	}
	if renders != 0 || resumes != 0 {
		t.Fatalf("side effect before request commit: render=%d resume=%d", renders, resumes)
	}

	faults.failOpen = nil
	if err := coordinator.Request(open); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Recover(); err != nil {
		t.Fatal(err)
	}
	state, _ := base.Load()
	if state.Pending == nil || state.Pending.Delivery != DeliveryPending || renders != 0 {
		t.Fatalf("pre-render recovery changed request: %#v, renders=%d", state.Pending, renders)
	}
	faults.failNextUpdate = errors.New("persist rendered")
	faults.updatesBeforeFailure = 1
	if err := coordinator.Render(context.Background(), open.InteractionID); err == nil {
		t.Fatal("rendered commit failure was hidden")
	}
	state, _ = base.Load()
	if renders != 1 || state.Pending == nil || state.Pending.Delivery != DeliveryIntended {
		t.Fatalf("state after rendered commit failure = %#v, renders=%d", state.Pending, renders)
	}
	if err := coordinator.Recover(); err != nil {
		t.Fatal(err)
	}
	state, _ = base.Load()
	if renders != 1 || state.Pending.Delivery != DeliveryUncertain {
		t.Fatalf("render was repeated during recovery: %#v, renders=%d", state.Pending, renders)
	}
	if _, err := coordinator.AcceptAnswer(AnswerAttempt{InteractionID: open.InteractionID, Owner: open.Owner, Answer: coordinatorTestAnswer(true)}); err != nil {
		t.Fatal(err)
	}
	faults.failFinish = errors.New("persist completion")
	if err := coordinator.Resume(context.Background()); err == nil {
		t.Fatal("completion persistence failure was hidden")
	}
	state, _ = base.Load()
	if resumes != 1 || state.Pending == nil || state.Pending.Phase != PhaseResuming || state.Pending.Resume != ResumeIntended {
		t.Fatalf("state after completion commit failure = %#v, resumes=%d", state.Pending, resumes)
	}
	faults.failFinish = nil
	if err := coordinator.Recover(); err != nil {
		t.Fatal(err)
	}
	state, _ = base.Load()
	if resumes != 1 || state.Pending.Resume != ResumeUncertain {
		t.Fatalf("resume was repeated during recovery: %#v, resumes=%d", state.Pending, resumes)
	}
}

type memoryLifecycleStore struct {
	mu    sync.Mutex
	state DurableState
}

type faultLifecycleStore struct {
	Store
	failOpen             error
	failNextUpdate       error
	failFinish           error
	updatesBeforeFailure int
}

func (s *faultLifecycleStore) Open(lifecycle *Lifecycle) error {
	if s.failOpen != nil {
		return s.failOpen
	}
	return s.Store.Open(lifecycle)
}

func (s *faultLifecycleStore) Update(id string, change func(*Lifecycle) error) error {
	if s.failNextUpdate != nil {
		if s.updatesBeforeFailure > 0 {
			s.updatesBeforeFailure--
			return s.Store.Update(id, change)
		}
		err := s.failNextUpdate
		s.failNextUpdate = nil
		return err
	}
	return s.Store.Update(id, change)
}

func (s *faultLifecycleStore) Finish(finish FinishRequest) error {
	if s.failFinish != nil {
		return s.failFinish
	}
	return s.Store.Finish(finish)
}

func (s *memoryLifecycleStore) Load() (DurableState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDurableState(s.state)
}

func (s *memoryLifecycleStore) Open(lifecycle *Lifecycle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := cloneDurableState(s.state)
	if err != nil {
		return err
	}
	if next.Pending != nil {
		return ErrAlreadyPending
	}
	next.Pending = lifecycle
	if err := next.Validate(); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *memoryLifecycleStore) Update(id string, change func(*Lifecycle) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := cloneDurableState(s.state)
	if err != nil {
		return err
	}
	if next.Pending == nil || next.Pending.ID != id {
		return ErrInteractionMissing
	}
	if err := change(next.Pending); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *memoryLifecycleStore) Finish(finish FinishRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := cloneDurableState(s.state)
	if err != nil {
		return err
	}
	if next.Pending == nil || next.Pending.ID != finish.InteractionID {
		digest := Digest(finish.InteractionID)
		for _, tombstone := range next.Tombstones {
			if tombstone.InteractionDigest == digest && tombstone.Phase == finish.Phase && (finish.AnswerDigest == "" || finish.AnswerDigest == tombstone.AnswerDigest) {
				return nil
			}
		}
		return ErrInteractionMissing
	}
	pending := next.Pending
	if finish.AnswerDigest != "" && finish.AnswerDigest != pending.AnswerDigest {
		return ErrInteractionConflict
	}
	if finish.ResultSessionID != "" && finish.Phase != PhaseCompleted {
		return ErrInteractionLate
	}
	if finish.Phase == PhaseExpired && (pending.Phase == PhaseAnswered || pending.Phase == PhaseResuming || finish.FinishedAt.Before(pending.ExpiresAt)) {
		return ErrInteractionLate
	}
	if finish.Phase == PhaseCompleted {
		normal := pending.Phase == PhaseResuming && pending.Resume == ResumeIntended && !finish.Recovery
		recovered := pending.Phase == PhaseResuming && (pending.Resume == ResumeFailed || pending.Resume == ResumeUncertain) && finish.Recovery
		if !normal && !recovered {
			return ErrInteractionLate
		}
	}
	if finish.Phase == PhaseCancelled {
		renderFailed := pending.Phase == PhaseRequested && pending.Delivery == DeliveryIntended
		answerCancelled := pending.Phase == PhaseAnswered && pending.Answer != nil && pending.Answer.Action == ActionCancel
		resumeRecovered := pending.Phase == PhaseResuming && (pending.Resume == ResumeFailed || pending.Resume == ResumeUncertain) && finish.Recovery
		if !renderFailed && !answerCancelled && !resumeRecovered {
			return ErrInteractionLate
		}
	}
	answerDigest := finish.AnswerDigest
	if answerDigest == "" {
		answerDigest = pending.AnswerDigest
	}
	next.Tombstones = append(next.Tombstones, Tombstone{
		InteractionDigest: Digest(pending.ID),
		OwnerDigest:       Digest(pending.Owner.SurfaceKey + ":" + pending.Owner.PrincipalKey),
		AnswerDigest:      answerDigest,
		Phase:             finish.Phase,
		FinishedAt:        finish.FinishedAt,
	})
	next.Pending = nil
	if err := next.Validate(); err != nil {
		return err
	}
	s.state = next
	return nil
}

func cloneDurableState(state DurableState) (DurableState, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return DurableState{}, err
	}
	var clone DurableState
	if err := json.Unmarshal(data, &clone); err != nil {
		return DurableState{}, err
	}
	return clone, nil
}

type rendererFunc func(context.Context, RenderIntent) EffectOutcome

func (f rendererFunc) Render(ctx context.Context, intent RenderIntent) EffectOutcome {
	return f(ctx, intent)
}

type continuationFunc func(context.Context, ContinuationIntent) ContinuationResult

func (f continuationFunc) Resume(ctx context.Context, intent ContinuationIntent) ContinuationResult {
	return f(ctx, intent)
}

func coordinatorTestOpen(now time.Time) OpenRequest {
	return OpenRequest{
		InteractionID: "interaction_1234567890", InputID: "message-1",
		Owner:        Owner{SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)},
		Request:      lifecycleTestRequest(),
		Resolution:   Resolution{Mode: RenderNative},
		Continuation: ContinuationTurn,
	}
}

func coordinatorTestAnswer(value bool) Answer {
	return Answer{SchemaVersion: SchemaVersion, Action: ActionSubmit, Fields: []FieldAnswer{{FieldID: "approved", Confirmed: &value}}}
}
