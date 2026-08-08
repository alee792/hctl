package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStagedChannelAdapterBindsAgentAndExactExecutable(t *testing.T) {
	payload := []byte("#!/bin/sh\nexit 0\n")
	packageRoot := t.TempDir()
	writeJSON(t, filepath.Join(packageRoot, packageManifestName), channelAdapterFixtureDocument(t, payload))
	writeTestBytes(t, filepath.Join(packageRoot, "payload", "fixture-channel"), payload, 0o600)
	store := NewStore(t.TempDir(), nil)
	if _, err := store.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveChannelAdapter(context.Background(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	staged, err := store.StageArtifacts(context.Background(), resolved.Selection.PackageID, []string{resolved.Selection.Artifact.ID}, root)
	if err != nil || len(staged) != 1 {
		t.Fatalf("StageArtifacts() = %#v, %v", staged, err)
	}
	agentID := "agent@fixture"
	fingerprint := strings.Repeat("a", 64)
	data, err := EncodeStagedChannelAdapter(agentID, fingerprint, resolved, staged[0])
	if err != nil {
		t.Fatal(err)
	}
	descriptor := filepath.Join(root, "opt", "hctl", "integrations", "channel-adapter.json")
	writeTestBytes(t, descriptor, data, 0o444)
	loaded, err := LoadStagedChannelAdapter(descriptor, agentID, fingerprint, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	launch, err := loaded.LaunchDescriptor(ChannelAdapterRuntime, "")
	if err != nil || launch.ChannelKind != "fixture" || launch.PackageID != "fixture-channel-adapter" || !strings.HasSuffix(launch.Command, filepath.FromSlash("bin/fixture-channel")) {
		t.Fatalf("LaunchDescriptor() = %#v, %v", launch, err)
	}
	if _, err := LoadStagedChannelAdapter(descriptor, "other@agent", fingerprint, "fixture"); err == nil || !strings.Contains(err.Error(), "does not match this agent") {
		t.Fatalf("agent mismatch error = %v", err)
	}
	if _, err := LoadStagedChannelAdapter(descriptor, agentID, fingerprint, "discord"); err == nil || !strings.Contains(err.Error(), "does not match this agent") {
		t.Fatalf("channel mismatch error = %v", err)
	}
	if err := os.Chmod(loaded.Executable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loaded.Executable, []byte("#!/bin/sh\nexit 9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStagedChannelAdapter(descriptor, agentID, fingerprint, "fixture"); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt executable error = %v", err)
	}
}

func TestStagedChannelAdapterRejectsForeignPlatformProjection(t *testing.T) {
	descriptor := StagedChannelAdapterDescriptor{
		SchemaVersion: stagedChannelAdapterSchema,
		AgentID:       "agent@fixture", SourceFingerprint: strings.Repeat("a", 64),
		PackageID: "fixture", PackageVersion: "1.0.0", ManifestSHA256: strings.Repeat("b", 64),
		Capability:   ChannelAdapter{Type: ChannelAdapterType, Version: ChannelAdapterVersion, ID: "fixture", ChannelKind: "fixture", Artifacts: []string{"foreign"}, Executable: "bin/fixture", Runtime: ChannelAdapterCommand{Arguments: []string{"run"}}, Setup: ChannelAdapterCommand{Arguments: []string{"setup"}}, Status: ChannelAdapterCommand{Arguments: []string{"status"}}, Remove: ChannelAdapterCommand{Arguments: []string{"remove"}}, Protocol: ChannelAdapterProtocolRange{Minimum: 1, Before: 2}, ProfileSelector: ProfileOpaqueID, Features: []ChannelFeature{FeatureTyping}},
		Artifact:     Artifact{ID: "foreign", OS: OperatingSystem("linux"), Architecture: ArchitectureAMD64, Format: FormatBinary, Source: ArtifactSource{Kind: SourcePackage, Path: "payload/fixture"}, Size: 1, SHA256: strings.Repeat("c", 64), Executable: Executable{Path: "bin/fixture", Size: 1, SHA256: strings.Repeat("c", 64)}},
		ArtifactRoot: filepath.ToSlash(filepath.Join("fixture", strings.Repeat("b", 64), "foreign")),
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		descriptor.Artifact.OS = OSDarwin
		descriptor.Artifact.Architecture = ArchitectureARM64
	}
	if err := descriptor.validateMetadata(); err == nil || !strings.Contains(err.Error(), "does not support this platform") {
		t.Fatalf("foreign platform error = %v", err)
	}
}
