package codex

import (
	"context"
	"encoding/json"
	"errors"

	"hctl/internal/harness"
	"hctl/internal/interaction"
)

const continuationEnvelopeType = "hctl.channel_input_answer"

// Continuation resumes one persisted Codex thread for exactly one new turn.
// It never retains the app-server process after Resume returns.
type continuation struct {
	driver  *Driver
	request harness.OpenRequest
	session string
	emit    func(harness.Event)
}

func newContinuation(driver *Driver, request harness.OpenRequest, sessionID string, emit func(harness.Event)) (*continuation, error) {
	if driver == nil || request.Root == "" || sessionID == "" {
		return nil, errors.New("codex continuation requires a driver, workspace, and persisted thread")
	}
	if emit == nil {
		emit = func(harness.Event) {}
	}
	request.ResumeID = sessionID
	request.ManagedRequestInput = false
	return &continuation{driver: driver, request: request, session: sessionID, emit: emit}, nil
}

// Resume implements interaction.Continuation. A successful call is a later
// turn in the same Codex thread, never a resumed native tool callback.
func (c *continuation) Resume(ctx context.Context, intent interaction.ContinuationIntent) interaction.ContinuationResult {
	if intent.Mode != interaction.ContinuationTurn || intent.InteractionID == "" || intent.InputID == "" {
		return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
	}
	text, err := continuationText(intent)
	if err != nil {
		return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
	}
	session, err := c.driver.Open(ctx, c.request)
	if err != nil {
		return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
	}
	for _, event := range session.InitialEvents() {
		c.emit(event)
	}
	result, runErr := session.RunTurn(ctx, harness.Input{ID: intent.InputID, Text: text}, c.emit)
	if runErr != nil {
		session.Abort()
		return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain"}
	}
	if closeErr := session.Close(); closeErr != nil && result.Status == "" {
		return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain"}
	}
	if result.SessionID != c.session {
		return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain"}
	}
	switch result.Status {
	case "completed":
		return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed", ResultSessionID: result.SessionID, ResultTurnID: result.TurnID}
	case "failed", "cancelled":
		return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: result.Status, ResultSessionID: result.SessionID}
	default:
		return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain", ResultSessionID: result.SessionID}
	}
}

func (d *Driver) ContinueTurn(ctx context.Context, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	continuation, err := newContinuation(d, request, sessionID, emit)
	if err != nil {
		return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
	}
	return continuation.Resume(ctx, intent)
}

func continuationText(intent interaction.ContinuationIntent) (string, error) {
	envelope := struct {
		Type          string              `json:"type"`
		SchemaVersion int                 `json:"schema_version"`
		InteractionID string              `json:"interaction_id"`
		Request       interaction.Request `json:"request"`
		Answer        interaction.Answer  `json:"answer"`
	}{
		Type: continuationEnvelopeType, SchemaVersion: 1,
		InteractionID: intent.InteractionID, Request: intent.Request, Answer: intent.Answer,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > interaction.MaxRequestBytes*2 {
		return "", errors.New("codex continuation envelope is invalid")
	}
	return string(encoded), nil
}
