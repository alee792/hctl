package stage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hctl/internal/project"
	"hctl/internal/rootfs"
)

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
