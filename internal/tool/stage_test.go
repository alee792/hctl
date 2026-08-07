package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hctl/internal/rootfs"
)

func TestStagePreparedRuntimeCarriesOnlySelectedExecutionClosure(t *testing.T) {
	artifact := t.TempDir()
	workspace := filepath.Join(artifact, "workspace")
	source := t.TempDir()
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	fingerprint := "staged-runtime-test"
	cache := cacheRelative(fingerprint)

	deno := executableTestFile(t, t.TempDir(), "deno", "#!/bin/sh\nexit 0\n")
	uvDirectory := t.TempDir()
	pythonBase := filepath.Join(t.TempDir(), "python")
	python := executableTestFile(t, filepath.Join(pythonBase, "bin"), "python", "#!/bin/sh\nexit 0\n")
	uv := executableTestFile(t, uvDirectory, "uv", "#!/bin/sh\nprintf '%s\\n' '{\"executable\":\""+python+"\",\"base_prefix\":\""+pythonBase+"\"}'\n")
	receipt, err := json.Marshal(preparedRuntime{Deno: deno, UV: uv})
	if err != nil {
		t.Fatal(err)
	}
	if err := rootfs.WriteAtomic(workspace, cache+"/executables.json", append(receipt, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rootfs.WriteAtomic(workspace, cache+"/deno-dir/npm/example/package.js", []byte("export {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rootfs.WriteAtomic(workspace, cache+"/deno-dir/gen/file/build-only.js", []byte("build path"), 0o644); err != nil {
		t.Fatal(err)
	}
	venv := filepath.Join(workspace, filepath.FromSlash(cache+"/python-venv"))
	if err := os.MkdirAll(filepath.Join(venv, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(python, filepath.Join(venv, "bin", "python")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(venv, "pyvenv.cfg"), []byte("home = "+filepath.Join(pythonBase, "bin")+"\nexecutable = "+python+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"go/main.go", "go/go.mod", "go/go.sum"} {
		if err := rootfs.WriteAtomic(workspace, cache+"/"+relative, []byte(relative), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := rootfs.WriteAtomic(workspace, cache+"/go/host", []byte("host"), 0o755); err != nil {
		t.Fatal(err)
	}
	inventory := Inventory{Sources: []Source{{Name: "ts", Language: TypeScript}, {Name: "py", Language: Python}, {Name: "go", Language: Go}}}
	if got, want := RequiredComponents(inventory), []string{"deno", "python", "uv", "go-host"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("components = %v, want %v", got, want)
	}
	if err := stagePreparedRuntime(context.Background(), source, workspace, fingerprint, inventory, artifact, t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(cache+"/deno-dir/gen"))); !os.IsNotExist(err) {
		t.Fatalf("Deno build cache remains: %v", err)
	}
	for _, relative := range []string{
		"opt/hctl/runtimes/deno/bin/deno",
		"opt/hctl/runtimes/uv/bin/uv",
		"opt/hctl/runtimes/python/bin/python",
		"workspace/" + cache + "/go/host",
	} {
		if _, err := os.Stat(filepath.Join(artifact, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("staged %s: %v", relative, err)
		}
	}
	for _, relative := range []string{"go/main.go", "go/go.mod", "go/go.sum"} {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(cache+"/"+relative))); !os.IsNotExist(err) {
			t.Fatalf("build-only %s remains: %v", relative, err)
		}
	}
	data, _, exists, err := rootfs.ReadOptional(workspace, cache+"/executables.json", 4096)
	if err != nil || !exists {
		t.Fatal(err)
	}
	var staged preparedRuntime
	if err := json.Unmarshal(data, &staged); err != nil {
		t.Fatal(err)
	}
	if staged.Deno != "/opt/hctl/runtimes/deno/bin/deno" || staged.UV != "/opt/hctl/runtimes/uv/bin/uv" || !strings.HasPrefix(staged.Python, "/workspace/") {
		t.Fatalf("staged receipt = %#v", staged)
	}
	if info, err := os.Lstat(filepath.Join(venv, "bin", "python")); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("staged Python executable = %v, %v", info, err)
	}
	config, err := os.ReadFile(filepath.Join(venv, "pyvenv.cfg"))
	if err != nil || strings.Contains(string(config), pythonBase) || !strings.Contains(string(config), "/opt/hctl/runtimes/python") {
		t.Fatalf("staged pyvenv.cfg = %q, %v", config, err)
	}
}

func TestRequiredComponentsSelectsOnlyAuthoredLanguages(t *testing.T) {
	tests := []struct {
		name    string
		sources []Source
		want    []string
	}{
		{name: "tool-free", want: []string{}},
		{name: "typescript", sources: []Source{{Language: TypeScript}}, want: []string{"deno"}},
		{name: "python", sources: []Source{{Language: Python}}, want: []string{"python", "uv"}},
		{name: "go", sources: []Source{{Language: Go}}, want: []string{"go-host"}},
		{name: "mixed", sources: []Source{{Language: Go}, {Language: TypeScript}, {Language: Python}}, want: []string{"deno", "python", "uv", "go-host"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RequiredComponents(Inventory{Sources: test.sources}); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("components = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStageRejectsNoncanonicalPythonRuntime(t *testing.T) {
	err := copyPythonRuntime(t.TempDir(), t.TempDir(), "cache", pythonRuntime{BasePrefix: t.TempDir()}, true)
	if err == nil || !strings.Contains(err.Error(), "/opt/hctl/runtimes/python") {
		t.Fatalf("noncanonical Python runtime error = %v", err)
	}
}

func executableTestFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
