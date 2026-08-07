// Package stage assembles a complete OCI-neutral agent filesystem tree.
// It writes only to a temporary sibling of the requested output and publishes
// the tree with one rename after every file and manifest entry is verified.
package stage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"hctl/internal/project"
	"hctl/internal/rootfs"
	"hctl/internal/secureenv"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

const (
	finalWorkspace = "/workspace"
	finalHome      = "/home/hctl"
	finalHCTL      = "/opt/hctl/bin/hctl"
	manifestPath   = "opt/hctl/artifact.json"
	maxStagedFile  = 384 << 20
	maxStagedFiles = 65536
	maxStagedBytes = int64(2 << 30)
	runtimeUID     = 65532
	runtimeGID     = 65532
)

var semanticVersion = regexp.MustCompile(`\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?`)

type Request struct {
	Project           *project.Project
	Output            string
	HCTLExecutable    string
	HarnessExecutable string
	HarnessVersion    string
}

type Result struct {
	Output      string
	Manifest    Manifest
	Diagnostics []setup.Diagnostic
}

type Manifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Generator     string              `json:"generator"`
	Harness       HarnessIdentity     `json:"harness"`
	Agent         AgentIdentity       `json:"agent"`
	Target        TargetIdentity      `json:"target"`
	Runtimes      []string            `json:"runtimes"`
	Paths         RuntimePaths        `json:"paths"`
	Directories   []ManifestDirectory `json:"directories"`
	Files         []ManifestFile      `json:"files"`
}

type HarnessIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type AgentIdentity struct {
	Name              string `json:"name"`
	ID                string `json:"id"`
	SourceFingerprint string `json:"source_fingerprint"`
}

type TargetIdentity struct {
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	ABI            string `json:"abi"`
	CompatibleBase string `json:"compatible_base"`
}

type RuntimePaths struct {
	HCTL        string `json:"hctl"`
	Harness     string `json:"harness"`
	Agent       string `json:"agent"`
	Workspace   string `json:"workspace"`
	HarnessHome string `json:"harness_home"`
	Entrypoint  string `json:"entrypoint"`
}

type ManifestDirectory struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	UID    int    `json:"uid"`
	GID    int    `json:"gid"`
}

// HarnessVersion returns the semantic version reported by a verified harness.
func HarnessVersion(ctx context.Context, executable string) (string, error) {
	home, err := os.MkdirTemp("", "hctl-harness-version-")
	if err != nil {
		return "", errors.New("cannot isolate harness version inspection")
	}
	defer func() { _ = os.RemoveAll(home) }()
	command := exec.CommandContext(ctx, executable, "--version")
	command.Env = secureenv.Staging(home)
	output, err := command.Output()
	if err != nil || len(output) > 4096 {
		return "", errors.New("cannot read harness version")
	}
	version := semanticVersion.FindString(string(output))
	if version == "" {
		return "", errors.New("harness did not report a semantic version")
	}
	return version, nil
}

func Create(ctx context.Context, request Request) (Result, error) {
	if request.Project == nil || request.HarnessVersion == "" {
		return Result{}, errors.New("staging request is incomplete")
	}
	output, err := safeOutput(request.Output, request.Project)
	if err != nil {
		return Result{}, err
	}
	parent := filepath.Dir(output)
	temporary, err := os.MkdirTemp(parent, ".hctl-stage-")
	if err != nil {
		return Result{}, errors.New("cannot create staging directory")
	}
	defer func() { _ = removePrivateBuildTree(temporary) }()
	if err := os.Chmod(temporary, 0o755); err != nil {
		return Result{}, errors.New("cannot set staging directory mode")
	}

	finalAgent := "/opt/hctl/agents/" + request.Project.Name
	physicalAgent := underRoot(temporary, finalAgent)
	physicalWorkspace := underRoot(temporary, finalWorkspace)
	buildHome := filepath.Join(temporary, ".hctl-build-home")
	if err := os.MkdirAll(physicalAgent, 0o755); err != nil {
		return Result{}, errors.New("cannot create staged agent directory")
	}
	if err := os.MkdirAll(physicalWorkspace, 0o755); err != nil {
		return Result{}, errors.New("cannot create staged workspace")
	}
	if err := os.MkdirAll(underRoot(temporary, finalHome), 0o700); err != nil {
		return Result{}, errors.New("cannot create staged harness home")
	}
	if err := os.Mkdir(buildHome, 0o700); err != nil {
		return Result{}, errors.New("cannot create isolated staging home")
	}
	if err := copyProjectSources(request.Project, temporary, strings.TrimPrefix(finalAgent, "/")); err != nil {
		return Result{}, err
	}
	prepared, err := project.LoadRelocated(physicalAgent, request.Project.Harness, physicalWorkspace, request.Project)
	if err != nil {
		return Result{}, fmt.Errorf("cannot validate staged source: %w", err)
	}
	if err := tool.PrepareStaged(ctx, prepared.SourceRoot, prepared.WorkspaceRoot, prepared.SourceFingerprint, prepared.Tools, buildHome); err != nil {
		return Result{}, err
	}
	if err := tool.StagePreparedRuntime(ctx, prepared.SourceRoot, prepared.WorkspaceRoot, prepared.SourceFingerprint, prepared.Tools, temporary, buildHome); err != nil {
		return Result{}, err
	}
	if err := removePrivateBuildTree(buildHome); err != nil {
		return Result{}, fmt.Errorf("cannot discard isolated staging home: %w", err)
	}
	if err := verifyPreparedSource(prepared); err != nil {
		return Result{}, err
	}

	if err := copyExecutable(request.HCTLExecutable, temporary, strings.TrimPrefix(finalHCTL, "/")); err != nil {
		return Result{}, fmt.Errorf("cannot stage hctl executable: %w", err)
	}
	finalHarness, err := copyHarness(request.Project.Harness, request.HarnessExecutable, temporary)
	if err != nil {
		return Result{}, err
	}
	logical, err := project.WithRuntimeRoots(prepared, finalAgent, finalWorkspace)
	if err != nil {
		return Result{}, err
	}
	applyResult, err := setup.ApplyAt(logical, finalHCTL, physicalWorkspace)
	if err != nil {
		return Result{}, err
	}
	entrypoint := entrypointBytes(logical, finalHarness)
	if err := rootfs.WriteAtomic(temporary, "opt/hctl/bin/agent-entrypoint", entrypoint, 0o755); err != nil {
		return Result{}, err
	}
	if err := rejectBuildPaths(temporary, request, prepared); err != nil {
		return Result{}, err
	}
	target := targetIdentity()
	manifest := Manifest{
		SchemaVersion: 1,
		Generator:     project.GeneratorVersion,
		Harness:       HarnessIdentity{Name: logical.Harness, Version: request.HarnessVersion},
		Agent:         AgentIdentity{Name: logical.Name, ID: logical.AgentID, SourceFingerprint: logical.SourceFingerprint},
		Target:        target,
		Runtimes:      tool.RequiredComponents(logical.Tools),
		Paths:         RuntimePaths{HCTL: finalHCTL, Harness: finalHarness, Agent: finalAgent, Workspace: finalWorkspace, HarnessHome: finalHome, Entrypoint: "/opt/hctl/bin/agent-entrypoint"},
	}
	manifest.Directories, err = collectDirectories(temporary)
	if err != nil {
		return Result{}, err
	}
	manifest.Files, err = collectFiles(temporary)
	if err != nil {
		return Result{}, err
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Result{}, errors.New("cannot encode artifact manifest")
	}
	if err := rootfs.WriteAtomic(temporary, manifestPath, append(manifestBytes, '\n'), 0o644); err != nil {
		return Result{}, err
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return Result{}, errors.New("stage output appeared before publication")
	}
	if err := os.Rename(temporary, output); err != nil {
		return Result{}, errors.New("cannot publish staged filesystem")
	}
	return Result{Output: output, Manifest: manifest, Diagnostics: applyResult.Diagnostics}, nil
}

func safeOutput(requested string, p *project.Project) (string, error) {
	if requested == "" {
		return "", errors.New("--output is required")
	}
	abs, err := filepath.Abs(requested)
	if err != nil || filepath.Clean(abs) != abs || abs == string(filepath.Separator) {
		return "", errors.New("stage output path is invalid")
	}
	if _, err := os.Lstat(abs); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return "", errors.New("stage output already exists")
		}
		return "", errors.New("cannot inspect stage output")
	}
	parent, err := rootfs.CanonicalDir(filepath.Dir(abs))
	if err != nil {
		return "", errors.New("stage output parent must be an existing real directory")
	}
	output := filepath.Join(parent, filepath.Base(abs))
	for _, protected := range []string{p.SourceRoot, p.WorkspaceRoot} {
		if within(protected, output) {
			return "", errors.New("stage output must be outside agent source and workspace")
		}
	}
	return output, nil
}

func removePrivateBuildTree(root string) error {
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect isolated build state")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect isolated build state")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0o600)
		if info.IsDir() {
			mode = 0o700
		}
		if err := os.Chmod(path, mode); err != nil {
			return errors.New("cannot make isolated build state removable")
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func underRoot(root, absolute string) string {
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(absolute, "/")))
}

func copyProjectSources(p *project.Project, artifactRoot, destination string) error {
	seen := map[string]bool{}
	for _, source := range p.Sources {
		if seen[source.Path] {
			return fmt.Errorf("agent source path %s collides during staging", source.Path)
		}
		seen[source.Path] = true
		info, err := os.Lstat(filepath.Join(p.SourceRoot, filepath.FromSlash(source.Path)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 8<<20 {
			return fmt.Errorf("agent source %s changed before staging", source.Path)
		}
		data, err := rootfs.ReadSource(p.SourceRoot, source.Path, 8<<20)
		if err != nil || rootfs.SHA256(data) != source.SHA256 {
			return fmt.Errorf("agent source %s changed before staging", source.Path)
		}
		mode := os.FileMode(0o644)
		if source.Executable {
			mode = 0o755
		}
		if err := rootfs.WriteAtomic(artifactRoot, destination+"/"+source.Path, data, mode); err != nil {
			return err
		}
	}
	return nil
}

func verifyPreparedSource(p *project.Project) error {
	files := make(map[string]project.SourceRecord, len(p.Sources))
	directories := map[string]bool{".": true}
	for _, source := range p.Sources {
		files[source.Path] = source
		for parent := filepath.ToSlash(filepath.Dir(source.Path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			directories[parent] = true
		}
	}
	return filepath.WalkDir(p.SourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot verify prepared agent source")
		}
		relative, err := filepath.Rel(p.SourceRoot, path)
		if err != nil {
			return errors.New("cannot describe prepared agent source")
		}
		relative = filepath.ToSlash(relative)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("tool preparation introduced an unsafe agent source entry")
		}
		if entry.IsDir() {
			if !directories[relative] {
				return errors.New("tool preparation introduced an unowned agent source directory")
			}
			return nil
		}
		record, exists := files[relative]
		if !exists || !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return errors.New("tool preparation introduced an unowned agent source file")
		}
		data, err := os.ReadFile(path)
		if err != nil || rootfs.SHA256(data) != record.SHA256 {
			return fmt.Errorf("tool preparation changed agent source %s", relative)
		}
		return nil
	})
}

func copyExecutable(source, root, relative string) error {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return errors.New("executable cannot be resolved")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Size() > maxStagedFile {
		return errors.New("executable must be a bounded regular executable")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return errors.New("executable cannot be read")
	}
	return rootfs.WriteAtomic(root, relative, data, 0o755)
}

func copyHarness(name, executable, root string) (string, error) {
	canonicalRoot := filepath.Clean("/opt/hctl/harness")
	abs, err := filepath.Abs(executable)
	if err != nil {
		return "", errors.New("cannot resolve harness executable")
	}
	if within(canonicalRoot, abs) {
		if err := copyTree(canonicalRoot, root, "opt/hctl/harness"); err != nil {
			return "", fmt.Errorf("cannot stage harness runtime: %w", err)
		}
		return filepath.ToSlash(abs), nil
	}
	final := "/opt/hctl/harness/bin/" + name
	if err := copyExecutable(abs, root, strings.TrimPrefix(final, "/")); err != nil {
		return "", fmt.Errorf("cannot stage harness executable: %w", err)
	}
	return final, nil
}

func copyTree(source, root, destination string) error {
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
			if err != nil || !within(source, readPath) {
				return errors.New("runtime symlink escapes its staged root")
			}
			info, err = os.Stat(readPath)
			if err != nil || info.IsDir() {
				return errors.New("runtime directory symlinks are not supported")
			}
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() > maxStagedFile {
			return errors.New("runtime entries must be bounded regular files")
		}
		data, err := os.ReadFile(readPath)
		if err != nil {
			return errors.New("cannot read runtime entry")
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		return rootfs.WriteAtomic(root, destination+"/"+filepath.ToSlash(relative), data, mode)
	})
}

func entrypointBytes(p *project.Project, harness string) []byte {
	return []byte("#!/bin/sh\nif [ \"$(id -u)\" != \"65532\" ] || [ \"$(id -g)\" != \"65532\" ]; then\n  echo 'hctl staged agents must run as uid 65532 gid 65532' >&2\n  exit 1\nfi\nexport HOME=" + finalHome + "\nexec " + finalHCTL + " run " + p.SourceRoot + " --workspace " + p.WorkspaceRoot + " --harness " + p.Harness + " --command " + harness + " \"$@\"\n")
}

func rejectBuildPaths(root string, request Request, prepared *project.Project) error {
	prohibited := [][]byte{[]byte(root), []byte(prepared.SourceRoot), []byte(prepared.WorkspaceRoot), []byte(request.Project.SourceRoot), []byte(request.Project.WorkspaceRoot)}
	return filepath.WalkDir(filepath.Join(root, "workspace"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect staged workspace")
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" && filepath.Ext(path) != ".toml" && filepath.Ext(path) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 8<<20 {
			return errors.New("cannot inspect staged configuration")
		}
		for _, value := range prohibited {
			if len(value) > 1 && bytes.Contains(data, value) {
				return errors.New("staged configuration contains a build-only path")
			}
		}
		return nil
	})
}

func collectFiles(root string) ([]ManifestFile, error) {
	files := []ManifestFile{}
	total := int64(0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect staged filesystem")
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxStagedFile {
			return errors.New("staged filesystem must contain bounded regular files without symlinks")
		}
		total += info.Size()
		if len(files) == maxStagedFiles || total > maxStagedBytes {
			return errors.New("staged filesystem exceeds artifact limits")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("cannot hash staged file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return errors.New("cannot describe staged file")
		}
		uid, gid := 0, 0
		if relative == "workspace" || strings.HasPrefix(relative, "workspace"+string(filepath.Separator)) {
			uid, gid = runtimeUID, runtimeGID
		}
		files = append(files, ManifestFile{Path: "/" + filepath.ToSlash(relative), SHA256: rootfs.SHA256(data), Mode: uint32(info.Mode().Perm()), UID: uid, GID: gid})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func collectDirectories(root string) ([]ManifestDirectory, error) {
	directories := []ManifestDirectory{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect staged directories")
		}
		if path == root || !entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("staged directory must not be a symlink")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return errors.New("cannot describe staged directory")
		}
		uid, gid := 0, 0
		if relative == "workspace" || strings.HasPrefix(relative, "workspace"+string(filepath.Separator)) || relative == "home/hctl" || strings.HasPrefix(relative, "home/hctl"+string(filepath.Separator)) {
			uid, gid = runtimeUID, runtimeGID
		}
		directories = append(directories, ManifestDirectory{Path: "/" + filepath.ToSlash(relative), Mode: uint32(info.Mode().Perm()), UID: uid, GID: gid})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(directories, func(i, j int) bool { return directories[i].Path < directories[j].Path })
	return directories, nil
}

func targetIdentity() TargetIdentity {
	abi := runtime.GOOS
	if runtime.GOOS == "linux" {
		abi = linuxABI()
	}
	return TargetIdentity{OS: runtime.GOOS, Architecture: runtime.GOARCH, ABI: abi, CompatibleBase: runtime.GOOS + "-" + runtime.GOARCH + "-" + abi}
}

func linuxABI() string {
	command := exec.Command("ldd", "--version")
	command.Env = secureenv.Child()
	output, err := command.CombinedOutput()
	if err == nil && bytes.Contains(bytes.ToLower(output), []byte("musl")) {
		return "musl"
	}
	if err == nil && (bytes.Contains(bytes.ToLower(output), []byte("glibc")) || bytes.Contains(bytes.ToLower(output), []byte("gnu libc"))) {
		return "glibc"
	}
	return "unknown-libc"
}
