package project

import (
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
