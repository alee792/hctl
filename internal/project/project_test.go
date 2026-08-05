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
	if len(first.Skills) != 1 || first.Skills[0].Path != "skills/echo.md" {
		t.Fatalf("discovered skills = %#v", first.Skills)
	}

	write(t, filepath.Join(root, "skills", "research.md"), "---\nname: research\ndescription: Research carefully.\n---\n\nFind evidence.\n")
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
	write(t, filepath.Join(root, "instructions.md"), "Be concise.\n")
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
	write(t, filepath.Join(root, "instructions.md"), "Help the user.\n")
	p, err := Load(root, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "simple-helper" || len(p.Skills) != 0 {
		t.Fatalf("project = name %q, skills %#v", p.Name, p.Skills)
	}
}

func TestLoadRejectsUnsafeOrAmbiguousSources(t *testing.T) {
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
		write(t, filepath.Join(root, "skills", "echo.md"), "---\nname: other\ndescription: Wrong name.\n---\n")
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "match its filename") {
			t.Fatalf("ambiguous skill was not rejected: %v", err)
		}
	})

	t.Run("skill symlink", func(t *testing.T) {
		root := agent(t, "portable")
		outside := filepath.Join(t.TempDir(), "outside.md")
		write(t, outside, "---\nname: linked\ndescription: Outside.\n---\n")
		if err := os.Symlink(outside, filepath.Join(root, "skills", "linked.md")); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "symlinks") {
			t.Fatalf("skill symlink was not rejected: %v", err)
		}
	})

	t.Run("skill limit", func(t *testing.T) {
		root := agent(t, "portable")
		for index := 0; index < maxSkills; index++ {
			name := fmt.Sprintf("extra-%d", index)
			write(t, filepath.Join(root, "skills", name+".md"), fmt.Sprintf("---\nname: %s\ndescription: Extra.\n---\n", name))
		}
		if _, err := Load(root, "claude"); err == nil || !strings.Contains(err.Error(), "at most") {
			t.Fatalf("skill limit was not enforced: %v", err)
		}
	})
}

func agent(t *testing.T, directory string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), directory)
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "instructions.md"), "Be concise.\n")
	write(t, filepath.Join(root, "skills", "echo.md"), "---\nname: echo\ndescription: Repeat safely.\n---\n\nUse echo.\n")
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
