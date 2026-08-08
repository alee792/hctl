package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"hctl/internal/integration"
	"hctl/internal/project"
	"hctl/internal/rootfs"
)

func TestApplyIsDeterministicAndRefusesConflicts(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(p, "/opt/hctl/bin/hctl")
	if err != nil {
		t.Fatal(err)
	}
	paths := result.Files
	want := []string{
		".claude/agents/docs-reviewer.md",
		".claude/skills/echo/SKILL.md",
		".claude/skills/echo/references/info.md",
		".claude/skills/echo/scripts/check.sh",
		".mcp.json",
		"CLAUDE.md",
		".hctl/apply/claude.json",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	first := snapshot(t, root, paths)
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := snapshot(t, root, paths); !reflect.DeepEqual(got, first) {
		t.Fatal("same source did not produce byte-identical setup")
	}
	if config := read(t, filepath.Join(root, ".mcp.json")); !strings.Contains(config, `"command": "/opt/hctl/bin/hctl"`) || !strings.Contains(config, root) || !strings.Contains(config, `"--workspace"`) || !strings.Contains(config, `"claude"`) {
		t.Fatal("Claude MCP configuration does not bind the absolute hctl path")
	}
	if instructions := read(t, filepath.Join(root, "CLAUDE.md")); strings.Contains(instructions, "description: Test agent") || !strings.Contains(instructions, "Be concise.") {
		t.Fatalf("Claude instructions did not strip source frontmatter: %q", instructions)
	}
	if child := read(t, filepath.Join(root, ".claude", "agents", "docs-reviewer.md")); !strings.Contains(child, `description: "Review docs."`) || !strings.Contains(child, "Review documentation.") {
		t.Fatalf("Claude subagent = %q", child)
	}
	if info, err := os.Stat(filepath.Join(root, ".claude", "skills", "echo", "scripts", "check.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("executable skill resource mode = %v, %v", info, err)
	}
	if got := read(t, filepath.Join(root, ".claude", "skills", "echo", "references", "info.md")); got != "Echo returns bounded text.\n" {
		t.Fatalf("skill resource changed during apply: %q", got)
	}
	codex, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(codex, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, ".agents", "skills", "echo", "references", "info.md")); got != "Echo returns bounded text.\n" {
		t.Fatalf("Codex skill resource changed during apply: %q", got)
	}
	if info, err := os.Stat(filepath.Join(root, ".agents", "skills", "echo", "scripts", "check.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("Codex executable skill resource mode = %v, %v", info, err)
	}
	config := read(t, filepath.Join(root, ".codex", "config.toml"))
	if !strings.Contains(config, `command = "/opt/hctl/bin/hctl"`) || !strings.Contains(config, `[mcp_servers.managed]`) || !strings.Contains(config, `"--workspace"`) || !strings.Contains(config, `"--harness", "codex"`) || !strings.Contains(config, "required = true") || !strings.Contains(config, `default_tools_approval_mode = "approve"`) {
		t.Fatal("Codex MCP configuration does not bind the shared managed server")
	}
	if child := read(t, filepath.Join(root, ".codex", "agents", "docs-reviewer.toml")); !strings.Contains(child, `name = "docs_reviewer"`) || !strings.Contains(child, `description = "Review docs."`) || !strings.Contains(child, `developer_instructions = "Review documentation."`) {
		t.Fatalf("Codex subagent = %q", child)
	}

	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("hand edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("edited generated file was not refused: %v", err)
	}

	conflictRoot := testAgent(t)
	if err := os.WriteFile(filepath.Join(conflictRoot, "CLAUDE.md"), []byte("hand authored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conflict, err := project.Load(conflictRoot, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(conflict, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "without hctl ownership") {
		t.Fatalf("hand-authored native file was not refused: %v", err)
	}
}

func TestGeneratedInstructionsIncludeFrictionGuidanceOnlyWhenEnabled(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			p := &project.Project{Name: "reviewer", Harness: harness, SourceFingerprint: "source", Instructions: []byte("Review carefully.\n")}
			generated, err := filesFor(p, "/opt/hctl/bin/hctl")
			if err != nil {
				t.Fatal(err)
			}
			path := "AGENTS.md"
			if harness == "claude" {
				path = "CLAUDE.md"
			}
			if strings.Contains(string(generated.Files[path].Content), "record-friction") {
				t.Fatal("disabled friction guidance was generated")
			}
			p.FrictionNotes = true
			generated, err = filesFor(p, "/opt/hctl/bin/hctl")
			if err != nil {
				t.Fatal(err)
			}
			instructions := string(generated.Files[path].Content)
			for _, required := range []string{"record-friction", "primary task", "at most one", "never retry", "sensitive"} {
				if !strings.Contains(instructions, required) {
					t.Fatalf("friction guidance missing %q: %s", required, instructions)
				}
			}
		})
	}
}

func TestGitHubConnectionGeneratesExactNativeUnmanagedConfiguration(t *testing.T) {
	const fakeValue = "conspicuous-fake-pat-marker"
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			root := testAgent(t)
			write(t, filepath.Join(root, "connections", "github.md"), "Inspect GitHub issues through discovered native tools.\n")
			p, err := project.Load(root, harness)
			if err != nil {
				t.Fatal(err)
			}
			descriptor := testNativeMCPDescriptor(t, harness)
			t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", fakeValue)
			if _, err := ApplyWithNativeMCP(p, "/opt/hctl/bin/hctl", []integration.NativeMCPLaunchDescriptor{descriptor}); err != nil {
				t.Fatal(err)
			}
			instructionsPath := filepath.Join(root, "AGENTS.md")
			configPath := filepath.Join(root, ".codex", "config.toml")
			if harness == "claude" {
				instructionsPath = filepath.Join(root, "CLAUDE.md")
				configPath = filepath.Join(root, ".mcp.json")
			}
			instructions := read(t, instructionsPath)
			for _, fragment := range []string{"Inspect GitHub issues", "discovered tools", "native and unmanaged", "does not filter, confirm, broker, or audit"} {
				if !strings.Contains(instructions, fragment) {
					t.Fatalf("generated instructions omit %q: %s", fragment, instructions)
				}
			}
			config := read(t, configPath)
			if strings.Contains(config, fakeValue) {
				t.Fatal("resolved ambient value entered generated native configuration")
			}
			if harness == "claude" {
				for _, fragment := range []string{`"github"`, `"command": "/usr/bin/env"`, `"-C"`, descriptor.WorkingDirectory, descriptor.Command, `"stdio"`} {
					if !strings.Contains(config, fragment) {
						t.Fatalf("Claude native config omitted %q: %s", fragment, config)
					}
				}
				if strings.Contains(config, `"GITHUB_PERSONAL_ACCESS_TOKEN"`) {
					t.Fatal("Claude config should inherit the launch environment without an env entry")
				}
			} else {
				for _, fragment := range []string{`[mcp_servers."github"]`, `command = "` + descriptor.Command + `"`, `args = ["stdio"]`, `cwd = "` + descriptor.WorkingDirectory + `"`, `enabled = true`, `required = false`, `default_tools_approval_mode = "prompt"`, `env_vars = ["GITHUB_PERSONAL_ACCESS_TOKEN"]`} {
					if !strings.Contains(config, fragment) {
						t.Fatalf("Codex native config omitted %q: %s", fragment, config)
					}
				}
			}
			for _, path := range []string{configPath, instructionsPath, filepath.Join(root, ".hctl", "apply", harness+".json")} {
				if strings.Contains(read(t, path), fakeValue) {
					t.Fatalf("resolved ambient value entered retained file %s", path)
				}
			}
		})
	}
}

func TestGitHubNativeMCPRejectsCollisionsBeforeMutation(t *testing.T) {
	root := testAgent(t)
	write(t, filepath.Join(root, "connections", "github.md"), "Use GitHub.\n")
	pluginRoot := filepath.Join(root, "plugins", "collision")
	write(t, filepath.Join(pluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"collision"}`)
	write(t, filepath.Join(pluginRoot, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"github":{"type":"stdio","command":"server"}}}`)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ApplyWithNativeMCP(p, "/opt/hctl/bin/hctl", []integration.NativeMCPLaunchDescriptor{testNativeMCPDescriptor(t, "codex")})
	if err == nil || !strings.Contains(err.Error(), `server "github" collides`) {
		t.Fatalf("collision error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("collision mutated native config: %v", err)
	}
}

func TestGeneratedGitHubNativeMCPLaunchesCredentialFreeFixture(t *testing.T) {
	const fakeValue = "conspicuous-native-fixture-marker"
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			root := testAgent(t)
			write(t, filepath.Join(root, "connections", "github.md"), "Use discovered GitHub tools.\n")
			p, err := project.Load(root, harness)
			if err != nil {
				t.Fatal(err)
			}
			descriptor, packageRoot, storeRoot := testInstalledNativeMCPDescriptor(t, harness)
			if _, err := ApplyWithNativeMCP(p, "/opt/hctl/bin/hctl", []integration.NativeMCPLaunchDescriptor{descriptor}); err != nil {
				t.Fatal(err)
			}
			harnessExecutable := testNativeConfigHarness(t, harness)
			approvalEnv := func(project, server, tool string) []string {
				if harness == "claude" {
					return []string{"FAKE_CLAUDE_PROJECT_SERVER_APPROVAL=" + server}
				}
				return []string{
					"FAKE_CODEX_PROJECT_TRUST=" + project,
					"FAKE_CODEX_SERVER_APPROVAL=" + server,
					"FAKE_CODEX_TOOL_APPROVAL=" + tool,
				}
			}
			runWithEnvironment := func(environment []string, approvals []string, runtimeEnv ...string) (string, string, error) {
				process := exec.Command(harnessExecutable)
				process.Env = append([]string{
					"PATH=" + os.Getenv("PATH"), "FAKE_WORKSPACE=" + root,
				}, approvals...)
				process.Env = append(process.Env, environment...)
				process.Env = append(process.Env, runtimeEnv...)
				var stdout, stderr bytes.Buffer
				process.Stdout = &stdout
				process.Stderr = &stderr
				err := process.Run()
				return stdout.String(), stderr.String(), err
			}
			run := func(value string, approvals []string, runtimeEnv ...string) (string, string, error) {
				return runWithEnvironment([]string{"GITHUB_PERSONAL_ACCESS_TOKEN=" + value}, approvals, runtimeEnv...)
			}
			approved := approvalEnv("approved", "approved", "approved")
			stdout, stderr, err := run(fakeValue, approved)
			if err != nil || stderr != "" {
				t.Fatalf("native fixture launch = stdout %q, stderr %q, error %v", stdout, stderr, err)
			}
			for _, fragment := range []string{`"name":"fixture_issue_inspect"`, `"name":"fixture_issue_claim"`, `"ok":true`} {
				if !strings.Contains(stdout, fragment) {
					t.Fatalf("native discovery/call omitted %q: %s", fragment, stdout)
				}
			}
			if strings.Contains(stdout, fakeValue) {
				t.Fatal("native protocol output exposed the ambient value")
			}
			restarted, restartStderr, err := run(fakeValue, approved)
			if err != nil || restartStderr != "" || restarted != stdout {
				t.Fatalf("restarted native fixture = stdout %q, stderr %q, error %v", restarted, restartStderr, err)
			}
			readOnly, readOnlyStderr, err := run(fakeValue, approved, "HCTL_EXECUTION_POLICY=read-only")
			if err != nil || readOnlyStderr != "" || !strings.Contains(readOnly, `"unmanaged_effect":true`) || !strings.Contains(readOnly, `"execution_policy":"read-only"`) {
				t.Fatalf("read-only native effect boundary = stdout %q, stderr %q, error %v", readOnly, readOnlyStderr, err)
			}
			type concurrentResult struct {
				stdout string
				stderr string
				err    error
			}
			results := make(chan concurrentResult, 3)
			for range 3 {
				go func() {
					out, diagnostic, runErr := run(fakeValue, approved)
					results <- concurrentResult{stdout: out, stderr: diagnostic, err: runErr}
				}()
			}
			for range 3 {
				result := <-results
				if result.err != nil || result.stderr != "" || result.stdout != stdout {
					t.Fatalf("concurrent native fixture = stdout %q, stderr %q, error %v", result.stdout, result.stderr, result.err)
				}
			}

			stdout, stderr, err = runWithEnvironment(nil, approved)
			if err != nil || stdout != "" || !strings.Contains(stderr, "github optional startup failed: authentication missing") || strings.Contains(stderr, fakeValue) {
				t.Fatalf("bounded missing authentication failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
			}
			stdout, stderr, err = run("", approved)
			if err != nil || stdout != "" || !strings.Contains(stderr, "github optional startup failed: authentication empty") || strings.Contains(stderr, fakeValue) {
				t.Fatalf("bounded empty authentication failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
			}
			const invalidValue = "invalid-native-fixture-marker"
			stdout, stderr, err = run(invalidValue, approved)
			if err != nil || stdout != "" || !strings.Contains(stderr, "github optional startup failed: authentication rejected") || strings.Contains(stderr, fakeValue) || strings.Contains(stderr, invalidValue) {
				t.Fatalf("bounded invalid authentication failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
			}
			if harness == "claude" {
				if stdout, stderr, err := run(fakeValue, approvalEnv("approved", "missing", "approved")); err == nil || stdout != "" || !strings.Contains(stderr, "Claude project MCP server approval required") {
					t.Fatalf("Claude project-server approval failure = stdout %q, stderr %q, error %v", stdout, stderr, err)
				}
			} else {
				for name, approvals := range map[string][]string{
					"project trust":   approvalEnv("missing", "approved", "approved"),
					"server approval": approvalEnv("approved", "missing", "approved"),
					"tool approval":   approvalEnv("approved", "approved", "missing"),
				} {
					t.Run(name, func(t *testing.T) {
						stdout, stderr, err := run(fakeValue, approvals)
						if err == nil || stdout != "" || !strings.Contains(stderr, "Codex "+name+" required") {
							t.Fatalf("%s failure = stdout %q, stderr %q, error %v", name, stdout, stderr, err)
						}
					})
				}
			}
			for name, runtimeEnv := range map[string]string{
				"protocol version": "FAKE_MCP_PROTOCOL_VERSION=2099-01-01",
				"server version":   "FAKE_MCP_SERVER_VERSION=999.0.0",
			} {
				t.Run("unsupported "+name, func(t *testing.T) {
					stdout, stderr, err := run(fakeValue, approved, runtimeEnv)
					if err != nil || stdout != "" || !strings.Contains(stderr, "github optional startup failed: unsupported MCP "+name) || strings.Contains(stderr, fakeValue) {
						t.Fatalf("unsupported %s failure = stdout %q, stderr %q, error %v", name, stdout, stderr, err)
					}
				})
			}
			for _, evidenceRoot := range []string{packageRoot, storeRoot, root} {
				assertTreeOmits(t, evidenceRoot, fakeValue)
				assertTreeOmits(t, evidenceRoot, invalidValue)
			}
		})
	}
}

func testInstalledNativeMCPDescriptor(t *testing.T, harness string) (integration.NativeMCPLaunchDescriptor, string, string) {
	t.Helper()
	packageRoot := t.TempDir()
	payload := []byte(`#!/bin/sh
if [ "${GITHUB_PERSONAL_ACCESS_TOKEN+x}" != x ]; then
  echo 'authentication missing' >&2
  exit 20
fi
if [ -z "$GITHUB_PERSONAL_ACCESS_TOKEN" ]; then
  echo 'authentication empty' >&2
  exit 21
fi
case "$GITHUB_PERSONAL_ACCESS_TOKEN" in
  invalid-*) echo 'authentication rejected' >&2; exit 22 ;;
esac
IFS= read -r initialize
IFS= read -r list
IFS= read -r call
case "$call" in
  *'"name":"fixture_issue_claim"'*) ;;
  *) echo 'unexpected fixture tool outcome' >&2; exit 23 ;;
esac
protocol_version=${FAKE_MCP_PROTOCOL_VERSION-2025-06-18}
server_version=${FAKE_MCP_SERVER_VERSION-1.8.0-fixture}
printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"$protocol_version\",\"serverInfo\":{\"name\":\"github-mcp-server\",\"version\":\"$server_version\"}}}"
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"fixture_issue_inspect"},{"name":"fixture_issue_claim"}]}}'
printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"structuredContent\":{\"ok\":true,\"unmanaged_effect\":true,\"execution_policy\":\"${HCTL_EXECUTION_POLICY-unset}\"}}}"
`)
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	artifactID := runtime.GOOS + "-" + runtime.GOARCH
	document := map[string]any{
		"schema_version": 1, "id": "github-mcp-server", "version": "1.8.0", "name": "Fake GitHub MCP", "description": "Credential-free native MCP fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://github.com/github/github-mcp-server", "revision": "v1.8.0"},
		"compatibility": map[string]any{"minimum": "0.1.0-dev", "before": "9.0.0"},
		"artifacts": []any{map[string]any{
			"id": artifactID, "os": runtime.GOOS, "architecture": runtime.GOARCH, "format": "binary",
			"source": map[string]any{"kind": "package", "path": "payload/github-mcp-server"}, "size": len(payload), "sha256": checksum,
			"executable": map[string]any{"path": "github-mcp-server", "size": len(payload), "sha256": checksum},
		}},
		"capabilities": []any{map[string]any{
			"type": "native-mcp", "version": 1, "id": "github", "server_name": "github", "collision": "reject", "artifacts": []string{artifactID}, "executable": "github-mcp-server",
			"arguments": []string{"stdio"}, "working_directory": ".", "environment": map[string]string{},
			"required_environment": []any{map[string]any{"name": "GITHUB_PERSONAL_ACCESS_TOKEN", "description": "Ambient authentication required at runtime."}},
			"harnesses": []any{
				map[string]any{"name": "claude", "startup": "optional", "trust": "native-project"},
				map[string]any{"name": "codex", "startup": "optional", "trust": "native-project"},
			},
		}},
	}
	manifest, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(packageRoot, "integration.json"), string(manifest)+"\n")
	write(t, filepath.Join(packageRoot, "payload", "github-mcp-server"), string(payload))
	storeRoot := t.TempDir()
	store := integration.NewStore(storeRoot, nil)
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: packageRoot, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveNativeMCP(context.Background(), "github-mcp-server", "github")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := resolved.LaunchDescriptor(harness)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor, packageRoot, storeRoot
}

func testNativeConfigHarness(t *testing.T, harness string) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), harness)
	trust := `#!/bin/sh
set -eu
`
	var parse string
	if harness == "claude" {
		trust += `[ "${FAKE_CLAUDE_PROJECT_SERVER_APPROVAL-}" = approved ] || { echo 'Claude project MCP server approval required' >&2; exit 30; }
`
		parse = `config="$FAKE_WORKSPACE/.mcp.json"
grep -F '"github": {' "$config" >/dev/null
grep -F '"command": "/usr/bin/env"' "$config" >/dev/null
cwd=$(awk '/"github": \{/{github=1} github && /"args": \[/{args=1; next} args {line=$0; gsub(/^[[:space:]]*"|",?[[:space:]]*$/, "", line); n++; if(n==2){print line; exit}}' "$config")
server=$(awk '/"github": \{/{github=1} github && /"args": \[/{args=1; next} args {line=$0; gsub(/^[[:space:]]*"|",?[[:space:]]*$/, "", line); n++; if(n==4){print line; exit}}' "$config")
grep -F '"stdio"' "$config" >/dev/null
`
	} else {
		trust += `[ "${FAKE_CODEX_PROJECT_TRUST-}" = approved ] || { echo 'Codex project trust required' >&2; exit 30; }
[ "${FAKE_CODEX_SERVER_APPROVAL-}" = approved ] || { echo 'Codex server approval required' >&2; exit 31; }
[ "${FAKE_CODEX_TOOL_APPROVAL-}" = approved ] || { echo 'Codex tool approval required' >&2; exit 32; }
`
		parse = `config="$FAKE_WORKSPACE/.codex/config.toml"
grep -F '[mcp_servers."github"]' "$config" >/dev/null
grep -F 'enabled = true' "$config" >/dev/null
grep -F 'required = false' "$config" >/dev/null
grep -F 'default_tools_approval_mode = "prompt"' "$config" >/dev/null
grep -F 'env_vars = ["GITHUB_PERSONAL_ACCESS_TOKEN"]' "$config" >/dev/null
server=$(awk '/^\[mcp_servers\."github"\]$/{github=1; next} github && /^command = /{line=$0; sub(/^command = "/, "", line); sub(/"$/, "", line); print line; exit}' "$config")
cwd=$(awk '/^\[mcp_servers\."github"\]$/{github=1; next} github && /^cwd = /{line=$0; sub(/^cwd = "/, "", line); sub(/"$/, "", line); print line; exit}' "$config")
grep -F 'args = ["stdio"]' "$config" >/dev/null
`
	}
	run := `failure=$(mktemp)
trap 'rm -f "$failure"' EXIT
set +e
output=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fixture_issue_claim","arguments":{}}}' | /usr/bin/env -C "$cwd" -- "$server" stdio 2>"$failure")
status=$?
set -e
if [ "$status" -ne 0 ]; then
  printf 'github optional startup failed: %s\n' "$(cat "$failure")" >&2
  exit 0
fi
printf '%s\n' "$output" | grep -F '"protocolVersion":"2025-06-18"' >/dev/null || { echo 'github optional startup failed: unsupported MCP protocol version' >&2; exit 0; }
printf '%s\n' "$output" | grep -F '"version":"1.8.0-fixture"' >/dev/null || { echo 'github optional startup failed: unsupported MCP server version' >&2; exit 0; }
printf '%s\n' "$output"
`
	write(t, executable, trust+parse+run)
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatal(err)
	}
	return executable
}

func assertTreeOmits(t *testing.T, root, value string) {
	t.Helper()
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(value)) {
			t.Fatalf("resolved environment value entered %s", path)
		}
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyMoreThanEightSkillsForBothHarnesses(t *testing.T) {
	root := testAgent(t)
	for index := 0; index < 12; index++ {
		name := "skill-" + strconv.Itoa(index)
		write(t, filepath.Join(root, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: Extra skill.\n---\n\nUse it.\n")
		write(t, filepath.Join(root, "skills", name, "references", "proof.txt"), "proof\n")
	}
	for _, harness := range []string{"claude", "codex"} {
		p, err := project.Load(root, harness)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Skills) != 13 {
			t.Fatalf("%s loaded %d skills", harness, len(p.Skills))
		}
		if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, ".claude", "skills", "skill-11", "references", "proof.txt")
		if harness == "codex" {
			path = filepath.Join(root, ".agents", "skills", "skill-11", "references", "proof.txt")
		}
		if got := read(t, path); got != "proof\n" {
			t.Fatalf("%s changed skill resource: %q", harness, got)
		}
	}
}

func TestApplyAndVerifyLargeSkillResource(t *testing.T) {
	root := testAgent(t)
	content := make([]byte, 2<<20)
	writeBytes(t, root, "skills/echo/assets/large.bin", content)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	if err := Verify(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, ".claude", "skills", "echo", "assets", "large.bin"))
	if err != nil || info.Size() != int64(len(content)) {
		t.Fatalf("large skill resource size = %v, err = %v", info, err)
	}
}

func TestGeneratedPluginMCPConfigurationIsBounded(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			root := t.TempDir()
			p := &project.Project{
				Name:              "bounded",
				Harness:           harness,
				SourceRoot:        root,
				WorkspaceRoot:     root,
				SourceFingerprint: "fingerprint",
				Instructions:      []byte("Be concise.\n"),
				PluginMCPServers: []project.PluginMCPServer{{
					Name:    "large",
					Type:    "streamable-http",
					URL:     "https://example.com/mcp",
					Headers: map[string]string{"X-Large": strings.Repeat("x", maxGeneratedConfigBytes)},
				}},
			}
			if _, err := filesFor(p, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "configuration") || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("oversized generated MCP configuration was not rejected: %v", err)
			}
		})
	}
}

func TestGeneratedMCPConfigurationBoundary(t *testing.T) {
	if err := validateGeneratedConfig(".mcp.json", make([]byte, maxGeneratedConfigBytes)); err != nil {
		t.Fatalf("maximum configuration was rejected: %v", err)
	}
	if err := validateGeneratedConfig(".mcp.json", make([]byte, maxGeneratedConfigBytes+1)); err == nil || !strings.Contains(err.Error(), ".mcp.json") {
		t.Fatalf("configuration above maximum was not rejected at its path: %v", err)
	}
}

func TestGeneratedMCPConfigurationVerificationUsesGenerationLimit(t *testing.T) {
	root := t.TempDir()
	writeBytes(t, root, ".mcp.json", make([]byte, maxGeneratedConfigBytes+1))
	if _, _, _, err := generatedState(root, ".mcp.json"); err == nil || !strings.Contains(err.Error(), ".mcp.json") {
		t.Fatalf("oversized generated configuration was accepted during verification: %v", err)
	}
}

func TestApplyRecordLimitFailsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	p := &project.Project{
		Name:              "bounded",
		AgentID:           "bounded",
		Harness:           "claude",
		SourceRoot:        root,
		WorkspaceRoot:     root,
		SourceReference:   strings.Repeat("s", maxMetadataBytes),
		SourceFingerprint: "fingerprint",
		Instructions:      []byte("Be concise.\n"),
	}
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "apply record exceeds") {
		t.Fatalf("oversized apply record was not rejected: %v", err)
	}
	for _, path := range []string{"CLAUDE.md", ".mcp.json", ".hctl"} {
		if _, err := os.Lstat(filepath.Join(root, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was mutated before apply-record validation: %v", path, err)
		}
	}
}

func TestGeneratedInstructionsContainDiscordParticipationPolicy(t *testing.T) {
	root := testAgent(t)
	if err := os.MkdirAll(filepath.Join(root, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "channels", "discord.md"), []byte("---\nmode: ambient\n---\n\nParticipate only in review work.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"claude", "codex"} {
		p, err := project.Load(root, harness)
		if err != nil {
			t.Fatal(err)
		}
		generated, err := filesFor(p, "/opt/hctl/bin/hctl")
		if err != nil {
			t.Fatal(err)
		}
		path := "CLAUDE.md"
		if harness == "codex" {
			path = "AGENTS.md"
		}
		content := string(generated.Files[path].Content)
		for _, required := range []string{"Participate only in review work.", "HCTL_NO_REPLY", "HCTL_REQUEST_WRITE_ACCESS", "enforced read-only", "`channel.request_input` tool is advertised", "proceed without asking", "never fabricate interaction or callback identifiers", "vendor payloads"} {
			if !strings.Contains(content, required) {
				t.Fatalf("%s instructions omitted %q: %q", harness, required, content)
			}
		}
		if harness == "codex" {
			for _, required := range []string{"`continuation_turn`", "end the current turn immediately", "`hctl.channel_input_answer`", "not an unrelated channel message", "never quote or expose the control envelope"} {
				if !strings.Contains(content, required) {
					t.Fatalf("Codex continuation instructions omitted %q: %q", required, content)
				}
			}
		} else if strings.Contains(content, "hctl.channel_input_answer") {
			t.Fatalf("Claude received Codex continuation instructions: %q", content)
		}
		if strings.Contains(content, "discord_components") || strings.Contains(content, "application_id") {
			t.Fatalf("%s instructions omitted Discord policy: %q", harness, content)
		}
		settings, present := generated.Files[".claude/hctl-settings.json"]
		if harness == "claude" {
			if !present || !strings.Contains(string(settings.Content), `"matcher": "^mcp__managed__channel\\.request_input$"`) || !strings.Contains(string(settings.Content), `"claude-deferred-input"`) || strings.Contains(string(settings.Content), "Bash|") {
				t.Fatalf("Claude deferred hook is not narrowly generated: %q", string(settings.Content))
			}
			var decoded struct {
				Hooks map[string][]struct {
					Matcher string `json:"matcher"`
				} `json:"hooks"`
			}
			if err := json.Unmarshal(settings.Content, &decoded); err != nil || len(decoded.Hooks["PreToolUse"]) != 1 {
				t.Fatalf("Claude deferred hook JSON = %#v, %v", decoded, err)
			}
			matcher := regexp.MustCompile(decoded.Hooks["PreToolUse"][0].Matcher)
			for _, value := range []string{"mcp__managed__channelXrequest_input", "prefixmcp__managed__channel.request_input", "mcp__managed__channel.request_input_suffix", "mcp__other__channel.request_input"} {
				if matcher.MatchString(value) {
					t.Fatalf("near-match %q was intercepted", value)
				}
			}
			if !matcher.MatchString("mcp__managed__channel.request_input") {
				t.Fatal("exact managed tool did not match")
			}
		} else if present {
			t.Fatal("Claude hook configuration was generated for Codex")
		}
	}
}

func TestWritableChannelInstructionsDoNotRequestElevationAgain(t *testing.T) {
	root := testAgent(t)
	if err := os.MkdirAll(filepath.Join(root, "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "channels", "discord.md"), []byte("---\nmode: ambient\n---\n\nParticipate only in review work.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"claude", "codex"} {
		p, err := project.Load(root, harness)
		if err != nil {
			t.Fatal(err)
		}
		generated, err := filesForPolicy(p, "/opt/hctl/bin/hctl", true)
		if err != nil {
			t.Fatal(err)
		}
		path := "CLAUDE.md"
		if harness == "codex" {
			path = "AGENTS.md"
		}
		content := string(generated.Files[path].Content)
		if !strings.Contains(content, "already has workspace-write access") || strings.Contains(content, "HCTL_REQUEST_WRITE_ACCESS") || strings.Contains(content, "enforced read-only") {
			t.Fatalf("%s writable instructions are contradictory: %q", harness, content)
		}
	}
}

func TestVendoredPluginSkillsGenerateForBothHarnesses(t *testing.T) {
	root := testAgent(t)
	write(t, filepath.Join(root, "plugins", "review-pack", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "review-pack",
  "extensions": {"com.example.review": {}},
  "future": true
}`)
	write(t, filepath.Join(root, "plugins", "review-pack", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review carefully.\nallowed-tools: Read\n---\n\nReview.\n")
	write(t, filepath.Join(root, "plugins", "review-pack", "skills", "review", "references", "guide.md"), "plugin guide\n")

	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			p, err := project.Load(root, harness)
			if err != nil {
				t.Fatal(err)
			}
			generated, err := filesFor(p, "/opt/hctl/bin/hctl")
			if err != nil {
				t.Fatal(err)
			}
			prefix := ".claude/skills"
			if harness == "codex" {
				prefix = ".agents/skills"
			}
			if got := string(generated.Files[prefix+"/review/references/guide.md"].Content); got != "plugin guide\n" {
				t.Fatalf("generated plugin resource = %q", got)
			}
			wantDiagnostics := 2
			if harness == "codex" {
				wantDiagnostics = 3
			}
			if len(generated.Diagnostics) != wantDiagnostics {
				t.Fatalf("%s diagnostics = %#v", harness, generated.Diagnostics)
			}
			for _, diagnostic := range generated.Diagnostics {
				if diagnostic.Harness != harness || !strings.HasPrefix(diagnostic.Path, "plugins/review-pack/") {
					t.Fatalf("%s diagnostic lost plugin source context: %#v", harness, diagnostic)
				}
			}
		})
	}
}

func TestVendoredPluginSkillRemovalCleansGeneratedFiles(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			source := testAgent(t)
			workspace := t.TempDir()
			pluginRoot := filepath.Join(source, "plugins", "review-pack")
			write(t, filepath.Join(pluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"review-pack"}`)
			write(t, filepath.Join(pluginRoot, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review carefully.\n---\n\nReview.\n")

			loaded, err := project.Load(source, harness, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Apply(loaded, "/opt/hctl/bin/hctl"); err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(workspace, ".claude", "skills", "review", "SKILL.md")
			if harness == "codex" {
				generated = filepath.Join(workspace, ".agents", "skills", "review", "SKILL.md")
			}
			if _, err := os.Stat(generated); err != nil {
				t.Fatalf("generated plugin skill missing before removal: %v", err)
			}

			if err := os.RemoveAll(pluginRoot); err != nil {
				t.Fatal(err)
			}
			withoutPlugin, err := project.Load(source, harness, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if withoutPlugin.SourceFingerprint == loaded.SourceFingerprint {
				t.Fatal("plugin removal did not change the source fingerprint")
			}
			if _, err := Apply(withoutPlugin, "/opt/hctl/bin/hctl"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(generated); !os.IsNotExist(err) {
				t.Fatalf("removed plugin skill remains in generated setup: %v", err)
			}
			if err := Verify(loaded); err == nil || !strings.Contains(err.Error(), "stale") {
				t.Fatalf("pre-removal project did not become stale: %v", err)
			}
		})
	}
}

func TestPluginMCPServersGenerateNativeUnmanagedConfiguration(t *testing.T) {
	source := testAgent(t)
	workspace := t.TempDir()
	pluginRoot := filepath.Join(source, "plugins", "tools")
	write(t, filepath.Join(pluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"tools"}`)
	writeBytes(t, source, "plugins/tools/bin/server", []byte("#!/bin/sh\n"))
	if err := os.Chmod(filepath.Join(pluginRoot, "bin", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(pluginRoot, "mcp.json"), `{
  "$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers":{
    "local.tool":{"type":"stdio","command":"./bin/server","args":["--root","${PLUGIN_ROOT}","--data","${PLUGIN_DATA}/input"],"env":{"CACHE":"${PLUGIN_DATA}/cache"},"cwd":"${PLUGIN_DATA}/state"},
    "remote":{"type":"streamable-http","url":"https://example.com/mcp","headers":{"X-Package":"visible"}}
  }
}`)

	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			p, err := project.Load(source, harness, workspace)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Apply(p, "/opt/hctl/bin/hctl")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Diagnostics) != 0 {
				t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
			}
			canonicalPluginRoot := filepath.Join(p.SourceRoot, "plugins", "tools")
			pluginData := filepath.Join(p.WorkspaceRoot, filepath.FromSlash(p.PluginMCPServers[0].DataPath))
			if info, err := os.Stat(filepath.Join(pluginData, "state")); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("plugin data cwd = %v, %v", info, err)
			}
			if err := Verify(p); err != nil {
				t.Fatal(err)
			}
			if harness == "claude" {
				var config struct {
					Servers map[string]struct {
						Type    string            `json:"type"`
						Command string            `json:"command"`
						Args    []string          `json:"args"`
						Env     map[string]string `json:"env"`
						CWD     string            `json:"cwd"`
						URL     string            `json:"url"`
						Headers map[string]string `json:"headers"`
					} `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(read(t, filepath.Join(workspace, ".mcp.json"))), &config); err != nil {
					t.Fatal(err)
				}
				local := config.Servers["local.tool"]
				if local.Type != "stdio" || local.Command != "/usr/bin/env" || local.CWD != "" || !reflect.DeepEqual(local.Args[:4], []string{"-C", filepath.Join(pluginData, "state"), "--", filepath.Join(canonicalPluginRoot, "bin", "server")}) || local.Env["PLUGIN_ROOT"] != canonicalPluginRoot || local.Env["PLUGIN_DATA"] != pluginData || local.Env["CACHE"] != filepath.Join(pluginData, "cache") || local.Args[7] != filepath.Join(pluginData, "input") {
					t.Fatalf("Claude local MCP config = %#v", local)
				}
				if remote := config.Servers["remote"]; remote.Type != "http" || remote.URL != "https://example.com/mcp" || remote.Headers["X-Package"] != "visible" {
					t.Fatalf("Claude remote MCP config = %#v", remote)
				}
			} else {
				config := read(t, filepath.Join(workspace, ".codex", "config.toml"))
				for _, fragment := range []string{`[mcp_servers."local.tool"]`, `command = "` + filepath.Join(canonicalPluginRoot, "bin", "server") + `"`, `cwd = "` + filepath.Join(pluginData, "state") + `"`, `default_tools_approval_mode = "prompt"`, `[mcp_servers."local.tool".env]`, `"PLUGIN_ROOT" = "` + canonicalPluginRoot + `"`, `[mcp_servers."remote".http_headers]`, `"X-Package" = "visible"`} {
					if !strings.Contains(config, fragment) {
						t.Fatalf("Codex MCP config omitted %q:\n%s", fragment, config)
					}
				}
				pluginSection := strings.Split(config, `[mcp_servers."local.tool"]`)[1]
				if strings.Contains(strings.Split(pluginSection, `[mcp_servers."local.tool".env]`)[0], "required = true") {
					t.Fatal("plugin MCP server was made required")
				}
			}
			if err := os.Chmod(pluginData, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := Verify(p); err == nil || !strings.Contains(err.Error(), "plugin data directory") {
				t.Fatalf("permissive plugin data did not stale setup: %v", err)
			}
			if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(pluginData); err != nil {
				t.Fatal(err)
			}
			if err := Verify(p); err == nil || !strings.Contains(err.Error(), "plugin data directory") {
				t.Fatalf("missing plugin data did not stale setup: %v", err)
			}
		})
	}
}

func testNativeMCPDescriptor(t *testing.T, harness string) integration.NativeMCPLaunchDescriptor {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "github-mcp-server")
	write(t, executable, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatal(err)
	}
	return integration.NativeMCPLaunchDescriptor{
		ServerName:       "github",
		Command:          executable,
		Arguments:        []string{"stdio"},
		WorkingDirectory: root,
		RequiredEnvironment: []integration.EnvironmentRequirement{{
			Name: "GITHUB_PERSONAL_ACCESS_TOKEN", Description: "Ambient authentication required at runtime.",
		}},
		Target: integration.NativeHarnessTarget{Name: harness, Startup: integration.StartupOptional, Trust: integration.TrustNativeProject},
	}
}

func TestPluginMCPRemovalUpdatesConfigAndPreservesData(t *testing.T) {
	source := testAgent(t)
	workspace := t.TempDir()
	pluginRoot := filepath.Join(source, "plugins", "tools")
	write(t, filepath.Join(pluginRoot, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"tools"}`)
	mcpPath := filepath.Join(pluginRoot, "mcp.json")
	write(t, mcpPath, `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"local":{"type":"stdio","command":"node"}}}`)
	p, err := project.Load(source, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	dataPath := filepath.Join(workspace, filepath.FromSlash(p.PluginMCPServers[0].DataPath))
	write(t, filepath.Join(dataPath, "retained.txt"), "state\n")
	if err := os.Remove(mcpPath); err != nil {
		t.Fatal(err)
	}
	without, err := project.Load(source, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(without, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, filepath.Join(workspace, ".mcp.json")), `"local"`) {
		t.Fatal("removed plugin MCP server remains configured")
	}
	if got := read(t, filepath.Join(dataPath, "retained.txt")); got != "state\n" {
		t.Fatalf("plugin data was not preserved: %q", got)
	}
	if err := Verify(p); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("pre-removal plugin MCP setup did not become stale: %v", err)
	}
}

func TestPluginMCPPlaceholderExpansionIsNonRecursive(t *testing.T) {
	root := filepath.Join("/source", "${PLUGIN_DATA}")
	data := filepath.Join("/workspace", "${PLUGIN_ROOT}")
	got := expandPluginMCPValue("${PLUGIN_ROOT}|${PLUGIN_DATA}|${OTHER}", root, data)
	want := root + "|" + data + "|${OTHER}"
	if got != want {
		t.Fatalf("placeholder expansion = %q, want %q", got, want)
	}
}

func TestSubagentEffortNativeOutput(t *testing.T) {
	for _, effort := range []string{"", "low", "medium", "high"} {
		label := effort
		if label == "" {
			label = "unspecified"
		}
		t.Run(label, func(t *testing.T) {
			root := testAgent(t)
			source := "---\ndescription: Review docs.\n"
			if effort != "" {
				source += "effort: " + effort + "\n"
			}
			write(t, filepath.Join(root, "subagents", "docs-reviewer", "instructions.md"), source+"---\n\nReview documentation.\n")

			claude, err := project.Load(root, "claude")
			if err != nil {
				t.Fatal(err)
			}
			claudeFiles, err := filesFor(claude, "/opt/hctl/bin/hctl")
			if err != nil {
				t.Fatal(err)
			}
			claudeEffort := ""
			if effort != "" {
				claudeEffort = "effort: " + effort + "\n"
			}
			wantClaude := "---\nname: docs-reviewer\ndescription: \"Review docs.\"\n" + claudeEffort + "---\n\nReview documentation.\n"
			if got := string(claudeFiles.Files[".claude/agents/docs-reviewer.md"].Content); got != wantClaude {
				t.Fatalf("Claude subagent = %q, want %q", got, wantClaude)
			}

			codex, err := project.Load(root, "codex")
			if err != nil {
				t.Fatal(err)
			}
			codexFiles, err := filesFor(codex, "/opt/hctl/bin/hctl")
			if err != nil {
				t.Fatal(err)
			}
			codexEffort := ""
			if effort != "" {
				codexEffort = "model_reasoning_effort = " + strconv.Quote(effort) + "\n"
			}
			wantCodex := "name = \"docs_reviewer\"\ndescription = \"Review docs.\"\n" + codexEffort + "developer_instructions = \"Review documentation.\"\n"
			if got := string(codexFiles.Files[".codex/agents/docs-reviewer.toml"].Content); got != wantCodex {
				t.Fatalf("Codex subagent = %q, want %q", got, wantCodex)
			}
		})
	}
}

func TestApplyRemovesSubagentEffortOnReapply(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		for _, description := range []string{"Review docs.", "Review: docs", "Review # docs", `"Review docs."`} {
			t.Run(harness+"/"+description, func(t *testing.T) {
				source := testAgent(t)
				path := filepath.Join(source, "subagents", "docs-reviewer", "instructions.md")
				write(t, path, "---\ndescription: "+description+"\neffort: high\n---\n\nReview documentation.\n")
				workspace := t.TempDir()
				withEffort, err := project.Load(source, harness, workspace)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Apply(withEffort, "/opt/hctl/bin/hctl"); err != nil {
					t.Fatal(err)
				}

				generated := filepath.Join(workspace, ".claude", "agents", "docs-reviewer.md")
				wantWithEffort := "---\nname: docs-reviewer\ndescription: " + strconv.Quote(description) + "\neffort: high\n---\n\nReview documentation.\n"
				if harness == "codex" {
					generated = filepath.Join(workspace, ".codex", "agents", "docs-reviewer.toml")
					wantWithEffort = "name = \"docs_reviewer\"\ndescription = " + strconv.Quote(description) + "\nmodel_reasoning_effort = \"high\"\ndeveloper_instructions = \"Review documentation.\"\n"
				}
				if got := read(t, generated); got != wantWithEffort {
					t.Fatalf("effort output = %q, want %q", got, wantWithEffort)
				}

				write(t, path, "---\ndescription: "+description+"\n---\n\nReview documentation.\n")
				withoutEffort, err := project.Load(source, harness, workspace)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := Apply(withoutEffort, "/opt/hctl/bin/hctl"); err != nil {
					t.Fatal(err)
				}
				wantWithoutEffort := "---\nname: docs-reviewer\ndescription: " + strconv.Quote(description) + "\n---\n\nReview documentation.\n"
				if harness == "codex" {
					wantWithoutEffort = "name = \"docs_reviewer\"\ndescription = " + strconv.Quote(description) + "\ndeveloper_instructions = \"Review documentation.\"\n"
				}
				if got := read(t, generated); got != wantWithoutEffort {
					t.Fatalf("description-only output = %q, want %q", got, wantWithoutEffort)
				}
			})
		}
	}
}

func TestApplyHandlesCrossHarnessVendorSemantics(t *testing.T) {
	t.Run("Claude fields to Codex", func(t *testing.T) {
		root := testAgent(t)
		write(t, filepath.Join(root, "skills", "echo", "SKILL.md"), `---
name: echo
description: Repeat text safely.
when_to_use: >-
  When text should be repeated.
argument-hint: "[text]"
model: sonnet
disallowed-tools: Write
---

Use echo.
`)
		p, err := project.Load(root, "codex")
		if err != nil {
			t.Fatal(err)
		}
		result, err := Apply(p, "/opt/hctl/bin/hctl")
		if err != nil {
			t.Fatal(err)
		}
		wantFields := []string{"argument-hint", "disallowed-tools", "model", "when_to_use"}
		if len(result.Diagnostics) != len(wantFields) {
			t.Fatalf("Codex diagnostics = %#v", result.Diagnostics)
		}
		for index, field := range wantFields {
			if result.Diagnostics[index].Field != field || !strings.Contains(result.Diagnostics[index].String(), "copied unchanged but may have no effect for codex") {
				t.Fatalf("Codex diagnostic %d = %#v", index, result.Diagnostics[index])
			}
		}
		generated := read(t, filepath.Join(root, ".agents", "skills", "echo", "SKILL.md"))
		for _, field := range []string{"when_to_use:", "argument-hint:", "model:", "disallowed-tools:"} {
			if !strings.Contains(generated, field) {
				t.Fatalf("field %q was not passed through: %q", field, generated)
			}
		}
	})

	t.Run("allowed tools to Codex", func(t *testing.T) {
		root := testAgent(t)
		write(t, filepath.Join(root, "skills", "echo", "SKILL.md"), "---\nname: echo\ndescription: Repeat text safely.\nallowed-tools: Read\n---\n\nUse echo.\n")
		claude, err := project.Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		generated, err := filesFor(claude, "/opt/hctl/bin/hctl")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(generated.Files[".claude/skills/echo/SKILL.md"].Content); !strings.Contains(got, "allowed-tools: Read") {
			t.Fatalf("Claude allowed-tools field was not preserved: %q", got)
		}
		p, err := project.Load(root, "codex")
		if err != nil {
			t.Fatal(err)
		}
		generated, err = filesFor(p, "/opt/hctl/bin/hctl")
		if err != nil {
			t.Fatal(err)
		}
		if len(generated.Diagnostics) != 1 || generated.Diagnostics[0].Field != "allowed-tools" {
			t.Fatalf("Codex allowed-tools diagnostics = %#v", generated.Diagnostics)
		}
		if got := string(generated.Files[".agents/skills/echo/SKILL.md"].Content); !strings.Contains(got, "allowed-tools: Read") {
			t.Fatalf("Codex allowed-tools field was not passed through: %q", got)
		}
	})

	t.Run("OpenAI metadata to Claude", func(t *testing.T) {
		root := testAgent(t)
		content := "interface:\n  display_name: Echo\n  default_prompt: Review this.\npolicy:\n  allow_implicit_invocation: false\n"
		write(t, filepath.Join(root, "skills", "echo", "agents", "openai.yaml"), content)
		claude, err := project.Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		generated, err := filesFor(claude, "/opt/hctl/bin/hctl")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(generated.Files[".claude/skills/echo/agents/openai.yaml"].Content); got != content {
			t.Fatalf("Claude OpenAI metadata changed: %q", got)
		}
		if len(generated.Diagnostics) != 1 || generated.Diagnostics[0].Field != "" || !strings.Contains(generated.Diagnostics[0].String(), "copied unchanged but may have no effect for claude") {
			t.Fatalf("Claude diagnostics = %#v", generated.Diagnostics)
		}
		codex, err := project.Load(root, "codex")
		if err != nil {
			t.Fatal(err)
		}
		generated, err = filesFor(codex, "/opt/hctl/bin/hctl")
		if err != nil {
			t.Fatal(err)
		}
		if got := string(generated.Files[".agents/skills/echo/agents/openai.yaml"].Content); got != content {
			t.Fatalf("Codex metadata changed: %q", got)
		}
		if len(generated.Diagnostics) != 0 {
			t.Fatalf("Codex OpenAI diagnostics = %#v", generated.Diagnostics)
		}
	})
}

func TestApplyMirrorsHarnessSpecificFilesWithOwnership(t *testing.T) {
	source := testAgent(t)
	claudeContent := "{\"permissions\":{\"deny\":[\"Read(./secrets/**)\"]}}\n"
	codexContent := "prefix_rule(pattern = [\"git\", \"status\"], decision = \"allow\")\n"
	write(t, filepath.Join(source, "harnesses", "claude", ".claude", "settings.json"), claudeContent)
	claudeHook := filepath.Join(source, "harnesses", "claude", ".claude", "hooks", "check.sh")
	write(t, claudeHook, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(claudeHook, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(source, "harnesses", "codex", ".codex", "rules", "default.rules"), codexContent)

	claudeWorkspace := t.TempDir()
	claude, err := project.Load(source, "claude", claudeWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(claude, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(claudeWorkspace, ".claude", "settings.json")); got != claudeContent {
		t.Fatalf("Claude settings changed during apply: %q", got)
	}
	if _, err := os.Stat(filepath.Join(claudeWorkspace, ".codex", "rules", "default.rules")); !os.IsNotExist(err) {
		t.Fatal("Codex-only file was applied to Claude workspace")
	}
	if info, err := os.Stat(filepath.Join(claudeWorkspace, ".claude", "hooks", "check.sh")); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("Claude hook mode = %v, %v", info, err)
	}

	codexWorkspace := t.TempDir()
	codex, err := project.Load(source, "codex", codexWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(codex, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(codexWorkspace, ".codex", "rules", "default.rules")); got != codexContent {
		t.Fatalf("Codex rules changed during apply: %q", got)
	}
	if _, err := os.Stat(filepath.Join(codexWorkspace, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("Claude-only file was applied to Codex workspace")
	}
}

func TestApplyProtectsHarnessSpecificCollisionsAndEdits(t *testing.T) {
	t.Run("collision is preflighted", func(t *testing.T) {
		source := testAgent(t)
		write(t, filepath.Join(source, "harnesses", "claude", ".claude", "settings.json"), "{}\n")
		workspace := t.TempDir()
		write(t, filepath.Join(workspace, ".claude", "settings.json"), "hand authored\n")
		p, err := project.Load(source, "claude", workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(p, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "without hctl ownership") {
			t.Fatalf("unowned harness file collision was not rejected: %v", err)
		}
		for _, path := range []string{"CLAUDE.md", ".mcp.json"} {
			if _, err := os.Stat(filepath.Join(workspace, path)); !os.IsNotExist(err) {
				t.Fatalf("%s was written before collision failure", path)
			}
		}
	})

	t.Run("modified owned file is not overwritten or removed", func(t *testing.T) {
		source := testAgent(t)
		sourceFile := filepath.Join(source, "harnesses", "claude", ".claude", "settings.json")
		write(t, sourceFile, "{}\n")
		workspace := t.TempDir()
		first, err := project.Load(source, "claude", workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(first, "/opt/hctl/bin/hctl"); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(workspace, ".claude", "settings.json")
		write(t, target, "workspace edit\n")
		write(t, sourceFile, "{\"changed\":true}\n")
		changed, err := project.Load(source, "claude", workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(changed, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "was changed") {
			t.Fatalf("modified owned file was overwritten: %v", err)
		}
		if got := read(t, target); got != "workspace edit\n" {
			t.Fatalf("workspace edit was overwritten: %q", got)
		}
		if err := os.Remove(sourceFile); err != nil {
			t.Fatal(err)
		}
		removed, err := project.Load(source, "claude", workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(removed, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "was changed") {
			t.Fatalf("modified owned file was removed: %v", err)
		}
		if got := read(t, target); got != "workspace edit\n" {
			t.Fatalf("workspace edit was removed: %q", got)
		}
	})

	t.Run("unchanged obsolete file is removed", func(t *testing.T) {
		source := testAgent(t)
		sourceFile := filepath.Join(source, "harnesses", "codex", ".codex", "rules", "default.rules")
		write(t, sourceFile, "prefix_rule(pattern = [\"git\", \"status\"], decision = \"allow\")\n")
		workspace := t.TempDir()
		first, err := project.Load(source, "codex", workspace)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Apply(first, "/opt/hctl/bin/hctl"); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(workspace, ".codex", "rules", "default.rules")
		if err := os.Remove(sourceFile); err != nil {
			t.Fatal(err)
		}
		removed, err := project.Load(source, "codex", workspace)
		if err != nil {
			t.Fatal(err)
		}
		if removed.SourceFingerprint == first.SourceFingerprint {
			t.Fatal("removed harness file did not change source fingerprint")
		}
		if _, err := Apply(removed, "/opt/hctl/bin/hctl"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("obsolete harness file remains: %v", err)
		}
		if err := Verify(first); err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("old harness source unexpectedly verified: %v", err)
		}
		if err := Verify(removed); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMaintainerCodeReviewSkillProjectsWithProvenance(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository source")
	}
	agentRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "agents", "maintainer"))
	provenance := read(t, filepath.Join(agentRoot, "skills", "code-review", "UPSTREAM.md"))
	for _, text := range []string{
		"https://github.com/mattpocock/skills",
		"8b36d4fb2635b3c21998dcd8144439c9e5ba7302",
		"MIT License",
		"Copyright (c) 2026 Matt Pocock",
	} {
		if !strings.Contains(provenance, text) {
			t.Fatalf("code-review provenance is missing %q", text)
		}
	}

	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			workspace := t.TempDir()
			p, err := project.Load(agentRoot, harness, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyWithNativeMCP(p, "/opt/hctl/bin/hctl", []integration.NativeMCPLaunchDescriptor{testNativeMCPDescriptor(t, harness)}); err != nil {
				t.Fatal(err)
			}
			prefix := ".claude/skills"
			if harness == "codex" {
				prefix = ".agents/skills"
			}
			generatedSkill := read(t, filepath.Join(workspace, filepath.FromSlash(prefix), "code-review", "SKILL.md"))
			for _, text := range []string{"## Standards", "## Spec", "### 4. Spawn both sub-agents in parallel"} {
				if !strings.Contains(generatedSkill, text) {
					t.Fatalf("generated %s code-review skill is missing %q", harness, text)
				}
			}
			if got := read(t, filepath.Join(workspace, filepath.FromSlash(prefix), "code-review", "UPSTREAM.md")); got != provenance {
				t.Fatalf("generated %s provenance changed during apply", harness)
			}
		})
	}
}

func TestMaintainerGitHubGuidanceKeepsClaimFirstAndNativeEffectsUnmanaged(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	agentRoot := filepath.Join(repositoryRoot, "agents", "maintainer")
	fixtureBytes, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "testdata", "maintainer-github-tracker.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Issue struct {
			State        string   `json:"state"`
			Assigned     bool     `json:"assigned"`
			Labels       []string `json:"labels"`
			OpenBlockers int      `json:"open_blockers"`
		} `json:"issue"`
		ExpectedFirstWrite string `json:"expected_first_tracker_write"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Issue.State != "open" || fixture.Issue.Assigned || !reflect.DeepEqual(fixture.Issue.Labels, []string{"ready-for-agent"}) || fixture.Issue.OpenBlockers != 0 || fixture.ExpectedFirstWrite != "assign_to_self" {
		t.Fatalf("tracker fixture no longer represents one eligible claim-first issue: %+v", fixture)
	}

	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			workspace := t.TempDir()
			p, err := project.Load(agentRoot, harness, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyWithNativeMCP(p, "/opt/hctl/bin/hctl", []integration.NativeMCPLaunchDescriptor{testNativeMCPDescriptor(t, harness)}); err != nil {
				t.Fatal(err)
			}
			instructionsPath := filepath.Join(workspace, "AGENTS.md")
			if harness == "claude" {
				instructionsPath = filepath.Join(workspace, "CLAUDE.md")
			}
			instructions := read(t, instructionsPath)
			for _, fragment := range []string{
				"discovered catalog", "first tracker write", "before a branch, edit, status comment",
				"not hctl authorization", "workspace-write promotion only", "read-only workspace does not", "make GitHub read-only",
				"instructions are not enforcement", "MCP PAT authenticates either", "branch exists remotely",
				"fine-grained, repository-scoped", "untrusted channel input", "live catalog and schemas are authoritative",
				"does not filter, confirm, broker, or audit", "must establish it deliberately before unattended launch",
			} {
				if !strings.Contains(instructions, fragment) {
					t.Fatalf("generated %s maintainer guidance omits %q: %s", harness, fragment, instructions)
				}
			}
			claim := strings.Index(instructions, "assigning the eligible issue to\nyourself must be the first tracker write")
			branch := strings.Index(instructions, "before a branch, edit, status comment")
			if claim < 0 || branch < 0 || claim > branch {
				t.Fatalf("generated %s guidance lost claim-first ordering", harness)
			}
		})
	}
}

func TestGeneratedSkillMarkerHandlesYAMLContentAndCRLF(t *testing.T) {
	source := []byte("---\r\nname: echo\r\ndescription: |\r\n  Includes --- inside YAML.\r\n---\r\n\r\nUse echo.\r\n")
	got, err := markGeneratedSkill(source, "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	marker := "<!-- Generated by " + project.GeneratorVersion
	if strings.Count(string(got), marker) != 1 || !strings.Contains(string(got), "Includes --- inside YAML.\r\n---\r\n"+marker) {
		t.Fatalf("generated marker placement = %q", got)
	}
}

func TestApplySwitchesAgentsInOneWorkspace(t *testing.T) {
	firstRoot := testAgent(t)
	secondRoot := testAgent(t)
	if err := os.RemoveAll(filepath.Join(secondRoot, "skills", "echo")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(secondRoot, "skills", "research", "SKILL.md"), "---\nname: research\ndescription: Research carefully.\n---\n\nResearch.\n")
	if err := os.RemoveAll(filepath.Join(secondRoot, "subagents")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(secondRoot, "subagents", "researcher", "instructions.md"), "---\ndescription: Research evidence.\n---\n\nFind evidence.\n")
	workspace := t.TempDir()
	first, err := project.Load(firstRoot, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := project.Load(secondRoot, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(first, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(second, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{".claude/skills/echo/SKILL.md", ".claude/skills/echo/references/info.md", ".claude/skills/echo/scripts/check.sh", ".claude/agents/docs-reviewer.md"} {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(obsolete))); !os.IsNotExist(err) {
			t.Fatalf("obsolete generated file %s remains", obsolete)
		}
	}
	for _, current := range []string{".claude/skills/research/SKILL.md", ".claude/agents/researcher.md"} {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(current))); err != nil {
			t.Fatalf("current generated file %s: %v", current, err)
		}
	}
	if err := Verify(first); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("switched agent unexpectedly verified: %v", err)
	}
	if err := Verify(second); err != nil {
		t.Fatal(err)
	}
}

func TestApplyTracksExecutableResourceMode(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(root, ".claude", "skills", "echo", "scripts", "check.sh")
	if err := os.Chmod(generated, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(p); err == nil || !strings.Contains(err.Error(), "missing or changed") {
		t.Fatalf("mode change unexpectedly verified: %v", err)
	}
	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err == nil || !strings.Contains(err.Error(), "mode was changed") {
		t.Fatalf("mode change was not protected: %v", err)
	}
}

func TestApplyUpgradesSchemaTwoRecordAndResourceModes(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := filesFor(p, "/opt/hctl/bin/hctl")
	if err != nil {
		t.Fatal(err)
	}
	files := generated.Files
	owned := make([]ownedFile, 0, len(files))
	for path, file := range files {
		writeBytes(t, root, path, file.Content)
		owned = append(owned, ownedFile{Path: path, SHA256: rootfs.SHA256(file.Content)})
	}
	prior := applyRecord{
		SchemaVersion:     2,
		Generator:         "hctl/0.2.0-dev",
		Harness:           "claude",
		AgentID:           p.AgentID,
		Source:            p.SourceReference,
		SourceFingerprint: p.SourceFingerprint,
		Files:             owned,
	}
	data, err := json.Marshal(prior)
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, root, applyRecordPath("claude"), data)

	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	upgraded, exists, err := readApplyRecord(root, applyRecordPath("claude"))
	if err != nil || !exists || upgraded.SchemaVersion != 3 {
		t.Fatalf("upgraded apply record = %#v, exists=%v, err=%v", upgraded, exists, err)
	}
	script := filepath.Join(root, ".claude", "skills", "echo", "scripts", "check.sh")
	if info, err := os.Stat(script); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("upgraded executable skill resource mode = %v, %v", info, err)
	}
}

func TestApplyMigratesLegacyProjectionRecord(t *testing.T) {
	root := testAgent(t)
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := filesFor(p, "/opt/hctl/bin/hctl")
	if err != nil {
		t.Fatal(err)
	}
	files := generated.Files
	owned := make([]ownedFile, 0, len(files)+1)
	for path, file := range files {
		writeBytes(t, root, path, file.Content)
		owned = append(owned, ownedFile{Path: path, SHA256: rootfs.SHA256(file.Content)})
	}
	manifestPath := ".hctl/manifests/claude.json"
	manifest := []byte("{}\n")
	writeBytes(t, root, manifestPath, manifest)
	owned = append(owned, ownedFile{Path: manifestPath, SHA256: rootfs.SHA256(manifest)})
	legacy := applyRecord{SchemaVersion: 1, Generator: project.GeneratorVersion, Harness: "claude", SourceFingerprint: p.SourceFingerprint, Files: owned}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, root, legacyRecordPath("claude"), data)

	if _, err := Apply(p, "/opt/hctl/bin/hctl"); err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{manifestPath, legacyRecordPath("claude")} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(obsolete))); !os.IsNotExist(err) {
			t.Fatalf("obsolete file %s still exists", obsolete)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(applyRecordPath("claude")))); err != nil {
		t.Fatal(err)
	}
}

func testAgent(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n")
	write(t, filepath.Join(root, "skills", "echo", "SKILL.md"), "---\nname: echo\ndescription: Repeat text safely.\n---\n\nUse echo.\n")
	write(t, filepath.Join(root, "skills", "echo", "references", "info.md"), "Echo returns bounded text.\n")
	script := filepath.Join(root, "skills", "echo", "scripts", "check.sh")
	write(t, script, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "subagents", "docs-reviewer", "instructions.md"), "---\ndescription: Review docs.\n---\n\nReview documentation.\n")
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeBytes(t *testing.T, root, path string, content []byte) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func snapshot(t *testing.T, root string, paths []string) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, path := range paths {
		result[path] = read(t, filepath.Join(root, filepath.FromSlash(path)))
	}
	return result
}
