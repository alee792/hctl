package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPrintsSafeCompatibilityWarning(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(root, "skills", "echo", "SKILL.md"), "---\nname: echo\ndescription: Repeat safely.\nargument-hint: '[text]'\n---\n\nUse echo.\n", 0o644)
	harness := filepath.Join(t.TempDir(), "codex")
	writeCLIFile(t, harness, "#!/bin/sh\necho 'codex-cli 0.144.1'\n", 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", root, "--harness", "codex", "--command", harness}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); !strings.Contains(got, `warning: skills/echo/SKILL.md: field "argument-hint":`) || !strings.Contains(got, "copied unchanged but may have no effect for codex") {
		t.Fatalf("warning output = %q", got)
	}
	if !strings.Contains(output.String(), "applied agent=") {
		t.Fatalf("apply output = %q", output.String())
	}
}

func TestApplyRejectsUnsupportedConnectionBeforeWorkspaceMutation(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "connections", "gitlab.md"), "GitLab.\n", 0o644)
	var output, stderr bytes.Buffer
	err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "codex"}, strings.NewReader(""), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), "supports github.md only") {
		t.Fatalf("unsupported connection error = %v", err)
	}
	entries, readErr := os.ReadDir(workspace)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("invalid project mutated workspace: %v, %v", entries, readErr)
	}
}

func writeCLIFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
