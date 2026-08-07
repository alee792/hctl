package claude

import (
	"context"
	"errors"

	"hctl/internal/harness"
	"hctl/internal/interaction"
)

// ResumeDeferredTool re-enters one exact, already-durable tool invocation.
// Scheduling, capacity, workspace selection, and commit ordering stay owned by
// dispatch.Manager; this adapter owns only Claude's native replay protocol.
func (d *Driver) ResumeDeferredTool(ctx context.Context, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	if intent.Mode != interaction.ContinuationNativeDeferredTool || sessionID == "" || intent.ContinuationKey == "" {
		return failedDeferredResult()
	}
	updated, digest, err := BuildDeferredUpdatedInput(intent.Request, intent.Answer, intent.ContinuationKey)
	if err != nil {
		return failedDeferredResult()
	}
	request.ResumeID = sessionID
	request.ManagedRequestInput = false
	request.Deferred = &harness.DeferredToolResume{
		ToolUseID: intent.ContinuationKey, ToolName: ManagedRequestInputTool,
		InputDigest: digest, UpdatedInput: updated,
	}
	process, err := d.Open(ctx, request)
	if err != nil {
		return failedDeferredResult()
	}
	result, runErr := process.RunTurn(ctx, harness.Input{ID: intent.InputID}, emit)
	if runErr != nil {
		process.Abort()
		if errors.Is(runErr, ErrDeferredUnavailable) || errors.Is(runErr, ErrDeferredMismatch) || errors.Is(runErr, ErrDeferredParallel) || errors.Is(runErr, ErrDeferredSessionLost) {
			return failedDeferredResult()
		}
		return uncertainDeferredResult()
	}
	if err := process.Close(); err != nil || result.Status != "completed" || result.SessionID == "" {
		return uncertainDeferredResult()
	}
	return interaction.ContinuationResult{
		Effect: interaction.EffectSucceeded, OriginOutcome: "completed",
		ResultSessionID: result.SessionID, ResultTurnID: intent.InputID,
	}
}

func failedDeferredResult() interaction.ContinuationResult {
	return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
}

func uncertainDeferredResult() interaction.ContinuationResult {
	return interaction.ContinuationResult{Effect: interaction.EffectUncertain, OriginOutcome: "uncertain"}
}
