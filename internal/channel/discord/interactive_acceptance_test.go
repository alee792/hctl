package discord

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"hctl/internal/channel/controller"
	"hctl/internal/channelconfig"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/harness/codex"
	"hctl/internal/interaction"
	"hctl/internal/project"
	"hctl/internal/session"
)

// TestInteractiveAcceptanceHarnessProcess is a process fixture, not a test in
// the ordinary suite. A wrapper re-executes this test binary so the acceptance
// test crosses the production process and protocol adapters without requiring
// credentials or an installed harness.
func TestInteractiveAcceptanceHarnessProcess(t *testing.T) {
	if os.Getenv("HCTL_ACCEPTANCE_CHILD") == "" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if os.Getenv("HCTL_DISCORD_TOKEN") != "" {
		appendAcceptanceLog("error: Discord credential reached harness child")
		os.Exit(1)
	}
	joinedEnvironment := strings.Join(os.Environ(), "\n")
	joinedArguments := strings.Join(args, "\n")
	for _, forbidden := range []string{acceptanceRequest.Prompt, "continued:yes", "message-sensitive-id", "toolu_acceptance", "call-acceptance"} {
		if strings.Contains(joinedEnvironment, forbidden) || strings.Contains(joinedArguments, forbidden) {
			appendAcceptanceLog("error: semantic or correlation value reached unintended child environment or arguments")
			os.Exit(1)
		}
	}
	appendAcceptanceLog("environment-scrubbed")
	var err error
	switch os.Getenv("HCTL_ACCEPTANCE_HARNESS") {
	case "claude":
		err = runClaudeAcceptanceChild(args)
	case "codex":
		err = runCodexAcceptanceChild(args)
	default:
		err = fmt.Errorf("unknown acceptance harness")
	}
	if err != nil {
		appendAcceptanceLog("error: " + err.Error())
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestDiscordInteractiveRequestProcessExitAndExactContinuation(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			root := discordAcceptanceProject(t)
			p, err := project.Load(root, harnessName)
			if err != nil {
				t.Fatal(err)
			}
			control := t.TempDir()
			t.Setenv("HCTL_ACCEPTANCE_CHILD", "1")
			t.Setenv("HCTL_ACCEPTANCE_HARNESS", harnessName)
			t.Setenv("HCTL_ACCEPTANCE_CONTROL", control)
			secret := "discord-token-must-not-reach-child"
			t.Setenv("HCTL_DISCORD_TOKEN", secret)
			executable := writeInteractiveAcceptanceWrapper(t)
			var driver harness.Driver
			if harnessName == "claude" {
				driver = claude.New(executable)
			} else {
				driver = codex.New(executable)
			}
			var audit bytes.Buffer
			runtime, err := New(p, driver, Config{
				Token: secret, Executable: "/usr/bin/true", Audit: &audit,
				TurnTimeout: 10 * time.Second, IdleTimeout: time.Hour, MaxResident: 1, MaxActive: 1,
				Runtime: channelconfig.Profile{ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := runtime.startController(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(runtime.Close)
			runtime.typing = func(string) error { return nil }
			deliveries := make(chan *discordgo.MessageSend, 8)
			runtime.deliver = func(_ string, message *discordgo.MessageSend) error {
				deliveries <- message
				return nil
			}
			acks := make(chan string, 4)
			runtime.respondNative = func(_ *discordgo.Interaction, response *discordgo.InteractionResponse) error {
				if response.Data != nil {
					acks <- response.Data.Content
				}
				return nil
			}

			runtime.handleMessage(nil, &discordgo.MessageCreate{Message: &discordgo.Message{
				ID: "message-sensitive-id", ChannelID: "555", GuildID: "444", Content: "sensitive deployment prompt",
				Author: &discordgo.User{ID: "333"}, Mentions: []*discordgo.User{{ID: "222"}},
			}})
			rendered := waitInteractiveDeliveryWithEvidence(t, deliveries, &audit, control)
			if rendered.Content != acceptanceRequest.Prompt || len(rendered.Components) == 0 || rendered.AllowedMentions == nil || len(rendered.AllowedMentions.Parse) != 0 {
				data, _ := os.ReadFile(filepath.Join(control, "child.log"))
				durable, _ := session.Load(root)
				t.Fatalf("rendered interaction = %#v; audit=%q child=%q state=%#v", rendered, audit.String(), data, acceptanceConversation(durable, conversationID("111", "555")))
			}
			waitForAcceptanceMarker(t, control, "initial-exited")
			status := waitForParkedCapacity(t, runtime, conversationID("111", "555"))
			if status.Conversation.State != "waiting_for_input" || status.Capacity.Active != 0 || status.Capacity.Resident != 0 {
				t.Fatalf("parked capacity = %#v", status)
			}

			// Pending input is not an ordinary queued turn, and /new cannot erase it.
			runtime.handleMessage(nil, &discordgo.MessageCreate{Message: &discordgo.Message{
				ID: "second-message", ChannelID: "555", GuildID: "444", Content: "second ordinary message",
				Author: &discordgo.User{ID: "333"},
			}})
			blocked := waitInteractiveDeliveryWithEvidence(t, deliveries, &audit, control)
			if blocked.Reference == nil || blocked.Reference.MessageID != "second-message" || blocked.Content != "I couldn't handle that request in this conversation. Please try again." || blocked.AllowedMentions == nil || len(blocked.AllowedMentions.Parse) != 0 {
				t.Fatalf("second-message policy = %#v", blocked)
			}
			if after := runtime.controller.Status(conversationID("111", "555")); after.Conversation.State != "waiting_for_input" || after.Conversation.Pending != 1 {
				t.Fatalf("second message changed parked queue = %#v", after)
			}
			var commandReply string
			runtime.interactionResponse = func(_ *discordgo.Interaction, content string) { commandReply = content }
			runtime.handleInteraction(nil, commandInteraction("new"))
			if !strings.Contains(commandReply, "busy") {
				t.Fatalf("/new while parked = %q", commandReply)
			}

			pending, ok, err := runtime.controller.PendingInteraction("555", conversationID("111", "555"))
			if err != nil || !ok {
				t.Fatalf("pending interaction = %#v, %v", pending, err)
			}
			callback := componentInteraction(customID(pending.InteractionID, "y"), discordgo.ButtonComponent)
			runtime.handleInteraction(nil, callback)
			select {
			case ack := <-acks:
				if ack != "Answer received." {
					t.Fatalf("callback acknowledgement = %q", ack)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("callback was not acknowledged")
			}
			final := waitInteractiveDeliveryWithEvidence(t, deliveries, &audit, control)
			if final.Content != "continued:yes" || final.Reference == nil || final.Reference.MessageID != "message-sensitive-id" || final.AllowedMentions == nil || len(final.AllowedMentions.Parse) != 0 {
				t.Fatalf("continued delivery = %#v", final)
			}
			waitForAcceptanceMarker(t, control, "continuation-exited")

			state, err := session.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			conversation := acceptanceConversation(state, conversationID("111", "555"))
			if conversation == nil || conversation.Interaction != nil || len(conversation.InteractionTombstones) != 1 || conversation.InteractionTombstones[0].Phase != interaction.PhaseCompleted || len(conversation.Queue) != 0 {
				t.Fatalf("completed durable state = %#v", conversation)
			}
			select {
			case duplicate := <-deliveries:
				t.Fatalf("continuation produced an extra Discord delivery: %#v", duplicate)
			case <-time.After(100 * time.Millisecond):
			}
			logData, err := os.ReadFile(filepath.Join(control, "child.log"))
			if err != nil {
				t.Fatal(err)
			}
			logText := string(logData)
			if strings.Contains(logText, secret) || !strings.Contains(logText, "initial-exited") || !strings.Contains(logText, "continuation-exited") {
				t.Fatalf("child process evidence = %q", logText)
			}
			for _, forbidden := range []string{
				secret, "sensitive deployment prompt", acceptanceRequest.Prompt, "continued:yes", "approved",
				"message-sensitive-id", pending.InteractionID, customID(pending.InteractionID, "y"),
				"toolu_acceptance", "call-acceptance", "11111111-1111-4111-8111-111111111111",
				"root-thread", "origin-turn", "continuation-turn",
			} {
				if strings.Contains(audit.String(), forbidden) {
					t.Fatalf("audit exposed %q: %s", forbidden, audit.String())
				}
			}
		})
	}
}

func TestDiscordCodexRequestInputRemainsAvailableAfterNew(t *testing.T) {
	root := discordAcceptanceProject(t)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	control := t.TempDir()
	t.Setenv("HCTL_ACCEPTANCE_CHILD", "1")
	t.Setenv("HCTL_ACCEPTANCE_HARNESS", "codex")
	t.Setenv("HCTL_ACCEPTANCE_CONTROL", control)
	runtime, err := New(p, codex.New(writeInteractiveAcceptanceWrapper(t)), Config{
		Executable: "/usr/bin/true", Audit: io.Discard, TurnTimeout: 10 * time.Second, IdleTimeout: time.Hour, MaxResident: 1, MaxActive: 1,
		Runtime: channelconfig.Profile{ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.startController(); err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	runtime.typing = func(string) error { return nil }
	deliveries := make(chan *discordgo.MessageSend, 8)
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error { deliveries <- message; return nil }
	runtime.respondNative = func(*discordgo.Interaction, *discordgo.InteractionResponse) error { return nil }

	runtime.handleMessage(nil, acceptanceInboundDM("first-message"))
	first := waitInteractiveDeliveryWithEvidence(t, deliveries, &bytes.Buffer{}, control)
	if first.Content != acceptanceRequest.Prompt {
		t.Fatalf("first interaction = %#v", first)
	}
	conversation := conversationID("111", "dm-555")
	pending, ok, err := runtime.controller.PendingInteraction("dm-555", conversation)
	if err != nil || !ok {
		t.Fatalf("first pending interaction = %#v, %v", pending, err)
	}
	waitForAcceptanceMarker(t, control, "initial-exited")
	if status := waitForParkedCapacity(t, runtime, conversation); status.Conversation.State != "waiting_for_input" || status.Capacity.Active != 0 || status.Capacity.Resident != 0 {
		t.Fatalf("first interaction did not release capacity = %#v", status)
	}
	firstCallback := componentInteraction(customID(pending.InteractionID, "y"), discordgo.ButtonComponent)
	firstCallback.GuildID, firstCallback.ChannelID, firstCallback.Member, firstCallback.User = "", "dm-555", nil, &discordgo.User{ID: "333"}
	runtime.handleInteraction(nil, firstCallback)
	completed := waitInteractiveDeliveryWithEvidence(t, deliveries, &bytes.Buffer{}, control)
	if completed.Content != "continued:yes" {
		t.Fatalf("first continuation = %#v", completed)
	}
	waitForAcceptanceMarker(t, control, "continuation-exited")

	var commandReply string
	runtime.interactionResponse = func(_ *discordgo.Interaction, content string) { commandReply = content }
	newCommand := commandInteraction("new")
	newCommand.GuildID, newCommand.ChannelID, newCommand.Member, newCommand.User = "", "dm-555", nil, &discordgo.User{ID: "333"}
	runtime.handleInteraction(nil, newCommand)
	if commandReply != "Started a new conversation." {
		t.Fatalf("/new response = %q", commandReply)
	}
	runtime.handleMessage(nil, acceptanceInboundDM("second-message"))
	second := waitInteractiveDeliveryWithEvidence(t, deliveries, &bytes.Buffer{}, control)
	if second.Content != secondAcceptanceRequest.Prompt || len(second.Components) == 0 {
		t.Fatalf("second interaction = %#v", second)
	}
	waitForAcceptanceMarker(t, control, "second-initial-exited")
	waitForAcceptanceMarker(t, control, "second-tool-registered")
	secondPending, ok, err := runtime.controller.PendingInteraction("dm-555", conversation)
	if err != nil || !ok || secondPending.Request.Kind != interaction.KindChooseOne {
		t.Fatalf("second pending interaction = %#v, %v", secondPending, err)
	}
}

func acceptanceInboundDM(id string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: id, ChannelID: "dm-555", Content: "exercise managed input", Author: &discordgo.User{ID: "333"},
	}}
}

func TestDiscordSemanticFixturesNormalizeAcrossNativeAndFallback(t *testing.T) {
	type fixture struct {
		name          string
		fallbackReply string
		native        func(interaction.PendingInteraction) (*discordgo.InteractionCreate, string)
		prepareNative func(*interaction.Request)
	}
	fixtures := []fixture{
		{name: "confirm", fallbackReply: "yes", native: func(p interaction.PendingInteraction) (*discordgo.InteractionCreate, string) {
			return componentInteraction(customID(p.InteractionID, "y"), discordgo.ButtonComponent), "y"
		}},
		{name: "choose_one", fallbackReply: "2", prepareNative: func(request *interaction.Request) {
			request.Field.AllowFreeform = false
			request.Field.MinLength, request.Field.MaxLength = 0, 0
		}, native: func(p interaction.PendingInteraction) (*discordgo.InteractionCreate, string) {
			return componentInteraction(customID(p.InteractionID, "o1"), discordgo.ButtonComponent), "o1"
		}},
		{name: "choose_many", fallbackReply: "3, 1", native: func(p interaction.PendingInteraction) (*discordgo.InteractionCreate, string) {
			incoming := componentInteraction(customID(p.InteractionID, "s"), discordgo.SelectMenuComponent)
			data := incoming.Data.(discordgo.MessageComponentInteractionData)
			data.Values = []string{"v2", "v0"}
			incoming.Data = data
			return incoming, "s"
		}},
		{name: "text", fallbackReply: "Ready\nfor release", native: func(p interaction.PendingInteraction) (*discordgo.InteractionCreate, string) {
			input := discordgo.TextInput{CustomID: customID(p.InteractionID, "f0"), Value: "Ready\nfor release"}
			data := discordgo.ModalSubmitInteractionData{CustomID: customID(p.InteractionID, "m"), Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{input}}}}
			return callbackInteraction(discordgo.InteractionModalSubmit, data), "m"
		}},
		{name: "date_time", fallbackReply: "2026-08-07T09:30:00-07:00"},
		{name: "form", fallbackReply: "environment: 1\nrelease_date: 2026-08-07\nnote: Canary first"},
	}
	runtime := testRuntime(newFakeChannelController())
	for _, test := range fixtures {
		t.Run(test.name, func(t *testing.T) {
			request := readInteractionRequestFixture(t, test.name)
			expected, err := interaction.NormalizeAnswer(request, readInteractionAnswerFixture(t, test.name))
			if err != nil {
				t.Fatal(err)
			}
			fallback, err := interaction.ParseTextAnswer(request, test.fallbackReply)
			if err != nil || !reflect.DeepEqual(fallback, expected) {
				t.Fatalf("fallback answer = %#v, want %#v, err=%v", fallback, expected, err)
			}
			resolution, err := interaction.Resolve(request, runtime.Capabilities())
			if test.native == nil {
				if err != nil || resolution.Mode != interaction.RenderTextFallback {
					t.Fatalf("expected deterministic fallback, got %#v, %v", resolution, err)
				}
				return
			}
			nativeRequest := request
			if test.prepareNative != nil {
				test.prepareNative(&nativeRequest)
			}
			resolution, err = interaction.Resolve(nativeRequest, runtime.Capabilities())
			if err != nil || resolution.Mode != interaction.RenderNative {
				t.Fatalf("expected native resolution, got %#v, %v", resolution, err)
			}
			pending := discordPending(runtime, nativeRequest, interaction.RenderNative)
			incoming, action := test.native(pending)
			native, err := decodeNativeAnswer(incoming, pending, action)
			if err != nil || !reflect.DeepEqual(native, expected) {
				t.Fatalf("native answer = %#v, want %#v, err=%v", native, expected, err)
			}
		})
	}

	t.Run("text_only_form", func(t *testing.T) {
		request := discordTextFormRequest()
		first, second := "release", "details"
		expected, err := interaction.NormalizeAnswer(request, interaction.Answer{SchemaVersion: 1, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "title", Text: &first}, {FieldID: "details", Text: &second}}})
		if err != nil {
			t.Fatal(err)
		}
		fallback, err := interaction.ParseTextAnswer(request, "title: release\ndetails: details")
		if err != nil || !reflect.DeepEqual(fallback, expected) {
			t.Fatalf("form fallback = %#v, want %#v, err=%v", fallback, expected, err)
		}
		pending := discordPending(runtime, request, interaction.RenderNative)
		data := discordgo.ModalSubmitInteractionData{CustomID: customID(pending.InteractionID, "m"), Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.TextInput{CustomID: customID(pending.InteractionID, "f0"), Value: first}}},
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.TextInput{CustomID: customID(pending.InteractionID, "f1"), Value: second}}},
		}}
		native, err := decodeNativeAnswer(callbackInteraction(discordgo.InteractionModalSubmit, data), pending, "m")
		if err != nil || !reflect.DeepEqual(native, expected) {
			t.Fatalf("form native = %#v, want %#v, err=%v", native, expected, err)
		}
	})
}

func readInteractionRequestFixture(t *testing.T, name string) interaction.Request {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "interaction", "testdata", "requests", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := interaction.DecodeRequest(data)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func readInteractionAnswerFixture(t *testing.T, name string) interaction.Answer {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "interaction", "testdata", "answers", name+".valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	answer, err := interaction.DecodeAnswer(data)
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

func waitForParkedCapacity(t *testing.T, runtime *Runtime, conversation string) controller.Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := runtime.controller.Status(conversation)
		if status.Conversation.State == "waiting_for_input" && status.Capacity.Active == 0 && status.Capacity.Resident == 0 {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	return runtime.controller.Status(conversation)
}

func writeInteractiveAcceptanceWrapper(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-harness")
	content := fmt.Sprintf("#!/bin/sh\nexec %q -test.run '^TestInteractiveAcceptanceHarnessProcess$' -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitInteractiveDeliveryWithEvidence(t *testing.T, deliveries <-chan *discordgo.MessageSend, audit *bytes.Buffer, control string) *discordgo.MessageSend {
	t.Helper()
	select {
	case message := <-deliveries:
		return message
	case <-time.After(10 * time.Second):
		data, _ := os.ReadFile(filepath.Join(control, "child.log"))
		t.Fatalf("timed out waiting for Discord delivery; audit=%q child=%q", audit.String(), data)
		return nil
	}
}

func waitForAcceptanceMarker(t *testing.T, control, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(control, "child.log"))
		if strings.Contains(string(data), marker) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for process marker %q", marker)
}

var acceptanceRequest = interaction.Request{
	SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindConfirm, Prompt: "Approve deployment?",
	Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
	Field:  &interaction.Field{ID: "approved", Kind: interaction.KindConfirm, Label: "Approve", Required: true},
}

var secondAcceptanceRequest = interaction.Request{
	SchemaVersion: interaction.SchemaVersion, Kind: interaction.KindChooseOne, Prompt: "Choose environment?",
	Policy: interaction.Policy{ExpiresAfterSeconds: interaction.MinExpirySeconds, Cancellation: interaction.CancellationAllowed},
	Field: &interaction.Field{ID: "environment", Kind: interaction.KindChooseOne, Label: "Environment", Required: true, MinSelections: 1, MaxSelections: 1, Options: []interaction.Option{
		{ID: "staging", Label: "Staging", Value: "staging"}, {ID: "production", Label: "Production", Value: "production"},
	}},
}

func acceptanceRequestJSON() []byte {
	data, _ := json.Marshal(acceptanceRequest)
	return data
}

func secondAcceptanceRequestJSON() []byte {
	data, _ := json.Marshal(secondAcceptanceRequest)
	return data
}

func appendAcceptanceLog(line string) {
	path := filepath.Join(os.Getenv("HCTL_ACCEPTANCE_CONTROL"), "child.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = fmt.Fprintln(file, line)
		_ = file.Close()
	}
}

func runClaudeAcceptanceChild(args []string) error {
	if os.Getenv(claude.DeferredBrokerEnv) == "" && (len(args) == 0 || args[0] != "--version" && args[0] != "--permission-mode") {
		return fmt.Errorf("Claude deferred broker path missing")
	}
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("2.1.221 (Claude Code)")
		return nil
	}
	if len(args) > 0 && args[0] == "--permission-mode" {
		fmt.Printf("--permission-mode %s\n", args[1])
		return nil
	}
	resume := false
	for _, arg := range args {
		resume = resume || arg == "--resume"
	}
	request := acceptanceRequestJSON()
	hookInput, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": claude.ManagedRequestInputTool, "tool_use_id": "toolu_acceptance", "tool_input": json.RawMessage(request), "session_id": "session", "cwd": "/tmp",
	})
	var hookOutput bytes.Buffer
	if err := claude.RunDeferredHook(bytes.NewReader(hookInput), &hookOutput, os.Getenv(claude.DeferredBrokerEnv)); err != nil {
		return err
	}
	hook := bytes.TrimSpace(hookOutput.Bytes())
	sessionID := "11111111-1111-4111-8111-111111111111"
	if !resume {
		if !strings.Contains(string(hook), `"permissionDecision":"defer"`) {
			return fmt.Errorf("initial Claude hook did not defer: %s", hook)
		}
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return fmt.Errorf("initial Claude input missing")
		}
		fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q}\n", sessionID)
		fmt.Printf("{\"type\":\"stream_event\",\"session_id\":%q,\"event\":{\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_acceptance\",\"name\":%q,\"input\":%s}}}\n", sessionID, claude.ManagedRequestInputTool, request)
		fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"stop_reason\":\"tool_deferred\",\"session_id\":%q,\"deferred_tool_use\":{\"id\":\"toolu_acceptance\",\"name\":%q,\"input\":%s}}\n", sessionID, claude.ManagedRequestInputTool, request)
		appendAcceptanceLog("initial-exited")
		return nil
	}
	var hookResponse struct {
		HookSpecificOutput struct {
			UpdatedInput json.RawMessage `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(hook, &hookResponse); err != nil || len(hookResponse.HookSpecificOutput.UpdatedInput) == 0 {
		return fmt.Errorf("resumed Claude hook did not provide updated input: %s", hook)
	}
	confirmed := true
	expected, digest, buildErr := claude.BuildDeferredUpdatedInput(acceptanceRequest, interaction.Answer{SchemaVersion: 1, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "approved", Confirmed: &confirmed}}}, "toolu_acceptance")
	if buildErr != nil || !bytes.Equal(expected, hookResponse.HookSpecificOutput.UpdatedInput) || digest == "" {
		return fmt.Errorf("Claude hook-produced input was not canonical")
	}
	if _, err := claude.RequestDeferredBrokerResult(os.Getenv(claude.DeferredBrokerEnv), hookResponse.HookSpecificOutput.UpdatedInput); err != nil {
		return err
	}
	fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q}\n", sessionID)
	fmt.Printf("{\"type\":\"stream_event\",\"session_id\":%q,\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"continued:yes\"}}}\n", sessionID)
	fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"stop_reason\":\"end_turn\",\"session_id\":%q,\"result\":\"continued:yes\"}\n", sessionID)
	appendAcceptanceLog("continuation-exited")
	return nil
}

func runCodexAcceptanceChild(args []string) error {
	if os.Getenv(claude.DeferredBrokerEnv) != "" {
		return fmt.Errorf("Claude broker path reached Codex child")
	}
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("codex-cli 0.144.1")
		return nil
	}
	resumed := false
	logData, _ := os.ReadFile(filepath.Join(os.Getenv("HCTL_ACCEPTANCE_CONTROL"), "child.log"))
	secondRequest := strings.Contains(string(logData), "continuation-exited")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			return err
		}
		switch message.Method {
		case "initialize":
			fmt.Printf(`{"id":%d,"result":{"userAgent":"acceptance"}}`+"\n", message.ID)
		case "thread/start":
			var params struct {
				DynamicTools []json.RawMessage `json:"dynamicTools"`
			}
			if json.Unmarshal(message.Params, &params) != nil || len(params.DynamicTools) == 0 {
				return fmt.Errorf("Codex managed input tool was not registered")
			}
			if secondRequest {
				appendAcceptanceLog("second-tool-registered")
			}
			fmt.Printf(`{"id":%d,"result":{"thread":{"id":"root-thread"},"sandbox":{"type":"readOnly"},"approvalPolicy":"never"}}`+"\n", message.ID)
		case "thread/resume":
			resumed = true
			fmt.Printf(`{"id":%d,"result":{"thread":{"id":"root-thread"},"sandbox":{"type":"readOnly"},"approvalPolicy":"never"}}`+"\n", message.ID)
		case "turn/start":
			turn := "origin-turn"
			if resumed {
				turn = "continuation-turn"
			}
			turnResponse := fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":%q,"status":"inProgress"}}}`, message.ID, turn)
			fmt.Println(turnResponse)
			if resumed {
				fmt.Printf(`{"method":"item/agentMessage/delta","params":{"threadId":"root-thread","turnId":"continuation-turn","itemId":"answer","delta":"continued:yes"}}` + "\n")
				fmt.Printf(`{"method":"turn/completed","params":{"threadId":"root-thread","turn":{"id":"continuation-turn","status":"completed"}}}` + "\n")
				appendAcceptanceLog("continuation-exited")
				return nil
			}
			request, callID := acceptanceRequestJSON(), "call-acceptance"
			if secondRequest {
				request, callID = secondAcceptanceRequestJSON(), "call-second"
			}
			toolRequest := fmt.Sprintf(`{"id":99,"method":"item/tool/call","params":{"threadId":"root-thread","turnId":"origin-turn","callId":%q,"namespace":"channel","tool":"request_input","arguments":%s}}`, callID, request)
			fmt.Println(toolRequest)
		default:
			if message.ID == 99 && len(message.Result) != 0 {
				fmt.Printf(`{"method":"turn/completed","params":{"threadId":"root-thread","turn":{"id":"origin-turn","status":"completed"}}}` + "\n")
				marker := "initial-exited"
				if secondRequest {
					marker = "second-initial-exited"
				}
				appendAcceptanceLog(marker)
				return nil
			}
		}
	}
	return scanner.Err()
}
