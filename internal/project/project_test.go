package project

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDiscoversConventionalProjectDeterministically(t *testing.T) {
	root := agent(t, "My Agent")
	first, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "my-agent" {
		t.Fatalf("derived name = %q", first.Name)
	}
	if first.SourceFingerprint != second.SourceFingerprint {
		t.Fatal("same source produced different fingerprints")
	}
	if len(first.Skills) != 1 || len(first.Skills[0].Files) != 1 || first.Skills[0].Files[0].Path != "SKILL.md" {
		t.Fatalf("discovered skills = %#v", first.Skills)
	}

	write(t, filepath.Join(root, "skills", "research", "SKILL.md"), "---\nname: research\ndescription: Research carefully.\n---\n\nFind evidence.\n")
	changed, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if changed.SourceFingerprint == first.SourceFingerprint {
		t.Fatal("adding a conventional skill did not change the fingerprint")
	}
	if len(changed.Skills) != 2 || changed.Skills[1].Name != "research" {
		t.Fatalf("discovered skills = %#v", changed.Skills)
	}
}

func TestToolSourceChangesFingerprint(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "instructions.md"), instructions("Be concise."))
	write(t, filepath.Join(root, "tools", "add.py"), "description = 'add'\n")
	write(t, filepath.Join(root, "pyproject.toml"), "[project]\nname = 'agent'\nversion = '0'\n")
	write(t, filepath.Join(root, "uv.lock"), "version = 1\n")
	first, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "tools", "add.py"), "description = 'changed'\n")
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceFingerprint == second.SourceFingerprint {
		t.Fatal("tool source change did not change the fingerprint")
	}
}

func TestLoadDiscoversGitHubConnectionForBothHarnesses(t *testing.T) {
	root := agent(t, "portable")
	baseline, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if baseline.GitHubConnection != nil {
		t.Fatalf("missing connection = %#v", baseline.GitHubConnection)
	}
	path := filepath.Join(root, "connections", "github.md")
	write(t, path, "Read public GitHub repositories and issues.\n")

	for _, harness := range []string{"claude", "codex"} {
		loaded, err := Load(root, harness)
		if err != nil {
			t.Fatalf("%s: %v", harness, err)
		}
		if loaded.GitHubConnection == nil || loaded.GitHubConnection.Description != "Read public GitHub repositories and issues." || loaded.GitHubConnection.Path != "connections/github.md" {
			t.Fatalf("%s GitHub connection = %#v", harness, loaded.GitHubConnection)
		}
		if loaded.SourceFingerprint == baseline.SourceFingerprint {
			t.Fatalf("%s connection did not join the source fingerprint", harness)
		}
	}

	first, _ := Load(root, "claude")
	write(t, path, "Read public GitHub data carefully.\n")
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceFingerprint == second.SourceFingerprint {
		t.Fatal("connection description change did not change the source fingerprint")
	}
}

func TestLoadRejectsInvalidConnections(t *testing.T) {
	tests := map[string]struct {
		path    string
		content []byte
		want    string
	}{
		"empty GitHub description":     {"connections/github.md", nil, "must contain 1-1024"},
		"oversized GitHub description": {"connections/github.md", bytes.Repeat([]byte("a"), 1025), "must contain 1-1024"},
		"non-UTF-8 GitHub description": {"connections/github.md", []byte{0xff}, "valid UTF-8"},
		"unsupported file":             {"connections/gitlab.md", []byte("GitLab.\n"), "supports github.md only"},
		"unsupported directory":        {"connections/github/connection.md", []byte("GitHub.\n"), "supports github.md only"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			writeBytes(t, filepath.Join(root, filepath.FromSlash(test.path)), test.content, 0o644)
			if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid connection was not rejected with %q: %v", test.want, err)
			}
		})
	}

	t.Run("connection file symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "github.md")
		write(t, outside, "GitHub.\n")
		path := filepath.Join(root, "connections", "github.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("connection symlink was not rejected: %v", err)
		}
	})

	t.Run("connections directory symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := t.TempDir()
		write(t, filepath.Join(outside, "github.md"), "GitHub.\n")
		if err := os.Symlink(outside, filepath.Join(root, "connections")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("connections directory symlink was not rejected: %v", err)
		}
	})
}

func TestLoadDiscoversDiscordChannelForBothHarnesses(t *testing.T) {
	root := agent(t, "portable")
	baseline, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "channels", "discord.md")
	write(t, path, "---\nmode: ambient\n---\n\nParticipate in hctl work.\n")

	for _, harness := range []string{"claude", "codex"} {
		loaded, err := Load(root, harness)
		if err != nil {
			t.Fatalf("%s: %v", harness, err)
		}
		if loaded.DiscordChannel == nil || loaded.DiscordChannel.Mode != "ambient" || string(loaded.DiscordChannel.Policy) != "Participate in hctl work.\n" || loaded.DiscordChannel.Path != "channels/discord.md" {
			t.Fatalf("%s Discord channel = %#v", harness, loaded.DiscordChannel)
		}
		if loaded.SourceFingerprint == baseline.SourceFingerprint {
			t.Fatalf("%s channel did not join the source fingerprint", harness)
		}
	}

	first, _ := Load(root, "claude")
	write(t, path, "---\nmode: ambient\n---\n\nParticipate carefully in Discord work.\n")
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceFingerprint == second.SourceFingerprint {
		t.Fatal("channel description change did not change source fingerprint")
	}
}

func TestLoadRejectsInvalidChannels(t *testing.T) {
	tests := map[string]struct {
		path    string
		content []byte
		want    string
	}{
		"empty Discord policy":          {"channels/discord.md", []byte("---\nmode: ambient\n---\n"), "must contain 1-1024"},
		"oversized Discord policy":      {"channels/discord.md", append([]byte("---\nmode: ambient\n---\n"), bytes.Repeat([]byte("a"), 1025)...), "must contain 1-1024"},
		"non-UTF-8 Discord description": {"channels/discord.md", []byte{0xff}, "valid UTF-8"},
		"unsupported file":              {"channels/slack.md", []byte("Slack.\n"), "supports discord.md only"},
		"unsupported directory":         {"channels/discord/channel.md", []byte("Discord.\n"), "supports discord.md only"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			writeBytes(t, filepath.Join(root, filepath.FromSlash(test.path)), test.content, 0o644)
			if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid channel was not rejected with %q: %v", test.want, err)
			}
		})
	}

	t.Run("channel file symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "discord.md")
		write(t, outside, "Discord.\n")
		path := filepath.Join(root, "channels", "discord.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("channel symlink was not rejected: %v", err)
		}
	})

	t.Run("channels directory symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := t.TempDir()
		write(t, filepath.Join(outside, "discord.md"), "Discord.\n")
		if err := os.Symlink(outside, filepath.Join(root, "channels")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("channels directory symlink was not rejected: %v", err)
		}
	})
}

func TestLoadDiscoversNestedSchedulesForBothHarnesses(t *testing.T) {
	root := agent(t, "portable")
	baseline, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "schedules", "billing", "sweep.md")
	write(t, path, "---\ncron: \"0 9 * * 1-5\"\n---\n\nSweep stale billing work.\n")

	for _, harness := range []string{"claude", "codex"} {
		loaded, err := Load(root, harness)
		if err != nil {
			t.Fatalf("%s: %v", harness, err)
		}
		if len(loaded.Schedules) != 1 || loaded.Schedules[0].Name != "billing/sweep" || loaded.Schedules[0].Cron != "0 9 * * 1-5" || string(loaded.Schedules[0].Prompt) != "Sweep stale billing work.\n" {
			t.Fatalf("%s schedules = %#v", harness, loaded.Schedules)
		}
		if loaded.SourceFingerprint == baseline.SourceFingerprint {
			t.Fatalf("%s schedule did not join the source fingerprint", harness)
		}
	}

	first, _ := Load(root, "claude")
	write(t, path, "---\ncron: \"0 10 * * 1-5\"\n---\n\nSweep stale billing work.\n")
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceFingerprint == second.SourceFingerprint {
		t.Fatal("schedule change did not change source fingerprint")
	}
}

func TestSchedulePreservesPathNameAndMarkdownBody(t *testing.T) {
	root := agent(t, "portable")
	path := filepath.Join(root, "schedules", "Daily Reports", "Weekly_Sweep.md")
	write(t, path, "---\ncron: '0 9 * * MON-FRI'\n---\n\n    preserve this code block\n\n")
	loaded, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Schedules) != 1 || loaded.Schedules[0].Name != "Daily Reports/Weekly_Sweep" || string(loaded.Schedules[0].Prompt) != "    preserve this code block\n\n" {
		t.Fatalf("schedule = %#v", loaded.Schedules)
	}
}

func TestLoadRejectsInvalidSchedules(t *testing.T) {
	tests := map[string]struct {
		path    string
		content []byte
		want    string
	}{
		"non-Markdown schedule": {"schedules/task.ts", []byte("export default {}\n"), "Markdown files only"},
		"missing frontmatter":   {"schedules/task.md", []byte("Run.\n"), "YAML frontmatter"},
		"unknown field":         {"schedules/task.md", []byte("---\ncron: '* * * * *'\ntimezone: UTC\n---\nRun.\n"), "one cron field only"},
		"duplicate cron":        {"schedules/task.md", []byte("---\ncron: '* * * * *'\ncron: '0 * * * *'\n---\nRun.\n"), "duplicated"},
		"non-string cron":       {"schedules/task.md", []byte("---\ncron: 5\n---\nRun.\n"), "must be a string"},
		"wrong field count":     {"schedules/task.md", []byte("---\ncron: '* * * *'\n---\nRun.\n"), "five-field"},
		"invalid cron":          {"schedules/task.md", []byte("---\ncron: 'foo bar baz qux quux'\n---\nRun.\n"), "valid standard"},
		"empty prompt":          {"schedules/task.md", []byte("---\ncron: '* * * * *'\n---\n"), "body must be non-empty"},
		"oversized prompt":      {"schedules/task.md", append([]byte("---\ncron: '* * * * *'\n---\n"), bytes.Repeat([]byte("a"), (32<<10)+1)...), "body exceeds"},
		"non-UTF-8":             {"schedules/task.md", []byte{0xff}, "valid UTF-8"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			writeBytes(t, filepath.Join(root, filepath.FromSlash(test.path)), test.content, 0o644)
			if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid schedule was not rejected with %q: %v", test.want, err)
			}
		})
	}

	t.Run("schedule symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "task.md")
		write(t, outside, "---\ncron: '* * * * *'\n---\nRun.\n")
		path := filepath.Join(root, "schedules", "task.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("schedule symlink was not rejected: %v", err)
		}
	})
}

func TestLoadAllowsInstructionsWithoutSkills(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Simple Helper")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "instructions.md"), instructions("Help the user."))
	p, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "simple-helper" || len(p.Skills) != 0 {
		t.Fatalf("project = name %q, skills %#v", p.Name, p.Skills)
	}
}

func TestLoadSeparatesPortableSourceFromWorkspace(t *testing.T) {
	source := agent(t, "reviewer")
	workspace := t.TempDir()
	p, err := Load(source, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceRoot == p.WorkspaceRoot || p.SourceReference == "." {
		t.Fatalf("project roots = source %q workspace %q reference %q", p.SourceRoot, p.WorkspaceRoot, p.SourceReference)
	}
	if string(p.Instructions) != "Be concise.\n" || p.Description != "Test agent." {
		t.Fatalf("parsed instructions = description %q body %q", p.Description, p.Instructions)
	}

	standalone, err := Load(source, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if standalone.SourceRoot != standalone.WorkspaceRoot || standalone.SourceReference != "." {
		t.Fatalf("standalone roots = %#v", standalone)
	}
}

func TestLoadDiscoversInstructionsOnlySubagents(t *testing.T) {
	root := agent(t, "portable")
	write(t, filepath.Join(root, "subagents", "docs-reviewer", "instructions.md"), "---\ndescription: Review documentation.\n---\n\nCheck links.\n")
	first, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Subagents) != 1 || first.Subagents[0].Name != "docs-reviewer" || string(first.Subagents[0].Instructions) != "Check links.\n" {
		t.Fatalf("subagents = %#v", first.Subagents)
	}
	write(t, filepath.Join(root, "subagents", "docs-reviewer", "instructions.md"), "---\ndescription: Review documentation.\n---\n\nCheck links and headings.\n")
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceFingerprint == second.SourceFingerprint {
		t.Fatal("subagent source change did not change fingerprint")
	}
}

func TestLoadSubagentEffort(t *testing.T) {
	root := agent(t, "portable")
	path := filepath.Join(root, "subagents", "docs-reviewer", "instructions.md")
	write(t, path, "---\ndescription: Review documentation.\n---\n\nCheck links.\n")
	baseline, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Subagents[0].Effort != "" {
		t.Fatalf("description-only effort = %q", baseline.Subagents[0].Effort)
	}

	fingerprints := map[string]bool{baseline.SourceFingerprint: true}
	for _, effort := range []string{"low", "medium", "high"} {
		write(t, path, "---\ndescription: Review documentation.\neffort: "+effort+"\n---\n\nCheck links.\n")
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatalf("effort %s: %v", effort, err)
		}
		if got := loaded.Subagents[0].Effort; got != effort {
			t.Fatalf("effort = %q, want %q", got, effort)
		}
		if fingerprints[loaded.SourceFingerprint] {
			t.Fatalf("effort %q did not produce a distinct source fingerprint", effort)
		}
		fingerprints[loaded.SourceFingerprint] = true
	}
}

func TestLoadRejectsInvalidSubagentEffortFrontmatter(t *testing.T) {
	tests := map[string]struct {
		frontmatter string
		want        string
	}{
		"unknown field":      {"description: Review.\nfuture: true", `field "future" is not supported`},
		"duplicate field":    {"description: Review.\neffort: low\neffort: high", `field "effort" is duplicated`},
		"non-string effort":  {"description: Review.\neffort: true", `field "effort" must be a string`},
		"empty effort":       {"description: Review.\neffort: ''", `must be low, medium, or high`},
		"unsupported effort": {"description: Review.\neffort: ultra", `must be low, medium, or high`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, "subagents", "reviewer", "instructions.md"), "---\n"+test.frontmatter+"\n---\n\nReview.\n")
			if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid frontmatter was not rejected clearly: %v", err)
			}
		})
	}
}

func TestLoadKeepsRootInstructionsDescriptionOnly(t *testing.T) {
	root := agent(t, "portable")
	write(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\neffort: high\n---\n\nBe concise.\n")
	if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "one plain description only") {
		t.Fatalf("root effort was not rejected: %v", err)
	}
}

func TestLoadDiscoversOnlySelectedHarnessFiles(t *testing.T) {
	root := agent(t, "portable")
	claudeSettings := filepath.Join(root, "harnesses", "claude", ".claude", "settings.json")
	claudeHook := filepath.Join(root, "harnesses", "claude", ".claude", "hooks", "check.sh")
	codexRules := filepath.Join(root, "harnesses", "codex", ".codex", "rules", "default.rules")
	write(t, claudeSettings, "{\"permissions\":{}}\n")
	writeBytes(t, claudeHook, []byte("#!/bin/sh\n"), 0o755)
	write(t, codexRules, "prefix_rule(pattern = [\"git\", \"status\"], decision = \"allow\")\n")

	claude, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	codex, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(claude.HarnessFiles) != 2 || claude.HarnessFiles[0].Path != ".claude/hooks/check.sh" || !claude.HarnessFiles[0].Executable || claude.HarnessFiles[1].Path != ".claude/settings.json" {
		t.Fatalf("Claude harness files = %#v", claude.HarnessFiles)
	}
	if len(codex.HarnessFiles) != 1 || codex.HarnessFiles[0].Path != ".codex/rules/default.rules" {
		t.Fatalf("Codex harness files = %#v", codex.HarnessFiles)
	}

	write(t, claudeSettings, "{\"permissions\":{\"allow\":[]}}\n")
	changedClaude, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	unchangedCodex, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if changedClaude.SourceFingerprint == claude.SourceFingerprint {
		t.Fatal("selected harness file change did not change source fingerprint")
	}
	if unchangedCodex.SourceFingerprint != codex.SourceFingerprint {
		t.Fatal("Claude-only file changed Codex source fingerprint")
	}
}

func TestLoadRejectsUnsafeHarnessFiles(t *testing.T) {
	tests := map[string]struct {
		harness string
		path    string
		want    string
	}{
		"wrong native directory": {"claude", "harnesses/claude/.codex/rules/default.rules", "supports .claude only"},
		"Claude skills reserved": {"claude", "harnesses/claude/.claude/skills/custom.md", "reserved for hctl"},
		"Claude skills alias":    {"claude", "harnesses/claude/.claude/Skills/custom.md", "reserved for hctl"},
		"Claude agents reserved": {"claude", "harnesses/claude/.claude/agents/custom.md", "reserved for hctl"},
		"Codex config reserved":  {"codex", "harnesses/codex/.codex/config.toml", "reserved for hctl"},
		"Codex config alias":     {"codex", "harnesses/codex/.codex/CONFIG.toml", "reserved for hctl"},
		"Codex agents reserved":  {"codex", "harnesses/codex/.codex/agents/custom.toml", "reserved for hctl"},
		"Codex agents alias":     {"codex", "harnesses/codex/.codex/Agents/custom.toml", "reserved for hctl"},
		"nonportable path":       {"claude", "harnesses/claude/.claude/bad\\name", "invalid path"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, filepath.FromSlash(test.path)), "content\n")
			if _, err := Load(root, test.harness); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe harness file was not rejected with %q: %v", test.want, err)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "outside.json")
		write(t, outside, "{}\n")
		path := filepath.Join(root, "harnesses", "claude", ".claude", "settings.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("harness file symlink was not rejected: %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		root := agent(t, "portable")
		writeBytes(t, filepath.Join(root, "harnesses", "claude", ".claude", "large.bin"), make([]byte, maxSkillFileBytes+1), 0o644)
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized harness file was not rejected: %v", err)
		}
	})
}

func TestLoadParsesAgentSkillsAndResources(t *testing.T) {
	root := agent(t, "portable")
	skillRoot := filepath.Join(root, "skills", "echo")
	write(t, filepath.Join(skillRoot, "SKILL.md"), `---
name: echo
description: >-
  Repeat text
  when asked.
license: Apache-2.0
compatibility: Requires a shell.
metadata:
  author: example
  version: "1"
allowed-tools: Bash(echo:*) Read
hooks:
  PreToolUse:
    - matcher: Bash
argument-hint: "[text]"
---

Use echo.
`)
	write(t, filepath.Join(skillRoot, "references", "guide.md"), "guide\n")
	writeBytes(t, filepath.Join(skillRoot, "assets", "binary.dat"), []byte{0xff, 0x00, 0x01}, 0o644)
	writeBytes(t, filepath.Join(skillRoot, "scripts", "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	write(t, filepath.Join(skillRoot, "other", "nested", "resource.txt"), "arbitrary\n")

	p, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	skill := p.Skills[0]
	if skill.Description != "Repeat text when asked." || skill.License != "Apache-2.0" || skill.Compatibility != "Requires a shell." {
		t.Fatalf("portable metadata = %#v", skill)
	}
	if skill.Metadata["author"] != "example" || skill.Metadata["version"] != "1" || !skill.AllowedToolsPresent || skill.AllowedTools != "Bash(echo:*) Read" {
		t.Fatalf("structured metadata = %#v", skill)
	}
	if fmt.Sprint(skill.ClaudeFields) != "[argument-hint hooks]" {
		t.Fatalf("Claude fields = %v", skill.ClaudeFields)
	}
	wantPaths := "[SKILL.md assets/binary.dat other/nested/resource.txt references/guide.md scripts/run.sh]"
	paths := make([]string, len(skill.Files))
	for index, file := range skill.Files {
		paths[index] = file.Path
		if file.Path == "scripts/run.sh" && !file.Executable {
			t.Fatal("executable resource lost its executable intent")
		}
	}
	if fmt.Sprint(paths) != wantPaths {
		t.Fatalf("resource paths = %v", paths)
	}
	if got := skill.Files[1].Content; len(got) != 3 || got[0] != 0xff {
		t.Fatalf("binary resource = %v", got)
	}
}

func TestSkillResourcesChangeFingerprint(t *testing.T) {
	root := agent(t, "portable")
	resource := filepath.Join(root, "skills", "echo", "scripts", "run.sh")
	baseline, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, resource, []byte("#!/bin/sh\n"), 0o644)
	added, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if added.SourceFingerprint == baseline.SourceFingerprint {
		t.Fatal("adding a resource did not change source fingerprint")
	}
	renamedResource := filepath.Join(root, "skills", "echo", "scripts", "renamed.sh")
	if err := os.Rename(resource, renamedResource); err != nil {
		t.Fatal(err)
	}
	renamed, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.SourceFingerprint == added.SourceFingerprint {
		t.Fatal("renaming a resource did not change source fingerprint")
	}
	if err := os.Remove(renamedResource); err != nil {
		t.Fatal(err)
	}
	removed, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if removed.SourceFingerprint != baseline.SourceFingerprint {
		t.Fatal("removing the only resource did not restore source fingerprint")
	}

	writeBytes(t, resource, []byte("#!/bin/sh\n"), 0o644)
	first, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, resource, []byte("#!/bin/sh\necho changed\n"), 0o644)
	contentChanged, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if contentChanged.SourceFingerprint == first.SourceFingerprint {
		t.Fatal("resource content did not change source fingerprint")
	}
	if err := os.Chmod(resource, 0o755); err != nil {
		t.Fatal(err)
	}
	modeChanged, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if modeChanged.SourceFingerprint == contentChanged.SourceFingerprint {
		t.Fatal("resource executable intent did not change source fingerprint")
	}
}

func TestSkillFrontmatterValidation(t *testing.T) {
	tests := map[string]struct {
		frontmatter string
		want        string
	}{
		"malformed YAML":       {"name: echo\ndescription: [", "valid YAML"},
		"unknown field":        {"name: echo\ndescription: Echo.\nfuture: true", `field "future"`},
		"duplicate field":      {"name: echo\nname: echo\ndescription: Echo.", "duplicated"},
		"alias":                {"name: &name echo\ndescription: *name", "aliases"},
		"name type":            {"name: true\ndescription: Echo.", `field "name" must be a string`},
		"description type":     {"name: echo\ndescription: [Echo]", `field "description" must be a string`},
		"license type":         {"name: echo\ndescription: Echo.\nlicense: 1", `field "license" must be a string`},
		"compatibility type":   {"name: echo\ndescription: Echo.\ncompatibility: false", `field "compatibility" must be a string`},
		"metadata type":        {"name: echo\ndescription: Echo.\nmetadata: []", `field "metadata" must map strings to strings`},
		"metadata value type":  {"name: echo\ndescription: Echo.\nmetadata:\n  version: 1", `field "metadata" must map strings to strings`},
		"allowed tools type":   {"name: echo\ndescription: Echo.\nallowed-tools: [Read]", `field "allowed-tools" must be a string`},
		"missing name":         {"description: Echo.", "frontmatter name"},
		"missing description":  {"name: echo", "frontmatter description"},
		"description length":   {"name: echo\ndescription: " + strings.Repeat("a", 1025), "1-1024"},
		"empty compatibility":  {"name: echo\ndescription: Echo.\ncompatibility: ''", "1-500"},
		"compatibility length": {"name: echo\ndescription: Echo.\ncompatibility: " + strings.Repeat("a", 501), "1-500"},
		"null Claude field":    {"name: echo\ndescription: Echo.\nhooks: null", `field "hooks" must not be null`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, "skills", "echo", "SKILL.md"), "---\n"+test.frontmatter+"\n---\n")
			if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid skill was not rejected with %q: %v", test.want, err)
			}
		})
	}
}

func TestSkillRecognizesClaudeExtensionFields(t *testing.T) {
	root := agent(t, "portable")
	write(t, filepath.Join(root, "skills", "echo", "SKILL.md"), `---
name: echo
description: Echo.
when_to_use: Always
argument-hint: "[text]"
arguments: [text]
disable-model-invocation: true
user-invocable: false
disallowed-tools: Write
model: opus
effort: high
context: fork
agent: Explore
background: true
hooks: {}
paths: ["**/*.go"]
shell: bash
---
`)
	p, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	want := "[agent argument-hint arguments background context disable-model-invocation disallowed-tools effort hooks model paths shell user-invocable when_to_use]"
	if got := fmt.Sprint(p.Skills[0].ClaudeFields); got != want {
		t.Fatalf("Claude fields = %s", got)
	}
}

func TestSkillNameValidation(t *testing.T) {
	for _, name := range []string{"-echo", "echo-", "echo--now", "Echo", "café", strings.Repeat("a", 65)} {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			if err := os.RemoveAll(filepath.Join(root, "skills", "echo")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: Echo.\n---\n")
			if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "skill directory") {
				t.Fatalf("nonportable name was not rejected: %v", err)
			}
		})
	}
}

func TestSkillNameAllowsStandardBoundary(t *testing.T) {
	for _, name := range []string{"1-skill", strings.Repeat("a", 64)} {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			if err := os.RemoveAll(filepath.Join(root, "skills", "echo")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(root, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: Valid.\n---\n")
			if _, err := Load(root, "claude"); err != nil {
				t.Fatalf("valid standard name was rejected: %v", err)
			}
		})
	}
}

func TestSkillResourceValidation(t *testing.T) {
	t.Run("invalid UTF-8 resource path", func(t *testing.T) {
		root := agent(t, "portable")
		path := filepath.Join(root, "skills", "echo", "references", string([]byte{0xff}))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("invalid name\n"), 0o644); err != nil {
			t.Skip("filesystem does not allow invalid UTF-8 names")
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "paths must be valid UTF-8") {
			t.Fatalf("invalid UTF-8 resource path was not rejected: %v", err)
		}
	})

	t.Run("missing SKILL.md", func(t *testing.T) {
		root := agent(t, "portable")
		if err := os.Remove(filepath.Join(root, "skills", "echo", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(root, "skills", "echo", "references", "guide.md"), "guide\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "SKILL.md is required") {
			t.Fatalf("missing SKILL.md was not rejected: %v", err)
		}
	})

	t.Run("invalid UTF-8 SKILL.md", func(t *testing.T) {
		root := agent(t, "portable")
		writeBytes(t, filepath.Join(root, "skills", "echo", "SKILL.md"), []byte{0xff}, 0o644)
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
			t.Fatalf("invalid SKILL.md encoding was not rejected: %v", err)
		}
	})

	t.Run("oversized resource", func(t *testing.T) {
		root := agent(t, "portable")
		writeBytes(t, filepath.Join(root, "skills", "echo", "assets", "large.bin"), make([]byte, maxSkillFileBytes+1), 0o644)
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized resource was not rejected: %v", err)
		}
	})

	t.Run("resource count", func(t *testing.T) {
		root := agent(t, "portable")
		for index := 0; index < maxSkillFiles; index++ {
			write(t, filepath.Join(root, "skills", "echo", "assets", fmt.Sprintf("%03d.bin", index)), "")
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("resource count was not bounded: %v", err)
		}
	})

	t.Run("aggregate resource size", func(t *testing.T) {
		root := agent(t, "portable")
		content := make([]byte, maxSkillFileBytes)
		for index := 0; index <= maxSkillBytes/maxSkillFileBytes; index++ {
			writeBytes(t, filepath.Join(root, "skills", "echo", "assets", fmt.Sprintf("%02d.bin", index)), content, 0o644)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "resources exceed") {
			t.Fatalf("aggregate resource size was not bounded: %v", err)
		}
	})
}

func TestLoadRejectsUnsafeOrAmbiguousSources(t *testing.T) {
	t.Run("instructions without frontmatter", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "instructions.md"), "Be concise.\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "frontmatter") {
			t.Fatalf("missing instruction frontmatter was not rejected: %v", err)
		}
	})

	t.Run("instructions without body", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "instructions.md"), "---\ndescription: Empty.\n---\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "body") {
			t.Fatalf("missing instruction body was not rejected: %v", err)
		}
	})

	t.Run("instruction symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "outside.md")
		write(t, outside, "outside\n")
		if err := os.Remove(filepath.Join(root, "instructions.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "instructions.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("source symlink was not rejected: %v", err)
		}
	})

	t.Run("skill name mismatch", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "skills", "echo", "SKILL.md"), "---\nname: other\ndescription: Wrong name.\n---\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "match its parent directory") {
			t.Fatalf("ambiguous skill was not rejected: %v", err)
		}
	})

	t.Run("skill symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "outside.md")
		write(t, outside, "---\nname: linked\ndescription: Outside.\n---\n")
		if err := os.MkdirAll(filepath.Join(root, "skills", "linked"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "skills", "linked", "SKILL.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("skill symlink was not rejected: %v", err)
		}
	})

	t.Run("skill directory symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "linked")
		write(t, filepath.Join(outside, "SKILL.md"), "---\nname: linked\ndescription: Outside.\n---\n")
		if err := os.Symlink(outside, filepath.Join(root, "skills", "linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "real skill directories") {
			t.Fatalf("skill directory symlink was not rejected: %v", err)
		}
	})

	t.Run("nested skill resource symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "outside.bin")
		write(t, outside, "outside\n")
		path := filepath.Join(root, "skills", "echo", "assets", "linked.bin")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("resource symlink was not rejected: %v", err)
		}
	})

	t.Run("flat skill migration", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "skills", "legacy.md"), "---\nname: legacy\ndescription: Legacy.\n---\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), `move it to "skills/legacy/SKILL.md"`) {
			t.Fatalf("flat-layout migration was not explained: %v", err)
		}
	})

	t.Run("skill limit", func(t *testing.T) {
		root := agent(t, "portable")
		for index := 0; index < maxSkills; index++ {
			name := fmt.Sprintf("extra-%d", index)
			write(t, filepath.Join(root, "skills", name, "SKILL.md"), fmt.Sprintf("---\nname: %s\ndescription: Extra.\n---\n", name))
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("skill limit was not enforced: %v", err)
		}
	})

	t.Run("subagent has its own tools", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "subagents", "reviewer", "instructions.md"), instructions("Review."))
		write(t, filepath.Join(root, "subagents", "reviewer", "tools", "bad.py"), "description = 'bad'\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "instructions.md only") {
			t.Fatalf("subagent tools were not rejected: %v", err)
		}
	})

	t.Run("nested subagent", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "subagents", "reviewer", "instructions.md"), instructions("Review."))
		write(t, filepath.Join(root, "subagents", "reviewer", "subagents", "nested", "instructions.md"), instructions("Nest."))
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "instructions.md only") {
			t.Fatalf("nested subagent was not rejected: %v", err)
		}
	})

	t.Run("subagent tool collision", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "subagents", "reviewer", "instructions.md"), instructions("Review."))
		write(t, filepath.Join(root, "tools", "reviewer.py"), "description = 'review'\n")
		write(t, filepath.Join(root, "pyproject.toml"), "[project]\nname = 'portable'\nversion = '0'\n")
		write(t, filepath.Join(root, "uv.lock"), "version = 1\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("tool/subagent collision was not rejected: %v", err)
		}
	})
}

func agent(t *testing.T, directory string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), directory)
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "instructions.md"), instructions("Be concise."))
	write(t, filepath.Join(root, "skills", "echo", "SKILL.md"), "---\nname: echo\ndescription: Repeat safely.\n---\n\nUse echo.\n")
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	writeBytes(t, path, []byte(content), 0o644)
}

func writeBytes(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func instructions(body string) string {
	return "---\ndescription: Test agent.\n---\n\n" + body + "\n"
}
