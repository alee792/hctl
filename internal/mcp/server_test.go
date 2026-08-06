package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hctl/internal/connection/github"
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
