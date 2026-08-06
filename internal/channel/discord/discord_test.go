package discord

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"

	"hctl/internal/channelconfig"
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
		if !terminalDispatchEvent(eventType) {
			t.Fatalf("terminal event %q rejected", eventType)
		}
	}
	for _, eventType := range []string{"input.accepted", "turn.queued", "turn.started", "agent.output.delta"} {
		if terminalDispatchEvent(eventType) {
			t.Fatalf("nonterminal event %q accepted", eventType)
		}
	}
}
