package integration

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOfficialDiscordPackageInstallsAndStagesThroughSharedStore(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the separate Discord adapter artifact")
	}
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(t.TempDir(), "package")
	target := runtime.GOOS + "-" + runtime.GOARCH
	command := exec.Command("sh", filepath.Join(repository, "discordadapter", "build-package.sh"), "--version", "0.1.0", "--revision", "test-fixture", "--target", target, "--output", packageRoot)
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Discord package: %v\n%s", err, output)
	}
	repeatRoot := filepath.Join(t.TempDir(), "package")
	repeat := exec.Command("sh", filepath.Join(repository, "discordadapter", "build-package.sh"), "--version", "0.1.0", "--revision", "test-fixture", "--target", target, "--output", repeatRoot)
	repeat.Env = append(os.Environ(), "GOWORK=off")
	if output, err := repeat.CombinedOutput(); err != nil {
		t.Fatalf("repeat Discord package build: %v\n%s", err, output)
	}
	for _, name := range []string{"integration.json", "payload/hctl-discord", "licenses/THIRD_PARTY_LICENSES.md"} {
		first, firstErr := os.ReadFile(filepath.Join(packageRoot, filepath.FromSlash(name)))
		second, secondErr := os.ReadFile(filepath.Join(repeatRoot, filepath.FromSlash(name)))
		if firstErr != nil || secondErr != nil || !bytes.Equal(first, second) {
			t.Fatalf("Discord package build is not reproducible for %s: %v, %v", name, firstErr, secondErr)
		}
	}
	store := NewStore(t.TempDir(), nil)
	installed, err := store.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator})
	if err != nil {
		t.Fatalf("install Discord package: %v", err)
	}
	if installed.State.PackageID != "hctl-discord" || len(installed.State.Capabilities) != 1 || installed.State.Capabilities[0] != (InstalledCapabilityIdentity{ID: "discord", Type: ChannelAdapterType, Version: ChannelAdapterVersion}) {
		t.Fatalf("installed Discord identity = %#v", installed.State)
	}
	resolved, err := store.Resolve(context.Background(), "hctl-discord")
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolved.Package.SelectChannelAdapter("discord", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Capability.ChannelKind != "discord" || selection.Capability.Executable != "bin/hctl-discord" || len(selection.Capability.Features) != 7 {
		t.Fatalf("Discord capability = %#v", selection.Capability)
	}
	stageRoot := t.TempDir()
	staged, err := store.StageArtifacts(context.Background(), "hctl-discord", selection.Capability.Artifacts, stageRoot)
	if err != nil || len(staged) != 1 {
		t.Fatalf("stage Discord package = %#v, %v", staged, err)
	}
	if info, err := os.Stat(filepath.Join(stageRoot, filepath.FromSlash(staged[0].Executable[1:]))); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("staged Discord executable: %v", err)
	}
}
