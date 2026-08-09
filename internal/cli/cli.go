package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"hctl/internal/channel/adapterhost"
	"hctl/internal/channelconfig"
	"hctl/internal/channelselection"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/harness/codex"
	"hctl/internal/integration"
	"hctl/internal/interaction"
	"hctl/internal/mcp"
	"hctl/internal/project"
	"hctl/internal/rootfs"
	"hctl/internal/schedule"
	"hctl/internal/setup"
	"hctl/internal/stage"
	"hctl/internal/tool"
	"hctl/internal/version"
)

const help = `Usage: hctl <command> [arguments]

Commands:
  version                                 Print the hctl build version
  apply AGENT --harness <claude|codex>    Prepare tools and native files
  stage AGENT --harness <claude|codex>    Prepare a runnable filesystem tree
  integration <command>                   Manage external integration packages
  connection <add|status|remove>          Manage authored native MCP connections
  run AGENT --harness <claude|codex>      Run configured conversational channels
  channel <setup|status|remove> discord AGENT
                                          Manage the installed Discord adapter
  schedule trigger AGENT NAME [options]   Run one scheduled occurrence
  schedule run AGENT [options]            Run schedules from a foreground clock

Run "hctl <command> --help" for command details.
`

func Run(args []string, input io.Reader, output, stderr io.Writer, self string) error {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		_, err := fmt.Fprintf(output, "hctl %s\n", version.Value)
		return err
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(output, help)
		return err
	}
	switch args[0] {
	case "apply":
		return runApply(args[1:], output, stderr, self)
	case "stage":
		return runStage(args[1:], output, stderr, self)
	case "integration":
		return runIntegration(args[1:], output, stderr)
	case "connection":
		return runConnection(args[1:], output, stderr)
	case "run":
		return runAgent(args[1:], input, output, stderr, self)
	case "channel":
		return runChannel(args[1:], input, output, stderr, self)
	case "schedule":
		return runSchedule(args[1:], output, stderr, self)
	case "mcp":
		return runMCP(args[1:], input, output, stderr)
	case "hook":
		return runHook(args[1:], input, output)
	default:
		return fmt.Errorf("unknown command %q; expected version, apply, stage, integration, connection, run, channel, or schedule", args[0])
	}
}

func runConnection(args []string, output, stderr io.Writer) error {
	const usage = `Usage:
  hctl connection add AGENT NAME --package PACKAGE --capability CAPABILITY [--context TEXT]
  hctl connection add AGENT NAME --url HTTPS_URL [--context TEXT]
  hctl connection status AGENT [NAME]
  hctl connection remove AGENT NAME
`
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, usage)
		return err
	}
	switch args[0] {
	case "add":
		if len(args) < 3 {
			return errors.New("usage: hctl connection add AGENT NAME (--package PACKAGE --capability CAPABILITY | --url HTTPS_URL) [--context TEXT]")
		}
		fs := flag.NewFlagSet("connection add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		packageID := fs.String("package", "", "installed integration package id")
		capabilityID := fs.String("capability", "", "installed native-mcp capability id")
		endpoint := fs.String("url", "", "credential-free HTTPS Streamable HTTP endpoint")
		contextText := fs.String("context", "", "optional model-facing Markdown context")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected connection add arguments")
		}
		installed := *packageID != "" || *capabilityID != ""
		if installed == (*endpoint != "") || installed && (*packageID == "" || *capabilityID == "") {
			return errors.New("connection add requires exactly package plus capability or one URL")
		}
		root, existing, err := project.LoadConnections(args[1])
		if err != nil {
			return err
		}
		if len(existing) == 128 {
			return errors.New("connections may contain at most 128 entries")
		}
		if err := project.ValidateConnectionNameAvailable(root, args[2]); err != nil {
			return err
		}
		var connection project.Connection
		if installed {
			connection, err = project.NewInstalledConnection(args[2], *packageID, *capabilityID, *contextText)
			if err == nil {
				store, storeErr := integration.NewDefaultStore()
				if storeErr != nil {
					return storeErr
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				resolved, resolveErr := store.ResolveNativeMCP(ctx, connection.Package, connection.Capability)
				if resolveErr != nil {
					return fmt.Errorf("%s: %w", connection.Path, resolveErr)
				}
				if resolved.Selection.Capability.ServerName != connection.Name {
					return fmt.Errorf("%s: installed native-mcp server name %q must equal connection name %q", connection.Path, resolved.Selection.Capability.ServerName, connection.Name)
				}
			}
		} else {
			connection, err = project.NewRemoteConnection(args[2], *endpoint, *contextText)
		}
		if err != nil {
			return err
		}
		if err := rootfs.WriteAtomicExclusive(root, connection.Path, connection.Source, 0o644); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "added connection=%s source=%s\n", connection.Name, connection.Path); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "next: hctl apply %s --harness claude\n", args[1]); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "next: hctl apply %s --harness codex\n", args[1])
		return err
	case "status":
		if len(args) != 2 && len(args) != 3 {
			return errors.New("usage: hctl connection status AGENT [NAME]")
		}
		var connections []project.Connection
		if len(args) == 3 {
			_, connection, err := project.LoadConnection(args[1], args[2])
			if err != nil {
				return err
			}
			connections = []project.Connection{connection}
		} else {
			_, loaded, err := project.LoadConnections(args[1])
			if err != nil {
				return err
			}
			connections = loaded
		}
		if len(connections) == 0 {
			_, err := fmt.Fprintln(output, "no connections configured")
			return err
		}
		return printConnectionStatus(connections, output)
	case "remove":
		if len(args) != 3 {
			return errors.New("usage: hctl connection remove AGENT NAME")
		}
		root, err := project.AgentRoot(args[1])
		if err != nil {
			return err
		}
		if !project.ValidConnectionName(args[2]) {
			return errors.New("connection name must match ^[a-z][a-z0-9_-]{0,63}$ and must not be reserved")
		}
		path := "connections/" + args[2] + ".md"
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s does not exist", path)
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s must be a real regular file without symlinks", path)
		}
		if err := rootfs.RemoveRegular(root, path); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "removed connection=%s source=%s\n", args[2], path); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "next: hctl apply %s --harness claude\n", args[1]); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "next: hctl apply %s --harness codex\n", args[1])
		return err
	default:
		return fmt.Errorf("unknown connection command %q; expected add, status, or remove", args[0])
	}
}

func printConnectionStatus(connections []project.Connection, output io.Writer) error {
	var store *integration.Store
	var firstError error
	for _, connection := range connections {
		contextState := "absent"
		if connection.Context != "" {
			contextState = "present"
		}
		if connection.Remote() {
			if _, err := fmt.Fprintf(output, "connection=%s target=remote transport=streamable-http url=%s status=configured runtime=unchecked context=%s\n", connection.Name, connection.URL, contextState); err != nil {
				return err
			}
			continue
		}
		if store == nil {
			var err error
			store, err = integration.NewDefaultStore()
			if err != nil {
				return err
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resolved, err := store.ResolveNativeMCP(ctx, connection.Package, connection.Capability)
		cancel()
		if err != nil {
			if _, writeErr := fmt.Fprintf(output, "connection=%s target=installed package=%s capability=%s status=unhealthy context=%s\n", connection.Name, connection.Package, connection.Capability, contextState); writeErr != nil {
				return writeErr
			}
			if firstError == nil {
				firstError = fmt.Errorf("%s: %w", connection.Path, err)
			}
			continue
		}
		if resolved.Selection.Capability.ServerName != connection.Name {
			if _, writeErr := fmt.Fprintf(output, "connection=%s target=installed package=%s capability=%s status=unhealthy context=%s\n", connection.Name, connection.Package, connection.Capability, contextState); writeErr != nil {
				return writeErr
			}
			if firstError == nil {
				firstError = fmt.Errorf("%s: installed native-mcp server name %q must equal connection name %q", connection.Path, resolved.Selection.Capability.ServerName, connection.Name)
			}
			continue
		}
		harnesses := make([]string, len(resolved.Selection.Capability.Harnesses))
		for index, target := range resolved.Selection.Capability.Harnesses {
			harnesses[index] = target.Name
		}
		slices.Sort(harnesses)
		if _, err := fmt.Fprintf(output, "connection=%s target=installed package=%s capability=%s status=ready harnesses=%s context=%s\n", connection.Name, connection.Package, connection.Capability, strings.Join(harnesses, ","), contextState); err != nil {
			return err
		}
	}
	return firstError
}

func runIntegration(args []string, output, stderr io.Writer) error {
	const usage = `Usage:
  hctl integration install SOURCE --trust operator
  hctl integration update ID SOURCE --trust operator
  hctl integration inspect ID
  hctl integration verify ID
  hctl integration list
  hctl integration enable ID
  hctl integration disable ID
  hctl integration remove ID
`
	if len(args) == 0 || len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, usage)
		return err
	}
	store, err := integration.NewDefaultStore()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	switch args[0] {
	case "install":
		if len(args) < 2 {
			return errors.New("usage: hctl integration install SOURCE --trust operator")
		}
		fs := flag.NewFlagSet("integration install", flag.ContinueOnError)
		fs.SetOutput(stderr)
		trust := fs.String("trust", "", "explicit package trust owner")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected integration install arguments")
		}
		installed, err := store.Install(ctx, integration.InstallOptions{Source: args[1], Trust: integration.InstallationTrust(*trust)})
		if err != nil {
			return err
		}
		return printIntegrationSummary(output, "installed", installed)
	case "update":
		if len(args) < 3 {
			return errors.New("usage: hctl integration update ID SOURCE --trust operator")
		}
		fs := flag.NewFlagSet("integration update", flag.ContinueOnError)
		fs.SetOutput(stderr)
		trust := fs.String("trust", "", "explicit package trust owner")
		if err := fs.Parse(args[3:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return errors.New("unexpected integration update arguments")
		}
		installed, err := store.Install(ctx, integration.InstallOptions{Source: args[2], Trust: integration.InstallationTrust(*trust), UpdatePackageID: args[1]})
		if err != nil {
			return err
		}
		return printIntegrationSummary(output, "updated", installed)
	case "list":
		if len(args) != 1 {
			return errors.New("usage: hctl integration list")
		}
		entries, err := store.List(ctx)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			_, err = fmt.Fprintln(output, "no integration packages installed")
			return err
		}
		for _, entry := range entries {
			if err := printIntegrationSummary(output, "integration", entry); err != nil {
				return err
			}
		}
		return nil
	case "inspect":
		if len(args) != 2 {
			return errors.New("usage: hctl integration inspect ID")
		}
		entry, err := store.Inspect(ctx, args[1])
		if err != nil {
			return err
		}
		consumers, err := store.Consumers(ctx, args[1])
		if err != nil {
			return err
		}
		return printIntegrationInspect(output, entry, consumers)
	case "verify":
		if len(args) != 2 {
			return errors.New("usage: hctl integration verify ID")
		}
		entry, err := store.Verify(ctx, args[1])
		if err != nil {
			return err
		}
		return printIntegrationSummary(output, "verified", entry)
	case "enable", "disable":
		if len(args) != 2 {
			return fmt.Errorf("usage: hctl integration %s ID", args[0])
		}
		enabled := args[0] == "enable"
		if err := store.SetEnabled(ctx, args[1], enabled); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "%sd integration=%s\n", args[0], args[1])
		return err
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: hctl integration remove ID")
		}
		if err := store.Remove(ctx, args[1]); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "removed integration=%s; shared immutable cache retained\n", args[1])
		return err
	default:
		return fmt.Errorf("unknown integration command %q; expected install, update, inspect, verify, list, enable, disable, or remove", args[0])
	}
}

func printIntegrationSummary(output io.Writer, action string, entry integration.Installed) error {
	manifest := entry.Package.Manifest()
	_, err := fmt.Fprintf(output, "%s integration=%s version=%s manifest=%s trust=%s enabled=%t verified_platform_artifacts=%d\n", action, manifest.ID, manifest.Version, entry.Package.Identity(), entry.State.Trust, entry.State.Enabled, len(entry.State.Artifacts))
	return err
}

func printIntegrationInspect(output io.Writer, entry integration.Installed, consumers []integration.Consumption) error {
	manifest := entry.Package.Manifest()
	if err := printIntegrationSummary(output, "integration", entry); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "name=%s\ndescription=%s\nlicense=%s\nprovenance=%s revision=%s\ncompatibility=[%s,%s)\ninstalled_verification=exact-manifest-artifact-executable\noffline_status=run-hctl-integration-verify\n", manifest.Name, manifest.Description, manifest.License, manifest.Provenance.Source, manifest.Provenance.Revision, manifest.Compatibility.Minimum, manifest.Compatibility.Before); err != nil {
		return err
	}
	for _, artifact := range manifest.Artifacts {
		if _, err := fmt.Fprintf(output, "artifact=%s platform=%s/%s format=%s sha256=%s executable=%s executable_sha256=%s\n", artifact.ID, artifact.OS, artifact.Architecture, artifact.Format, artifact.SHA256, artifact.Executable.Path, artifact.Executable.SHA256); err != nil {
			return err
		}
	}
	for _, capability := range manifest.Capabilities {
		if _, err := fmt.Fprintf(output, "capability=%s type=%s version=%d\n", capability.ID, capability.Type, capability.Version); err != nil {
			return err
		}
		if capability.NativeMCP != nil {
			for _, required := range capability.NativeMCP.RequiredEnvironment {
				if _, err := fmt.Fprintf(output, "required_environment=%s description=%s value=not-read\n", required.Name, required.Description); err != nil {
					return err
				}
			}
		}
	}
	if len(consumers) == 0 {
		_, err := fmt.Fprintln(output, "consuming_agents=none")
		return err
	}
	for _, consumer := range consumers {
		if _, err := fmt.Fprintf(output, "consuming_agent=%s id=%s capabilities=%s\n", consumer.AgentName, consumer.AgentID, strings.Join(consumer.Capabilities, ",")); err != nil {
			return err
		}
	}
	return nil
}

func runHook(args []string, input io.Reader, output io.Writer) error {
	if len(args) != 1 || args[0] != "claude-deferred-input" {
		return errors.New("unsupported internal hook")
	}
	return claude.RunDeferredHook(input, output, os.Getenv(claude.DeferredBrokerEnv))
}

func runSchedule(args []string, output, stderr io.Writer, self string) error {
	const usage = "Usage:\n  hctl schedule trigger AGENT NAME [--workspace DIR] --harness <claude|codex> --input-id ID [--command PATH] [--timeout DURATION] [--turn-timeout DURATION]\n  hctl schedule run AGENT [--workspace DIR] --harness <claude|codex> [--command PATH] [--turn-timeout DURATION] [--max-active-turns N]\n"
	if len(args) > 0 && isHelp(args[len(args)-1]) {
		_, err := io.WriteString(output, usage)
		return err
	}
	if len(args) >= 1 && args[0] == "run" {
		return runScheduleClock(args[1:], output, stderr, self)
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
	turnTimeout := fs.Duration("turn-timeout", 90*time.Second, "bounded task turn lifetime")
	if err := fs.Parse(args[3:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected schedule arguments")
	}
	if *timeout <= 0 || *timeout > 30*time.Minute {
		return errors.New("--timeout must be greater than zero and at most 30m")
	}
	if *turnTimeout <= 0 || *turnTimeout > 30*time.Minute {
		return errors.New("--turn-timeout must be greater than zero and at most 30m")
	}
	if err := dispatch.ValidateInputID(*inputID); err != nil {
		return err
	}
	p, err := project.Load(args[1], *harnessName, *workspace)
	if err != nil {
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
	if err := ensureApplied(p, self, stderr); err != nil {
		return err
	}
	currentDriver := newCurrentSetupDriver(driver, p, self, stderr)
	result, triggerErr := schedule.TriggerWithTurnTimeout(ctx, p, currentDriver, args[2], *inputID, *turnTimeout)
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
		if result.Reason != "" {
			if _, err := fmt.Fprintf(output, " reason=%s", result.Reason); err != nil {
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

func runScheduleClock(args []string, output, stderr io.Writer, self string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runScheduleClockContext(ctx, args, output, stderr, self, nil)
}

func runScheduleClockContext(ctx context.Context, args []string, output, stderr io.Writer, self string, clock schedule.Clock) error {
	if len(args) == 0 {
		return errors.New("usage: hctl schedule run AGENT --harness <claude|codex>")
	}
	fs := flag.NewFlagSet("schedule run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	workspace := fs.String("workspace", "", "target workspace (defaults to AGENT)")
	command := fs.String("command", "", "harness executable override")
	turnTimeout := fs.Duration("turn-timeout", 90*time.Second, "bounded task turn lifetime")
	maxActive := fs.Int("max-active-turns", dispatch.DefaultMaxActiveTurns, "maximum simultaneous task turns")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected schedule run arguments")
	}
	if *turnTimeout <= 0 || *turnTimeout > 30*time.Minute {
		return errors.New("--turn-timeout must be greater than zero and at most 30m")
	}
	if *maxActive <= 0 || *maxActive > 64 {
		return errors.New("--max-active-turns must be between 1 and 64")
	}
	p, err := project.Load(args[0], *harnessName, *workspace)
	if err != nil {
		return err
	}
	if len(p.Schedules) == 0 {
		return errors.New("agent project defines no schedules")
	}
	driver, err := newDriver(*harnessName, *command)
	if err != nil {
		return err
	}
	verifyCtx, cancelVerify := context.WithTimeout(ctx, 30*time.Second)
	defer cancelVerify()
	if err := driver.Verify(verifyCtx); err != nil {
		return err
	}
	if err := ensureApplied(p, self, stderr); err != nil {
		return err
	}
	lock, err := schedule.AcquireRuntimeLock(p)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	currentDriver := newCurrentSetupDriver(driver, p, self, stderr)
	runtime, err := dispatch.NewTaskRuntime(p, currentDriver, *turnTimeout, *maxActive)
	if err != nil {
		return err
	}
	defer runtime.Close()
	emit := func(diagnostic schedule.Diagnostic) error {
		if diagnostic.Kind == "runtime" {
			_, err = fmt.Fprintf(output, "schedule_runtime status=%s", diagnostic.Status)
		} else {
			_, err = fmt.Fprintf(output, "schedule=%q occurrence=%s status=%s", diagnostic.Schedule, diagnostic.OccurrenceID, diagnostic.Status)
		}
		if err != nil {
			return err
		}
		if diagnostic.Reason != "" {
			if _, err = fmt.Fprintf(output, " reason=%s", diagnostic.Reason); err != nil {
				return err
			}
		}
		_, err = fmt.Fprintln(output)
		return err
	}
	return schedule.RunForeground(ctx, p.Schedules, runtime, schedule.RunnerOptions{Clock: clock, Emit: emit})
}

type currentSetupDriver struct {
	harness.Driver
	project     *project.Project
	self        string
	diagnostics io.Writer
}

type currentContinuationDriver struct {
	*currentSetupDriver
	continuation harness.ContinuationTurnDriver
}

func (d *currentContinuationDriver) ContinueTurn(ctx context.Context, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	return d.ContinueProjectTurn(ctx, d.project, request, sessionID, intent, emit)
}

func (d *currentContinuationDriver) ContinueProjectTurn(ctx context.Context, p *project.Project, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	if err := ensureAppliedForPolicyContext(ctx, p, d.self, d.diagnostics, request.Policy); err != nil {
		return d.failedContinuation(err)
	}
	return d.continuation.ContinueTurn(ctx, request, sessionID, intent, emit)
}

type currentDeferredDriver struct {
	*currentSetupDriver
	deferred harness.NativeDeferredToolDriver
}

func (d *currentDeferredDriver) ResumeDeferredTool(ctx context.Context, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	return d.ResumeProjectDeferredTool(ctx, d.project, request, sessionID, intent, emit)
}

func (d *currentDeferredDriver) ResumeProjectDeferredTool(ctx context.Context, p *project.Project, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	if err := ensureAppliedForPolicyContext(ctx, p, d.self, d.diagnostics, request.Policy); err != nil {
		return d.failedContinuation(err)
	}
	return d.deferred.ResumeDeferredTool(ctx, request, sessionID, intent, emit)
}

type currentContinuationDeferredDriver struct {
	*currentContinuationDriver
	deferred harness.NativeDeferredToolDriver
}

func (d *currentContinuationDeferredDriver) ResumeDeferredTool(ctx context.Context, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	return d.ResumeProjectDeferredTool(ctx, d.project, request, sessionID, intent, emit)
}

func (d *currentContinuationDeferredDriver) ResumeProjectDeferredTool(ctx context.Context, p *project.Project, request harness.OpenRequest, sessionID string, intent interaction.ContinuationIntent, emit func(harness.Event)) interaction.ContinuationResult {
	if err := ensureAppliedForPolicyContext(ctx, p, d.self, d.diagnostics, request.Policy); err != nil {
		return d.failedContinuation(err)
	}
	return d.deferred.ResumeDeferredTool(ctx, request, sessionID, intent, emit)
}

func newCurrentSetupDriver(driver harness.Driver, p *project.Project, self string, diagnostics io.Writer) harness.Driver {
	current := &currentSetupDriver{Driver: driver, project: p, self: self, diagnostics: diagnostics}
	continuation, hasContinuation := driver.(harness.ContinuationTurnDriver)
	deferred, hasDeferred := driver.(harness.NativeDeferredToolDriver)
	switch {
	case hasContinuation && hasDeferred:
		return &currentContinuationDeferredDriver{currentContinuationDriver: &currentContinuationDriver{currentSetupDriver: current, continuation: continuation}, deferred: deferred}
	case hasContinuation:
		return &currentContinuationDriver{currentSetupDriver: current, continuation: continuation}
	case hasDeferred:
		return &currentDeferredDriver{currentSetupDriver: current, deferred: deferred}
	default:
		return current
	}
}

func (d *currentSetupDriver) Open(ctx context.Context, request harness.OpenRequest) (harness.Session, error) {
	return d.OpenProject(ctx, d.project, request)
}

func (d *currentSetupDriver) failedContinuation(err error) interaction.ContinuationResult {
	_, _ = fmt.Fprintf(d.diagnostics, "native continuation setup failed: %v\n", err)
	return interaction.ContinuationResult{Effect: interaction.EffectFailed, OriginOutcome: "failed"}
}

func (d *currentSetupDriver) OpenProject(ctx context.Context, p *project.Project, request harness.OpenRequest) (harness.Session, error) {
	if err := ensureAppliedForPolicyContext(ctx, p, d.self, d.diagnostics, request.Policy); err != nil {
		return nil, err
	}
	return d.Driver.Open(ctx, request)
}

func runChannel(args []string, input io.Reader, output, stderr io.Writer, self string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runChannelContext(ctx, args, input, output, stderr, self)
}

func runChannelContext(ctx context.Context, args []string, input io.Reader, output, stderr io.Writer, self string) error {
	if len(args) < 3 || args[1] != "discord" || (args[0] != "setup" && args[0] != "status" && args[0] != "remove") {
		return errors.New("usage: hctl channel <setup|status|remove> discord AGENT [--profile NAME] [--config PATH]")
	}
	fs := flag.NewFlagSet("channel "+args[0]+" discord", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profileName := fs.String("profile", "", "Discord runtime profile")
	configPath := fs.String("config", "", "legacy hctl configuration path used only for profile selection")
	if err := fs.Parse(args[3:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected channel setup arguments")
	}
	p, err := project.Load(args[2], "codex")
	if err != nil {
		return err
	}
	profile, err := selectedAdapterProfile(p.AgentID, *profileName, *configPath)
	if err != nil {
		return err
	}
	_, resolved, err := resolveChannelAdapter(ctx, p, self)
	if err != nil {
		return err
	}
	mode := map[string]integration.ChannelAdapterMode{"setup": integration.ChannelAdapterSetup, "status": integration.ChannelAdapterStatus, "remove": integration.ChannelAdapterRemove}[args[0]]
	launch, err := resolved.LaunchDescriptor(mode, profile)
	if err != nil {
		return discordAdapterRemedy(err, args[2])
	}
	result, err := adapterhost.RunOperation(ctx, mode, launch, adapterhost.AdapterEnvironment("HCTL_DISCORD_TOKEN"), input, stderr)
	if err != nil {
		return err
	}
	if result.Operation != args[0] || result.ProfileID != profile {
		return errors.New("channel-adapter operation result does not match the requested operation and profile")
	}
	selections, err := channelselection.DefaultStore()
	if err != nil {
		return err
	}
	switch args[0] {
	case "setup":
		if err := selections.Set(p.AgentID, "discord", profile); err != nil {
			return err
		}
	case "remove":
		if err := selections.Delete(p.AgentID, "discord", profile); err != nil {
			return err
		}
	}
	if args[0] == "setup" && p.DiscordChannel == nil {
		root, rootErr := rootfs.CanonicalDir(args[2])
		if rootErr != nil {
			return rootErr
		}
		defaultSource := []byte("---\nmode: ambient\n---\n\nRespond conversationally to greetings, direct questions, follow-ups, and messages within this agent's responsibilities. Stay silent only during clearly unrelated conversation when no contribution is useful.\n")
		if err := rootfs.WriteAtomic(root, "channels/discord.md", defaultSource, 0o644); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output, "channel=%s operation=%s profile=%s status=%s", launch.ChannelKind, result.Operation, result.ProfileID, result.Status)
	if err != nil {
		return err
	}
	if result.Identity != "" {
		if _, err = fmt.Fprintf(output, " identity=%s", result.Identity); err != nil {
			return err
		}
	}
	if result.Message != "" {
		if _, err = fmt.Fprintf(output, " message=%s", result.Message); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(output)
	return err
}

func selectedAdapterProfile(agentID, explicit, configPath string) (string, error) {
	if explicit != "" {
		if err := integration.ValidateChannelProfileID(explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}
	if ambient := os.Getenv("HCTL_DISCORD_PROFILE"); ambient != "" {
		if err := integration.ValidateChannelProfileID(ambient); err != nil {
			return "", err
		}
		return ambient, nil
	}
	selections, err := channelselection.DefaultStore()
	if err != nil {
		return "", err
	}
	name, err := selections.Get(agentID, "discord")
	if err != nil {
		return "", err
	}
	if name != "" {
		if err := integration.ValidateChannelProfileID(name); err != nil {
			return "", err
		}
		return name, nil
	}
	path, err := channelconfig.SelectedPath(configPath)
	if err != nil {
		return "", err
	}
	config, err := channelconfig.LoadProfileSelection(path, true)
	if err != nil {
		return "", err
	}
	name = config.AgentProfiles[agentID]
	if name == "" {
		name = config.DefaultProfile
	}
	if name == "" {
		name = "default"
	}
	if err := integration.ValidateChannelProfileID(name); err != nil {
		return "", err
	}
	return name, nil
}

func discordAdapterRemedy(err error, agent string) error {
	return fmt.Errorf("discord channel adapter is unavailable: %w; install and enable the exact hctl-discord package, then run hctl channel setup discord %s", err, agent)
}

func resolveChannelAdapter(ctx context.Context, p *project.Project, self string) (*integration.Store, integration.ChannelAdapterResolution, error) {
	if path := stagedChannelAdapterPath(self); path != "" {
		resolved, err := integration.LoadStagedChannelAdapter(path, p.AgentID, p.SourceFingerprint, "discord")
		if err != nil {
			return nil, integration.ChannelAdapterResolution{}, err
		}
		return nil, resolved, nil
	}
	store, err := integration.NewDefaultStore()
	if err != nil {
		return nil, integration.ChannelAdapterResolution{}, err
	}
	resolved, err := store.ResolveChannelAdapter(ctx, "discord")
	if err != nil {
		return nil, integration.ChannelAdapterResolution{}, discordAdapterRemedy(err, p.SourceRoot)
	}
	return store, resolved, nil
}

func recordChannelConsumption(ctx context.Context, p *project.Project, self string) error {
	if p.DiscordChannel == nil {
		return nil
	}
	store, resolved, err := resolveChannelAdapter(ctx, p, self)
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	return store.RecordConsumption(ctx, resolved.Selection.PackageID, p.AgentID, p.Name, []string{resolved.Selection.Capability.ID})
}

func verifyChannelConsumption(ctx context.Context, store *integration.Store, resolved integration.ChannelAdapterResolution, p *project.Project) error {
	if store == nil {
		return nil
	}
	consumers, err := store.Consumers(ctx, resolved.Selection.PackageID)
	if err != nil {
		return err
	}
	for _, consumer := range consumers {
		if consumer.AgentID == p.AgentID && slices.Contains(consumer.Capabilities, resolved.Selection.Capability.ID) {
			return nil
		}
	}
	return errors.New("channel-adapter generated state is stale or missing; run hctl apply again before hctl run")
}

// stagedChannelAdapterPath honors the generated descriptor only when it is at
// the fixed location adjacent to the running staged hctl binary. An ambient
// value cannot redirect a normal direct installation to arbitrary code.
func stagedChannelAdapterPath(self string) string {
	configured := os.Getenv(integration.StagedChannelAdapterEnvironment)
	if configured == "" {
		return ""
	}
	executable, err := resolvedSelf(self)
	if err != nil {
		return ""
	}
	expected, err := filepath.Abs(filepath.Join(filepath.Dir(executable), "..", "integrations", "channel-adapter.json"))
	if err != nil {
		return ""
	}
	configured, err = filepath.Abs(configured)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(configured); err == nil {
		configured = resolved
	}
	if filepath.Clean(configured) != filepath.Clean(expected) {
		return ""
	}
	return expected
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
	if p.DiscordChannel != nil {
		resolveContext, cancelResolve := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelResolve()
		if _, _, err := resolveChannelAdapter(resolveContext, p, self); err != nil {
			return err
		}
	}
	driver, err := newDriver(*harnessName, *command)
	if err != nil {
		return err
	}
	if err := driver.Verify(context.Background()); err != nil {
		return err
	}
	nativeMCP, err := resolveProjectNativeMCP(context.Background(), p)
	if err != nil {
		return err
	}
	if err := setup.ValidateNativeMCP(p, nativeMCP); err != nil {
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
	result, err := setup.ApplyWithNativeMCP(p, self, nativeMCP)
	if err != nil {
		return err
	}
	recordContext, cancelRecord := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRecord()
	if err := recordChannelConsumption(recordContext, p, self); err != nil {
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
	if p.FrictionNotes {
		toolNames = append(toolNames, "record-friction")
	}
	for _, source := range p.Tools.Sources {
		toolNames = append(toolNames, source.Name)
	}
	if _, err := fmt.Fprintf(output, "managed tools=%s via MCP; native harness tools allowed and unmanaged\n", strings.Join(toolNames, ",")); err != nil {
		return err
	}
	installedDescriptors := make(map[string]integration.NativeMCPLaunchDescriptor, len(nativeMCP))
	for _, descriptor := range nativeMCP {
		installedDescriptors[descriptor.ServerName] = descriptor
	}
	for _, connection := range p.Connections {
		target := "transport=streamable-http url=" + connection.URL + " startup=optional trust=native-project runtime=unchecked"
		if connection.Installed() {
			descriptor := installedDescriptors[connection.Name]
			names := make([]string, len(descriptor.RequiredEnvironment))
			for index, requirement := range descriptor.RequiredEnvironment {
				names[index] = requirement.Name
			}
			slices.Sort(names)
			target = "package=" + connection.Package + " capability=" + connection.Capability + " startup=" + string(descriptor.Target.Startup) + " trust=" + string(descriptor.Target.Trust)
			if len(names) > 0 {
				target += " required_environment=" + strings.Join(names, ",") + " value=not-read"
			}
		}
		if _, err := fmt.Fprintf(output, "native server=%s %s; startup, trust, approval, authentication, calls, and effects owned by %s\n", connection.Name, target, driver.Name()); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(output, "next: cd %s && %s\n", p.WorkspaceRoot, driver.Name()); err != nil {
		return err
	}
	if driver.Name() == "codex" {
		if _, err := fmt.Fprintln(output, "note: Codex loads project .codex configuration after you trust the project; Codex owns native server and tool approval"); err != nil {
			return err
		}
	} else if len(p.Connections) > 0 {
		if _, err := fmt.Fprintln(output, "note: Claude owns project MCP server approval and runtime state"); err != nil {
			return err
		}
	}
	return nil
}

func runStage(args []string, output, stderr io.Writer, self string) error {
	if len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, "Usage: hctl stage AGENT --harness <claude|codex> --output DIR [--command PATH]\n")
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: hctl stage AGENT --harness <claude|codex> --output DIR")
	}
	fs := flag.NewFlagSet("stage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	harnessName := fs.String("harness", "", "target harness")
	outputPath := fs.String("output", "", "new staged filesystem directory")
	command := fs.String("command", "", "harness executable override")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected stage arguments")
	}
	p, err := project.Load(args[0], *harnessName)
	if err != nil {
		return err
	}
	driver, err := newDriver(*harnessName, *command)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := driver.Verify(ctx); err != nil {
		return err
	}
	version, err := stage.HarnessVersion(ctx, driver.Executable())
	if err != nil {
		return err
	}
	executable, err := resolvedSelf(self)
	if err != nil {
		return err
	}
	var integrationStore *integration.Store
	if hasInstalledConnection(p) || p.DiscordChannel != nil {
		integrationStore, err = integration.NewDefaultStore()
		if err != nil {
			return err
		}
	}
	result, err := stage.Create(ctx, stage.Request{Project: p, Output: *outputPath, HCTLExecutable: executable, HarnessExecutable: driver.Executable(), HarnessVersion: version, IntegrationStore: integrationStore})
	if err != nil {
		return err
	}
	for _, diagnostic := range result.Diagnostics {
		if _, err := fmt.Fprintln(stderr, diagnostic.String()); err != nil {
			return err
		}
	}
	runtimes := strings.Join(result.Manifest.Runtimes, ",")
	if runtimes == "" {
		runtimes = "none"
	}
	_, err = fmt.Fprintf(output, "staged agent=%s harness=%s fingerprint=%s output=%s runtimes=%s\n", p.Name, driver.Name(), p.SourceFingerprint, result.Output, runtimes)
	return err
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
	name, err := selectedAdapterProfile(p.AgentID, *profileName, *configPath)
	if err != nil {
		return err
	}
	resolveCtx, cancelResolve := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelResolve()
	store, resolved, err := resolveChannelAdapter(resolveCtx, p, self)
	if err != nil {
		return err
	}
	if err := verifyChannelConsumption(resolveCtx, store, resolved, p); err != nil {
		return err
	}
	launch, err := resolved.LaunchDescriptor(integration.ChannelAdapterRuntime, "")
	if err != nil {
		return discordAdapterRemedy(err, args[0])
	}
	hctlExecutable, err := resolvedSelf(self)
	if err != nil {
		return err
	}
	currentDriver := newCurrentSetupDriver(driver, p, self, stderr)
	runtime, err := adapterhost.New(adapterhost.Config{Project: p, Driver: currentDriver, Launch: launch, ProfileID: name, Environment: adapterhost.AdapterEnvironment("HCTL_DISCORD_TOKEN"), TurnTimeout: *turnTimeout, IdleTimeout: *idleTimeout, MaxResident: *maxResident, MaxActive: *maxActive, Audit: stderr, Executable: hctlExecutable})
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return runtime.Run(ctx)
}

func ensureApplied(p *project.Project, self string, stderr io.Writer) error {
	return ensureAppliedContext(context.Background(), p, self, stderr)
}

func ensureAppliedContext(ctx context.Context, p *project.Project, self string, stderr io.Writer) error {
	return ensureAppliedForPolicyContext(ctx, p, self, stderr, harness.PolicyDefault)
}

func ensureAppliedForPolicyContext(ctx context.Context, p *project.Project, self string, stderr io.Writer, policy harness.ExecutionPolicy) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nativeMCP, err := resolveProjectNativeMCP(ctx, p)
	if err != nil {
		return err
	}
	if err := setup.ValidateNativeMCP(p, nativeMCP); err != nil {
		return err
	}
	verify := setup.Verify
	if policy == harness.PolicyWorkspaceWrite {
		verify = setup.VerifyWritableChannel
	}
	if err := verify(p); err == nil && len(nativeMCP) == 0 {
		return nil
	}
	prepareContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := tool.Prepare(prepareContext, p.SourceRoot, p.WorkspaceRoot, p.SourceFingerprint, p.Tools); err != nil {
		return err
	}
	executable, err := resolvedSelf(self)
	if err != nil {
		return err
	}
	var result setup.Result
	if policy == harness.PolicyWorkspaceWrite {
		result, err = setup.ApplyWritableChannelWithNativeMCP(p, executable, nativeMCP)
	} else {
		result, err = setup.ApplyWithNativeMCP(p, executable, nativeMCP)
	}
	if err != nil {
		return err
	}
	recordContext, cancelRecord := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRecord()
	if err := recordChannelConsumption(recordContext, p, self); err != nil {
		return err
	}
	for _, diagnostic := range result.Diagnostics {
		_, _ = fmt.Fprintln(stderr, diagnostic.String())
	}
	_, _ = fmt.Fprintf(stderr, "auto-applied agent=%s harness=%s fingerprint=%s\n", p.Name, p.Harness, p.SourceFingerprint)
	return nil
}

func resolveProjectNativeMCP(ctx context.Context, p *project.Project) ([]integration.NativeMCPLaunchDescriptor, error) {
	if !hasInstalledConnection(p) {
		return nil, nil
	}
	store, err := integration.NewDefaultStore()
	if err != nil {
		return nil, err
	}
	result := make([]integration.NativeMCPLaunchDescriptor, 0, len(p.Connections))
	for _, connection := range p.Connections {
		if !connection.Installed() {
			continue
		}
		resolved, err := store.ResolveNativeMCP(ctx, connection.Package, connection.Capability)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", connection.Path, err)
		}
		descriptor, err := resolved.LaunchDescriptor(p.Harness)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", connection.Path, err)
		}
		if descriptor.ServerName != connection.Name {
			return nil, fmt.Errorf("%s: installed native-mcp server name %q must equal connection name %q", connection.Path, descriptor.ServerName, connection.Name)
		}
		result = append(result, descriptor)
	}
	return result, nil
}

func hasInstalledConnection(p *project.Project) bool {
	for _, connection := range p.Connections {
		if connection.Installed() {
			return true
		}
	}
	return false
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
