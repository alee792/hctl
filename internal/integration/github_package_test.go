package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const githubPackageID = "github-mcp-server"

type githubUpstreamSource struct {
	Platform         string
	PackageID        string
	URL              string
	RedirectOrigin   string
	PackagePath      string
	ArchiveSize      string
	ArchiveSHA256    string
	ArchiveLayout    string
	ExecutablePath   string
	ExecutableSize   string
	ExecutableSHA256 string
}

func TestCuratedGitHubPackagePinsOfficialRelease(t *testing.T) {
	packageRoot := filepath.Join("..", "..", "integrations", githubPackageID)
	pkg, err := Load(filepath.Join(packageRoot, packageManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest := pkg.Manifest()
	if manifest.ID != githubPackageID || manifest.Version != "1.8.0" || manifest.License != "MIT" {
		t.Fatalf("package identity = %s@%s license=%s", manifest.ID, manifest.Version, manifest.License)
	}
	if manifest.Provenance.Source != "https://github.com/github/github-mcp-server" || manifest.Provenance.Revision != "v1.8.0" {
		t.Fatalf("package provenance = %#v", manifest.Provenance)
	}
	want := map[string]githubUpstreamSource{
		"darwin-arm64": {
			Platform: "darwin-arm64", PackageID: githubPackageID, URL: "https://github.com/github/github-mcp-server/releases/download/v1.8.0/github-mcp-server_Darwin_arm64.tar.gz", RedirectOrigin: "release-assets.githubusercontent.com",
			PackagePath: "payload/github-mcp-server_Darwin_arm64.tar.gz", ArchiveSize: "7721103", ArchiveSHA256: "1da9cff2490f2908e2fd051e090c5c0792cd44773ee195b85ad0f549d3c435d0", ArchiveLayout: "LICENSE,README.md,github-mcp-server",
			ExecutablePath: "github-mcp-server", ExecutableSize: "23864706", ExecutableSHA256: "3b04331241338c34570cc0ec70e9b9637ef0303689832da0d88ea7739a4cac9b",
		},
		"linux-amd64": {
			Platform: "linux-amd64", PackageID: githubPackageID, URL: "https://github.com/github/github-mcp-server/releases/download/v1.8.0/github-mcp-server_Linux_x86_64.tar.gz", RedirectOrigin: "release-assets.githubusercontent.com",
			PackagePath: "payload/github-mcp-server_Linux_x86_64.tar.gz", ArchiveSize: "8039369", ArchiveSHA256: "b2754921aec1b1302b19a71531d26d242ef0e7f1e05696b8444beab5a7e61d5b", ArchiveLayout: "LICENSE,README.md,github-mcp-server",
			ExecutablePath: "github-mcp-server", ExecutableSize: "24563896", ExecutableSHA256: "f9c7846aebc56ea19dd00d6404f2d9041cd80871dec2fbaf0e5a5842df36f7ce",
		},
	}
	sources := readGitHubUpstreamSources(t, filepath.Join(packageRoot, "sources.tsv"))
	if len(sources) != len(want) || len(manifest.Artifacts) != len(want) {
		t.Fatalf("sources=%d artifacts=%d", len(sources), len(manifest.Artifacts))
	}
	for _, artifact := range manifest.Artifacts {
		source, ok := sources[artifact.ID]
		if !ok || source != want[artifact.ID] {
			t.Fatalf("upstream source %q = %#v", artifact.ID, source)
		}
		if artifact.Source.Kind != SourcePackage || artifact.Source.Path != source.PackagePath || artifact.Format != FormatTarGZ {
			t.Fatalf("artifact source %q = %#v", artifact.ID, artifact.Source)
		}
		if strconv.FormatInt(artifact.Size, 10) != source.ArchiveSize || artifact.SHA256 != source.ArchiveSHA256 || artifact.Executable.Path != source.ExecutablePath || strconv.FormatInt(artifact.Executable.Size, 10) != source.ExecutableSize || artifact.Executable.SHA256 != source.ExecutableSHA256 {
			t.Fatalf("artifact identity %q is not bound to sources.tsv", artifact.ID)
		}
	}
	for _, target := range []struct{ os, arch string }{{"darwin", "arm64"}, {"linux", "amd64"}} {
		selection, err := pkg.SelectNativeMCP("github", target.os, target.arch)
		if err != nil {
			t.Fatalf("SelectNativeMCP(%s/%s): %v", target.os, target.arch, err)
		}
		if selection.Capability.ServerName != "github" || strings.Join(selection.Capability.Arguments, " ") != "stdio" || selection.Capability.WorkingDirectory != "." || selection.Capability.Collision != CollisionReject {
			t.Fatalf("native launch metadata = %#v", selection.Capability)
		}
		if len(selection.Capability.RequiredEnvironment) != 1 || selection.Capability.RequiredEnvironment[0].Name != "GITHUB_PERSONAL_ACCESS_TOKEN" {
			t.Fatalf("required environment = %#v", selection.Capability.RequiredEnvironment)
		}
	}
}

func TestCuratedGitHubPackageFakeArtifactJourney(t *testing.T) {
	packageRoot, payload := writeFakeGitHubPackage(t)
	store := NewStore(t.TempDir(), nil)
	t.Setenv("GITHUB_PERSONAL_ACCESS_TOKEN", "fake-value-that-must-not-be-read")
	installed, err := store.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if installed.State.PackageID != githubPackageID || len(installed.State.Artifacts) != 1 {
		t.Fatalf("installed = %#v", installed.State)
	}

	unavailable := packageRoot + "-unavailable"
	if err := os.Rename(packageRoot, unavailable); err != nil {
		t.Fatal(err)
	}
	pathDirectory := t.TempDir()
	writeTestBytes(t, filepath.Join(pathDirectory, "github-mcp-server"), []byte("wrong PATH executable"), 0o700)
	t.Setenv("PATH", pathDirectory)

	resolved, err := store.ResolveNativeMCP(context.Background(), githubPackageID, "github")
	if err != nil {
		t.Fatalf("offline ResolveNativeMCP() error = %v", err)
	}
	if resolved.Executable == filepath.Join(pathDirectory, "github-mcp-server") {
		t.Fatal("native MCP resolution fell back to PATH")
	}
	if data, err := os.ReadFile(resolved.Executable); err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("resolved executable = %q, %v", data, err)
	}
	descriptor, err := resolved.LaunchDescriptor("codex")
	if err != nil {
		t.Fatal(err)
	}
	commandInfo, commandErr := os.Stat(descriptor.Command)
	resolvedInfo, resolvedErr := os.Stat(resolved.Executable)
	descriptorRootInfo, descriptorRootErr := os.Stat(descriptor.WorkingDirectory)
	resolvedRootInfo, resolvedRootErr := os.Stat(resolved.Root)
	if commandErr != nil || resolvedErr != nil || !os.SameFile(commandInfo, resolvedInfo) || descriptorRootErr != nil || resolvedRootErr != nil || !os.SameFile(descriptorRootInfo, resolvedRootInfo) || strings.Join(descriptor.Arguments, " ") != "stdio" {
		t.Fatalf("launch descriptor = %#v", descriptor)
	}
	if len(descriptor.RequiredEnvironment) != 1 || descriptor.RequiredEnvironment[0].Name != "GITHUB_PERSONAL_ACCESS_TOKEN" {
		t.Fatalf("launch required environment = %#v", descriptor.RequiredEnvironment)
	}

	selectedRoot := t.TempDir()
	staged, err := store.StageArtifacts(context.Background(), githubPackageID, resolved.Selection.Capability.Artifacts, selectedRoot)
	if err != nil || len(staged) != 1 || !strings.Contains(staged[0].Executable, "/opt/hctl/integrations/github-mcp-server/") {
		t.Fatalf("StageArtifacts() = %#v, %v", staged, err)
	}
	physicalExecutable := filepath.Join(selectedRoot, filepath.FromSlash(strings.TrimPrefix(staged[0].Executable, "/")))
	if data, err := os.ReadFile(physicalExecutable); err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("staged executable = %q, %v", data, err)
	}
	assertTreeOmitsBytes(t, store.root, []byte("fake-value-that-must-not-be-read"))
	assertTreeOmitsBytes(t, selectedRoot, []byte("fake-value-that-must-not-be-read"))
	omittedRoot := t.TempDir()
	if _, err := os.Stat(filepath.Join(omittedRoot, "opt", "hctl", "integrations")); !os.IsNotExist(err) {
		t.Fatalf("GitHub-free closure contains integration state: %v", err)
	}

	if _, err := store.Verify(context.Background(), githubPackageID); err != nil {
		t.Fatalf("offline Verify() error = %v", err)
	}
	if err := os.Chmod(resolved.Executable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolved.Executable, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(context.Background(), githubPackageID); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt Verify() error = %v", err)
	}
}

func assertTreeOmitsBytes(t *testing.T, root string, forbidden []byte) {
	t.Helper()
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, forbidden) {
			t.Fatalf("credential value retained in %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCuratedGitHubPackageFakeFailuresAndConcurrency(t *testing.T) {
	packageRoot, _ := writeFakeGitHubPackage(t)

	storeRoot := t.TempDir()
	const workers = 6
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
	if _, err := NewStore(storeRoot, nil).Verify(context.Background(), githubPackageID); err != nil {
		t.Fatalf("concurrent installation Verify() error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewStore(t.TempDir(), nil).Install(cancelled, InstallOptions{Source: packageRoot, Trust: TrustOperator}); err == nil {
		t.Fatal("cancelled package installation succeeded")
	}

	unsupported := NewStore(t.TempDir(), nil)
	unsupported.targetOS, unsupported.targetArch = "linux", "arm64"
	if _, err := unsupported.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "does not support linux/arm64") {
		t.Fatalf("unsupported platform error = %v", err)
	}

	driftStore := NewStore(t.TempDir(), nil)
	if _, err := driftStore.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(packageRoot, packageManifestName))
	if err != nil {
		t.Fatal(err)
	}
	drifted := bytes.Replace(data, []byte(`"stdio"`), []byte(`"drifted"`), 1)
	if bytes.Equal(data, drifted) {
		t.Fatal("fixture launch metadata was not found")
	}
	if err := os.WriteFile(filepath.Join(packageRoot, packageManifestName), drifted, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := driftStore.Install(context.Background(), InstallOptions{Source: packageRoot, Trust: TrustOperator}); err == nil || !strings.Contains(err.Error(), "different exact identity") {
		t.Fatalf("descriptor drift error = %v", err)
	}
}

func TestGitHubPackageMaterializerUsesPinnedFakeUpstream(t *testing.T) {
	executable := []byte("#!/bin/sh\nprintf fake-official-server\n")
	archive := orderedTarGZ(t, []orderedArchiveEntry{
		{Name: "LICENSE", Data: []byte("fake MIT license\n"), Mode: 0o644},
		{Name: "README.md", Data: []byte("fake upstream readme\n"), Mode: 0o644},
		{Name: "github-mcp-server", Data: executable, Mode: 0o755},
	})
	packageMetadata := t.TempDir()
	document := fixtureDocument(fixtureOptions{Artifacts: []fixtureArtifact{{ID: "linux-amd64", PayloadPath: "payload/github-mcp-server_Linux_x86_64.tar.gz", ExecutablePath: "github-mcp-server", Data: archive}}})
	document["id"] = githubPackageID
	document["version"] = "1.8.0"
	document["name"] = "Official GitHub MCP server fake upstream"
	document["description"] = "Credential free materializer evidence."
	document["provenance"] = map[string]any{"source": "https://github.com/github/github-mcp-server", "revision": "v1.8.0-fake-upstream"}
	artifact := document["artifacts"].([]any)[0].(map[string]any)
	artifact["os"] = "linux"
	artifact["architecture"] = "amd64"
	artifact["format"] = "tar.gz"
	artifact["size"] = len(archive)
	artifact["sha256"] = digestHex(archive)
	artifact["executable"] = map[string]any{"path": "github-mcp-server", "size": len(executable), "sha256": digestHex(executable)}
	capability := document["capabilities"].([]any)[0].(map[string]any)
	capability["id"] = "github"
	capability["server_name"] = "github"
	capability["artifacts"] = []string{"linux-amd64"}
	capability["executable"] = "github-mcp-server"
	capability["arguments"] = []string{"stdio"}
	capability["required_environment"] = []any{map[string]any{"name": "GITHUB_PERSONAL_ACCESS_TOKEN", "description": "Ambient GitHub authentication required by the server at runtime."}}
	writeJSON(t, filepath.Join(packageMetadata, packageManifestName), document)
	sourceRecord := strings.Join([]string{
		"linux-amd64",
		githubPackageID,
		"https://github.com/github/github-mcp-server/releases/download/v1.8.0/github-mcp-server_Linux_x86_64.tar.gz",
		"release-assets.githubusercontent.com",
		"payload/github-mcp-server_Linux_x86_64.tar.gz",
		strconv.Itoa(len(archive)), digestHex(archive), "LICENSE,README.md,github-mcp-server",
		"github-mcp-server", strconv.Itoa(len(executable)), digestHex(executable),
	}, "\t") + "\n"
	writeTestBytes(t, filepath.Join(packageMetadata, "sources.tsv"), []byte(sourceRecord), 0o600)

	fakeBin := t.TempDir()
	fakeCurl := `#!/bin/sh
set -eu
if [ "${FAKE_CURL_FAIL:-}" = 1 ]; then
  echo fake unavailable >&2
  exit 22
fi
output=
head_request=false
last=
while [ "$#" -gt 0 ]; do
  last=$1
  if [ "$1" = "--head" ]; then head_request=true; shift; continue; fi
  if [ "$1" = "--output" ]; then output=$2; shift 2; continue; fi
  shift
done
if [ "$head_request" = true ]; then
  printf 'HTTP/1.1 302 Found\r\nLocation: %s\r\n\r\nHCTL_STATUS:302\n' "${FAKE_REDIRECT_URL:-https://release-assets.githubusercontent.com/fake/release-asset}"
  exit 0
fi
case "$last" in
  https://rejected.example/*)
    printf contacted >"$FAKE_REJECTED_ENDPOINT"
    exit 99
    ;;
esac
cp "$FAKE_INTEGRATION_ARCHIVE" "$output"
`
	writeTestBytes(t, filepath.Join(fakeBin, "curl"), []byte(fakeCurl), 0o700)
	fakeArchive := filepath.Join(t.TempDir(), "upstream.tar.gz")
	writeTestBytes(t, fakeArchive, archive, 0o600)
	output := filepath.Join(t.TempDir(), "materialized")
	materializer, err := filepath.Abs(filepath.Join("..", "..", "scripts", "materialize-integration-package.sh"))
	if err != nil {
		t.Fatal(err)
	}
	executableFields := "\tgithub-mcp-server\t" + strconv.Itoa(len(executable)) + "\t" + digestHex(executable)
	unsafeRecord := strings.Replace(sourceRecord, executableFields, "\t--checkpoint-action=exec=sh\t"+strconv.Itoa(len(executable))+"\t"+digestHex(executable), 1)
	writeTestBytes(t, filepath.Join(packageMetadata, "sources-unsafe.tsv"), []byte(unsafeRecord), 0o600)
	if err := os.Rename(filepath.Join(packageMetadata, "sources.tsv"), filepath.Join(packageMetadata, "sources-safe.tsv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(packageMetadata, "sources-unsafe.tsv"), filepath.Join(packageMetadata, "sources.tsv")); err != nil {
		t.Fatal(err)
	}
	unsafeOutput := filepath.Join(t.TempDir(), "materialized")
	unsafeCommand := exec.Command(materializer, "--package", packageMetadata, "--platform", "linux-amd64", "--output", unsafeOutput)
	unsafeCommand.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_INTEGRATION_ARCHIVE="+fakeArchive)
	if result, err := unsafeCommand.CombinedOutput(); err == nil || !strings.Contains(string(result), "executable path is invalid") {
		t.Fatalf("unsafe executable path result = %q, %v", result, err)
	}
	if err := os.Rename(filepath.Join(packageMetadata, "sources.tsv"), filepath.Join(packageMetadata, "sources-unsafe.tsv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(packageMetadata, "sources-safe.tsv"), filepath.Join(packageMetadata, "sources.tsv")); err != nil {
		t.Fatal(err)
	}
	rejectedEndpoint := filepath.Join(t.TempDir(), "contacted")
	rejectedOutput := filepath.Join(t.TempDir(), "materialized")
	rejectedCommand := exec.Command(materializer, "--package", packageMetadata, "--platform", "linux-amd64", "--output", rejectedOutput)
	rejectedCommand.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_INTEGRATION_ARCHIVE="+fakeArchive, "FAKE_REDIRECT_URL=https://rejected.example/asset", "FAKE_REJECTED_ENDPOINT="+rejectedEndpoint)
	if result, err := rejectedCommand.CombinedOutput(); err == nil || !strings.Contains(string(result), "left the approved release-asset origin") {
		t.Fatalf("rejected redirect result = %q, %v", result, err)
	}
	if _, err := os.Stat(rejectedEndpoint); !os.IsNotExist(err) {
		t.Fatalf("rejected redirect endpoint was contacted: %v", err)
	}
	unavailableOutput := filepath.Join(t.TempDir(), "materialized")
	unavailableCommand := exec.Command(materializer, "--package", packageMetadata, "--platform", "linux-amd64", "--output", unavailableOutput)
	unavailableCommand.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_INTEGRATION_ARCHIVE="+fakeArchive, "FAKE_CURL_FAIL=1")
	if result, err := unavailableCommand.CombinedOutput(); err == nil || !strings.Contains(string(result), "integration package github-mcp-server source is unavailable") {
		t.Fatalf("unavailable source result = %q, %v", result, err)
	}
	wrongChecksumRecord := strings.Replace(sourceRecord, digestHex(archive), strings.Repeat("0", 64), 1)
	if err := os.WriteFile(filepath.Join(packageMetadata, "sources.tsv"), []byte(wrongChecksumRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatchOutput := filepath.Join(t.TempDir(), "materialized")
	mismatchCommand := exec.Command(materializer, "--package", packageMetadata, "--platform", "linux-amd64", "--output", mismatchOutput)
	mismatchCommand.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_INTEGRATION_ARCHIVE="+fakeArchive)
	if result, err := mismatchCommand.CombinedOutput(); err == nil || !strings.Contains(string(result), "integration package github-mcp-server archive SHA-256") {
		t.Fatalf("archive mismatch result = %q, %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(packageMetadata, "sources.tsv"), []byte(sourceRecord), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(materializer, "--package", packageMetadata, "--platform", "linux-amd64", "--output", output)
	command.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "FAKE_INTEGRATION_ARCHIVE="+fakeArchive)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("materializer error = %v, output = %s", err, result)
	}

	store := NewStore(t.TempDir(), nil)
	store.targetOS, store.targetArch = "linux", "amd64"
	if _, err := store.Install(context.Background(), InstallOptions{Source: output, Trust: TrustOperator}); err != nil {
		t.Fatalf("materialized Install() error = %v", err)
	}
	resolved, err := store.ResolveNativeMCP(context.Background(), githubPackageID, "github")
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(resolved.Executable); err != nil || !bytes.Equal(data, executable) {
		t.Fatalf("materialized executable = %q, %v", data, err)
	}
}

func TestGitHubPackageInstallWrapperUsesGenericTrustJourney(t *testing.T) {
	repo := t.TempDir()
	packageRoot := filepath.Join(repo, "integrations", githubPackageID)
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	checkedInstall, err := os.ReadFile(filepath.Join("..", "..", "integrations", githubPackageID, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestBytes(t, filepath.Join(packageRoot, "install.sh"), checkedInstall, 0o700)
	materializer := `#!/bin/sh
set -eu
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then output=$2; shift 2; continue; fi
  shift
done
mkdir -p "$output"
printf '{}\n' >"$output/integration.json"
`
	writeTestBytes(t, filepath.Join(repo, "scripts", "materialize-integration-package.sh"), []byte(materializer), 0o700)

	fakeBin := t.TempDir()
	fakeUname := `#!/bin/sh
case "$1" in
  -s) printf Linux ;;
  -m) printf x86_64 ;;
  *) exit 64 ;;
esac
`
	writeTestBytes(t, filepath.Join(fakeBin, "uname"), []byte(fakeUname), 0o700)
	vendorMarker := filepath.Join(t.TempDir(), "vendor-path-used")
	fakeVendor := "#!/bin/sh\nprintf used >\"$FAKE_VENDOR_MARKER\"\nexit 99\n"
	writeTestBytes(t, filepath.Join(fakeBin, "github-mcp-server"), []byte(fakeVendor), 0o700)
	record := filepath.Join(t.TempDir(), "hctl-arguments")
	fakeHCTL := `#!/bin/sh
set -eu
printf '%s\n' "$@" >"$FAKE_HCTL_RECORD"
[ "$1" = integration ]
[ "$2" = install ]
[ -f "$3/integration.json" ]
[ "$4" = --trust ]
[ "$5" = operator ]
`
	fakeHCTLPath := filepath.Join(fakeBin, "hctl")
	writeTestBytes(t, fakeHCTLPath, []byte(fakeHCTL), 0o700)
	command := exec.Command(filepath.Join(packageRoot, "install.sh"))
	command.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "HCTL_EXECUTABLE="+fakeHCTLPath, "FAKE_HCTL_RECORD="+record, "FAKE_VENDOR_MARKER="+vendorMarker)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install wrapper error = %v, output = %s", err, result)
	}
	arguments, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(arguments)), "\n")
	if len(lines) != 5 || lines[0] != "integration" || lines[1] != "install" || lines[3] != "--trust" || lines[4] != "operator" {
		t.Fatalf("hctl install arguments = %q", arguments)
	}
	if _, err := os.Stat(vendorMarker); !os.IsNotExist(err) {
		t.Fatalf("install wrapper executed ambient vendor PATH entry: %v", err)
	}
}

func readGitHubUpstreamSources(t *testing.T, path string) map[string]githubUpstreamSource {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader := csv.NewReader(file)
	reader.Comma = '\t'
	reader.Comment = '#'
	reader.FieldsPerRecord = 11
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]githubUpstreamSource, len(records))
	for _, record := range records {
		if _, exists := result[record[0]]; exists {
			t.Fatalf("duplicate upstream platform %q", record[0])
		}
		result[record[0]] = githubUpstreamSource{
			Platform: record[0], PackageID: record[1], URL: record[2], RedirectOrigin: record[3], PackagePath: record[4], ArchiveSize: record[5], ArchiveSHA256: record[6], ArchiveLayout: record[7],
			ExecutablePath: record[8], ExecutableSize: record[9], ExecutableSHA256: record[10],
		}
	}
	return result
}

func writeFakeGitHubPackage(t *testing.T) (string, []byte) {
	t.Helper()
	payload := []byte("#!/bin/sh\nprintf '%s' \"$1\"\n")
	root := t.TempDir()
	document := fixtureDocument(fixtureOptions{Artifacts: []fixtureArtifact{{ID: "current", PayloadPath: "payload/github-mcp-server", ExecutablePath: "bin/github-mcp-server", Data: payload}}})
	document["id"] = githubPackageID
	document["version"] = "1.8.0"
	document["name"] = "Official GitHub MCP server fake"
	document["description"] = "Credential free official package evidence."
	document["provenance"] = map[string]any{"source": "https://github.com/github/github-mcp-server", "revision": "v1.8.0-fake-evidence"}
	capability := document["capabilities"].([]any)[0].(map[string]any)
	capability["id"] = "github"
	capability["server_name"] = "github"
	capability["arguments"] = []string{"stdio"}
	capability["required_environment"] = []any{map[string]any{"name": "GITHUB_PERSONAL_ACCESS_TOKEN", "description": "Ambient GitHub authentication required by the server at runtime."}}
	writeJSON(t, filepath.Join(root, packageManifestName), document)
	writeTestBytes(t, filepath.Join(root, "payload", "github-mcp-server"), payload, 0o600)
	return root, payload
}

type orderedArchiveEntry struct {
	Name string
	Data []byte
	Mode int64
}

func orderedTarGZ(t *testing.T, entries []orderedArchiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.Name, Mode: entry.Mode, Size: int64(len(entry.Data)), Typeflag: tar.TypeReg}); err != nil {
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
