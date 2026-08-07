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
	"hctl/internal/interaction"
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
printf 'POLICY\t%s\n' "$HCTL_EXECUTION_POLICY" >> "$FAKE_LOG"
turn=0
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":%s,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}\n' "$id"
      ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*)
      case "$line" in
        *'"sandbox":"workspace-write"'*) sandbox=workspaceWrite ;;
        *) sandbox=readOnly ;;
      esac
      printf '{"id":%s,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"},"sandbox":{"type":"%s","networkAccess":false},"approvalPolicy":"never"}}\n' "$id" "$sandbox"
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

	session, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault})
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

	resumed, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), ResumeID: result.SessionID, Policy: harness.PolicyDefault})
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
	methods, texts := wire(t, log)
	wantMethods := []string{"initialize", "initialized", "thread/start", "turn/start", "initialize", "initialized", "thread/resume", "turn/start", "initialize", "initialized", "thread/start", "initialize", "initialized", "thread/start"}
	if !reflect.DeepEqual(methods, wantMethods) {
		t.Fatalf("methods = %v, want %v", methods, wantMethods)
	}
	if !reflect.DeepEqual(texts, []string{"first", "second"}) {
		t.Fatalf("texts = %v", texts)
	}
	if !strings.Contains(log, `"approvalPolicy":"never"`) || !strings.Contains(log, `"sandbox":"read-only"`) || !strings.Contains(log, "POLICY\tread-only") {
		t.Fatalf("read-only policy missing:\n%s", log)
	}
	if !strings.Contains(log, `"sandbox":"workspace-write"`) || !strings.Contains(log, "POLICY\tworkspace-write") {
		t.Fatalf("workspace-write policy missing:\n%s", log)
	}
	if _, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.ExecutionPolicy("unsupported")}); err == nil {
		t.Fatal("unsupported Codex execution policy was accepted")
	}
}

func TestDynamicRequestInputUsesRootTurnProvenanceAndBoundedResult(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv("FAKE_LOG", logPath)
	executable := writeExecutable(t, `#!/bin/sh
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{"userAgent":"fake"}}\n' "$id" ;;
    *'"method":"thread/start"'*) printf '{"id":%s,"result":{"thread":{"id":"root-thread"},"sandbox":{},"approvalPolicy":""}}\n' "$id" ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"root-turn","status":"inProgress"}}}\n' "$id"
      printf '{"id":99,"method":"item/tool/call","params":{"threadId":"root-thread","turnId":"root-turn","callId":"call-1","namespace":"channel","tool":"request_input","arguments":{"schema_version":1,"kind":"confirm","prompt":"Proceed?","policy":{"expires_after_seconds":60,"cancellation":"allowed"},"field":{"id":"approved","kind":"confirm","label":"Proceed","required":true}}}}\n'
      ;;
    *'"id":99'*) printf '{"method":"turn/completed","params":{"threadId":"root-thread","turn":{"id":"root-turn","status":"completed"}}}\n' ;;
  esac
done
`)
	driver := New(executable)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := driver.Open(ctx, harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault, ManagedRequestInput: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.RunTurn(ctx, harness.Input{ID: "message-1", Text: "deploy"}, func(event harness.Event) {
		if event.RequestInput == nil {
			return
		}
		if !event.RequestInput.ProvenRoot() || event.RequestInput.CorrelationID != "call-1" || event.RequestInput.Request.Prompt != "Proceed?" {
			t.Errorf("request event = %#v", event.RequestInput)
		}
		event.RequestInput.Reply <- harness.RequestInputAcknowledgement{
			Accepted: true, Status: "accepted",
			Result: harness.RequestInputToolResult{Disposition: harness.RequestInputContinuationTurn},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "waiting_for_input" {
		t.Fatalf("result = %#v", result)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	log := readFile(t, logPath)
	if !strings.Contains(log, `"experimentalApi":true`) || !strings.Contains(log, `"name":"channel"`) || !strings.Contains(log, `"name":"request_input"`) {
		t.Fatalf("dynamic tool setup missing:\n%s", log)
	}
	if !strings.Contains(log, `"id":99,"result":{"contentItems":[{"text":"continuation_turn","type":"inputText"}],"success":true}`) {
		t.Fatalf("bounded tool result missing:\n%s", log)
	}
}

func TestDynamicRequestInputRejectsUnrelatedThreadBeforeEmission(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv("FAKE_LOG", logPath)
	executable := writeExecutable(t, `#!/bin/sh
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{"userAgent":"fake"}}\n' "$id" ;;
    *'"method":"thread/start"'*) printf '{"id":%s,"result":{"thread":{"id":"root-thread"},"sandbox":{},"approvalPolicy":""}}\n' "$id" ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"root-turn","status":"inProgress"}}}\n' "$id"
      printf '{"id":98,"method":"item/tool/call","params":{"threadId":"child-thread","turnId":"child-turn","callId":"child-call","namespace":"channel","tool":"request_input","arguments":{}}}\n'
      ;;
    *'"id":98'*) printf '{"method":"turn/completed","params":{"threadId":"root-thread","turn":{"id":"root-turn","status":"completed"}}}\n' ;;
  esac
done
`)
	session, err := New(executable).Open(context.Background(), harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault, ManagedRequestInput: true})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	result, err := session.RunTurn(context.Background(), harness.Input{ID: "message-1", Text: "hello"}, func(event harness.Event) {
		if event.RequestInput != nil {
			requests++
		}
	})
	if err != nil || result.Status != "completed" || requests != 0 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if log := readFile(t, logPath); !strings.Contains(log, `"id":98,"result":{"contentItems":[{"text":"interactive input is unavailable in this session","type":"inputText"}],"success":false}`) {
		t.Fatalf("child call was not rejected: %s", log)
	}
}

func TestContinuationResumesSameThreadForStructuredNewTurn(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv("FAKE_LOG", logPath)
	executable := writeExecutable(t, `#!/bin/sh
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{"userAgent":"fake"}}\n' "$id" ;;
    *'"method":"thread/resume"'*) printf '{"id":%s,"result":{"thread":{"id":"root-thread"},"sandbox":{},"approvalPolicy":""}}\n' "$id" ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"answer-turn","status":"inProgress"}}}\n' "$id"
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"root-thread","turnId":"answer-turn","itemId":"answer","delta":"done"}}\n'
      printf '{"method":"turn/completed","params":{"threadId":"root-thread","turn":{"id":"answer-turn","status":"completed"}}}\n'
      ;;
  esac
done
`)
	driver := New(executable)
	confirmed := true
	intent := interaction.ContinuationIntent{
		InteractionID: "interaction_1234567890", InputID: "message-1", Mode: interaction.ContinuationTurn,
		Request: interaction.Request{SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindConfirm, Prompt: "Proceed?", Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed}, Field: &interaction.Field{ID: "approved", Kind: interaction.KindConfirm, Label: "Proceed", Required: true}},
		Answer:  interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}},
	}
	result := driver.ContinueTurn(context.Background(), harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault}, "root-thread", intent, func(harness.Event) {})
	if result.Effect != interaction.EffectSucceeded || result.ResultSessionID != "root-thread" {
		t.Fatalf("result = %#v", result)
	}
	log := readFile(t, logPath)
	if !strings.Contains(log, `"method":"thread/resume"`) || !strings.Contains(log, `"threadId":"root-thread"`) || !strings.Contains(log, `hctl.channel_input_answer`) || !strings.Contains(log, `interaction_id\":\"interaction_1234567890`) {
		t.Fatalf("continuation wire contract missing:\n%s", log)
	}
	if strings.Contains(log, `"method":"turn/steer"`) || strings.Contains(log, `"dynamicTools"`) {
		t.Fatalf("continuation used a live-turn or re-registration path:\n%s", log)
	}
}

func TestContinuationTransportLossIsUncertainAndNeverSteers(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv("FAKE_LOG", logPath)
	executable := writeExecutable(t, `#!/bin/sh
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  id=$(printf '%s\n' "$line" | sed -n 's/.*"id":\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{"userAgent":"fake"}}\n' "$id" ;;
    *'"method":"thread/resume"'*) printf '{"id":%s,"result":{"thread":{"id":"root-thread"},"sandbox":{},"approvalPolicy":""}}\n' "$id" ;;
    *'"method":"turn/start"'*) printf '{"id":%s,"result":{"turn":{"id":"answer-turn","status":"inProgress"}}}\n' "$id"; exit 0 ;;
  esac
done
`)
	confirmed := true
	intent := interaction.ContinuationIntent{
		InteractionID: "interaction_1234567890", InputID: "message-1", Mode: interaction.ContinuationTurn,
		Request: interaction.Request{SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindConfirm, Prompt: "Proceed?", Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed}, Field: &interaction.Field{ID: "approved", Kind: interaction.KindConfirm, Label: "Proceed", Required: true}},
		Answer:  interaction.Answer{SchemaVersion: interaction.SchemaVersion, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}},
	}
	result := New(executable).ContinueTurn(context.Background(), harness.OpenRequest{Root: t.TempDir(), Policy: harness.PolicyDefault}, "root-thread", intent, func(harness.Event) {})
	if result.Effect != interaction.EffectUncertain || result.OriginOutcome != "uncertain" {
		t.Fatalf("result = %#v", result)
	}
	if log := readFile(t, logPath); strings.Contains(log, `"method":"turn/steer"`) {
		t.Fatalf("transport loss was retried or steered: %s", log)
	}
}

func TestReadOnlyPolicyRequiresEffectiveServerConfirmation(t *testing.T) {
	for _, test := range []struct {
		sandbox  string
		approval string
	}{
		{sandbox: "workspaceWrite", approval: "never"},
		{sandbox: "readOnly", approval: "on-request"},
		{sandbox: "", approval: ""},
	} {
		if err := validateEffectivePolicy(harness.PolicyReadOnly, test.sandbox, test.approval); err == nil {
			t.Fatalf("effective policy sandbox=%q approval=%q accepted", test.sandbox, test.approval)
		}
	}
	if err := validateEffectivePolicy(harness.PolicyReadOnly, "readOnly", "never"); err != nil {
		t.Fatal(err)
	}
	if err := validateEffectivePolicy(harness.PolicyWorkspaceWrite, "workspaceWrite", "never"); err != nil {
		t.Fatal(err)
	}
	if err := validateEffectivePolicy(harness.PolicyWorkspaceWrite, "readOnly", "never"); err == nil {
		t.Fatal("workspace-write policy accepted a read-only response")
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
