package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/acquisition"
	"hctl/internal/channelselection"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/integration"
	"hctl/internal/interaction"
	"hctl/internal/project"
	"hctl/internal/schedule"
	"hctl/internal/setup"
	"hctl/internal/version"
)

func TestVersionCommandsPrintBuildVersion(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		var output, stderr bytes.Buffer
		if err := Run(args, strings.NewReader(""), &output, &stderr, ""); err != nil {
			t.Fatalf("Run(%q): %v", args, err)
		}
		if got, want := output.String(), "hctl "+version.Value+"\n"; got != want {
			t.Fatalf("Run(%q) output = %q, want %q", args, got, want)
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%q) stderr = %q", args, stderr.String())
		}
	}
}

func TestFreshCopiedAcquiredDependenciesApplyWithoutOriginalSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	source := filepath.Join(t.TempDir(), "agent")
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	externalParent := t.TempDir()
	external := filepath.Join(externalParent, "review")
	writeCLIFile(t, filepath.Join(external, "SKILL.md"), "---\nname: review\ndescription: Review carefully.\n---\n\nReview.\n", 0o644)
	writeCLIFile(t, filepath.Join(external, "references", "binary"), string([]byte{0, 1, 2, 255}), 0o644)
	manager := project.AcquisitionManager(source)
	if _, err := manager.Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: external}); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(externalParent, "review-pack")
	writeCLIFile(t, filepath.Join(plugin, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"review-pack"}`, 0o644)
	writeCLIFile(t, filepath.Join(plugin, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"review-server":{"type":"stdio","command":"./server.sh"}}}`, 0o644)
	writeCLIFile(t, filepath.Join(plugin, "server.sh"), "#!/bin/sh\n", 0o755)
	writeCLIFile(t, filepath.Join(plugin, "binary"), string([]byte{0, 1, 2, 255}), 0o644)
	if err := os.Mkdir(filepath.Join(plugin, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(plugin, "skills", "plugin-review", "SKILL.md"), "---\nname: plugin-review\ndescription: Review from the Plugin.\n---\n", 0o644)
	if err := Run([]string{"plugin", "add", source, "--from-dir", plugin, "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	if got := readCLIFile(t, filepath.Join(source, "plugins", "review-pack", "plugin.json")); got != `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"review-pack"}` {
		t.Fatalf("acquired Plugin manifest changed: %q", got)
	}
	fresh := filepath.Join(t.TempDir(), "fresh-agent")
	copyCLITree(t, source, fresh)
	if err := os.RemoveAll(externalParent); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name, version, skillRoot string
	}{{"claude", "2.1.221 (Claude Code)", ".claude"}, {"codex", "codex-cli 0.144.1", ".agents"}} {
		t.Run(fixture.name, func(t *testing.T) {
			workspace := t.TempDir()
			harness := filepath.Join(t.TempDir(), fixture.name)
			writeCLIFile(t, harness, "#!/bin/sh\necho '"+fixture.version+"'\n", 0o755)
			var output, stderr bytes.Buffer
			if err := Run([]string{"apply", fresh, "--workspace", workspace, "--harness", fixture.name, "--command", harness}, strings.NewReader(""), &output, &stderr, self); err != nil {
				t.Fatal(err)
			}
			if got := readCLIFile(t, filepath.Join(workspace, fixture.skillRoot, "skills", "review", "references", "binary")); got != string([]byte{0, 1, 2, 255}) {
				t.Fatalf("fresh copied acquired resource = %q", got)
			}
			if got := readCLIFile(t, filepath.Join(workspace, fixture.skillRoot, "skills", "plugin-review", "SKILL.md")); !strings.Contains(got, "Review from the Plugin") {
				t.Fatalf("fresh copied Plugin Skill = %q", got)
			}
			serverPath := filepath.Join(fresh, "plugins", "review-pack", "server.sh")
			if info, err := os.Stat(serverPath); err != nil || info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("fresh acquired Plugin server = %v, %v", info, err)
			}
			if got := readCLIFile(t, filepath.Join(fresh, "plugins", "review-pack", "binary")); got != string([]byte{0, 1, 2, 255}) {
				t.Fatalf("fresh acquired Plugin binary = %q", got)
			}
			if info, err := os.Stat(filepath.Join(fresh, "plugins", "review-pack", "empty")); err != nil || !info.IsDir() {
				t.Fatalf("fresh acquired Plugin empty directory = %v, %v", info, err)
			}
			configPath := filepath.Join(workspace, ".mcp.json")
			if fixture.name == "codex" {
				configPath = filepath.Join(workspace, ".codex", "config.toml")
			}
			config := readCLIFile(t, configPath)
			if !strings.Contains(config, "review-server") || !strings.Contains(config, serverPath) {
				t.Fatalf("fresh acquired Plugin MCP config omitted server identity or path: %s", config)
			}
		})
	}
}

func TestPluginConsumerCommandsUseSharedAcquisitionWorkflow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	agent := filepath.Join(t.TempDir(), "agent")
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Plugin consumer.\n---\n\nUse Plugins.\n", 0o644)
	writeCLIFile(t, filepath.Join(agent, "plugins", "z-manual", "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"manual-pack"}`, 0o644)
	source := filepath.Join(t.TempDir(), "review-pack")
	writeCLIPluginFixture(t, source, "one\n")

	var output, stderr bytes.Buffer
	if err := Run([]string{"plugin", "add", agent, "--from-dir", source}, strings.NewReader("yes\n"), &output, &stderr, ""); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("noninteractive add did not fail closed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(agent, "plugins", "review-pack")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("noninteractive failure changed source: %v", err)
	}
	output.Reset()
	stderr.Reset()
	terminal := &markedTerminalReader{Reader: strings.NewReader("yes\n")}
	if err := Run([]string{"plugin", "add", agent, "--from-dir", source}, terminal, &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `added plugin="review-pack"`) || !strings.Contains(stderr.String(), "executables=1") || !strings.Contains(stderr.String(), "plugin_skills=1") || !strings.Contains(stderr.String(), "plugin_mcp_servers=1") || !strings.Contains(stderr.String(), `executable="bin/server"`) || !strings.Contains(stderr.String(), "Acquire this Agent Plugin?") {
		t.Fatalf("add output omitted trust or result evidence: stdout=%q stderr=%q", output.String(), stderr.String())
	}

	output.Reset()
	stderr.Reset()
	if err := Run([]string{"plugin", "status", agent}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	status := output.String()
	if !strings.Contains(status, `plugin="review-pack" manifest="review-pack" state=clean`) || !strings.Contains(status, "source_type=local") || !strings.Contains(status, `plugin="z-manual" manifest="manual-pack" state=untracked`) || strings.Index(status, `plugin="review-pack"`) > strings.Index(status, `plugin="z-manual"`) {
		t.Fatalf("plugin status = %q", status)
	}
	terminal = &markedTerminalReader{Reader: strings.NewReader("no\n")}
	if err := Run([]string{"plugin", "remove", agent, "review-pack"}, terminal, io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("negative removal confirmation was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent, "plugins", "review-pack", "plugin.json")); err != nil {
		t.Fatalf("canceled removal changed the Plugin: %v", err)
	}

	writeCLIFile(t, filepath.Join(source, "resource.txt"), "two\n", 0o644)
	output.Reset()
	stderr.Reset()
	if err := Run([]string{"plugin", "update", agent, "review-pack", "--yes"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	got := readCLIFile(t, filepath.Join(agent, "plugins", "review-pack", "resource.txt"))
	if !strings.Contains(output.String(), `updated plugin="review-pack"`) || got != "two\n" {
		t.Fatalf("plugin update output=%q resource=%q", output.String(), got)
	}
	output.Reset()
	if err := Run([]string{"plugin", "update", agent, "review-pack", "--yes"}, strings.NewReader(""), &output, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `unchanged plugin="review-pack"`) {
		t.Fatalf("unchanged update output=%q", output.String())
	}
	if err := Run([]string{"plugin", "remove", agent, "z-manual", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "not recorded") {
		t.Fatalf("manual Plugin removal was not rejected: %v", err)
	}

	writeCLIFile(t, filepath.Join(agent, "plugins", "review-pack", "drift.txt"), "drift\n", 0o644)
	output.Reset()
	if err := Run([]string{"plugin", "status", agent, "review-pack"}, strings.NewReader(""), &output, &stderr, ""); err != nil || !strings.Contains(output.String(), "state=drifted") {
		t.Fatalf("drift status=%q err=%v", output.String(), err)
	}
	if err := Run([]string{"plugin", "remove", agent, "review-pack", "--force"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "--force requires --yes") {
		t.Fatalf("force removal did not require --yes: %v", err)
	}
	output.Reset()
	stderr.Reset()
	if err := Run([]string{"plugin", "remove", agent, "review-pack", "--force", "--yes"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `removed plugin="review-pack"`) || !strings.Contains(stderr.String(), "state=drifted") {
		t.Fatalf("remove output omitted target status/result: stdout=%q stderr=%q", output.String(), stderr.String())
	}
	if _, err := os.Lstat(filepath.Join(agent, "plugins", "review-pack")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed Plugin remains: %v", err)
	}
}

func TestPluginConsumerReportsAndForceRemovesMissingDependency(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	agent := filepath.Join(t.TempDir(), "agent")
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Plugin consumer.\n---\n\nUse Plugins.\n", 0o644)
	source := filepath.Join(t.TempDir(), "review-pack")
	writeCLIPluginFixture(t, source, "one\n")
	if err := Run([]string{"plugin", "add", agent, "--from-dir", source, "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(agent, "plugins", "review-pack")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run([]string{"plugin", "status", agent, "review-pack"}, strings.NewReader(""), &output, io.Discard, ""); err != nil || !strings.Contains(output.String(), `plugin="review-pack" manifest="review-pack" state=missing`) {
		t.Fatalf("missing status=%q err=%v", output.String(), err)
	}
	if err := Run([]string{"plugin", "remove", agent, "review-pack", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "explicit destructive removal") {
		t.Fatalf("ordinary missing removal was not rejected: %v", err)
	}
	if err := Run([]string{"plugin", "remove", agent, "review-pack", "--force", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
}

func TestSkillConsumerCommandsAndFreshApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	agent := filepath.Join(t.TempDir(), "agent")
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Skill consumer.\n---\n\nUse Skills.\n", 0o644)
	writeCLIFile(t, filepath.Join(agent, "skills", "manual", "SKILL.md"), "---\nname: manual\ndescription: Manual.\n---\n", 0o644)
	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "review")
	writeCLISkillFixture(t, source, "one\n")

	var output, stderr bytes.Buffer
	if err := Run([]string{"skill", "add", agent, "--from-dir", source, "--yes"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `added skill="review"`) || !strings.Contains(stderr.String(), "executables=1") || strings.Contains(stderr.String(), "plugin_skills") {
		t.Fatalf("Skill add output=%q stderr=%q", output.String(), stderr.String())
	}
	wantMarker := "---\nname: review\ndescription: Review carefully.\n---\n\nReview.\n"
	if got := readCLIFile(t, filepath.Join(agent, "skills", "review", "SKILL.md")); got != wantMarker {
		t.Fatalf("acquired SKILL.md changed: %q", got)
	}
	output.Reset()
	if err := Run([]string{"skill", "status", agent}, strings.NewReader(""), &output, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	if status := output.String(); !strings.Contains(status, `skill="review" skill_name="review" state=clean`) || !strings.Contains(status, `skill="manual" skill_name="manual" state=untracked`) || !strings.Contains(status, "source_type=local") {
		t.Fatalf("Skill status=%q", status)
	}
	writeCLIFile(t, filepath.Join(source, "references", "guide.txt"), "two\n", 0o644)
	output.Reset()
	if err := Run([]string{"skill", "update", agent, "review", "--yes"}, strings.NewReader(""), &output, io.Discard, ""); err != nil || !strings.Contains(output.String(), `updated skill="review"`) {
		t.Fatalf("Skill update=%q err=%v", output.String(), err)
	}
	output.Reset()
	if err := Run([]string{"skill", "update", agent, "review", "--yes"}, strings.NewReader(""), &output, io.Discard, ""); err != nil || !strings.Contains(output.String(), `unchanged skill="review"`) {
		t.Fatalf("unchanged Skill update=%q err=%v", output.String(), err)
	}

	fresh := filepath.Join(t.TempDir(), "fresh-agent")
	copyCLITree(t, agent, fresh)
	writeCLIFile(t, filepath.Join(agent, "skills", "review", "drift.txt"), "drift\n", 0o644)
	if err := Run([]string{"skill", "remove", agent, "review", "--force", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"skill", "add", agent, "--from-dir", source, "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(agent, "skills", "review")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Run([]string{"skill", "status", agent, "review"}, strings.NewReader(""), &output, io.Discard, ""); err != nil || !strings.Contains(output.String(), "state=missing") {
		t.Fatalf("missing Skill status=%q err=%v", output.String(), err)
	}
	if err := Run([]string{"skill", "remove", agent, "review", "--force", "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sourceParent); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(agent); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(fresh, "skills", "review", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("fresh acquired Skill empty directory=%v %v", info, err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ name, version, skillRoot string }{{"claude", "2.1.221 (Claude Code)", ".claude"}, {"codex", "codex-cli 0.144.1", ".agents"}} {
		t.Run(fixture.name, func(t *testing.T) {
			workspace := t.TempDir()
			harness := filepath.Join(t.TempDir(), fixture.name)
			writeCLIFile(t, harness, "#!/bin/sh\necho '"+fixture.version+"'\n", 0o755)
			if err := Run([]string{"apply", fresh, "--workspace", workspace, "--harness", fixture.name, "--command", harness}, strings.NewReader(""), io.Discard, io.Discard, self); err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(workspace, fixture.skillRoot, "skills", "review")
			if got := readCLIFile(t, filepath.Join(generated, "references", "binary")); got != string([]byte{0, 1, 2, 255}) {
				t.Fatalf("generated Skill binary=%q", got)
			}
			if info, err := os.Stat(filepath.Join(generated, "scripts", "review.sh")); err != nil || info.Mode().Perm()&0o111 == 0 {
				t.Fatalf("generated Skill executable=%v %v", info, err)
			}
		})
	}
}

func TestSkillCommandClosedGrammarCancellationAndPluginCollision(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	agent := filepath.Join(t.TempDir(), "agent")
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Skill consumer.\n---\n\nUse Skills.\n", 0o644)
	source := filepath.Join(t.TempDir(), "review")
	writeCLISkillFixture(t, source, "one\n")
	for _, fixture := range []struct {
		args []string
		want string
	}{
		{[]string{"skill", "add", agent, "--yes"}, "exactly one"},
		{[]string{"skill", "add", agent, "--from-git", "https://example.com/repo.git", "--yes"}, "requires exactly one"},
		{[]string{"skill", "add", agent, "--from-archive", "https://example.com/review.zip", "--yes"}, "requires exactly one"},
		{[]string{"skill", "update", agent, "review", "--subdir", "nested", "--yes"}, "require a source selector"},
	} {
		if err := Run(fixture.args, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), fixture.want) {
			t.Fatalf("Skill selector error=%v for %v", err, fixture.args)
		}
	}
	if err := Run([]string{"skill", "add", agent, "--from-dir", source}, strings.NewReader("yes\n"), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("noninteractive Skill add did not fail closed: %v", err)
	}
	terminal := &markedTerminalReader{Reader: strings.NewReader("no\n")}
	if err := Run([]string{"skill", "add", agent, "--from-dir", source}, terminal, io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("Skill cancellation=%v", err)
	}
	writeCLIFile(t, filepath.Join(agent, "plugins", "pack", "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"pack"}`, 0o644)
	writeCLIFile(t, filepath.Join(agent, "plugins", "pack", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Plugin Skill.\n---\n", 0o644)
	if err := Run([]string{"skill", "add", agent, "--from-dir", source, "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("Plugin Skill collision=%v", err)
	}
	if err := os.MkdirAll(filepath.Join(agent, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"skill", "status", filepath.Join(agent, "skills")}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("Skill status inferred an agent root: %v", err)
	}
}

func TestPluginCommandSelectorGrammarAndCancellation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	agent := filepath.Join(t.TempDir(), "agent")
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Plugin consumer.\n---\n\nUse Plugins.\n", 0o644)
	source := filepath.Join(t.TempDir(), "review-pack")
	writeCLIPluginFixture(t, source, "one\n")
	digest := strings.Repeat("a", 64)
	for _, fixture := range []struct {
		name string
		args []string
		want string
	}{
		{"missing selector", []string{"plugin", "add", agent, "--yes"}, "exactly one"},
		{"multiple selectors", []string{"plugin", "add", agent, "--from-dir", source, "--from-git", "https://example.com/repo.git", "--ref", "main", "--yes"}, "mutually exclusive"},
		{"git ref", []string{"plugin", "add", agent, "--from-git", "https://example.com/repo.git", "--yes"}, "requires exactly one"},
		{"archive digest", []string{"plugin", "add", agent, "--from-archive", "https://example.com/review.zip", "--yes"}, "requires exactly one"},
		{"local ref", []string{"plugin", "add", agent, "--from-dir", source, "--ref", "main", "--yes"}, "does not accept"},
		{"empty subdirectory", []string{"plugin", "add", agent, "--from-dir", source, "--subdir", "", "--yes"}, "must be nonempty"},
		{"duplicate selector flag", []string{"plugin", "add", agent, "--from-dir", source, "--from-dir", source, "--yes"}, "at most once"},
		{"update orphan subdir", []string{"plugin", "update", agent, "review-pack", "--subdir", "nested", "--yes"}, "require a source selector"},
		{"archive ref", []string{"plugin", "add", agent, "--from-archive", "https://example.com/review.zip", "--sha256", digest, "--ref", "main", "--yes"}, "does not accept"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if err := Run(fixture.args, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), fixture.want) {
				t.Fatalf("selector error = %v", err)
			}
		})
	}

	var stderr bytes.Buffer
	terminal := &markedTerminalReader{Reader: strings.NewReader("no\n")}
	if err := Run([]string{"plugin", "add", agent, "--from-dir", source}, terminal, io.Discard, &stderr, ""); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("negative confirmation was not preserved: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(agent, "plugins", "review-pack")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled add changed source: %v", err)
	}
	if !strings.Contains(stderr.String(), "source_type=local") || !strings.Contains(stderr.String(), "[y/N]") {
		t.Fatalf("confirmation omitted bounded summary or prompt: %q", stderr.String())
	}
	writeCLIFile(t, filepath.Join(agent, "skills", "plugin-review", "SKILL.md"), "---\nname: plugin-review\ndescription: Existing.\n---\n", 0o644)
	if err := Run([]string{"plugin", "add", agent, "--from-dir", source, "--yes"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("Plugin component collision was not rejected: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(agent, "plugins", "review-pack")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("colliding add changed source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(agent, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"plugin", "status", filepath.Join(agent, "plugins")}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("status inferred an agent root: %v", err)
	}
}

func TestPluginSourceFlagsProduceSharedTypedSelectors(t *testing.T) {
	for _, fixture := range []struct {
		name      string
		arguments []string
		wantType  acquisition.SourceType
		wantValue string
	}{
		{"local", []string{"--from-dir", "/catalog/review-pack", "--subdir", "plugin"}, acquisition.SourceLocal, "/catalog/review-pack"},
		{"git", []string{"--from-git", "https://example.com/catalog.git", "--ref", "main", "--subdir", "plugins/review-pack"}, acquisition.SourceGit, "https://example.com/catalog.git"},
		{"archive", []string{"--from-archive", "https://example.com/review-pack.zip", "--sha256", strings.Repeat("a", 64)}, acquisition.SourceArchive, "https://example.com/review-pack.zip"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			flags := flag.NewFlagSet("source", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			sourceFlags := addAcquisitionSourceFlags(flags)
			if err := flags.Parse(fixture.arguments); err != nil {
				t.Fatal(err)
			}
			selector, err := sourceFlags.selector(flags, true)
			if err != nil {
				t.Fatal(err)
			}
			value := selector.Path
			if selector.Type != acquisition.SourceLocal {
				value = selector.URL
			}
			if selector.Type != fixture.wantType || value != fixture.wantValue {
				t.Fatalf("selector = %#v", selector)
			}
		})
	}
}

func TestRemoteComponentCommandsUseSharedManager(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "review-pack")
	writeCLIPluginFixture(t, source, "remote\n")
	sourceTree, err := acquisition.ReadTree(source)
	if err != nil {
		t.Fatal(err)
	}
	skillSource := filepath.Join(t.TempDir(), "review")
	writeCLISkillFixture(t, skillSource, "remote\n")
	skillTree, err := acquisition.ReadTree(skillSource)
	if err != nil {
		t.Fatal(err)
	}
	archives := map[string][]byte{
		"review-pack.zip":    cliComponentZIP(t, source, "review-pack"),
		"review-pack.tar.gz": cliComponentTarGzip(t, source, "review-pack"),
		"review.zip":         cliComponentZIP(t, skillSource, "review"),
		"review.tar.gz":      cliComponentTarGzip(t, skillSource, "review"),
	}

	webRoot := t.TempDir()
	repository := filepath.Join(webRoot, "component.git")
	runCLIGit(t, "", "init", "--bare", repository)
	work := filepath.Join(t.TempDir(), "work")
	runCLIGit(t, "", "init", "-b", "main", work)
	runCLIGit(t, work, "config", "user.name", "Fixture")
	runCLIGit(t, work, "config", "user.email", "fixture@example.com")
	copyCLITree(t, source, filepath.Join(work, "review-pack"))
	copyCLITree(t, skillSource, filepath.Join(work, "review"))
	if err := os.Remove(filepath.Join(work, "review-pack", "empty")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(work, "review", "empty")); err != nil {
		t.Fatal(err)
	}
	runCLIGit(t, work, "add", "review-pack", "review")
	runCLIGit(t, work, "commit", "-m", "fixture")
	runCLIGit(t, work, "remote", "add", "origin", repository)
	runCLIGit(t, work, "push", "origin", "main")

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if payload, ok := archives[strings.TrimPrefix(request.URL.Path, "/")]; ok {
			_, _ = writer.Write(payload)
			return
		}
		command := exec.Command("git", "http-backend")
		command.Env = append(os.Environ(),
			"GIT_PROJECT_ROOT="+webRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO="+request.URL.Path,
			"REQUEST_METHOD="+request.Method,
			"QUERY_STRING="+request.URL.RawQuery,
			"CONTENT_TYPE="+request.Header.Get("Content-Type"),
			fmt.Sprintf("CONTENT_LENGTH=%d", request.ContentLength),
		)
		command.Stdin = request.Body
		response, err := command.Output()
		if err != nil {
			http.Error(writer, "backend failed", http.StatusInternalServerError)
			return
		}
		head, body, ok := bytes.Cut(response, []byte("\r\n\r\n"))
		if !ok {
			http.Error(writer, "backend response invalid", http.StatusInternalServerError)
			return
		}
		status := http.StatusOK
		for _, line := range bytes.Split(head, []byte("\r\n")) {
			key, value, found := bytes.Cut(line, []byte(":"))
			if !found {
				continue
			}
			if strings.EqualFold(string(key), "Status") {
				_, _ = fmt.Sscanf(strings.TrimSpace(string(value)), "%d", &status)
				continue
			}
			writer.Header().Add(string(key), strings.TrimSpace(string(value)))
		}
		writer.WriteHeader(status)
		_, _ = writer.Write(body)
	}))
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "ca.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	writeCLIFile(t, certificate, string(certificatePEM), 0o600)
	t.Setenv("GIT_SSL_CAINFO", certificate)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	fixtures := []struct {
		name, noun, component string
		args                  []string
		expected              acquisition.Tree
	}{
		{"plugin git", "plugin", "review-pack", []string{"--from-git", server.URL + "/component.git", "--ref", "refs/heads/main", "--subdir", "review-pack"}, mustReadCLITree(t, filepath.Join(work, "review-pack"))},
		{"plugin zip", "plugin", "review-pack", []string{"--from-archive", server.URL + "/review-pack.zip", "--sha256", cliSHA256(archives["review-pack.zip"]), "--subdir", "review-pack"}, sourceTree},
		{"plugin tar.gz", "plugin", "review-pack", []string{"--from-archive", server.URL + "/review-pack.tar.gz", "--sha256", cliSHA256(archives["review-pack.tar.gz"]), "--subdir", "review-pack"}, sourceTree},
		{"skill git", "skill", "review", []string{"--from-git", server.URL + "/component.git", "--ref", "refs/heads/main", "--subdir", "review"}, mustReadCLITree(t, filepath.Join(work, "review"))},
		{"skill zip", "skill", "review", []string{"--from-archive", server.URL + "/review.zip", "--sha256", cliSHA256(archives["review.zip"]), "--subdir", "review"}, skillTree},
		{"skill tar.gz", "skill", "review", []string{"--from-archive", server.URL + "/review.tar.gz", "--sha256", cliSHA256(archives["review.tar.gz"]), "--subdir", "review"}, skillTree},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			agent := filepath.Join(t.TempDir(), "agent")
			writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Remote Plugin.\n---\n\nUse it.\n", 0o644)
			arguments := append([]string{fixture.noun, "add", agent}, fixture.args...)
			arguments = append(arguments, "--yes")
			input := &acquisitionTransportReader{Reader: strings.NewReader(""), transport: server.Client().Transport}
			if err := Run(arguments, input, io.Discard, io.Discard, ""); err != nil {
				t.Fatal(err)
			}
			installed, err := acquisition.ReadTree(filepath.Join(agent, fixture.noun+"s", fixture.component))
			if err != nil {
				t.Fatal(err)
			}
			if installed.SHA256 != fixture.expected.SHA256 || installed.FileCount != fixture.expected.FileCount || installed.ByteCount != fixture.expected.ByteCount {
				t.Fatalf("%s command changed the complete Plugin tree: source=%#v installed=%#v", fixture.name, fixture.expected, installed)
			}
		})
	}
}

func TestHeadlessCommandIsNamedRun(t *testing.T) {
	var output, stderr bytes.Buffer
	if err := Run([]string{"run", "--help"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Usage: hctl run AGENT") || !strings.Contains(got, "--idle-timeout DURATION") || !strings.Contains(got, "--max-resident-sessions N") || !strings.Contains(got, "--max-active-turns N") || strings.Contains(got, "gateway") {
		t.Fatalf("run help = %q", got)
	}

	output.Reset()
	err := Run([]string{"gateway"}, strings.NewReader(""), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), `unknown command "gateway"`) {
		t.Fatalf("legacy gateway command error = %v", err)
	}
}

func TestIntegrationPackageCLIJourneyIsExplicitAndContentFree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "must-not-appear")
	packageRoot := t.TempDir()
	payload := []byte("#!/bin/sh\necho fixture\n")
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	document := map[string]any{
		"schema_version": 1, "id": "cli-fixture", "version": "1.0.0", "name": "CLI fixture", "description": "Credentialless CLI package fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://example.invalid/cli-fixture", "revision": "fixture-v1"},
		"compatibility": map[string]any{"minimum": "0.1.0-dev", "before": "9.0.0"},
		"artifacts": []any{map[string]any{
			"id": "current", "os": runtime.GOOS, "architecture": runtime.GOARCH, "format": "binary",
			"source": map[string]any{"kind": "package", "path": "payload/server"}, "size": len(payload), "sha256": checksum,
			"executable": map[string]any{"path": "bin/server", "size": len(payload), "sha256": checksum},
		}},
		"capabilities": []any{map[string]any{
			"type": "native-mcp", "version": 1, "id": "fixture", "server_name": "fixture", "collision": "reject", "artifacts": []string{"current"}, "executable": "bin/server",
			"arguments": []string{}, "working_directory": ".", "environment": map[string]string{},
			"required_environment": []any{map[string]any{"name": "GITHUB_PERSONAL_ACCESS_TOKEN", "description": "Ambient authentication required at runtime."}},
			"harnesses":            []any{map[string]any{"name": "codex", "startup": "optional", "trust": "native-project"}},
		}},
	}
	manifest, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(packageRoot, "integration.json"), string(manifest)+"\n", 0o600)
	writeCLIFile(t, filepath.Join(packageRoot, "payload", "server"), string(payload), 0o600)

	var output, stderr bytes.Buffer
	if err := runIntegration([]string{"install", packageRoot}, &output, &stderr); err == nil || !strings.Contains(err.Error(), "--trust operator") {
		t.Fatalf("implicit trust error = %v", err)
	}
	if err := runIntegration([]string{"install", packageRoot, "--trust", "operator"}, &output, &stderr); err != nil {
		t.Fatalf("install error = %v", err)
	}
	if !strings.Contains(output.String(), "installed integration=cli-fixture") {
		t.Fatalf("install output = %q", output.String())
	}
	output.Reset()
	if err := runIntegration([]string{"inspect", "cli-fixture"}, &output, &stderr); err != nil {
		t.Fatalf("inspect error = %v", err)
	}
	if got := output.String(); !strings.Contains(got, "required_environment=GITHUB_PERSONAL_ACCESS_TOKEN") || !strings.Contains(got, "value=not-read") || strings.Contains(got, "must-not-appear") {
		t.Fatalf("inspect output = %q", got)
	}
	output.Reset()
	if err := runIntegration([]string{"verify", "cli-fixture"}, &output, &stderr); err != nil || !strings.Contains(output.String(), "verified integration=cli-fixture") {
		t.Fatalf("verify = %q, %v", output.String(), err)
	}
	if err := runIntegration([]string{"disable", "cli-fixture"}, io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := runIntegration([]string{"enable", "cli-fixture"}, io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := runIntegration([]string{"remove", "cli-fixture"}, io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionCLIAuthorsInspectsAndRemovesGenericSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := integration.NewDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := writeCLIGitHubPackage(t, "1.8.0", []byte("#!/bin/sh\nexit 0\n"))
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: packageRoot, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(t.TempDir(), "agent")
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)

	var output, stderr bytes.Buffer
	if err := Run([]string{"connection", "add", agent, "github", "--package", "github-mcp-server", "--capability", "github", "--context", "Use discovered GitHub tools."}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if got := readCLIFile(t, filepath.Join(agent, "connections", "github.md")); got != "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nUse discovered GitHub tools.\n" {
		t.Fatalf("installed source = %q", got)
	}
	if !strings.Contains(output.String(), "next: hctl apply "+agent+" --harness claude") || !strings.Contains(output.String(), "next: hctl apply "+agent+" --harness codex") {
		t.Fatalf("add output = %q", output.String())
	}
	output.Reset()
	if err := Run([]string{"connection", "add", agent, "public", "--url", "https://127.0.0.1:1/mcp"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if got := readCLIFile(t, filepath.Join(agent, "connections", "public.md")); got != "---\ntype: mcp\ntransport: streamable-http\nurl: https://127.0.0.1:1/mcp\n---\n" {
		t.Fatalf("remote source = %q", got)
	}

	output.Reset()
	if err := Run([]string{"connection", "status", agent}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	status := output.String()
	installed := "connection=github target=installed package=github-mcp-server capability=github status=ready harnesses=claude,codex context=present"
	remote := "connection=public target=remote transport=streamable-http url=https://127.0.0.1:1/mcp status=configured runtime=unchecked context=absent"
	if !strings.Contains(status, installed) || !strings.Contains(status, remote) || strings.Index(status, "connection=github") > strings.Index(status, "connection=public") {
		t.Fatalf("connection status = %q", status)
	}
	before := readCLIFile(t, filepath.Join(agent, "connections", "public.md"))
	if err := Run([]string{"connection", "add", agent, "public", "--url", "https://example.com/other"}, strings.NewReader(""), io.Discard, &stderr, ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwrite error = %v", err)
	}
	if after := readCLIFile(t, filepath.Join(agent, "connections", "public.md")); after != before {
		t.Fatal("failed add changed existing source")
	}
	if err := Run([]string{"connection", "add", agent, "wrong", "--package", "github-mcp-server", "--capability", "github"}, strings.NewReader(""), io.Discard, &stderr, ""); err == nil || !strings.Contains(err.Error(), "must equal connection name") {
		t.Fatalf("server-name mismatch error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent, "connections", "wrong.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched add wrote source: %v", err)
	}
	if err := Run([]string{"connection", "add", agent, "bad", "--url", "https://example.com/mcp?token=nope"}, strings.NewReader(""), io.Discard, &stderr, ""); err == nil || !strings.Contains(err.Error(), "absolute HTTPS URL") {
		t.Fatalf("bad URL error = %v", err)
	}

	if err := store.SetEnabled(context.Background(), "github-mcp-server", false); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Run([]string{"connection", "status", agent, "github"}, strings.NewReader(""), &output, &stderr, ""); err == nil || !strings.Contains(err.Error(), "connections/github.md") || !strings.Contains(output.String(), "status=unhealthy") {
		t.Fatalf("unhealthy status = %q, %v", output.String(), err)
	}
	output.Reset()
	if err := Run([]string{"connection", "remove", agent, "github"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(agent, "connections", "github.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unhealthy source was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent, "connections")); err != nil {
		t.Fatalf("remove deleted connections directory: %v", err)
	}
	if err := Run([]string{"connection", "status", filepath.Join(agent, "connections")}, strings.NewReader(""), io.Discard, &stderr, ""); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("inferred-root status error = %v", err)
	}
}

func TestConnectionAddRejectsPluginCollisionAndRemoveRejectsUnsafeTarget(t *testing.T) {
	agent := t.TempDir()
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	plugin := filepath.Join(agent, "plugins", "catalog")
	writeCLIFile(t, filepath.Join(plugin, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"catalog"}`, 0o644)
	writeCLIFile(t, filepath.Join(plugin, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"catalog":{"type":"streamable-http","url":"https://example.com/mcp"}}}`, 0o644)
	if err := Run([]string{"connection", "add", agent, "catalog", "--url", "https://example.com/standalone"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "collides with an authored plugin server") {
		t.Fatalf("plugin collision error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeCLIFile(t, outside, "outside\n", 0o644)
	if err := os.MkdirAll(filepath.Join(agent, "connections"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(agent, "connections", "unsafe.md")); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"connection", "remove", agent, "unsafe"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "real regular file") {
		t.Fatalf("unsafe remove error = %v", err)
	}
	if got := readCLIFile(t, outside); got != "outside\n" {
		t.Fatalf("unsafe remove changed symlink target: %q", got)
	}
	writeCLIFile(t, filepath.Join(agent, "connections", "legacy.md"), "legacy body-only source\n", 0o644)
	if err := Run([]string{"connection", "remove", agent, "legacy"}, strings.NewReader(""), io.Discard, io.Discard, ""); err != nil {
		t.Fatalf("remove unhealthy authored source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent, "connections", "legacy.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unhealthy authored source remains: %v", err)
	}
}

func TestConnectionAddEnforcesPostAddInventoryAndSourceBounds(t *testing.T) {
	agent := t.TempDir()
	writeCLIFile(t, filepath.Join(agent, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	for index := 0; index < 128; index++ {
		name := fmt.Sprintf("server%03d", index)
		writeCLIFile(t, filepath.Join(agent, "connections", name+".md"), "---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n", 0o644)
	}
	if err := Run([]string{"connection", "add", agent, "overflow", "--url", "https://example.com/mcp"}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "at most 128 entries") {
		t.Fatalf("post-add inventory bound error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(agent, "connections", "overflow.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overflow add wrote source: %v", err)
	}

	smallAgent := t.TempDir()
	writeCLIFile(t, filepath.Join(smallAgent, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	endpoint := "https://example.com/" + strings.Repeat("a", 8<<10)
	if err := Run([]string{"connection", "add", smallAgent, "oversized", "--url", endpoint}, strings.NewReader(""), io.Discard, io.Discard, ""); err == nil || !strings.Contains(err.Error(), "at most 8192 bytes") {
		t.Fatalf("post-add source bound error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(smallAgent, "connections", "oversized.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized add wrote source: %v", err)
	}
}

func TestApplySelectsInstalledGitHubNativeMCPWithoutReadingPAT(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	const fakeValue = "conspicuous-cli-fake-pat"
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", fakeValue)
	packageRoot := t.TempDir()
	payload := []byte("#!/bin/sh\nexit 0\n")
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	artifactID := runtime.GOOS + "-" + runtime.GOARCH
	document := map[string]any{
		"schema_version": 1, "id": "github-mcp-server", "version": "1.8.0", "name": "GitHub fixture", "description": "Credential-free GitHub fixture.", "license": "MIT",
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
	writeCLIFile(t, filepath.Join(packageRoot, "integration.json"), string(manifest)+"\n", 0o600)
	writeCLIFile(t, filepath.Join(packageRoot, "payload", "github-mcp-server"), string(payload), 0o600)
	store, err := integration.NewDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: packageRoot, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}

	source := filepath.Join(t.TempDir(), "github-agent")
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "connections", "github.md"), "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nInspect GitHub through discovered tools.\n", 0o644)
	harness := filepath.Join(t.TempDir(), "codex")
	writeCLIFile(t, harness, "#!/bin/sh\necho 'codex-cli 0.144.1'\n", 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--harness", "codex", "--command", harness}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "native server=github package=github-mcp-server capability=github") || !strings.Contains(got, "owned by codex") || !strings.Contains(got, "managed tools=echo") || strings.Contains(got, "github__") || strings.Contains(got, fakeValue) {
		t.Fatalf("apply output = %q", got)
	}
	config := readCLIFile(t, filepath.Join(source, ".codex", "config.toml"))
	if !strings.Contains(config, `[mcp_servers."github"]`) || !strings.Contains(config, `env_vars = ["GITHUB_PERSONAL_ACCESS_TOKEN"]`) || strings.Contains(config, fakeValue) {
		t.Fatalf("generated native config = %q", config)
	}

	if err := store.SetEnabled(context.Background(), "github-mcp-server", false); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	err = Run([]string{"apply", source, "--workspace", workspace, "--harness", "codex", "--command", harness}, strings.NewReader(""), io.Discard, io.Discard, self)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled package apply error = %v", err)
	}
	if entries, readErr := os.ReadDir(workspace); readErr != nil || len(entries) != 0 {
		t.Fatalf("failed offline selection mutated workspace: %v, %v", entries, readErr)
	}
}

func TestScheduledOpenRevalidatesCurrentGitHubPackageState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := integration.NewDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	first := writeCLIGitHubPackage(t, "1.8.0", []byte("#!/bin/sh\n# first\nexit 0\n"))
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: first, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "scheduled-github-agent")
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Scheduled GitHub agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "connections", "github.md"), "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nUse discovered GitHub tools.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "probe.md"), "---\ncron: '* * * * *'\n---\n\nProbe GitHub.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	servers, err := resolveProjectNativeMCP(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.ApplyWithNativeMCP(p, self, servers); err != nil {
		t.Fatal(err)
	}
	underlying := &countingOpenDriver{}
	driver := &currentSetupDriver{Driver: underlying, project: p, self: self, diagnostics: io.Discard}

	if err := store.SetEnabled(context.Background(), "github-mcp-server", false); err != nil {
		t.Fatal(err)
	}
	if session, err := driver.Open(context.Background(), harness.OpenRequest{Root: source}); err == nil || session != nil || underlying.opens != 0 || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled scheduled open = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
	if err := store.SetEnabled(context.Background(), "github-mcp-server", true); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), "github-mcp-server"); err != nil {
		t.Fatal(err)
	}
	if session, err := driver.Open(context.Background(), harness.OpenRequest{Root: source}); err == nil || session != nil || underlying.opens != 0 || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("removed scheduled open = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: first, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	second := writeCLIGitHubPackage(t, "1.8.1", []byte("#!/bin/sh\n# second\nexit 0\n"))
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: second, Trust: integration.TrustOperator, UpdatePackageID: "github-mcp-server"}); err != nil {
		t.Fatal(err)
	}
	current, err := store.ResolveNativeMCP(context.Background(), "github-mcp-server", "github")
	if err != nil {
		t.Fatal(err)
	}
	session, err := driver.Open(context.Background(), harness.OpenRequest{Root: source})
	if err != nil || session == nil || underlying.opens != 1 {
		t.Fatalf("updated scheduled open = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
	if config := readCLIFile(t, filepath.Join(source, ".codex", "config.toml")); !strings.Contains(config, current.Executable) {
		t.Fatalf("scheduled open retained stale executable: %s", config)
	}
	if err := os.Chmod(current.Executable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current.Executable, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if session, err := driver.Open(context.Background(), harness.OpenRequest{Root: source}); err == nil || session != nil || underlying.opens != 1 || !strings.Contains(err.Error(), "cache is corrupt") {
		t.Fatalf("corrupt scheduled open = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
}

func TestDelayedChannelOpenAndReopenUseCurrentGitHubPackage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := integration.NewDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	first := writeCLIGitHubPackage(t, "1.8.0", []byte("#!/bin/sh\n# channel-first\nexit 0\n"))
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: first, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	installCLIChannelAdapter(t, store)
	source := filepath.Join(t.TempDir(), "channel-github-agent")
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Channel GitHub agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "connections", "github.md"), "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nUse discovered GitHub tools.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "channels", "discord.md"), "---\nmode: ambient\n---\n\nParticipate when useful.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	servers, err := resolveProjectNativeMCP(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.ApplyWithNativeMCP(p, self, servers); err != nil {
		t.Fatal(err)
	}
	underlying := &countingOpenDriver{}
	driver := &currentSetupDriver{Driver: underlying, project: p, self: self, diagnostics: io.Discard}

	if err := store.SetEnabled(context.Background(), "github-mcp-server", false); err != nil {
		t.Fatal(err)
	}
	if session, err := driver.OpenProject(context.Background(), p, harness.OpenRequest{Root: source, Policy: harness.PolicyReadOnly}); err == nil || session != nil || underlying.opens != 0 {
		t.Fatalf("delayed disabled channel open = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
	if err := store.SetEnabled(context.Background(), "github-mcp-server", true); err != nil {
		t.Fatal(err)
	}
	if session, err := driver.OpenProject(context.Background(), p, harness.OpenRequest{Root: source, Policy: harness.PolicyReadOnly}); err != nil || session == nil || underlying.opens != 1 {
		t.Fatalf("enabled channel open = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
	second := writeCLIGitHubPackage(t, "1.8.1", []byte("#!/bin/sh\n# channel-second\nexit 0\n"))
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: second, Trust: integration.TrustOperator, UpdatePackageID: "github-mcp-server"}); err != nil {
		t.Fatal(err)
	}
	current, err := store.ResolveNativeMCP(context.Background(), "github-mcp-server", "github")
	if err != nil {
		t.Fatal(err)
	}
	if session, err := driver.OpenProject(context.Background(), p, harness.OpenRequest{Root: source, ResumeID: "after-hibernation", Policy: harness.PolicyReadOnly}); err != nil || session == nil || underlying.opens != 2 {
		t.Fatalf("channel reopen = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
	if config := readCLIFile(t, filepath.Join(source, ".codex", "config.toml")); !strings.Contains(config, current.Executable) {
		t.Fatalf("channel reopen retained stale executable: %s", config)
	}
	if err := os.Chmod(current.Executable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current.Executable, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if session, err := driver.OpenProject(context.Background(), p, harness.OpenRequest{Root: source, ResumeID: "second-reopen", Policy: harness.PolicyReadOnly}); err == nil || session != nil || underlying.opens != 2 {
		t.Fatalf("corrupt channel reopen = session %#v, opens %d, error %v", session, underlying.opens, err)
	}
}

func TestGuardedWritableChannelOpenPreservesWritableSetup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := integration.NewDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := writeCLIGitHubPackage(t, "1.8.0", []byte("#!/bin/sh\n# writable-channel\nexit 0\n"))
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: packageRoot, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
	installCLIChannelAdapter(t, store)
	root := filepath.Join(t.TempDir(), "writable-channel-agent")
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Writable channel agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(root, "connections", "github.md"), "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nUse discovered GitHub tools.\n", 0o644)
	writeCLIFile(t, filepath.Join(root, "channels", "discord.md"), "---\nmode: ambient\n---\n\nParticipate when useful.\n", 0o644)
	p, err := project.Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	servers, err := resolveProjectNativeMCP(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.ApplyWritableChannelWithNativeMCP(p, self, servers); err != nil {
		t.Fatal(err)
	}
	underlying := &countingContinuationDriver{}
	driver := newCurrentSetupDriver(underlying, p, self, io.Discard)
	guarded := driver.(interface {
		OpenProject(context.Context, *project.Project, harness.OpenRequest) (harness.Session, error)
	})
	request := harness.OpenRequest{Root: root, Policy: harness.PolicyWorkspaceWrite}
	for attempt := 1; attempt <= 2; attempt++ {
		session, err := guarded.OpenProject(context.Background(), p, request)
		if err != nil || session == nil || underlying.opens != attempt {
			t.Fatalf("writable guarded open %d = session %#v, opens %d, error %v", attempt, session, underlying.opens, err)
		}
		if err := setup.VerifyWritableChannel(p); err != nil {
			t.Fatalf("writable setup after guarded open %d: %v", attempt, err)
		}
	}
	continuation := driver.(interface {
		ContinueProjectTurn(context.Context, *project.Project, harness.OpenRequest, string, interaction.ContinuationIntent, func(harness.Event)) interaction.ContinuationResult
	})
	result := continuation.ContinueProjectTurn(context.Background(), p, request, "persisted-session", interaction.ContinuationIntent{}, func(harness.Event) {})
	if result.Effect != interaction.EffectSucceeded || underlying.continuations != 1 {
		t.Fatalf("writable guarded continuation = %+v, starts %d", result, underlying.continuations)
	}
	if err := setup.VerifyWritableChannel(p); err != nil {
		t.Fatalf("writable setup after guarded continuation: %v", err)
	}
	if instructions := readCLIFile(t, filepath.Join(root, "AGENTS.md")); !strings.Contains(instructions, "already has workspace-write access") || strings.Contains(instructions, "enforced read-only") {
		t.Fatalf("guard downgraded writable instructions: %s", instructions)
	}
	if record := readCLIFile(t, filepath.Join(root, ".hctl", "apply", "codex.json")); !strings.Contains(record, `"channel_writable": true`) {
		t.Fatalf("guard downgraded writable apply record: %s", record)
	}
}

func TestParkedContinuationsRevalidateCurrentGitHubPackage(t *testing.T) {
	for _, harnessName := range []string{"codex", "claude"} {
		t.Run(harnessName, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			store, err := integration.NewDefaultStore()
			if err != nil {
				t.Fatal(err)
			}
			first := writeCLIGitHubPackage(t, "1.8.0", []byte("#!/bin/sh\n# parked-first\nexit 0\n"))
			if _, err := store.Install(context.Background(), integration.InstallOptions{Source: first, Trust: integration.TrustOperator}); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), harnessName+"-parked-agent")
			writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Parked continuation agent.\n---\n\nBe concise.\n", 0o644)
			writeCLIFile(t, filepath.Join(root, "connections", "github.md"), "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nUse discovered GitHub tools.\n", 0o644)
			p, err := project.Load(root, harnessName)
			if err != nil {
				t.Fatal(err)
			}
			servers, err := resolveProjectNativeMCP(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := setup.ApplyWithNativeMCP(p, self, servers); err != nil {
				t.Fatal(err)
			}

			starts := 0
			var diagnostics bytes.Buffer
			var invoke func() interaction.ContinuationResult
			if harnessName == "codex" {
				underlying := &countingContinuationDriver{}
				driver := newCurrentSetupDriver(underlying, p, self, &diagnostics).(harness.ContinuationTurnDriver)
				invoke = func() interaction.ContinuationResult {
					result := driver.ContinueTurn(context.Background(), harness.OpenRequest{Root: root, Policy: harness.PolicyReadOnly}, "persisted-session", interaction.ContinuationIntent{}, func(harness.Event) {})
					starts = underlying.continuations
					return result
				}
			} else {
				underlying := &countingDeferredDriver{}
				driver := newCurrentSetupDriver(underlying, p, self, &diagnostics).(harness.NativeDeferredToolDriver)
				invoke = func() interaction.ContinuationResult {
					result := driver.ResumeDeferredTool(context.Background(), harness.OpenRequest{Root: root, Policy: harness.PolicyReadOnly}, "persisted-session", interaction.ContinuationIntent{}, func(harness.Event) {})
					starts = underlying.continuations
					return result
				}
			}

			if err := store.SetEnabled(context.Background(), "github-mcp-server", false); err != nil {
				t.Fatal(err)
			}
			if result := invoke(); result.Effect != interaction.EffectFailed || starts != 0 || !strings.Contains(diagnostics.String(), "disabled") {
				t.Fatalf("disabled parked continuation = %+v, starts %d", result, starts)
			}
			diagnostics.Reset()
			if err := store.SetEnabled(context.Background(), "github-mcp-server", true); err != nil {
				t.Fatal(err)
			}
			if err := store.Remove(context.Background(), "github-mcp-server"); err != nil {
				t.Fatal(err)
			}
			if result := invoke(); result.Effect != interaction.EffectFailed || starts != 0 || !strings.Contains(diagnostics.String(), "not installed") {
				t.Fatalf("removed parked continuation = %+v, starts %d", result, starts)
			}
			diagnostics.Reset()
			if _, err := store.Install(context.Background(), integration.InstallOptions{Source: first, Trust: integration.TrustOperator}); err != nil {
				t.Fatal(err)
			}
			second := writeCLIGitHubPackage(t, "1.8.1", []byte("#!/bin/sh\n# parked-second\nexit 0\n"))
			if _, err := store.Install(context.Background(), integration.InstallOptions{Source: second, Trust: integration.TrustOperator, UpdatePackageID: "github-mcp-server"}); err != nil {
				t.Fatal(err)
			}
			current, err := store.ResolveNativeMCP(context.Background(), "github-mcp-server", "github")
			if err != nil {
				t.Fatal(err)
			}
			if result := invoke(); result.Effect != interaction.EffectSucceeded || starts != 1 {
				t.Fatalf("updated parked continuation = %+v, starts %d", result, starts)
			}
			diagnostics.Reset()
			configPath := filepath.Join(root, ".codex", "config.toml")
			if harnessName == "claude" {
				configPath = filepath.Join(root, ".mcp.json")
			}
			if config := readCLIFile(t, configPath); !strings.Contains(config, current.Executable) {
				t.Fatalf("parked continuation retained stale executable: %s", config)
			}
			if err := os.Chmod(current.Executable, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(current.Executable, []byte("corrupt"), 0o700); err != nil {
				t.Fatal(err)
			}
			if result := invoke(); result.Effect != interaction.EffectFailed || starts != 1 || !strings.Contains(diagnostics.String(), "cache is corrupt") {
				t.Fatalf("corrupt parked continuation = %+v, starts %d", result, starts)
			}
		})
	}
}

func TestCurrentSetupOpenHonorsCallerCancellation(t *testing.T) {
	source := filepath.Join(t.TempDir(), "cancelled-agent")
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Cancelled setup agent.\n---\n\nBe concise.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	underlying := &countingOpenDriver{}
	driver := &currentSetupDriver{Driver: underlying, project: p, self: "/usr/bin/true", diagnostics: io.Discard}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for name, ctx := range map[string]context.Context{"cancelled": cancelled, "deadline": deadline} {
		t.Run(name, func(t *testing.T) {
			session, err := driver.Open(ctx, harness.OpenRequest{Root: source})
			if err == nil || session != nil || underlying.opens != 0 || !errors.Is(err, ctx.Err()) {
				t.Fatalf("canceled setup open = session %#v, opens %d, error %v", session, underlying.opens, err)
			}
		})
	}
	if entries, err := os.ReadDir(source); err != nil || len(entries) != 1 {
		t.Fatalf("canceled preparation mutated workspace: %v, %v", entries, err)
	}
}

func TestStageCreatesRunnableToolFreeFilesystem(t *testing.T) {
	source := filepath.Join(t.TempDir(), "sample-agent")
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	bin := t.TempDir()
	harness := filepath.Join(bin, "codex")
	self := filepath.Join(bin, "hctl")
	writeCLIFile(t, harness, "#!/bin/sh\necho 'codex-cli 0.144.1'\n", 0o755)
	writeCLIFile(t, self, "#!/bin/sh\nexit 0\n", 0o755)
	outputPath := filepath.Join(t.TempDir(), "artifact")
	var output, stderr bytes.Buffer
	if err := Run([]string{"stage", source, "--harness", "codex", "--command", harness, "--output", outputPath}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "staged agent=sample-agent") || !strings.Contains(got, "runtimes=none") {
		t.Fatalf("stage output = %q", got)
	}
	for _, path := range []string{
		"opt/hctl/artifact.json",
		"opt/hctl/bin/hctl",
		"opt/hctl/bin/agent-entrypoint",
		"opt/hctl/harness/bin/codex",
		"opt/hctl/agents/sample-agent/instructions.md",
		"workspace/.codex/config.toml",
		"workspace/.hctl/apply/codex.json",
	} {
		if _, err := os.Stat(filepath.Join(outputPath, filepath.FromSlash(path))); err != nil {
			t.Fatalf("staged %s: %v", path, err)
		}
	}
}

func TestClaudeDeferredHookCommandFailsClosedWithExitZeroResult(t *testing.T) {
	for _, input := range []string{"not-json", `{}`, `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_x","tool_input":{}}`} {
		var output, stderr bytes.Buffer
		if err := Run([]string{"hook", "claude-deferred-input"}, strings.NewReader(input), &output, &stderr, ""); err != nil {
			t.Fatalf("malformed hook returned command failure: %v", err)
		}
		if !strings.Contains(output.String(), `"permissionDecision":"deny"`) || strings.Contains(output.String(), `"permissionDecision":"defer"`) {
			t.Fatalf("malformed hook output = %q", output.String())
		}
	}
}

func TestRunRejectsInvalidSessionCapacity(t *testing.T) {
	var output, stderr bytes.Buffer
	err := Run([]string{"run", ".", "--harness", "codex", "--max-resident-sessions", "1", "--max-active-turns", "2"}, strings.NewReader(""), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), "capacity limit is invalid") {
		t.Fatalf("invalid capacity error = %v", err)
	}
}

func TestDiscordChannelJourneyRequiresInstalledAdapterWithoutSourceMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	source := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	var output, stderr bytes.Buffer
	err := Run([]string{"channel", "setup", "discord", source}, strings.NewReader("fake\n"), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), "hctl integration install SOURCE --trust operator") || !strings.Contains(err.Error(), "hctl channel setup discord") {
		t.Fatalf("missing adapter remedy = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(source, "channels", "discord.md")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed adapter setup mutated source: %v", statErr)
	}
	output.Reset()
	if err := Run([]string{"channel", "remove", "discord", source}, strings.NewReader(""), &output, &stderr, ""); err == nil || !strings.Contains(err.Error(), "adapter is unavailable") {
		t.Fatalf("remove missing adapter remedy = %v", err)
	}
}

func TestInstalledDiscordAdapterSetupStatusRemoveJourneyUsesExactModes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	store, err := integration.NewDefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "operations.log")
	t.Setenv("HCTL_OPERATION_LOG", logPath)
	t.Setenv("HCTL_DISCORD_TOKEN", "fake-adapter-only-token")
	// A direct hctl invocation must ignore an arbitrary ambient descriptor
	// path and continue to use the operator-installed shared store.
	attackerDescriptor := filepath.Join(t.TempDir(), "channel-adapter.json")
	writeCLIFile(t, attackerDescriptor, "{}\n", 0o600)
	t.Setenv(integration.StagedChannelAdapterEnvironment, attackerDescriptor)
	payload := []byte(`#!/bin/sh
operation=$1
profile=$3
token=absent
[ -n "${HCTL_DISCORD_TOKEN-}" ] && token=present
printf '%s profile=%s token=%s descriptor=%s\n' "$operation" "$profile" "$token" "${HCTL_CHANNEL_ADAPTER_DESCRIPTOR-unset}" >> "$HCTL_OPERATION_LOG"
status=ready
[ "$operation" = remove ] && status=removed
printf '{"schema_version":1,"operation":"%s","profile_id":"%s","status":"%s","identity":"fixture-bot"}\n' "$operation" "$profile" "$status"
`)
	installCLIChannelAdapterPayload(t, store, payload)
	source := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Installed adapter journey.\n---\n\nBe concise.\n", 0o644)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	for _, operation := range []string{"setup", "status", "remove"} {
		output.Reset()
		if err := Run([]string{"channel", operation, "discord", source}, strings.NewReader(""), &output, &stderr, self); err != nil {
			t.Fatalf("channel %s: %v; stderr=%s", operation, err, stderr.String())
		}
		if !strings.Contains(output.String(), "operation="+operation+" profile=default") {
			t.Fatalf("channel %s output = %q", operation, output.String())
		}
	}
	if _, err := os.Stat(filepath.Join(source, "channels", "discord.md")); err != nil {
		t.Fatalf("setup did not create portable channel source: %v", err)
	}
	log := readCLIFile(t, logPath)
	for _, operation := range []string{"setup", "status", "remove"} {
		if !strings.Contains(log, operation+" profile=default token=present descriptor=unset") {
			t.Fatalf("exact %s adapter mode or environment boundary missing:\n%s", operation, log)
		}
	}
	if strings.Contains(log, "fake-adapter-only-token") || strings.Contains(log, attackerDescriptor) {
		t.Fatalf("adapter operation evidence retained sensitive or redirecting value: %s", log)
	}
}

func TestStagedDiscordAdapterResolutionIsAgentBoundAndOffline(t *testing.T) {
	source := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Staged adapter resolution.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "channels", "discord.md"), "---\nmode: ambient\n---\n\nParticipate when useful.\n", 0o644)
	p, err := project.Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	store := integration.NewStore(t.TempDir(), nil)
	installCLIChannelAdapter(t, store)
	resolved, err := store.ResolveChannelAdapter(context.Background(), "discord")
	if err != nil {
		t.Fatal(err)
	}
	stagedRoot := t.TempDir()
	artifacts, err := store.StageArtifacts(context.Background(), resolved.Selection.PackageID, []string{resolved.Selection.Artifact.ID}, stagedRoot)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("StageArtifacts() = %#v, %v", artifacts, err)
	}
	descriptorData, err := integration.EncodeStagedChannelAdapter(p.AgentID, p.SourceFingerprint, resolved, artifacts[0])
	if err != nil {
		t.Fatal(err)
	}
	descriptor := filepath.Join(stagedRoot, "opt", "hctl", "integrations", "channel-adapter.json")
	writeCLIFile(t, descriptor, string(descriptorData), 0o444)
	self := filepath.Join(stagedRoot, "opt", "hctl", "bin", "hctl")
	writeCLIFile(t, self, "#!/bin/sh\nexit 0\n", 0o755)
	t.Setenv(integration.StagedChannelAdapterEnvironment, descriptor)
	if err := store.SetEnabled(context.Background(), resolved.Selection.PackageID, false); err != nil {
		t.Fatal(err)
	}
	selectedStore, staged, err := resolveChannelAdapter(context.Background(), p, self)
	if err != nil || selectedStore != nil || staged.Selection.ManifestSHA256 != resolved.Selection.ManifestSHA256 {
		t.Fatalf("staged resolve = store=%v resolution=%#v error=%v", selectedStore, staged, err)
	}
	if _, err := integration.LoadStagedChannelAdapter(descriptor, p.AgentID, strings.Repeat("f", 64), "discord"); err == nil || !strings.Contains(err.Error(), "does not match this agent") {
		t.Fatalf("stale staged source error = %v", err)
	}
}

func TestAdapterProfileSelectionPreservesAgentBindingAndLegacyFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("HCTL_DISCORD_PROFILE", "")
	legacy := filepath.Join(t.TempDir(), "config.toml")
	writeCLIFile(t, legacy, "schema_version=1\n[discord]\ndefault_profile='legacy-default'\n[discord.profiles.ignored]\nvendor_field='not-read'\n[agent_profiles]\n'agent@one'='legacy-agent'\n'agent@legacy'='legacy-agent'\n", 0o600)

	if got, err := selectedAdapterProfile("agent@one", "explicit", legacy); err != nil || got != "explicit" {
		t.Fatalf("explicit selection = %q, %v", got, err)
	}
	t.Setenv("HCTL_DISCORD_PROFILE", "ambient")
	if got, err := selectedAdapterProfile("agent@one", "", legacy); err != nil || got != "ambient" {
		t.Fatalf("ambient selection = %q, %v", got, err)
	}
	t.Setenv("HCTL_DISCORD_PROFILE", "")
	selections, err := channelselection.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := selections.Set("agent@one", "discord", "persisted"); err != nil {
		t.Fatalf("persist selection: %v", err)
	}
	if got, err := selectedAdapterProfile("agent@one", "", legacy); err != nil || got != "persisted" {
		t.Fatalf("persisted selection = %q, %v", got, err)
	}
	if got, err := selectedAdapterProfile("agent@legacy", "", legacy); err != nil || got != "legacy-agent" {
		t.Fatalf("legacy agent selection = %q, %v", got, err)
	}
	if got, err := selectedAdapterProfile("agent@two", "", legacy); err != nil || got != "legacy-default" {
		t.Fatalf("legacy default selection = %q, %v", got, err)
	}
}

func TestRunJSONLAutoAppliesAndScrubsDiscordToken(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	harness := filepath.Join(t.TempDir(), "codex")
	writeCLIFile(t, harness, "#!/bin/sh\nif [ -n \"$HCTL_DISCORD_TOKEN\" ]; then exit 7; fi\necho 'codex-cli 0.144.1'\n", 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HCTL_DISCORD_TOKEN", "must-not-reach-child")
	var output, stderr bytes.Buffer
	if err := Run([]string{"run", root, "--harness", "codex", "--command", harness, "--input", "jsonl"}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal("run did not auto-apply")
	}
}

func TestRunGitHubNativeMCPEnvironmentAndOptionalFailureForClaudeAndCodex(t *testing.T) {
	const fakeValue = "conspicuous-headless-fake-pat"
	commands := map[string]string{
		"claude": `#!/bin/sh
if [ "${1-}" = "--version" ]; then echo "2.1.221 (Claude Code)"; exit 0; fi
if [ "${1-}" = "--permission-mode" ]; then echo "--permission-mode plan acceptEdits"; exit 0; fi
probe_output=$("$FAKE_NATIVE_PROBE" claude) || exit $?
IFS= read -r line || exit 1
session_id="11111111-1111-4111-8111-111111111111"
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$session_id"
printf '%s\n' "$probe_output" | while IFS= read -r diagnostic; do
  printf '{"type":"stream_event","session_id":"%s","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"%s"}}}\n' "$session_id" "$diagnostic"
done
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"%s","result":"github optional; managed session available"}\n' "$session_id"
`,
		"codex": `#!/bin/sh
if [ "${1-}" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
probe_output=$("$FAKE_NATIVE_PROBE" codex) || exit $?
while IFS= read -r line; do
 case "$line" in
  *'"method":"initialize"'*) echo '{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}' ;;
  *'"method":"thread/start"'*|*'"method":"thread/resume"'*) echo '{"id":2,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"}}}' ;;
  *'"method":"turn/start"'*)
    echo '{"id":3,"result":{"turn":{"id":"01922222-2222-7222-8222-222222222222","items":[],"status":"inProgress"}}}'
    printf '%s\n' "$probe_output" | while IFS= read -r diagnostic; do
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"01911111-1111-7111-8111-111111111111","turnId":"01922222-2222-7222-8222-222222222222","itemId":"fixture-diagnostic","delta":"%s"}}\n' "$diagnostic"
    done
    echo '{"method":"turn/completed","params":{"threadId":"01911111-1111-7111-8111-111111111111","turn":{"id":"01922222-2222-7222-8222-222222222222","items":[],"status":"completed"}}}'
    ;;
 esac
done
`,
	}
	for harnessName, script := range commands {
		t.Run(harnessName, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			managedServer := filepath.Join(t.TempDir(), "fake-hctl")
			writeCLIFile(t, managedServer, `#!/bin/sh
IFS= read -r initialize || exit 1
IFS= read -r list || exit 1
IFS= read -r call || exit 1
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"managed-fixture","version":"1.0.0"}}}'
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo"}]}}'
printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"structuredContent":{"managed_echo":"ok"}}}'
`, 0o755)
			store, err := integration.NewDefaultStore()
			if err != nil {
				t.Fatal(err)
			}
			packageRoot := writeCLIGitHubPackage(t, "1.8.0", []byte(`#!/bin/sh
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
IFS= read -r initialize || exit 1
IFS= read -r list || exit 1
IFS= read -r call || call=
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"github-mcp-server","version":"1.8.0-fixture"}}}'
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"fixture_issue_inspect"}]}}'
[ -z "$call" ] || printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"structuredContent":{"github_read":"ok"}}}'
`))
			if _, err := store.Install(context.Background(), integration.InstallOptions{Source: packageRoot, Trust: integration.TrustOperator}); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), "maintainer")
			writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Headless GitHub fixture.\n---\n\nUse discovered GitHub outcomes.\n", 0o644)
			writeCLIFile(t, filepath.Join(source, "connections", "github.md"), "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nUse discovered GitHub tools.\n", 0o644)
			command := filepath.Join(t.TempDir(), harnessName)
			writeCLIFile(t, command, script, 0o755)
			probe := filepath.Join(t.TempDir(), "native-probe")
			// The probe stands in for native harness MCP startup and approval. It
			// consumes raw server stderr and emits only fixed categories through the
			// harness's normal bounded output; hctl transports but does not classify it.
			writeCLIFile(t, probe, `#!/bin/sh
set -u
harness=$1
if [ "$harness" = codex ] && [ "${FAKE_CODEX_PROJECT_TRUST-}" != approved ]; then
  echo 'Codex project trust required' >&2
  exit 41
fi
if [ "$harness" = claude ]; then
  config=.mcp.json
  grep -F '"github": {' "$config" >/dev/null || exit 42
  github_cwd=$(awk '/"github": \{/{github=1} github && /"args": \[/{args=1; next} args {line=$0; gsub(/^[[:space:]]*"|",?[[:space:]]*$/, "", line); n++; if(n==2){print line; exit}}' "$config")
  github_server=$(awk '/"github": \{/{github=1} github && /"args": \[/{args=1; next} args {line=$0; gsub(/^[[:space:]]*"|",?[[:space:]]*$/, "", line); n++; if(n==4){print line; exit}}' "$config")
  managed_server=$(awk '/"managed": \{/{managed=1; next} managed && /"command":/{line=$0; sub(/^[[:space:]]*"command": "/, "", line); sub(/",?$/, "", line); print line; exit}' "$config")
  server_approved=${FAKE_CLAUDE_PROJECT_SERVER_APPROVAL-}
  tool_approved=approved
else
  config=.codex/config.toml
  grep -F '[mcp_servers."github"]' "$config" >/dev/null || exit 42
  github_cwd=$(awk '/^\[mcp_servers\."github"\]$/{github=1; next} github && /^cwd = /{line=$0; sub(/^cwd = "/, "", line); sub(/"$/, "", line); print line; exit}' "$config")
  github_server=$(awk '/^\[mcp_servers\."github"\]$/{github=1; next} github && /^command = /{line=$0; sub(/^command = "/, "", line); sub(/"$/, "", line); print line; exit}' "$config")
  managed_server=$(awk '/^\[mcp_servers\.managed\]$/{managed=1; next} managed && /^command = /{line=$0; sub(/^command = "/, "", line); sub(/"$/, "", line); print line; exit}' "$config")
  server_approved=${FAKE_CODEX_SERVER_APPROVAL-}
  tool_approved=${FAKE_CODEX_TOOL_APPROVAL-}
fi

failure=$(mktemp)
trap 'rm -f "$failure"' EXIT
github_diagnostic='github-unavailable reason=server-approval-required'
if [ "$server_approved" = approved ]; then
  set +e
  if [ "$tool_approved" = approved ]; then
    github_output=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fixture_issue_inspect","arguments":{}}}' | /usr/bin/env -C "$github_cwd" -- "$github_server" stdio 2>"$failure")
  else
    github_output=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' | /usr/bin/env -C "$github_cwd" -- "$github_server" stdio 2>"$failure")
  fi
  github_status=$?
  set -e
  if [ "$github_status" -ne 0 ]; then
    if grep -F 'authentication missing' "$failure" >/dev/null; then
      github_diagnostic='github-unavailable reason=missing-credential'
    elif grep -F 'authentication empty' "$failure" >/dev/null; then
      github_diagnostic='github-unavailable reason=empty-credential'
    elif grep -F 'authentication rejected' "$failure" >/dev/null; then
      github_diagnostic='github-unavailable reason=invalid-credential'
    else
      github_diagnostic='github-unavailable reason=native-startup-failed'
    fi
  elif [ "$tool_approved" != approved ]; then
    github_diagnostic='github-unavailable reason=tool-approval-required'
  elif printf '%s\n' "$github_output" | grep -F '"github_read":"ok"' >/dev/null; then
    github_diagnostic='github-available'
  else
    github_diagnostic='github-unavailable reason=native-startup-failed'
  fi
fi
managed_output=$(printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"ok"}}}' | "$managed_server" mcp serve fixture --harness "$harness")
printf '%s\n' "$managed_output" | grep -F '"managed_echo":"ok"' >/dev/null || exit 43
printf '%s\n' "$github_diagnostic" 'managed-echo=ok'
`, 0o755)
			t.Setenv("FAKE_NATIVE_PROBE", probe)
			t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", fakeValue)
			t.Setenv("FAKE_CLAUDE_PROJECT_SERVER_APPROVAL", "approved")
			t.Setenv("FAKE_CODEX_PROJECT_TRUST", "approved")
			t.Setenv("FAKE_CODEX_SERVER_APPROVAL", "approved")
			t.Setenv("FAKE_CODEX_TOOL_APPROVAL", "approved")
			run := func(workspace, conversation, inputID string) (string, string, error) {
				var output, stderr bytes.Buffer
				input := `{"input_id":"` + inputID + `","text":"inspect GitHub"}` + "\n"
				err := Run([]string{"run", source, "--workspace", workspace, "--harness", harnessName, "--command", command, "--input", "jsonl", "--conversation", conversation}, strings.NewReader(input), &output, &stderr, managedServer)
				return output.String(), stderr.String(), err
			}

			workspace := t.TempDir()
			for _, inputID := range []string{"first", "after-restart"} {
				output, stderr, err := run(workspace, "maintainer", inputID)
				if err != nil || !strings.Contains(output, `"delta":"github-available"`) || !strings.Contains(output, `"delta":"managed-echo=ok"`) || !strings.Contains(output, `"type":"turn.completed"`) || strings.Contains(output, fakeValue) || strings.Contains(stderr, fakeValue) {
					t.Fatalf("headless %s run %s = output %q, stderr %q, error %v", harnessName, inputID, output, stderr, err)
				}
			}
			if err := os.Unsetenv("GITHUB_PERSONAL_ACCESS_TOKEN"); err != nil {
				t.Fatal(err)
			}
			output, stderr, err := run(workspace, "maintainer", "github-missing")
			if err != nil || !strings.Contains(output, `"delta":"github-unavailable reason=missing-credential"`) || !strings.Contains(output, `"delta":"managed-echo=ok"`) || !strings.Contains(output, `"type":"turn.completed"`) || strings.Contains(output, "authentication missing") || strings.Contains(stderr, fakeValue) {
				t.Fatalf("missing GitHub credential broke %s session = output %q, stderr %q, error %v", harnessName, output, stderr, err)
			}
			t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "")
			output, stderr, err = run(workspace, "maintainer", "github-empty")
			if err != nil || !strings.Contains(output, `"delta":"github-unavailable reason=empty-credential"`) || !strings.Contains(output, `"delta":"managed-echo=ok"`) || !strings.Contains(output, `"type":"turn.completed"`) || strings.Contains(output, "authentication empty") || strings.Contains(stderr, fakeValue) {
				t.Fatalf("empty GitHub credential broke %s session = output %q, stderr %q, error %v", harnessName, output, stderr, err)
			}
			const invalidValue = "invalid-headless-fixture-marker"
			t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", invalidValue)
			output, stderr, err = run(workspace, "maintainer", "github-invalid")
			if err != nil || !strings.Contains(output, `"delta":"github-unavailable reason=invalid-credential"`) || !strings.Contains(output, `"delta":"managed-echo=ok"`) || !strings.Contains(output, `"type":"turn.completed"`) || strings.Contains(output, "authentication rejected") || strings.Contains(output, invalidValue) || strings.Contains(stderr, invalidValue) {
				t.Fatalf("invalid GitHub credential broke %s session = output %q, stderr %q, error %v", harnessName, output, stderr, err)
			}
			t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", fakeValue)
			if harnessName == "claude" {
				t.Setenv("FAKE_CLAUDE_PROJECT_SERVER_APPROVAL", "")
			} else {
				t.Setenv("FAKE_CODEX_SERVER_APPROVAL", "")
			}
			output, stderr, err = run(workspace, "maintainer", "server-approval-unavailable")
			if err != nil || !strings.Contains(output, `"delta":"github-unavailable reason=server-approval-required"`) || !strings.Contains(output, `"delta":"managed-echo=ok"`) || !strings.Contains(output, `"type":"turn.completed"`) {
				t.Fatalf("optional %s server approval broke managed session = output %q, stderr %q, error %v", harnessName, output, stderr, err)
			}
			if harnessName == "claude" {
				t.Setenv("FAKE_CLAUDE_PROJECT_SERVER_APPROVAL", "approved")
			} else {
				t.Setenv("FAKE_CODEX_SERVER_APPROVAL", "approved")
				t.Setenv("FAKE_CODEX_TOOL_APPROVAL", "")
				output, stderr, err = run(workspace, "maintainer", "tool-approval-unavailable")
				if err != nil || !strings.Contains(output, `"delta":"github-unavailable reason=tool-approval-required"`) || !strings.Contains(output, `"delta":"managed-echo=ok"`) || !strings.Contains(output, `"type":"turn.completed"`) {
					t.Fatalf("optional Codex tool approval broke managed session = output %q, stderr %q, error %v", output, stderr, err)
				}
				t.Setenv("FAKE_CODEX_TOOL_APPROVAL", "approved")
				t.Setenv("FAKE_CODEX_PROJECT_TRUST", "")
				output, stderr, err = run(t.TempDir(), "untrusted-project", "project-trust-required")
				if err == nil || !strings.Contains(err.Error(), "codex initialize handshake failed") || !strings.Contains(output, `"type":"driver.process_failed"`) || !strings.Contains(output, `"status":"startup_failure"`) || strings.Contains(output, `"type":"turn.completed"`) {
					t.Fatalf("missing Codex project trust = output %q, stderr %q, error %v", output, stderr, err)
				}
				t.Setenv("FAKE_CODEX_PROJECT_TRUST", "approved")
			}
			type result struct {
				output string
				stderr string
				err    error
			}
			results := make(chan result, 2)
			for index := range 2 {
				workspace := t.TempDir()
				conversation := "concurrent-" + strconv.Itoa(index)
				inputID := "input-" + strconv.Itoa(index)
				go func() {
					out, diagnostic, runErr := run(workspace, conversation, inputID)
					results <- result{output: out, stderr: diagnostic, err: runErr}
				}()
			}
			for range 2 {
				result := <-results
				if result.err != nil || !strings.Contains(result.output, `"delta":"github-available"`) || !strings.Contains(result.output, `"delta":"managed-echo=ok"`) || !strings.Contains(result.output, `"type":"turn.completed"`) || strings.Contains(result.stderr, fakeValue) {
					t.Fatalf("concurrent %s headless session = output %q, stderr %q, error %v", harnessName, result.output, result.stderr, result.err)
				}
			}
			for _, evidenceRoot := range []string{home, source, workspace} {
				assertCLITreeOmits(t, evidenceRoot, fakeValue, invalidValue)
			}
		})
	}
}

func assertCLITreeOmits(t *testing.T, root string, values ...string) {
	t.Helper()
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, value := range values {
			if bytes.Contains(data, []byte(value)) {
				t.Fatalf("resolved environment value entered %s", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPrintsSafeCompatibilityWarning(t *testing.T) {
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\nfriction-notes: true\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(root, "skills", "echo", "SKILL.md"), "---\nname: echo\ndescription: Repeat safely.\nargument-hint: '[text]'\n---\n\nUse echo.\n", 0o644)
	harness := filepath.Join(t.TempDir(), "codex")
	writeCLIFile(t, harness, "#!/bin/sh\necho 'codex-cli 0.144.1'\n", 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", root, "--harness", "codex", "--command", harness}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); !strings.Contains(got, `warning: skills/echo/SKILL.md: field "argument-hint":`) || !strings.Contains(got, "copied unchanged but may have no effect for codex") {
		t.Fatalf("warning output = %q", got)
	}
	if !strings.Contains(output.String(), "applied agent=") || !strings.Contains(output.String(), "managed tools=echo,record-friction") {
		t.Fatalf("apply output = %q", output.String())
	}
}

func TestApplyRejectsUnsupportedChannelBeforeWorkspaceMutation(t *testing.T) {
	for _, harnessName := range []string{"claude", "codex"} {
		t.Run(harnessName, func(t *testing.T) {
			source := t.TempDir()
			workspace := t.TempDir()
			writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
			writeCLIFile(t, filepath.Join(source, "channels", "slack.md"), "Slack.\n", 0o644)
			var output, stderr bytes.Buffer
			err := Run([]string{"apply", source, "--workspace", workspace, "--harness", harnessName}, strings.NewReader(""), &output, &stderr, "")
			if err == nil || !strings.Contains(err.Error(), "supports discord.md only") {
				t.Fatalf("unsupported channel error = %v", err)
			}
			entries, readErr := os.ReadDir(workspace)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("invalid project mutated workspace: %v, %v", entries, readErr)
			}
		})
	}
}

func TestApplyRejectsLegacyBodyOnlyConnectionBeforeWorkspaceMutation(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "connections", "gitlab.md"), "GitLab.\n", 0o644)
	var output, stderr bytes.Buffer
	err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "codex"}, strings.NewReader(""), &output, &stderr, "")
	if err == nil || !strings.Contains(err.Error(), "body-only connection files are no longer supported") {
		t.Fatalf("legacy connection error = %v", err)
	}
	entries, readErr := os.ReadDir(workspace)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("invalid project mutated workspace: %v, %v", entries, readErr)
	}
}

func TestScheduleTriggerRunsFreshTaskAndDeduplicates(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "billing", "sweep.md"), "---\ncron: '0 9 * * 1-5'\n---\n\nSweep stale billing work.\n", 0o644)
	logPath := filepath.Join(t.TempDir(), "claude.log")
	t.Setenv("FAKE_LOG", logPath)
	command := filepath.Join(t.TempDir(), "claude")
	writeCLIFile(t, command, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "2.1.221 (Claude Code)"
  exit 0
fi
printf 'RUN\nARGS' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >> "$FAKE_LOG"
IFS= read -r line || exit 1
printf 'IN\t%s\n' "$line" >> "$FAKE_LOG"
session_id="session-$$"
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$session_id"
printf '{"type":"stream_event","session_id":"%s","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"SECRET MODEL OUTPUT"}}}\n' "$session_id"
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"%s","result":"SECRET MODEL OUTPUT"}\n' "$session_id"
`, 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "claude", "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}

	trigger := func(inputID string) string {
		t.Helper()
		output.Reset()
		stderr.Reset()
		err := Run([]string{"schedule", "trigger", source, "billing/sweep", "--workspace", workspace, "--harness", "claude", "--input-id", inputID, "--command", command}, strings.NewReader(""), &output, &stderr, self)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "SECRET MODEL OUTPUT") {
			t.Fatalf("schedule output exposed model text: %q", output.String())
		}
		return output.String()
	}

	if got := trigger("occurrence-1"); !strings.Contains(got, "status=completed duplicate=false") {
		t.Fatalf("first trigger output = %q", got)
	}
	if got := trigger("occurrence-1"); !strings.Contains(got, "status=completed duplicate=true") {
		t.Fatalf("duplicate trigger output = %q", got)
	}
	if got := trigger("occurrence-2"); !strings.Contains(got, "status=completed duplicate=false") {
		t.Fatalf("second trigger output = %q", got)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "RUN\n") != 2 || strings.Contains(string(log), "--resume") {
		t.Fatalf("schedule harness runs were not fresh and deduplicated:\n%s", log)
	}
	if strings.Count(string(log), "Sweep stale billing work") != 2 {
		t.Fatalf("schedule prompt was not submitted twice:\n%s", log)
	}
}

func TestScheduleTriggerTurnDeadlinePersistsUncertainAndAllowsLaterOccurrence(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '0 9 * * 1-5'\n---\n\nSweep stale work.\n", 0o644)
	runtimeRoot := t.TempDir()
	logPath := filepath.Join(runtimeRoot, "claude.log")
	markerPath := filepath.Join(runtimeRoot, "stalled-once")
	t.Setenv("FAKE_LOG", logPath)
	t.Setenv("FAKE_MARKER", markerPath)
	command := filepath.Join(runtimeRoot, "claude")
	writeCLIFile(t, command, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "2.1.221 (Claude Code)"
  exit 0
fi
printf 'RUN\n' >> "$FAKE_LOG"
IFS= read -r line || exit 1
if [ ! -f "$FAKE_MARKER" ]; then
  : > "$FAKE_MARKER"
  IFS= read -r line
  exit 1
fi
session_id="session-$$"
printf '{"type":"system","subtype":"init","session_id":"%s"}\n' "$session_id"
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"%s","result":"done"}\n' "$session_id"
`, 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "claude", "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}

	trigger := func(inputID string) error {
		output.Reset()
		stderr.Reset()
		return Run([]string{"schedule", "trigger", source, "sweep", "--workspace", workspace, "--harness", "claude", "--input-id", inputID, "--command", command, "--turn-timeout", "20ms", "--timeout", "2s"}, strings.NewReader(""), &output, &stderr, self)
	}
	if err := trigger("occurrence-1"); !errors.Is(err, dispatch.ErrTurnDeadlineExceeded) || !strings.Contains(output.String(), "status=uncertain duplicate=false") || !strings.Contains(output.String(), "reason=deadline_exceeded") {
		t.Fatalf("deadline trigger: err=%v output=%q", err, output.String())
	}
	if err := trigger("occurrence-1"); err == nil || !strings.Contains(err.Error(), "status uncertain") || !strings.Contains(output.String(), "status=uncertain duplicate=true") || !strings.Contains(output.String(), "reason=deadline_exceeded") {
		t.Fatalf("duplicate trigger: err=%v output=%q", err, output.String())
	}
	if err := trigger("occurrence-2"); err != nil || !strings.Contains(output.String(), "status=completed duplicate=false") {
		t.Fatalf("later trigger: err=%v output=%q stderr=%q", err, output.String(), stderr.String())
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "RUN\n") != 2 {
		t.Fatalf("deadline deduplication opened the harness unexpectedly:\n%s", log)
	}
}

func TestScheduleTriggerValidatesTurnTimeoutIndependently(t *testing.T) {
	for _, value := range []string{"0s", "31m"} {
		var output, stderr bytes.Buffer
		err := Run([]string{"schedule", "trigger", "agent", "sweep", "--harness", "claude", "--input-id", "occurrence-1", "--turn-timeout", value}, strings.NewReader(""), &output, &stderr, "")
		if err == nil || !strings.Contains(err.Error(), "--turn-timeout must be greater than zero and at most 30m") {
			t.Fatalf("turn timeout %q error = %v", value, err)
		}
	}
}

func TestScheduleRunHelpAndValidation(t *testing.T) {
	var output, stderr bytes.Buffer
	if err := Run([]string{"schedule", "run", "--help"}, strings.NewReader(""), &output, &stderr, ""); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "hctl schedule run AGENT") || !strings.Contains(got, "--max-active-turns N") {
		t.Fatalf("help=%q", got)
	}
	if err := Run([]string{"schedule", "run"}, strings.NewReader(""), &output, &stderr, ""); err == nil || !strings.Contains(err.Error(), "schedule run AGENT") {
		t.Fatalf("missing agent error=%v", err)
	}
	for _, args := range [][]string{
		{"schedule", "run", "agent", "--harness", "claude", "--turn-timeout", "0s"},
		{"schedule", "run", "agent", "--harness", "claude", "--max-active-turns", "0"},
	} {
		if err := Run(args, strings.NewReader(""), &output, &stderr, ""); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	if err := Run([]string{"schedule", "run", root, "--harness", "claude"}, strings.NewReader(""), &output, &stderr, ""); err == nil || err.Error() != "agent project defines no schedules" {
		t.Fatalf("no schedules error=%v", err)
	}
}

func TestScheduleRunCancellationStopsHarnessVerification(t *testing.T) {
	source, workspace := t.TempDir(), t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '* * * * *'\n---\n\nSweep.\n", 0o644)
	command := filepath.Join(t.TempDir(), "claude")
	writeCLIFile(t, command, "#!/bin/sh\necho '2.1.221 (Claude Code)'\n", 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var applyOut, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "claude", "--command", command}, strings.NewReader(""), &applyOut, &stderr, self); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "verifying")
	t.Setenv("VERIFY_MARKER", marker)
	writeCLIFile(t, command, "#!/bin/sh\n: > \"$VERIFY_MARKER\"\nexec sleep 300\n", 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runScheduleClockContext(ctx, []string{source, "--workspace", workspace, "--harness", "claude", "--command", command}, io.Discard, &stderr, "", nil)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("harness verification did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled verification succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled harness verification did not stop")
	}
}

func TestScheduleTriggerRunsFreshCodexTaskAndDiscardsOutput(t *testing.T) {
	source := t.TempDir()
	workspace := t.TempDir()
	writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
	writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '0 9 * * 1-5'\n---\n\nSweep stale work.\n", 0o644)
	logPath := filepath.Join(t.TempDir(), "codex.log")
	t.Setenv("FAKE_LOG", logPath)
	command := filepath.Join(t.TempDir(), "codex")
	writeCLIFile(t, command, `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  echo "codex-cli 0.144.1"
  exit 0
fi
printf 'ARGS' >> "$FAKE_LOG"
for arg in "$@"; do printf '\t%s' "$arg" >> "$FAKE_LOG"; done
printf '\n' >> "$FAKE_LOG"
while IFS= read -r line; do
  printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
  case "$line" in
    *'"method":"initialize"'*)
      echo '{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}'
      ;;
    *'"method":"thread/start"'*|*'"method":"thread/resume"'*)
      echo '{"id":2,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"}}}'
      ;;
    *'"method":"turn/start"'*)
      turn_id="01922222-2222-7222-8222-222222222222"
      echo '{"id":3,"result":{"turn":{"id":"01922222-2222-7222-8222-222222222222","items":[],"status":"inProgress"}}}'
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"01933333-3333-7333-8333-333333333333","turnId":"01944444-4444-7444-8444-444444444444","itemId":"child-item","delta":"child"}}\n'
      printf '{"method":"turn/completed","params":{"threadId":"01933333-3333-7333-8333-333333333333","turn":{"id":"01944444-4444-7444-8444-444444444444","items":[],"status":"completed"}}}\n'
      printf '{"method":"item/agentMessage/delta","params":{"threadId":"01911111-1111-7111-8111-111111111111","turnId":"%s","itemId":"item-1","delta":"SECRET MODEL OUTPUT"}}\n' "$turn_id"
      printf '{"method":"turn/completed","params":{"threadId":"01911111-1111-7111-8111-111111111111","turn":{"id":"%s","items":[],"status":"completed"}}}\n' "$turn_id"
      ;;
  esac
done
`, 0o755)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output, stderr bytes.Buffer
	if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", "codex", "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
		t.Fatal(err)
	}

	for _, inputID := range []string{"occurrence-1", "occurrence-1", "occurrence-2"} {
		output.Reset()
		stderr.Reset()
		if err := Run([]string{"schedule", "trigger", source, "sweep", "--workspace", workspace, "--harness", "codex", "--input-id", inputID, "--command", command}, strings.NewReader(""), &output, &stderr, self); err != nil {
			log, _ := os.ReadFile(logPath)
			t.Fatalf("%v\n%s", err, log)
		}
		if strings.Contains(output.String(), "SECRET MODEL OUTPUT") || !strings.Contains(output.String(), "status=completed") {
			t.Fatalf("Codex schedule output = %q", output.String())
		}
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), `"method":"thread/start"`) != 2 || strings.Contains(string(log), `"method":"thread/resume"`) {
		t.Fatalf("Codex schedule tasks were not fresh and deduplicated:\n%s", log)
	}
}

type cliScheduleClock struct {
	mu     sync.Mutex
	now    time.Time
	timers chan *cliScheduleTimer
}
type cliScheduleTimer struct{ c chan time.Time }

func (c *cliScheduleClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *cliScheduleClock) NewTimer(time.Duration) schedule.Timer {
	timer := &cliScheduleTimer{c: make(chan time.Time, 1)}
	c.timers <- timer
	return timer
}
func (t *cliScheduleTimer) C() <-chan time.Time { return t.c }
func (*cliScheduleTimer) Stop() bool            { return true }
func (c *cliScheduleClock) wake(t *testing.T, at time.Time) {
	t.Helper()
	select {
	case timer := <-c.timers:
		c.mu.Lock()
		c.now = at
		c.mu.Unlock()
		timer.c <- at
	case <-time.After(time.Second):
		t.Fatal("schedule timer was not created")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.Buffer.String() }

func TestScheduleRunForegroundFakeClaudeAndCodex(t *testing.T) {
	commands := map[string]string{
		"claude": `#!/bin/sh
if [ "${1-}" = "--version" ]; then echo "2.1.221 (Claude Code)"; exit 0; fi
IFS= read -r line || exit 1
printf '{"type":"system","subtype":"init","session_id":"session-fake"}\n'
printf '{"type":"stream_event","session_id":"session-fake","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"SECRET MODEL OUTPUT"}}}\n'
printf '{"type":"result","subtype":"success","is_error":false,"session_id":"session-fake","result":"SECRET MODEL OUTPUT"}\n'
`,
		"codex": `#!/bin/sh
if [ "${1-}" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
while IFS= read -r line; do
 printf 'WIRE\t%s\n' "$line" >> "$FAKE_LOG"
 case "$line" in
  *'"method":"initialize"'*) echo '{"id":1,"result":{"codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos","userAgent":"codex-cli/0.144.1"}}' ;;
  *'"method":"thread/start"'*|*'"method":"thread/resume"'*)
    echo '{"id":2,"result":{"thread":{"id":"01911111-1111-7111-8111-111111111111"}}}'
    ;;
  *'"method":"turn/start"'*)
    turn_id="01922222-2222-7222-8222-222222222222"
    echo '{"id":3,"result":{"turn":{"id":"01922222-2222-7222-8222-222222222222","items":[],"status":"inProgress"}}}'
    printf '{"method":"item/agentMessage/delta","params":{"threadId":"01933333-3333-7333-8333-333333333333","turnId":"01944444-4444-7444-8444-444444444444","itemId":"child-item","delta":"child"}}\n'
    printf '{"method":"turn/completed","params":{"threadId":"01933333-3333-7333-8333-333333333333","turn":{"id":"01944444-4444-7444-8444-444444444444","items":[],"status":"completed"}}}\n'
    printf '{"method":"item/agentMessage/delta","params":{"threadId":"01911111-1111-7111-8111-111111111111","turnId":"%s","itemId":"item-1","delta":"SECRET MODEL OUTPUT"}}\n' "$turn_id"
    printf '{"method":"turn/completed","params":{"threadId":"01911111-1111-7111-8111-111111111111","turn":{"id":"%s","items":[],"status":"completed"}}}\n' "$turn_id"
    ;;
 esac
done
`,
	}
	for harnessName, script := range commands {
		t.Run(harnessName, func(t *testing.T) {
			source, workspace := t.TempDir(), t.TempDir()
			writeCLIFile(t, filepath.Join(source, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n", 0o644)
			writeCLIFile(t, filepath.Join(source, "schedules", "sweep.md"), "---\ncron: '* * * * *'\n---\n\nSweep stale work.\n", 0o644)
			command := filepath.Join(t.TempDir(), harnessName)
			logPath := filepath.Join(t.TempDir(), harnessName+".log")
			t.Setenv("FAKE_LOG", logPath)
			writeCLIFile(t, command, script, 0o755)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			var applyOut, stderr bytes.Buffer
			if err := Run([]string{"apply", source, "--workspace", workspace, "--harness", harnessName, "--command", command}, strings.NewReader(""), &applyOut, &stderr, self); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
			clock := &cliScheduleClock{now: start, timers: make(chan *cliScheduleTimer, 8)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output lockedBuffer
			done := make(chan error, 1)
			go func() {
				done <- runScheduleClockContext(ctx, []string{source, "--workspace", workspace, "--harness", harnessName, "--command", command}, &output, &stderr, "", clock)
			}()
			clock.wake(t, start.Add(time.Minute))
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && !strings.Contains(output.String(), "status=completed") {
				time.Sleep(time.Millisecond)
			}
			if got := output.String(); !strings.Contains(got, "status=completed") || strings.Contains(got, "SECRET MODEL OUTPUT") || strings.Contains(got, "Sweep stale work") {
				log, _ := os.ReadFile(logPath)
				t.Fatalf("schedule run output=%q stderr=%q log=%s", got, stderr.String(), log)
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func writeCLIFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func copyCLITree(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, info.Mode().Perm())
	}); err != nil {
		t.Fatal(err)
	}
}

type markedTerminalReader struct {
	io.Reader
}

func (*markedTerminalReader) Terminal() bool { return true }

type acquisitionTransportReader struct {
	io.Reader
	transport http.RoundTripper
}

func (reader *acquisitionTransportReader) acquisitionArchiveTransportForTesting() http.RoundTripper {
	return reader.transport
}

func writeCLIPluginFixture(t *testing.T, root, resource string) {
	t.Helper()
	writeCLIFile(t, filepath.Join(root, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"review-pack"}`, 0o644)
	writeCLIFile(t, filepath.Join(root, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"review-server":{"type":"stdio","command":"./bin/server"}}}`, 0o644)
	writeCLIFile(t, filepath.Join(root, "bin", "server"), "#!/bin/sh\n", 0o755)
	writeCLIFile(t, filepath.Join(root, "binary"), string([]byte{0, 1, 2, 255}), 0o644)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIFile(t, filepath.Join(root, "skills", "plugin-review", "SKILL.md"), "---\nname: plugin-review\ndescription: Review from the Plugin.\n---\n", 0o644)
	writeCLIFile(t, filepath.Join(root, "resource.txt"), resource, 0o644)
}

func writeCLISkillFixture(t *testing.T, root, guide string) {
	t.Helper()
	writeCLIFile(t, filepath.Join(root, "SKILL.md"), "---\nname: review\ndescription: Review carefully.\n---\n\nReview.\n", 0o644)
	writeCLIFile(t, filepath.Join(root, "scripts", "review.sh"), "#!/bin/sh\n", 0o755)
	writeCLIFile(t, filepath.Join(root, "references", "guide.txt"), guide, 0o644)
	writeCLIFile(t, filepath.Join(root, "references", "binary"), string([]byte{0, 1, 2, 255}), 0o644)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func cliComponentZIP(t *testing.T, root, rootName string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	walkCLIFixture(t, root, func(path string, info os.FileInfo, data []byte) {
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			t.Fatal(err)
		}
		header.Name = rootName + "/" + path
		if info.IsDir() {
			header.Name += "/"
		}
		header.Method = zip.Deflate
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func cliComponentTarGzip(t *testing.T, root, rootName string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	writer := tar.NewWriter(gzipWriter)
	walkCLIFixture(t, root, func(path string, info os.FileInfo, data []byte) {
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			t.Fatal(err)
		}
		header.Name = rootName + "/" + path
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func walkCLIFixture(t *testing.T, root string, visit func(string, os.FileInfo, []byte)) {
	t.Helper()
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		visit(filepath.ToSlash(relative), info, data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func cliSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func runCLIGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func mustReadCLITree(t *testing.T, root string) acquisition.Tree {
	t.Helper()
	tree, err := acquisition.ReadTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func readCLIFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeCLIGitHubPackage(t *testing.T, packageVersion string, payload []byte) string {
	t.Helper()
	root := t.TempDir()
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	artifactID := runtime.GOOS + "-" + runtime.GOARCH
	document := map[string]any{
		"schema_version": 1, "id": "github-mcp-server", "version": packageVersion, "name": "GitHub fixture", "description": "Credential-free scheduled fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://github.com/github/github-mcp-server", "revision": "v" + packageVersion},
		"compatibility": map[string]any{"minimum": "0.1.0-dev", "before": "9.0.0"},
		"artifacts": []any{map[string]any{
			"id": artifactID, "os": runtime.GOOS, "architecture": runtime.GOARCH, "format": "binary", "source": map[string]any{"kind": "package", "path": "payload/github-mcp-server"}, "size": len(payload), "sha256": checksum,
			"executable": map[string]any{"path": "github-mcp-server", "size": len(payload), "sha256": checksum},
		}},
		"capabilities": []any{map[string]any{
			"type": "native-mcp", "version": 1, "id": "github", "server_name": "github", "collision": "reject", "artifacts": []string{artifactID}, "executable": "github-mcp-server", "arguments": []string{"stdio"}, "working_directory": ".", "environment": map[string]string{},
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
	writeCLIFile(t, filepath.Join(root, "integration.json"), string(manifest)+"\n", 0o600)
	writeCLIFile(t, filepath.Join(root, "payload", "github-mcp-server"), string(payload), 0o600)
	return root
}

func installCLIChannelAdapter(t *testing.T, store *integration.Store) {
	t.Helper()
	installCLIChannelAdapterPayload(t, store, []byte("#!/bin/sh\nexit 0\n"))
}

func installCLIChannelAdapterPayload(t *testing.T, store *integration.Store, payload []byte) {
	t.Helper()
	digest := sha256.Sum256(payload)
	checksum := hex.EncodeToString(digest[:])
	artifactID := runtime.GOOS + "-" + runtime.GOARCH
	document := map[string]any{
		"schema_version": 1, "id": "hctl-discord", "version": "1.0.0", "name": "Discord adapter fixture", "description": "Credential-free channel guard fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://example.invalid/hctl-discord", "revision": "fixture-v1"},
		"compatibility": map[string]any{"minimum": "0.1.0-dev", "before": "9.0.0"},
		"artifacts": []any{map[string]any{
			"id": artifactID, "os": runtime.GOOS, "architecture": runtime.GOARCH, "format": "binary", "source": map[string]any{"kind": "package", "path": "payload/hctl-discord"}, "size": len(payload), "sha256": checksum,
			"executable": map[string]any{"path": "bin/hctl-discord", "size": len(payload), "sha256": checksum},
		}},
		"capabilities": []any{map[string]any{
			"type": "channel-adapter", "version": 1, "id": "discord", "channel_kind": "discord", "artifacts": []string{artifactID}, "executable": "bin/hctl-discord",
			"runtime": map[string]any{"arguments": []string{"runtime", "--stdio"}}, "setup": map[string]any{"arguments": []string{"setup"}}, "status": map[string]any{"arguments": []string{"status"}}, "remove": map[string]any{"arguments": []string{"remove"}},
			"protocol": map[string]any{"minimum": 1, "before": 2}, "profile_selector": "opaque-id-v1", "features": []string{"typing", "replies", "text-fallback"},
		}},
	}
	manifest, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeCLIFile(t, filepath.Join(root, "integration.json"), string(manifest)+"\n", 0o600)
	writeCLIFile(t, filepath.Join(root, "payload", "hctl-discord"), string(payload), 0o600)
	if _, err := store.Install(context.Background(), integration.InstallOptions{Source: root, Trust: integration.TrustOperator}); err != nil {
		t.Fatal(err)
	}
}

type countingOpenDriver struct{ opens int }

func (d *countingOpenDriver) Name() string                 { return "codex" }
func (d *countingOpenDriver) Executable() string           { return "/usr/bin/true" }
func (d *countingOpenDriver) Verify(context.Context) error { return nil }
func (d *countingOpenDriver) Open(context.Context, harness.OpenRequest) (harness.Session, error) {
	d.opens++
	return countingSession{}, nil
}

type countingContinuationDriver struct {
	countingOpenDriver
	continuations int
}

func (d *countingContinuationDriver) ContinueTurn(context.Context, harness.OpenRequest, string, interaction.ContinuationIntent, func(harness.Event)) interaction.ContinuationResult {
	d.continuations++
	return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed"}
}

type countingDeferredDriver struct {
	countingOpenDriver
	continuations int
}

func (d *countingDeferredDriver) ResumeDeferredTool(context.Context, harness.OpenRequest, string, interaction.ContinuationIntent, func(harness.Event)) interaction.ContinuationResult {
	d.continuations++
	return interaction.ContinuationResult{Effect: interaction.EffectSucceeded, OriginOutcome: "completed"}
}

type countingSession struct{}

func (countingSession) InitialEvents() []harness.Event { return nil }
func (countingSession) RunTurn(context.Context, harness.Input, func(harness.Event)) (harness.TurnResult, error) {
	return harness.TurnResult{Status: "completed"}, nil
}
func (countingSession) Close() error { return nil }
func (countingSession) Abort()       {}
