package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/dispatch"
	"hctl/internal/schedule"
	"hctl/internal/version"
)

func TestVersionCommandsPrintBuildVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var output, stderr bytes.Buffer
		if err := Run(args, strings.NewReader(""), &output, &stderr, ""); err != nil {
			t.Fatalf("Run(%q): %v", args, err)
		}
		if got, want := output.String(), "hctl "+version.Value+"\n"; got != want {
			t.Fatalf("Run(%q) output = %q, want %q", args, got, want)
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%q) stderr = %q", args, stderr.String())
		}
	}
}

func TestHeadlessCommandIsNamedRun(t *testing.T) {
	var output, stderr bytes.Buffer
	if err := Run([]string{"run", "--help"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Usage: hctl run AGENT") || !strings.Contains(got, "--idle-timeout DURATION") || !strings.Contains(got, "--max-resident-sessions N") || !strings.Contains(got, "--max-active-turns N") || strings.Contains(got, "gateway") {
		t.Fatalf("run help = %q", got)
	}

	output.Reset()
	err := Run([]string{"gateway"}, strings.NewReader(""), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), `unknown command "gateway"`) {
		t.Fatalf("legacy gateway command error = %v", err)
	}
}

func TestStageCreatesRunnableToolFreeFilesystem(t *testing.T) {
	source := filepath.Join(t.TempDir(), "sample-agent")
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	bin := t.TempDir()
	harness := filepath.Join(bin, "codex")
	self := filepath.Join(bin, "hctl")
	writeCLIFile(t, harness, "#!/bin/sh\necho 'codex-cli 0.144.1'\n", 0o755)
	writeCLIFile(t, self, "#!/bin/sh\nexit 0\n", 0o755)
	outputPath := filepath.Join(t.TempDir(), "artifact")
	var output, stderr bytes.Buffer
	if err := Run([]string{"stage", source, "--harness", "codex", "--command", harness, "--output", outputPath}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "staged agent=sample-agent") || !strings.Contains(got, "runtimes=none") {
		t.Fatalf("stage output = %q", got)
	}
	for _, path := range []string{
		"opt/hctl/artifact.json",
		"opt/hctl/bin/hctl",
		"opt/hctl/bin/agent-entrypoint",
		"opt/hctl/harness/bin/codex",
		"opt/hctl/agents/sample-agent/instructions.md",
		"workspace/.codex/config.toml",
		"workspace/.hctl/apply/codex.json",
	} {
		if _, err := os.Stat(filepath.Join(outputPath, filepath.FromSlash(path))); err != nil {
			t.Fatalf("staged %s: %v", path, err)
		}
	}
}

func TestClaudeDeferredHookCommandFailsClosedWithExitZeroResult(t *testing.T) {
	for _, input := range []string{"not-json", `{}`, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_x","tool_input":{}}`} {
		var output, stderr bytes.Buffer
		if err := Run([]string{"hook", "claude-deferred-input"}, strings.NewReader(input), &output, &stderr, ""); err != nil {
			t.Fatalf("malformed hook returned command failure: %v", err)
		}
		if !strings.Contains(output.String(), `"permissionDecision":"deny"`) || strings.Contains(output.String(), `"permissionDecision":"defer"`) {
			t.Fatalf("malformed hook output = %q", output.String())
		}
	}
}

func TestRunRejectsInvalidSessionCapacity(t *testing.T) {
	var output, stderr bytes.Buffer
	err := Run([]string{"run", ".", "--harness", "codex", "--max-resident-sessions", "1", "--max-active-turns", "2"}, strings.NewReader(""), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), "capacity limit is invalid") {
		t.Fatalf("invalid capacity error = %v", err)
	}
}

func TestRunJSONLAutoAppliesAndScrubsDiscordToken(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	harness := filepath.Join(t.TempDir(), "codex")
	writeCLIFile(t, harness, "#!/bin/sh\nif [ -n \"$HCTL_DISCORD_TOKEN\" ]; then exit 7; fi\necho 'codex-cli 0.144.1'\n", 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HCTL_DISCORD_TOKEN", "must-not-reach-child")
	var output, stderr bytes.Buffer
	if err := Run([]string{"run", root, "--harness", "codex", "--command", harness, "--input", "jsonl"}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal("run did not auto-apply")
	}
}

func TestApplyPrintsSafeCompatibilityWarning(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\nfriction-notes: true\n---\n\nBe concise.\n", 0o644)
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
	if !strings.Contains(output.String(), "applied agent=") || !strings.Contains(output.String(), "managed tools=echo,record-friction") {
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

func TestScheduleTriggerTurnDeadlinePersistsUncertainAndAllowsLaterOccurrence(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '0 9 * * 1-5'\n---\n\nSweep stale work.\n", 0o644)
	runtimeRoot := t.TempDir()
	logPath := filepath.Join(runtimeRoot, "claude.log")
	markerPath := filepath.Join(runtimeRoot, "stalled-once")
	t.Setenv("FAKE_LOG", logPath)
	t.Setenv("FAKE_MARKER", markerPath)
	command := filepath.Join(runtimeRoot, "claude")
	writeCLIFile(t, command, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "2.1.221 (Claude Code)"
  exit 0
fi
printf 'RUN\n' >> "$FAKE_LOG"
IFS= read -r line || exit 1
if [ ! -f "$FAKE_MARKER" ]; then
  : > "$FAKE_MARKER"
  IFS= read -r line
  exit 1
fi
session_id="session-$$"
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$session_id"
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"%s","result":"done"}\n' "$session_id"
`, 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "claude", "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}

	trigger := func(inputID string) error {
		output.Reset()
		stderr.Reset()
		return Run([]string{"schedule", "trigger", source, "sweep", "--workspace", workspace, "--harness", "claude", "--input-id", inputID, "--command", command, "--turn-timeout", "20ms", "--timeout", "2s"}, strings.NewReader(""), &output, &stderr, self)
	}
	if err := trigger("occurrence-1"); !errors.Is(err, dispatch.ErrTurnDeadlineExceeded) || !strings.Contains(output.String(), "status=uncertain duplicate=false") || !strings.Contains(output.String(), "reason=deadline_exceeded") {
		t.Fatalf("deadline trigger: err=%v output=%q", err, output.String())
	}
	if err := trigger("occurrence-1"); err == nil || !strings.Contains(err.Error(), "status uncertain") || !strings.Contains(output.String(), "status=uncertain duplicate=true") || !strings.Contains(output.String(), "reason=deadline_exceeded") {
		t.Fatalf("duplicate trigger: err=%v output=%q", err, output.String())
	}
	if err := trigger("occurrence-2"); err != nil || !strings.Contains(output.String(), "status=completed duplicate=false") {
		t.Fatalf("later trigger: err=%v output=%q stderr=%q", err, output.String(), stderr.String())
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "RUN\n") != 2 {
		t.Fatalf("deadline deduplication opened the harness unexpectedly:\n%s", log)
	}
}

func TestScheduleTriggerValidatesTurnTimeoutIndependently(t *testing.T) {
	for _, value := range []string{"0s", "31m"} {
		var output, stderr bytes.Buffer
		err := Run([]string{"schedule", "trigger", "agent", "sweep", "--harness", "claude", "--input-id", "occurrence-1", "--turn-timeout", value}, strings.NewReader(""), &output, &stderr, "")
		if err == nil || !strings.Contains(err.Error(), "--turn-timeout must be greater than zero and at most 30m") {
			t.Fatalf("turn timeout %q error = %v", value, err)
		}
	}
}

func TestScheduleRunHelpAndValidation(t *testing.T) {
	var output, stderr bytes.Buffer
	if err := Run([]string{"schedule", "run", "--help"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "hctl schedule run AGENT") || !strings.Contains(got, "--max-active-turns N") {
		t.Fatalf("help=%q", got)
	}
	if err := Run([]string{"schedule", "run"}, strings.NewReader(""), &output, &stderr, ""); err == nil || !strings.Contains(err.Error(), "schedule run AGENT") {
		t.Fatalf("missing agent error=%v", err)
	}
	for _, args := range [][]string{
		{"schedule", "run", "agent", "--harness", "claude", "--turn-timeout", "0s"},
		{"schedule", "run", "agent", "--harness", "claude", "--max-active-turns", "0"},
	} {
		if err := Run(args, strings.NewReader(""), &output, &stderr, ""); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	if err := Run([]string{"schedule", "run", root, "--harness", "claude"}, strings.NewReader(""), &output, &stderr, ""); err == nil || err.Error() != "agent project defines no schedules" {
		t.Fatalf("no schedules error=%v", err)
	}
}

func TestScheduleRunCancellationStopsHarnessVerification(t *testing.T) {
	source, workspace := t.TempDir(), t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '* * * * *'\n---\n\nSweep.\n", 0o644)
	command := filepath.Join(t.TempDir(), "claude")
	writeCLIFile(t, command, "#!/bin/sh\necho '2.1.221 (Claude Code)'\n", 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var applyOut, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "claude", "--command", command}, strings.NewReader(""), &applyOut, &stderr, self); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "verifying")
	t.Setenv("VERIFY_MARKER", marker)
	writeCLIFile(t, command, "#!/bin/sh\n: > \"$VERIFY_MARKER\"\nexec sleep 300\n", 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runScheduleClockContext(ctx, []string{source, "--workspace", workspace, "--harness", "claude", "--command", command}, io.Discard, &stderr, nil)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("harness verification did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled verification succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled harness verification did not stop")
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

type cliScheduleClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan *cliScheduleTimer
}
type cliScheduleTimer struct{ c chan time.Time }

func (c *cliScheduleClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *cliScheduleClock) NewTimer(time.Duration) schedule.Timer {
	timer := &cliScheduleTimer{c: make(chan time.Time, 1)}
	c.timers <- timer
	return timer
}
func (t *cliScheduleTimer) C() <-chan time.Time { return t.c }
func (*cliScheduleTimer) Stop() bool            { return true }
func (c *cliScheduleClock) wake(t *testing.T, at time.Time) {
	t.Helper()
	select {
	case timer := <-c.timers:
		c.mu.Lock()
		c.now = at
		c.mu.Unlock()
		timer.c <- at
	case <-time.After(time.Second):
		t.Fatal("schedule timer was not created")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.Buffer.String() }

func TestScheduleRunForegroundFakeClaudeAndCodex(t *testing.T) {
	commands := map[string]string{
		"claude": `#!/bin/sh
if [ "${1-}" = "--version" ]; then echo "2.1.221 (Claude Code)"; exit 0; fi
IFS= read -r line || exit 1
printf '{"type":"system","subtype":"init","session_id":"session-fake"}\n'
printf '{"type":"stream_event","session_id":"session-fake","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"SECRET MODEL OUTPUT"}}}\n'
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"session-fake","result":"SECRET MODEL OUTPUT"}\n'
`,
		"codex": `#!/bin/sh
if [ "${1-}" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
while IFS= read -r line; do
 printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
 case "$line" in
  *'"method":"initialize"'*) echo '{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}' ;;
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
`,
	}
	for harnessName, script := range commands {
		t.Run(harnessName, func(t *testing.T) {
			source, workspace := t.TempDir(), t.TempDir()
			writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
			writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '* * * * *'\n---\n\nSweep stale work.\n", 0o644)
			command := filepath.Join(t.TempDir(), harnessName)
			logPath := filepath.Join(t.TempDir(), harnessName+".log")
			t.Setenv("FAKE_LOG", logPath)
			writeCLIFile(t, command, script, 0o755)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			var applyOut, stderr bytes.Buffer
			if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", harnessName, "--command", command}, strings.NewReader(""), &applyOut, &stderr, self); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
			clock := &cliScheduleClock{now: start, timers: make(chan *cliScheduleTimer, 8)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output lockedBuffer
			done := make(chan error, 1)
			go func() {
				done <- runScheduleClockContext(ctx, []string{source, "--workspace", workspace, "--harness", harnessName, "--command", command}, &output, &stderr, clock)
			}()
			clock.wake(t, start.Add(time.Minute))
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && !strings.Contains(output.String(), "status=completed") {
				time.Sleep(time.Millisecond)
			}
			if got := output.String(); !strings.Contains(got, "status=completed") || strings.Contains(got, "SECRET MODEL OUTPUT") || strings.Contains(got, "Sweep stale work") {
				log, _ := os.ReadFile(logPath)
				t.Fatalf("schedule run output=%q stderr=%q log=%s", got, stderr.String(), log)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
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
