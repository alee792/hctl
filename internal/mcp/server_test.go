package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hctl/internal/connection/github"
	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/setup"
)

func TestManagedContract(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Apply(p, self); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hello"},"_meta":{"progressToken":3}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"text":""}}}`,
	}, "\n") + "\n"
	var output, audit bytes.Buffer
	if err := Serve(root, root, "claude", strings.NewReader(input), &output, &audit); err != nil {
		t.Fatal(err)
	}
	responses := decodeLines(t, output.String())
	if len(responses) != 4 {
		t.Fatalf("got %d responses, want 4", len(responses))
	}
	tools := responses[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("connection-free tools/list = %#v", tools)
	}
	valid := responses[2]["result"].(map[string]any)
	if valid["isError"] != false || valid["structuredContent"].(map[string]any)["text"] != "hello" {
		t.Fatalf("valid result = %#v", valid)
	}
	if responses[3]["result"].(map[string]any)["isError"] != true {
		t.Fatal("invalid tool input did not fail closed")
	}
	log := audit.String()
	if strings.Contains(log, "hello") || !strings.Contains(log, "outcome=requested") || !strings.Contains(log, "outcome=authorized") || !strings.Contains(log, "outcome=completed") {
		t.Fatalf("unsafe or incomplete audit output: %q", log)
	}
}

func TestFrictionToolIsOptInAndNonInterfering(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	var disabledOutput bytes.Buffer
	if err := serveRequestsWithFriction(p, nil, github.NewClient(nil), &stubFrictionRecorder{}, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`+"\n"), &disabledOutput, io.Discard); err != nil {
		t.Fatal(err)
	}
	disabledTools := decodeLines(t, disabledOutput.String())[0]["result"].(map[string]any)["tools"].([]any)
	if len(disabledTools) != 1 {
		t.Fatalf("default tools = %#v", disabledTools)
	}

	if err := os.WriteFile(filepath.Join(root, "instructions.md"), []byte("---\ndescription: Test agent.\nfriction-notes: true\n---\n\nBe concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err = project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	recorder := &stubFrictionRecorder{recorded: false}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"record-friction","arguments":{"note":"The managed tool contract required repeated interpretation."}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"still usable"}}}`,
	}, "\n") + "\n"
	var output, audit bytes.Buffer
	if err := serveRequestsWithFriction(p, nil, github.NewClient(nil), recorder, strings.NewReader(input), &output, &audit); err != nil {
		t.Fatal(err)
	}
	responses := decodeLines(t, output.String())
	tools := responses[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[1].(map[string]any)["name"] != "record-friction" {
		t.Fatalf("enabled tools = %#v", tools)
	}
	annotations := tools[1].(map[string]any)["annotations"].(map[string]any)
	if annotations["readOnlyHint"] != false || annotations["destructiveHint"] != false || annotations["idempotentHint"] != false {
		t.Fatalf("friction annotations = %#v", annotations)
	}
	frictionResult := responses[1]["result"].(map[string]any)
	if frictionResult["isError"] != false || frictionResult["structuredContent"].(map[string]any)["recorded"] != false {
		t.Fatalf("failed store result = %#v", frictionResult)
	}
	if responses[2]["result"].(map[string]any)["structuredContent"].(map[string]any)["text"] != "still usable" {
		t.Fatalf("subsequent echo = %#v", responses[2])
	}
	if recorder.note == "" || strings.Contains(audit.String(), recorder.note) {
		t.Fatalf("recorder note or audit = %q / %q", recorder.note, audit.String())
	}
}

func TestFrictionToolRejectsInvalidInputWithoutCallingStore(t *testing.T) {
	p := &project.Project{AgentID: "test@0123456789ab", Name: "test", SourceFingerprint: "source", Harness: "claude", FrictionNotes: true}
	recorder := &stubFrictionRecorder{recorded: true}
	params := json.RawMessage(`{"name":"record-friction","arguments":{"note":"   ","cause":"guess"}}`)
	result, _, _, err := callManagedWithInputAndFriction(p, nil, github.NewClient(nil), nil, recorder, json.RawMessage(`1`), params, io.Discard)
	if err == nil || result != nil || recorder.calls != 0 {
		t.Fatalf("invalid friction call = result %#v, calls %d, error %v", result, recorder.calls, err)
	}
}

func TestReadOnlyChannelPolicyRejectsAuthoredManagedTools(t *testing.T) {
	t.Setenv("HCTL_EXECUTION_POLICY", string(harness.PolicyReadOnly))
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Apply(p, self); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"still readable"}}}`,
	}, "\n") + "\n"
	opened := 0
	var output bytes.Buffer
	if err := serveWithRuntime(root, root, "claude", strings.NewReader(input), &output, io.Discard, github.NewClient(nil), func(context.Context, *project.Project) (managedRuntime, error) {
		opened++
		return nil, errors.New("authored runtime started")
	}); err != nil {
		t.Fatal(err)
	}
	if opened != 0 {
		t.Fatalf("authored runtime opened %d times", opened)
	}
	responses := decodeLines(t, output.String())
	if responses[1]["result"].(map[string]any)["isError"] != false {
		t.Fatalf("read-only echo failed: %#v", responses[1])
	}

	params := json.RawMessage(`{"name":"write-file","arguments":{"path":"changed.txt"}}`)
	var audit bytes.Buffer
	_, _, toolName, err := callManaged(&project.Project{AgentID: "test-agent"}, nil, github.NewClient(nil), json.RawMessage(`1`), params, &audit)
	if err == nil || !strings.Contains(err.Error(), "unavailable in a read-only channel session") || toolName != "write-file" {
		t.Fatalf("read-only authored call = tool %q error %v", toolName, err)
	}
}

func TestGitHubConnectionUsesSameManagedSurfaceForBothHarnesses(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			root := testAgent(t)
			if err := os.MkdirAll(filepath.Join(root, "connections"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "connections", "github.md"), []byte("Search public GitHub project context.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := project.Load(root, harness)
			if err != nil {
				t.Fatal(err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := setup.Apply(p, self); err != nil {
				t.Fatal(err)
			}

			calls := 0
			client := github.NewClient(&http.Client{Transport: mcpRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.Header.Get("Authorization") != "" {
					t.Fatal("managed GitHub request included authorization")
				}
				if calls == 1 {
					return mcpResponse(http.StatusInternalServerError, "sensitive upstream failure"), nil
				}
				return mcpResponse(http.StatusOK, `{"full_name":"acme/widgets","description":null,"html_url":"https://github.com/acme/widgets","default_branch":"main","archived":false,"fork":false,"open_issues_count":1,"updated_at":"2026-08-06T00:00:00Z"}`), nil
			})})
			input := strings.Join([]string{
				`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"github__get-repository","arguments":{"owner":"acme","repo":"widgets"}}}`,
				`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"github__get-repository","arguments":{"owner":"acme","repo":"widgets"}}}`,
			}, "\n") + "\n"
			var output, audit bytes.Buffer
			if err := serve(root, root, harness, strings.NewReader(input), &output, &audit, client); err != nil {
				t.Fatal(err)
			}
			responses := decodeLines(t, output.String())
			tools := responses[0]["result"].(map[string]any)["tools"].([]any)
			gotNames := make([]string, len(tools))
			for index, tool := range tools {
				definition := tool.(map[string]any)
				gotNames[index] = definition["name"].(string)
				if index > 0 && !strings.Contains(definition["description"].(string), "Search public GitHub project context.") {
					t.Fatalf("connection description missing from %#v", definition)
				}
			}
			wantNames := []string{"echo", github.GetRepository, github.ListIssues, github.GetIssue}
			if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
				t.Fatalf("managed tools = %v, want %v", gotNames, wantNames)
			}
			if responses[1]["result"].(map[string]any)["isError"] != true || responses[2]["result"].(map[string]any)["isError"] != false {
				t.Fatalf("failed call did not leave MCP service usable: %#v", responses)
			}
			if calls != 2 {
				t.Fatalf("GitHub calls = %d, want 2", calls)
			}
			log := audit.String()
			if strings.Contains(log, "acme") || strings.Contains(log, "widgets") || strings.Contains(log, "sensitive") || !strings.Contains(log, "tool=github__get-repository") {
				t.Fatalf("unsafe or incomplete audit = %q", log)
			}
		})
	}
}

type mcpRoundTripFunc func(*http.Request) (*http.Response, error)

type stubFrictionRecorder struct {
	recorded bool
	calls    int
	note     string
}

func (recorder *stubFrictionRecorder) Record(_ *project.Project, note string) bool {
	recorder.calls++
	recorder.note = note
	return recorder.recorded
}

func (function mcpRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func mcpResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func testAgent(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"instructions.md":      "---\ndescription: Test agent.\n---\n\nBe concise.\n",
		"skills/echo/SKILL.md": "---\nname: echo\ndescription: Repeat safely.\n---\n\nUse echo.\n",
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func decodeLines(t *testing.T, content string) []map[string]any {
	t.Helper()
	var result []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	return result
}
