package discordadapter

import (
	"context"
	"errors"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Discord is the responsibility-specific transport seam used by setup and the
// runtime. Tests supply a deterministic fake; production wraps discordgo.
type Discord interface {
	Open() error
	Close() error
	AddHandler(any) func()
	User(string, ...discordgo.RequestOption) (*discordgo.User, error)
	Application(string) (*discordgo.Application, error)
	Channel(string, ...discordgo.RequestOption) (*discordgo.Channel, error)
	GuildMember(string, string, ...discordgo.RequestOption) (*discordgo.Member, error)
	ApplicationCommandBulkOverwrite(string, string, []*discordgo.ApplicationCommand, ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error)
	ChannelMessageSendComplex(string, *discordgo.MessageSend, ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEditComplex(*discordgo.MessageEdit, ...discordgo.RequestOption) (*discordgo.Message, error)
	MessageReactionAdd(string, string, string, ...discordgo.RequestOption) error
	ChannelTyping(string, ...discordgo.RequestOption) error
	InteractionRespond(*discordgo.Interaction, *discordgo.InteractionResponse, ...discordgo.RequestOption) error
}

type DiscordFactory func(string) (Discord, error)

func NewDiscord(token string) (Discord, error) {
	if err := validateTokenShape(token); err != nil {
		return nil, err
	}
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, errors.New("cannot initialize Discord client")
	}
	session.Client.Timeout = 10 * time.Second
	// Serial dispatch lets the bounded protocol writer apply Gateway
	// backpressure without accumulating one blocked goroutine per event.
	session.SyncEvents = true
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	return session, nil
}

type Identity struct {
	ApplicationID string
	BotUserID     string
	BotName       string
}

func validateIdentity(ctx context.Context, client Discord) (Identity, error) {
	type result struct {
		identity Identity
		err      error
	}
	results := make(chan result, 1)
	go func() {
		user, err := client.User("@me", discordgo.WithContext(ctx))
		if err != nil || user == nil || !user.Bot || !snowflake(user.ID) {
			results <- result{err: errors.New("Discord credential did not identify a bot user")}
			return
		}
		application, err := client.Application("@me")
		if err != nil || application == nil || !snowflake(application.ID) {
			results <- result{err: errors.New("Discord credential did not identify an application")}
			return
		}
		results <- result{identity: Identity{ApplicationID: application.ID, BotUserID: user.ID, BotName: user.Username}}
	}()
	select {
	case <-ctx.Done():
		return Identity{}, errors.New("Discord identity validation timed out")
	case result := <-results:
		return result.identity, result.err
	}
}

func validateScope(ctx context.Context, client Discord, profile Profile) error {
	channel, err := client.Channel(profile.AllowedChannelID, discordgo.WithContext(ctx))
	if err != nil || channel == nil {
		return errors.New("bot cannot access the configured Discord channel; install it and check the channel id")
	}
	if channel.GuildID != profile.AllowedGuildID {
		return errors.New("configured Discord channel does not belong to the configured guild")
	}
	member, err := client.GuildMember(profile.AllowedGuildID, profile.AllowedUserID, discordgo.WithContext(ctx))
	if err != nil || member == nil || member.User == nil {
		return errors.New("authorized user is not visible in the configured guild")
	}
	if member.User.Bot {
		return errors.New("authorized user must be a person")
	}
	return nil
}

func validateIdentityMatches(identity Identity, profile Profile) error {
	if identity.ApplicationID != profile.ApplicationID || identity.BotUserID != profile.BotUserID {
		return errors.New("Discord credential does not match the configured application and bot identity")
	}
	return nil
}
