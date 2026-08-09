package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hctl/internal/acquisition"
)

func TestAcquiredSkillLoadsOfflineDetectsDriftAndRemovesExplicitly(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := agent(t, "portable")
	write(t, filepath.Join(root, "skills", "manual", "SKILL.md"), "---\nname: manual\ndescription: Manually authored.\n---\n")
	externalLock := filepath.Join(root, "skills-lock.json")
	write(t, externalLock, "external and opaque\n")
	sourceParent := t.TempDir()
	source := filepath.Join(sourceParent, "review")
	write(t, filepath.Join(source, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n\nReview.\n")
	write(t, filepath.Join(source, "references", "guide.bin"), string([]byte{0, 1, 2, 255}))
	if err := os.Mkdir(filepath.Join(source, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := AcquisitionManager(root)
	dependency, err := manager.Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: source})
	if err != nil {
		t.Fatal(err)
	}
	if dependency.Name != "review" || dependency.Destination != "skills/review" || dependency.FileCount != 2 {
		t.Fatalf("unexpected dependency: %#v", dependency)
	}
	if err := os.RemoveAll(sourceParent); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root, "codex")
	if err != nil {
		t.Fatalf("vendored dependency did not load without its source: %v", err)
	}
	if len(loaded.SourceDirectories) == 0 || len(loaded.SourceContents["skills/review/references/guide.bin"]) != 4 || len(loaded.SourceContents[acquisition.LockFilename]) == 0 {
		t.Fatalf("project did not capture the complete acquired snapshot: %#v", loaded.SourceDirectories)
	}
	if len(loaded.Skills) != 3 || loaded.Skills[0].Name != "echo" || loaded.Skills[1].Name != "manual" || loaded.Skills[2].Name != "review" {
		t.Fatalf("manual and acquired Skills did not coexist: %#v", loaded.Skills)
	}
	statuses, err := manager.Status(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]acquisition.State{}
	for _, status := range statuses {
		states[string(status.Kind)+"/"+status.Name] = status.State
	}
	if states["skill/echo"] != acquisition.StateUntracked || states["skill/manual"] != acquisition.StateUntracked || states["skill/review"] != acquisition.StateClean {
		t.Fatalf("status did not distinguish manual and acquired Skills: %#v", statuses)
	}
	before := loaded.SourceFingerprint
	write(t, filepath.Join(root, "skills", "review", "new.txt"), "drift\n")
	if _, err := Load(root, "codex"); err == nil || !strings.Contains(err.Error(), "drifted") {
		t.Fatalf("offline load did not reject drift: %v", err)
	}
	kind := acquisition.Skill
	statuses, err = manager.Status(context.Background(), &kind, "review")
	if err != nil || len(statuses) != 1 || statuses[0].State != acquisition.StateDrifted {
		t.Fatalf("status did not report drift: %#v %v", statuses, err)
	}
	if err := manager.Remove(context.Background(), acquisition.Skill, "review", false); err == nil {
		t.Fatal("ordinary removal accepted a drifted dependency")
	}
	if err := os.RemoveAll(filepath.Join(root, "skills", "review")); err != nil {
		t.Fatal(err)
	}
	statuses, err = manager.Status(context.Background(), &kind, "review")
	if err != nil || len(statuses) != 1 || statuses[0].State != acquisition.StateMissing {
		t.Fatalf("status did not report a missing dependency: %#v %v", statuses, err)
	}
	if err := manager.Remove(context.Background(), acquisition.Skill, "review", false); err == nil {
		t.Fatal("ordinary removal accepted a missing dependency")
	}
	if err := manager.Remove(context.Background(), acquisition.Skill, "review", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "skills", "review")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dependency destination remains: %v", err)
	}
	data, err := os.ReadFile(externalLock)
	if err != nil || string(data) != "external and opaque\n" {
		t.Fatalf("external skills lock was changed: %q %v", data, err)
	}
	after, err := Load(root, "codex")
	if err != nil || after.SourceFingerprint == before {
		t.Fatalf("removal did not produce a valid distinct project: %v", err)
	}
}

func TestAcquiredSkillUpdateAndPluginCollisionPreflight(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := agent(t, "portable")
	first := filepath.Join(t.TempDir(), "review")
	second := filepath.Join(t.TempDir(), "review")
	write(t, filepath.Join(first, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n\nOne.\n")
	write(t, filepath.Join(second, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n\nTwo.\n")
	manager := AcquisitionManager(root)
	initial, err := manager.Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: first})
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := manager.Update(context.Background(), acquisition.Skill, "review", &acquisition.Selector{Type: "local", Path: second})
	if err != nil || !changed || updated.TreeSHA256 == initial.TreeSHA256 {
		t.Fatalf("update did not replace the complete tree: %#v %v %v", updated, changed, err)
	}
	approvals := 0
	manager.Approve = func(acquisition.TrustSummary) error { approvals++; return nil }
	unchanged, changed, err := manager.Update(context.Background(), acquisition.Skill, "review", nil)
	if err != nil || changed || unchanged.TreeSHA256 != updated.TreeSHA256 || approvals != 1 {
		t.Fatalf("unchanged update was not a no-op: %#v %v %v", unchanged, changed, err)
	}
	third := filepath.Join(t.TempDir(), "review")
	write(t, filepath.Join(third, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n\nTwo.\n")
	replacedSource, changed, err := manager.Update(context.Background(), acquisition.Skill, "review", &acquisition.Selector{Type: "local", Path: third})
	if err != nil || !changed || replacedSource.TreeSHA256 != updated.TreeSHA256 || replacedSource.Source.Path == updated.Source.Path || approvals != 2 {
		t.Fatalf("same-tree explicit source replacement was not recorded and confirmed: %#v %v %v", replacedSource, changed, err)
	}
	beforeLock, err := os.ReadFile(filepath.Join(root, acquisition.LockFilename))
	if err != nil {
		t.Fatal(err)
	}
	fourth := filepath.Join(t.TempDir(), "review")
	write(t, filepath.Join(fourth, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n\nThree.\n")
	manager.Approve = func(acquisition.TrustSummary) error { return context.Canceled }
	if _, _, err := manager.Update(context.Background(), acquisition.Skill, "review", &acquisition.Selector{Type: "local", Path: fourth}); !errors.Is(err, context.Canceled) {
		t.Fatalf("update cancellation was not preserved: %v", err)
	}
	afterLock, err := os.ReadFile(filepath.Join(root, acquisition.LockFilename))
	if err != nil || string(afterLock) != string(beforeLock) {
		t.Fatalf("canceled update changed provenance: %v", err)
	}
	afterCancellation, err := Load(root, "codex")
	if err != nil || !strings.Contains(string(afterCancellation.SourceContents["skills/review/SKILL.md"]), "Two.") {
		t.Fatalf("canceled update changed active source: %v", err)
	}

	plugin := filepath.Join(t.TempDir(), "review-plugin")
	write(t, filepath.Join(plugin, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"review-plugin"}`)
	write(t, filepath.Join(plugin, "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Duplicate.\n---\n")
	if _, err := manager.Add(context.Background(), acquisition.Plugin, acquisition.Selector{Type: "local", Path: plugin}); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("Plugin component collision was not rejected: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "plugins", "review-plugin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collision changed the active project: %v", err)
	}
}

func TestAcquisitionValidatesCompleteProspectiveProjectBeforeMutation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := agent(t, "portable")
	source := filepath.Join(t.TempDir(), "review")
	write(t, filepath.Join(source, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n")
	write(t, filepath.Join(root, "instructions.md"), "not valid instructions\n")
	if _, err := AcquisitionManager(root).Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: source}); err == nil || !strings.Contains(err.Error(), "current project is invalid") {
		t.Fatalf("malformed prospective project was not rejected: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "skills", "review")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed prospective validation changed source: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, acquisition.LockFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed prospective validation wrote provenance: %v", err)
	}

	root = agent(t, "tool-project")
	write(t, filepath.Join(root, "tools", "review.py"), "description = 'review'\n")
	write(t, filepath.Join(root, "pyproject.toml"), "[project]\nname = 'agent-tools'\nversion = '0'\n")
	write(t, filepath.Join(root, "uv.lock"), "version = 1\n")
	if _, err := AcquisitionManager(root).Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: source}); err != nil {
		t.Fatalf("valid tool dependency files were omitted from prospective validation: %v", err)
	}

	root = agent(t, "bounded")
	for index := 0; index < maxSkills; index++ {
		name := fmt.Sprintf("skill-%03d", index)
		write(t, filepath.Join(root, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: Existing.\n---\n")
	}
	if _, err := AcquisitionManager(root).Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: source}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("prospective Skill cardinality was not rejected: %v", err)
	}
}

func TestAcquisitionApprovalAndSkillBasenameFailBeforeMutation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := agent(t, "portable")
	mismatched := filepath.Join(t.TempDir(), "not-review")
	write(t, filepath.Join(mismatched, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n")
	manager := AcquisitionManager(root)
	if _, err := manager.Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: mismatched}); err == nil || !strings.Contains(err.Error(), "basename") {
		t.Fatalf("mismatched Skill source directory was not rejected: %v", err)
	}
	source := filepath.Join(t.TempDir(), "review")
	write(t, filepath.Join(source, "SKILL.md"), "---\nname: review\ndescription: Reviews changes.\n---\n")
	approved := false
	manager.Approve = func(summary acquisition.TrustSummary) error {
		approved = true
		if summary.Name != "review" || summary.Destination != "skills/review" || summary.TreeSHA256 == "" || summary.FileCount != 1 {
			t.Fatalf("trust summary is incomplete: %#v", summary)
		}
		return context.Canceled
	}
	if _, err := manager.Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: "local", Path: source}); !errors.Is(err, context.Canceled) {
		t.Fatalf("approval cancellation was not preserved: %v", err)
	}
	if !approved {
		t.Fatal("approval hook was not called")
	}
	if _, err := os.Lstat(filepath.Join(root, "skills", "review")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled approval changed the active project: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, acquisition.LockFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled approval wrote provenance: %v", err)
	}
}

func TestAcquiredPluginManifestNameMustMatchProvenance(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := agent(t, "portable")
	source := filepath.Join(t.TempDir(), "review-pack")
	write(t, filepath.Join(source, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"review-pack"}`)
	manager := AcquisitionManager(root)
	if _, err := manager.Add(context.Background(), acquisition.Plugin, acquisition.Selector{Type: "local", Path: source}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "plugins", "review-pack"), filepath.Join(root, "plugins", "other")); err != nil {
		t.Fatal(err)
	}
	lockBytes, err := os.ReadFile(filepath.Join(root, acquisition.LockFilename))
	if err != nil {
		t.Fatal(err)
	}
	lock, err := acquisition.ParseLock(lockBytes)
	if err != nil {
		t.Fatal(err)
	}
	lock.Dependencies[0].Name = "other"
	lock.Dependencies[0].Destination = "plugins/other"
	lockBytes, err = acquisition.EncodeLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, acquisition.LockFilename), lockBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "codex"); err == nil || !strings.Contains(err.Error(), "does not match lock name") {
		t.Fatalf("project load accepted mismatched Plugin provenance: %v", err)
	}
	kind := acquisition.Plugin
	if _, err := manager.Status(context.Background(), &kind, "other"); err == nil || !strings.Contains(err.Error(), "validated component name") {
		t.Fatalf("status accepted mismatched Plugin provenance: %v", err)
	}
}

func TestPluginCollisionProjectionUsesOrdinaryAcceptedSkillCeiling(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := agent(t, "portable")
	write(t, filepath.Join(root, "skills", "zz-collision", "SKILL.md"), "---\nname: zz-collision\ndescription: Existing.\n---\n")
	source := filepath.Join(t.TempDir(), "large-pack")
	write(t, filepath.Join(source, "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"large-pack"}`)
	for index := 0; index < maxSkills; index++ {
		name := fmt.Sprintf("candidate-%03d", index)
		write(t, filepath.Join(source, "skills", name, "SKILL.md"), "---\nname: "+name+"\ndescription: Candidate.\n---\n")
	}
	write(t, filepath.Join(source, "skills", "zz-collision", "SKILL.md"), "---\nname: zz-collision\ndescription: Beyond the accepted ceiling.\n---\n")
	if _, err := AcquisitionManager(root).Add(context.Background(), acquisition.Plugin, acquisition.Selector{Type: "local", Path: source}); err != nil {
		t.Fatalf("collision projection included a Plugin Skill ordinary loading skips: %v", err)
	}
}

func TestForcedRemovalValidatesProjectWithoutCorruptTrackedComponent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := agent(t, "portable")
	source := filepath.Join(t.TempDir(), "review")
	write(t, filepath.Join(source, "SKILL.md"), "---\nname: review\ndescription: Review.\n---\n")
	manager := AcquisitionManager(root)
	if _, err := manager.Add(context.Background(), acquisition.Skill, acquisition.Selector{Type: acquisition.SourceLocal, Path: source}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "skills", "review", "SKILL.md"), "corrupt\n")
	if err := manager.Remove(context.Background(), acquisition.Skill, "review", true); err != nil {
		t.Fatalf("forced removal could not validate the project without the corrupt tracked component: %v", err)
	}
	if _, err := Load(root, "codex"); err != nil {
		t.Fatalf("project was invalid after forced removal: %v", err)
	}
}
