package stage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hctl/internal/integration"
	"hctl/internal/project"
	"hctl/internal/rootfs"
)

func TestHarnessVersionDoesNotMutateConfiguredHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	executable := filepath.Join(t.TempDir(), "codex")
	writeTestFile(t, executable, "#!/bin/sh\nmkdir -p \"$HOME/.codex/tmp\"\n: > \"$HOME/.codex/tmp/arg0\"\necho 'codex-cli 1.2.3'\n", 0o755)
	version, err := HarnessVersion(context.Background(), executable)
	if err != nil {
		t.Fatal(err)
	}
	if version != "1.2.3" {
		t.Fatalf("version = %q", version)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("version inspection mutated configured home: %v", entries)
	}
}

func TestBuildPathDetectionRequiresAStandalonePathToken(t *testing.T) {
	buildPath := []byte("/agent")
	for _, data := range []string{
		"command = \"/agent/tool\"",
		"cwd=/agent",
	} {
		if !containsStandalonePath([]byte(data), buildPath) {
			t.Fatalf("build path was not detected in %q", data)
		}
	}
	for _, data := range []string{
		"command = \"/opt/hctl/agents/agent\"",
		"command = \"/prefix/agent/tool\"",
	} {
		if containsStandalonePath([]byte(data), buildPath) {
			t.Fatalf("embedded path was rejected in %q", data)
		}
	}
}

func TestCreateStagesToolFreeAgentDeterministically(t *testing.T) {
	source := filepath.Join(t.TempDir(), "sample-agent")
	writeTestFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Staged test agent.\n---\n\nBe concise.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	hctl := filepath.Join(bin, "hctl")
	harness := filepath.Join(bin, "codex")
	writeTestFile(t, hctl, "#!/bin/sh\nexit 0\n", 0o755)
	writeTestFile(t, harness, "#!/bin/sh\necho 'codex-cli 1.2.3'\n", 0o755)

	parent := t.TempDir()
	first := filepath.Join(parent, "first")
	second := filepath.Join(parent, "second")
	for _, output := range []string{first, second} {
		result, err := Create(context.Background(), Request{Project: p, Output: output, HCTLExecutable: hctl, HarnessExecutable: harness, HarnessVersion: "1.2.3"})
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(result.Output) != filepath.Base(output) || len(result.Manifest.Runtimes) != 0 {
			t.Fatalf("result = %#v", result)
		}
	}

	firstManifest := readTestFile(t, filepath.Join(first, filepath.FromSlash(manifestPath)))
	secondManifest := readTestFile(t, filepath.Join(second, filepath.FromSlash(manifestPath)))
	if !bytes.Equal(firstManifest, secondManifest) {
		t.Fatalf("manifest changed across identical staging:\n%s\n%s", firstManifest, secondManifest)
	}
	var manifest Manifest
	if err := json.Unmarshal(firstManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Paths.Agent != "/opt/hctl/agents/sample-agent" || manifest.Paths.Workspace != finalWorkspace || manifest.Paths.HarnessHome != finalHome || manifest.Paths.Harness != "/opt/hctl/harness/bin/codex" {
		t.Fatalf("paths = %#v", manifest.Paths)
	}
	if _, err := os.Stat(filepath.Join(first, "opt", "hctl", "runtimes")); !os.IsNotExist(err) {
		t.Fatalf("tool-free artifact has runtimes: %v", err)
	}
	config := readTestFile(t, filepath.Join(first, "workspace", ".codex", "config.toml"))
	for _, expected := range []string{finalHCTL, manifest.Paths.Agent, finalWorkspace} {
		if !bytes.Contains(config, []byte(expected)) {
			t.Fatalf("config lacks final path %q: %s", expected, config)
		}
	}
	for _, prohibited := range []string{source, first, second, parent} {
		if bytes.Contains(config, []byte(prohibited)) {
			t.Fatalf("config contains build path %q: %s", prohibited, config)
		}
	}
	entrypoint := string(readTestFile(t, filepath.Join(first, "opt", "hctl", "bin", "agent-entrypoint")))
	if !strings.Contains(entrypoint, "uid 65532 gid 65532") || !strings.Contains(entrypoint, "HOME=/home/hctl") || !strings.Contains(entrypoint, "hctl run /opt/hctl/agents/sample-agent") || !strings.Contains(entrypoint, "--command /opt/hctl/harness/bin/codex") {
		t.Fatalf("entrypoint = %q", entrypoint)
	}
	for _, file := range manifest.Files {
		data := readTestFile(t, filepath.Join(first, filepath.FromSlash(strings.TrimPrefix(file.Path, "/"))))
		if got := rootfs.SHA256(data); got != file.SHA256 {
			t.Fatalf("hash for %s = %s, want %s", file.Path, got, file.SHA256)
		}
		if file.Path == "/"+manifestPath {
			t.Fatal("manifest must not hash itself")
		}
	}
}

func TestCreateSelectivelyStagesGitHubNativeMCPClosure(t *testing.T) {
	const fakeValue = "conspicuous-stage-fake-pat"
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", fakeValue)
	source := filepath.Join(t.TempDir(), "github-agent")
	writeTestFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: GitHub staged test agent.\n---\n\nBe concise.\n", 0o644)
	writeTestFile(t, filepath.Join(source, "connections", "github.md"), "Inspect GitHub using discovered native tools.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	store := integration.NewStore(t.TempDir(), nil)
	packageRoot := testGitHubPackage(t)
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: packageRoot, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	hctl := filepath.Join(bin, "hctl")
	harness := filepath.Join(bin, "codex")
	writeTestFile(t, hctl, "#!/bin/sh\nexit 0\n", 0o755)
	writeTestFile(t, harness, "#!/bin/sh\necho 'codex-cli 1.2.3'\n", 0o755)
	output := filepath.Join(t.TempDir(), "staged")
	if _, err := Create(context.Background(), Request{Project: p, Output: output, HCTLExecutable: hctl, HarnessExecutable: harness, HarnessVersion: "1.2.3", IntegrationStore: store}); err != nil {
		t.Fatal(err)
	}
	config := readTestFile(t, filepath.Join(output, "workspace", ".codex", "config.toml"))
	for _, fragment := range []string{`[mcp_servers."github"]`, `/opt/hctl/integrations/github-mcp-server/`, `args = ["stdio"]`, `env_vars = ["GITHUB_PERSONAL_ACCESS_TOKEN"]`} {
		if !bytes.Contains(config, []byte(fragment)) {
			t.Fatalf("staged config omitted %q: %s", fragment, config)
		}
	}
	if bytes.Contains(config, []byte(fakeValue)) {
		t.Fatal("staged config retained the resolved ambient value")
	}
	matches, err := filepath.Glob(filepath.Join(output, "opt", "hctl", "integrations", "github-mcp-server", "*", runtime.GOOS+"-"+runtime.GOARCH, "github-mcp-server"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("selective GitHub closure = %v, %v", matches, err)
	}
	if err := filepath.Walk(output, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(fakeValue)) {
			t.Fatalf("resolved ambient value entered staged file %s", path)
		}
		return readErr
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(source, "connections")); err != nil {
		t.Fatal(err)
	}
	withoutGitHub, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	withoutOutput := filepath.Join(t.TempDir(), "staged-without-github")
	if _, err := Create(context.Background(), Request{Project: withoutGitHub, Output: withoutOutput, HCTLExecutable: hctl, HarnessExecutable: harness, HarnessVersion: "1.2.3", IntegrationStore: store}); err != nil {
		t.Fatal(err)
	}
	withoutConfig := readTestFile(t, filepath.Join(withoutOutput, "workspace", ".codex", "config.toml"))
	if bytes.Contains(withoutConfig, []byte(`[mcp_servers."github"]`)) || bytes.Contains(withoutConfig, []byte("GITHUB_PERSONAL_ACCESS_TOKEN")) {
		t.Fatalf("GitHub-free counterpart contains native GitHub configuration: %s", withoutConfig)
	}
	if _, err := os.Stat(filepath.Join(withoutOutput, "opt", "hctl", "integrations")); !os.IsNotExist(err) {
		t.Fatalf("GitHub-free counterpart staged integration artifacts: %v", err)
	}
}

func TestCreateSelectivelyStagesDiscordAdapterAndOmitsItWithoutChannel(t *testing.T) {
	const fakeToken = "conspicuous-stage-fake-discord-token"
	t.Setenv("HCTL_DISCORD_TOKEN", fakeToken)
	source := filepath.Join(t.TempDir(), "discord-agent")
	writeTestFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Discord staged test agent.\n---\n\nBe concise.\n", 0o644)
	writeTestFile(t, filepath.Join(source, "channels", "discord.md"), "---\nmode: ambient\n---\n\nParticipate in review work.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	store := integration.NewStore(t.TempDir(), nil)
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: testDiscordPackage(t), Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	hctl := filepath.Join(bin, "hctl")
	harness := filepath.Join(bin, "codex")
	writeTestFile(t, hctl, "#!/bin/sh\nexit 0\n", 0o755)
	writeTestFile(t, harness, "#!/bin/sh\necho 'codex-cli 1.2.3'\n", 0o755)
	output := filepath.Join(t.TempDir(), "staged")
	created, err := Create(context.Background(), Request{Project: p, Output: output, HCTLExecutable: hctl, HarnessExecutable: harness, HarnessVersion: "1.2.3", IntegrationStore: store})
	if err != nil {
		t.Fatal(err)
	}
	descriptor := filepath.Join(output, "opt", "hctl", "integrations", "channel-adapter.json")
	if created.Manifest.Agent.ID == p.AgentID {
		t.Fatal("staged agent identity did not relocate to the canonical runtime source")
	}
	resolved, err := integration.LoadStagedChannelAdapter(descriptor, created.Manifest.Agent.ID, created.Manifest.Agent.SourceFingerprint, "discord")
	if err != nil {
		t.Fatal(err)
	}
	launch, err := resolved.LaunchDescriptor(integration.ChannelAdapterRuntime, "")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(launch.Command, launch.Arguments...)
	command.Dir = launch.WorkingDirectory
	result, err := command.Output()
	if err != nil || string(result) != "adapter:run --stdio" {
		t.Fatalf("staged adapter launch = %q, %v", result, err)
	}
	entrypoint := readTestFile(t, filepath.Join(output, "opt", "hctl", "bin", "agent-entrypoint"))
	if !bytes.Contains(entrypoint, []byte("export "+integration.StagedChannelAdapterEnvironment+"=/opt/hctl/integrations/channel-adapter.json")) {
		t.Fatalf("Discord staged entrypoint omitted adapter descriptor: %s", entrypoint)
	}
	if err := filepath.Walk(output, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Contains(data, []byte(fakeToken)) {
			t.Fatalf("Discord credential entered staged file %s", path)
		}
		return readErr
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(source, "channels")); err != nil {
		t.Fatal(err)
	}
	withoutDiscord, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	withoutOutput := filepath.Join(t.TempDir(), "staged-without-discord")
	if _, err := Create(context.Background(), Request{Project: withoutDiscord, Output: withoutOutput, HCTLExecutable: hctl, HarnessExecutable: harness, HarnessVersion: "1.2.3", IntegrationStore: store}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(withoutOutput, "opt", "hctl", "integrations")); !os.IsNotExist(err) {
		t.Fatalf("Discord-free counterpart staged integration artifacts: %v", err)
	}
	withoutEntrypoint := readTestFile(t, filepath.Join(withoutOutput, "opt", "hctl", "bin", "agent-entrypoint"))
	if bytes.Contains(withoutEntrypoint, []byte(integration.StagedChannelAdapterEnvironment)) {
		t.Fatalf("Discord-free entrypoint retained staged adapter locator: %s", withoutEntrypoint)
	}
}

func testGitHubPackage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	payload := []byte("#!/bin/sh\nexit 0\n")
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	artifactID := runtime.GOOS + "-" + runtime.GOARCH
	otherOS, otherArch := "linux", "amd64"
	if runtime.GOOS == otherOS && runtime.GOARCH == otherArch {
		otherOS, otherArch = "darwin", "arm64"
	}
	otherArtifactID := otherOS + "-" + otherArch
	artifact := func(id, targetOS, targetArch string) map[string]any {
		return map[string]any{
			"id": id, "os": targetOS, "architecture": targetArch, "format": "binary",
			"source": map[string]any{"kind": "package", "path": "payload/github-mcp-server"}, "size": len(payload), "sha256": checksum,
			"executable": map[string]any{"path": "github-mcp-server", "size": len(payload), "sha256": checksum},
		}
	}
	document := map[string]any{
		"schema_version": 1, "id": "github-mcp-server", "version": "1.8.0", "name": "Fake GitHub MCP", "description": "Credential-free native MCP fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://github.com/github/github-mcp-server", "revision": "v1.8.0"},
		"compatibility": map[string]any{"minimum": "0.1.0-dev", "before": "9.0.0"},
		"artifacts":     []any{artifact(artifactID, runtime.GOOS, runtime.GOARCH), artifact(otherArtifactID, otherOS, otherArch)},
		"capabilities": []any{map[string]any{
			"type": "native-mcp", "version": 1, "id": "github", "server_name": "github", "collision": "reject", "artifacts": []string{artifactID, otherArtifactID}, "executable": "github-mcp-server",
			"arguments": []string{"stdio"}, "working_directory": ".", "environment": map[string]string{},
			"required_environment": []any{map[string]any{"name": "GITHUB_PERSONAL_ACCESS_TOKEN", "description": "Ambient authentication required at runtime."}},
			"harnesses":            []any{map[string]any{"name": "codex", "startup": "optional", "trust": "native-project"}},
		}},
	}
	manifest, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "integration.json"), string(manifest)+"\n", 0o600)
	writeTestFile(t, filepath.Join(root, "payload", "github-mcp-server"), string(payload), 0o600)
	return root
}

func testDiscordPackage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	payload := []byte("#!/bin/sh\nprintf 'adapter:%s' \"$*\"\n")
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	artifactID := runtime.GOOS + "-" + runtime.GOARCH
	document := map[string]any{
		"schema_version": 1, "id": "hctl-discord", "version": "1.0.0", "name": "Fake Discord adapter", "description": "Credential-free staged channel fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://example.invalid/hctl-discord", "revision": "fixture-v1"},
		"compatibility": map[string]any{"minimum": "0.1.0-dev", "before": "9.0.0"},
		"artifacts": []any{map[string]any{
			"id": artifactID, "os": runtime.GOOS, "architecture": runtime.GOARCH, "format": "binary",
			"source": map[string]any{"kind": "package", "path": "payload/hctl-discord"}, "size": len(payload), "sha256": checksum,
			"executable": map[string]any{"path": "bin/hctl-discord", "size": len(payload), "sha256": checksum},
		}},
		"capabilities": []any{map[string]any{
			"type": "channel-adapter", "version": 1, "id": "discord", "channel_kind": "discord", "artifacts": []string{artifactID}, "executable": "bin/hctl-discord",
			"runtime": map[string]any{"arguments": []string{"run", "--stdio"}}, "setup": map[string]any{"arguments": []string{"setup"}}, "status": map[string]any{"arguments": []string{"status"}}, "remove": map[string]any{"arguments": []string{"remove"}},
			"protocol": map[string]any{"minimum": 1, "before": 2}, "profile_selector": "opaque-id-v1", "features": []string{"typing", "replies", "edits", "reactions", "attachments", "interactive-components", "text-fallback"},
		}},
	}
	manifest, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "integration.json"), string(manifest)+"\n", 0o600)
	writeTestFile(t, filepath.Join(root, "payload", "hctl-discord"), string(payload), 0o600)
	return root
}

func TestCreateRejectsUnsafeOutputWithoutPartialArtifact(t *testing.T) {
	source := filepath.Join(t.TempDir(), "sample-agent")
	writeTestFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Staged test agent.\n---\n\nBe concise.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	hctl := filepath.Join(bin, "hctl")
	harness := filepath.Join(bin, "codex")
	writeTestFile(t, hctl, "#!/bin/sh\nexit 0\n", 0o755)
	writeTestFile(t, harness, "#!/bin/sh\necho 'codex-cli 1.2.3'\n", 0o755)

	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{existing, filepath.Join(source, "artifact")} {
		_, err := Create(context.Background(), Request{Project: p, Output: output, HCTLExecutable: hctl, HarnessExecutable: harness, HarnessVersion: "1.2.3"})
		if err == nil {
			t.Fatalf("unsafe output %q was accepted", output)
		}
	}

	output := filepath.Join(t.TempDir(), "artifact")
	writeTestFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Changed.\n---\n\nChanged.\n", 0o644)
	_, err = Create(context.Background(), Request{Project: p, Output: output, HCTLExecutable: hctl, HarnessExecutable: harness, HarnessVersion: "1.2.3"})
	if err == nil || !strings.Contains(err.Error(), "changed before staging") {
		t.Fatalf("changed source error = %v", err)
	}
	if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
		t.Fatalf("failed staging published output: %v", statErr)
	}
}

func TestRemovePrivateBuildTreeHandlesReadOnlyToolCaches(t *testing.T) {
	root := filepath.Join(t.TempDir(), "build-home")
	directory := filepath.Join(root, "go", "pkg", "mod", "example@v1")
	writeTestFile(t, filepath.Join(directory, "source.go"), "package example\n", 0o444)
	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := removePrivateBuildTree(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("build tree remains: %v", err)
	}
}

func writeTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
