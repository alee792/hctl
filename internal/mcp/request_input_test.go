package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/interaction"
	"hctl/internal/project"
)

func TestRequestInputCapabilityGatingAndRedaction(t *testing.T) {
	p := &project.Project{AgentID: "test-agent", MaxToolInput: 1024}
	request := validRequestInput()
	arguments, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"channel.request_input","arguments":` + string(arguments) + `}}`

	for _, test := range []struct {
		name      string
		bridge    *fakeControllerRequestInput
		wantTools int
		wantError bool
	}{
		{name: "no channel bridge", wantTools: 1, wantError: true},
		{name: "missing harness strategy", bridge: &fakeControllerRequestInput{capabilities: requestInputCapabilities(), responder: true}, wantTools: 1, wantError: true},
		{name: "missing responder", bridge: &fakeControllerRequestInput{capabilities: requestInputCapabilities(), strategy: true}, wantTools: 1, wantError: true},
		{name: "channel root bridge with strategy and responder", bridge: &fakeControllerRequestInput{capabilities: requestInputCapabilities(), strategy: true, responder: true, disposition: harness.RequestInputContinuationTurn}, wantTools: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, call,
			}, "\n") + "\n"
			var output, audit bytes.Buffer
			var bridge requestInputRuntime
			if test.bridge != nil {
				bridge = test.bridge
			}
			if err := serveRequestsWithInput(p, nil, bridge, strings.NewReader(input), &output, &audit); err != nil {
				t.Fatal(err)
			}
			responses := decodeLines(t, output.String())
			tools := responses[0]["result"].(map[string]any)["tools"].([]any)
			if len(tools) != test.wantTools {
				t.Fatalf("tools = %#v", tools)
			}
			result := responses[1]["result"].(map[string]any)
			if got := result["isError"].(bool); got != test.wantError {
				t.Fatalf("isError = %v, result = %#v", got, result)
			}
			combined := output.String() + audit.String()
			for _, secret := range []string{request.Prompt, request.Field.Options[0].Label, request.Field.Options[0].Value} {
				if strings.Contains(combined, secret) {
					t.Fatalf("managed output or audit exposed semantic content: %q", combined)
				}
			}
			if !test.wantError && (test.bridge.calls != 1 || test.bridge.request.Prompt != request.Prompt || result["structuredContent"].(map[string]any)["disposition"] != "continuation_turn") {
				t.Fatalf("bridge/result = %#v / %#v", test.bridge, result)
			}
		})
	}
}

func TestClaudeDeferredBrokerEnvironmentCannotAdvertiseOrExecuteWithoutOwner(t *testing.T) {
	p := &project.Project{AgentID: "test-agent", MaxToolInput: 1024}
	request := validRequestInput()
	t.Setenv(claude.DeferredBrokerEnv, "/missing/hctl-broker.sock")
	if requestInputAvailable(nil) {
		t.Fatal("unowned broker environment advertised managed input")
	}
	arguments, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"name":"channel.request_input","arguments":` + string(arguments) + `}`)
	if result, _, _, err := callManagedWithInput(p, nil, nil, json.RawMessage(`8`), params, io.Discard); err == nil || result != nil {
		t.Fatalf("unowned broker result = %#v, %v", result, err)
	}
}

func TestRequestInputRejectsSchemaAndUnavailableFallbackBeforeBridge(t *testing.T) {
	p := &project.Project{AgentID: "test-agent"}
	bridge := &fakeControllerRequestInput{strategy: true, responder: true, disposition: harness.RequestInputDeferred, capabilities: requestInputCapabilities()}
	bridge.capabilities.Kinds = []interaction.Kind{interaction.KindText}
	inputs := []string{
		`{"name":"channel.request_input","arguments":{"schema_version":1,"kind":"confirm","prompt":"Proceed?","callback_id":"forged","policy":{"expires_after_seconds":60,"cancellation":"allowed"},"field":{"id":"ok","kind":"confirm","label":"Proceed","required":true}}}`,
		`{"name":"channel.request_input","arguments":{"schema_version":1,"kind":"confirm","prompt":"Proceed?","policy":{"expires_after_seconds":60,"cancellation":"allowed"},"field":{"id":"ok","kind":"confirm","label":"Proceed","required":true}}}`,
	}
	for _, params := range inputs {
		result, _, _, err := callManagedWithInput(p, nil, bridge, json.RawMessage(`1`), json.RawMessage(params), io.Discard)
		if err == nil || result != nil {
			t.Fatalf("invalid call result=%#v err=%v", result, err)
		}
	}
	if bridge.calls != 0 {
		t.Fatalf("rejected requests reached bridge %d times", bridge.calls)
	}
}

func TestRequestInputAuditCorrelationExcludesSemanticBytesAndDispositionIsBounded(t *testing.T) {
	p := &project.Project{AgentID: "test-agent"}
	bridge := &fakeControllerRequestInput{strategy: true, responder: true, disposition: harness.RequestInputDeferred, capabilities: requestInputCapabilities()}
	request := validRequestInput()
	requestIDs := make([]string, 0, 2)
	for _, prompt := range []string{"first private prompt", "different private prompt"} {
		request.Prompt = prompt
		arguments, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		params := json.RawMessage(`{"name":"channel.request_input","arguments":` + string(arguments) + `}`)
		result, requestID, _, err := callManagedWithInput(p, nil, bridge, json.RawMessage(`7`), params, io.Discard)
		if err != nil || result["structuredContent"].(map[string]any)["disposition"] != "deferred" {
			t.Fatalf("request result=%#v err=%v", result, err)
		}
		requestIDs = append(requestIDs, requestID)
	}
	if requestIDs[0] != requestIDs[1] {
		t.Fatalf("semantic bytes affected audit correlation: %v", requestIDs)
	}

	bridge.disposition = harness.RequestInputDisposition("private_customer_name")
	arguments, _ := json.Marshal(request)
	params := json.RawMessage(`{"name":"channel.request_input","arguments":` + string(arguments) + `}`)
	if result, _, _, err := callManagedWithInput(p, nil, bridge, json.RawMessage(`8`), params, io.Discard); err == nil || result != nil {
		t.Fatalf("unbounded strategy disposition result=%#v err=%v", result, err)
	}
}

type fakeControllerRequestInput struct {
	strategy     bool
	responder    bool
	disposition  harness.RequestInputDisposition
	capabilities interaction.Capabilities
	calls        int
	request      interaction.Request
}

func (f *fakeControllerRequestInput) HarnessStrategyAvailable() bool { return f.strategy }
func (f *fakeControllerRequestInput) ResponderAvailable() bool       { return f.responder }
func (f *fakeControllerRequestInput) Capabilities() interaction.Capabilities {
	return f.capabilities
}
func (f *fakeControllerRequestInput) Request(_ context.Context, _ string, request interaction.Request) (harness.RequestInputToolResult, error) {
	f.calls++
	f.request = request
	return harness.RequestInputToolResult{Disposition: f.disposition}, nil
}

func validRequestInput() interaction.Request {
	return interaction.Request{
		SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindChooseOne,
		Prompt: "Choose a deployment target", FallbackText: "Reply with staging or production.",
		Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
		Field: &interaction.Field{ID: "target", Kind: interaction.KindChooseOne, Label: "Target", Required: true, MinSelections: 1, MaxSelections: 1, Options: []interaction.Option{
			{ID: "staging", Label: "Staging", Value: "staging"}, {ID: "production", Label: "Production", Value: "production"},
		}},
	}
}

func requestInputCapabilities() interaction.Capabilities {
	return interaction.Capabilities{
		Kinds: []interaction.Kind{interaction.KindChooseOne}, MaxRequestBytes: interaction.MaxRequestBytes,
		MaxPromptBytes: interaction.MaxPromptBytes, MaxFields: interaction.MaxFields,
		MaxOptionsPerField: interaction.MaxOptionsPerField, MaxSelections: interaction.MaxSelections,
		MaxTotalOptions: interaction.MaxTotalOptions, MaxLabelBytes: interaction.MaxLabelBytes,
		MaxDescriptionBytes: interaction.MaxDescriptionBytes, MaxValueBytes: interaction.MaxValueBytes,
		MaxTextRunes: interaction.MaxTextRunes,
	}
}
