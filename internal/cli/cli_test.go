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

func TestScheduleTriggerRunsFreshCodexTaskAndDiscardsOutput(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '0 9 * * 1-5'\n---\n\nSweep stale work.\n", 0o644)
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv("FAKE_LOG", logPath)
	command := filepath.Join(t.TempDir(), "codex")
	writeCLIFile(t, command, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "codex-cli 0.144.1"
  exit 0
fi
printf 'ARGS' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >> "$FAKE_LOG"
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}'
      ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*)
      echo '{"id":2,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"}}}'
      ;;
    *'"method":"turn/start"'*)
      turn_id="01922222-2222-7222-8222-222222222222"
      echo '{"id":3,"result":{"turn":{"id":"01922222-2222-7222-8222-222222222222","items":[],"status":"inProgress"}}}'
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"01933333-3333-7333-8333-333333333333","turnId":"01944444-4444-7444-8444-444444444444","itemId":"child-item","delta":"child"}}\n'
      printf '{"method":"turn/completed","params":{"threadId":"01933333-3333-7333-8333-333333333333","turn":{"id":"01944444-4444-7444-8444-444444444444","items":[],"status":"completed"}}}\n'
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"01911111-1111-7111-8111-111111111111","turnId":"%s","itemId":"item-1","delta":"SECRET MODEL OUTPUT"}}\n' "$turn_id"
      printf '{"method":"turn/completed","params":{"threadId":"01911111-1111-7111-8111-111111111111","turn":{"id":"%s","items":[],"status":"completed"}}}\n' "$turn_id"
      ;;
  esac
done
`, 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "codex", "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}

	for _, inputID := range []string{"occurrence-1", "occurrence-1", "occurrence-2"} {
		output.Reset()
		stderr.Reset()
		if err := Run([]string{"schedule", "trigger", source, "sweep", "--workspace", workspace, "--harness", "codex", "--input-id", inputID, "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
			log, _ := os.ReadFile(logPath)
			t.Fatalf("%v\n%s", err, log)
		}
		if strings.Contains(output.String(), "SECRET MODEL OUTPUT") || !strings.Contains(output.String(), "status=completed") {
			t.Fatalf("Codex schedule output = %q", output.String())
		}
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), `"method":"thread/start"`) != 2 || strings.Contains(string(log), `"method":"thread/resume"`) {
		t.Fatalf("Codex schedule tasks were not fresh and deduplicated:\n%s", log)
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
