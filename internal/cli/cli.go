package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hctl/internal/channel/discord"
	"hctl/internal/connection/github"
	"hctl/internal/gateway"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/harness/codex"
	"hctl/internal/mcp"
	"hctl/internal/project"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

const help = `Usage: hctl <command> [arguments]

Commands:
  apply AGENT --harness <claude|codex>    Prepare tools and native files
  gateway AGENT --harness <claude|codex>  Run a headless JSONL gateway
  channel discord AGENT [options]         Run signed Discord Interactions

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
	case "gateway":
		return runGateway(args[1:], input, output, stderr)
	case "channel":
		return runChannel(args[1:], output, stderr)
	case "mcp":
		return runMCP(args[1:], input, output, stderr)
	default:
		return fmt.Errorf("unknown command %q; expected apply, gateway, or channel", args[0])
	}
}

func runChannel(args []string, output, stderr io.Writer) error {
	if len(args) > 0 && isHelp(args[len(args)-1]) {
		_, err := io.WriteString(output, "Usage: hctl channel discord AGENT [--workspace DIR] --harness <claude|codex> --application-id ID --public-key HEX --allowed-user ID [--conversation ID] [--listen 127.0.0.1:PORT] [--command PATH]\n")
		return err
	}
	if len(args) < 2 || args[0] != "discord" {
		return errors.New("usage: hctl channel discord AGENT --harness <claude|codex> --application-id ID --public-key HEX --allowed-user ID")
	}
	fs := flag.NewFlagSet("channel discord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	workspace := fs.String("workspace", "", "target workspace (defaults to AGENT)")
	conversation := fs.String("conversation", "", "conversation id (defaults to this Discord application and user)")
	command := fs.String("command", "", "harness executable override")
	applicationID := fs.String("application-id", "", "Discord application ID")
	publicKeyValue := fs.String("public-key", "", "Discord Ed25519 public key")
	allowedUser := fs.String("allowed-user", "", "only Discord user allowed to submit commands")
	listen := fs.String("listen", discord.DefaultListen, "loopback listen address")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected channel arguments")
	}
	publicKey, err := discord.ParsePublicKey(*publicKeyValue)
	if err != nil {
		return err
	}
	config := discord.Config{ApplicationID: *applicationID, AllowedUserID: *allowedUser, PublicKey: publicKey, Listen: *listen, Path: discord.DefaultPath}
	if err := discord.ValidateRuntime(config); err != nil {
		return err
	}
	if *conversation == "" {
		*conversation = discord.DefaultConversation(*applicationID, *allowedUser)
	}
	if err := gateway.ValidateConversation(*conversation); err != nil {
		return err
	}
	p, err := project.Load(args[1], *harnessName, *workspace)
	if err != nil {
		return err
	}
	if p.DiscordChannel == nil {
		return errors.New("agent project does not define channels/discord.md")
	}
	if err := setup.Verify(p); err != nil {
		return err
	}
	driver, err := newDriver(*harnessName, *command)
	if err != nil {
		return err
	}
	if err := driver.Verify(context.Background()); err != nil {
		return err
	}
	config.Audit = stderr
	return discord.Run(context.Background(), p, driver, *conversation, config)
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

func runGateway(args []string, input io.Reader, output, stderr io.Writer) error {
	if len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, "Usage: hctl gateway AGENT [--workspace DIR] --harness <claude|codex> [--conversation ID] [--command PATH] [--timeout DURATION]\n")
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: hctl gateway AGENT --harness <claude|codex>")
	}
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	workspace := fs.String("workspace", "", "target workspace (defaults to AGENT)")
	conversation := fs.String("conversation", "local", "stable local conversation id")
	command := fs.String("command", "", "harness executable override")
	timeout := fs.Duration("timeout", 2*time.Minute, "bounded gateway process lifetime")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected gateway arguments")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 30m")
	}
	if err := gateway.ValidateConversation(*conversation); err != nil {
		return err
	}
	p, err := project.Load(args[0], *harnessName, *workspace)
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
	return gateway.Run(ctx, p, driver, *conversation, input, output)
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
