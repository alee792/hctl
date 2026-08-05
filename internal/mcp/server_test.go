package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if tools[0].(map[string]any)["name"] != "echo" {
		t.Fatal("tools/list did not advertise echo")
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
