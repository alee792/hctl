package discord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/flock"

	"hctl/internal/channel/controller"
	"hctl/internal/channelconfig"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/interaction"
	"hctl/internal/project"
)

const (
	maxChunks        = 6
	defaultTurnLimit = 2 * time.Minute
	defaultIdleLimit = dispatch.DefaultIdleTimeout
)

type Identity struct {
	ApplicationID string
	BotUserID     string
	BotName       string
}

type Config struct {
	Profile     string
	Runtime     channelconfig.Profile
	Token       string
	TurnTimeout time.Duration
	IdleTimeout time.Duration
	MaxResident int
	MaxActive   int
	Audit       io.Writer
	Executable  string
}

type channelController interface {
	Submit(context.Context, controller.Inbound) (dispatch.SubmissionResult, error)
	Status(string) controller.Status
	Reset(string, string) error
	PendingInteraction(string, string) (interaction.PendingInteraction, bool, error)
	AcceptInteraction(string, string, interaction.AnswerAttempt) (interaction.AnswerDisposition, error)
	ContinueInteraction(string) error
	RenderInteraction(string, string) (bool, error)
	Done() <-chan struct{}
	Err() error
	Close()
}

type replyTarget struct {
	channelID string
	messageID string
}

type Runtime struct {
	project             *project.Project
	driver              harness.Driver
	config              Config
	session             *discordgo.Session
	ctx                 context.Context
	cancel              context.CancelFunc
	controller          channelController
	deliver             func(string, *discordgo.MessageSend) error
	typing              func(string) error
	interactionResponse func(*discordgo.Interaction, string)
	respondNative       func(*discordgo.Interaction, *discordgo.InteractionResponse) error
	validateIdentity    func(context.Context, string) (Identity, error)

	mu          sync.Mutex
	closed      bool
	lock        *flock.Flock
	targets     map[string]replyTarget
	targetOrder []string
}

func ValidateIdentity(ctx context.Context, token string) (Identity, error) {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return Identity{}, errors.New("discord bot token is empty or malformed")
	}
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return Identity{}, errors.New("discord bot token is malformed")
	}
	s.Client.Timeout = 10 * time.Second
	type result struct {
		identity Identity
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		user, userErr := s.User("@me", discordgo.WithContext(ctx))
		if userErr != nil || user == nil || !user.Bot {
			resultCh <- result{err: errors.New("discord token did not identify a bot user")}
			return
		}
		application, appErr := s.Application("@me")
		if appErr != nil || application == nil || !channelconfig.Snowflake(application.ID) {
			resultCh <- result{err: errors.New("discord token did not identify an application")}
			return
		}
		resultCh <- result{identity: Identity{ApplicationID: application.ID, BotUserID: user.ID, BotName: user.Username}}
	}()
	select {
	case <-ctx.Done():
		return Identity{}, errors.New("discord identity validation timed out")
	case result := <-resultCh:
		return result.identity, result.err
	}
}

func ValidateProfile(identity Identity, profile channelconfig.Profile) error {
	if identity.ApplicationID != profile.ApplicationID || identity.BotUserID != profile.BotUserID {
		return errors.New("discord token does not match the configured application and bot identity")
	}
	return nil
}

func ValidateScope(ctx context.Context, token string, profile channelconfig.Profile) error {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return errors.New("cannot validate Discord authorization scope")
	}
	s.Client.Timeout = 10 * time.Second
	channel, err := s.Channel(profile.AllowedChannelID, discordgo.WithContext(ctx))
	if err != nil || channel == nil {
		return errors.New("bot cannot access the configured Discord channel; install it in the target server and check the channel ID")
	}
	if channel.GuildID != profile.AllowedGuildID {
		return errors.New("configured Discord channel does not belong to the configured guild")
	}
	member, err := s.GuildMember(profile.AllowedGuildID, profile.AllowedUserID, discordgo.WithContext(ctx))
	if err != nil || member == nil || member.User == nil {
		return errors.New("authorized user is not visible in the configured guild; enter your personal Discord user ID, not the bot or application ID")
	}
	if member.User.Bot {
		return errors.New("authorized user must be a person; enter your personal Discord user ID, not the bot ID")
	}
	return nil
}

func New(p *project.Project, driver harness.Driver, config Config) (*Runtime, error) {
	if p == nil || p.DiscordChannel == nil {
		return nil, errors.New("agent project does not define channels/discord.md")
	}
	if driver == nil {
		return nil, errors.New("discord requires a harness driver")
	}
	if config.TurnTimeout == 0 {
		config.TurnTimeout = defaultTurnLimit
	}
	if config.TurnTimeout <= 0 || config.TurnTimeout > 30*time.Minute {
		return nil, errors.New("discord turn timeout must be greater than zero and at most 30m")
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = defaultIdleLimit
	}
	if config.IdleTimeout <= 0 || config.IdleTimeout > 24*time.Hour {
		return nil, errors.New("discord idle timeout must be greater than zero and at most 24h")
	}
	if config.MaxResident == 0 {
		config.MaxResident = dispatch.DefaultMaxResidentSessions
	}
	if config.MaxActive == 0 {
		config.MaxActive = dispatch.DefaultMaxActiveTurns
	}
	if config.MaxResident <= 0 || config.MaxActive <= 0 || config.MaxActive > config.MaxResident || config.MaxResident > 64 {
		return nil, errors.New("discord session capacity limits are invalid")
	}
	if config.Audit == nil {
		config.Audit = io.Discard
	}
	s, err := discordgo.New("Bot " + config.Token)
	if err != nil {
		return nil, errors.New("cannot initialize Discord Gateway client")
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &Runtime{
		project: p, driver: driver, config: config, session: s, ctx: ctx, cancel: cancel,
		targets: map[string]replyTarget{}, validateIdentity: ValidateIdentity,
	}
	runtime.deliver = func(channelID string, message *discordgo.MessageSend) error {
		_, err := s.ChannelMessageSendComplex(channelID, message)
		return err
	}
	runtime.typing = func(channelID string) error { return s.ChannelTyping(channelID) }
	runtime.respondNative = func(interaction *discordgo.Interaction, response *discordgo.InteractionResponse) error {
		return s.InteractionRespond(interaction, response)
	}
	s.AddHandler(runtime.handleMessage)
	s.AddHandler(runtime.handleInteraction)
	return runtime, nil
}

// startController crosses the trusted runtime boundary only after Run has
// validated the token identity and acquired the per-application lock.
func (r *Runtime) startController() error {
	if r.controller != nil {
		return nil
	}
	channelController, err := controller.New(r.ctx, controller.Config{
		Project: r.project, Driver: r.driver, TurnTimeout: r.config.TurnTimeout, IdleTimeout: r.config.IdleTimeout,
		MaxResident: r.config.MaxResident, MaxActive: r.config.MaxActive, Executable: r.config.Executable,
		Audit: r.config.Audit, AuditPrefix: "Discord",
		Interactions:    r,
		InitialSurfaces: []controller.InitialSurface{{SurfaceID: r.config.Runtime.AllowedChannelID, ConversationID: conversationID(r.config.Runtime.ApplicationID, r.config.Runtime.AllowedChannelID)}},
	}, r)
	if err != nil {
		return err
	}
	r.controller = channelController
	return nil
}

func (r *Runtime) Run(ctx context.Context) error {
	defer r.Close()
	identity, err := r.validateIdentity(ctx, r.config.Token)
	if err != nil {
		return err
	}
	if err := ValidateProfile(identity, r.config.Runtime); err != nil {
		return err
	}
	lock, err := applicationLock(identity.ApplicationID)
	if err != nil {
		return err
	}
	locked, err := lock.TryLock()
	if err != nil || !locked {
		return errors.New("this Discord application is already active in another hctl run")
	}
	r.lock = lock
	defer func() { _ = lock.Unlock() }()
	if err := r.startController(); err != nil {
		return err
	}
	if err := r.registerCommands(identity.ApplicationID); err != nil {
		return err
	}
	if err := r.session.Open(); err != nil {
		return errors.New("cannot connect to the Discord Gateway")
	}
	r.recoverSurface(r.config.Runtime.AllowedChannelID)
	_, _ = fmt.Fprintf(r.config.Audit, "Discord connected profile=%s agent=%s\n", r.config.Profile, r.project.Name)
	select {
	case <-ctx.Done():
		return nil
	case <-r.ctx.Done():
		return nil
	case <-r.controller.Done():
		return r.controller.Err()
	}
}

func (r *Runtime) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	_ = r.session.Close()
	if r.controller != nil {
		r.controller.Close()
	}
}

func (r *Runtime) handleMessage(_ *discordgo.Session, incoming *discordgo.MessageCreate) {
	profile := r.config.Runtime
	if !eligibleMessage(profile, incoming) {
		return
	}
	if r.handleFallbackReply(incoming) {
		return
	}
	conversation := conversationID(r.config.Runtime.ApplicationID, incoming.ChannelID)
	if pending, ok, _ := r.controller.PendingInteraction(incoming.ChannelID, conversation); ok {
		r.rememberTarget(pending.InputID, replyTarget{channelID: incoming.ChannelID, messageID: pending.InputID})
		_ = r.Deliver(controller.Outcome{
			InputID: incoming.ID, Target: replyTarget{channelID: incoming.ChannelID, messageID: incoming.ID}, Failure: controller.FailureAdmission,
		})
		return
	}
	surfaceKind := "guild"
	if incoming.GuildID == "" {
		surfaceKind = "dm"
	}
	text, err := json.Marshal(map[string]any{
		"platform": "discord", "surface": surfaceKind, "direct": directMessage(r.config.Runtime.BotUserID, incoming),
		"guild_id": incoming.GuildID, "channel_id": incoming.ChannelID, "message_id": incoming.ID,
		"author_id": incoming.Author.ID, "content": incoming.Content,
	})
	if err != nil {
		return
	}
	target := replyTarget{channelID: incoming.ChannelID, messageID: incoming.ID}
	r.rememberTarget(incoming.ID, target)
	_, err = r.controller.Submit(r.ctx, controller.Inbound{
		SurfaceID: incoming.ChannelID, ConversationID: conversation,
		InputID: incoming.ID, Text: "Discord message (JSON):\n" + string(text),
		Target: target,
	})
	if err != nil {
		r.forgetTarget(incoming.ID)
		return
	}
}

func eligibleMessage(profile channelconfig.Profile, incoming *discordgo.MessageCreate) bool {
	if incoming == nil || incoming.Author == nil || incoming.Author.Bot || incoming.WebhookID != "" || strings.TrimSpace(incoming.Content) == "" || incoming.Author.ID != profile.AllowedUserID {
		return false
	}
	return incoming.GuildID == "" || incoming.GuildID == profile.AllowedGuildID && incoming.ChannelID == profile.AllowedChannelID
}

func directMessage(botUserID string, incoming *discordgo.MessageCreate) bool {
	if incoming == nil || incoming.Message == nil {
		return false
	}
	if incoming.GuildID == "" {
		return true
	}
	for _, mention := range incoming.Mentions {
		if mention != nil && mention.ID == botUserID {
			return true
		}
	}
	return incoming.ReferencedMessage != nil && incoming.ReferencedMessage.Author != nil && incoming.ReferencedMessage.Author.ID == botUserID
}

func (r *Runtime) handleInteraction(_ *discordgo.Session, incoming *discordgo.InteractionCreate) {
	if incoming == nil {
		return
	}
	if incoming.Type == discordgo.InteractionMessageComponent || incoming.Type == discordgo.InteractionModalSubmit {
		r.handleComponent(incoming)
		return
	}
	if incoming.Type != discordgo.InteractionApplicationCommand {
		return
	}
	if incoming.AppID != "" && incoming.AppID != r.config.Runtime.ApplicationID {
		return
	}
	userID := ""
	if incoming.Member != nil && incoming.Member.User != nil {
		userID = incoming.Member.User.ID
	} else if incoming.User != nil {
		userID = incoming.User.ID
	}
	if userID != r.config.Runtime.AllowedUserID {
		r.respond(incoming.Interaction, "Not authorized.")
		return
	}
	if incoming.GuildID != "" && (incoming.GuildID != r.config.Runtime.AllowedGuildID || incoming.ChannelID != r.config.Runtime.AllowedChannelID) {
		r.respond(incoming.Interaction, "This channel is not configured for hctl.")
		return
	}
	data := incoming.ApplicationCommandData()
	switch data.Name {
	case "status":
		surfaceKind := "guild"
		if incoming.GuildID == "" {
			surfaceKind = "dm"
		}
		status := r.controller.Status(conversationID(r.config.Runtime.ApplicationID, incoming.ChannelID))
		r.respond(incoming.Interaction, statusMessage(r.project.Name, r.driver.Name(), surfaceKind, status.Conversation, status.Capacity))
	case "new":
		if err := r.controller.Reset(incoming.ChannelID, conversationID(r.config.Runtime.ApplicationID, incoming.ChannelID)); err != nil {
			r.respond(incoming.Interaction, "The conversation is busy. Try again after current work finishes.")
			return
		}
		r.respond(incoming.Interaction, "Started a new conversation.")
	}
}

func (r *Runtime) Typing(target any) error {
	reply, ok := target.(replyTarget)
	if !ok || r.typing == nil {
		return nil
	}
	return r.typing(reply.channelID)
}

func (r *Runtime) Deliver(outcome controller.Outcome) error {
	r.forgetTarget(outcome.InputID)
	reply, ok := outcome.Target.(replyTarget)
	if !ok {
		return errors.New("discord reply target is invalid")
	}
	parts := outcome.Parts
	if outcome.Failure != controller.FailureNone {
		parts = []string{discordFailureMessage(outcome.Failure)}
	}
	chunks := responseMessages(parts, outcome.Truncated)
	for index, chunk := range chunks {
		message := &discordgo.MessageSend{Content: chunk, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}}
		if index == 0 {
			failIfMissing := false
			message.Reference = &discordgo.MessageReference{MessageID: reply.messageID, ChannelID: reply.channelID, FailIfNotExists: &failIfMissing}
		}
		if err := r.deliver(reply.channelID, message); err != nil {
			return err
		}
	}
	return nil
}

func discordFailureMessage(failure controller.Failure) string {
	switch failure {
	case controller.FailureAdmission:
		return "I couldn't handle that request in this conversation. Please try again."
	case controller.FailureProcess:
		return "I hit an error while handling that. Please try again."
	case controller.FailureCancelled:
		return "That request was cancelled."
	case controller.FailureUncertain:
		return "I lost track of that response during recovery. Please try again."
	case controller.FailureWriteAccess:
		return "I couldn't continue that request with write access."
	case controller.FailureWorkspace:
		return "I couldn't safely create a writable workspace for that request."
	default:
		return "I couldn't produce a response. Please try again."
	}
}

func statusMessage(agent, harnessName, surfaceKind string, status dispatch.ConversationStatus, capacity dispatch.CapacityStatus) string {
	return fmt.Sprintf("hctl is online: agent=%s harness=%s surface=%s state=%s pending=%d active=%d/%d resident=%d/%d queued=%d", agent, harnessName, surfaceKind, status.State, status.Pending, capacity.Active, capacity.ActiveLimit, capacity.Resident, capacity.ResidentLimit, capacity.Queued)
}

func (r *Runtime) registerCommands(applicationID string) error {
	commands := []*discordgo.ApplicationCommand{{Name: "new", Description: "Start a fresh hctl conversation"}, {Name: "status", Description: "Show hctl runtime status"}}
	if _, err := r.session.ApplicationCommandBulkOverwrite(applicationID, "", commands); err != nil {
		return errors.New("cannot reconcile Discord slash commands")
	}
	return nil
}

func (r *Runtime) respond(interaction *discordgo.Interaction, content string) {
	if r.interactionResponse != nil {
		r.interactionResponse(interaction, content)
		return
	}
	_ = r.session.InteractionRespond(interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Content: content, Flags: discordgo.MessageFlagsEphemeral, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}}})
}

func applicationLock(applicationID string) (*flock.Flock, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, errors.New("cannot resolve runtime lock directory")
	}
	directory := filepath.Join(cache, "hctl", "locks")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("cannot create runtime lock directory")
	}
	return flock.New(filepath.Join(directory, "discord-"+applicationID+".lock")), nil
}

func conversationID(applicationID, channelID string) string {
	digest := sha256.Sum256([]byte(applicationID + ":" + channelID))
	return "discord-" + hex.EncodeToString(digest[:12])
}

func responseMessages(parts []string, truncated bool) []string {
	messages := make([]string, 0, maxChunks)
	for partIndex, part := range parts {
		chunks := responseChunks(part, false)
		for _, chunk := range chunks {
			if len(messages) == maxChunks {
				truncated = true
				break
			}
			messages = append(messages, chunk)
		}
		if len(messages) == maxChunks {
			if partIndex != len(parts)-1 {
				truncated = true
			}
			break
		}
	}
	if truncated && len(messages) > 0 {
		const marker = "\n\n[output truncated]"
		last := []rune(messages[len(messages)-1])
		limit := 2000 - len([]rune(marker))
		if len(last) > limit {
			last = last[:limit]
		}
		messages[len(messages)-1] = string(last) + marker
	}
	return messages
}

func responseChunks(content string, truncated bool) []string {
	if truncated {
		content += "\n\n[output truncated]"
	}
	runes := []rune(content)
	chunks := make([]string, 0, maxChunks)
	for len(runes) > 0 && len(chunks) < maxChunks {
		count := min(len(runes), 2000)
		chunks = append(chunks, string(runes[:count]))
		runes = runes[count:]
	}
	return chunks
}
