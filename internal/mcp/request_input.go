package mcp

import (
	"hctl/internal/harness"
	"hctl/internal/interaction"
)

const requestInputToolName = "channel.request_input"

func requestInputDefinition() map[string]any {
	return map[string]any{
		"name":        requestInputToolName,
		"description": "Pause this channel conversation and request bounded structured input from the authorized user. Use only when the missing answer materially changes the work.",
		"inputSchema": interaction.RequestJSONSchema(),
		"outputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"disposition": map[string]any{"type": "string", "enum": []string{string(harness.RequestInputDeferred), string(harness.RequestInputContinuationTurn)}}},
			"required":   []string{"disposition"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": false, "openWorldHint": false},
	}
}
