package discord

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"hctl/internal/channel/controller"
	"hctl/internal/channelconfig"
	"hctl/internal/dispatch"
	"hctl/internal/harness/codex"
	"hctl/internal/project"
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
	}
}

type fakeChannelController struct {
	inputs      []controller.Inbound
	status      controller.Status
	statusCalls []string
	resets      []resetCall
	resetErr    error
	done        chan struct{}
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
func (f *fakeChannelController) Done() <-chan struct{} { return f.done }
func (f *fakeChannelController) Err() error            { return nil }
func (f *fakeChannelController) Close()                {}
