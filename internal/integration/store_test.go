package integration

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStoreLocalInstallResolveSelectiveStageAndLifecycle(t *testing.T) {
	packageRoot := t.TempDir()
	first := []byte("#!/bin/sh\necho first\n")
	second := []byte("#!/bin/sh\necho second\n")
	writePackageFixture(t, packageRoot, fixtureOptions{
		Version: "1.0.0",
		Artifacts: []fixtureArtifact{
			{ID: "primary", PayloadPath: "payload/primary", ExecutablePath: "bin/primary", Data: first},
			{ID: "secondary", PayloadPath: "payload/secondary", ExecutablePath: "bin/secondary", Data: second},
		},
	})
	store := NewStore(t.TempDir(), nil)
	ctx := context.Background()
	installed, err := store.Install(ctx, InstallOptions{Source: packageRoot, Trust: TrustOperator})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if installed.State.PackageID != "fixture-package" || !installed.State.Enabled || len(installed.State.Artifacts) != 2 || len(installed.State.Capabilities) != 2 {
		t.Fatalf("installed = %#v", installed)
	}
	resolved, err := store.Resolve(ctx, "fixture-package")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(resolved.Artifacts) != 2 {
		t.Fatalf("resolved artifacts = %#v", resolved.Artifacts)
	}
	if err := store.RecordConsumption(ctx, "fixture-package", "sample-agent@0123456789abcdef", "sample-agent", []string{"primary"}); err != nil {
		t.Fatalf("RecordConsumption() error = %v", err)
	}
	consumers, err := store.Consumers(ctx, "fixture-package")
	if err != nil || len(consumers) != 1 || consumers[0].AgentName != "sample-agent" || len(consumers[0].Capabilities) != 1 {
		t.Fatalf("Consumers() = %#v, %v", consumers, err)
	}
	native, err := store.ResolveNativeMCP(ctx, "fixture-package", "primary")
	if err != nil {
		t.Fatalf("ResolveNativeMCP() error = %v", err)
	}
	if err := native.ValidateExecutable(); err != nil {
		t.Fatalf("ValidateExecutable() error = %v", err)
	}
	if data, err := os.ReadFile(native.Executable); err != nil || !bytes.Equal(data, first) {
		t.Fatalf("resolved executable = %q, %v", data, err)
	}

	stageRoot := t.TempDir()
	staged, err := store.StageArtifacts(ctx, "fixture-package", []string{"primary"}, stageRoot)
	if err != nil {
		t.Fatalf("StageArtifacts() error = %v", err)
	}
	if len(staged) != 1 || staged[0].Executable == "" || !strings.HasPrefix(staged[0].Root, "/opt/hctl/integrations/fixture-package/") {
		t.Fatalf("staged = %#v", staged)
	}
	physicalPrimary := filepath.Join(stageRoot, filepath.FromSlash(strings.TrimPrefix(staged[0].Executable, "/")))
	if data, err := os.ReadFile(physicalPrimary); err != nil || !bytes.Equal(data, first) {
		t.Fatalf("staged primary = %q, %v", data, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(stageRoot, "opt", "hctl", "integrations", "fixture-package", "*", "secondary")); len(matches) != 0 {
		t.Fatalf("unselected artifact was staged: %v", matches)
	}

	if err := store.SetEnabled(ctx, "fixture-package", false); err != nil {
		t.Fatalf("disable error = %v", err)
	}
	if _, err := store.Resolve(ctx, "fixture-package"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled resolve error = %v", err)
	}
	if err := store.SetEnabled(ctx, "fixture-package", true); err != nil {
		t.Fatalf("enable error = %v", err)
	}
	if _, err := store.Verify(ctx, "fixture-package"); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if err := store.Remove(ctx, "fixture-package"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := store.Inspect(ctx, "fixture-package"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("removed inspect error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.root, "blobs", digestHex(first))); err != nil {
		t.Fatalf("shared blob removed with metadata: %v", err)
	}
}

func TestNativeMCPConsumerResolvesLaunchesAndStagesOffline(t *testing.T) {
	parent := t.TempDir()
	packageRoot := filepath.Join(parent, "source")
	if err := os.Mkdir(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	primary := []byte("#!/bin/sh\nprintf '%s:%s' \"$1\" \"$FIXTURE_MODE\"\n")
	secondary := []byte("#!/bin/sh\nexit 0\n")
	document := fixtureDocument(fixtureOptions{Artifacts: []fixtureArtifact{
		{ID: "primary", PayloadPath: "payload/primary", ExecutablePath: "bin/primary", Data: primary},
		{ID: "secondary", PayloadPath: "payload/secondary", ExecutablePath: "bin/secondary", Data: secondary},
	}})
	capability := document["capabilities"].([]any)[0].(map[string]any)
	capability["arguments"] = []string{"launch-descriptor"}
	capability["environment"] = map[string]string{"FIXTURE_MODE": "offline"}
	writeJSON(t, filepath.Join(packageRoot, packageManifestName), document)
	writeTestBytes(t, filepath.Join(packageRoot, "payload", "primary"), primary, 0o600)
	writeTestBytes(t, filepath.Join(packageRoot, "payload", "secondary"), secondary, 0o600)

	store := NewStore(t.TempDir(), nil)
	if _, err := store.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator}); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if err := os.Rename(packageRoot, filepath.Join(parent, "source-unavailable")); err != nil {
		t.Fatal(err)
	}

	resolved, err := store.ResolveNativeMCP(context.Background(), "fixture-package", "primary")
	if err != nil {
		t.Fatalf("offline ResolveNativeMCP() error = %v", err)
	}
	descriptor, err := resolved.LaunchDescriptor("codex")
	if err != nil {
		t.Fatalf("LaunchDescriptor() error = %v", err)
	}
	if descriptor.Command != resolved.Executable || descriptor.ServerName != "primary" || descriptor.Target.Startup != StartupOptional || descriptor.Target.Trust != TrustNativeProject {
		t.Fatalf("launch descriptor = %#v", descriptor)
	}
	command := exec.Command(descriptor.Command, descriptor.Arguments...)
	command.Dir = descriptor.WorkingDirectory
	for name, value := range descriptor.EnvironmentDefaults {
		command.Env = append(command.Env, name+"="+value)
	}
	output, err := command.Output()
	if err != nil || string(output) != "launch-descriptor:offline" {
		t.Fatalf("fake native launch = %q, %v", output, err)
	}

	stageRoot := t.TempDir()
	staged, err := store.StageArtifacts(context.Background(), "fixture-package", []string{"primary"}, stageRoot)
	if err != nil {
		t.Fatalf("offline StageArtifacts() error = %v", err)
	}
	if len(staged) != 1 || staged[0].Artifact.ID != "primary" {
		t.Fatalf("staged = %#v", staged)
	}
	physicalExecutable := filepath.Join(stageRoot, filepath.FromSlash(strings.TrimPrefix(staged[0].Executable, "/")))
	if data, err := os.ReadFile(physicalExecutable); err != nil || !bytes.Equal(data, primary) {
		t.Fatalf("staged executable = %q, %v", data, err)
	}
	secondaryPattern := filepath.Join(stageRoot, "opt", "hctl", "integrations", "fixture-package", "*", "secondary")
	if matches, _ := filepath.Glob(secondaryPattern); len(matches) != 0 {
		t.Fatalf("unselected closure was staged: %v", matches)
	}
}

func TestStoreInstallsChannelAdapterThroughSharedEnvelope(t *testing.T) {
	payload := []byte("#!/bin/sh\nexit 0\n")
	packageRoot := t.TempDir()
	document := channelAdapterFixtureDocument(t, payload)
	writeJSON(t, filepath.Join(packageRoot, packageManifestName), document)
	writeTestBytes(t, filepath.Join(packageRoot, "payload", "fixture-channel"), payload, 0o600)

	store := NewStore(t.TempDir(), nil)
	installed, err := store.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator})
	if err != nil {
		t.Fatalf("channel-adapter Install() error = %v", err)
	}
	if len(installed.State.Capabilities) != 1 || installed.State.Capabilities[0].Type != ChannelAdapterType || installed.State.Capabilities[0].Version != ChannelAdapterVersion {
		t.Fatalf("installed channel capability = %#v", installed.State.Capabilities)
	}
	resolved, err := store.Resolve(context.Background(), "fixture-channel-adapter")
	if err != nil {
		t.Fatalf("channel-adapter Resolve() error = %v", err)
	}
	selection, err := resolved.Package.SelectChannelAdapter("fixture", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("SelectChannelAdapter() error = %v", err)
	}
	artifact, found := resolved.Artifact(selection.Artifact.ID)
	if !found || artifact.Executable == "" {
		t.Fatalf("resolved channel artifact = %#v, %t", artifact, found)
	}
	staged, err := store.StageArtifacts(context.Background(), "fixture-channel-adapter", selection.Capability.Artifacts, t.TempDir())
	if err != nil || len(staged) != 1 {
		t.Fatalf("channel-adapter StageArtifacts() = %#v, %v", staged, err)
	}
}

func TestStorePinnedHTTPSOfflineReuse(t *testing.T) {
	payload := []byte("#!/bin/sh\necho remote\n")
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		_, _ = response.Write(payload)
	}))
	packageRoot := t.TempDir()
	writePackageFixture(t, packageRoot, fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", URL: server.URL, ExecutablePath: "bin/server", Data: payload}}})
	store := NewStore(t.TempDir(), server.Client())
	if _, err := store.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator}); err != nil {
		t.Fatalf("remote Install() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("remote calls = %d", calls.Load())
	}
	if err := store.Remove(context.Background(), "fixture-package"); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if _, err := store.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator}); err != nil {
		t.Fatalf("offline cached Install() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("offline install made another request: %d", calls.Load())
	}
	if _, err := store.Resolve(context.Background(), "fixture-package"); err != nil {
		t.Fatalf("offline Resolve() error = %v", err)
	}
}

func TestStoreConcurrentInstallPublishesOneExactEntry(t *testing.T) {
	packageRoot := t.TempDir()
	payload := []byte("concurrent fixture")
	writePackageFixture(t, packageRoot, fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: payload}}})
	storeRoot := t.TempDir()
	store := NewStore(storeRoot, nil)
	const workers = 8
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := NewStore(storeRoot, nil).Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator})
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Install() error = %v", err)
		}
	}
	entries, err := store.List(context.Background())
	if err != nil || len(entries) != 1 {
		t.Fatalf("List() = %#v, %v", entries, err)
	}
	prepared, err := os.ReadDir(filepath.Join(storeRoot, "prepared"))
	if err != nil || len(prepared) != 1 {
		t.Fatalf("prepared cache = %#v, %v", prepared, err)
	}
}

func TestStorePreparedCacheIncludesTransformationIdentity(t *testing.T) {
	payload := []byte("identical raw binary")
	firstRoot := t.TempDir()
	writePackageFixture(t, firstRoot, fixtureOptions{ID: "fixture-first", Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/first", Data: payload}}})
	secondRoot := t.TempDir()
	writePackageFixture(t, secondRoot, fixtureOptions{ID: "fixture-second", Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/second", Data: payload}}})
	store := NewStore(t.TempDir(), nil)
	ctx := context.Background()
	if _, err := store.Install(ctx, InstallOptions{Source: firstRoot, Trust: TrustOperator}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(ctx, InstallOptions{Source: secondRoot, Trust: TrustOperator}); err != nil {
		t.Fatal(err)
	}
	first, err := store.Resolve(ctx, "fixture-first")
	if err != nil {
		t.Fatalf("first Resolve() error = %v", err)
	}
	second, err := store.Resolve(ctx, "fixture-second")
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if first.Artifacts[0].Root == second.Artifacts[0].Root {
		t.Fatalf("different executable transformations share prepared root %q", first.Artifacts[0].Root)
	}
	if filepath.Base(first.Artifacts[0].Executable) != "first" || filepath.Base(second.Artifacts[0].Executable) != "second" {
		t.Fatalf("resolved executables = %q, %q", first.Artifacts[0].Executable, second.Artifacts[0].Executable)
	}
	prepared, err := os.ReadDir(filepath.Join(store.root, "prepared"))
	if err != nil || len(prepared) != 2 {
		t.Fatalf("prepared transformations = %#v, %v", prepared, err)
	}
	blobs, err := os.ReadDir(filepath.Join(store.root, "blobs"))
	if err != nil || len(blobs) != 1 {
		t.Fatalf("shared raw blobs = %#v, %v", blobs, err)
	}
}

func TestStoreInterruptedFetchDoesNotPublishInstallation(t *testing.T) {
	payload := []byte("never delivered")
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer server.Close()
	packageRoot := t.TempDir()
	writePackageFixture(t, packageRoot, fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", URL: server.URL, ExecutablePath: "bin/server", Data: payload}}})
	store := NewStore(t.TempDir(), server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := store.Install(ctx, InstallOptions{Source: packageRoot, Trust: TrustOperator})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("interrupted fetch succeeded")
	}
	if _, err := store.Inspect(context.Background(), "fixture-package"); err == nil {
		t.Fatal("interrupted fetch published installation state")
	}
}

func TestStoreUpdateRequiresExactIntentAndPreservesOldCache(t *testing.T) {
	firstRoot := t.TempDir()
	first := []byte("first version")
	writePackageFixture(t, firstRoot, fixtureOptions{Version: "1.0.0", Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: first}}})
	secondRoot := t.TempDir()
	second := []byte("second version")
	writePackageFixture(t, secondRoot, fixtureOptions{Version: "1.1.0", Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: second}}})
	store := NewStore(t.TempDir(), nil)
	if _, err := store.Install(context.Background(), InstallOptions{Source: firstRoot, Trust: TrustOperator}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(context.Background(), InstallOptions{Source: secondRoot, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "use update") {
		t.Fatalf("implicit drift error = %v", err)
	}
	updated, err := store.Install(context.Background(), InstallOptions{Source: secondRoot, Trust: TrustOperator, Update: "fixture-package"})
	if err != nil {
		t.Fatalf("update error = %v", err)
	}
	if updated.State.PackageVersion != "1.1.0" {
		t.Fatalf("updated state = %#v", updated.State)
	}
	for _, payload := range [][]byte{first, second} {
		if _, err := os.Stat(filepath.Join(store.root, "blobs", digestHex(payload))); err != nil {
			t.Fatalf("immutable cache %s missing: %v", digestHex(payload), err)
		}
	}
}

func TestStoreRemoveRefusesSymlinkedConsumptionDirectory(t *testing.T) {
	packageRoot := t.TempDir()
	payload := []byte("consumer cleanup fixture")
	writePackageFixture(t, packageRoot, fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: payload}}})
	store := NewStore(t.TempDir(), nil)
	ctx := context.Background()
	if _, err := store.Install(ctx, InstallOptions{Source: packageRoot, Trust: TrustOperator}); err != nil {
		t.Fatal(err)
	}
	agentID := "sample-agent@0123456789abcdef"
	if err := store.RecordConsumption(ctx, "fixture-package", agentID, "sample-agent", []string{"primary"}); err != nil {
		t.Fatal(err)
	}
	consumerDirectory := filepath.Join(store.root, "consumers", "fixture-package")
	if err := os.Remove(filepath.Join(consumerDirectory, consumerFilename(agentID))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(consumerDirectory); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	externalReceipt := filepath.Join(external, consumerFilename(agentID))
	writeTestBytes(t, externalReceipt, []byte("must remain"), 0o600)
	if err := os.Symlink(external, consumerDirectory); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, "fixture-package"); err == nil || !strings.Contains(err.Error(), "unsafe integration consumption") {
		t.Fatalf("Remove() error = %v", err)
	}
	if data, err := os.ReadFile(externalReceipt); err != nil || string(data) != "must remain" {
		t.Fatalf("external receipt was changed: %q, %v", data, err)
	}
	if _, err := store.Inspect(ctx, "fixture-package"); err != nil {
		t.Fatalf("failed removal retired installation state: %v", err)
	}
}

func TestStoreArchiveSourcesAndPreparedArchiveClosure(t *testing.T) {
	executable := []byte("#!/bin/sh\necho archive\n")
	artifactArchive := tarGZ(t, map[string]archiveEntry{
		"bin/server": {Data: executable, Mode: 0o755},
		"lib/data":   {Data: []byte("runtime dependency"), Mode: 0o644},
	})
	directory := t.TempDir()
	manifest := writePackageFixture(t, directory, fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/runtime.tar.gz", ExecutablePath: "bin/server", Data: artifactArchive, Format: "tar.gz", ExecutableData: executable}}})
	packageArchive := filepath.Join(t.TempDir(), "package.zip")
	zipFile(t, packageArchive, map[string][]byte{packageManifestName: manifest, "payload/runtime.tar.gz": artifactArchive})
	store := NewStore(t.TempDir(), nil)
	if _, err := store.Install(context.Background(), InstallOptions{Source: packageArchive, Trust: TrustOperator}); err != nil {
		t.Fatalf("archive Install() error = %v", err)
	}
	stageRoot := t.TempDir()
	staged, err := store.StageArtifacts(context.Background(), "fixture-package", []string{"primary"}, stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	dependency := filepath.Join(stageRoot, filepath.FromSlash(strings.TrimPrefix(staged[0].Root, "/")), "lib", "data")
	if data, err := os.ReadFile(dependency); err != nil || string(data) != "runtime dependency" {
		t.Fatalf("staged dependency = %q, %v", data, err)
	}
}

func TestStoreRejectsTrustMismatchCancellationCorruptionAndUnsafeSources(t *testing.T) {
	payload := []byte("safe payload")
	makePackage := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		writePackageFixture(t, root, fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: payload}}})
		return root
	}
	t.Run("trust", func(t *testing.T) {
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: makePackage(t)}); err == nil || !strings.Contains(err.Error(), "--trust operator") {
			t.Fatalf("trust error = %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		store := NewStore(t.TempDir(), nil)
		if _, err := store.Install(ctx, InstallOptions{Source: makePackage(t), Trust: TrustOperator}); err == nil {
			t.Fatal("canceled install succeeded")
		}
		if _, err := store.Inspect(context.Background(), "fixture-package"); err == nil {
			t.Fatal("canceled install published state")
		}
	})
	t.Run("corruption", func(t *testing.T) {
		store := NewStore(t.TempDir(), nil)
		if _, err := store.Install(context.Background(), InstallOptions{Source: makePackage(t), Trust: TrustOperator}); err != nil {
			t.Fatal(err)
		}
		resolved, _ := store.Resolve(context.Background(), "fixture-package")
		path := resolved.Artifacts[0].Executable
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("changed"), 0o500); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Verify(context.Background(), "fixture-package"); err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("corruption error = %v", err)
		}
	})
	t.Run("world writable", func(t *testing.T) {
		root := makePackage(t)
		path := filepath.Join(root, "payload", "server")
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: root, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "writable by another user") {
			t.Fatalf("ownership error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := makePackage(t)
		path := filepath.Join(root, "payload", "server")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(packageManifestName, path); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: root, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "symlinked") {
			t.Fatalf("symlink error = %v", err)
		}
	})
	t.Run("archive escape", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "bad.zip")
		zipFile(t, archive, map[string][]byte{"../escape": []byte("bad")})
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: archive, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "unsafe path") {
			t.Fatalf("escape error = %v", err)
		}
	})
}

func TestStoreRejectsMismatchAndUnsupportedCapabilityBeforePayloadAccess(t *testing.T) {
	t.Run("checksum", func(t *testing.T) {
		root := t.TempDir()
		writePackageFixture(t, root, fixtureOptions{OverrideSHA: strings.Repeat("0", 64), Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: []byte("payload")}}})
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: root, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("checksum error = %v", err)
		}
	})
	t.Run("compatibility", func(t *testing.T) {
		root := t.TempDir()
		writePackageFixture(t, root, fixtureOptions{Minimum: "9.0.0", Before: "10.0.0", Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: []byte("payload")}}})
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: root, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "does not support hctl") {
			t.Fatalf("compatibility error = %v", err)
		}
	})
	t.Run("platform", func(t *testing.T) {
		root := t.TempDir()
		document := fixtureDocument(fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/server", ExecutablePath: "bin/server", Data: []byte("payload")}}})
		artifacts := document["artifacts"].([]any)
		artifact := artifacts[0].(map[string]any)
		if runtime.GOOS == "darwin" {
			artifact["os"] = "linux"
		} else {
			artifact["os"] = "darwin"
		}
		writeJSON(t, filepath.Join(root, packageManifestName), document)
		writeTestBytes(t, filepath.Join(root, "payload", "server"), []byte("payload"), 0o600)
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: root, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Fatalf("platform error = %v", err)
		}
	})
	t.Run("prepared executable", func(t *testing.T) {
		executable := []byte("actual executable")
		archive := tarGZ(t, map[string]archiveEntry{"bin/server": {Data: executable, Mode: 0o755}})
		root := t.TempDir()
		writePackageFixture(t, root, fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/runtime.tar.gz", ExecutablePath: "bin/server", Data: archive, Format: "tar.gz", ExecutableData: bytes.Repeat([]byte{'x'}, len(executable))}}})
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: root, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "executable does not match") {
			t.Fatalf("executable error = %v", err)
		}
	})
	t.Run("capability", func(t *testing.T) {
		root := t.TempDir()
		manifest := fixtureDocument(fixtureOptions{Artifacts: []fixtureArtifact{{ID: "primary", PayloadPath: "payload/missing", ExecutablePath: "bin/server", Data: []byte("payload")}}})
		manifest["capabilities"] = []any{map[string]any{"type": "channel-adapter", "version": 2, "id": "future"}}
		writeJSON(t, filepath.Join(root, packageManifestName), manifest)
		if _, err := NewStore(t.TempDir(), nil).Install(context.Background(), InstallOptions{Source: root, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "channel-adapter") {
			t.Fatalf("unsupported capability error = %v", err)
		}
	})
}

type fixtureOptions struct {
	ID          string
	Version     string
	Minimum     string
	Before      string
	OverrideSHA string
	Artifacts   []fixtureArtifact
}

type fixtureArtifact struct {
	ID             string
	PayloadPath    string
	URL            string
	ExecutablePath string
	Data           []byte
	ExecutableData []byte
	Format         string
}

func writePackageFixture(t *testing.T, root string, options fixtureOptions) []byte {
	t.Helper()
	document := fixtureDocument(options)
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	writeTestBytes(t, filepath.Join(root, packageManifestName), data, 0o600)
	for _, artifact := range options.Artifacts {
		if artifact.PayloadPath != "" {
			writeTestBytes(t, filepath.Join(root, filepath.FromSlash(artifact.PayloadPath)), artifact.Data, 0o600)
		}
	}
	return data
}

func fixtureDocument(options fixtureOptions) map[string]any {
	if options.ID == "" {
		options.ID = "fixture-package"
	}
	if options.Version == "" {
		options.Version = "1.0.0"
	}
	if options.Minimum == "" {
		options.Minimum = "0.1.0-dev"
	}
	if options.Before == "" {
		options.Before = "9.0.0"
	}
	artifacts := make([]any, 0, len(options.Artifacts))
	capabilities := make([]any, 0, len(options.Artifacts))
	for _, fixture := range options.Artifacts {
		format := fixture.Format
		if format == "" {
			format = "binary"
		}
		executableData := fixture.ExecutableData
		if executableData == nil {
			executableData = fixture.Data
		}
		sha := digestHex(fixture.Data)
		executableSHA := digestHex(executableData)
		if options.OverrideSHA != "" {
			sha = options.OverrideSHA
			if format == "binary" {
				executableSHA = options.OverrideSHA
			}
		}
		source := map[string]any{"kind": "package", "path": fixture.PayloadPath}
		if fixture.URL != "" {
			source = map[string]any{"kind": "https", "url": fixture.URL}
		}
		artifacts = append(artifacts, map[string]any{
			"id": fixture.ID, "os": runtime.GOOS, "architecture": runtime.GOARCH, "format": format,
			"source": source, "size": len(fixture.Data), "sha256": sha,
			"executable": map[string]any{"path": fixture.ExecutablePath, "size": len(executableData), "sha256": executableSHA},
		})
		capabilities = append(capabilities, map[string]any{
			"type": "native-mcp", "version": 1, "id": fixture.ID, "server_name": strings.ReplaceAll(fixture.ID, "-", "_"), "collision": "reject",
			"artifacts": []string{fixture.ID}, "executable": fixture.ExecutablePath, "arguments": []string{}, "working_directory": ".", "environment": map[string]string{}, "required_environment": []any{},
			"harnesses": []any{map[string]any{"name": "codex", "startup": "optional", "trust": "native-project"}},
		})
	}
	return map[string]any{
		"schema_version": 1, "id": options.ID, "version": options.Version, "name": "Fixture package", "description": "Credentialless installer fixture.", "license": "MIT",
		"provenance":    map[string]any{"source": "https://example.invalid/fixture", "revision": options.ID + "-" + options.Version},
		"compatibility": map[string]any{"minimum": options.Minimum, "before": options.Before}, "artifacts": artifacts, "capabilities": capabilities,
	}
}

func channelAdapterFixtureDocument(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "channel-adapter-fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	artifact := document["artifacts"].([]any)[0].(map[string]any)
	artifact["id"] = "current-platform"
	artifact["os"] = runtime.GOOS
	artifact["architecture"] = runtime.GOARCH
	artifact["size"] = len(payload)
	artifact["sha256"] = digestHex(payload)
	executable := artifact["executable"].(map[string]any)
	executable["size"] = len(payload)
	executable["sha256"] = digestHex(payload)
	capability := document["capabilities"].([]any)[0].(map[string]any)
	capability["artifacts"] = []string{"current-platform"}
	return document
}

type archiveEntry struct {
	Data []byte
	Mode int64
}

func tarGZ(t *testing.T, entries map[string]archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, entry := range entries {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: entry.Mode, Size: int64(len(entry.Data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func zipFile(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, data := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTestBytes(t, path, append(data, '\n'), 0o600)
}

func writeTestBytes(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func digestHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
