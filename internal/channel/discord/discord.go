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
	"unicode"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/flock"

	"hctl/internal/channelconfig"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/worktree"
)

const (
	NoReply            = channelconfig.NoReplyResult
	RequestWriteAccess = channelconfig.RequestWriteAccessResult
	maxOutputRunes     = 6*2000 - 64
	maxChunks          = 6
	defaultTurnLimit   = 2 * time.Minute
	defaultIdleLimit   = dispatch.DefaultIdleTimeout
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
	Audit       io.Writer
	Executable  string
}

type pendingTurn struct {
	channelID string
	messageID string
	outputs   []*bufferedOutput
	byItem    map[string]*bufferedOutput
	runes     int
	truncated bool
}

type bufferedOutput struct {
	itemID string
	text   strings.Builder
}

type surface struct {
	id           string
	conversation string
	turns        map[string]*pendingTurn
}

type conversationManager interface {
	Submit(context.Context, string, dispatch.Submission) (dispatch.SubmissionResult, error)
	Elevate(context.Context, string, dispatch.Submission) (dispatch.SubmissionResult, error)
	Status(string) dispatch.ConversationStatus
	Reset(string) error
	Done() <-chan struct{}
	Err() error
	Close()
}

type Runtime struct {
	project *project.Project
	driver  harness.Driver
	config  Config
	session *discordgo.Session
	ctx     context.Context
	cancel  context.CancelFunc
	manager conversationManager
	deliver func(string, *discordgo.MessageSend) error

	mu             sync.Mutex
	surfaces       map[string]*surface
	byConversation map[string]*surface
	closed         bool
	lock           *flock.Flock
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
		surfaces: map[string]*surface{}, byConversation: map[string]*surface{},
	}
	runtime.deliver = func(channelID string, message *discordgo.MessageSend) error {
		_, err := s.ChannelMessageSendComplex(channelID, message)
		return err
	}
	emit := func(conversation string, event dispatch.Event) error {
		runtime.handleDispatch(conversation, event)
		return nil
	}
	workspaceManager, _ := worktree.New(ctx, p, config.Executable)
	var manager *dispatch.Manager
	if workspaceManager != nil {
		manager, err = dispatch.NewManagerWithWorkspace(ctx, p, driver, config.TurnTimeout, config.IdleTimeout, emit, workspaceManager)
	} else {
		manager, err = dispatch.NewManagerWithIdleTimeout(ctx, p, driver, config.TurnTimeout, config.IdleTimeout, emit)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	runtime.manager = manager
	s.AddHandler(runtime.handleMessage)
	s.AddHandler(runtime.handleInteraction)
	return runtime, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	identity, err := ValidateIdentity(ctx, r.config.Token)
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
	if err := r.registerCommands(identity.ApplicationID); err != nil {
		return err
	}
	if err := r.session.Open(); err != nil {
		return errors.New("cannot connect to the Discord Gateway")
	}
	defer r.Close()
	_, _ = fmt.Fprintf(r.config.Audit, "Discord connected profile=%s agent=%s\n", r.config.Profile, r.project.Name)
	select {
	case <-ctx.Done():
		return nil
	case <-r.ctx.Done():
		return nil
	case <-r.manager.Done():
		return r.manager.Err()
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
	r.manager.Close()
}

func (r *Runtime) handleMessage(_ *discordgo.Session, incoming *discordgo.MessageCreate) {
	profile := r.config.Runtime
	if !eligibleMessage(profile, incoming) {
		return
	}
	surfaceID := incoming.ChannelID
	current, err := r.surface(surfaceID)
	if err != nil {
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
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	current.turns[incoming.ID] = &pendingTurn{channelID: incoming.ChannelID, messageID: incoming.ID}
	r.mu.Unlock()
	result, err := r.manager.Submit(r.ctx, current.conversation, dispatch.Submission{InputID: incoming.ID, Text: "Discord message (JSON):\n" + string(text)})
	if err != nil {
		r.drop(current, incoming.ID)
		if !errors.Is(err, context.Canceled) && !errors.Is(err, dispatch.ErrManagerClosed) {
			r.cancel()
		}
		return
	}
	if result.Status != "queued" && result.Status != "active" && result.Status != "completed" {
		r.drop(current, incoming.ID)
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
	if incoming == nil || incoming.Type != discordgo.InteractionApplicationCommand {
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
		status := r.manager.Status(conversationID(r.config.Runtime.ApplicationID, incoming.ChannelID))
		r.respond(incoming.Interaction, statusMessage(r.project.Name, r.driver.Name(), surfaceKind, status))
	case "new":
		if err := r.resetSurface(incoming.ChannelID); err != nil {
			r.respond(incoming.Interaction, "The conversation is busy. Try again after current work finishes.")
			return
		}
		r.respond(incoming.Interaction, "Started a new conversation.")
	}
}

func (r *Runtime) surface(id string) (*surface, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.surfaces[id]; current != nil {
		return current, nil
	}
	conversation := conversationID(r.config.Runtime.ApplicationID, id)
	current := &surface{id: id, conversation: conversation, turns: map[string]*pendingTurn{}}
	r.surfaces[id] = current
	r.byConversation[conversation] = current
	return current, nil
}

func (r *Runtime) handleDispatch(conversation string, event dispatch.Event) {
	r.mu.Lock()
	current := r.byConversation[conversation]
	if current == nil {
		r.mu.Unlock()
		return
	}
	turn := current.turns[event.InputID]
	if turn == nil {
		r.mu.Unlock()
		return
	}
	if event.Type == "agent.output.delta" {
		appendBounded(turn, event.ItemID, event.Delta)
		showTyping := visibleReplyDecided(combinedOutput(turn))
		r.mu.Unlock()
		if showTyping {
			_ = r.session.ChannelTyping(turn.channelID)
		}
		return
	}
	if !event.Terminal() {
		r.mu.Unlock()
		return
	}
	content := strings.TrimSpace(combinedOutput(turn))
	parts := outputParts(turn)
	truncated := turn.truncated
	if event.Type == "turn.completed" && suppressedControl(content) == RequestWriteAccess && !strings.HasSuffix(event.InputID, ":write") {
		delete(current.turns, event.InputID)
		continuationID := event.InputID + ":write"
		current.turns[continuationID] = &pendingTurn{channelID: turn.channelID, messageID: turn.messageID}
		r.mu.Unlock()
		_, _ = fmt.Fprintf(r.config.Audit, "Discord turn suppressed input_id=%s class=write_access_requested\n", event.InputID)
		go r.continueWritable(current, continuationID)
		return
	}
	delete(current.turns, event.InputID)
	r.mu.Unlock()
	if suppressedControl(content) == NoReply {
		_, _ = fmt.Fprintf(r.config.Audit, "Discord turn suppressed input_id=%s class=no_reply\n", event.InputID)
		return
	}
	if suppressedControl(content) == RequestWriteAccess {
		content = "I couldn't continue that request with write access."
		parts = []string{content}
	}
	if content == "" {
		_, _ = fmt.Fprintf(r.config.Audit, "Discord turn empty input_id=%s class=%s\n", event.InputID, event.Type)
		content = discordTerminalMessage(event.Type)
		parts = []string{content}
	}
	r.sendTurn(event.InputID, turn, parts, truncated)
}

func (r *Runtime) sendTurn(inputID string, turn *pendingTurn, parts []string, truncated bool) {
	chunks := responseMessages(parts, truncated)
	for index, chunk := range chunks {
		message := &discordgo.MessageSend{Content: chunk, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}}
		if index == 0 {
			failIfMissing := false
			message.Reference = &discordgo.MessageReference{MessageID: turn.messageID, ChannelID: turn.channelID, FailIfNotExists: &failIfMissing}
		}
		if err := r.deliver(turn.channelID, message); err != nil {
			_, _ = fmt.Fprintf(r.config.Audit, "Discord delivery failed input_id=%s class=uncertain\n", inputID)
			return
		}
	}
}

func (r *Runtime) continueWritable(current *surface, inputID string) {
	result, err := r.manager.Elevate(r.ctx, current.conversation, dispatch.Submission{InputID: inputID, Text: channelconfig.WriteContinuationPrompt})
	if err == nil && (result.Status == "queued" || result.Status == "active") {
		return
	}
	if err == nil && result.Status == "completed" {
		r.drop(current, inputID)
		return
	}
	r.mu.Lock()
	turn := current.turns[inputID]
	delete(current.turns, inputID)
	r.mu.Unlock()
	_, _ = fmt.Fprintf(r.config.Audit, "Discord elevation failed input_id=%s class=workspace_failure\n", inputID)
	if turn != nil {
		r.sendTurn(inputID, turn, []string{"I couldn't safely create a writable workspace for that request."}, false)
	}
}

func suppressedControl(output string) string {
	switch strings.TrimSpace(output) {
	case NoReply:
		return NoReply
	case RequestWriteAccess:
		return RequestWriteAccess
	default:
		return ""
	}
}

func discordTerminalMessage(eventType string) string {
	switch eventType {
	case "turn.failed", "driver.process_failed":
		return "I hit an error while handling that. Please try again."
	case "turn.cancelled":
		return "That request was cancelled."
	case "turn.uncertain":
		return "I lost track of that response during recovery. Please try again."
	default:
		return "I couldn't produce a response. Please try again."
	}
}

func visibleReplyDecided(output string) bool {
	candidate := strings.TrimLeftFunc(output, unicode.IsSpace)
	if candidate == "" {
		return false
	}
	for _, control := range []string{NoReply, RequestWriteAccess} {
		if strings.HasPrefix(control, candidate) {
			return false
		}
	}
	return true
}

func statusMessage(agent, harnessName, surfaceKind string, status dispatch.ConversationStatus) string {
	return fmt.Sprintf("hctl is online: agent=%s harness=%s surface=%s state=%s pending=%d", agent, harnessName, surfaceKind, status.State, status.Pending)
}

func (r *Runtime) resetSurface(id string) error {
	r.mu.Lock()
	current := r.surfaces[id]
	if current != nil && len(current.turns) != 0 {
		r.mu.Unlock()
		return errors.New("busy")
	}
	r.mu.Unlock()
	conversation := conversationID(r.config.Runtime.ApplicationID, id)
	if err := r.manager.Reset(conversation); err != nil {
		return err
	}
	r.mu.Lock()
	if r.surfaces[id] == current {
		delete(r.surfaces, id)
		delete(r.byConversation, conversation)
	}
	r.mu.Unlock()
	return nil
}

func (r *Runtime) drop(current *surface, inputID string) {
	r.mu.Lock()
	delete(current.turns, inputID)
	r.mu.Unlock()
}

func (r *Runtime) registerCommands(applicationID string) error {
	commands := []*discordgo.ApplicationCommand{{Name: "new", Description: "Start a fresh hctl conversation"}, {Name: "status", Description: "Show hctl runtime status"}}
	if _, err := r.session.ApplicationCommandBulkOverwrite(applicationID, "", commands); err != nil {
		return errors.New("cannot reconcile Discord slash commands")
	}
	return nil
}

func (r *Runtime) respond(interaction *discordgo.Interaction, content string) {
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

func appendBounded(turn *pendingTurn, itemID, value string) {
	if turn.truncated || value == "" {
		return
	}
	if itemID == "" {
		itemID = "default"
	}
	if turn.byItem == nil {
		turn.byItem = map[string]*bufferedOutput{}
	}
	output := turn.byItem[itemID]
	if output == nil {
		output = &bufferedOutput{itemID: itemID}
		turn.byItem[itemID] = output
		turn.outputs = append(turn.outputs, output)
	}
	remaining := maxOutputRunes - turn.runes
	for index := range value {
		if remaining == 0 {
			output.text.WriteString(value[:index])
			turn.truncated = true
			return
		}
		remaining--
		turn.runes++
	}
	output.text.WriteString(value)
}

func combinedOutput(turn *pendingTurn) string {
	values := make([]string, 0, len(turn.outputs))
	for _, output := range turn.outputs {
		values = append(values, output.text.String())
	}
	return strings.Join(values, "\n\n")
}

func outputParts(turn *pendingTurn) []string {
	values := make([]string, 0, len(turn.outputs))
	for _, output := range turn.outputs {
		if value := strings.TrimSpace(output.text.String()); value != "" {
			values = append(values, value)
		}
	}
	return values
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
