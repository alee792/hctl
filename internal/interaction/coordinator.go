package interaction

import (
	"context"
	"errors"
	"time"
)

var (
	ErrAlreadyPending      = errors.New("interaction already pending")
	ErrInteractionMissing  = errors.New("interaction unavailable")
	ErrInteractionOwner    = errors.New("interaction owner mismatch")
	ErrInteractionConflict = errors.New("interaction answer conflicts")
	ErrInteractionLate     = errors.New("interaction is no longer accepting answers")
	ErrRenderFailed        = errors.New("interaction render failed")
	ErrResumeFailed        = errors.New("interaction resume failed")
)

type EffectOutcome string

const (
	EffectSucceeded EffectOutcome = "succeeded"
	EffectFailed    EffectOutcome = "failed"
	EffectUncertain EffectOutcome = "uncertain"
)

type Store interface {
	Load() (DurableState, error)
	Open(*Lifecycle) error
	Update(string, func(*Lifecycle) error) error
	Finish(FinishRequest) error
}

type RenderIntent struct {
	InteractionID string
	InputID       string
	Owner         Owner
	Request       Request
	Resolution    Resolution
}

type Renderer interface {
	Render(context.Context, RenderIntent) EffectOutcome
}

type ContinuationIntent struct {
	InteractionID   string
	InputID         string
	Mode            ContinuationMode
	ContinuationKey string
	Request         Request
	Answer          Answer
}

type ContinuationResult struct {
	Effect          EffectOutcome
	OriginOutcome   string
	ResultSessionID string
	ResultTurnID    string
}

type Continuation interface {
	Resume(context.Context, ContinuationIntent) ContinuationResult
}

type committedContinuation interface {
	Committed(ContinuationIntent, ContinuationResult) error
}

type OpenRequest struct {
	InteractionID   string
	InputID         string
	Owner           Owner
	Request         Request
	Resolution      Resolution
	Continuation    ContinuationMode
	ContinuationKey string
}

type AnswerAttempt struct {
	InteractionID string
	Owner         Owner
	Answer        Answer
}

type AnswerDisposition string

const (
	AnswerAccepted  AnswerDisposition = "accepted"
	AnswerDuplicate AnswerDisposition = "duplicate"
	AnswerCancelled AnswerDisposition = "cancelled"
)

type ResumeResolution string

const (
	ResolveResumeCompleted ResumeResolution = "completed"
	ResolveResumeFailed    ResumeResolution = "failed"
)

type Coordinator struct {
	store        Store
	renderer     Renderer
	continuation Continuation
	now          func() time.Time
}

func NewCoordinator(store Store, renderer Renderer, continuation Continuation, now func() time.Time) (*Coordinator, error) {
	if store == nil || renderer == nil || continuation == nil {
		return nil, errors.New("interaction coordinator dependencies are required")
	}
	if now == nil {
		now = time.Now
	}
	return &Coordinator{store: store, renderer: renderer, continuation: continuation, now: now}, nil
}

// Request atomically parks the originating dispatcher input and records a
// delivery intent. Render is deliberately separate so a harness can exit and
// release its process before network delivery begins.
func (c *Coordinator) Request(open OpenRequest) error {
	expiresAt := c.now().UTC().Truncate(time.Second).Add(time.Duration(open.Request.Policy.ExpiresAfterSeconds) * time.Second)
	lifecycle := &Lifecycle{
		ID: open.InteractionID, InputID: open.InputID, Owner: open.Owner,
		Request: open.Request, Resolution: open.Resolution, ExpiresAt: expiresAt,
		Continuation: open.Continuation, ContinuationKey: open.ContinuationKey,
		Phase: PhaseRequested, Delivery: DeliveryPending,
	}
	if err := lifecycle.Validate(); err != nil {
		return err
	}
	return c.store.Open(lifecycle)
}

// Render performs at most one delivery attempt. Every non-successful or
// unrecognized result is durably classified and is never automatically retried.
func (c *Coordinator) Render(ctx context.Context, interactionID string) error {
	state, err := c.store.Load()
	if err != nil {
		return err
	}
	pending := state.Pending
	if pending == nil || pending.ID != interactionID {
		return ErrInteractionMissing
	}
	now := c.nowUTC()
	expired := false
	if err := c.store.Update(interactionID, func(current *Lifecycle) error {
		if current.Phase != PhaseRequested || current.Delivery != DeliveryPending {
			return ErrInteractionLate
		}
		if !now.Before(current.ExpiresAt) {
			expired = true
			return ErrInteractionLate
		}
		current.Delivery = DeliveryIntended
		return nil
	}); err != nil {
		if expired {
			if finishErr := c.store.Finish(FinishRequest{InteractionID: interactionID, Phase: PhaseExpired, OriginOutcome: "expired", FinishedAt: now}); finishErr != nil {
				return finishErr
			}
		}
		return err
	}
	intent := RenderIntent{
		InteractionID: pending.ID, InputID: pending.InputID, Owner: pending.Owner,
		Request: pending.Request, Resolution: pending.Resolution,
	}
	switch c.renderer.Render(ctx, intent) {
	case EffectSucceeded:
		return c.store.Update(interactionID, func(current *Lifecycle) error {
			if current.Phase != PhaseRequested || current.Delivery != DeliveryIntended {
				return ErrInteractionLate
			}
			current.Delivery = DeliveryDelivered
			current.Phase = PhaseRendered
			return nil
		})
	case EffectFailed:
		if err := c.store.Finish(FinishRequest{InteractionID: interactionID, Phase: PhaseCancelled, OriginOutcome: "failed", FinishedAt: c.nowUTC()}); err != nil {
			return err
		}
		return ErrRenderFailed
	default:
		if err := c.markDeliveryUncertain(interactionID); err != nil {
			return err
		}
		return ErrRenderFailed
	}
}

func (c *Coordinator) AcceptAnswer(attempt AnswerAttempt) (AnswerDisposition, error) {
	attemptDigest, err := DigestAnswer(attempt.Answer)
	if err != nil {
		return "", errors.New("interaction answer is invalid")
	}
	disposition := AnswerAccepted
	expired := false
	acceptedDigest := ""
	err = c.store.Update(attempt.InteractionID, func(pending *Lifecycle) error {
		if pending.Owner != attempt.Owner {
			return ErrInteractionOwner
		}
		if !c.nowUTC().Before(pending.ExpiresAt) && pending.Phase != PhaseAnswered && pending.Phase != PhaseResuming {
			expired = true
			return ErrInteractionLate
		}
		normalized, err := NormalizeAnswer(pending.Request, attempt.Answer)
		if err != nil {
			return errors.New("interaction answer is invalid")
		}
		normalizedDigest, err := DigestAnswer(normalized)
		if err != nil {
			return err
		}
		if pending.Phase == PhaseAnswered || pending.Phase == PhaseResuming {
			if pending.AnswerDigest == normalizedDigest {
				disposition = AnswerDuplicate
				return nil
			}
			return ErrInteractionConflict
		}
		if pending.Phase != PhaseRendered && (pending.Phase != PhaseRequested || pending.Delivery != DeliveryIntended && pending.Delivery != DeliveryUncertain) {
			return ErrInteractionLate
		}
		pending.Delivery = DeliveryDelivered
		pending.Answer = &normalized
		pending.AnswerDigest = normalizedDigest
		acceptedDigest = normalizedDigest
		pending.Phase = PhaseAnswered
		pending.Resume = ResumePending
		if normalized.Action == ActionCancel {
			disposition = AnswerCancelled
		}
		return nil
	})
	if errors.Is(err, ErrInteractionMissing) {
		return c.classifyTerminal(attempt, attemptDigest)
	}
	if expired {
		finishErr := c.store.Finish(FinishRequest{InteractionID: attempt.InteractionID, Phase: PhaseExpired, OriginOutcome: "expired", FinishedAt: c.nowUTC()})
		if finishErr != nil && !errors.Is(finishErr, ErrInteractionMissing) {
			return disposition, finishErr
		}
		return disposition, ErrInteractionLate
	}
	if err != nil {
		return disposition, err
	}
	if disposition == AnswerCancelled {
		if err := c.store.Finish(FinishRequest{InteractionID: attempt.InteractionID, Phase: PhaseCancelled, AnswerDigest: acceptedDigest, OriginOutcome: "cancelled", FinishedAt: c.nowUTC()}); err != nil {
			return disposition, err
		}
	}
	return disposition, nil
}

// Resume commits intent before invoking the harness continuation. A failed or
// ambiguous invocation remains in the monotonic resuming phase for explicit
// recovery; it is never made silently retryable.
func (c *Coordinator) Resume(ctx context.Context) error {
	state, err := c.store.Load()
	if err != nil {
		return err
	}
	if state.Pending == nil {
		return ErrInteractionMissing
	}
	var intent ContinuationIntent
	interactionID := state.Pending.ID
	if err := c.store.Update(interactionID, func(pending *Lifecycle) error {
		if pending.Phase != PhaseAnswered || pending.Resume != ResumePending || pending.Answer == nil || pending.Answer.Action == ActionCancel {
			return ErrInteractionMissing
		}
		intent = ContinuationIntent{InteractionID: pending.ID, InputID: pending.InputID, Mode: pending.Continuation, ContinuationKey: pending.ContinuationKey, Request: pending.Request, Answer: *pending.Answer}
		pending.Phase = PhaseResuming
		pending.Resume = ResumeIntended
		return nil
	}); err != nil {
		return err
	}

	result := c.continuation.Resume(ctx, intent)
	switch result.Effect {
	case EffectSucceeded:
		if result.OriginOutcome == "" {
			result.OriginOutcome = "completed"
		}
		if err := c.store.Finish(FinishRequest{
			InteractionID: interactionID, Phase: PhaseCompleted, OriginOutcome: result.OriginOutcome,
			ResultSessionID: result.ResultSessionID, FinishedAt: c.nowUTC(),
		}); err != nil {
			return err
		}
		if committed, ok := c.continuation.(committedContinuation); ok {
			return committed.Committed(intent, result)
		}
		return nil
	case EffectFailed:
		if err := c.store.Update(interactionID, func(pending *Lifecycle) error {
			if pending.Phase != PhaseResuming || pending.Resume != ResumeIntended {
				return ErrInteractionLate
			}
			pending.Resume = ResumeFailed
			return nil
		}); err != nil {
			return err
		}
		return ErrResumeFailed
	default:
		if err := c.markResumeUncertain(interactionID); err != nil {
			return err
		}
		return ErrResumeFailed
	}
}

func (c *Coordinator) Recover() error {
	state, err := c.store.Load()
	if err != nil || state.Pending == nil {
		return err
	}
	pending := state.Pending
	if pending.Phase == PhaseAnswered && pending.Answer != nil && pending.Answer.Action == ActionCancel {
		return c.store.Finish(FinishRequest{InteractionID: pending.ID, Phase: PhaseCancelled, AnswerDigest: pending.AnswerDigest, OriginOutcome: "cancelled", FinishedAt: c.nowUTC()})
	}
	if pending.Phase == PhaseRequested && pending.Delivery == DeliveryIntended {
		return c.markDeliveryUncertain(pending.ID)
	}
	if pending.Phase == PhaseResuming && pending.Resume == ResumeIntended {
		return c.markResumeUncertain(pending.ID)
	}
	return nil
}

func (c *Coordinator) Expire() error {
	state, err := c.store.Load()
	if err != nil || state.Pending == nil {
		return err
	}
	pending := state.Pending
	if pending.Phase == PhaseAnswered || pending.Phase == PhaseResuming || c.nowUTC().Before(pending.ExpiresAt) {
		return ErrInteractionLate
	}
	return c.store.Finish(FinishRequest{InteractionID: pending.ID, Phase: PhaseExpired, OriginOutcome: "expired", FinishedAt: c.nowUTC()})
}

// ResolveResume is an explicit operator recovery action for a continuation
// whose outcome cannot be safely retried. It never invokes the harness.
func (c *Coordinator) ResolveResume(interactionID string, resolution ResumeResolution) error {
	finish := FinishRequest{InteractionID: interactionID, FinishedAt: c.nowUTC(), Recovery: true}
	switch resolution {
	case ResolveResumeCompleted:
		finish.Phase = PhaseCompleted
		finish.OriginOutcome = "completed"
	case ResolveResumeFailed:
		finish.Phase = PhaseCancelled
		finish.OriginOutcome = "failed"
	default:
		return errors.New("interaction resume recovery resolution is invalid")
	}
	return c.store.Finish(finish)
}

func (c *Coordinator) classifyTerminal(attempt AnswerAttempt, answerDigest string) (AnswerDisposition, error) {
	state, err := c.store.Load()
	if err != nil {
		return "", err
	}
	interactionDigest := Digest(attempt.InteractionID)
	ownerDigest := Digest(attempt.Owner.SurfaceKey + ":" + attempt.Owner.PrincipalKey)
	for index := len(state.Tombstones) - 1; index >= 0; index-- {
		tombstone := state.Tombstones[index]
		if tombstone.InteractionDigest != interactionDigest {
			continue
		}
		if tombstone.OwnerDigest != ownerDigest {
			return "", ErrInteractionOwner
		}
		if tombstone.AnswerDigest != "" && tombstone.AnswerDigest == answerDigest {
			return AnswerDuplicate, nil
		}
		return "", ErrInteractionLate
	}
	return "", ErrInteractionMissing
}

func (c *Coordinator) markDeliveryUncertain(id string) error {
	return c.store.Update(id, func(pending *Lifecycle) error {
		if pending.Phase != PhaseRequested || pending.Delivery != DeliveryIntended {
			return ErrInteractionLate
		}
		pending.Delivery = DeliveryUncertain
		return nil
	})
}

func (c *Coordinator) markResumeUncertain(id string) error {
	return c.store.Update(id, func(pending *Lifecycle) error {
		if pending.Phase != PhaseResuming || pending.Resume != ResumeIntended {
			return ErrInteractionLate
		}
		pending.Resume = ResumeUncertain
		return nil
	})
}

func (c *Coordinator) nowUTC() time.Time { return c.now().UTC().Truncate(time.Second) }
