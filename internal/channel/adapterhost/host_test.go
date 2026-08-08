package adapterhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"hctl/channeladapter"
	"hctl/internal/harness/codex"
	"hctl/internal/integration"
	"hctl/internal/project"
	"hctl/internal/secureenv"
	"hctl/internal/setup"
)

func TestCredentialFreeInstalledAdapterConversationAndIsolation(t *testing.T) {
	if os.Getenv("HCTL_FAKE_ADAPTER") == "1" {
		t.Skip("parent-only test")
	}
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Adapter host fixture.\n---\n\nReply briefly.\n", 0o644)
	writeFile(t, filepath.Join(source, "channels", "discord.md"), "---\nmode: ambient\n---\n\nReply to relevant messages.\n", 0o644)
	harness := filepath.Join(t.TempDir(), "codex")
	harnessLog := filepath.Join(t.TempDir(), "harness.log")
	t.Setenv("HCTL_HARNESS_LOG", harnessLog)
	writeFile(t, harness, `#!/bin/sh
if [ -n "${HCTL_DISCORD_TOKEN-}" ]; then exit 90; fi
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) echo '{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}' ;;
  *'"method":"thread/start"'*) printf 'thread/start\n' >> "$HCTL_HARNESS_LOG"; echo '{"id":2,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"},"sandbox":{"type":"readOnly"},"approvalPolicy":"never"}}' ;;
  *'"method":"thread/resume"'*) printf 'thread/resume\n' >> "$HCTL_HARNESS_LOG"; echo '{"id":2,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"},"sandbox":{"type":"readOnly"},"approvalPolicy":"never"}}' ;;
  *'"method":"turn/start"'*)
    echo '{"id":3,"result":{"turn":{"id":"01922222-2222-7222-8222-222222222222","items":[],"status":"inProgress"}}}'
    echo '{"method":"item/agentMessage/delta","params":{"threadId":"01911111-1111-7111-8111-111111111111","turnId":"01922222-2222-7222-8222-222222222222","itemId":"reply","delta":"hello back"}}'
    echo '{"method":"turn/completed","params":{"threadId":"01911111-1111-7111-8111-111111111111","turn":{"id":"01922222-2222-7222-8222-222222222222","items":[],"status":"completed"}}}' ;;
 esac
done
`, 0o755)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launch, store := installFakeAdapter(t, self)
	if _, err := setup.Apply(p, self); err != nil {
		t.Fatalf("apply fixture: %v", err)
	}
	if err := store.RecordConsumption(context.Background(), launch.PackageID, p.AgentID, p.Name, []string{launch.CapabilityID}); err != nil {
		t.Fatalf("record fixture consumption: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "delivered")
	secret := "opaque-adapter-only-value"
	t.Setenv("HCTL_DISCORD_TOKEN", secret)
	environment := secureenv.Replace(AdapterEnvironment("HCTL_DISCORD_TOKEN"), map[string]string{"HCTL_FAKE_ADAPTER": "1", "HCTL_FAKE_MARKER": marker})
	var audit bytes.Buffer
	runtime, err := New(Config{
		Project: p, Driver: codex.New(harness), ProfileID: "default", Environment: environment,
		Launch:      launch,
		TurnTimeout: 2 * time.Second, IdleTimeout: time.Minute, MaxResident: 1, MaxActive: 1, Executable: self, Audit: &audit,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	var earlyErr error
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case earlyErr = <-done:
			deadline = time.Now()
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		cancel()
		t.Fatalf("conversation did not complete: %v runtime=%v audit=%s", err, earlyErr, audit.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("adapter host did not shut down")
	}
	if strings.Contains(audit.String(), secret) {
		t.Fatal("adapter credential reached retained diagnostics")
	}
	methods, err := os.ReadFile(harnessLog)
	if err != nil {
		t.Fatal(err)
	}
	if starts, resumes := bytes.Count(methods, []byte("thread/start\n")), bytes.Count(methods, []byte("thread/resume\n")); starts != 2 || resumes != 1 {
		t.Fatalf("MaxResident=1 harness lifecycle starts=%d resumes=%d log=%q", starts, resumes, methods)
	}
	if err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(secret)) {
			t.Errorf("credential persisted in %s", path)
		}
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
}

func TestInstalledAdapterRunsConcurrentConversations(t *testing.T) {
	if os.Getenv("HCTL_FAKE_ADAPTER") == "1" {
		t.Skip("parent-only test")
	}
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Concurrent adapter fixture.\n---\n\nReply briefly.\n", 0o644)
	writeFile(t, filepath.Join(source, "channels", "discord.md"), "---\nmode: ambient\n---\n\nReply to relevant messages.\n", 0o644)
	harnessLog := filepath.Join(t.TempDir(), "harness.log")
	t.Setenv("HCTL_HARNESS_LOG", harnessLog)
	harness := filepath.Join(t.TempDir(), "codex")
	writeFile(t, harness, `#!/bin/sh
if mkdir "$HCTL_HARNESS_LOG.claim" 2>/dev/null; then
  thread_id=01911111-1111-7111-8111-111111111111
  turn_id=01922222-2222-7222-8222-222222222222
else
  thread_id=01933333-3333-7333-8333-333333333333
  turn_id=01944444-4444-7444-8444-444444444444
fi
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) echo '{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}' ;;
  *'"method":"thread/start"'*) printf 'thread/start\n' >> "$HCTL_HARNESS_LOG"; printf '{"id":2,"result":{"thread":{"id":"%s"},"sandbox":{"type":"readOnly"},"approvalPolicy":"never"}}\n' "$thread_id" ;;
  *'"method":"turn/start"'*)
    printf 'turn/start\n' >> "$HCTL_HARNESS_LOG"
    remaining=300
    while [ "$(grep -c '^turn/start$' "$HCTL_HARNESS_LOG")" -lt 2 ]; do
      remaining=$((remaining - 1)); [ "$remaining" -gt 0 ] || exit 91
      sleep 0.01
    done
    printf '{"id":3,"result":{"turn":{"id":"%s","items":[],"status":"inProgress"}}}\n' "$turn_id"
    printf '{"method":"item/agentMessage/delta","params":{"threadId":"%s","turnId":"%s","itemId":"reply","delta":"hello back"}}\n' "$thread_id" "$turn_id"
    printf '{"method":"turn/completed","params":{"threadId":"%s","turn":{"id":"%s","items":[],"status":"completed"}}}\n' "$thread_id" "$turn_id" ;;
 esac
done
`, 0o755)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launch, store := installFakeAdapter(t, self)
	if _, err := setup.Apply(p, self); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordConsumption(context.Background(), launch.PackageID, p.AgentID, p.Name, []string{launch.CapabilityID}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "concurrent")
	environment := secureenv.Replace(AdapterEnvironment(""), map[string]string{"HCTL_FAKE_ADAPTER": "1", "HCTL_FAKE_SCENARIO": "concurrent", "HCTL_FAKE_MARKER": marker})
	var audit bytes.Buffer
	running, err := New(Config{Project: p, Driver: codex.New(harness), ProfileID: "default", Environment: environment, Launch: launch, TurnTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxResident: 2, MaxActive: 2, Executable: self, Audit: &audit})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- running.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	var earlyErr error
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case earlyErr = <-done:
			deadline = time.Now()
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		cancel()
		methods, _ := os.ReadFile(harnessLog)
		t.Fatalf("concurrent conversations did not complete: %v runtime=%v audit=%s harness=%q", err, earlyErr, audit.String(), methods)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent adapter host did not shut down")
	}
	methods, err := os.ReadFile(harnessLog)
	if err != nil {
		t.Fatal(err)
	}
	if starts, turns := bytes.Count(methods, []byte("thread/start\n")), bytes.Count(methods, []byte("turn/start\n")); starts != 2 || turns != 2 {
		t.Fatalf("concurrent harness lifecycle starts=%d overlapping_turns=%d log=%q", starts, turns, methods)
	}
}

func installFakeAdapter(t *testing.T, executable string) (Launch, *integration.Store) {
	t.Helper()
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	root := t.TempDir()
	document := map[string]any{
		"schema_version": 1, "id": "fixture-channel", "version": "1.0.0", "name": "Fixture channel", "description": "Credential-free external host fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://example.invalid/fixture-channel", "revision": "fixture-v1"},
		"compatibility": map[string]any{"minimum": "0.1.0-dev", "before": "9.0.0"},
		"artifacts":     []any{map[string]any{"id": "current", "os": runtime.GOOS, "architecture": runtime.GOARCH, "format": "binary", "source": map[string]any{"kind": "package", "path": "payload/adapter"}, "size": len(payload), "sha256": checksum, "executable": map[string]any{"path": "bin/adapter", "size": len(payload), "sha256": checksum}}},
		"capabilities":  []any{map[string]any{"type": "channel-adapter", "version": 1, "id": "fixture", "channel_kind": "fixture", "artifacts": []string{"current"}, "executable": "bin/adapter", "runtime": map[string]any{"arguments": []string{"-test.run=^TestFakeAdapterProcess$"}}, "setup": map[string]any{"arguments": []string{"setup"}}, "status": map[string]any{"arguments": []string{"status"}}, "remove": map[string]any{"arguments": []string{"remove"}}, "protocol": map[string]any{"minimum": 1, "before": 2}, "profile_selector": "opaque-id-v1", "features": []string{"typing", "replies", "interactive-components", "text-fallback"}}},
	}
	manifest, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "integration.json"), string(manifest)+"\n", 0o600)
	if err := os.MkdirAll(filepath.Join(root, "payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload", "adapter"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	store := integration.NewStore(t.TempDir(), nil)
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: root, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveChannelAdapter(context.Background(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launch, err := resolved.LaunchDescriptor(integration.ChannelAdapterRuntime, "")
	if err != nil {
		t.Fatal(err)
	}
	return launch, store
}

func TestFakeAdapterProcess(t *testing.T) {
	if os.Getenv("HCTL_FAKE_ADAPTER") != "1" {
		t.Skip("subprocess helper")
	}
	scenario := os.Getenv("HCTL_FAKE_SCENARIO")
	if scenario == "" && os.Getenv("HCTL_DISCORD_TOKEN") == "" {
		os.Exit(81)
	}
	decoder, encoder := channeladapter.NewDecoder(os.Stdin), channeladapter.NewEncoder(os.Stdout)
	limits := channeladapter.Limits{MaxFrameBytes: channeladapter.MaxFrameBytes, MaxTextBytes: channeladapter.MaxTextBytes, MaxAttachments: 0, MaxAttachmentBytes: channeladapter.MaxAttachmentBytes, MaxOutstanding: 16}
	if scenario == "bounds" {
		limits.MaxTextBytes = 4
	}
	features := []channeladapter.Feature{channeladapter.FeatureTyping, channeladapter.FeatureReplies, channeladapter.FeatureInteractiveComponents, channeladapter.FeatureTextFallback}
	hello := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.hello.1", Payload: channeladapter.Hello{ChannelKind: "fixture", Protocol: channeladapter.ProtocolRange{Minimum: 1, Before: 2}, Features: features, Limits: limits}}
	if err := encoder.Write(hello, channeladapter.FromAdapter); err != nil {
		os.Exit(82)
	}
	initialize, err := decoder.Read(channeladapter.FromHost)
	if err != nil {
		os.Exit(83)
	}
	surface := channeladapter.Surface{Route: channeladapter.Route{Handle: "route_1"}, ConversationID: "fixture-conversation-1", Kind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64)}
	readyPayload := channeladapter.Ready{ChannelKind: "fixture", Features: features, Limits: limits}
	if scenario == "recovery" {
		readyPayload.Surfaces = []channeladapter.Surface{surface}
	}
	ready := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.ready.1", CorrelationID: initialize.ID, Payload: readyPayload}
	if err := encoder.Write(ready, channeladapter.FromAdapter); err != nil {
		os.Exit(84)
	}
	_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.connection.1", Payload: channeladapter.Connection{State: channeladapter.ConnectionReady, Attempt: 0}}, channeladapter.FromAdapter)
	if scenario == "child_failure" {
		return
	}
	inbound := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.inbound.1", Payload: channeladapter.InboundMessage{SourceID: "source-1", Route: surface.Route, ConversationID: surface.ConversationID, SurfaceKind: surface.Kind, SurfaceKey: surface.SurfaceKey, PrincipalKey: surface.PrincipalKey, Message: channeladapter.MessageRef{Handle: "message_1"}, Author: channeladapter.Author{Handle: "author_1", Label: "Operator"}, Text: "hello"}}
	switch scenario {
	case "concurrent":
		second := channeladapter.InboundMessage{SourceID: "source-2", Route: channeladapter.Route{Handle: "route_2"}, ConversationID: "fixture-conversation-2", SurfaceKind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("c", 64), PrincipalKey: strings.Repeat("d", 64), Message: channeladapter.MessageRef{Handle: "message_2"}, Author: channeladapter.Author{Handle: "author_2", Label: "Operator"}, Text: "second"}
		if err := encoder.Write(inbound, channeladapter.FromAdapter); err != nil {
			os.Exit(85)
		}
		if err := encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.inbound.2", Payload: second}, channeladapter.FromAdapter); err != nil {
			os.Exit(85)
		}
	case "recovery":
		if err := encoder.Write(inbound, channeladapter.FromAdapter); err != nil {
			os.Exit(85)
		}
	case "controls":
		status := channeladapter.ControlRequest{SourceID: "control.status", Route: surface.Route, ConversationID: surface.ConversationID, SurfaceKind: surface.Kind, SurfaceKey: surface.SurfaceKey, PrincipalKey: surface.PrincipalKey, Message: channeladapter.MessageRef{Handle: "control_message_1"}, Action: channeladapter.ControlStatus}
		reset := status
		reset.SourceID, reset.Message.Handle, reset.Action = "control.reset", "control_message_2", channeladapter.ControlReset
		_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.control.status", Payload: status}, channeladapter.FromAdapter)
		_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.control.reset", Payload: reset}, channeladapter.FromAdapter)
	case "bounds":
		message := inbound.Payload.(channeladapter.InboundMessage)
		message.Text = "too long"
		inbound.Payload = message
		if err := encoder.Write(inbound, channeladapter.FromAdapter); err != nil {
			os.Exit(85)
		}
	default:
		if err := encoder.Write(inbound, channeladapter.FromAdapter); err != nil {
			os.Exit(85)
		}
	}
	replayed, deliveries := false, 0
	for {
		frame, err := decoder.Read(channeladapter.FromHost)
		if err != nil {
			return
		}
		switch payload := frame.Payload.(type) {
		case *channeladapter.EventAck:
			if scenario == "" && frame.CorrelationID == inbound.ID && !replayed {
				replayed = true
				_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.connection.2", Payload: channeladapter.Connection{State: channeladapter.ConnectionReconnecting, Attempt: 1}}, channeladapter.FromAdapter)
				_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.connection.3", Payload: channeladapter.Connection{State: channeladapter.ConnectionReady, Attempt: 1}}, channeladapter.FromAdapter)
				_ = encoder.Write(inbound, channeladapter.FromAdapter)
			} else if scenario == "" && frame.CorrelationID == inbound.ID && payload.Disposition != "duplicate" {
				os.Exit(86)
			}
		case *channeladapter.Activity:
		case *channeladapter.Delivery:
			if scenario == "ambiguous" {
				result := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.delivery.ambiguous", CorrelationID: frame.ID, Payload: channeladapter.DeliveryResult{Disposition: channeladapter.EffectAmbiguous}}
				_ = encoder.Write(result, channeladapter.FromAdapter)
				_ = os.WriteFile(os.Getenv("HCTL_FAKE_MARKER"), []byte("ambiguous\n"), 0o600)
				continue
			}
			if payload.Text != "hello back" || payload.Route.Handle != "route_1" && payload.Route.Handle != "route_2" {
				_, _ = fmt.Fprintf(os.Stderr, "unexpected delivery text=%q route=%q\n", payload.Text, payload.Route.Handle)
				os.Exit(87)
			}
			result := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.delivery.1", CorrelationID: frame.ID, Payload: channeladapter.DeliveryResult{Disposition: channeladapter.EffectExact, Message: &channeladapter.MessageRef{Handle: "reply_1"}}}
			if err := encoder.Write(result, channeladapter.FromAdapter); err != nil {
				os.Exit(88)
			}
			deliveries++
			if scenario == "concurrent" {
				if deliveries == 2 {
					_ = os.WriteFile(os.Getenv("HCTL_FAKE_MARKER"), []byte("concurrent\n"), 0o600)
				}
				continue
			}
			switch deliveries {
			case 1:
				second := channeladapter.InboundMessage{SourceID: "source-2", Route: channeladapter.Route{Handle: "route_2"}, ConversationID: "fixture-conversation-2", SurfaceKind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("c", 64), PrincipalKey: strings.Repeat("d", 64), Message: channeladapter.MessageRef{Handle: "message_2"}, Author: channeladapter.Author{Handle: "author_2", Label: "Operator"}, Text: "second"}
				_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.inbound.2", Payload: second}, channeladapter.FromAdapter)
			case 2:
				third := channeladapter.InboundMessage{SourceID: "source-3", Route: channeladapter.Route{Handle: "route_1"}, ConversationID: "fixture-conversation-1", SurfaceKind: channeladapter.SurfaceDirect, SurfaceKey: strings.Repeat("a", 64), PrincipalKey: strings.Repeat("b", 64), Message: channeladapter.MessageRef{Handle: "message_3"}, Author: channeladapter.Author{Handle: "author_1", Label: "Operator"}, Text: "again"}
				_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.inbound.3", Payload: third}, channeladapter.FromAdapter)
			case 3:
				_ = os.WriteFile(os.Getenv("HCTL_FAKE_MARKER"), []byte("delivered\n"), 0o600)
			}
		case *channeladapter.InteractionRequest:
			if scenario == "recovery" && !payload.Restore || scenario != "recovery" && payload.Restore {
				os.Exit(90)
			}
			receipt := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.interaction.receipt", CorrelationID: frame.ID, Payload: channeladapter.InteractionReceipt{InteractionID: payload.InteractionID, Disposition: channeladapter.EffectExact}}
			_ = encoder.Write(receipt, channeladapter.FromAdapter)
			if scenario == "recovery" {
				_ = os.WriteFile(os.Getenv("HCTL_FAKE_MARKER"), []byte("restored\n"), 0o600)
				continue
			}
			action := channeladapter.AnswerSubmit
			answer := channeladapter.SemanticInteractionAnswer{SchemaVersion: 1, Action: action, Fields: []channeladapter.FieldAnswer{{FieldID: "confirmation", Confirmed: boolAddress(true)}}}
			if scenario == "interaction_cancel" {
				answer.Action, answer.Fields = channeladapter.AnswerCancel, nil
			}
			result := channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.interaction.result", CorrelationID: frame.ID, Payload: channeladapter.InteractionResult{InteractionID: payload.InteractionID, Answer: answer}}
			_ = encoder.Write(result, channeladapter.FromAdapter)
			_ = os.WriteFile(os.Getenv("HCTL_FAKE_MARKER"), []byte("answered\n"), 0o600)
		case *channeladapter.InteractionCancel:
		case *channeladapter.ControlResult:
			if frame.CorrelationID == "adapter.control.reset" && payload.Disposition == channeladapter.ControlExact {
				_ = os.WriteFile(os.Getenv("HCTL_FAKE_MARKER"), []byte("reset\n"), 0o600)
			}
		case *channeladapter.Shutdown:
			_ = encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: "adapter.shutdown.1", CorrelationID: frame.ID, Payload: channeladapter.ShutdownComplete{}}, channeladapter.FromAdapter)
			return
		default:
			_, _ = fmt.Fprintln(os.Stderr, "unexpected host frame")
			os.Exit(89)
		}
	}
}

func TestOperationIsBoundedAndUsesExactEnvironment(t *testing.T) {
	script := filepath.Join(t.TempDir(), "adapter")
	writeFile(t, script, `#!/bin/sh
test "${HCTL_DISCORD_TOKEN-}" = expected || exit 4
operation="$1"
status=ready
test "$operation" = remove && status=removed
test "$2" = --profile && test "$3" = default || exit 5
printf '{"schema_version":1,"operation":"%s","profile_id":"default","status":"%s","message":"ok"}\n' "$operation" "$status"
`, 0o755)
	t.Setenv("HCTL_DISCORD_TOKEN", "expected")
	for _, operation := range []string{"setup", "status", "remove"} {
		result, err := RunOperation(context.Background(), integration.ChannelAdapterMode(operation), Launch{Command: script, Arguments: []string{operation, "--profile", "default"}, WorkingDirectory: filepath.Dir(script)}, AdapterEnvironment("HCTL_DISCORD_TOKEN"), strings.NewReader(""), io.Discard)
		if err != nil || result.Operation != operation || result.ProfileID != "default" || operation == "remove" && result.Status != "removed" {
			t.Fatalf("%s operation = %#v, %v", operation, result, err)
		}
	}
	writeFile(t, script, "#!/bin/sh\nhead -c 20000 /dev/zero | tr '\\000' x\n", 0o755)
	if _, err := RunOperation(context.Background(), integration.ChannelAdapterStatus, Launch{Command: script, Arguments: []string{"status"}, WorkingDirectory: filepath.Dir(script)}, AdapterEnvironment("HCTL_DISCORD_TOKEN"), strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "invalid non-secret result") {
		t.Fatalf("oversized operation result error = %v", err)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
