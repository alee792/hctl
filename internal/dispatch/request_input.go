package dispatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"hctl/internal/harness"
	"hctl/internal/interaction"
)

var ErrRequestInputUnavailable = errors.New("managed interactive input is unavailable")

type RequestInputContext struct {
	ConversationID  string
	InputID         string
	CorrelationID   string
	ContinuationKey string
	Request         interaction.Request
}

type RequestInputHandler interface {
	Handle(context.Context, RequestInputContext) error
}

type interactionRequester interface {
	Request(interaction.OpenRequest) error
}

// CoordinatorRequestInputHandler is bound to one dispatcher conversation.
// Invocation on the dispatcher loop preserves the store's sole-writer order.
type CoordinatorRequestInputHandler struct {
	Coordinator     interactionRequester
	Owner           interaction.Owner
	Continuation    interaction.ContinuationMode
	ContinuationKey string
	Capabilities    interaction.Capabilities
}

func (h CoordinatorRequestInputHandler) Handle(_ context.Context, request RequestInputContext) error {
	if h.Coordinator == nil || h.Owner.Validate() != nil {
		return ErrRequestInputUnavailable
	}
	if err := interaction.ValidateRequest(request.Request); err != nil {
		return errors.New("interactive input request is invalid")
	}
	resolution, err := interaction.Resolve(request.Request, h.Capabilities)
	if err != nil {
		return errors.New("interactive input request has no available rendering or fallback")
	}
	digest := sha256.Sum256([]byte(request.ConversationID + "\x00" + request.InputID + "\x00" + request.CorrelationID))
	return h.Coordinator.Request(interaction.OpenRequest{
		InteractionID: "interaction_" + hex.EncodeToString(digest[:16]), InputID: request.InputID,
		Owner: h.Owner, Request: request.Request, Resolution: resolution,
		Continuation: h.Continuation, ContinuationKey: firstNonEmpty(request.ContinuationKey, h.ContinuationKey),
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func handleRequestInput(ctx context.Context, handler RequestInputHandler, conversation, inputID string, event *harness.RequestInputEvent) error {
	if event == nil {
		return nil
	}
	status := "unavailable"
	accepted := false
	var err error
	if !event.ProvenRoot() || handler == nil || event.CorrelationID == "" {
		err = ErrRequestInputUnavailable
	} else {
		err = handler.Handle(ctx, RequestInputContext{
			ConversationID: conversation, InputID: inputID, CorrelationID: event.CorrelationID,
			ContinuationKey: event.ContinuationKey,
			Request:         event.Request,
		})
		if err == nil {
			accepted = true
			status = "accepted"
		} else {
			status = "rejected"
		}
	}
	if event.Reply != nil {
		result := harness.RequestInputToolResult{}
		if accepted {
			result.Disposition = harness.RequestInputContinuationTurn
		}
		select {
		case event.Reply <- harness.RequestInputAcknowledgement{Accepted: accepted, Status: status, Result: result}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
