package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"hctl/internal/gateway"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/harness/codex"
	"hctl/internal/mcp"
	"hctl/internal/project"
	"hctl/internal/projection"
)

const help = `Usage: hctl <command> [arguments]

Commands:
  apply AGENT --harness <claude|codex>    Generate and validate native files
  gateway AGENT --harness <claude|codex>  Run a headless JSONL gateway

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
	case "mcp":
		if len(args) != 3 || args[1] != "serve" {
			return errors.New("usage: hctl mcp serve AGENT")
		}
		return mcp.Serve(args[2], input, output, stderr)
	default:
		return fmt.Errorf("unknown command %q; expected apply or gateway", args[0])
	}
}

func runApply(args []string, output, stderr io.Writer, self string) error {
	if len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, "Usage: hctl apply AGENT --harness <claude|codex> [--command PATH]\n")
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: hctl apply AGENT --harness <claude|codex>")
	}
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	command := fs.String("command", "", "harness executable override")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected apply arguments")
	}
	p, err := project.Load(args[0], *harnessName)
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
	self, err = resolvedSelf(self)
	if err != nil {
		return err
	}
	files, err := projection.Apply(p, self)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "applied agent=%s harness=%s fingerprint=%s\n", p.Config.Name, driver.Name(), p.Manifest.SourceFingerprint); err != nil {
		return err
	}
	for _, path := range files {
		if _, err := fmt.Fprintln(output, "generated", path); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(output, "managed echo via MCP; native harness capabilities allowed and unmanaged"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "next: cd %s && %s\n", p.Root, driver.Name()); err != nil {
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
		_, err := io.WriteString(output, "Usage: hctl gateway AGENT --harness <claude|codex> [--conversation ID] [--command PATH] [--timeout DURATION]\n")
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: hctl gateway AGENT --harness <claude|codex>")
	}
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
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
	p, err := project.Load(args[0], *harnessName)
	if err != nil {
		return err
	}
	if err := projection.Verify(p); err != nil {
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
