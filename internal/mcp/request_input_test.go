package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hctl/internal/harness/claude"
	"hctl/internal/interaction"
	"hctl/internal/project"
)

func TestRequestInputIsGatedOnTheDeferredBrokerAndRedactsSemanticContent(t *testing.T) {
	p := &project.Project{AgentID: "test-agent", MaxToolInput: 1024}
	request := validRequestInput()
	arguments, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"channel.request_input","arguments":` + string(arguments) + `}}`

	for _, test := range []struct {
		name      string
		broker    bool
		wantTools int
		wantError bool
	}{
		{name: "no deferred broker", wantTools: 1, wantError: true},
		{name: "owned deferred broker", broker: true, wantTools: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.broker {
				t.Setenv(claude.DeferredBrokerEnv, startFakeBroker(t))
			} else {
				t.Setenv(claude.DeferredBrokerEnv, "")
			}
			input := strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, call,
			}, "\n") + "\n"
			var output, audit bytes.Buffer
			if err := serveRequestsWithFriction(p, nil, &stubFrictionRecorder{}, strings.NewReader(input), &output, &audit); err != nil {
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
			if !test.wantError {
				answer := result["structuredContent"].(map[string]any)["answer"].(map[string]any)
				if answer["action"] != "submit" {
					t.Fatalf("broker answer = %#v", answer)
				}
			}
		})
	}
}

func TestClaudeDeferredBrokerEnvironmentCannotAdvertiseOrExecuteWithoutOwner(t *testing.T) {
	p := &project.Project{AgentID: "test-agent", MaxToolInput: 1024}
	request := validRequestInput()
	t.Setenv(claude.DeferredBrokerEnv, "/missing/hctl-broker.sock")
	if requestInputAvailable() {
		t.Fatal("unowned broker environment advertised managed input")
	}
	arguments, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"name":"channel.request_input","arguments":` + string(arguments) + `}`)
	if result, _, _, err := callManaged(p, nil, json.RawMessage(`8`), params, io.Discard); err == nil || result != nil {
		t.Fatalf("unowned broker result = %#v, %v", result, err)
	}
}

func TestRequestInputAuditCorrelationExcludesSemanticBytes(t *testing.T) {
	p := &project.Project{AgentID: "test-agent"}
	t.Setenv(claude.DeferredBrokerEnv, startFakeBroker(t))
	request := validRequestInput()
	requestIDs := make([]string, 0, 2)
	for _, prompt := range []string{"first private prompt", "different private prompt"} {
		request.Prompt = prompt
		arguments, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		params := json.RawMessage(`{"name":"channel.request_input","arguments":` + string(arguments) + `}`)
		var audit bytes.Buffer
		result, requestID, _, err := callManaged(p, nil, json.RawMessage(`7`), params, &audit)
		if err != nil || result["isError"] != false {
			t.Fatalf("request result=%#v err=%v", result, err)
		}
		if strings.Contains(audit.String(), prompt) {
			t.Fatalf("audit exposed semantic content: %q", audit.String())
		}
		requestIDs = append(requestIDs, requestID)
	}
	if requestIDs[0] != requestIDs[1] {
		t.Fatalf("semantic bytes affected audit correlation: %v", requestIDs)
	}
}

// startFakeBroker answers the Claude deferred broker wire protocol so the
// managed server's production gating and result path can be exercised without
// a live Claude session.
func startFakeBroker(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "hctl-mcp-broker-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "broker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.RemoveAll(directory)
	})
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			scanner := bufio.NewScanner(connection)
			if scanner.Scan() {
				var request struct {
					Kind string `json:"kind"`
				}
				response := map[string]any{"error": true}
				if json.Unmarshal(scanner.Bytes(), &request) == nil {
					switch request.Kind {
					case "available":
						response = map[string]any{"available": true}
					case "mcp":
						response = map[string]any{"answer": interaction.Answer{
							SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit,
							Fields: []interaction.FieldAnswer{{FieldID: "target", OptionIDs: []string{"chosen"}}},
						}}
					}
				}
				_ = json.NewEncoder(connection).Encode(response)
			}
			_ = connection.Close()
		}
	}()
	return path
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
