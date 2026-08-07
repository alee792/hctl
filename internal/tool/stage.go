package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"hctl/internal/rootfs"
	"hctl/internal/secureenv"
)

const (
	maxRuntimeFileBytes = 256 << 20
	maxRuntimeFiles     = 32768
	maxRuntimeBytes     = 1 << 30
)

// RequiredComponents reports the stable execution components selected by an
// authored tool inventory. Go contributes only its compiled host.
func RequiredComponents(inventory Inventory) []string {
	components := []string{}
	if hasLanguage(inventory, TypeScript) {
		components = append(components, "deno")
	}
	if hasLanguage(inventory, Python) {
		components = append(components, "python", "uv")
	}
	if hasLanguage(inventory, Go) {
		components = append(components, "go-host")
	}
	return components
}

// StagePreparedRuntime converts an already inspected workspace tool runtime
// into the canonical execution-only form used by a staged filesystem. The
// artifact root must contain workspaceRoot at its final relative location.
func StagePreparedRuntime(ctx context.Context, sourceRoot, workspaceRoot, sourceFingerprint string, inventory Inventory, artifactRoot, buildHome string) error {
	return stagePreparedRuntime(ctx, sourceRoot, workspaceRoot, sourceFingerprint, inventory, artifactRoot, buildHome, true)
}

func stagePreparedRuntime(ctx context.Context, sourceRoot, workspaceRoot, sourceFingerprint string, inventory Inventory, artifactRoot, buildHome string, requireCanonicalPython bool) error {
	if len(inventory.Sources) == 0 {
		return nil
	}
	prepared, err := readPreparedRuntime(workspaceRoot, sourceFingerprint)
	if err != nil {
		return err
	}
	cache := cacheRelative(sourceFingerprint)
	if hasLanguage(inventory, TypeScript) {
		if err := copyRuntimeExecutable(artifactRoot, prepared.Deno, "opt/hctl/runtimes/deno/bin/deno"); err != nil {
			return fmt.Errorf("cannot stage Deno runtime: %w", err)
		}
		prepared.Deno = "/opt/hctl/runtimes/deno/bin/deno"
		if err := pruneDenoBuildCache(workspaceRoot, cache); err != nil {
			return err
		}
	}
	if hasLanguage(inventory, Python) {
		originalUV := prepared.UV
		if err := copyRuntimeExecutable(artifactRoot, originalUV, "opt/hctl/runtimes/uv/bin/uv"); err != nil {
			return fmt.Errorf("cannot stage uv runtime: %w", err)
		}
		python, err := inspectPreparedPython(ctx, sourceRoot, workspaceRoot, cache, originalUV, buildHome)
		if err != nil {
			return err
		}
		if err := copyPythonRuntime(artifactRoot, workspaceRoot, cache, python, requireCanonicalPython); err != nil {
			return err
		}
		prepared.UV = "/opt/hctl/runtimes/uv/bin/uv"
		prepared.Python = "/workspace/" + cache + "/python-venv/bin/python"
	}
	if hasLanguage(inventory, Go) {
		for _, relative := range []string{cache + "/go/main.go", cache + "/go/go.mod", cache + "/go/go.sum"} {
			if err := rootfs.RemoveRegular(workspaceRoot, relative); err != nil {
				return err
			}
		}
	}
	receipt, err := json.Marshal(prepared)
	if err != nil {
		return errors.New("cannot encode staged tool runtime")
	}
	return rootfs.WriteAtomic(workspaceRoot, cache+"/executables.json", append(receipt, '\n'), 0o600)
}

type pythonRuntime struct {
	Executable string `json:"executable"`
	BasePrefix string `json:"base_prefix"`
}

func inspectPreparedPython(ctx context.Context, sourceRoot, workspaceRoot, cache, uv, buildHome string) (pythonRuntime, error) {
	if _, err := preparedExecutable(uv, "uv"); err != nil {
		return pythonRuntime{}, err
	}
	script := "import json,sys; print(json.dumps({'executable':getattr(sys,'_base_executable',sys.executable),'base_prefix':sys.base_prefix}))"
	environment := secureenv.Replace(secureenv.Staging(buildHome), map[string]string{"PYTHONDONTWRITEBYTECODE": "1", "UV_PROJECT_ENVIRONMENT": filepath.Join(workspaceRoot, filepath.FromSlash(cache+"/python-venv"))})
	output, err := runNativeEnvironmentOutput(ctx, workspaceRoot, environment, uv, "run", "--locked", "--no-sync", "--project", sourceRoot, "python", "-c", script)
	if err != nil {
		return pythonRuntime{}, fmt.Errorf("cannot inspect prepared Python runtime: %w", err)
	}
	var result pythonRuntime
	if json.Unmarshal(bytes.TrimSpace(output), &result) != nil || !filepath.IsAbs(result.Executable) || !filepath.IsAbs(result.BasePrefix) {
		return pythonRuntime{}, errors.New("prepared Python runtime reported invalid paths")
	}
	return result, nil
}

func runNativeEnvironmentOutput(ctx context.Context, directory string, environment []string, executable string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	command.Env = environment
	output := &boundedBuffer{remaining: 64 << 10}
	command.Stdout, command.Stderr = output, output
	if err := command.Run(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func copyPythonRuntime(artifactRoot, workspaceRoot, cache string, runtime pythonRuntime, requireCanonical bool) error {
	if requireCanonical && filepath.Clean(runtime.BasePrefix) != "/opt/hctl/runtimes/python" {
		return errors.New("python runtime must be installed at /opt/hctl/runtimes/python before staging")
	}
	base, err := filepath.EvalSymlinks(runtime.BasePrefix)
	if err != nil {
		return errors.New("cannot resolve prepared Python base runtime")
	}
	executable, err := filepath.EvalSymlinks(runtime.Executable)
	if err != nil {
		return errors.New("cannot resolve prepared Python executable")
	}
	relativeExecutable, err := filepath.Rel(base, executable)
	if err != nil || relativeExecutable == "." || strings.HasPrefix(relativeExecutable, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeExecutable) {
		return errors.New("prepared Python executable is outside its base runtime")
	}
	if err := copyRuntimeTree(artifactRoot, base, "opt/hctl/runtimes/python"); err != nil {
		return fmt.Errorf("cannot stage Python base runtime: %w", err)
	}
	stagedBase := filepath.Join(artifactRoot, filepath.FromSlash("opt/hctl/runtimes/python"))
	if err := prunePythonRuntime(stagedBase, filepath.ToSlash(relativeExecutable), executable); err != nil {
		return err
	}
	venv := filepath.Join(workspaceRoot, filepath.FromSlash(cache+"/python-venv"))
	if err := materializeRuntimeSymlinks(venv); err != nil {
		return fmt.Errorf("cannot make prepared Python environment relocatable: %w", err)
	}
	configPath := filepath.Join(venv, "pyvenv.cfg")
	config, err := os.ReadFile(configPath)
	if err != nil {
		return errors.New("prepared Python environment has no pyvenv.cfg")
	}
	finalBase := "/opt/hctl/runtimes/python"
	finalExecutable := filepath.ToSlash(filepath.Join(finalBase, relativeExecutable))
	rewritten := rewritePythonVenvConfig(config, filepath.ToSlash(filepath.Dir(finalExecutable)), finalExecutable)
	if err := os.WriteFile(configPath, rewritten, 0o644); err != nil {
		return errors.New("cannot rewrite prepared Python environment")
	}
	if err := prunePythonVenv(venv, executable); err != nil {
		return err
	}
	return nil
}

func pruneDenoBuildCache(workspaceRoot, cache string) error {
	directory := filepath.Join(workspaceRoot, filepath.FromSlash(cache+"/deno-dir"))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return errors.New("prepared Deno cache is missing")
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "gen" && !strings.Contains(name, "analysis_cache") && !strings.HasPrefix(name, "v8_code_cache") && !strings.HasPrefix(name, "check_cache") && !strings.HasPrefix(name, "fast_check_cache") {
			continue
		}
		if err := removeGeneratedTree(directory, name); err != nil {
			return fmt.Errorf("cannot discard Deno build cache: %w", err)
		}
	}
	registryFiles := []string{}
	if err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect prepared Deno cache")
		}
		if !entry.IsDir() && entry.Name() == "registry.json" {
			registryFiles = append(registryFiles, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, path := range registryFiles {
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return errors.New("cannot describe Deno registry metadata")
		}
		if err := removeGeneratedTree(directory, filepath.ToSlash(relative)); err != nil {
			return errors.New("cannot discard Deno registry metadata")
		}
	}
	return nil
}

func prunePythonRuntime(root, relativeExecutable, sourceExecutable string) error {
	if relativeExecutable == "bin" || !strings.HasPrefix(relativeExecutable, "bin/") {
		return errors.New("prepared Python executable is outside the runtime bin directory")
	}
	if err := removeGeneratedTree(root, "bin"); err != nil {
		return errors.New("cannot prune Python runtime commands")
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o755); err != nil {
		return errors.New("cannot recreate Python runtime bin directory")
	}
	if err := copyRegularPath(sourceExecutable, filepath.Join(root, filepath.FromSlash(relativeExecutable)), 0o755); err != nil {
		return errors.New("cannot install staged Python executable")
	}
	return removePythonBytecode(root)
}

func prunePythonVenv(root, sourceExecutable string) error {
	bin := filepath.Join(root, "bin")
	entries, err := os.ReadDir(bin)
	if err != nil {
		return errors.New("prepared Python environment has no bin directory")
	}
	for _, entry := range entries {
		if entry.Name() == "python" {
			continue
		}
		if err := removeGeneratedTree(bin, entry.Name()); err != nil {
			return errors.New("cannot prune Python environment commands")
		}
	}
	if err := copyRegularPath(sourceExecutable, filepath.Join(bin, "python"), 0o755); err != nil {
		return errors.New("cannot install staged Python virtual-environment executable")
	}
	return removePythonBytecode(root)
}

func rewritePythonVenvConfig(config []byte, home, executable string) []byte {
	lines := bytes.Split(config, []byte{'\n'})
	for index, line := range lines {
		key, _, found := bytes.Cut(line, []byte{'='})
		if !found {
			continue
		}
		switch strings.TrimSpace(string(key)) {
		case "home":
			lines[index] = []byte("home = " + home)
		case "executable":
			lines[index] = []byte("executable = " + executable)
		case "command":
			lines[index] = []byte("command = " + executable + " -m venv /workspace")
		}
	}
	return bytes.Join(lines, []byte{'\n'})
}

func removePythonBytecode(root string) error {
	paths := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect Python runtime")
		}
		if entry.Name() == "__pycache__" && entry.IsDir() || !entry.IsDir() && filepath.Ext(entry.Name()) == ".pyc" {
			paths = append(paths, path)
			if entry.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return errors.New("cannot describe Python bytecode path")
		}
		if err := removeGeneratedTree(root, filepath.ToSlash(relative)); err != nil {
			return errors.New("cannot discard Python bytecode")
		}
	}
	return nil
}

func removeGeneratedTree(root, relative string) error {
	cleaned, err := rootfs.CleanRelative(filepath.ToSlash(relative))
	if err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(cleaned))
	if err := filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return filepath.SkipDir
		}
		if walkErr != nil {
			return errors.New("cannot inspect generated runtime path")
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("generated runtime path contains a symlink")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return errors.New("cannot remove generated runtime path")
	}
	return nil
}

func copyRuntimeExecutable(root, source, relative string) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return errors.New("runtime executable cannot be resolved")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Size() > maxRuntimeFileBytes {
		return errors.New("runtime executable must be a bounded regular executable")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return errors.New("runtime executable cannot be read")
	}
	return rootfs.WriteAtomic(root, relative, data, 0o755)
}

func copyRuntimeTree(root, source, destination string) error {
	files := 0
	total := int64(0)
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot read runtime tree")
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return errors.New("cannot describe runtime path")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect runtime path")
		}
		readPath := path
		if info.Mode()&os.ModeSymlink != 0 {
			readPath, err = filepath.EvalSymlinks(path)
			if err != nil {
				return errors.New("runtime contains an unresolved symlink")
			}
			info, err = os.Stat(readPath)
			if err != nil {
				return errors.New("runtime symlink target is unavailable")
			}
			if info.IsDir() {
				// Build-time convenience links such as global site-packages and
				// development headers are not part of the selected interpreter
				// closure. The prepared virtual environment carries its own
				// locked site-packages.
				return nil
			}
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() > maxRuntimeFileBytes {
			return errors.New("runtime entries must be bounded regular files")
		}
		files++
		total += info.Size()
		if files > maxRuntimeFiles || total > maxRuntimeBytes {
			return errors.New("runtime tree exceeds staging limits")
		}
		data, err := os.ReadFile(readPath)
		if err != nil {
			return errors.New("cannot read runtime file")
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		return rootfs.WriteAtomic(root, destination+"/"+filepath.ToSlash(relative), data, mode)
	})
}

func materializeRuntimeSymlinks(root string) error {
	paths := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect prepared environment")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect prepared environment")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return errors.New("prepared environment contains an unresolved symlink")
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return errors.New("prepared environment symlink target is unavailable")
		}
		if info.IsDir() {
			if !pathWithin(root, resolved) {
				return errors.New("prepared environment directory symlink escapes its root")
			}
			if err := os.Remove(path); err != nil {
				return errors.New("cannot replace prepared environment directory symlink")
			}
			if err := copyLocalRuntimeTree(resolved, path); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() > maxRuntimeFileBytes {
			return errors.New("prepared environment symlink must resolve to a bounded regular file")
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.Remove(path); err != nil {
			return errors.New("cannot replace prepared environment symlink")
		}
		if err := copyRegularPath(resolved, path, mode); err != nil {
			return err
		}
	}
	return nil
}

func copyLocalRuntimeTree(source, destination string) error {
	if err := os.Mkdir(destination, 0o755); err != nil {
		return errors.New("cannot create materialized runtime directory")
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot read materialized runtime directory")
		}
		if path == source {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return errors.New("cannot describe materialized runtime path")
		}
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect materialized runtime path")
		}
		readPath := path
		if info.Mode()&os.ModeSymlink != 0 {
			readPath, err = filepath.EvalSymlinks(path)
			if err != nil {
				return errors.New("materialized runtime contains an unresolved symlink")
			}
			info, err = os.Stat(readPath)
			if err != nil || info.IsDir() {
				return errors.New("nested runtime directory symlinks are not supported")
			}
		}
		if info.IsDir() {
			if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return errors.New("cannot create materialized runtime directory")
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() > maxRuntimeFileBytes {
			return errors.New("materialized runtime entries must be bounded regular files")
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		return copyRegularPath(readPath, target, mode)
	})
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func copyRegularPath(source, destination string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil || len(data) > maxRuntimeFileBytes {
		return errors.New("cannot copy bounded runtime file")
	}
	if err := os.WriteFile(destination, data, mode); err != nil {
		return errors.New("cannot write runtime file")
	}
	return nil
}
