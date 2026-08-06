package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hctl/internal/harness"
)

func TestStreamJSONStartTurnsAndResume(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "claude.log")
	t.Setenv("FAKE_LOG", logPath)
	executable := writeExecutable(t, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "2.1.221 (Claude Code)"
  exit 0
fi
if [ "${1-}" = "--permission-mode" ] && [ "${3-}" = "--help" ]; then
  echo '  --permission-mode <mode> (choices: plan, acceptEdits)'
  exit 0
fi
printf 'ARGS' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >> "$FAKE_LOG"
printf 'POLICY\t%s\n' "$HCTL_EXECUTION_POLICY" >> "$FAKE_LOG"
first=1
while IFS= read -r line; do
  printf 'IN\t%s\n' "$line" >> "$FAKE_LOG"
  if [ "$first" -eq 1 ]; then
    echo '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
    first=0
  fi
  echo '{"type":"stream_event","session_id":"11111111-1111-4111-8111-111111111111","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}}'
  echo '{"type":"result","subtype":"success","is_error":false,"session_id":"11111111-1111-4111-8111-111111111111","result":"ok"}'
done
`)
	driver := New(executable)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := driver.Verify(ctx); err != nil {
		t.Fatal(err)
	}

	session, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault})
	if err != nil {
		t.Fatal(err)
	}
	if got := session.InitialEvents()[0].Type; got != "driver.ready" {
		t.Fatalf("initial event = %s", got)
	}
	var events []harness.Event
	result, err := session.RunTurn(ctx, harness.Input{ID: "message-1", Text: "first"}, func(event harness.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.SessionID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("turn result = %#v", result)
	}
	if types(events)[0] != "session.started" || types(events)[1] != "turn.started" || types(events)[2] != "agent.output.delta" {
		t.Fatalf("events = %#v", events)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), ResumeID: result.SessionID, Policy: harness.PolicyDefault})
	if err != nil {
		t.Fatal(err)
	}
	events = nil
	if _, err := resumed.RunTurn(ctx, harness.Input{ID: "message-2", Text: "second"}, func(event harness.Event) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Type != "session.resumed" {
		t.Fatalf("resume events = %#v", events)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	writable, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyWorkspaceWrite})
	if err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}

	log := readFile(t, logPath)
	if !strings.Contains(log, "--resume\t11111111-1111-4111-8111-111111111111") {
		t.Fatalf("resume flag missing:\n%s", log)
	}
	if got := inputTexts(t, log); strings.Join(got, ",") != "first,second" {
		t.Fatalf("wire inputs = %v", got)
	}
	if !strings.Contains(log, "--permission-mode\tplan") || !strings.Contains(log, "POLICY\tread-only") {
		t.Fatalf("read-only policy missing:\n%s", log)
	}
	if !strings.Contains(log, "--permission-mode\tacceptEdits") || !strings.Contains(log, "POLICY\tworkspace-write") {
		t.Fatalf("workspace-write policy missing:\n%s", log)
	}
	if _, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.ExecutionPolicy("unsupported")}); err == nil {
		t.Fatal("unsupported Claude execution policy was accepted")
	}
}

func TestReadOnlyOpenRequiresPlanModeCapability(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("FAKE_STARTED", started)
	executable := writeExecutable(t, `#!/bin/sh
if [ "${1-}" = "--permission-mode" ]; then
  exit 2
fi
touch "$FAKE_STARTED"
`)
	driver := New(executable)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyReadOnly}); err == nil || !strings.Contains(err.Error(), "plan permission mode support") {
		t.Fatalf("read-only open error = %v", err)
	}
	if _, err := os.Stat(started); !os.IsNotExist(err) {
		t.Fatalf("Claude session started before policy verification: %v", err)
	}
}

func types(events []harness.Event) []string {
	result := make([]string, 0, len(events))
	for _, event := range events {
		result = append(result, event.Type)
	}
	return result
}

func inputTexts(t *testing.T, log string) []string {
	t.Helper()
	var result []string
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "IN\t") {
			continue
		}
		var message struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "IN\t")), &message); err != nil {
			t.Fatal(err)
		}
		result = append(result, message.Message.Content)
	}
	return result
}

func writeExecutable(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
