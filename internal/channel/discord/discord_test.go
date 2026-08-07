package discord

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"hctl/internal/channel/controller"
	"hctl/internal/channelconfig"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/harness/codex"
	"hctl/internal/interaction"
	"hctl/internal/project"
	"hctl/internal/session"
)

func TestValidateProfilePinsTokenIdentity(t *testing.T) {
	profile := channelconfig.Profile{ApplicationID: "111", BotUserID: "222"}
	if err := ValidateProfile(Identity{ApplicationID: "111", BotUserID: "222"}, profile); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProfile(Identity{ApplicationID: "999", BotUserID: "222"}, profile); err == nil {
		t.Fatal("mismatched application was accepted")
	}
}

func TestRunDoesNotStartControllerBeforeIdentityAgreement(t *testing.T) {
	root := discordAcceptanceProject(t)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(p, codex.New("codex"), Config{
		Token: "test.token.value", Runtime: channelconfig.Profile{
			ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime.validateIdentity = func(context.Context, string) (Identity, error) {
		return Identity{ApplicationID: "999", BotUserID: "222"}, nil
	}
	if err := runtime.Run(context.Background()); err == nil {
		t.Fatal("mismatched identity was accepted")
	}
	if runtime.controller != nil {
		t.Fatal("mismatched identity started recovered workers")
	}
}

func TestEligibleMessageFailsClosed(t *testing.T) {
	profile := channelconfig.Profile{AllowedUserID: "111", AllowedGuildID: "222", AllowedChannelID: "333"}
	message := &discordgo.MessageCreate{Message: &discordgo.Message{Content: "help", ChannelID: "333", GuildID: "222", Author: &discordgo.User{ID: "111"}}}
	if !eligibleMessage(profile, message) {
		t.Fatal("authorized guild message rejected")
	}
	message.Author.ID = "999"
	if eligibleMessage(profile, message) {
		t.Fatal("unauthorized user accepted")
	}
	message.Author.ID, message.ChannelID = "111", "999"
	if eligibleMessage(profile, message) {
		t.Fatal("wrong channel accepted")
	}
	message.GuildID, message.ChannelID = "", "444"
	if !eligibleMessage(profile, message) {
		t.Fatal("authorized DM rejected")
	}
	message.Author.Bot = true
	if eligibleMessage(profile, message) {
		t.Fatal("bot message accepted")
	}
}

func TestDirectMessageClassification(t *testing.T) {
	if !directMessage("222", &discordgo.MessageCreate{Message: &discordgo.Message{}}) {
		t.Fatal("DM was not direct")
	}
	mention := &discordgo.MessageCreate{Message: &discordgo.Message{GuildID: "111", Mentions: []*discordgo.User{{ID: "222"}}}}
	if !directMessage("222", mention) {
		t.Fatal("bot mention was not direct")
	}
	reply := &discordgo.MessageCreate{Message: &discordgo.Message{GuildID: "111", ReferencedMessage: &discordgo.Message{Author: &discordgo.User{ID: "222"}}}}
	if !directMessage("222", reply) {
		t.Fatal("reply to bot was not direct")
	}
	if directMessage("222", &discordgo.MessageCreate{Message: &discordgo.Message{GuildID: "111"}}) {
		t.Fatal("ambient guild message was direct")
	}
}

func TestConversationIDIsStableAndOpaque(t *testing.T) {
	first := conversationID("111", "222")
	if first != conversationID("111", "222") || first == conversationID("111", "333") || strings.Contains(first, "111") {
		t.Fatalf("conversation id = %q", first)
	}
}

func TestDiscordMapsEligibleMessageToNormalizedInput(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	incoming := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "message-1", Content: "please check", ChannelID: "555", GuildID: "444",
		Author: &discordgo.User{ID: "333"}, Mentions: []*discordgo.User{{ID: "222"}},
	}}
	runtime.handleMessage(nil, incoming)
	if len(fake.inputs) != 1 {
		t.Fatalf("normalized inputs = %#v", fake.inputs)
	}
	got := fake.inputs[0]
	if got.SurfaceID != "555" || got.ConversationID != conversationID("111", "555") || got.InputID != "message-1" {
		t.Fatalf("normalized input = %#v", got)
	}
	if !strings.Contains(got.Text, `"platform":"discord"`) || !strings.Contains(got.Text, `"direct":true`) || !strings.Contains(got.Text, `"content":"please check"`) {
		t.Fatalf("normalized text = %q", got.Text)
	}
	if got.Target != (replyTarget{channelID: "555", messageID: "message-1"}) {
		t.Fatalf("reply target = %#v", got.Target)
	}
}

func TestDiscordRendersAdmissionFailureAgainstTriggeringMessage(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	var delivered []*discordgo.MessageSend
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error {
		delivered = append(delivered, message)
		return nil
	}
	if err := runtime.Deliver(controller.Outcome{InputID: "message-1", Target: replyTarget{channelID: "555", messageID: "message-1"}, Failure: controller.FailureAdmission}); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].Reference == nil || delivered[0].Reference.MessageID != "message-1" || !strings.Contains(delivered[0].Content, "this conversation") {
		t.Fatalf("failure delivery = %#v", delivered)
	}
}

func TestDiscordRendersSafeTerminalFailureMessages(t *testing.T) {
	tests := map[controller.Failure]string{
		controller.FailureProcess:   "hit an error",
		controller.FailureCancelled: "was cancelled",
		controller.FailureUncertain: "lost track",
		controller.FailureNoOutput:  "couldn't produce",
	}
	for failure, phrase := range tests {
		t.Run(string(failure), func(t *testing.T) {
			runtime := testRuntime(newFakeChannelController())
			var content string
			runtime.deliver = func(_ string, message *discordgo.MessageSend) error {
				content = message.Content
				return nil
			}
			if err := runtime.Deliver(controller.Outcome{InputID: "input", Target: replyTarget{channelID: "channel", messageID: "message"}, Parts: []string{"unsafe partial"}, Failure: failure}); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(content, phrase) || strings.Contains(content, "unsafe partial") {
				t.Fatalf("failure content = %q", content)
			}
		})
	}
}

func TestDiscordRendersNativeReplyWithReferencesAndDisabledMentions(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	type delivery struct {
		channel string
		message *discordgo.MessageSend
	}
	var delivered []delivery
	runtime.deliver = func(channel string, message *discordgo.MessageSend) error {
		delivered = append(delivered, delivery{channel: channel, message: message})
		return nil
	}
	err := runtime.Deliver(controller.Outcome{
		InputID: "input", Target: replyTarget{channelID: "channel", messageID: "message"}, Parts: []string{"I'll check that.", "Yes, origin is configured."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || delivered[0].channel != "channel" || delivered[0].message.Content != "I'll check that." || delivered[1].message.Content != "Yes, origin is configured." {
		t.Fatalf("deliveries = %#v", delivered)
	}
	if delivered[0].message.Reference == nil || delivered[0].message.Reference.MessageID != "message" || delivered[1].message.Reference != nil {
		t.Fatalf("references = %#v %#v", delivered[0].message.Reference, delivered[1].message.Reference)
	}
	for _, got := range delivered {
		if got.message.AllowedMentions == nil || len(got.message.AllowedMentions.Parse) != 0 {
			t.Fatalf("mentions were not disabled: %#v", got.message.AllowedMentions)
		}
	}
}

func TestDiscordChunksBoundedOutcome(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	var chunks []string
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error {
		chunks = append(chunks, message.Content)
		return nil
	}
	err := runtime.Deliver(controller.Outcome{Target: replyTarget{channelID: "channel", messageID: "message"}, Parts: []string{strings.Repeat("a", controller.DefaultMaxOutputRunes)}, Truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != maxChunks || !strings.HasSuffix(chunks[len(chunks)-1], "[output truncated]") {
		t.Fatalf("chunks = %d, last = %q", len(chunks), chunks[len(chunks)-1])
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 2000 {
			t.Fatal("oversized Discord chunk")
		}
	}
}

func TestDiscordTypingUsesTransportTarget(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	var channels []string
	runtime.typing = func(channel string) error {
		channels = append(channels, channel)
		return nil
	}
	if err := runtime.Typing(replyTarget{channelID: "channel", messageID: "message"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(channels, []string{"channel"}) {
		t.Fatalf("typing channels = %#v", channels)
	}
}

func TestDiscordCapabilitiesDegradeMixedFormsAndFreeformChoices(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	textForm := discordTextFormRequest()
	if resolution, err := interaction.Resolve(textForm, runtime.Capabilities()); err != nil || resolution.Mode != interaction.RenderNative {
		t.Fatalf("text form = %#v, %v", resolution, err)
	}
	mixed := textForm
	mixed.Fields = append(mixed.Fields, interaction.Field{ID: "when", Kind: interaction.KindDateTime, Label: "When", Required: true, DateTimeRepresentation: interaction.DateOnly})
	mixed.FallbackText = "Reply with the requested values."
	if resolution, err := interaction.Resolve(mixed, runtime.Capabilities()); err != nil || resolution.Mode != interaction.RenderTextFallback || resolution.Reason != interaction.ReasonFormFieldKind {
		t.Fatalf("mixed form = %#v, %v", resolution, err)
	}
	choice := discordChoiceRequest()
	choice.Field.AllowFreeform, choice.Field.MinLength, choice.Field.MaxLength = true, 1, 40
	if resolution, err := interaction.Resolve(choice, runtime.Capabilities()); err != nil || resolution.Mode != interaction.RenderTextFallback || resolution.Reason != interaction.ReasonFreeform {
		t.Fatalf("freeform = %#v, %v", resolution, err)
	}
}

func TestDiscordRendersOpaqueNativeComponentsAndClassifiesAmbiguity(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	request := discordConfirmRequest()
	intent := discordRenderIntent(runtime, request, interaction.RenderNative)
	runtime.targets[intent.InputID] = replyTarget{channelID: "555", messageID: "message-1"}
	var sent *discordgo.MessageSend
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error { sent = message; return nil }
	if got := runtime.Render(context.Background(), intent); got != interaction.EffectSucceeded {
		t.Fatalf("render = %q", got)
	}
	if sent == nil || sent.Reference == nil || sent.Reference.MessageID != "message-1" || len(sent.Components) != 1 {
		t.Fatalf("message = %#v", sent)
	}
	row := sent.Components[0].(discordgo.ActionsRow)
	for _, component := range row.Components {
		button := component.(discordgo.Button)
		if len(button.CustomID) > 100 || strings.Contains(button.CustomID, intent.InteractionID) || strings.Contains(button.CustomID, request.Prompt) || !strings.HasPrefix(button.CustomID, "h1.") {
			t.Fatalf("custom ID = %q", button.CustomID)
		}
	}

	intent.InputID = "message-2"
	runtime.targets[intent.InputID] = replyTarget{channelID: "555", messageID: "message-2"}
	runtime.deliver = func(string, *discordgo.MessageSend) error { return errors.New("ambiguous") }
	if got := runtime.Render(context.Background(), intent); got != interaction.EffectUncertain {
		t.Fatalf("ambiguous render = %q", got)
	}
	if _, exists := runtime.targets[intent.InputID]; exists {
		t.Fatal("ambiguous delivery remained retryable")
	}
}

func TestDiscordCallbackCommitsBeforeAcknowledgementAndContinuation(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	request := discordConfirmRequest()
	fake.pending = discordPending(runtime, request, interaction.RenderNative)
	fake.hasPending = true
	runtime.respondNative = func(_ *discordgo.Interaction, response *discordgo.InteractionResponse) error {
		fake.order = append(fake.order, "ack")
		if response.Data == nil || response.Data.AllowedMentions == nil {
			t.Fatal("ack mentions were not disabled")
		}
		return errors.New("acknowledgement outcome unknown")
	}
	incoming := componentInteraction(customID(fake.pending.InteractionID, "y"), discordgo.ButtonComponent)
	runtime.handleInteraction(nil, incoming)
	if !reflect.DeepEqual(fake.order, []string{"accept", "ack", "continue"}) {
		t.Fatalf("order = %v", fake.order)
	}
	if len(fake.attempts) != 1 || !*fake.attempts[0].Answer.Fields[0].Confirmed {
		t.Fatalf("attempt = %#v", fake.attempts)
	}

	fake.order = nil
	incoming.AppID = "wrong-app"
	runtime.handleInteraction(nil, incoming)
	if len(fake.order) != 0 {
		t.Fatalf("cross-application callback accepted: %v", fake.order)
	}
}

func TestDiscordTextAndFormUseAnswerButtonThenModal(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	fake.pending = discordPending(runtime, discordTextFormRequest(), interaction.RenderNative)
	fake.hasPending = true
	var responses []*discordgo.InteractionResponse
	runtime.respondNative = func(_ *discordgo.Interaction, response *discordgo.InteractionResponse) error {
		responses = append(responses, response)
		fake.order = append(fake.order, "ack")
		return nil
	}
	runtime.handleInteraction(nil, componentInteraction(customID(fake.pending.InteractionID, "a"), discordgo.ButtonComponent))
	if len(responses) != 1 || responses[0].Type != discordgo.InteractionResponseModal || len(fake.attempts) != 0 {
		t.Fatalf("modal response = %#v attempts=%d", responses, len(fake.attempts))
	}
	modal := responses[0].Data
	rows := modal.Components
	inputs := make([]discordgo.MessageComponent, len(rows))
	for i, raw := range rows {
		row := raw.(discordgo.ActionsRow)
		input := row.Components[0].(discordgo.TextInput)
		input.Value = []string{"release", "details"}[i]
		inputs[i] = discordgo.ActionsRow{Components: []discordgo.MessageComponent{input}}
	}
	submit := callbackInteraction(discordgo.InteractionModalSubmit, discordgo.ModalSubmitInteractionData{CustomID: modal.CustomID, Components: inputs})
	runtime.handleInteraction(nil, submit)
	if len(fake.attempts) != 1 || len(fake.attempts[0].Answer.Fields) != 2 || *fake.attempts[0].Answer.Fields[1].Text != "details" {
		contents := []string{}
		for _, response := range responses {
			if response.Data != nil {
				contents = append(contents, response.Data.Content)
			}
		}
		t.Fatalf("modal attempt = %#v responses=%v order=%v", fake.attempts, contents, fake.order)
	}
}

func TestDiscordChoiceCallbacksMapOnlyTrustedSlots(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	request := discordChoiceRequest()
	request.Kind = interaction.KindChooseMany
	request.Field.Kind = interaction.KindChooseMany
	request.Field.MaxSelections = 2
	fake.pending = discordPending(runtime, request, interaction.RenderNative)
	fake.hasPending = true
	runtime.respondNative = func(*discordgo.Interaction, *discordgo.InteractionResponse) error { return nil }
	selected := componentInteraction(customID(fake.pending.InteractionID, "s"), discordgo.SelectMenuComponent)
	data := selected.Data.(discordgo.MessageComponentInteractionData)
	data.Values = []string{"v1"}
	selected.Data = data
	runtime.handleInteraction(nil, selected)
	if len(fake.attempts) != 1 || !reflect.DeepEqual(fake.attempts[0].Answer.Fields[0].OptionIDs, []string{"production"}) {
		t.Fatalf("selection = %#v", fake.attempts)
	}

	fake.attempts, fake.order = nil, nil
	data.Values = []string{"production"}
	selected.Data = data
	runtime.handleInteraction(nil, selected)
	if len(fake.attempts) != 0 {
		t.Fatal("semantic option value was trusted as a callback slot")
	}
}

func TestDiscordButtonChoiceAndCancellationPolicy(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	choice := discordPending(runtime, discordChoiceRequest(), interaction.RenderNative)
	selected, err := decodeNativeAnswer(componentInteraction(customID(choice.InteractionID, "o1"), discordgo.ButtonComponent), choice, "o1")
	if err != nil || !reflect.DeepEqual(selected.Fields[0].OptionIDs, []string{"production"}) {
		t.Fatalf("button choice = %#v, %v", selected, err)
	}
	cancelled, err := decodeNativeAnswer(componentInteraction(customID(choice.InteractionID, "x"), discordgo.ButtonComponent), choice, "x")
	if err != nil || cancelled.Action != interaction.ActionCancel {
		t.Fatalf("allowed cancellation = %#v, %v", cancelled, err)
	}
	choice.Request.Policy.Cancellation = interaction.CancellationForbidden
	if _, err := decodeNativeAnswer(componentInteraction(customID(choice.InteractionID, "x"), discordgo.ButtonComponent), choice, "x"); err == nil {
		t.Fatal("forbidden cancellation was accepted")
	}
}

func TestDiscordCallbackDispositionControlsContinuation(t *testing.T) {
	tests := []struct {
		name         string
		disposition  interaction.AnswerDisposition
		err          error
		wantContinue bool
	}{
		{name: "duplicate", disposition: interaction.AnswerDuplicate, wantContinue: true},
		{name: "cancelled", disposition: interaction.AnswerCancelled},
		{name: "conflicting duplicate", err: interaction.ErrInteractionConflict},
		{name: "late answer", err: interaction.ErrInteractionLate},
		{name: "expired answer", err: interaction.ErrInteractionLate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeChannelController()
			fake.acceptDisposition, fake.acceptErr = test.disposition, test.err
			runtime := testRuntime(fake)
			fake.pending = discordPending(runtime, discordConfirmRequest(), interaction.RenderNative)
			fake.hasPending = true
			runtime.respondNative = func(_ *discordgo.Interaction, _ *discordgo.InteractionResponse) error {
				fake.order = append(fake.order, "ack")
				return nil
			}
			runtime.handleInteraction(nil, componentInteraction(customID(fake.pending.InteractionID, "y"), discordgo.ButtonComponent))
			if slices.Contains(fake.order, "continue") != test.wantContinue {
				t.Fatalf("continuation mismatch: %v", fake.order)
			}
			wantOrder := []string{"accept", "ack"}
			if test.wantContinue {
				wantOrder = append(wantOrder, "continue")
			}
			if !reflect.DeepEqual(fake.order, wantOrder) {
				t.Fatalf("order = %v", fake.order)
			}
		})
	}
}

func TestDiscordAuthorizedDMCallbackUsesIndependentSurface(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	fake.pending = discordPending(runtime, discordConfirmRequest(), interaction.RenderNative)
	fake.pending.Owner = runtime.Owner("dm-555")
	fake.hasPending = true
	incoming := componentInteraction(customID(fake.pending.InteractionID, "y"), discordgo.ButtonComponent)
	incoming.GuildID, incoming.ChannelID, incoming.Member = "", "dm-555", nil
	incoming.User = &discordgo.User{ID: "333"}
	runtime.handleInteraction(nil, incoming)
	if len(fake.attempts) != 1 || !reflect.DeepEqual(fake.order, []string{"accept", "continue"}) {
		t.Fatalf("DM callback attempts=%d order=%v", len(fake.attempts), fake.order)
	}
}

func TestDiscordCallbacksFailClosedOnProvenanceAndMalformedData(t *testing.T) {
	mutations := map[string]func(*discordgo.InteractionCreate){
		"wrong application": func(i *discordgo.InteractionCreate) { i.AppID = "999" },
		"wrong guild":       func(i *discordgo.InteractionCreate) { i.GuildID = "999" },
		"wrong channel":     func(i *discordgo.InteractionCreate) { i.ChannelID = "999" },
		"wrong user":        func(i *discordgo.InteractionCreate) { i.Member.User.ID = "999" },
		"bot user":          func(i *discordgo.InteractionCreate) { i.Member.User.Bot = true },
		"malformed data": func(i *discordgo.InteractionCreate) {
			i.Data = discordgo.ApplicationCommandInteractionData{Name: "status"}
		},
		"wrong component kind": func(i *discordgo.InteractionCreate) {
			data := i.Data.(discordgo.MessageComponentInteractionData)
			data.ComponentType = discordgo.SelectMenuComponent
			i.Data = data
		},
		"modal data as component": func(i *discordgo.InteractionCreate) {
			i.Data = discordgo.ModalSubmitInteractionData{CustomID: customID("interaction_0123456789abcdef0123456789abcdef", "y")}
		},
		"button data as modal": func(i *discordgo.InteractionCreate) {
			i.Type = discordgo.InteractionModalSubmit
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			fake := newFakeChannelController()
			runtime := testRuntime(fake)
			fake.pending = discordPending(runtime, discordConfirmRequest(), interaction.RenderNative)
			fake.hasPending = true
			incoming := componentInteraction(customID(fake.pending.InteractionID, "y"), discordgo.ButtonComponent)
			mutate(incoming)
			runtime.handleInteraction(nil, incoming)
			if len(fake.attempts) != 0 {
				t.Fatalf("callback accepted: %#v", fake.attempts)
			}
		})
	}
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	fake.pending = discordPending(runtime, discordConfirmRequest(), interaction.RenderNative)
	fake.hasPending = true
	runtime.closed = true
	runtime.handleInteraction(nil, componentInteraction(customID(fake.pending.InteractionID, "y"), discordgo.ButtonComponent))
	if len(fake.attempts) != 0 {
		t.Fatal("callback was admitted after shutdown")
	}
}

func TestDiscordFallbackChunksAreBoundedCorrelatedAndMentionDisabled(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	request := discordConfirmRequest()
	request.FallbackText = strings.Repeat("a", interaction.MaxFallbackBytes)
	intent := discordRenderIntent(runtime, request, interaction.RenderTextFallback)
	intent.Resolution.FallbackText = request.FallbackText
	runtime.targets[intent.InputID] = replyTarget{channelID: "555", messageID: "message-1"}
	var messages []*discordgo.MessageSend
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error { messages = append(messages, message); return nil }
	if got := runtime.Render(context.Background(), intent); got != interaction.EffectSucceeded || len(messages) < 2 {
		t.Fatalf("render=%q messages=%d", got, len(messages))
	}
	marker := interactionHandle(intent.InteractionID)
	for _, message := range messages {
		if len([]rune(message.Content)) > 2000 || !strings.Contains(message.Content, marker) || message.AllowedMentions == nil || len(message.AllowedMentions.Parse) != 0 || message.Reference == nil {
			t.Fatalf("unsafe fallback chunk = %#v", message)
		}
	}
}

func TestDiscordFallbackStopsAfterAmbiguousPartialDelivery(t *testing.T) {
	runtime := testRuntime(newFakeChannelController())
	request := discordConfirmRequest()
	request.FallbackText = strings.Repeat("a", interaction.MaxFallbackBytes)
	intent := discordRenderIntent(runtime, request, interaction.RenderTextFallback)
	intent.Resolution.FallbackText = request.FallbackText
	runtime.targets[intent.InputID] = replyTarget{channelID: "555", messageID: "message-1"}
	attempts := 0
	runtime.deliver = func(_ string, _ *discordgo.MessageSend) error {
		attempts++
		if attempts == 2 {
			return errors.New("ambiguous")
		}
		return nil
	}
	if got := runtime.Render(context.Background(), intent); got != interaction.EffectUncertain || attempts != 2 {
		t.Fatalf("render=%q attempts=%d", got, attempts)
	}
	if _, exists := runtime.targets[intent.InputID]; exists {
		t.Fatal("partial ambiguous delivery remained retryable")
	}
}

func TestDiscordRestartRendersPersistedRequestExactlyOnce(t *testing.T) {
	root := discordAcceptanceProject(t)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	profile := channelconfig.Profile{ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"}
	conversation := conversationID(profile.ApplicationID, profile.AllowedChannelID)
	ownerRuntime := &Runtime{config: Config{Runtime: profile}}
	state, err := session.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := state.GetOrCreate(p.AgentID, "codex", conversation, p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	durable.SessionID = "thread-1"
	durable.Queue = []session.Input{{ID: "message-1", Text: "origin", Status: "parked"}}
	durable.Interaction = &interaction.Lifecycle{
		ID: "interaction_0123456789abcdef0123456789abcdef", InputID: "message-1", Owner: ownerRuntime.Owner("555"),
		Request: discordConfirmRequest(), Resolution: interaction.Resolution{Mode: interaction.RenderNative},
		ExpiresAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), Continuation: interaction.ContinuationTurn,
		Phase: interaction.PhaseRequested, Delivery: interaction.DeliveryPending,
	}
	if err := session.Save(root, state); err != nil {
		t.Fatal(err)
	}

	start := func() (*Runtime, chan *discordgo.MessageSend) {
		runtime, err := New(p, &recoveryContinuationDriver{}, Config{
			Executable: "/usr/bin/true", TurnTimeout: time.Minute, IdleTimeout: time.Hour, MaxResident: 1, MaxActive: 1, Runtime: profile,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := runtime.startController(); err != nil {
			t.Fatal(err)
		}
		delivered := make(chan *discordgo.MessageSend, 2)
		runtime.deliver = func(_ string, message *discordgo.MessageSend) error { delivered <- message; return nil }
		runtime.recoverSurface("555")
		return runtime, delivered
	}

	first, firstDeliveries := start()
	select {
	case message := <-firstDeliveries:
		if message.Content != "Deploy?" || message.Reference == nil || message.Reference.MessageID != "message-1" || len(message.Components) == 0 || message.AllowedMentions == nil || len(message.AllowedMentions.Parse) != 0 {
			t.Fatalf("recovered render = %#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("persisted request was not rendered after restart")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		persisted, loadErr := session.Load(root)
		var current *session.Conversation
		if loadErr == nil {
			current = acceptanceConversation(persisted, conversation)
		}
		if loadErr == nil && current != nil && current.Interaction != nil && current.Interaction.Phase == interaction.PhaseRendered && current.Interaction.Delivery == interaction.DeliveryDelivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("render completion was not persisted: %#v, %v", current, loadErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	first.Close()
	persisted, err := session.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	afterFirst := acceptanceConversation(persisted, conversation)
	if afterFirst == nil || afterFirst.Interaction == nil || afterFirst.Interaction.Phase != interaction.PhaseRendered || afterFirst.Interaction.Delivery != interaction.DeliveryDelivered {
		t.Fatalf("rendered lifecycle = %#v", afterFirst)
	}

	second, secondDeliveries := start()
	defer second.Close()
	select {
	case duplicate := <-secondDeliveries:
		t.Fatalf("rendered interaction was duplicated after restart: %#v", duplicate)
	case <-time.After(150 * time.Millisecond):
	}
	afterSecondState, err := session.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	afterSecond := acceptanceConversation(afterSecondState, conversation)
	if afterSecond == nil || afterSecond.Interaction == nil || afterSecond.Interaction.Phase != interaction.PhaseRendered || afterSecond.Interaction.Delivery != interaction.DeliveryDelivered {
		t.Fatalf("second recovery changed lifecycle = %#v", afterSecond)
	}
}

func TestDiscordRestartReattachesGuildTargetBeforeRecoveredContinuation(t *testing.T) {
	root := discordAcceptanceProject(t)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	profile := channelconfig.Profile{ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"}
	runtime := &Runtime{project: p, config: Config{Runtime: profile}, targets: map[string]replyTarget{}}
	request := discordConfirmRequest()
	confirmed := true
	answer := interaction.Answer{SchemaVersion: 1, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "deploy", Confirmed: &confirmed}}}
	digest, err := interaction.DigestAnswer(answer)
	if err != nil {
		t.Fatal(err)
	}
	conversation := conversationID(profile.ApplicationID, profile.AllowedChannelID)
	state, err := session.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := state.GetOrCreate(p.AgentID, "codex", conversation, p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	durable.SessionID = "thread-1"
	durable.Queue = []session.Input{{ID: "message-1", Text: "origin", Status: "parked"}}
	durable.Interaction = &interaction.Lifecycle{
		ID: "interaction_0123456789abcdef0123456789abcdef", InputID: "message-1", Owner: runtime.Owner("555"),
		Request: request, Resolution: interaction.Resolution{Mode: interaction.RenderNative},
		ExpiresAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), Continuation: interaction.ContinuationTurn,
		Phase: interaction.PhaseAnswered, Delivery: interaction.DeliveryDelivered, Answer: &answer, AnswerDigest: digest, Resume: interaction.ResumePending,
	}
	if err := session.Save(root, state); err != nil {
		t.Fatal(err)
	}

	driver := &recoveryContinuationDriver{}
	runtime.driver = driver
	runtime.ctx, runtime.cancel = context.WithCancel(context.Background())
	delivered := make(chan *discordgo.MessageSend, 1)
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error { delivered <- message; return nil }
	runtime.typing = func(string) error { return nil }
	managed, err := controller.New(runtime.ctx, controller.Config{
		Project: p, Driver: driver, TurnTimeout: time.Minute, IdleTimeout: time.Hour, MaxResident: 1, MaxActive: 1,
		Interactions: runtime, InitialSurfaces: []controller.InitialSurface{{SurfaceID: "555", ConversationID: conversation}},
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	runtime.controller = managed
	t.Cleanup(managed.Close)
	select {
	case message := <-delivered:
		if message.Content != "recovered answer" || message.Reference == nil || message.Reference.MessageID != "message-1" {
			t.Fatalf("recovered delivery = %#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered continuation output was dropped")
	}
}

func TestDiscordRestartResumesAnsweredDMAfterDuplicateCallback(t *testing.T) {
	root := discordAcceptanceProject(t)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	profile := channelconfig.Profile{ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"}
	runtime := &Runtime{project: p, config: Config{Runtime: profile}, targets: map[string]replyTarget{}}
	confirmed := true
	answer := interaction.Answer{SchemaVersion: 1, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "deploy", Confirmed: &confirmed}}}
	digest, err := interaction.DigestAnswer(answer)
	if err != nil {
		t.Fatal(err)
	}
	dmChannel := "dm-555"
	conversation := conversationID(profile.ApplicationID, dmChannel)
	state, err := session.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := state.GetOrCreate(p.AgentID, "codex", conversation, p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	durable.SessionID = "thread-1"
	durable.Queue = []session.Input{{ID: "message-1", Text: "origin", Status: "parked"}}
	durable.Interaction = &interaction.Lifecycle{
		ID: "interaction_0123456789abcdef0123456789abcdef", InputID: "message-1", Owner: runtime.Owner(dmChannel),
		Request: discordConfirmRequest(), Resolution: interaction.Resolution{Mode: interaction.RenderNative},
		ExpiresAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), Continuation: interaction.ContinuationTurn,
		Phase: interaction.PhaseAnswered, Delivery: interaction.DeliveryDelivered, Answer: &answer, AnswerDigest: digest, Resume: interaction.ResumePending,
	}
	if err := session.Save(root, state); err != nil {
		t.Fatal(err)
	}

	driver := &recoveryContinuationDriver{}
	runtime.driver = driver
	runtime.ctx, runtime.cancel = context.WithCancel(context.Background())
	delivered := make(chan *discordgo.MessageSend, 1)
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error { delivered <- message; return nil }
	runtime.typing = func(string) error { return nil }
	runtime.respondNative = func(*discordgo.Interaction, *discordgo.InteractionResponse) error { return nil }
	managed, err := controller.New(runtime.ctx, controller.Config{
		Project: p, Driver: driver, TurnTimeout: time.Minute, IdleTimeout: time.Hour, MaxResident: 1, MaxActive: 1,
		Interactions: runtime, InitialSurfaces: []controller.InitialSurface{{SurfaceID: "555", ConversationID: conversationID(profile.ApplicationID, "555")}},
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	runtime.controller = managed
	t.Cleanup(managed.Close)

	callback := componentInteraction(customID(durable.Interaction.ID, "y"), discordgo.ButtonComponent)
	callback.GuildID, callback.ChannelID, callback.Member = "", dmChannel, nil
	callback.User = &discordgo.User{ID: profile.AllowedUserID}
	runtime.handleInteraction(nil, callback)
	select {
	case message := <-delivered:
		if message.Content != "recovered answer" || message.Reference == nil || message.Reference.MessageID != "message-1" || message.Reference.ChannelID != dmChannel {
			t.Fatalf("recovered DM delivery = %#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("answered DM continuation remained stuck after duplicate callback")
	}
}

func TestDiscordRestartDoesNotResumeInteractionOwnedByAnotherPrincipal(t *testing.T) {
	root := discordAcceptanceProject(t)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	profile := channelconfig.Profile{ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"}
	runtime := &Runtime{project: p, config: Config{Runtime: profile}, targets: map[string]replyTarget{}}
	confirmed := true
	answer := interaction.Answer{SchemaVersion: 1, Action: interaction.ActionSubmit, Fields: []interaction.FieldAnswer{{FieldID: "deploy", Confirmed: &confirmed}}}
	digest, err := interaction.DigestAnswer(answer)
	if err != nil {
		t.Fatal(err)
	}
	conversation := conversationID(profile.ApplicationID, profile.AllowedChannelID)
	state, err := session.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := state.GetOrCreate(p.AgentID, "codex", conversation, p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	durable.SessionID = "thread-1"
	durable.Queue = []session.Input{{ID: "message-1", Text: "origin", Status: "parked"}}
	durable.Interaction = &interaction.Lifecycle{
		ID: "interaction_0123456789abcdef0123456789abcdef", InputID: "message-1",
		Owner:   interaction.Owner{SurfaceKey: interaction.Digest("wrong-surface"), PrincipalKey: interaction.Digest("wrong-principal")},
		Request: discordConfirmRequest(), Resolution: interaction.Resolution{Mode: interaction.RenderNative},
		ExpiresAt: time.Now().UTC().Truncate(time.Second).Add(time.Hour), Continuation: interaction.ContinuationTurn,
		Phase: interaction.PhaseAnswered, Delivery: interaction.DeliveryDelivered, Answer: &answer, AnswerDigest: digest, Resume: interaction.ResumePending,
	}
	if err := session.Save(root, state); err != nil {
		t.Fatal(err)
	}

	driver := &recoveryContinuationDriver{}
	runtime.driver = driver
	runtime.ctx, runtime.cancel = context.WithCancel(context.Background())
	delivered := make(chan *discordgo.MessageSend, 1)
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error { delivered <- message; return nil }
	runtime.typing = func(string) error { return nil }
	managed, err := controller.New(runtime.ctx, controller.Config{
		Project: p, Driver: driver, TurnTimeout: time.Minute, IdleTimeout: time.Hour, MaxResident: 1, MaxActive: 1,
		Interactions: runtime, InitialSurfaces: []controller.InitialSurface{{SurfaceID: "555", ConversationID: conversation}},
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	runtime.controller = managed
	t.Cleanup(managed.Close)
	select {
	case message := <-delivered:
		t.Fatalf("wrong-owner interaction resumed: %#v", message)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestDiscordFallbackReplyBypassesOrdinaryFIFO(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	request := discordConfirmRequest()
	request.FallbackText = "Please confirm."
	fake.pending = discordPending(runtime, request, interaction.RenderTextFallback)
	fake.pending.Resolution.FallbackText = request.FallbackText
	fake.hasPending = true
	message := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "answer-message", Content: "yes", ChannelID: "555", GuildID: "444", Author: &discordgo.User{ID: "333"},
		ReferencedMessage: &discordgo.Message{Author: &discordgo.User{ID: "222", Bot: true}, Content: "Please confirm.\n\n[hctl request " + interactionHandle(fake.pending.InteractionID) + "]"},
	}}
	runtime.handleMessage(nil, message)
	if len(fake.inputs) != 0 || len(fake.attempts) != 1 || !reflect.DeepEqual(fake.order, []string{"accept", "continue"}) {
		t.Fatalf("inputs=%d attempts=%d order=%v", len(fake.inputs), len(fake.attempts), fake.order)
	}
	stale := *message
	stale.Message = &discordgo.Message{ID: "stale", Content: "yes", ChannelID: "555", GuildID: "444", Author: &discordgo.User{ID: "333"}, ReferencedMessage: &discordgo.Message{Author: &discordgo.User{ID: "222", Bot: true}, Content: "[hctl request 000000000000000000000000]"}}
	runtime.handleMessage(nil, &stale)
	if len(fake.inputs) != 0 || len(fake.attempts) != 1 {
		t.Fatalf("stale fallback was not ignored safely: inputs=%d attempts=%d", len(fake.inputs), len(fake.attempts))
	}
}

func TestDiscordInvalidFallbackReplyGetsBoundedCorrectionAndRemainsPending(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	request := discordConfirmRequest()
	fake.pending = discordPending(runtime, request, interaction.RenderTextFallback)
	fake.pending.Resolution.FallbackText = request.FallbackText
	fake.hasPending = true
	var correction *discordgo.MessageSend
	runtime.deliver = func(_ string, message *discordgo.MessageSend) error { correction = message; return nil }
	incoming := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "invalid-answer", Content: "maybe", ChannelID: "555", GuildID: "444", Author: &discordgo.User{ID: "333"},
		ReferencedMessage: &discordgo.Message{Author: &discordgo.User{ID: "222", Bot: true}, Content: "Please confirm.\n\n[hctl request " + interactionHandle(fake.pending.InteractionID) + "]"},
	}}
	runtime.handleMessage(nil, incoming)
	if len(fake.inputs) != 0 || len(fake.attempts) != 0 || !fake.hasPending {
		t.Fatalf("invalid fallback entered dispatcher inputs=%d attempts=%d pending=%v", len(fake.inputs), len(fake.attempts), fake.hasPending)
	}
	if correction == nil || correction.Content != "That answer doesn't match the requested format. Reply again using the shown format." || correction.Reference == nil || correction.Reference.MessageID != "invalid-answer" || correction.AllowedMentions == nil || len(correction.AllowedMentions.Parse) != 0 {
		t.Fatalf("fallback correction = %#v", correction)
	}
}

func TestDiscordCancellationAcknowledgementIsExplicit(t *testing.T) {
	fake := newFakeChannelController()
	fake.acceptDisposition = interaction.AnswerCancelled
	runtime := testRuntime(fake)
	fake.pending = discordPending(runtime, discordConfirmRequest(), interaction.RenderNative)
	fake.hasPending = true
	var acknowledgement string
	runtime.respondNative = func(_ *discordgo.Interaction, response *discordgo.InteractionResponse) error {
		acknowledgement = response.Data.Content
		return nil
	}
	runtime.handleInteraction(nil, componentInteraction(customID(fake.pending.InteractionID, "x"), discordgo.ButtonComponent))
	if acknowledgement != "Request cancelled." || len(fake.attempts) != 1 || fake.attempts[0].Answer.Action != interaction.ActionCancel || slices.Contains(fake.order, "continue") {
		t.Fatalf("cancel acknowledgement=%q attempts=%#v order=%v", acknowledgement, fake.attempts, fake.order)
	}
}

func TestStatusMessageUsesOnlySafeLifecycleState(t *testing.T) {
	message := statusMessage("maintainer", "codex", "guild", dispatch.ConversationStatus{State: dispatch.LifecycleQueued, Pending: 2}, dispatch.CapacityStatus{Active: 1, ActiveLimit: 2, Resident: 2, ResidentLimit: 4, Queued: 3})
	if message != "hctl is online: agent=maintainer harness=codex surface=guild state=queued pending=2 active=1/2 resident=2/4 queued=3" {
		t.Fatalf("status = %q", message)
	}
	for _, forbidden := range []string{"discord-", "/Users/", "session-", "channel_id", "token"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("status exposed %q: %s", forbidden, message)
		}
	}
}

func TestDiscordStatusCommandRoutesThroughControllerAndStaysRedacted(t *testing.T) {
	fake := newFakeChannelController()
	fake.status = controller.Status{
		Conversation: dispatch.ConversationStatus{State: dispatch.LifecycleQueued, Pending: 2},
		Capacity:     dispatch.CapacityStatus{Active: 1, ActiveLimit: 2, Resident: 2, ResidentLimit: 4, Queued: 3},
	}
	runtime := testRuntime(fake)
	var response string
	runtime.interactionResponse = func(_ *discordgo.Interaction, content string) { response = content }
	runtime.handleInteraction(nil, commandInteraction("status"))

	wantConversation := conversationID("111", "555")
	if !reflect.DeepEqual(fake.statusCalls, []string{wantConversation}) {
		t.Fatalf("status calls = %#v", fake.statusCalls)
	}
	if response != "hctl is online: agent=maintainer harness=codex surface=guild state=queued pending=2 active=1/2 resident=2/4 queued=3" {
		t.Fatalf("status response = %q", response)
	}
	for _, forbidden := range []string{wantConversation, "111", "555", "/Users/", "token", "session-"} {
		if strings.Contains(response, forbidden) {
			t.Fatalf("status exposed %q: %s", forbidden, response)
		}
	}
}

func TestDiscordNewCommandRoutesThroughController(t *testing.T) {
	fake := newFakeChannelController()
	runtime := testRuntime(fake)
	var response string
	runtime.interactionResponse = func(_ *discordgo.Interaction, content string) { response = content }
	runtime.handleInteraction(nil, commandInteraction("new"))

	want := resetCall{surface: "555", conversation: conversationID("111", "555")}
	if !reflect.DeepEqual(fake.resets, []resetCall{want}) || response != "Started a new conversation." {
		t.Fatalf("resets = %#v response = %q", fake.resets, response)
	}
	fake.resetErr = dispatch.ErrConversationBusy
	runtime.handleInteraction(nil, commandInteraction("new"))
	if response != "The conversation is busy. Try again after current work finishes." {
		t.Fatalf("busy response = %q", response)
	}
}

func commandInteraction(name string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand, GuildID: "444", ChannelID: "555",
		Member: &discordgo.Member{User: &discordgo.User{ID: "333"}},
		Data:   discordgo.ApplicationCommandInteractionData{Name: name},
	}}
}

func testRuntime(channelController channelController) *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{
		project: &project.Project{Name: "maintainer"}, driver: codex.New("codex"),
		config: Config{Runtime: channelconfig.Profile{
			ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555",
		}},
		ctx: ctx, cancel: cancel, controller: channelController,
		deliver: func(string, *discordgo.MessageSend) error { return nil },
		targets: map[string]replyTarget{},
	}
}

type fakeChannelController struct {
	inputs            []controller.Inbound
	status            controller.Status
	statusCalls       []string
	resets            []resetCall
	resetErr          error
	done              chan struct{}
	pending           interaction.PendingInteraction
	hasPending        bool
	attempts          []interaction.AnswerAttempt
	order             []string
	acceptDisposition interaction.AnswerDisposition
	acceptErr         error
}

type resetCall struct {
	surface      string
	conversation string
}

func newFakeChannelController() *fakeChannelController {
	return &fakeChannelController{done: make(chan struct{})}
}

func (f *fakeChannelController) Submit(_ context.Context, input controller.Inbound) (dispatch.SubmissionResult, error) {
	f.inputs = append(f.inputs, input)
	return dispatch.SubmissionResult{Status: "queued"}, nil
}
func (f *fakeChannelController) Status(conversation string) controller.Status {
	f.statusCalls = append(f.statusCalls, conversation)
	return f.status
}
func (f *fakeChannelController) Reset(surface, conversation string) error {
	f.resets = append(f.resets, resetCall{surface: surface, conversation: conversation})
	return f.resetErr
}
func (f *fakeChannelController) PendingInteraction(string, string) (interaction.PendingInteraction, bool, error) {
	return f.pending, f.hasPending, nil
}
func (f *fakeChannelController) AcceptInteraction(_ string, _ string, attempt interaction.AnswerAttempt) (interaction.AnswerDisposition, error) {
	f.order = append(f.order, "accept")
	f.attempts = append(f.attempts, attempt)
	if f.acceptErr != nil {
		return "", f.acceptErr
	}
	if f.acceptDisposition != "" {
		return f.acceptDisposition, nil
	}
	return interaction.AnswerAccepted, nil
}
func (f *fakeChannelController) ContinueInteraction(string) error {
	f.order = append(f.order, "continue")
	return nil
}
func (f *fakeChannelController) RenderInteraction(string, string) (bool, error) {
	return f.hasPending, nil
}
func (f *fakeChannelController) Done() <-chan struct{} { return f.done }
func (f *fakeChannelController) Err() error            { return nil }
func (f *fakeChannelController) Close()                {}

func discordConfirmRequest() interaction.Request {
	return interaction.Request{SchemaVersion: 1, Kind: interaction.KindConfirm, Prompt: "Deploy?", FallbackText: "Please confirm.", Policy: interaction.Policy{ExpiresAfterSeconds: 3600, Cancellation: interaction.CancellationAllowed}, Field: &interaction.Field{ID: "deploy", Kind: interaction.KindConfirm, Label: "Deploy", Required: true}}
}

func discordChoiceRequest() interaction.Request {
	return interaction.Request{SchemaVersion: 1, Kind: interaction.KindChooseOne, Prompt: "Environment?", FallbackText: "Choose an environment.", Policy: interaction.Policy{ExpiresAfterSeconds: 3600, Cancellation: interaction.CancellationAllowed}, Field: &interaction.Field{ID: "environment", Kind: interaction.KindChooseOne, Label: "Environment", Required: true, Options: []interaction.Option{{ID: "staging", Label: "Staging", Value: "staging"}, {ID: "production", Label: "Production", Value: "production"}}, MinSelections: 1, MaxSelections: 1}}
}

func discordTextFormRequest() interaction.Request {
	return interaction.Request{SchemaVersion: 1, Kind: interaction.KindForm, Prompt: "Release details", FallbackText: "Provide release details.", Policy: interaction.Policy{ExpiresAfterSeconds: 3600, Cancellation: interaction.CancellationAllowed}, Fields: []interaction.Field{{ID: "title", Kind: interaction.KindText, Label: "Title", Required: true, MinLength: 1, MaxLength: 100}, {ID: "details", Kind: interaction.KindText, Label: "Details", Required: true, MinLength: 1, MaxLength: 4000}}}
}

func discordRenderIntent(runtime *Runtime, request interaction.Request, mode interaction.RenderMode) interaction.RenderIntent {
	return interaction.RenderIntent{InteractionID: "interaction_0123456789abcdef0123456789abcdef", InputID: "message-1", Owner: runtime.Owner("555"), Request: request, Resolution: interaction.Resolution{Mode: mode}}
}

func discordPending(runtime *Runtime, request interaction.Request, mode interaction.RenderMode) interaction.PendingInteraction {
	return interaction.PendingInteraction{InteractionID: "interaction_0123456789abcdef0123456789abcdef", InputID: "message-1", Owner: runtime.Owner("555"), Request: request, Resolution: interaction.Resolution{Mode: mode}}
}

func componentInteraction(id string, componentType discordgo.ComponentType) *discordgo.InteractionCreate {
	return callbackInteraction(discordgo.InteractionMessageComponent, discordgo.MessageComponentInteractionData{CustomID: id, ComponentType: componentType})
}

func callbackInteraction(kind discordgo.InteractionType, data discordgo.InteractionData) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "callback", AppID: "111", Type: kind, GuildID: "444", ChannelID: "555",
		Member: &discordgo.Member{User: &discordgo.User{ID: "333"}}, Data: data,
	}}
}

type recoveryContinuationDriver struct{}

func (*recoveryContinuationDriver) Name() string                 { return "codex" }
func (*recoveryContinuationDriver) Executable() string           { return "/fake/codex" }
func (*recoveryContinuationDriver) Verify(context.Context) error { return nil }
func (*recoveryContinuationDriver) Open(context.Context, harness.OpenRequest) (harness.Session, error) {
	return &recoverySession{}, nil
}
func (*recoveryContinuationDriver) ContinueTurn(_ context.Context, _ harness.OpenRequest, _ string, _ interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	emit(harness.Event{Type: "turn.started", TurnID: "new-turn"})
	emit(harness.Event{Type: "agent.output.delta", TurnID: "new-turn", ItemID: "answer", Delta: "recovered answer"})
	return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed", ResultSessionID: "thread-1", ResultTurnID: "new-turn"}
}

type recoverySession struct{}

func (*recoverySession) InitialEvents() []harness.Event { return nil }
func (*recoverySession) RunTurn(context.Context, harness.Input, func(harness.Event)) (harness.TurnResult, error) {
	return harness.TurnResult{}, errors.New("unexpected ordinary turn")
}
func (*recoverySession) Close() error { return nil }
func (*recoverySession) Abort()       {}
