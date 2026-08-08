package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"hctl/internal/rootfs"
)

const (
	// StagedChannelAdapterEnvironment points a staged hctl process at the
	// immutable, non-secret adapter descriptor included in its agent closure.
	StagedChannelAdapterEnvironment = "HCTL_CHANNEL_ADAPTER_DESCRIPTOR"
	stagedChannelAdapterSchema      = 1
	maxStagedAdapterDescriptorBytes = 64 << 10
)

// StagedChannelAdapterDescriptor is the narrow runtime projection of one
// operator-trusted channel-adapter package. It contains no credential, profile,
// workspace path, or mutable installation state.
type StagedChannelAdapterDescriptor struct {
	SchemaVersion     int            `json:"schema_version"`
	AgentID           string         `json:"agent_id"`
	SourceFingerprint string         `json:"source_fingerprint"`
	PackageID         string         `json:"package_id"`
	PackageVersion    string         `json:"package_version"`
	ManifestSHA256    string         `json:"manifest_sha256"`
	Capability        ChannelAdapter `json:"capability"`
	Artifact          Artifact       `json:"artifact"`
	ArtifactRoot      string         `json:"artifact_root"`
}

// EncodeStagedChannelAdapter binds a selected staged artifact to one agent.
// The artifact root is relative to the descriptor, so the same bytes work at
// their physical build path and the canonical /opt/hctl runtime path.
func EncodeStagedChannelAdapter(agentID, sourceFingerprint string, resolved ChannelAdapterResolution, staged StagedArtifact) ([]byte, error) {
	if !validStagedIdentity(agentID, 512) || !checksumPattern.MatchString(sourceFingerprint) {
		return nil, errors.New("staged channel-adapter agent identity is invalid")
	}
	if resolved.Selection.Artifact.ID != staged.Artifact.ID || resolved.Selection.Artifact.SHA256 != staged.Artifact.SHA256 {
		return nil, errors.New("staged channel-adapter artifact does not match its selection")
	}
	prefix := "/opt/hctl/integrations/"
	logicalRoot := filepath.ToSlash(filepath.Clean(staged.Root))
	if !strings.HasPrefix(logicalRoot, prefix) {
		return nil, errors.New("staged channel-adapter artifact root is not canonical")
	}
	relativeRoot := strings.TrimPrefix(logicalRoot, prefix)
	if _, err := rootfs.CleanRelative(relativeRoot); err != nil {
		return nil, errors.New("staged channel-adapter artifact root is invalid")
	}
	capability := cloneChannelAdapter(resolved.Selection.Capability)
	// The staged projection contains one current-platform artifact, even when
	// the source manifest advertises other platform artifacts.
	capability.Artifacts = []string{staged.Artifact.ID}
	descriptor := StagedChannelAdapterDescriptor{
		SchemaVersion: stagedChannelAdapterSchema, AgentID: agentID, SourceFingerprint: sourceFingerprint,
		PackageID: resolved.Selection.PackageID, PackageVersion: resolved.Selection.PackageVersion,
		ManifestSHA256: resolved.Selection.ManifestSHA256, Capability: capability,
		Artifact: staged.Artifact, ArtifactRoot: relativeRoot,
	}
	if err := descriptor.validateMetadata(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return nil, errors.New("cannot encode staged channel-adapter descriptor")
	}
	return append(data, '\n'), nil
}

// LoadStagedChannelAdapter validates an immutable staged descriptor and its
// exact executable before returning the normal narrow process resolution.
func LoadStagedChannelAdapter(path, agentID, sourceFingerprint, channelKind string) (ChannelAdapterResolution, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxStagedAdapterDescriptorBytes || info.Mode().Perm()&0o022 != 0 {
		return ChannelAdapterResolution{}, errors.New("staged channel-adapter descriptor is missing or unsafe; rebuild the staged agent")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ChannelAdapterResolution{}, errors.New("cannot read staged channel-adapter descriptor; rebuild the staged agent")
	}
	var descriptor StagedChannelAdapterDescriptor
	if err := decodeStrict(data, &descriptor); err != nil {
		return ChannelAdapterResolution{}, errors.New("staged channel-adapter descriptor is invalid; rebuild the staged agent")
	}
	if descriptor.AgentID != agentID || descriptor.SourceFingerprint != sourceFingerprint || descriptor.Capability.ChannelKind != channelKind {
		return ChannelAdapterResolution{}, errors.New("staged channel-adapter descriptor does not match this agent; rebuild the staged agent")
	}
	if err := descriptor.validateMetadata(); err != nil {
		return ChannelAdapterResolution{}, fmt.Errorf("staged channel-adapter descriptor is invalid; rebuild the staged agent: %w", err)
	}
	descriptorDirectory, err := rootfs.CanonicalDir(filepath.Dir(path))
	if err != nil {
		return ChannelAdapterResolution{}, errors.New("staged channel-adapter descriptor directory is unsafe; rebuild the staged agent")
	}
	root := filepath.Join(descriptorDirectory, filepath.FromSlash(descriptor.ArtifactRoot))
	executable := filepath.Join(root, filepath.FromSlash(descriptor.Artifact.Executable.Path))
	content, mode, found, err := rootfs.ReadOptional(root, descriptor.Artifact.Executable.Path, descriptor.Artifact.Executable.Size)
	if err != nil || !found || mode.Perm()&0o111 == 0 || rootfs.SHA256(content) != descriptor.Artifact.Executable.SHA256 {
		return ChannelAdapterResolution{}, errors.New("staged channel-adapter executable is corrupt; rebuild the staged agent")
	}
	selection := ChannelAdapterSelection{
		PackageID: descriptor.PackageID, PackageVersion: descriptor.PackageVersion,
		ManifestSHA256: descriptor.ManifestSHA256, Capability: cloneChannelAdapter(descriptor.Capability), Artifact: descriptor.Artifact,
	}
	resolved := ChannelAdapterResolution{Selection: selection, Root: root, Executable: executable}
	if err := resolved.ValidateExecutable(); err != nil {
		return ChannelAdapterResolution{}, errors.New("staged channel-adapter executable is unsafe; rebuild the staged agent")
	}
	return resolved, nil
}

func (descriptor StagedChannelAdapterDescriptor) validateMetadata() error {
	if descriptor.SchemaVersion != stagedChannelAdapterSchema || !validStagedIdentity(descriptor.AgentID, 512) || !checksumPattern.MatchString(descriptor.SourceFingerprint) {
		return errors.New("staged channel-adapter identity is invalid")
	}
	if !packageIDPattern.MatchString(descriptor.PackageID) || len(descriptor.PackageID) > 128 || descriptor.PackageVersion == "" || !checksumPattern.MatchString(descriptor.ManifestSHA256) {
		return errors.New("staged channel-adapter package identity is invalid")
	}
	if err := descriptor.Artifact.Validate(); err != nil {
		return err
	}
	if string(descriptor.Artifact.OS) != runtime.GOOS || string(descriptor.Artifact.Architecture) != runtime.GOARCH {
		return errors.New("staged channel-adapter artifact does not support this platform")
	}
	if len(descriptor.Capability.Artifacts) != 1 || descriptor.Capability.Artifacts[0] != descriptor.Artifact.ID {
		return errors.New("staged channel-adapter capability does not select its artifact")
	}
	if err := descriptor.Capability.Validate(map[string]Artifact{descriptor.Artifact.ID: descriptor.Artifact}); err != nil {
		return err
	}
	if _, err := rootfs.CleanRelative(descriptor.ArtifactRoot); err != nil {
		return errors.New("staged channel-adapter artifact root is invalid")
	}
	wantPrefix := filepath.ToSlash(filepath.Join(descriptor.PackageID, descriptor.ManifestSHA256, descriptor.Artifact.ID))
	if descriptor.ArtifactRoot != wantPrefix {
		return errors.New("staged channel-adapter artifact root does not match its package identity")
	}
	return nil
}

func validStagedIdentity(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	return !bytes.ContainsAny([]byte(value), "\x00\r\n")
}
