package discord

import (
	"context"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"hctl/internal/channelconfig"
	"hctl/internal/dispatch"
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
	dm := &discordgo.MessageCreate{Message: &discordgo.Message{GuildID: ""}}
	if !directMessage("222", dm) {
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

func TestAppendAndChunkBounded(t *testing.T) {
	turn := &pendingTurn{}
	appendBounded(turn, "message-1", strings.Repeat("a", maxOutputRunes+10))
	if !turn.truncated || turn.runes != maxOutputRunes {
		t.Fatalf("turn = %+v", turn)
	}
	chunks := responseMessages(outputParts(turn), turn.truncated)
	if len(chunks) != maxChunks {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 2000 {
			t.Fatal("oversized Discord chunk")
		}
	}
}

func TestAssistantMessageItemsRemainSeparate(t *testing.T) {
	turn := &pendingTurn{}
	appendBounded(turn, "message-1", "I'll check that.")
	appendBounded(turn, "message-2", "Yes, origin is configured.")
	appendBounded(turn, "message-2", " It uses GitHub.")
	messages := responseMessages(outputParts(turn), turn.truncated)
	if len(messages) != 2 || messages[0] != "I'll check that." || messages[1] != "Yes, origin is configured. It uses GitHub." {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestNoReplyIsExact(t *testing.T) {
	if strings.TrimSpace("  "+NoReply+"\n") != NoReply {
		t.Fatal("no reply control result changed")
	}
	if strings.TrimSpace(NoReply+" explanation") == NoReply {
		t.Fatal("non-exact output was suppressed")
	}
}

func TestTypingWaitsUntilVisibleReplyIsDecided(t *testing.T) {
	for _, output := range []string{"", "  ", "H", "HCTL_NO_", "HCTL_NO_REPLY", " \nHCTL_NO_REPLY"} {
		if visibleReplyDecided(output) {
			t.Fatalf("typing started for possible no-reply output %q", output)
		}
	}
	for _, output := range []string{"Hi", "Sure", "HCTL_NO_REPLY because"} {
		if !visibleReplyDecided(output) {
			t.Fatalf("typing did not start for visible output %q", output)
		}
	}
}

func TestOnlyTerminalDispatchEventsCompleteDiscordTurn(t *testing.T) {
	for _, eventType := range []string{"turn.completed", "turn.failed", "turn.cancelled", "turn.uncertain", "driver.process_failed"} {
		if !(dispatch.Event{Type: eventType}).Terminal() {
			t.Fatalf("terminal event %q rejected", eventType)
		}
	}
	for _, eventType := range []string{"input.accepted", "turn.queued", "turn.started", "agent.output.delta"} {
		if (dispatch.Event{Type: eventType}).Terminal() {
			t.Fatalf("nonterminal event %q accepted", eventType)
		}
	}
}

func TestDiscordSurfaceSubmitsThroughManagedConversation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := newFakeConversationManager()
	runtime := &Runtime{
		config: Config{Runtime: channelconfig.Profile{
			ApplicationID: "111", BotUserID: "222", AllowedUserID: "333",
			AllowedGuildID: "444", AllowedChannelID: "555",
		}},
		ctx: ctx, cancel: cancel, manager: manager,
		surfaces: map[string]*surface{}, byConversation: map[string]*surface{},
	}
	incoming := &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "message-1", Content: "please check", ChannelID: "555", GuildID: "444",
		Author: &discordgo.User{ID: "333"},
	}}

	runtime.handleMessage(nil, incoming)

	if len(manager.submitted) != 1 {
		t.Fatalf("managed submissions = %#v", manager.submitted)
	}
	wantConversation := conversationID("111", "555")
	if manager.submitted[0].conversation != wantConversation || manager.submitted[0].submission.InputID != "message-1" {
		t.Fatalf("managed submission = %#v", manager.submitted[0])
	}
	if current := runtime.surfaces["555"]; current == nil || current.conversation != wantConversation || current.turns["message-1"] == nil {
		t.Fatalf("Discord surface = %#v", current)
	}
}

func TestStatusMessageUsesOnlySafeLifecycleState(t *testing.T) {
	message := statusMessage("maintainer", "codex", "guild", dispatch.ConversationStatus{State: dispatch.LifecycleQueued, Pending: 2})
	if message != "hctl is online: agent=maintainer harness=codex surface=guild state=queued pending=2" {
		t.Fatalf("status = %q", message)
	}
	for _, forbidden := range []string{"discord-", "/Users/", "session-", "channel_id", "token"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("status exposed %q: %s", forbidden, message)
		}
	}
}

func TestStatusMessageReportsHibernatedWithoutRuntimeIdentity(t *testing.T) {
	message := statusMessage("maintainer", "claude", "dm", dispatch.ConversationStatus{State: dispatch.LifecycleHibernated})
	if message != "hctl is online: agent=maintainer harness=claude surface=dm state=hibernated pending=0" {
		t.Fatalf("status = %q", message)
	}
}

type fakeManagedSubmission struct {
	conversation string
	submission   dispatch.Submission
}

type fakeConversationManager struct {
	submitted []fakeManagedSubmission
	statuses  map[string]dispatch.ConversationStatus
	done      chan struct{}
}

func newFakeConversationManager() *fakeConversationManager {
	return &fakeConversationManager{statuses: map[string]dispatch.ConversationStatus{}, done: make(chan struct{})}
}

func (m *fakeConversationManager) Submit(_ context.Context, conversation string, submission dispatch.Submission) (dispatch.SubmissionResult, error) {
	m.submitted = append(m.submitted, fakeManagedSubmission{conversation: conversation, submission: submission})
	return dispatch.SubmissionResult{Status: "queued"}, nil
}

func (m *fakeConversationManager) Status(conversation string) dispatch.ConversationStatus {
	return m.statuses[conversation]
}

func (m *fakeConversationManager) Reset(string) error    { return nil }
func (m *fakeConversationManager) Done() <-chan struct{} { return m.done }
func (m *fakeConversationManager) Err() error            { return nil }
func (m *fakeConversationManager) Close()                {}
