package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
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

func TestApplyRejectsUnsupportedChannelBeforeWorkspaceMutation(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			source := t.TempDir()
			workspace := t.TempDir()
			writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
			writeCLIFile(t, filepath.Join(source, "channels", "slack.md"), "Slack.\n", 0o644)
			var output, stderr bytes.Buffer
			err := Run([]string{"apply", source, "--workspace", workspace, "--harness", harnessName}, strings.NewReader(""), &output, &stderr, "")
			if err == nil || !strings.Contains(err.Error(), "supports discord.md only") {
				t.Fatalf("unsupported channel error = %v", err)
			}
			entries, readErr := os.ReadDir(workspace)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("invalid project mutated workspace: %v, %v", entries, readErr)
			}
		})
	}
}

func TestDiscordChannelRequiresAppliedSetup(t *testing.T) {
	source := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "channels", "discord.md"), "Receive signed commands.\n", 0o644)
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "claude")
	writeCLIFile(t, command, "#!/bin/sh\necho 'claude 1.0.0'\n", 0o755)
	var output, stderr bytes.Buffer
	err = Run([]string{
		"channel", "discord", source,
		"--harness", "claude",
		"--command", command,
		"--application-id", "123456789012345678",
		"--public-key", hex.EncodeToString(publicKey),
		"--allowed-user", "234567890123456789",
	}, strings.NewReader(""), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), "setup is missing or stale") {
		t.Fatalf("channel setup error = %v", err)
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

func TestScheduleTriggerRunsFreshTaskAndDeduplicates(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "billing", "sweep.md"), "---\ncron: '0 9 * * 1-5'\n---\n\nSweep stale billing work.\n", 0o644)
	logPath := filepath.Join(t.TempDir(), "claude.log")
	t.Setenv("FAKE_LOG", logPath)
	command := filepath.Join(t.TempDir(), "claude")
	writeCLIFile(t, command, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "2.1.221 (Claude Code)"
  exit 0
fi
printf 'RUN\nARGS' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >> "$FAKE_LOG"
IFS= read -r line || exit 1
printf 'IN\t%s\n' "$line" >> "$FAKE_LOG"
session_id="session-$$"
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$session_id"
printf '{"type":"stream_event","session_id":"%s","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"SECRET MODEL OUTPUT"}}}\n' "$session_id"
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"%s","result":"SECRET MODEL OUTPUT"}\n' "$session_id"
`, 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "claude", "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}

	trigger := func(inputID string) string {
		t.Helper()
		output.Reset()
		stderr.Reset()
		err := Run([]string{"schedule", "trigger", source, "billing/sweep", "--workspace", workspace, "--harness", "claude", "--input-id", inputID, "--command", command}, strings.NewReader(""), &output, &stderr, self)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "SECRET MODEL OUTPUT") {
			t.Fatalf("schedule output exposed model text: %q", output.String())
		}
		return output.String()
	}

	if got := trigger("occurrence-1"); !strings.Contains(got, "status=completed duplicate=false") {
		t.Fatalf("first trigger output = %q", got)
	}
	if got := trigger("occurrence-1"); !strings.Contains(got, "status=completed duplicate=true") {
		t.Fatalf("duplicate trigger output = %q", got)
	}
	if got := trigger("occurrence-2"); !strings.Contains(got, "status=completed duplicate=false") {
		t.Fatalf("second trigger output = %q", got)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "RUN\n") != 2 || strings.Contains(string(log), "--resume") {
		t.Fatalf("schedule harness runs were not fresh and deduplicated:\n%s", log)
	}
	if strings.Count(string(log), "Sweep stale billing work") != 2 {
		t.Fatalf("schedule prompt was not submitted twice:\n%s", log)
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
