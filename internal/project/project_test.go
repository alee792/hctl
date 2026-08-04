package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadIsDeterministicAndRejectsUnsafeSource(t *testing.T) {
	root := agent(t)
	first, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.SourceFingerprint != second.Manifest.SourceFingerprint {
		t.Fatal("same source produced different fingerprints")
	}

	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "instructions.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "instructions.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("source symlink was not rejected: %v", err)
	}
}

func TestLoadRejectsTraversalUnknownFieldsAndUnboundedCapability(t *testing.T) {
	cases := []struct {
		name   string
		config string
		want   string
	}{
		{"traversal", `{"schema_version":1,"name":"portable","instructions":"../outside.md","skills":["skills/echo/SKILL.md"],"managed_capability":{"name":"echo","max_input_bytes":10}}`, "remain inside"},
		{"unknown", `{"schema_version":1,"name":"portable","instructions":"instructions.md","skills":["skills/echo/SKILL.md"],"managed_capability":{"name":"echo","max_input_bytes":10},"surprise":true}`, "unknown field"},
		{"capability", `{"schema_version":1,"name":"portable","instructions":"instructions.md","skills":["skills/echo/SKILL.md"],"managed_capability":{"name":"anything","max_input_bytes":10}}`, "must be echo"},
		{"limit", `{"schema_version":1,"name":"portable","instructions":"instructions.md","skills":["skills/echo/SKILL.md"],"managed_capability":{"name":"echo","max_input_bytes":99999}}`, "between 1 and 4096"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := agent(t)
			if err := os.WriteFile(filepath.Join(root, "agent.json"), []byte(tc.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root, "codex"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func agent(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"agent.json":           `{"schema_version":1,"name":"portable","instructions":"instructions.md","skills":["skills/echo/SKILL.md"],"managed_capability":{"name":"echo","max_input_bytes":128}}`,
		"instructions.md":      "Be concise.\n",
		"skills/echo/SKILL.md": "---\nname: echo\ndescription: Repeat safely.\n---\n\nUse echo.\n",
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
