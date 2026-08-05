package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
	if !strings.Contains(config, `command = "/opt/hctl/bin/hctl"`) || !strings.Contains(config, `[mcp_servers.managed]`) || !strings.Contains(config, `"--workspace"`) || !strings.Contains(config, `"--harness", "codex"`) {
		t.Fatal("Codex MCP configuration does not bind the shared managed server")
	}
	if child := read(t, filepath.Join(root, ".codex", "agents", "docs-reviewer.toml")); !strings.Contains(child, `description = "Review docs."`) || !strings.Contains(child, `developer_instructions = "Review documentation."`) {
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
