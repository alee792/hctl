package project

import (
	"bytes"
	"encoding/json"
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

func TestLoadDiscoversGenericConnectionsForBothHarnesses(t *testing.T) {
	root := agent(t, "portable")
	baseline, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Connections) != 0 {
		t.Fatalf("missing connections = %#v", baseline.Connections)
	}
	path := filepath.Join(root, "connections", "github.md")
	write(t, path, "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nRead public GitHub repositories and issues.\n")
	write(t, filepath.Join(root, "connections", "reference.md"), "---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n")

	for _, harness := range []string{"claude", "codex"} {
		loaded, err := Load(root, harness)
		if err != nil {
			t.Fatalf("%s: %v", harness, err)
		}
		if len(loaded.Connections) != 2 || loaded.Connections[0].Name != "github" || loaded.Connections[0].Context != "Read public GitHub repositories and issues." || loaded.Connections[0].Package != "github-mcp-server" || loaded.Connections[0].Capability != "github" || loaded.Connections[0].Path != "connections/github.md" {
			t.Fatalf("%s installed connection = %#v", harness, loaded.Connections)
		}
		if loaded.Connections[1].Name != "reference" || loaded.Connections[1].Transport != "streamable-http" || loaded.Connections[1].URL != "https://example.com/mcp" || loaded.Connections[1].Context != "" {
			t.Fatalf("%s remote connection = %#v", harness, loaded.Connections[1])
		}
		if loaded.SourceFingerprint == baseline.SourceFingerprint {
			t.Fatalf("%s connection did not join the source fingerprint", harness)
		}
	}

	first, _ := Load(root, "claude")
	write(t, path, "---\ntype: mcp\npackage: github-mcp-server\ncapability: github\n---\n\nRead public GitHub data carefully.\n")
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
		"legacy body only":             {"connections/github.md", []byte("GitHub.\n"), "connection must start with YAML frontmatter declaring \"type: mcp\" and one supported target; body-only connection files are no longer supported"},
		"oversized context":            {"connections/github.md", append([]byte("---\ntype: mcp\npackage: pkg\ncapability: github\n---\n"), bytes.Repeat([]byte("a"), 1025)...), "at most 1024"},
		"non-UTF-8 source":             {"connections/github.md", []byte{0xff}, "valid UTF-8"},
		"unsupported extension":        {"connections/github.json", []byte("{}\n"), "Markdown files only"},
		"unsupported directory":        {"connections/github/connection.md", []byte("GitHub.\n"), "real regular file"},
		"invalid name":                 {"connections/GitHub.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: github\n---\n"), "connection name must match"},
		"reserved name":                {"connections/managed.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: managed\n---\n"), "must not be reserved"},
		"unknown field":                {"connections/github.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: github\nheader: nope\n---\n"), "must contain exactly"},
		"duplicate field":              {"connections/github.md", []byte("---\ntype: mcp\npackage: pkg\npackage: other\ncapability: github\n---\n"), "duplicated"},
		"mixed targets":                {"connections/github.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: github\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n"), "must contain exactly"},
		"non-string":                   {"connections/github.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: 1\n---\n"), "must be a string"},
		"tagged value":                 {"connections/github.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: !tag github\n---\n"), "must be a string"},
		"alias":                        {"connections/github.md", []byte("---\ntype: mcp\npackage: &pkg package\ncapability: *pkg\n---\n"), "aliases are not supported"},
		"multiple YAML documents":      {"connections/github.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: github\n...\ntype: mcp\n---\n"), "must contain one YAML document"},
		"remote query":                 {"connections/public.md", []byte("---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp?q=1\n---\n"), "absolute HTTPS URL"},
		"remote port without hostname": {"connections/public.md", []byte("---\ntype: mcp\ntransport: streamable-http\nurl: https://:443/mcp\n---\n"), "absolute HTTPS URL"},
		"remote wrong transport":       {"connections/public.md", []byte("---\ntype: mcp\ntransport: sse\nurl: https://example.com/mcp\n---\n"), "must contain exactly"},
		"installed invalid package":    {"connections/github.md", []byte("---\ntype: mcp\npackage: GitHub\ncapability: github\n---\n"), "package\" is invalid"},
		"installed invalid capability": {"connections/github.md", []byte("---\ntype: mcp\npackage: pkg\ncapability: git_hub\n---\n"), "capability\" is invalid"},
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
		write(t, outside, "---\ntype: mcp\npackage: pkg\ncapability: github\n---\n")
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
		write(t, filepath.Join(outside, "github.md"), "---\ntype: mcp\npackage: pkg\ncapability: github\n---\n")
		if err := os.Symlink(outside, filepath.Join(root, "connections")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("connections directory symlink was not rejected: %v", err)
		}
	})

	t.Run("inventory bound", func(t *testing.T) {
		root := agent(t, "portable")
		for index := 0; index < maxConnections+1; index++ {
			name := fmt.Sprintf("server%03d", index)
			write(t, filepath.Join(root, "connections", name+".md"), "---\ntype: mcp\ntransport: streamable-http\nurl: https://example.com/mcp\n---\n")
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "at most 128 entries") {
			t.Fatalf("oversized inventory was not rejected: %v", err)
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
	if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "description and optional friction-notes only") {
		t.Fatalf("root effort was not rejected: %v", err)
	}
}

func TestLoadParsesOptionalFrictionNotes(t *testing.T) {
	root := agent(t, "portable")
	baseline, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if baseline.FrictionNotes {
		t.Fatal("friction notes enabled by default")
	}

	write(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\nfriction-notes: false\n---\n\nBe concise.\n")
	disabled, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if disabled.FrictionNotes {
		t.Fatal("explicitly disabled friction notes were enabled")
	}

	write(t, filepath.Join(root, "instructions.md"), "---\nfriction-notes: true\ndescription: Test agent.\n---\n\nBe concise.\n")
	enabled, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.FrictionNotes {
		t.Fatal("friction notes opt-in was ignored")
	}
	if enabled.SourceFingerprint == baseline.SourceFingerprint || enabled.SourceFingerprint == disabled.SourceFingerprint {
		t.Fatal("friction-notes source change did not change the fingerprint")
	}
}

func TestLoadReservesFrictionToolNameForSubagents(t *testing.T) {
	root := agent(t, "portable")
	write(t, filepath.Join(root, "subagents", "record-friction", "instructions.md"), instructions("Record friction."))
	if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "conflicts with a tool") {
		t.Fatalf("reserved friction subagent was accepted: %v", err)
	}
}

func TestLoadRejectsInvalidFrictionNotesFrontmatter(t *testing.T) {
	for name, frontmatter := range map[string]string{
		"non-boolean": "description: Test agent.\nfriction-notes: yes",
		"duplicate":   "description: Test agent.\nfriction-notes: true\nfriction-notes: false",
	} {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, "instructions.md"), "---\n"+frontmatter+"\n---\n\nBe concise.\n")
			if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "friction-notes") {
				t.Fatalf("invalid friction-notes was accepted: %v", err)
			}
		})
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
		writeBytes(t, filepath.Join(root, "harnesses", "claude", ".claude", "large.bin"), make([]byte, maxHarnessFileBytes+1), 0o644)
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

func TestLoadImportsVendoredPluginSkillsDeterministically(t *testing.T) {
	root := agent(t, "portable")
	write(t, filepath.Join(root, "plugins", "zeta", "plugin.json"), pluginManifest("zeta"))
	write(t, filepath.Join(root, "plugins", "zeta", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review carefully.\n---\n\nReview.\n")
	write(t, filepath.Join(root, "plugins", "zeta", "skills", "review", "references", "guide.md"), "guide one\n")
	write(t, filepath.Join(root, "plugins", "alpha", "plugin.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "alpha",
  "extensions": {"com.example.alpha": {"mode": "safe"}},
  "future": true
}`)
	write(t, filepath.Join(root, "plugins", "alpha", "skills", "echo", "SKILL.md"), "---\nname: echo\ndescription: Must lose to the root.\n---\n")
	write(t, filepath.Join(root, "plugins", "alpha", "skills", "analyze", "SKILL.md"), "---\nname: analyze\ndescription: Analyze evidence.\n---\n")
	write(t, filepath.Join(root, "plugins", "broken", "plugin.json"), `{"$schema":"wrong","name":"broken"}`)
	write(t, filepath.Join(root, "plugins", "broken", "skills", "ignored", "SKILL.md"), "---\nname: ignored\ndescription: Ignore.\n---\n")
	write(t, filepath.Join(root, "plugins", "invalid-skill", "plugin.json"), pluginManifest("invalid-skill"))
	write(t, filepath.Join(root, "plugins", "invalid-skill", "skills", "bad", "SKILL.md"), "not frontmatter\n")

	first, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceFingerprint != second.SourceFingerprint {
		t.Fatal("same plugin source produced different fingerprints")
	}
	got := []string{}
	for _, skill := range first.Skills {
		got = append(got, skill.Name+"@"+skill.SourcePath)
	}
	want := "[echo@skills/echo analyze@plugins/alpha/skills/analyze review@plugins/zeta/skills/review]"
	if fmt.Sprint(got) != want {
		t.Fatalf("plugin skill order = %v, want %s", got, want)
	}
	if len(first.Diagnostics) != 5 {
		t.Fatalf("plugin diagnostics = %#v", first.Diagnostics)
	}
	diagnosticText := fmt.Sprint(first.Diagnostics)
	for _, fragment := range []string{"extensions.com.example.alpha", "future", "already provided", "plugin rejected", "plugin skill skipped"} {
		if !strings.Contains(diagnosticText, fragment) {
			t.Fatalf("plugin diagnostics omitted %q: %#v", fragment, first.Diagnostics)
		}
	}

	write(t, filepath.Join(root, "plugins", "zeta", "skills", "review", "references", "guide.md"), "guide two\n")
	changed, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if changed.SourceFingerprint == first.SourceFingerprint {
		t.Fatal("plugin skill resource did not change the fingerprint")
	}
	write(t, filepath.Join(root, "plugins", "zeta", "plugin.json"), strings.Replace(pluginManifest("zeta"), "\n}", ",\n  \"description\": \"Changed\"\n}", 1))
	manifestChanged, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if manifestChanged.SourceFingerprint == changed.SourceFingerprint {
		t.Fatal("plugin manifest did not change the fingerprint")
	}
	if err := os.Chmod(filepath.Join(root, "plugins", "zeta", "skills", "review", "references", "guide.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	modeChanged, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if modeChanged.SourceFingerprint == manifestChanged.SourceFingerprint {
		t.Fatal("plugin skill executable intent did not change the fingerprint")
	}
}

func TestPluginManifestValidationAndIsolation(t *testing.T) {
	tests := map[string]struct {
		manifest string
		want     string
	}{
		"missing schema":       {`{"name":"example"}`, "must equal"},
		"wrong schema":         {`{"$schema":"wrong","name":"example"}`, "must equal"},
		"invalid name":         {`{"$schema":"` + pluginSchema + `","name":"Bad_Name"}`, "name format"},
		"wrong metadata type":  {`{"$schema":"` + pluginSchema + `","name":"example","version":1}`, "must be a string"},
		"unknown author field": {`{"$schema":"` + pluginSchema + `","name":"example","author":{"team":"x"}}`, "unsupported field"},
		"trailing JSON":        {`{"$schema":"` + pluginSchema + `","name":"example"} {}`, "one JSON value"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, "plugins", "example", "plugin.json"), test.manifest)
			write(t, filepath.Join(root, "plugins", "example", "skills", "from-plugin", "SKILL.md"), "---\nname: from-plugin\ndescription: Import me.\n---\n")
			loaded, err := Load(root, "codex")
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Skills) != 1 || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, test.want) {
				t.Fatalf("invalid plugin was not isolated with %q: skills=%#v diagnostics=%#v", test.want, loaded.Skills, loaded.Diagnostics)
			}
		})
	}

	t.Run("unknown field is reported with rejection", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "example", "plugin.json"), `{"$schema":"wrong","name":"example","future":true}`)
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Diagnostics) != 2 || loaded.Diagnostics[0].Field != "future" || !strings.Contains(loaded.Diagnostics[1].Message, "plugin rejected") {
			t.Fatalf("unknown field and rejection diagnostics = %#v", loaded.Diagnostics)
		}
	})

	for name, extensions := range map[string]string{
		"non-object extensions":       `42`,
		"unvalidated namespace value": `{"com.example":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, "plugins", "example", "plugin.json"), `{"$schema":"`+pluginSchema+`","name":"example","extensions":`+extensions+`}`)
			write(t, filepath.Join(root, "plugins", "example", "skills", "from-plugin", "SKILL.md"), "---\nname: from-plugin\ndescription: Import me.\n---\n")
			loaded, err := Load(root, "codex")
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Skills) != 2 || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, "ignored") {
				t.Fatalf("extensions were not ignored: skills=%#v diagnostics=%#v", loaded.Skills, loaded.Diagnostics)
			}
		})
	}
}

func TestLoadPluginMCPServersDeterministically(t *testing.T) {
	root := agent(t, "portable")
	write(t, filepath.Join(root, "plugins", "alpha", "plugin.json"), pluginManifest("alpha"))
	writeBytes(t, filepath.Join(root, "plugins", "alpha", "bin", "server"), []byte("one\n"), 0o755)
	write(t, filepath.Join(root, "plugins", "alpha", "work", ".keep"), "keep\n")
	write(t, filepath.Join(root, "plugins", "alpha", "mcp.json"), `{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "zeta": {"type":"streamable-http","url":"https://example.com/mcp","headers":{"X-Package":"visible"}},
    "local": {"type":"stdio","command":"./bin/server","args":["--root=${PLUGIN_ROOT}","${PLUGIN_DATA}"],"env":{"CACHE":"${PLUGIN_DATA}/cache"},"cwd":"./work"},
    "legacy": {"type":"sse","url":"https://example.com/events"},
    "bad": {"type":"stdio","command":"node","future":true}
  }
}`)
	write(t, filepath.Join(root, "plugins", "zeta", "plugin.json"), pluginManifest("zeta"))
	write(t, filepath.Join(root, "plugins", "zeta", "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"local":{"type":"stdio","command":"node"},"managed":{"type":"stdio","command":"node"}}}`)

	loaded, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.PluginMCPServers) != 2 || loaded.PluginMCPServers[0].Name != "local" || loaded.PluginMCPServers[1].Name != "zeta" {
		t.Fatalf("plugin MCP order = %#v", loaded.PluginMCPServers)
	}
	local := loaded.PluginMCPServers[0]
	if local.Command != "./bin/server" || local.CWD != "./work" || local.Env["CACHE"] != "${PLUGIN_DATA}/cache" || local.DataPath == "" {
		t.Fatalf("stdio server = %#v", local)
	}
	if got := fmt.Sprint(loaded.Diagnostics); !strings.Contains(got, "unsupported field") || !strings.Contains(got, "SSE transport") || !strings.Contains(got, "earlier source") {
		t.Fatalf("MCP diagnostics = %#v", loaded.Diagnostics)
	}
}

func TestPluginMCPValidationIsIsolatedAndFingerprintTracksAcceptedInputs(t *testing.T) {
	root := agent(t, "portable")
	pluginRoot := filepath.Join(root, "plugins", "example")
	write(t, filepath.Join(pluginRoot, "plugin.json"), pluginManifest("example"))
	write(t, filepath.Join(pluginRoot, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review.\n---\n")
	mcpPath := filepath.Join(pluginRoot, "mcp.json")
	write(t, mcpPath, `{"$schema":"wrong","mcpServers":{}}`)
	invalid, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid.Skills) != 2 || len(invalid.PluginMCPServers) != 0 || len(invalid.Diagnostics) != 1 {
		t.Fatalf("invalid MCP component was not isolated: %#v", invalid)
	}
	write(t, mcpPath, `{"$schema":"wrong-again","mcpServers":{}}`)
	changedInvalid, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if changedInvalid.SourceFingerprint != invalid.SourceFingerprint {
		t.Fatal("rejected MCP input changed the source fingerprint")
	}
	write(t, mcpPath, `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{}}`)
	empty, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if empty.SourceFingerprint == invalid.SourceFingerprint || len(empty.PluginMCPServers) != 0 {
		t.Fatal("accepted empty MCP component did not change the source fingerprint")
	}
	write(t, mcpPath, `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"remote":{"type":"streamable-http","url":"https://example.com/one"}}}`)
	accepted, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.SourceFingerprint == invalid.SourceFingerprint || len(accepted.PluginMCPServers) != 1 {
		t.Fatal("accepted MCP input did not change the source fingerprint")
	}
	write(t, mcpPath, `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"remote":{"type":"streamable-http","url":"https://example.com/two"}}}`)
	changedAccepted, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if changedAccepted.SourceFingerprint == accepted.SourceFingerprint {
		t.Fatal("accepted MCP value did not change the source fingerprint")
	}
}

func TestPluginMCPHarnessSafetyAndDataIdentity(t *testing.T) {
	root := agent(t, "portable")
	pluginRoot := filepath.Join(root, "plugins", "example")
	write(t, filepath.Join(pluginRoot, "plugin.json"), pluginManifest("example"))
	write(t, filepath.Join(pluginRoot, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"command-secret":{"type":"stdio","command":"${AWS_SECRET_ACCESS_KEY}"},"literal":{"type":"stdio","command":"node","args":["${OTHER}"]},"secret-header":{"type":"streamable-http","url":"https://example.com/mcp","headers":{"Authorization":"${AWS_SECRET_ACCESS_KEY}"}},"upper":{"type":"streamable-http","url":"HTTPS://example.com/mcp"}}}`)

	claude, err := Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(claude.PluginMCPServers) != 1 || claude.PluginMCPServers[0].Name != "upper" || !strings.Contains(fmt.Sprint(claude.Diagnostics), "Claude project configuration would expand") {
		t.Fatalf("Claude expansion safety = servers %#v diagnostics %#v", claude.PluginMCPServers, claude.Diagnostics)
	}
	codex, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(codex.PluginMCPServers) != 4 || codex.PluginMCPServers[0].Command != "${AWS_SECRET_ACCESS_KEY}" || codex.PluginMCPServers[1].Args[0] != "${OTHER}" || codex.PluginMCPServers[2].Headers["Authorization"] != "${AWS_SECRET_ACCESS_KEY}" {
		t.Fatalf("Codex literal placeholder support = %#v", codex.PluginMCPServers)
	}

	other := agent(t, "portable")
	write(t, filepath.Join(other, "plugins", "example", "plugin.json"), pluginManifest("example"))
	write(t, filepath.Join(other, "plugins", "example", "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"literal":{"type":"stdio","command":"node"}}}`)
	otherLoaded, err := Load(other, "codex", codex.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if otherLoaded.PluginMCPServers[0].DataPath == codex.PluginMCPServers[0].DataPath {
		t.Fatal("different agent sources shared plugin data identity")
	}
}

func TestRelocatedPluginKeepsSelectedDataIdentity(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	relocated := filepath.Join(base, "relocated")
	workspace := filepath.Join(base, "workspace")
	for _, root := range []string{source, relocated} {
		write(t, filepath.Join(root, "instructions.md"), instructions("Portable."))
		write(t, filepath.Join(root, "plugins", "example", "plugin.json"), pluginManifest("example"))
		write(t, filepath.Join(root, "plugins", "example", "mcp.json"), `{"$schema":"`+pluginMCPSchema+`","mcpServers":{"local":{"type":"stdio","command":"node"}}}`)
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	selected, err := Load(source, "codex", workspace)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRelocated(relocated, "codex", workspace, selected)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AgentID != selected.AgentID || loaded.PluginMCPServers[0].DataPath != selected.PluginMCPServers[0].DataPath {
		t.Fatalf("relocation changed plugin data identity: selected=%#v relocated=%#v", selected.PluginMCPServers, loaded.PluginMCPServers)
	}
}

func TestPluginMCPRejectsUnsafeValuesPerServer(t *testing.T) {
	tests := map[string]struct {
		server string
		want   string
	}{
		"remote cleartext":        {`{"type":"streamable-http","url":"http://example.com/mcp"}`, "must use HTTPS"},
		"remote user info":        {`{"type":"streamable-http","url":"https://user@example.com/mcp"}`, "without user information"},
		"remote fragment":         {`{"type":"streamable-http","url":"https://example.com/mcp#fragment"}`, "without user information"},
		"invalid header value":    {`{"type":"streamable-http","url":"https://example.com/mcp","headers":{"X-Test":"one\r\ntwo"}}`, "invalid HTTP header"},
		"duplicate headers":       {`{"type":"streamable-http","url":"https://example.com/mcp","headers":{"X-Test":"one","x-test":"two"}}`, "duplicate name"},
		"exact duplicate headers": {`{"type":"streamable-http","url":"https://example.com/mcp","headers":{"X-Test":"one","X-Test":"two"}}`, "duplicate name"},
		"reserved env":            {`{"type":"stdio","command":"node","env":{"PLUGIN_ROOT":"bad"}}`, "must not configure PLUGIN_ROOT"},
		"reserved data env":       {`{"type":"stdio","command":"node","env":{"PLUGIN_DATA":"bad"}}`, "must not configure PLUGIN_DATA"},
		"escaping cwd":            {`{"type":"stdio","command":"node","cwd":"${PLUGIN_DATA}/../escape"}`, "normalized"},
		"command shell text":      {`{"type":"stdio","command":"node --flag"}`, "bare executable name"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, "plugins", "example", "plugin.json"), pluginManifest("example"))
			write(t, filepath.Join(root, "plugins", "example", "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"bad":`+test.server+`}}`)
			loaded, err := Load(root, "claude")
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.PluginMCPServers) != 0 || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, test.want) {
				t.Fatalf("unsafe server was not isolated with %q: %#v", test.want, loaded.Diagnostics)
			}
		})
	}
}

func TestPluginMCPComponentValidationAndBounds(t *testing.T) {
	for name, content := range map[string]string{
		"malformed":          `{`,
		"wrong kind":         `[]`,
		"extra field":        `{"$schema":"` + pluginMCPSchema + `","mcpServers":{},"future":true}`,
		"wrong servers kind": `{"$schema":"` + pluginMCPSchema + `","mcpServers":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := agent(t, "portable")
			write(t, filepath.Join(root, "plugins", "example", "plugin.json"), pluginManifest("example"))
			write(t, filepath.Join(root, "plugins", "example", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review.\n---\n")
			write(t, filepath.Join(root, "plugins", "example", "mcp.json"), content)
			loaded, err := Load(root, "codex")
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Skills) != 2 || len(loaded.PluginMCPServers) != 0 || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, "component skipped") {
				t.Fatalf("invalid component was not isolated: %#v", loaded)
			}
		})
	}

	t.Run("server bound", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "example", "plugin.json"), pluginManifest("example"))
		servers := map[string]any{}
		for index := 0; index < maxPluginMCPServers; index++ {
			servers[fmt.Sprintf("server-%02d", index)] = map[string]any{"type": "stdio", "command": "node"}
		}
		writePluginMCP := func() {
			content, err := json.Marshal(map[string]any{"$schema": pluginMCPSchema, "mcpServers": servers})
			if err != nil {
				t.Fatal(err)
			}
			writeBytes(t, filepath.Join(root, "plugins", "example", "mcp.json"), content, 0o644)
		}
		writePluginMCP()
		loaded, err := Load(root, "codex")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.PluginMCPServers) != maxPluginMCPServers {
			t.Fatalf("maximum MCP server count loaded %d servers", len(loaded.PluginMCPServers))
		}
		servers["overflow"] = map[string]any{"type": "stdio", "command": "node"}
		writePluginMCP()
		loaded, err = Load(root, "codex")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.PluginMCPServers) != 0 || !strings.Contains(fmt.Sprint(loaded.Diagnostics), "at most") {
			t.Fatalf("MCP server bound was not isolated: %#v", loaded.Diagnostics)
		}
	})

	t.Run("loopback HTTP accepted", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "example", "plugin.json"), pluginManifest("example"))
		write(t, filepath.Join(root, "plugins", "example", "mcp.json"), `{"$schema":"`+pluginMCPSchema+`","mcpServers":{"local":{"type":"streamable-http","url":"HTTP://127.0.0.1:8080/mcp"}}}`)
		loaded, err := Load(root, "codex")
		if err != nil || len(loaded.PluginMCPServers) != 1 {
			t.Fatalf("loopback HTTP was rejected: servers=%#v err=%v", loaded.PluginMCPServers, err)
		}
	})
}

func TestPluginMCPCommandContentAndModeChangeFingerprint(t *testing.T) {
	root := agent(t, "portable")
	pluginRoot := filepath.Join(root, "plugins", "example")
	write(t, filepath.Join(pluginRoot, "plugin.json"), pluginManifest("example"))
	command := filepath.Join(pluginRoot, "server")
	writeBytes(t, command, []byte("one\n"), 0o644)
	write(t, filepath.Join(pluginRoot, "mcp.json"), `{"$schema":"`+pluginMCPSchema+`","mcpServers":{"local":{"type":"stdio","command":"./server"}}}`)
	first, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	writeBytes(t, command, []byte("two\n"), 0o644)
	second, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if second.SourceFingerprint == first.SourceFingerprint {
		t.Fatal("plugin command content did not change fingerprint")
	}
	if err := os.Chmod(command, 0o755); err != nil {
		t.Fatal(err)
	}
	third, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if third.SourceFingerprint == second.SourceFingerprint {
		t.Fatal("plugin command executable intent did not change fingerprint")
	}
}

func TestPluginMCPFileSymlinkIsIsolated(t *testing.T) {
	root := agent(t, "portable")
	pluginRoot := filepath.Join(root, "plugins", "example")
	write(t, filepath.Join(pluginRoot, "plugin.json"), pluginManifest("example"))
	outside := filepath.Join(t.TempDir(), "mcp.json")
	write(t, outside, `{"$schema":"`+pluginMCPSchema+`","mcpServers":{}}`)
	if err := os.Symlink(outside, filepath.Join(pluginRoot, "mcp.json")); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.PluginMCPServers) != 0 || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, "without symlinks") {
		t.Fatalf("symlinked MCP component was not isolated: %#v", loaded.Diagnostics)
	}
}

func TestPluginMCPRejectsSymlinkedCommandAndCWD(t *testing.T) {
	for _, target := range []string{"command", "cwd"} {
		t.Run(target, func(t *testing.T) {
			root := agent(t, "portable")
			pluginRoot := filepath.Join(root, "plugins", "example")
			write(t, filepath.Join(pluginRoot, "plugin.json"), pluginManifest("example"))
			outside := t.TempDir()
			server := `{"type":"stdio","command":"node","cwd":"./linked"}`
			if target == "command" {
				write(t, filepath.Join(outside, "server"), "outside\n")
				if err := os.Symlink(filepath.Join(outside, "server"), filepath.Join(pluginRoot, "server")); err != nil {
					t.Fatal(err)
				}
				server = `{"type":"stdio","command":"./server"}`
			} else if err := os.Symlink(outside, filepath.Join(pluginRoot, "linked")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(pluginRoot, "mcp.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/mcp.schema.json","mcpServers":{"bad":`+server+`}}`)
			loaded, err := Load(root, "claude")
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.PluginMCPServers) != 0 || !strings.Contains(fmt.Sprint(loaded.Diagnostics), "symlink") {
				t.Fatalf("symlinked %s was not isolated: %#v", target, loaded.Diagnostics)
			}
		})
	}
}

func TestPluginFilesystemBoundaries(t *testing.T) {
	t.Run("missing skills is normal", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "metadata-only", "plugin.json"), pluginManifest("metadata-only"))
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Skills) != 1 || len(loaded.Diagnostics) != 0 {
			t.Fatalf("metadata-only plugin = skills %#v diagnostics %#v", loaded.Skills, loaded.Diagnostics)
		}
	})

	t.Run("plugins directory symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "plugins")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("plugins symlink was not rejected: %v", err)
		}
	})

	t.Run("plugin directory symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := t.TempDir()
		write(t, filepath.Join(outside, "plugin.json"), pluginManifest("linked"))
		if err := os.MkdirAll(filepath.Join(root, "plugins"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "plugins", "linked")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "real plugin directories") {
			t.Fatalf("plugin directory symlink was not rejected: %v", err)
		}
	})

	t.Run("plugin skill resource symlink", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "example", "plugin.json"), pluginManifest("example"))
		write(t, filepath.Join(root, "plugins", "example", "skills", "safe", "SKILL.md"), "---\nname: safe\ndescription: Safe.\n---\n")
		outside := filepath.Join(t.TempDir(), "secret")
		write(t, outside, "secret\n")
		if err := os.Symlink(outside, filepath.Join(root, "plugins", "example", "skills", "safe", "secret")); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Skills) != 1 || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, "symlinks") {
			t.Fatalf("plugin skill symlink was not isolated: skills=%#v diagnostics=%#v", loaded.Skills, loaded.Diagnostics)
		}
	})

	t.Run("aggregate skill limit", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "many", "plugin.json"), pluginManifest("many"))
		for index := 0; index < maxSkills; index++ {
			name := fmt.Sprintf("plugin-%d", index)
			write(t, filepath.Join(root, "plugins", "many", "skills", name, "SKILL.md"), fmt.Sprintf("---\nname: %s\ndescription: Extra.\n---\n", name))
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Skills) != maxSkills || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, fmt.Sprintf("at most %d", maxSkills)) {
			t.Fatalf("aggregate plugin skill limit was not isolated: skills=%d diagnostics=%#v", len(loaded.Skills), loaded.Diagnostics)
		}
	})

	t.Run("aggregate plugin skill byte budget", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "bounded", "plugin.json"), pluginManifest("bounded"))
		write(t, filepath.Join(root, "plugins", "bounded", "skills", "large", "SKILL.md"), "---\nname: large\ndescription: Extra.\n---\n")
		budget := &skillSetBudget{maxFiles: maxSkillSetFiles, maxBytes: 1}
		loaded, _, _, diagnostics, err := loadPlugins(root, root, "claude", "portable", nil, budget)
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded) != 0 || len(diagnostics) != 1 || diagnostics[0].Path != "plugins/bounded/skills/large" || !strings.Contains(diagnostics[0].Message, "aggregate") {
			t.Fatalf("aggregate plugin skill byte limit was not isolated at its path: skills=%#v diagnostics=%#v", loaded, diagnostics)
		}
	})

	t.Run("plugin directory entry limit", func(t *testing.T) {
		root := agent(t, "portable")
		for index := 0; index < maxPlugins; index++ {
			write(t, filepath.Join(root, "plugins", fmt.Sprintf("plugin-%02d", index), "plugin.json"), pluginManifest(fmt.Sprintf("plugin-%02d", index)))
		}
		if _, err := Load(root, "claude"); err != nil {
			t.Fatalf("maximum plugin directory count was rejected: %v", err)
		}
		write(t, filepath.Join(root, "plugins", "overflow", "plugin.json"), pluginManifest("overflow"))
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d", maxPlugins)) {
			t.Fatalf("plugin directory entry limit was not enforced: %v", err)
		}
	})

	t.Run("plugin skill entry limit", func(t *testing.T) {
		root := agent(t, "portable")
		write(t, filepath.Join(root, "plugins", "many", "plugin.json"), pluginManifest("many"))
		for index := 0; index < maxPluginSkills; index++ {
			if err := os.MkdirAll(filepath.Join(root, "plugins", "many", "skills", fmt.Sprintf("entry-%03d", index)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Load(root, "claude"); err != nil {
			t.Fatalf("maximum plugin skill directory count was rejected: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(root, "plugins", "many", "skills", "overflow"), 0o755); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Skills) != 1 || len(loaded.Diagnostics) != 1 || !strings.Contains(loaded.Diagnostics[0].Message, fmt.Sprintf("at most %d", maxPluginSkills)) {
			t.Fatalf("plugin skill entry limit was not isolated: skills=%#v diagnostics=%#v", loaded.Skills, loaded.Diagnostics)
		}
	})
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
		for index := 0; index < maxSkillFiles-1; index++ {
			write(t, filepath.Join(root, "skills", "echo", "assets", fmt.Sprintf("%03d.bin", index)), "")
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Skills[0].Files) != maxSkillFiles {
			t.Fatalf("maximum resource count loaded %d files", len(loaded.Skills[0].Files))
		}
		write(t, filepath.Join(root, "skills", "echo", "assets", "overflow.bin"), "")
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
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "per-skill aggregate") {
			t.Fatalf("aggregate resource size was not bounded: %v", err)
		}
	})
}

func TestAggregateSkillBudget(t *testing.T) {
	budget := &skillSetBudget{maxFiles: 2, maxBytes: 3}
	if err := budget.claim("skills/one/SKILL.md", 2); err != nil {
		t.Fatal(err)
	}
	if err := budget.claim("skills/two/SKILL.md", 1); err != nil {
		t.Fatal(err)
	}
	if err := budget.claim("skills/overflow/SKILL.md", 0); err == nil || !strings.Contains(err.Error(), "skills/overflow/SKILL.md exceeds the aggregate 2-file") {
		t.Fatalf("aggregate file budget was not enforced: %v", err)
	}
	bytesBudget := &skillSetBudget{maxFiles: 2, maxBytes: 2}
	if err := bytesBudget.claim("skills/large/SKILL.md", 3); err == nil || !strings.Contains(err.Error(), "skills/large/SKILL.md exceeds the aggregate 2-byte") {
		t.Fatalf("aggregate byte budget was not enforced: %v", err)
	}

	realFileBudget := &skillSetBudget{files: maxSkillSetFiles - 1, maxFiles: maxSkillSetFiles, maxBytes: maxSkillSetBytes}
	if err := realFileBudget.claim("skills/final/SKILL.md", 0); err != nil {
		t.Fatalf("real aggregate file maximum was rejected: %v", err)
	}
	if err := realFileBudget.claim("plugins/overflow/skills/next/SKILL.md", 0); err == nil || !strings.Contains(err.Error(), "plugins/overflow/skills/next/SKILL.md") {
		t.Fatalf("file above real aggregate maximum was not rejected at its path: %v", err)
	}

	realByteBudget := &skillSetBudget{bytes: maxSkillSetBytes - 1, maxFiles: maxSkillSetFiles, maxBytes: maxSkillSetBytes}
	if err := realByteBudget.claim("skills/final/resource.bin", 1); err != nil {
		t.Fatalf("real aggregate byte maximum was rejected: %v", err)
	}
	if err := realByteBudget.claim("plugins/overflow/skills/next/resource.bin", 1); err == nil || !strings.Contains(err.Error(), "plugins/overflow/skills/next/resource.bin") {
		t.Fatalf("byte above real aggregate maximum was not rejected at its path: %v", err)
	}
}

func TestAuthoredByteBudgetBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		max  int64
	}{
		{name: "subagent-source", max: maxSubagentBytes},
		{name: "schedule-source", max: maxScheduleBytes},
		{name: "harness-specific source", max: maxHarnessBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			budget := &byteBudget{used: test.max - 1, max: test.max, label: test.name}
			if err := budget.claim("authored/final", 1); err != nil {
				t.Fatalf("maximum was rejected: %v", err)
			}
			if err := budget.claim("authored/overflow", 1); err == nil || !strings.Contains(err.Error(), "authored/overflow") {
				t.Fatalf("value above maximum was not rejected at its path: %v", err)
			}
		})
	}
}

func TestRaisedAuthoredCountBoundaries(t *testing.T) {
	t.Run("subagents", func(t *testing.T) {
		root := agent(t, "portable")
		for index := 0; index < maxSubagents; index++ {
			write(t, filepath.Join(root, "subagents", fmt.Sprintf("agent-%03d", index), "instructions.md"), instructions("Review."))
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Subagents) != maxSubagents {
			t.Fatalf("maximum subagent count loaded %d subagents", len(loaded.Subagents))
		}
		write(t, filepath.Join(root, "subagents", "overflow", "instructions.md"), instructions("Review."))
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "subagents/overflow") || !strings.Contains(err.Error(), fmt.Sprintf("at most %d", maxSubagents)) {
			t.Fatalf("subagent limit was not enforced: %v", err)
		}
	})

	t.Run("schedules", func(t *testing.T) {
		root := agent(t, "portable")
		content := "---\ncron: \"0 0 * * *\"\n---\n\nRun.\n"
		for index := 0; index < maxSchedules; index++ {
			write(t, filepath.Join(root, "schedules", fmt.Sprintf("task-%03d.md", index)), content)
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Schedules) != maxSchedules {
			t.Fatalf("maximum schedule count loaded %d schedules", len(loaded.Schedules))
		}
		write(t, filepath.Join(root, "schedules", "overflow.md"), content)
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "schedules/") || !strings.Contains(err.Error(), ".md") || !strings.Contains(err.Error(), fmt.Sprintf("at most %d", maxSchedules)) {
			t.Fatalf("schedule limit was not enforced: %v", err)
		}
	})

	t.Run("harness files", func(t *testing.T) {
		root := agent(t, "portable")
		for index := 0; index < maxHarnessFiles; index++ {
			write(t, filepath.Join(root, "harnesses", "claude", ".claude", "rules", fmt.Sprintf("rule-%04d.md", index)), "rule\n")
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.HarnessFiles) != maxHarnessFiles {
			t.Fatalf("maximum harness file count loaded %d files", len(loaded.HarnessFiles))
		}
		write(t, filepath.Join(root, "harnesses", "claude", ".claude", "rules", "overflow.md"), "rule\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d", maxHarnessFiles)) {
			t.Fatalf("harness file limit was not enforced: %v", err)
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
		for index := 0; index < maxSkills-1; index++ {
			name := fmt.Sprintf("extra-%d", index)
			write(t, filepath.Join(root, "skills", name, "SKILL.md"), fmt.Sprintf("---\nname: %s\ndescription: Extra.\n---\n", name))
		}
		loaded, err := Load(root, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if len(loaded.Skills) != maxSkills {
			t.Fatalf("maximum skill count loaded %d skills", len(loaded.Skills))
		}
		write(t, filepath.Join(root, "skills", "overflow", "SKILL.md"), "---\nname: overflow\ndescription: Extra.\n---\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "skills/overflow") || !strings.Contains(err.Error(), "at most") {
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

func pluginManifest(name string) string {
	return "{\n  \"$schema\": \"" + pluginSchema + "\",\n  \"name\": \"" + name + "\"\n}\n"
}
