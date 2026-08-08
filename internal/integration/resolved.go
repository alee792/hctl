package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hctl/internal/rootfs"
)

// ResolvedPackage is offline, content-free evidence for one enabled exact
// installation. Capability consumers receive this value and ask only for the
// artifact ids in their own closed contract.
type ResolvedPackage struct {
	Package   Package
	State     InstallationState
	Artifacts []ResolvedArtifact
}

// ResolvedArtifact names one verified immutable prepared closure. Root and
// Executable are absolute local cache paths; neither is persisted in operator
// installation state.
type ResolvedArtifact struct {
	Artifact   Artifact
	Root       string
	Executable string
}

// NativeMCPResolution is the narrow native-mcp consumer result. It adds an
// exact verified executable path to #75's immutable selection metadata but
// owns no process, credential, proxy, policy, or harness behavior.
type NativeMCPResolution struct {
	Selection  NativeMCPSelection
	Root       string
	Executable string
}

// NativeMCPLaunchDescriptor is the credential-free process metadata a narrow
// native harness generator can consume. EnvironmentDefaults contains only
// literal manifest values; RequiredEnvironment contains names and descriptions
// but never reads or resolves ambient values.
type NativeMCPLaunchDescriptor struct {
	ServerName          string
	Command             string
	Arguments           []string
	WorkingDirectory    string
	EnvironmentDefaults map[string]string
	RequiredEnvironment []EnvironmentRequirement
	Target              NativeHarnessTarget
}

// StagedArtifact identifies one selectively copied closure beneath the
// canonical staged integration prefix.
type StagedArtifact struct {
	Artifact   Artifact
	Root       string
	Executable string
}

// Resolve verifies one enabled installation entirely from local state and
// cache. It never opens an install source or performs a network request.
func (s *Store) Resolve(ctx context.Context, id string) (ResolvedPackage, error) {
	var result ResolvedPackage
	err := s.locked(ctx, func() error {
		entry, found, err := s.loadInstalled(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("integration package %q is not installed; run hctl integration install", id)
		}
		if !entry.State.Enabled {
			return fmt.Errorf("integration package %q is disabled; run hctl integration enable %s", id, id)
		}
		compatible, err := entry.Package.Manifest().Compatibility.Contains(s.hctlVersion)
		if err != nil || !compatible {
			return fmt.Errorf("integration package %q is incompatible with hctl %s; install a compatible exact version", id, s.hctlVersion)
		}
		artifacts := make([]ResolvedArtifact, 0, len(entry.State.Artifacts))
		for _, identity := range entry.State.Artifacts {
			artifact, ok := artifactByID(entry.Package.Manifest(), identity.ID)
			if !ok || string(artifact.OS) != s.targetOS || string(artifact.Architecture) != s.targetArch {
				return fmt.Errorf("integration package %q has no verified artifact for %s/%s; update it from a compatible source", id, s.targetOS, s.targetArch)
			}
			if err := s.verifyCachedArtifact(artifact); err != nil {
				return fmt.Errorf("integration package %q cache is corrupt; reinstall or update it from the exact trusted source", id)
			}
			root := filepath.Join(s.root, "prepared", preparedArtifactKey(artifact))
			artifacts = append(artifacts, ResolvedArtifact{Artifact: artifact, Root: root, Executable: filepath.Join(root, filepath.FromSlash(artifact.Executable.Path))})
		}
		sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Artifact.ID < artifacts[j].Artifact.ID })
		result = ResolvedPackage{Package: entry.Package, State: cloneInstallationState(entry.State), Artifacts: artifacts}
		return nil
	})
	return result, err
}

// ResolveNativeMCP applies only the closed native-mcp v1 metadata selection
// after generic offline package resolution.
func (s *Store) ResolveNativeMCP(ctx context.Context, packageID, capabilityID string) (NativeMCPResolution, error) {
	resolved, err := s.Resolve(ctx, packageID)
	if err != nil {
		return NativeMCPResolution{}, err
	}
	selection, err := resolved.Package.SelectNativeMCP(capabilityID, s.targetOS, s.targetArch)
	if err != nil {
		return NativeMCPResolution{}, err
	}
	artifact, ok := resolved.Artifact(selection.Artifact.ID)
	if !ok {
		return NativeMCPResolution{}, fmt.Errorf("native-mcp capability %q artifact is not installed; update %s from a compatible exact source", capabilityID, packageID)
	}
	return NativeMCPResolution{Selection: selection, Root: artifact.Root, Executable: artifact.Executable}, nil
}

// Artifact returns one defensive artifact selection for a capability-specific
// consumer. The common store does not infer closure from capability type.
func (resolved ResolvedPackage) Artifact(id string) (ResolvedArtifact, bool) {
	for _, artifact := range resolved.Artifacts {
		if artifact.Artifact.ID == id {
			return artifact, true
		}
	}
	return ResolvedArtifact{}, false
}

// StageArtifacts copies only the exact artifact ids selected by a narrow
// capability consumer. The caller supplies a new staged filesystem root; this
// function adds no source, package, or network lookup.
func (s *Store) StageArtifacts(ctx context.Context, packageID string, artifactIDs []string, outputRoot string) ([]StagedArtifact, error) {
	if len(artifactIDs) == 0 || len(artifactIDs) > maxCapabilities {
		return nil, errors.New("integration staging requires a bounded non-empty artifact closure")
	}
	resolved, err := s.Resolve(ctx, packageID)
	if err != nil {
		return nil, err
	}
	root, err := rootfs.CanonicalDir(outputRoot)
	if err != nil {
		return nil, errors.New("integration staged filesystem root must be an existing real directory")
	}
	seen := map[string]bool{}
	result := make([]StagedArtifact, 0, len(artifactIDs))
	for _, id := range artifactIDs {
		if seen[id] {
			return nil, fmt.Errorf("integration staging artifact %q is duplicated", id)
		}
		seen[id] = true
		artifact, ok := resolved.Artifact(id)
		if !ok {
			return nil, fmt.Errorf("integration staging artifact %q is not in the verified installation", id)
		}
		relativeBase := filepath.ToSlash(filepath.Join("opt", "hctl", "integrations", packageID, resolved.Package.Identity(), id))
		if err := copyPreparedClosure(artifact.Root, root, relativeBase); err != nil {
			return nil, err
		}
		logicalRoot := "/" + relativeBase
		result = append(result, StagedArtifact{
			Artifact: artifact.Artifact, Root: logicalRoot,
			Executable: filepath.ToSlash(filepath.Join(logicalRoot, filepath.FromSlash(artifact.Artifact.Executable.Path))),
		})
	}
	return result, nil
}

func copyPreparedClosure(sourceRoot, outputRoot, relativeBase string) error {
	receiptData, err := rootfs.ReadSource(sourceRoot, preparedReceipt, maxManifestBytes)
	if err != nil {
		return errors.New("cannot read verified integration artifact receipt")
	}
	var receipt preparedArtifactReceipt
	if err := decodeStrict(receiptData, &receipt); err != nil {
		return errors.New("cannot decode verified integration artifact receipt")
	}
	for _, file := range receipt.Files {
		if strings.HasPrefix(file.Path, "/") {
			return errors.New("verified integration artifact contains an invalid path")
		}
		data, mode, found, err := rootfs.ReadOptional(sourceRoot, file.Path, file.Size)
		if err != nil || !found || int64(len(data)) != file.Size || rootfs.SHA256(data) != file.SHA256 || uint32(mode.Perm()) != file.Mode {
			return errors.New("verified integration artifact changed during staging")
		}
		destination := relativeBase + "/" + file.Path
		if err := rootfs.WriteAtomicExclusive(outputRoot, destination, data, mode.Perm()); err != nil {
			return fmt.Errorf("cannot stage integration artifact file %q", file.Path)
		}
	}
	return nil
}

// Ensure the paths returned by Resolve remain regular executable files at the
// boundary where a capability consumer is about to hand them to a process or
// native configuration generator.
func (resolved NativeMCPResolution) ValidateExecutable() error {
	info, err := os.Lstat(resolved.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return errors.New("resolved native-mcp executable is missing or unsafe; verify and reinstall the package")
	}
	if !strings.HasPrefix(resolved.Executable, resolved.Root+string(filepath.Separator)) {
		return errors.New("resolved native-mcp executable escapes its immutable artifact root")
	}
	return nil
}

// LaunchDescriptor derives one harness-targeted descriptor entirely from an
// offline verified resolution. It does not read the environment, start a
// process, or write native Claude or Codex configuration.
func (resolved NativeMCPResolution) LaunchDescriptor(harness string) (NativeMCPLaunchDescriptor, error) {
	if err := resolved.ValidateExecutable(); err != nil {
		return NativeMCPLaunchDescriptor{}, err
	}
	var target NativeHarnessTarget
	found := false
	for _, candidate := range resolved.Selection.Capability.Harnesses {
		if candidate.Name == harness {
			target = candidate
			found = true
			break
		}
	}
	if !found {
		return NativeMCPLaunchDescriptor{}, fmt.Errorf("native-mcp capability %q does not support harness %q", resolved.Selection.Capability.ID, harness)
	}
	workingDirectory := filepath.Join(resolved.Root, filepath.FromSlash(resolved.Selection.Capability.WorkingDirectory))
	canonicalRoot, err := rootfs.CanonicalDir(resolved.Root)
	if err != nil {
		return NativeMCPLaunchDescriptor{}, errors.New("resolved native-mcp artifact root is missing or unsafe; verify and reinstall the package")
	}
	canonicalWorkingDirectory, err := rootfs.CanonicalDir(workingDirectory)
	if err != nil {
		return NativeMCPLaunchDescriptor{}, errors.New("resolved native-mcp working directory is missing or unsafe; verify and reinstall the package")
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalWorkingDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return NativeMCPLaunchDescriptor{}, errors.New("resolved native-mcp working directory escapes its immutable artifact root")
	}
	environment := make(map[string]string, len(resolved.Selection.Capability.Environment))
	for name, value := range resolved.Selection.Capability.Environment {
		environment[name] = value
	}
	return NativeMCPLaunchDescriptor{
		ServerName:          resolved.Selection.Capability.ServerName,
		Command:             resolved.Executable,
		Arguments:           append([]string(nil), resolved.Selection.Capability.Arguments...),
		WorkingDirectory:    canonicalWorkingDirectory,
		EnvironmentDefaults: environment,
		RequiredEnvironment: append([]EnvironmentRequirement(nil), resolved.Selection.Capability.RequiredEnvironment...),
		Target:              target,
	}, nil
}
