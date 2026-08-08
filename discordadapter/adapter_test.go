package discordadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/channeladapter"

	"github.com/bwmarrin/discordgo"
)

type memoryProfiles struct {
	mu       sync.Mutex
	profiles map[string]Profile
	putErr   error
}

type failingRestoreProfiles struct{ store FileProfileStore }

func (profiles failingRestoreProfiles) Get(id string) (Profile, error) { return profiles.store.Get(id) }
func (profiles failingRestoreProfiles) Put(string, Profile) error {
	return errors.New("restore failed")
}
func (profiles failingRestoreProfiles) Delete(id string) error { return profiles.store.Delete(id) }

func (store *memoryProfiles) Get(id string) (Profile, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	profile, ok := store.profiles[id]
	if !ok {
		return Profile{}, errors.New("discord profile is not configured; run setup")
	}
	return profile, nil
}
func (store *memoryProfiles) Put(id string, profile Profile) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.putErr != nil {
		return store.putErr
	}
	store.profiles[id] = profile
	return nil
}
func (store *memoryProfiles) Delete(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.profiles, id)
	return nil
}

type memoryCredentials struct {
	mu        sync.Mutex
	values    map[string]string
	getErr    error
	setErr    error
	deleteErr error
	setCalls  int
	failSetAt int
}

func (store *memoryCredentials) Get(id string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.getErr != nil {
		return "", store.getErr
	}
	value, ok := store.values[id]
	if !ok {
		return "", ErrCredentialNotFound
	}
	return value, nil
}
func (store *memoryCredentials) Set(id, value string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.setCalls++
	if store.setErr != nil && (store.failSetAt == 0 || store.setCalls == store.failSetAt) {
		return store.setErr
	}
	store.values[id] = value
	return nil
}
func (store *memoryCredentials) Delete(id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.deleteErr != nil {
		return store.deleteErr
	}
	delete(store.values, id)
	return nil
}

type fakeLock struct{ locked bool }

func (lock *fakeLock) TryLock() (bool, error) { lock.locked = true; return true, nil }
func (lock *fakeLock) Unlock() error          { lock.locked = false; return nil }

type httpClientFunc func(*http.Request) (*http.Response, error)

func (function httpClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

type fakeDiscord struct {
	mu                  sync.Mutex
	handlers            []any
	sent                []*discordgo.MessageSend
	edits               []*discordgo.MessageEdit
	reactions           []string
	responses           []*discordgo.InteractionResponse
	typing              []string
	sendErr             error
	opened              bool
	closed              bool
	applicationCommands int
}

func (*fakeDiscord) User(string, ...discordgo.RequestOption) (*discordgo.User, error) {
	return &discordgo.User{ID: "222", Username: "fixture-bot", Bot: true}, nil
}
func (*fakeDiscord) Application(string) (*discordgo.Application, error) {
	return &discordgo.Application{ID: "111"}, nil
}
func (*fakeDiscord) Channel(string, ...discordgo.RequestOption) (*discordgo.Channel, error) {
	return &discordgo.Channel{ID: "555", GuildID: "444"}, nil
}
func (*fakeDiscord) GuildMember(string, string, ...discordgo.RequestOption) (*discordgo.Member, error) {
	return &discordgo.Member{User: &discordgo.User{ID: "333"}}, nil
}
func (discord *fakeDiscord) Open() error {
	discord.opened = true
	discord.emitReady()
	return nil
}
func (discord *fakeDiscord) Close() error { discord.closed = true; return nil }
func (discord *fakeDiscord) AddHandler(handler any) func() {
	discord.handlers = append(discord.handlers, handler)
	return func() {}
}
func (discord *fakeDiscord) ApplicationCommandBulkOverwrite(string, string, []*discordgo.ApplicationCommand, ...discordgo.RequestOption) ([]*discordgo.ApplicationCommand, error) {
	discord.mu.Lock()
	defer discord.mu.Unlock()
	discord.applicationCommands++
	return nil, nil
}
func (discord *fakeDiscord) ChannelMessageSendComplex(_ string, message *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	discord.mu.Lock()
	defer discord.mu.Unlock()
	discord.sent = append(discord.sent, message)
	if discord.sendErr != nil {
		return nil, discord.sendErr
	}
	return &discordgo.Message{ID: "777"}, nil
}
func (discord *fakeDiscord) ChannelMessageEditComplex(message *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	discord.mu.Lock()
	defer discord.mu.Unlock()
	discord.edits = append(discord.edits, message)
	return &discordgo.Message{ID: message.ID}, nil
}
func (discord *fakeDiscord) MessageReactionAdd(_, _, reaction string, _ ...discordgo.RequestOption) error {
	discord.mu.Lock()
	defer discord.mu.Unlock()
	discord.reactions = append(discord.reactions, reaction)
	return nil
}
func (discord *fakeDiscord) ChannelTyping(channel string, _ ...discordgo.RequestOption) error {
	discord.mu.Lock()
	defer discord.mu.Unlock()
	discord.typing = append(discord.typing, channel)
	return nil
}
func (discord *fakeDiscord) InteractionRespond(_ *discordgo.Interaction, response *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	discord.mu.Lock()
	defer discord.mu.Unlock()
	discord.responses = append(discord.responses, response)
	return nil
}
func (discord *fakeDiscord) emitMessage(message *discordgo.MessageCreate) {
	for _, handler := range discord.handlers {
		if typed, ok := handler.(func(*discordgo.Session, *discordgo.MessageCreate)); ok {
			typed(nil, message)
		}
	}
}
func (discord *fakeDiscord) emitInteraction(interaction *discordgo.InteractionCreate) {
	for _, handler := range discord.handlers {
		if typed, ok := handler.(func(*discordgo.Session, *discordgo.InteractionCreate)); ok {
			typed(nil, interaction)
		}
	}
}
func (discord *fakeDiscord) emitDisconnect() {
	for _, handler := range discord.handlers {
		if typed, ok := handler.(func(*discordgo.Session, *discordgo.Disconnect)); ok {
			typed(nil, &discordgo.Disconnect{})
		}
	}
}
func (discord *fakeDiscord) emitResumed() {
	for _, handler := range discord.handlers {
		if typed, ok := handler.(func(*discordgo.Session, *discordgo.Resumed)); ok {
			typed(nil, &discordgo.Resumed{})
		}
	}
}
func (discord *fakeDiscord) emitReady() {
	for _, handler := range discord.handlers {
		if typed, ok := handler.(func(*discordgo.Session, *discordgo.Ready)); ok {
			typed(nil, &discordgo.Ready{})
		}
	}
}

func (discord *fakeDiscord) snapshot() (sent []*discordgo.MessageSend, typing int, responses []*discordgo.InteractionResponse, commands int) {
	discord.mu.Lock()
	defer discord.mu.Unlock()
	return append([]*discordgo.MessageSend(nil), discord.sent...), len(discord.typing), append([]*discordgo.InteractionResponse(nil), discord.responses...), discord.applicationCommands
}

func fixtureProfile() Profile {
	return Profile{ApplicationID: "111", BotUserID: "222", BotName: "fixture-bot", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"}
}

func fixtureDependencies(discord *fakeDiscord) Dependencies {
	profiles := &memoryProfiles{profiles: map[string]Profile{"default": fixtureProfile()}}
	credentials := &memoryCredentials{values: map[string]string{"default": "fake-token"}}
	return Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(string) (Discord, error) { return discord, nil }, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }, After: time.After}
}

func TestDiscordGatewayDispatchUsesBoundedSynchronousAdmission(t *testing.T) {
	client, err := NewDiscord("fake-token")
	if err != nil {
		t.Fatal(err)
	}
	session, ok := client.(*discordgo.Session)
	if !ok || !session.SyncEvents {
		t.Fatal("Discord Gateway events can bypass adapter backpressure")
	}
}

func TestSetupStatusRemoveAndCredentialRedaction(t *testing.T) {
	if KeyringService != "hctl.discord" {
		t.Fatalf("keyring service changed to %q", KeyringService)
	}
	t.Setenv("HCTL_DISCORD_TOKEN", "")
	discord := &fakeDiscord{}
	profiles := &memoryProfiles{profiles: map[string]Profile{}}
	credentials := &memoryCredentials{values: map[string]string{}}
	dependencies := Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(token string) (Discord, error) {
		if token != "fake-token" {
			t.Fatalf("factory token = %q", token)
		}
		return discord, nil
	}, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }}
	input := strings.NewReader("fake-token\n333\n444\n555\n")
	var output, terminal bytes.Buffer
	if err := RunCommand(context.Background(), []string{"setup", "--profile", "default"}, input, &output, &terminal, dependencies); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String()+terminal.String(), "fake-token") {
		t.Fatal("setup output contained credential")
	}
	result, err := channeladapter.DecodeOperationResult(bytes.TrimSpace(output.Bytes()))
	if err != nil || result.Status != "ready" || result.Identity != "fixture-bot" {
		t.Fatalf("setup result = %#v, %v", result, err)
	}
	if credentials.values["default"] != "fake-token" || profiles.profiles["default"].ApplicationID != "111" {
		t.Fatal("setup did not atomically enroll profile and credential")
	}

	t.Setenv("HCTL_DISCORD_TOKEN", "deployment-token")
	dependencies.Discord = func(token string) (Discord, error) {
		if token != "deployment-token" {
			t.Fatalf("status did not prefer deployment environment")
		}
		return discord, nil
	}
	output.Reset()
	if err := RunCommand(context.Background(), []string{"status", "--profile", "default"}, strings.NewReader(""), &output, io.Discard, dependencies); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "deployment-token") {
		t.Fatal("status output contained environment credential")
	}
	output.Reset()
	if err := RunCommand(context.Background(), []string{"remove", "--profile", "default"}, strings.NewReader(""), &output, io.Discard, dependencies); err != nil {
		t.Fatal(err)
	}
	if _, ok := profiles.profiles["default"]; ok {
		t.Fatal("remove retained profile")
	}
	if _, ok := credentials.values["default"]; ok {
		t.Fatal("remove retained credential")
	}
}

func TestRemoveRestoresProfileWhenCredentialDeletionFails(t *testing.T) {
	profiles := &memoryProfiles{profiles: map[string]Profile{"default": fixtureProfile()}}
	credentials := &memoryCredentials{values: map[string]string{"default": "fake-token"}, deleteErr: errors.New("keyring unavailable")}
	_, err := remove("default", Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(string) (Discord, error) { return &fakeDiscord{}, nil }, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }})
	if err == nil || profiles.profiles["default"] != fixtureProfile() {
		t.Fatalf("remove rollback profile = %#v, %v", profiles.profiles["default"], err)
	}
}

func TestSetupRollsBackCredentialOnProfileFailure(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "")
	profiles := &memoryProfiles{profiles: map[string]Profile{"default": fixtureProfile()}, putErr: errors.New("save failed")}
	credentials := &memoryCredentials{values: map[string]string{"default": "old-token"}}
	dependencies := Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(string) (Discord, error) { return &fakeDiscord{}, nil }, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }}
	err := RunCommand(context.Background(), []string{"setup", "--profile", "default"}, strings.NewReader("new-token\n333\n444\n555\n"), io.Discard, io.Discard, dependencies)
	if err == nil || credentials.values["default"] != "old-token" {
		t.Fatalf("setup rollback = %q, %v", credentials.values["default"], err)
	}
}

func TestSetupReportsCredentialRollbackFailure(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "")
	profiles := &memoryProfiles{profiles: map[string]Profile{"default": fixtureProfile()}, putErr: errors.New("save failed")}
	credentials := &memoryCredentials{values: map[string]string{"default": "old-token"}, setErr: errors.New("restore failed"), failSetAt: 2}
	dependencies := Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(string) (Discord, error) { return &fakeDiscord{}, nil }, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }}
	err := RunCommand(context.Background(), []string{"setup", "--profile", "default"}, strings.NewReader("new-token\n333\n444\n555\n"), io.Discard, io.Discard, dependencies)
	if err == nil || !strings.Contains(err.Error(), "cannot restore the prior credential") {
		t.Fatalf("setup rollback error = %v", err)
	}
	if credentials.values["default"] != "new-token" {
		t.Fatalf("failed rollback unexpectedly changed credential = %q", credentials.values["default"])
	}
}

func TestSetupAbortsOnCredentialReadFailure(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "")
	original := fixtureProfile()
	profiles := &memoryProfiles{profiles: map[string]Profile{"default": original}}
	credentials := &memoryCredentials{values: map[string]string{"default": "old-token"}, getErr: errors.New("transient keyring failure")}
	dependencies := Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(string) (Discord, error) { return &fakeDiscord{}, nil }, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }}
	err := RunCommand(context.Background(), []string{"setup", "--profile", "default"}, strings.NewReader("new-token\n333\n444\n555\n"), io.Discard, io.Discard, dependencies)
	if err == nil || !strings.Contains(err.Error(), "no setup state was changed") {
		t.Fatalf("credential read error = %v", err)
	}
	if credentials.setCalls != 0 || credentials.values["default"] != "old-token" || profiles.profiles["default"] != original {
		t.Fatalf("transient read failure mutated state: sets=%d credential=%q profile=%#v", credentials.setCalls, credentials.values["default"], profiles.profiles["default"])
	}
}

func TestRemoveReportsProfileRollbackFailure(t *testing.T) {
	profiles := &memoryProfiles{profiles: map[string]Profile{"default": fixtureProfile()}, putErr: errors.New("restore failed")}
	credentials := &memoryCredentials{values: map[string]string{"default": "fake-token"}, deleteErr: errors.New("keyring unavailable")}
	_, err := remove("default", Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(string) (Discord, error) { return &fakeDiscord{}, nil }, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }})
	if err == nil || !strings.Contains(err.Error(), "cannot restore the profile") {
		t.Fatalf("remove rollback error = %v", err)
	}
	if _, ok := profiles.profiles["default"]; ok {
		t.Fatal("failed rollback unexpectedly restored profile")
	}
}

func TestLegacyProfileMigrationPreservesExactIdentity(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "config.toml")
	data := []byte("schema_version = 1\n[discord]\ndefault_profile = 'default'\n[discord.profiles.default]\napplication_id = '111'\nbot_user_id = '222'\nallowed_user_id = '333'\nallowed_guild_id = '444'\nallowed_channel_id = '555'\n[agent_profiles]\n'agent@one' = 'default'\n")
	if err := os.WriteFile(legacy, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := FileProfileStore{Path: filepath.Join(root, "adapter", "profiles.toml"), LegacyPath: legacy}
	profile, err := store.Get("default")
	if err != nil || profile != fixtureProfileWithoutName() {
		t.Fatalf("migrated profile = %#v, %v", profile, err)
	}
	if info, err := os.Stat(store.Path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("migrated store mode = %v, %v", info.Mode(), err)
	}
}

func TestProfilePersistsAuthorizedDirectSurfaceForRecovery(t *testing.T) {
	root := t.TempDir()
	store := FileProfileStore{Path: filepath.Join(root, "profiles.toml")}
	profile := fixtureProfile()
	profile.DirectChannelID = "777"
	if err := store.Put("default", profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get("default")
	if err != nil || loaded.DirectChannelID != "777" {
		t.Fatalf("direct recovery profile = %#v, %v", loaded, err)
	}
	surfaces := discordSurfaces(loaded)
	if len(surfaces) != 2 || surfaces[0].Kind != channeladapter.SurfaceShared || surfaces[1].Kind != channeladapter.SurfaceDirect || surfaces[1].ConversationID != discordConversationID(profile.ApplicationID, "777") {
		t.Fatalf("recovery surfaces = %#v", surfaces)
	}
}

func TestLegacyProfileRemovalTombstonePreventsReimportAndPreservesRollback(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "ambient-token")
	root := t.TempDir()
	legacy := filepath.Join(root, "config.toml")
	data := []byte("schema_version = 1\n[discord]\ndefault_profile = 'default'\n[discord.profiles.default]\napplication_id = '111'\nbot_user_id = '222'\nallowed_user_id = '333'\nallowed_guild_id = '444'\nallowed_channel_id = '555'\n")
	if err := os.WriteFile(legacy, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := FileProfileStore{Path: filepath.Join(root, "adapter", "profiles.toml"), LegacyPath: legacy}
	if profile, err := store.Get("default"); err != nil || profile != fixtureProfileWithoutName() {
		t.Fatalf("initial migration = %#v, %v", profile, err)
	}
	credentials := &memoryCredentials{values: map[string]string{"default": "stored-token"}, deleteErr: errors.New("keyring unavailable")}
	factoryCalls := 0
	dependencies := Dependencies{Profiles: store, Credentials: credentials, Discord: func(string) (Discord, error) {
		factoryCalls++
		return &fakeDiscord{}, nil
	}, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }}
	if _, err := remove("default", dependencies); err == nil {
		t.Fatal("remove unexpectedly ignored credential deletion failure")
	}
	if profile, err := store.Get("default"); err != nil || profile != fixtureProfileWithoutName() {
		t.Fatalf("remove rollback did not restore migrated profile = %#v, %v", profile, err)
	}
	if document, _, err := store.load(); err != nil || document.LegacyRemovalTombstones["default"] {
		t.Fatalf("remove rollback retained tombstone = %#v, %v", document, err)
	}
	credentials.deleteErr = nil
	if _, err := remove("default", dependencies); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("default"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("removed legacy profile reimported = %v", err)
	}
	if _, err := status(context.Background(), "default", dependencies); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("status after remove = %v", err)
	}
	var output bytes.Buffer
	runtime, err := NewRuntime(&output, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.connect(context.Background(), "default"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("runtime after remove = %v", err)
	}
	if factoryCalls != 0 {
		t.Fatalf("removed profile reached Discord with ambient credential: %d calls", factoryCalls)
	}
	document, exists, err := store.load()
	if err != nil || !exists || !document.LegacyRemovalTombstones["default"] {
		t.Fatalf("removal tombstone = %#v, %t, %v", document, exists, err)
	}
	if info, err := os.Stat(store.Path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("tombstone store mode = %v, %v", info.Mode(), err)
	}
}

func TestLegacyRemovalRollbackFailureLeavesProtectiveTombstone(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "config.toml")
	data := []byte("schema_version = 1\n[discord.profiles.default]\napplication_id = '111'\nbot_user_id = '222'\nallowed_user_id = '333'\nallowed_guild_id = '444'\nallowed_channel_id = '555'\n")
	if err := os.WriteFile(legacy, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := FileProfileStore{Path: filepath.Join(root, "adapter", "profiles.toml"), LegacyPath: legacy}
	if _, err := store.Get("default"); err != nil {
		t.Fatal(err)
	}
	profiles := failingRestoreProfiles{store: store}
	credentials := &memoryCredentials{values: map[string]string{"default": "stored-token"}, deleteErr: errors.New("keyring unavailable")}
	_, err := remove("default", Dependencies{Profiles: profiles, Credentials: credentials, Discord: func(string) (Discord, error) { return &fakeDiscord{}, nil }, Locks: func(string) (ApplicationLock, error) { return &fakeLock{}, nil }})
	if err == nil || !strings.Contains(err.Error(), "cannot restore the profile") {
		t.Fatalf("remove rollback failure = %v", err)
	}
	if _, err := store.Get("default"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("rollback failure reimported legacy profile = %v", err)
	}
	document, _, err := store.load()
	if err != nil || !document.LegacyRemovalTombstones["default"] {
		t.Fatalf("rollback-failure tombstone = %#v, %v", document, err)
	}
}

func fixtureProfileWithoutName() Profile {
	profile := fixtureProfile()
	profile.BotName = ""
	return profile
}

func TestRuntimeProtocolCoversGatewayDeliveryInteractionReconnectAndShutdown(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "")
	discord := &fakeDiscord{}
	dependencies := fixtureDependencies(discord)
	dependencies.HTTP = httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://cdn.example.invalid/attachment" {
			t.Fatalf("attachment URL = %s", request.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("abc"))}, nil
	})
	hostRead, adapterWrite := io.Pipe()
	adapterRead, hostWrite := io.Pipe()
	runtime, err := NewRuntime(adapterWrite, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runtime.Run(context.Background(), adapterRead) }()
	decoder := channeladapter.NewDecoder(hostRead)
	encoder := channeladapter.NewEncoder(hostWrite)
	hello := readAdapterFrame(t, decoder)
	if _, ok := hello.Payload.(*channeladapter.Hello); !ok {
		t.Fatalf("first frame = %#v", hello)
	}
	initializeID := "host.initialize.1"
	initialize := channeladapter.Initialize{SelectedVersion: 1, ProfileID: "default", Features: append([]channeladapter.Feature(nil), discordFeatures...), Limits: discordLimits, Policy: channeladapter.RuntimePolicy{Participation: channeladapter.ParticipationAmbient, MaxInboundTextBytes: channeladapter.MaxTextBytes, MaxDeliveryTextBytes: channeladapter.MaxTextBytes, MaxAttachmentBytes: channeladapter.MaxAttachmentBytes}}
	writeHostFrame(t, encoder, initializeID, hello.ID, initialize)
	if frame := readAdapterFrame(t, decoder); frame.CorrelationID != initializeID {
		t.Fatalf("ready correlation = %q", frame.CorrelationID)
	}
	assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionConnecting)
	assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionReady)
	_, _, _, commandCount := discord.snapshot()
	if !discord.opened || commandCount != 1 {
		t.Fatal("runtime did not open Gateway and reconcile commands")
	}

	discord.emitMessage(&discordgo.MessageCreate{Message: &discordgo.Message{ID: "666", ChannelID: "555", GuildID: "444", Content: "please review", Author: &discordgo.User{ID: "333", Username: "operator"}}})
	inbound := readAdapterFrame(t, decoder)
	message, ok := inbound.Payload.(*channeladapter.InboundMessage)
	if !ok || message.Text != "please review" || message.Route.Handle != "555" || strings.Contains(string(mustMarshalFrame(t, inbound)), "fake-token") {
		t.Fatalf("inbound = %#v", inbound)
	}
	discord.emitDisconnect()
	assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionReconnecting)
	discord.emitReady()
	assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionReady)
	replayed := readAdapterFrame(t, decoder)
	if replayed.ID != inbound.ID || !bytes.Equal(mustMarshalFrame(t, replayed), mustMarshalFrame(t, inbound)) {
		t.Fatalf("replayed event changed: %#v, %#v", inbound, replayed)
	}
	writeHostFrame(t, encoder, "host.ack.1", replayed.ID, channeladapter.EventAck{Disposition: "accepted"})
	waitUntil(t, func() bool { return pendingEventCount(runtime) == 0 })

	discord.emitMessage(&discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "667", ChannelID: "555", GuildID: "444", Content: "see attachment",
		Author: &discordgo.User{ID: "333", Username: "operator"},
		Attachments: []*discordgo.MessageAttachment{{
			ID: "900", Filename: "note.txt", ContentType: "text/plain", Size: 3,
			URL: "https://cdn.example.invalid/attachment",
		}},
	}})
	attachmentInbound := readAdapterFrame(t, decoder)
	attachmentMessage := attachmentInbound.Payload.(*channeladapter.InboundMessage)
	if len(attachmentMessage.Attachments) != 1 || strings.Contains(string(mustMarshalFrame(t, attachmentInbound)), "cdn.example") {
		t.Fatalf("attachment descriptor = %#v", attachmentMessage.Attachments)
	}
	writeHostFrame(t, encoder, "host.fetch.1", "", channeladapter.AttachmentFetch{TransferID: "transfer.in.1", AttachmentHandle: attachmentMessage.Attachments[0].Handle, MaximumBytes: 16})
	firstChunk := readAdapterFrame(t, decoder).Payload.(*channeladapter.AttachmentChunk)
	if firstChunk.Data != "YWJj" || !firstChunk.Final {
		t.Fatalf("attachment chunk = %#v", firstChunk)
	}
	writeHostFrame(t, encoder, "host.ack.2", attachmentInbound.ID, channeladapter.EventAck{Disposition: "accepted"})
	waitUntil(t, func() bool { return pendingEventCount(runtime) == 0 })

	writeHostFrame(t, encoder, "host.activity.1", "", channeladapter.Activity{Route: channeladapter.Route{Handle: "555"}, Kind: channeladapter.ActivityTyping})
	writeHostFrame(t, encoder, "host.delivery.1", "", channeladapter.Delivery{Action: channeladapter.DeliverySend, Route: channeladapter.Route{Handle: "555"}, ReplyTo: &channeladapter.MessageRef{Handle: "666"}, Text: "done"})
	deliveryResult := readAdapterFrame(t, decoder)
	if result, ok := deliveryResult.Payload.(*channeladapter.DeliveryResult); !ok || result.Disposition != channeladapter.EffectExact || deliveryResult.CorrelationID != "host.delivery.1" {
		t.Fatalf("delivery result = %#v", deliveryResult)
	}
	sent, typingCount, _, _ := discord.snapshot()
	if typingCount != 1 || len(sent) != 1 || sent[0].AllowedMentions == nil || len(sent[0].AllowedMentions.Parse) != 0 {
		t.Fatal("Discord delivery did not preserve typing, reply, and mention safety")
	}
	writeHostFrame(t, encoder, "host.attachment.1", "", channeladapter.AttachmentDeliver{TransferID: "transfer.out.1", Sequence: 0, Name: "report.txt", MediaType: "text/plain", Data: "YWJj", Final: true})
	if result := readAdapterFrame(t, decoder).Payload.(*channeladapter.AttachmentResult); result.Disposition != channeladapter.EffectExact {
		t.Fatalf("attachment delivery = %#v", result)
	}
	writeHostFrame(t, encoder, "host.delivery.attachment", "", channeladapter.Delivery{Action: channeladapter.DeliverySend, Route: channeladapter.Route{Handle: "555"}, Text: "report", AttachmentTransfers: []string{"transfer.out.1"}})
	if result := readAdapterFrame(t, decoder).Payload.(*channeladapter.DeliveryResult); result.Disposition != channeladapter.EffectExact {
		t.Fatalf("attachment message = %#v", result)
	}
	sent, _, _, _ = discord.snapshot()
	if len(sent) != 2 || len(sent[1].Files) != 1 || sent[1].Files[0].Name != "report.txt" {
		t.Fatalf("Discord attachment message = %#v", sent)
	}
	writeHostFrame(t, encoder, "host.delivery.missing", "", channeladapter.Delivery{Action: channeladapter.DeliverySend, Route: channeladapter.Route{Handle: "555"}, AttachmentTransfers: []string{"transfer.missing"}})
	if result := readAdapterFrame(t, decoder).Payload.(*channeladapter.DeliveryResult); result.Disposition != channeladapter.EffectFailed || result.Failure.Code != "discord_delivery_invalid" {
		t.Fatalf("pre-attempt attachment failure = %#v", result)
	}

	request := confirmRequest()
	writeHostFrame(t, encoder, "host.interaction.1", "", request)
	if receipt := readAdapterFrame(t, decoder); receipt.CorrelationID != "host.interaction.1" || receipt.Payload.(*channeladapter.InteractionReceipt).Disposition != channeladapter.EffectExact {
		t.Fatalf("interaction receipt = %#v", receipt)
	}
	waitUntil(t, func() bool { sent, _, _, _ := discord.snapshot(); return len(sent) == 3 })
	sent, _, _, _ = discord.snapshot()
	if len(sent[2].Components) == 0 {
		t.Fatal("interaction was not rendered with native components")
	}
	runtime.mu.Lock()
	pending := runtime.interactions[request.InteractionID]
	runtime.mu.Unlock()
	discord.emitInteraction(componentInteraction(customID(pending.handle, "y")))
	interactionResult := readAdapterFrame(t, decoder)
	if result, ok := interactionResult.Payload.(*channeladapter.InteractionResult); !ok || result.Answer.Action != channeladapter.AnswerSubmit || interactionResult.CorrelationID != "host.interaction.1" {
		t.Fatalf("interaction result = %#v", interactionResult)
	}
	writeHostFrame(t, encoder, "host.ack.interaction", interactionResult.ID, channeladapter.EventAck{Disposition: "accepted"})

	discord.emitInteraction(commandInteraction("status"))
	control := readAdapterFrame(t, decoder)
	controlRequest, ok := control.Payload.(*channeladapter.ControlRequest)
	if !ok || controlRequest.Action != channeladapter.ControlStatus {
		t.Fatalf("control request = %#v", control)
	}
	writeHostFrame(t, encoder, "host.control.1", control.ID, channeladapter.ControlResult{Action: channeladapter.ControlStatus, Disposition: channeladapter.ControlExact, Status: &channeladapter.RuntimeStatus{Agent: "maintainer", Harness: "codex", State: channeladapter.LifecycleIdle, ActiveLimit: 2, ResidentLimit: 4}})
	waitUntil(t, func() bool { _, _, responses, _ := discord.snapshot(); return len(responses) >= 2 })
	_, _, responses, _ := discord.snapshot()
	if strings.Contains(responses[len(responses)-1].Data.Content, "555") {
		t.Fatal("status response exposed a vendor route")
	}

	discord.emitDisconnect()
	assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionReconnecting)
	discord.emitResumed()
	assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionReady)

	discord.mu.Lock()
	discord.sendErr = errors.New("ambiguous transport failure")
	discord.mu.Unlock()
	writeHostFrame(t, encoder, "host.delivery.2", "", channeladapter.Delivery{Action: channeladapter.DeliverySend, Route: channeladapter.Route{Handle: "555"}, Text: "uncertain"})
	if result := readAdapterFrame(t, decoder).Payload.(*channeladapter.DeliveryResult); result.Disposition != channeladapter.EffectAmbiguous {
		t.Fatalf("ambiguous result = %#v", result)
	}

	writeHostFrame(t, encoder, "host.shutdown.1", "", channeladapter.Shutdown{Reason: "test complete"})
	assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionClosed)
	shutdown := readAdapterFrame(t, decoder)
	if _, ok := shutdown.Payload.(*channeladapter.ShutdownComplete); !ok || shutdown.CorrelationID != "host.shutdown.1" {
		t.Fatalf("shutdown = %#v", shutdown)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime did not stop")
	}
}

func TestDiscordDeliveryChunksStayBoundedAndVisible(t *testing.T) {
	chunks := discordChunks(strings.Repeat("a", 6*2000+1))
	if len(chunks) != 6 || !strings.HasSuffix(chunks[5], "[output truncated]") {
		t.Fatalf("chunks = %d, last = %q", len(chunks), chunks[len(chunks)-1])
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 2000 {
			t.Fatal("Discord delivery chunk exceeded 2,000 runes")
		}
	}
}

func TestRuntimeCancellationAndMalformedFrameAreBounded(t *testing.T) {
	t.Setenv("HCTL_DISCORD_TOKEN", "")
	for _, test := range []struct {
		name string
		run  func(*testing.T, *channeladapter.Encoder, *io.PipeWriter, *channeladapter.Decoder, *Runtime)
	}{
		{name: "cancellation", run: func(t *testing.T, encoder *channeladapter.Encoder, _ *io.PipeWriter, decoder *channeladapter.Decoder, runtime *Runtime) {
			request := confirmRequest()
			writeHostFrame(t, encoder, "host.interaction.cancel", "", request)
			if receipt := readAdapterFrame(t, decoder); receipt.Payload.(*channeladapter.InteractionReceipt).Disposition != channeladapter.EffectExact {
				t.Fatalf("interaction receipt = %#v", receipt)
			}
			writeHostFrame(t, encoder, "host.cancel.1", "", channeladapter.InteractionCancel{InteractionID: request.InteractionID})
			waitUntil(t, func() bool {
				runtime.mu.Lock()
				defer runtime.mu.Unlock()
				return len(runtime.interactions) == 0
			})
			_ = decoder
			_ = runtime
		}},
		{name: "malformed", run: func(t *testing.T, _ *channeladapter.Encoder, writer *io.PipeWriter, _ *channeladapter.Decoder, _ *Runtime) {
			if _, err := io.WriteString(writer, "{not-json}\n"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized", run: func(_ *testing.T, _ *channeladapter.Encoder, writer *io.PipeWriter, _ *channeladapter.Decoder, _ *Runtime) {
			go func() {
				_, _ = writer.Write(append(bytes.Repeat([]byte("x"), channeladapter.MaxFrameBytes+1), '\n'))
			}()
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			discord := &fakeDiscord{}
			hostRead, adapterWrite := io.Pipe()
			adapterRead, hostWrite := io.Pipe()
			runtime, _ := NewRuntime(adapterWrite, fixtureDependencies(discord))
			done := make(chan error, 1)
			go func() { done <- runtime.Run(context.Background(), adapterRead) }()
			decoder, encoder := channeladapter.NewDecoder(hostRead), channeladapter.NewEncoder(hostWrite)
			hello := readAdapterFrame(t, decoder)
			initialize := channeladapter.Initialize{SelectedVersion: 1, ProfileID: "default", Features: discordFeatures, Limits: discordLimits, Policy: channeladapter.RuntimePolicy{Participation: channeladapter.ParticipationAmbient, MaxInboundTextBytes: channeladapter.MaxTextBytes, MaxDeliveryTextBytes: channeladapter.MaxTextBytes, MaxAttachmentBytes: channeladapter.MaxAttachmentBytes}}
			writeHostFrame(t, encoder, "host.initialize.1", hello.ID, initialize)
			_ = readAdapterFrame(t, decoder)
			_ = readAdapterFrame(t, decoder)
			_ = readAdapterFrame(t, decoder)
			test.run(t, encoder, hostWrite, decoder, runtime)
			if test.name == "cancellation" {
				writeHostFrame(t, encoder, "host.shutdown.1", "", channeladapter.Shutdown{Reason: "done"})
				_ = readAdapterFrame(t, decoder)
				_ = readAdapterFrame(t, decoder)
			} else {
				assertConnection(t, readAdapterFrame(t, decoder), channeladapter.ConnectionClosed)
			}
			select {
			case err := <-done:
				if test.name != "cancellation" && err == nil {
					t.Fatal("invalid frame was accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("runtime did not terminate")
			}
		})
	}
}

func TestRuntimeHandshakeUsesInjectedDeadline(t *testing.T) {
	discord := &fakeDiscord{}
	dependencies := fixtureDependencies(discord)
	deadline := make(chan time.Time, 1)
	deadline <- time.Unix(1, 0)
	dependencies.After = func(time.Duration) <-chan time.Time { return deadline }
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	var output bytes.Buffer
	runtime, err := NewRuntime(&output, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	err = runtime.Run(context.Background(), reader)
	if err == nil || !strings.Contains(err.Error(), "initialization timed out") || !strings.Contains(output.String(), `"kind":"hello"`) {
		t.Fatalf("handshake timeout = %v, output = %q", err, output.String())
	}
}

func confirmRequest() channeladapter.InteractionRequest {
	return channeladapter.InteractionRequest{InteractionID: "interaction.1", Route: channeladapter.Route{Handle: "555"}, ReplyTo: channeladapter.MessageRef{Handle: "666"}, Request: channeladapter.SemanticInteractionRequest{SchemaVersion: 1, Kind: channeladapter.InteractionConfirm, Prompt: "Continue?", Policy: channeladapter.InteractionPolicy{ExpiresAfterSeconds: 60, Cancellation: channeladapter.CancellationAllowed}, Field: &channeladapter.Field{ID: "confirmation", Kind: channeladapter.InteractionConfirm, Label: "Continue", Required: true}}}
}

func componentInteraction(customID string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "888", AppID: "111", Type: discordgo.InteractionMessageComponent, GuildID: "444", ChannelID: "555", Member: &discordgo.Member{User: &discordgo.User{ID: "333"}}, Data: discordgo.MessageComponentInteractionData{CustomID: customID, ComponentType: discordgo.ButtonComponent}}}
}

func commandInteraction(name string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{ID: "889", AppID: "111", Type: discordgo.InteractionApplicationCommand, GuildID: "444", ChannelID: "555", Member: &discordgo.Member{User: &discordgo.User{ID: "333"}}, Data: discordgo.ApplicationCommandInteractionData{Name: name}}}
}

func writeHostFrame(t *testing.T, encoder *channeladapter.Encoder, id, correlation string, payload channeladapter.Payload) {
	t.Helper()
	if err := encoder.Write(channeladapter.Envelope{ProtocolVersion: 1, ID: id, CorrelationID: correlation, Payload: payload}, channeladapter.FromHost); err != nil {
		t.Fatal(err)
	}
}

func readAdapterFrame(t *testing.T, decoder *channeladapter.Decoder) channeladapter.Envelope {
	t.Helper()
	type result struct {
		frame channeladapter.Envelope
		err   error
	}
	resultChannel := make(chan result, 1)
	go func() {
		frame, err := decoder.Read(channeladapter.FromAdapter)
		resultChannel <- result{frame: frame, err: err}
	}()
	select {
	case result := <-resultChannel:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.frame
	case <-time.After(time.Second):
		t.Fatal("timed out reading adapter frame")
		return channeladapter.Envelope{}
	}
}

func assertConnection(t *testing.T, frame channeladapter.Envelope, expected channeladapter.ConnectionState) {
	t.Helper()
	connection, ok := frame.Payload.(*channeladapter.Connection)
	if !ok || connection.State != expected {
		t.Fatalf("connection = %#v, expected %s", frame, expected)
	}
}

func mustMarshalFrame(t *testing.T, frame channeladapter.Envelope) []byte {
	t.Helper()
	data, err := channeladapter.MarshalFrame(frame, channeladapter.FromAdapter)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition was not satisfied")
		}
		time.Sleep(time.Millisecond)
	}
}

func pendingEventCount(runtime *Runtime) int {
	runtime.writer.mu.Lock()
	defer runtime.writer.mu.Unlock()
	return len(runtime.writer.pending)
}
