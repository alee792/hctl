package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hctl/internal/project"
	"hctl/internal/rootfs"
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
		".mcp.json",
		"CLAUDE.md",
		".hctl/apply/claude.json",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	first := snapshot(t, root, paths)
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := snapshot(t, root, paths); !reflect.DeepEqual(got, first) {
		t.Fatal("same source did not produce byte-identical setup")
	}
	if config := read(t, filepath.Join(root, ".mcp.json")); !strings.Contains(config, `"command": "/opt/hctl/bin/hctl"`) || !strings.Contains(config, `"--harness"`) || !strings.Contains(config, `"claude"`) {
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
	if !strings.Contains(config, `command = "/opt/hctl/bin/hctl"`) || !strings.Contains(config, `[mcp_servers.managed]`) || !strings.Contains(config, `"--harness", "codex"`) {
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

func TestApplyMigratesLegacyProjectionRecord(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	files, err := filesFor(p, "/opt/hctl/bin/hctl")
	if err != nil {
		t.Fatal(err)
	}
	owned := make([]ownedFile, 0, len(files)+1)
	for path, data := range files {
		writeBytes(t, root, path, data)
		owned = append(owned, ownedFile{Path: path, SHA256: rootfs.SHA256(data)})
	}
	manifestPath := ".hctl/manifests/claude.json"
	manifest := []byte("{}\n")
	writeBytes(t, root, manifestPath, manifest)
	owned = append(owned, ownedFile{Path: manifestPath, SHA256: rootfs.SHA256(manifest)})
	legacy := applyRecord{SchemaVersion: 1, Generator: project.GeneratorVersion, Harness: "claude", SourceFingerprint: p.SourceFingerprint, Files: owned}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, root, legacyRecordPath("claude"), data)

	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{manifestPath, legacyRecordPath("claude")} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(obsolete))); !os.IsNotExist(err) {
			t.Fatalf("obsolete file %s still exists", obsolete)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(applyRecordPath("claude")))); err != nil {
		t.Fatal(err)
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

func writeBytes(t *testing.T, root, path string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
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
