package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hctl/internal/harness"
)

func TestAppServerStartTurnAndResume(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv("FAKE_LOG", logPath)
	executable := writeExecutable(t, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "codex-cli 0.144.1"
  exit 0
fi
printf 'ARGS' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >> "$FAKE_LOG"
turn=0
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}\n' "$id"
      ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*)
      printf '{"id":%s,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"}}}\n' "$id"
      ;;
    *'"method":"turn/start"'*)
      turn=$((turn + 1))
      turn_id="01922222-2222-7222-8222-22222222222$turn"
      printf '{"id":%s,"result":{"turn":{"id":"%s","items":[],"status":"inProgress"}}}\n' "$id" "$turn_id"
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"01933333-3333-7333-8333-333333333333","turnId":"01944444-4444-7444-8444-444444444444","itemId":"child-item","delta":"child"}}\n'
      printf '{"method":"turn/completed","params":{"threadId":"01933333-3333-7333-8333-333333333333","turn":{"id":"01944444-4444-7444-8444-444444444444","items":[],"status":"completed"}}}\n'
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"01911111-1111-7111-8111-111111111111","turnId":"%s","itemId":"item-1","delta":"ok"}}\n' "$turn_id"
      printf '{"method":"turn/completed","params":{"threadId":"01911111-1111-7111-8111-111111111111","turn":{"id":"%s","items":[],"status":"completed"}}}\n' "$turn_id"
      ;;
  esac
done
`)
	driver := New(executable)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := driver.Verify(ctx); err != nil {
		t.Fatal(err)
	}

	session, err := driver.Open(ctx, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	initial := session.InitialEvents()
	if len(initial) != 2 || initial[0].Type != "driver.ready" || initial[1].Type != "session.started" {
		t.Fatalf("initial events = %#v", initial)
	}
	var events []harness.Event
	result, err := session.RunTurn(ctx, harness.Input{ID: "message-1", Text: "first"}, func(event harness.Event) { events = append(events, event) })
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.SessionID != "01911111-1111-7111-8111-111111111111" {
		t.Fatalf("turn result = %#v", result)
	}
	if len(events) != 2 || events[0].Type != "turn.started" || events[1].Type != "agent.output.delta" || events[1].ItemID != "item-1" {
		t.Fatalf("events = %#v", events)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	resumed, err := driver.Open(ctx, t.TempDir(), result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.InitialEvents()[1].Type; got != "session.resumed" {
		t.Fatalf("resume event = %s", got)
	}
	if _, err := resumed.RunTurn(ctx, harness.Input{ID: "message-2", Text: "second"}, func(harness.Event) {}); err != nil {
		t.Fatal(err)
	}
	if err := resumed.Close(); err != nil {
		t.Fatal(err)
	}

	methods, texts := wire(t, readFile(t, logPath))
	wantMethods := []string{"initialize", "initialized", "thread/start", "turn/start", "initialize", "initialized", "thread/resume", "turn/start"}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("methods = %v, want %v", methods, wantMethods)
	}
	if !reflect.DeepEqual(texts, []string{"first", "second"}) {
		t.Fatalf("texts = %v", texts)
	}
}

func wire(t *testing.T, log string) ([]string, []string) {
	t.Helper()
	var methods, texts []string
	for _, line := range strings.Split(log, "\n") {
		if !strings.HasPrefix(line, "WIRE\t") {
			continue
		}
		var message struct {
			Method string `json:"method"`
			Params struct {
				Input []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "WIRE\t")), &message); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, message.Method)
		for _, input := range message.Params.Input {
			if input.Type == "text" {
				texts = append(texts, input.Text)
			}
		}
	}
	return methods, texts
}

func writeExecutable(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex")
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
