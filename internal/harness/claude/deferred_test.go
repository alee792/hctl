package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hctl/internal/harness"
	"hctl/internal/interaction"
)

func TestDeferredHookDefersThenAllowsOnlyExactCall(t *testing.T) {
	request := deferredRequest(t)
	hookInput := hookJSON(t, "toolu_exact", request)
	initialBroker, err := startDeferredBroker(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer initialBroker.Close()
	if info, err := os.Stat(filepath.Dir(initialBroker.path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("broker directory permissions = %v, %v", info, err)
	}
	var output bytes.Buffer
	if err := RunDeferredHook(strings.NewReader(hookInput), &output, initialBroker.path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"permissionDecision":"defer"`) || strings.Contains(output.String(), "updatedInput") {
		t.Fatalf("defer output = %s", output.String())
	}
	digest, err := RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	updated := append(bytes.TrimSuffix(request, []byte("}")), []byte(`,"_hctl_response":{"tool_use_id":"toolu_exact","answer":{"schema_version":1,"action":"submit","fields":[{"field_id":"ok","confirmed":true}]}}}`)...)
	resumeBroker, err := startDeferredBroker(&harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated})
	if err != nil {
		t.Fatal(err)
	}
	defer resumeBroker.Close()
	output.Reset()
	if err := RunDeferredHook(strings.NewReader(hookInput), &output, resumeBroker.path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"permissionDecision":"allow"`) || !strings.Contains(output.String(), `"_hctl_response"`) {
		t.Fatalf("allow output = %s", output.String())
	}
	var changedOutput bytes.Buffer
	if err := RunDeferredHook(strings.NewReader(hookJSON(t, "toolu_changed", request)), &changedOutput, resumeBroker.path); err != nil || !strings.Contains(changedOutput.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("changed tool call did not fail closed: %v %s", err, changedOutput.String())
	}
	var unrelated bytes.Buffer
	if err := RunDeferredHook(strings.NewReader(strings.Replace(hookInput, ManagedRequestInputTool, "Bash", 1)), &unrelated, initialBroker.path); err != nil || !strings.Contains(unrelated.String(), `"permissionDecision":"deny"`) {
		t.Fatalf("unrelated tool did not fail closed: %v %s", err, unrelated.String())
	}
	var subagentOutput bytes.Buffer
	subagentInput := strings.TrimSuffix(hookInput, "}") + `,"agent_id":"agent-child"}`
	if err := RunDeferredHook(strings.NewReader(subagentInput), &subagentOutput, initialBroker.path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subagentOutput.String(), `"permissionDecision":"deny"`) || strings.Contains(subagentOutput.String(), `"permissionDecision":"defer"`) {
		t.Fatalf("subagent output = %s", subagentOutput.String())
	}
}

func TestDeferredMCPResultRequiresExactEphemeralEnvelope(t *testing.T) {
	requestBytes := deferredRequest(t)
	request, err := interaction.DecodeRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := true
	answer := interaction.Answer{SchemaVersion: 1, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "ok", Confirmed: &confirmed}}}
	updated, digest, err := BuildDeferredUpdatedInput(request, answer, "toolu_exact")
	if err != nil {
		t.Fatal(err)
	}
	broker, err := startDeferredBroker(&harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	var hookOutput bytes.Buffer
	if err := RunDeferredHook(strings.NewReader(hookJSON(t, "toolu_exact", requestBytes)), &hookOutput, broker.path); err != nil {
		t.Fatal(err)
	}
	var hookResponse struct {
		HookSpecificOutput struct {
			UpdatedInput json.RawMessage `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(hookOutput.Bytes(), &hookResponse); err != nil || len(hookResponse.HookSpecificOutput.UpdatedInput) == 0 {
		t.Fatalf("hook response = %q, %v", hookOutput.String(), err)
	}
	got, err := RequestDeferredBrokerResult(broker.path, hookResponse.HookSpecificOutput.UpdatedInput)
	if err != nil || len(got.Fields) != 1 || got.Fields[0].Confirmed == nil || !*got.Fields[0].Confirmed {
		t.Fatalf("decoded = %#v, %v", got, err)
	}
	changed := bytes.Replace(updated, []byte(`"confirmed":true`), []byte(`"confirmed":false`), 1)
	if _, err := RequestDeferredBrokerResult(broker.path, changed); err == nil {
		t.Fatal("changed resume input was accepted")
	}
	if _, err := RequestDeferredBrokerResult("/missing/broker.sock", updated); err == nil {
		t.Fatal("missing exact ephemeral state was accepted")
	}
	duplicate := bytes.Replace(updated, []byte(`"kind":"confirm"`), []byte(`"kind":"confirm","kind":"confirm"`), 1)
	resume, err := validateDeferredResume("toolu_exact", digest, updated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDeferredToolResult(duplicate, resume); err == nil {
		t.Fatal("duplicate-key deferred input was accepted")
	}
}

func TestStreamJSONParksDeferredToolAfterDurableAcknowledgement(t *testing.T) {
	request := deferredRequest(t)
	executable := writeExecutable(t, `#!/bin/sh
IFS= read -r line
echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
echo '{"type":"result","subtype":"success","is_error":false,"stop_reason":"tool_deferred","session_id":"11111111-1111-4111-8111-111111111111","deferred_tool_use":{"id":"toolu_exact","name":"mcp__managed__channel.request_input","input":`+string(request)+`}}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened, err := New(executable).Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault, ManagedRequestInput: true})
	if err != nil {
		t.Fatal(err)
	}
	var hookOutput bytes.Buffer
	if err := RunDeferredHook(strings.NewReader(hookJSON(t, "toolu_exact", request)), &hookOutput, opened.(*session).broker.path); err != nil {
		t.Fatal(err)
	}
	result, err := opened.RunTurn(ctx, harness.Input{ID: "input-1", Text: "ask"}, func(event harness.Event) {
		if event.RequestInput != nil {
			if event.RequestInput.ContinuationKey != "toolu_exact" || event.RequestInput.Request.Kind != interaction.KindConfirm {
				t.Errorf("request event = %#v", event.RequestInput)
			}
			event.RequestInput.Reply <- harness.RequestInputAcknowledgement{Accepted: true, Status: "accepted"}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "waiting_for_input" || result.SessionID == "" {
		t.Fatalf("result = %#v", result)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamJSONResumesWithoutNewUserPromptAndClassifiesFailures(t *testing.T) {
	request := deferredRequest(t)
	digest, err := RequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	updated := append(bytes.TrimSuffix(request, []byte("}")), []byte(`,"_hctl_response":{"tool_use_id":"toolu_exact","answer":{"schema_version":1,"action":"cancel"}}}`)...)
	executable := writeExecutable(t, `#!/bin/sh
if [ -z "$HCTL_CLAUDE_DEFERRED_BROKER" ]; then exit 9; fi
echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
echo '{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","session_id":"11111111-1111-4111-8111-111111111111","result":"continued"}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened, err := New(executable).Open(ctx, harness.OpenRequest{
		Root: t.TempDir(), ResumeID: "11111111-1111-4111-8111-111111111111", Policy: harness.PolicyDefault,
		Deferred: &harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated},
	})
	if err != nil {
		t.Fatal(err)
	}
	native := opened.(*session)
	var hookOutput bytes.Buffer
	if err := RunDeferredHook(strings.NewReader(hookJSON(t, "toolu_exact", request)), &hookOutput, native.broker.path); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestDeferredBrokerResult(native.broker.path, updated); err != nil {
		t.Fatal(err)
	}
	result, err := opened.RunTurn(ctx, harness.Input{ID: "input-1"}, func(harness.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("result = %#v", result)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}

	unavailable := writeExecutable(t, `#!/bin/sh
IFS= read -r line
echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
echo '{"type":"result","subtype":"error","is_error":true,"stop_reason":"tool_deferred_unavailable","session_id":"11111111-1111-4111-8111-111111111111","deferred_tool_use":{"id":"toolu_exact","name":"mcp__managed__channel.request_input","input":`+string(request)+`}}'
`)
	failed, err := New(unavailable).Open(ctx, harness.OpenRequest{Root: t.TempDir(), ResumeID: "11111111-1111-4111-8111-111111111111", Policy: harness.PolicyDefault})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failed.RunTurn(ctx, harness.Input{ID: "input-1"}, func(harness.Event) {}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable error = %v", err)
	}
	failed.Abort()
}

func TestDeferredResultRequiresConsumedRootHookReceipt(t *testing.T) {
	request := deferredRequest(t)
	executable := writeExecutable(t, `#!/bin/sh
IFS= read -r line
echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
echo '{"type":"result","subtype":"success","is_error":false,"stop_reason":"tool_deferred","session_id":"11111111-1111-4111-8111-111111111111","deferred_tool_use":{"id":"toolu_exact","name":"mcp__managed__channel.request_input","input":`+string(request)+`}}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := New(executable).Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault, ManagedRequestInput: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.RunTurn(ctx, harness.Input{ID: "input-1", Text: "ask"}, func(harness.Event) {}); !errors.Is(err, ErrDeferredMismatch) {
		t.Fatalf("unproven deferred result = %v", err)
	}
	session.Abort()
}

func TestBrokerCommitsHookAndMCPStateOnlyAfterSuccessfulWrite(t *testing.T) {
	requestBytes := deferredRequest(t)
	request, err := interaction.DecodeRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	answer := interaction.Answer{SchemaVersion: 1, Action: interaction.ActionCancel}
	updated, digest, err := BuildDeferredUpdatedInput(request, answer, "toolu_exact")
	if err != nil {
		t.Fatal(err)
	}

	failedInitial, err := startDeferredBroker(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer failedInitial.Close()
	if err := failedInitial.writeResponse(failingWriter{}, brokerRequest{Kind: "hook", Value: json.RawMessage(hookJSON(t, "toolu_exact", requestBytes))}); err == nil {
		t.Fatal("failed defer write was reported successful")
	}
	if failedInitial.consumeDeferred("toolu_exact", requestBytes) {
		t.Fatal("failed defer write minted root provenance")
	}
	initial, err := startDeferredBroker(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer initial.Close()
	var deferred bytes.Buffer
	if err := initial.writeResponse(&deferred, brokerRequest{Kind: "hook", Value: json.RawMessage(hookJSON(t, "toolu_exact", requestBytes))}); err != nil {
		t.Fatal(err)
	}
	changedRequest := bytes.Replace(requestBytes, []byte("Proceed?"), []byte("Changed?"), 1)
	if initial.consumeDeferred("toolu_other", requestBytes) || initial.consumeDeferred("toolu_exact", changedRequest) {
		t.Fatal("defer receipt accepted changed tool identity or input")
	}
	if !initial.consumeDeferred("toolu_exact", requestBytes) || initial.consumeDeferred("toolu_exact", requestBytes) {
		t.Fatal("defer receipt was not exact and single-use")
	}

	resume, err := startDeferredBroker(&harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated})
	if err != nil {
		t.Fatal(err)
	}
	defer resume.Close()
	var allowed bytes.Buffer
	if err := resume.writeResponse(&allowed, brokerRequest{Kind: "hook", Value: json.RawMessage(hookJSON(t, "toolu_exact", requestBytes))}); err != nil {
		t.Fatal(err)
	}
	if err := resume.writeResponse(failingWriter{}, brokerRequest{Kind: "mcp", Value: updated}); err == nil {
		t.Fatal("failed MCP response write was reported successful")
	}
	if got := resume.resumeDisposition(); got != resumeBrokerAmbiguous {
		t.Fatalf("failed MCP write disposition = %v", got)
	}

	failedHook, err := startDeferredBroker(&harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated})
	if err != nil {
		t.Fatal(err)
	}
	defer failedHook.Close()
	if err := failedHook.writeResponse(failingWriter{}, brokerRequest{Kind: "hook", Value: json.RawMessage(hookJSON(t, "toolu_exact", requestBytes))}); err == nil {
		t.Fatal("failed allow-hook response write was reported successful")
	}
	if got := failedHook.resumeDisposition(); got != resumeBrokerAmbiguous {
		t.Fatalf("failed allow-hook write disposition = %v", got)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disconnected") }

func TestDeferredResumeClassifiesRetainedSessionLossBeforeExecution(t *testing.T) {
	requestBytes := deferredRequest(t)
	request, err := interaction.DecodeRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	answer := interaction.Answer{SchemaVersion: 1, Action: interaction.ActionCancel}
	updated, digest, err := BuildDeferredUpdatedInput(request, answer, "toolu_exact")
	if err != nil {
		t.Fatal(err)
	}
	executable := writeExecutable(t, "#!/bin/sh\nexit 1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := New(executable).Open(ctx, harness.OpenRequest{
		Root: t.TempDir(), ResumeID: "11111111-1111-4111-8111-111111111111", Policy: harness.PolicyDefault,
		Deferred: &harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.RunTurn(ctx, harness.Input{ID: "input-1"}, func(harness.Event) {}); !errors.Is(err, ErrDeferredSessionLost) {
		t.Fatalf("retained session loss = %v", err)
	}
	session.Abort()
}

func TestDeferredResumeRejectsProcessSuccessWithoutCompletedBrokerExchange(t *testing.T) {
	requestBytes := deferredRequest(t)
	request, err := interaction.DecodeRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	updated, digest, err := BuildDeferredUpdatedInput(request, interaction.Answer{SchemaVersion: 1, Action: interaction.ActionCancel}, "toolu_exact")
	if err != nil {
		t.Fatal(err)
	}
	executable := writeExecutable(t, `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
echo '{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","session_id":"11111111-1111-4111-8111-111111111111","result":"continued"}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened, err := New(executable).Open(ctx, harness.OpenRequest{
		Root: t.TempDir(), ResumeID: "11111111-1111-4111-8111-111111111111", Policy: harness.PolicyDefault,
		Deferred: &harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.RunTurn(ctx, harness.Input{ID: "input-1"}, func(harness.Event) {}); !errors.Is(err, ErrDeferredMismatch) {
		t.Fatalf("incomplete broker exchange = %v", err)
	}
	opened.Abort()
}

func TestDeferredResumeClassifiesBrokerWriteFailureUncertain(t *testing.T) {
	requestBytes := deferredRequest(t)
	request, err := interaction.DecodeRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	updated, digest, err := BuildDeferredUpdatedInput(request, interaction.Answer{SchemaVersion: 1, Action: interaction.ActionCancel}, "toolu_exact")
	if err != nil {
		t.Fatal(err)
	}
	executable := writeExecutable(t, `#!/bin/sh
echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
echo '{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","session_id":"11111111-1111-4111-8111-111111111111","result":"continued"}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened, err := New(executable).Open(ctx, harness.OpenRequest{
		Root: t.TempDir(), ResumeID: "11111111-1111-4111-8111-111111111111", Policy: harness.PolicyDefault,
		Deferred: &harness.DeferredToolResume{ToolUseID: "toolu_exact", ToolName: ManagedRequestInputTool, InputDigest: digest, UpdatedInput: updated},
	})
	if err != nil {
		t.Fatal(err)
	}
	native := opened.(*session)
	if err := native.broker.writeResponse(failingWriter{}, brokerRequest{Kind: "hook", Value: json.RawMessage(hookJSON(t, "toolu_exact", requestBytes))}); err == nil {
		t.Fatal("failed hook write was reported successful")
	}
	if _, err := opened.RunTurn(ctx, harness.Input{ID: "input-1"}, func(harness.Event) {}); !errors.Is(err, ErrDeferredDelivery) {
		t.Fatalf("broker write failure = %v", err)
	}
	opened.Abort()
}

func TestStreamJSONRejectsParallelManagedToolBatch(t *testing.T) {
	executable := writeExecutable(t, `#!/bin/sh
IFS= read -r line
echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
echo '{"type":"stream_event","session_id":"11111111-1111-4111-8111-111111111111","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_one","name":"mcp__managed__channel.request_input","input":{}}}}'
echo '{"type":"stream_event","session_id":"11111111-1111-4111-8111-111111111111","event":{"type":"content_block_start","content_block":{"type":"tool_use","id":"toolu_two","name":"Bash","input":{}}}}'
echo '{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","session_id":"11111111-1111-4111-8111-111111111111","result":"not deferred"}'
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := New(executable).Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.RunTurn(ctx, harness.Input{ID: "input-1", Text: "ask"}, func(harness.Event) {}); err == nil || !strings.Contains(err.Error(), "parallel") {
		t.Fatalf("parallel error = %v", err)
	}
	session.Abort()
}

func deferredRequest(t *testing.T) []byte {
	t.Helper()
	request := interaction.Request{SchemaVersion: 1, Kind: interaction.KindConfirm, Prompt: "Proceed?", Policy: interaction.Policy{ExpiresAfterSeconds: 60, Cancellation: interaction.CancellationAllowed}, Field: &interaction.Field{ID: "ok", Kind: interaction.KindConfirm, Label: "Proceed", Required: true}}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func hookJSON(t *testing.T, toolUseID string, request []byte) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": ManagedRequestInputTool, "tool_use_id": toolUseID, "tool_input": json.RawMessage(request), "session_id": "session", "cwd": "/tmp"})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
