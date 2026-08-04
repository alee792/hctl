package projection

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hctl/internal/project"
)

func TestApplyIsDeterministicAndRefusesConflicts(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := Apply(p, "/opt/hctl/bin/hctl")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".claude/skills/echo/SKILL.md",
		".hctl/manifests/claude.json",
		".mcp.json",
		"CLAUDE.md",
		".hctl/projections/claude.json",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	first := snapshot(t, root, paths)
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := snapshot(t, root, paths); !reflect.DeepEqual(got, first) {
		t.Fatal("same source did not produce byte-identical projection")
	}
	if !strings.Contains(read(t, filepath.Join(root, ".mcp.json")), `"command": "/opt/hctl/bin/hctl"`) {
		t.Fatal("Claude MCP configuration does not bind the absolute hctl path")
	}
	codex, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(codex, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	config := read(t, filepath.Join(root, ".codex", "config.toml"))
	if !strings.Contains(config, `command = "/opt/hctl/bin/hctl"`) || !strings.Contains(config, `[mcp_servers.managed]`) {
		t.Fatal("Codex MCP configuration does not bind the shared managed server")
	}

	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("edited generated file was not refused: %v", err)
	}

	conflictRoot := testAgent(t)
	if err := os.WriteFile(filepath.Join(conflictRoot, "CLAUDE.md"), []byte("hand authored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict, err := project.Load(conflictRoot, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(conflict, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "without hctl ownership") {
		t.Fatalf("hand-authored native file was not refused: %v", err)
	}
}

func testAgent(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "instructions.md"), "Be concise.\n")
	write(t, filepath.Join(root, "skills", "echo.md"), "---\nname: echo\ndescription: Repeat text safely.\n---\n\nUse echo.\n")
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func snapshot(t *testing.T, root string, paths []string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, path := range paths {
		result[path] = read(t, filepath.Join(root, filepath.FromSlash(path)))
	}
	return result
}
