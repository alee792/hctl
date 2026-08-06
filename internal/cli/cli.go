package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"hctl/internal/channel/discord"
	"hctl/internal/channelconfig"
	"hctl/internal/connection/github"
	"hctl/internal/credential"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/harness/codex"
	"hctl/internal/mcp"
	"hctl/internal/project"
	"hctl/internal/rootfs"
	"hctl/internal/schedule"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

const help = `Usage: hctl <command> [arguments]

Commands:
  apply AGENT --harness <claude|codex>    Prepare tools and native files
  run AGENT --harness <claude|codex>      Run configured conversational channels
  channel setup discord AGENT             Enroll an existing Discord bot
  channel status discord AGENT            Validate Discord configuration
  schedule trigger AGENT NAME [options]   Run one scheduled occurrence

Run "hctl <command> --help" for command details.
`

func Run(args []string, input io.Reader, output, stderr io.Writer, self string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(output, help)
		return err
	}
	switch args[0] {
	case "apply":
		return runApply(args[1:], output, stderr, self)
	case "run":
		return runAgent(args[1:], input, output, stderr, self)
	case "channel":
		return runChannel(args[1:], input, output, stderr)
	case "schedule":
		return runSchedule(args[1:], output, stderr)
	case "mcp":
		return runMCP(args[1:], input, output, stderr)
	default:
		return fmt.Errorf("unknown command %q; expected apply, run, channel, or schedule", args[0])
	}
}

func runSchedule(args []string, output, stderr io.Writer) error {
	const usage = "Usage: hctl schedule trigger AGENT NAME [--workspace DIR] --harness <claude|codex> --input-id ID [--command PATH] [--timeout DURATION]\n"
	if len(args) > 0 && isHelp(args[len(args)-1]) {
		_, err := io.WriteString(output, usage)
		return err
	}
	if len(args) < 3 || args[0] != "trigger" {
		return errors.New("usage: hctl schedule trigger AGENT NAME --harness <claude|codex> --input-id ID")
	}
	fs := flag.NewFlagSet("schedule trigger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	workspace := fs.String("workspace", "", "target workspace (defaults to AGENT)")
	inputID := fs.String("input-id", "", "stable id for this occurrence")
	command := fs.String("command", "", "harness executable override")
	timeout := fs.Duration("timeout", 2*time.Minute, "bounded trigger process lifetime")
	if err := fs.Parse(args[3:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected schedule arguments")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 30m")
	}
	if err := dispatch.ValidateInputID(*inputID); err != nil {
		return err
	}
	p, err := project.Load(args[1], *harnessName, *workspace)
	if err != nil {
		return err
	}
	if err := setup.Verify(p); err != nil {
		return err
	}
	driver, err := newDriver(*harnessName, *command)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if err := driver.Verify(ctx); err != nil {
		return err
	}
	result, triggerErr := schedule.Trigger(ctx, p, driver, args[2], *inputID)
	if result.Status != "" {
		if _, err := fmt.Fprintf(output, "schedule=%q input_id=%q status=%s duplicate=%t", result.Name, result.InputID, result.Status, result.Duplicate); err != nil {
			return err
		}
		if result.SessionID != "" {
			if _, err := fmt.Fprintf(output, " session_id=%s", result.SessionID); err != nil {
				return err
			}
		}
		if result.TurnID != "" {
			if _, err := fmt.Fprintf(output, " turn_id=%s", result.TurnID); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(output); err != nil {
			return err
		}
	}
	if triggerErr != nil {
		return triggerErr
	}
	if result.Status != "completed" {
		return fmt.Errorf("schedule trigger ended with status %s", result.Status)
	}
	return nil
}

func runChannel(args []string, input io.Reader, output, stderr io.Writer) error {
	if len(args) < 3 || args[1] != "discord" || (args[0] != "setup" && args[0] != "status") {
		return errors.New("usage: hctl channel <setup|status> discord AGENT [--profile NAME] [--config PATH]")
	}
	fs := flag.NewFlagSet("channel "+args[0]+" discord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profileName := fs.String("profile", "", "Discord runtime profile")
	configPath := fs.String("config", "", "hctl configuration path")
	if err := fs.Parse(args[3:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected channel setup arguments")
	}
	path, err := channelconfig.SelectedPath(*configPath)
	if err != nil {
		return err
	}
	config, err := channelconfig.Load(path, args[0] == "setup")
	if err != nil {
		return err
	}
	if args[0] == "setup" {
		return setupDiscord(args[2], *profileName, path, config, input, output)
	}
	p, err := project.Load(args[2], "codex")
	if err != nil {
		return err
	}
	if p.DiscordChannel == nil {
		return errors.New("agent project does not define channels/discord.md")
	}
	name, profile, err := channelconfig.Resolve(config, p.AgentID, *profileName)
	if err != nil {
		return err
	}
	token, err := credential.Resolve(credential.OSStore{}, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	identity, err := discord.ValidateIdentity(ctx, token)
	if err != nil {
		return err
	}
	if err := discord.ValidateProfile(identity, profile); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Discord profile %s is valid for agent %s.\n", name, p.Name)
	return err
}

func runMCP(args []string, input io.Reader, output, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "serve" {
		return errors.New("usage: hctl mcp serve AGENT [--workspace DIR] --harness <claude|codex>")
	}
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	workspace := fs.String("workspace", "", "target workspace (defaults to AGENT)")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*harnessName != "claude" && *harnessName != "codex") {
		return errors.New("usage: hctl mcp serve AGENT [--workspace DIR] --harness <claude|codex>")
	}
	return mcp.Serve(args[1], *workspace, *harnessName, input, output, stderr)
}

func runApply(args []string, output, stderr io.Writer, self string) error {
	if len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, "Usage: hctl apply AGENT [--workspace DIR] --harness <claude|codex> [--command PATH]\n")
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: hctl apply AGENT --harness <claude|codex>")
	}
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	workspace := fs.String("workspace", "", "target workspace (defaults to AGENT)")
	command := fs.String("command", "", "harness executable override")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected apply arguments")
	}
	p, err := project.Load(args[0], *harnessName, *workspace)
	if err != nil {
		return err
	}
	driver, err := newDriver(*harnessName, *command)
	if err != nil {
		return err
	}
	if err := driver.Verify(context.Background()); err != nil {
		return err
	}
	prepareContext, cancelPrepare := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelPrepare()
	if err := tool.Prepare(prepareContext, p.SourceRoot, p.WorkspaceRoot, p.SourceFingerprint, p.Tools); err != nil {
		return err
	}
	self, err = resolvedSelf(self)
	if err != nil {
		return err
	}
	result, err := setup.Apply(p, self)
	if err != nil {
		return err
	}
	for _, diagnostic := range result.Diagnostics {
		if _, err := fmt.Fprintln(stderr, diagnostic.String()); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(output, "applied agent=%s harness=%s fingerprint=%s\n", p.Name, driver.Name(), p.SourceFingerprint); err != nil {
		return err
	}
	for _, path := range result.Files {
		if _, err := fmt.Fprintln(output, "generated", path); err != nil {
			return err
		}
	}
	toolNames := []string{"echo"}
	if p.GitHubConnection != nil {
		toolNames = append(toolNames, github.GetRepository, github.ListIssues, github.GetIssue)
	}
	for _, source := range p.Tools.Sources {
		toolNames = append(toolNames, source.Name)
	}
	if _, err := fmt.Fprintf(output, "managed tools=%s via MCP; native harness tools allowed and unmanaged\n", strings.Join(toolNames, ",")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "next: cd %s && %s\n", p.WorkspaceRoot, driver.Name()); err != nil {
		return err
	}
	if driver.Name() == "codex" {
		if _, err := fmt.Fprintln(output, "note: Codex loads project .codex configuration after you trust the project"); err != nil {
			return err
		}
	}
	return nil
}

func runAgent(args []string, input io.Reader, output, stderr io.Writer, self string) error {
	if len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, "Usage: hctl run AGENT [--workspace DIR] --harness <claude|codex> [--input channels|jsonl] [--profile NAME] [--config PATH] [--conversation ID] [--command PATH] [--timeout DURATION] [--turn-timeout DURATION] [--idle-timeout DURATION] [--max-resident-sessions N] [--max-active-turns N]\n")
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: hctl run AGENT --harness <claude|codex>")
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	workspace := fs.String("workspace", "", "target workspace (defaults to AGENT)")
	conversation := fs.String("conversation", "local", "stable local conversation id")
	command := fs.String("command", "", "harness executable override")
	inputMode := fs.String("input", "channels", "input mode: channels or jsonl")
	profileName := fs.String("profile", "", "Discord runtime profile")
	configPath := fs.String("config", "", "hctl configuration path")
	timeout := fs.Duration("timeout", 2*time.Minute, "bounded run process lifetime")
	turnTimeout := fs.Duration("turn-timeout", 2*time.Minute, "bounded channel turn lifetime")
	idleTimeout := fs.Duration("idle-timeout", dispatch.DefaultIdleTimeout, "idle channel session lifetime")
	maxResident := fs.Int("max-resident-sessions", dispatch.DefaultMaxResidentSessions, "maximum resident channel harness sessions")
	maxActive := fs.Int("max-active-turns", dispatch.DefaultMaxActiveTurns, "maximum simultaneous channel turns")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected run arguments")
	}
	if (*inputMode != "channels" && *inputMode != "jsonl") || *timeout <= 0 || *timeout > 30*time.Minute || *turnTimeout <= 0 || *turnTimeout > 30*time.Minute || *idleTimeout <= 0 || *idleTimeout > 24*time.Hour || *maxResident <= 0 || *maxResident > 64 || *maxActive <= 0 || *maxActive > *maxResident {
		return errors.New("run mode, timeout, or capacity limit is invalid")
	}
	if err := dispatch.ValidateConversation(*conversation); err != nil {
		return err
	}
	p, err := project.Load(args[0], *harnessName, *workspace)
	if err != nil {
		return err
	}
	driver, err := newDriver(*harnessName, *command)
	if err != nil {
		return err
	}
	if err := driver.Verify(context.Background()); err != nil {
		return err
	}
	if err := ensureApplied(p, self, stderr); err != nil {
		return err
	}
	if *inputMode == "jsonl" {
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		return dispatch.Run(ctx, p, driver, *conversation, input, output)
	}
	if p.DiscordChannel == nil {
		return errors.New("agent has no configured channels; use --input jsonl or add channels/discord.md")
	}
	path, err := channelconfig.SelectedPath(*configPath)
	if err != nil {
		return err
	}
	config, err := channelconfig.Load(path, false)
	if err != nil {
		return err
	}
	name, profile, err := channelconfig.Resolve(config, p.AgentID, *profileName)
	if err != nil {
		return err
	}
	token, err := credential.Resolve(credential.OSStore{}, name)
	if err != nil {
		return err
	}
	hctlExecutable, err := resolvedSelf(self)
	if err != nil {
		return err
	}
	runtime, err := discord.New(p, driver, discord.Config{Profile: name, Runtime: profile, Token: token, TurnTimeout: *turnTimeout, IdleTimeout: *idleTimeout, MaxResident: *maxResident, MaxActive: *maxActive, Audit: stderr, Executable: hctlExecutable})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runtime.Run(ctx)
}

func ensureApplied(p *project.Project, self string, stderr io.Writer) error {
	if err := setup.Verify(p); err == nil {
		return nil
	}
	prepareContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := tool.Prepare(prepareContext, p.SourceRoot, p.WorkspaceRoot, p.SourceFingerprint, p.Tools); err != nil {
		return err
	}
	executable, err := resolvedSelf(self)
	if err != nil {
		return err
	}
	result, err := setup.Apply(p, executable)
	if err != nil {
		return err
	}
	for _, diagnostic := range result.Diagnostics {
		_, _ = fmt.Fprintln(stderr, diagnostic.String())
	}
	_, _ = fmt.Fprintf(stderr, "auto-applied agent=%s harness=%s fingerprint=%s\n", p.Name, p.Harness, p.SourceFingerprint)
	return nil
}

func setupDiscord(source, requestedProfile, path string, config channelconfig.Config, input io.Reader, output io.Writer) error {
	root, err := rootfs.CanonicalDir(source)
	if err != nil {
		return err
	}
	channelPath := filepath.Join(root, "channels", "discord.md")
	_, channelErr := os.Lstat(channelPath)
	channelMissing := errors.Is(channelErr, os.ErrNotExist)
	if channelErr != nil && !channelMissing {
		return errors.New("cannot inspect Discord channel declaration")
	}
	p, err := project.Load(root, "codex")
	if err != nil {
		return err
	}
	reader := bufio.NewReader(input)
	profileName := requestedProfile
	if profileName == "" {
		profileName = "default"
	}
	if _, err := fmt.Fprint(output, "Discord bot token: "); err != nil {
		return err
	}
	var token string
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		secret, readErr := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(output)
		if readErr != nil {
			return errors.New("cannot read Discord bot token")
		}
		token = string(secret)
	} else {
		token, err = reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return errors.New("cannot read Discord bot token")
		}
	}
	token = strings.TrimSpace(token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	identity, err := discord.ValidateIdentity(ctx, token)
	if err != nil {
		return err
	}
	invite := fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=274877975552", identity.ApplicationID)
	if _, err := fmt.Fprintf(output, "\nInstall the bot in the target server: %s\nEnable Message Content Intent in the Discord Developer Portal, then enter the authorized scope below.\n", invite); err != nil {
		return err
	}
	readID := func(label string) (string, error) {
		if _, err := fmt.Fprint(output, label+": "); err != nil {
			return "", err
		}
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value = strings.TrimSpace(value)
		if !channelconfig.Snowflake(value) {
			return "", fmt.Errorf("%s must be a Discord snowflake", label)
		}
		return value, nil
	}
	allowedUser, err := readID("Authorized user ID")
	if err != nil {
		return err
	}
	allowedGuild, err := readID("Guild ID")
	if err != nil {
		return err
	}
	allowedChannel, err := readID("Channel ID")
	if err != nil {
		return err
	}
	profile := channelconfig.Profile{ApplicationID: identity.ApplicationID, BotUserID: identity.BotUserID, AllowedUserID: allowedUser, AllowedGuildID: allowedGuild, AllowedChannelID: allowedChannel}
	if err := discord.ValidateScope(ctx, token, profile); err != nil {
		return err
	}
	if channelMissing {
		defaultSource := []byte("---\nmode: ambient\n---\n\nRespond conversationally to greetings, direct questions, follow-ups, and messages within this agent's responsibilities. Stay silent only during clearly unrelated conversation when no contribution is useful.\n")
		if err := rootfs.WriteAtomic(root, "channels/discord.md", defaultSource, 0o644); err != nil {
			return err
		}
	}
	if config.Discord.Profiles == nil {
		config.Discord.Profiles = map[string]channelconfig.Profile{}
	}
	if config.AgentProfiles == nil {
		config.AgentProfiles = map[string]string{}
	}
	config.SchemaVersion = 1
	config.Discord.Profiles[profileName] = profile
	if config.Discord.DefaultProfile == "" || profileName == "default" {
		config.Discord.DefaultProfile = profileName
	}
	config.AgentProfiles[p.AgentID] = profileName
	store := credential.OSStore{}
	if err := store.Set(profileName, token); err != nil {
		return err
	}
	if err := channelconfig.Save(path, config); err != nil {
		_ = store.Delete(profileName)
		return err
	}
	_, err = fmt.Fprintf(output, "\nSaved Discord profile %s for agent %s. Run hctl run to connect it.\n", profileName, p.Name)
	return err
}

func newDriver(name, override string) (harness.Driver, error) {
	if name != "claude" && name != "codex" {
		return nil, errors.New("--harness must be claude or codex")
	}
	executable, err := harness.ResolveExecutable(name, override)
	if err != nil {
		return nil, err
	}
	if name == "claude" {
		return claude.New(executable), nil
	}
	return codex.New(executable), nil
}

func resolvedSelf(path string) (string, error) {
	if path == "" {
		var err error
		path, err = os.Executable()
		if err != nil {
			return "", errors.New("cannot resolve hctl executable")
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("cannot resolve hctl executable")
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("hctl executable must be a regular file")
	}
	return abs, nil
}

func isHelp(value string) bool { return value == "--help" || value == "-h" }
