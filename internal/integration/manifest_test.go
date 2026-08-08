package integration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeMCPFixturesUseOneVendorNeutralContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file       string
		capability string
		executable string
	}{
		{file: "native-mcp-fixture.json", capability: "fixture", executable: "bin/fixture-mcp"},
		{file: "github-mcp-server.json", capability: "github", executable: "bin/github-mcp-server"},
	}
	for _, test := range tests {
		pkg, err := Load(filepath.Join("testdata", test.file))
		if err != nil {
			t.Fatalf("Load(%s): %v", test.file, err)
		}
		selection, err := pkg.SelectNativeMCP(test.capability, "darwin", "arm64")
		if err != nil {
			t.Fatalf("SelectNativeMCP(%s): %v", test.file, err)
		}
		if selection.Artifact.Executable.Path != test.executable || selection.ManifestSHA256 != pkg.Identity() {
			t.Fatalf("selection for %s = %#v", test.file, selection)
		}
		if len(pkg.Identity()) != 64 {
			t.Fatalf("manifest identity for %s = %q", test.file, pkg.Identity())
		}
	}
}

func TestUnknownCapabilityIsRejectedWithoutReadingArtifact(t *testing.T) {
	t.Parallel()
	_, err := Load(filepath.Join("testdata", "future-capability.json"))
	var unsupported UnsupportedCapabilityError
	if !errors.As(err, &unsupported) {
		t.Fatalf("future capability error = %v", err)
	}
	if unsupported.Type != "channel-adapter" || unsupported.Version != 1 {
		t.Fatalf("future capability = %#v", unsupported)
	}
}

func TestDecodeIsBoundedAndStrict(t *testing.T) {
	t.Parallel()
	valid := `{
  "schema_version":1,
  "id":"fixture",
  "version":"1.0.0",
  "name":"Fixture",
  "description":"Fixture package.",
  "license":"MIT",
  "provenance":{"source":"https://example.invalid/source","revision":"v1"},
  "compatibility":{"minimum":"0.1.0-dev","before":"1.0.0"},
  "artifacts":[{"id":"darwin-arm64","os":"darwin","architecture":"arm64","format":"binary","source":{"kind":"package","path":"payload/server"},"size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","executable":{"path":"bin/server","size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}],
  "capabilities":[{"type":"native-mcp","version":1,"id":"fixture","server_name":"fixture","collision":"reject","artifacts":["darwin-arm64"],"executable":"bin/server","arguments":[],"working_directory":".","environment":{},"required_environment":[],"harnesses":[{"name":"codex","startup":"optional","trust":"native-project"}]}]
}`
	first, err := Decode(strings.NewReader(valid))
	if err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	second, err := Decode(strings.NewReader(valid + "\n"))
	if err != nil {
		t.Fatalf("valid reformatted manifest: %v", err)
	}
	if first.Identity() == second.Identity() {
		t.Fatal("different exact manifest bytes received the same identity")
	}
	if _, err := Decode(strings.NewReader(strings.Replace(valid, `"license":"MIT"`, `"license":"MIT","unexpected":true`, 1))); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := Decode(strings.NewReader(valid + `{}`)); err == nil || !strings.Contains(err.Error(), "one JSON document") {
		t.Fatalf("trailing document error = %v", err)
	}
	if _, err := Decode(strings.NewReader(strings.Repeat(" ", maxManifestBytes+1))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized manifest error = %v", err)
	}
}

func TestLoadRejectsSymlinkManifest(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	target := filepath.Join("testdata", "native-mcp-fixture.json")
	link := filepath.Join(directory, "manifest.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil || !strings.Contains(err.Error(), "without symlinks") {
		t.Fatalf("symlink manifest error = %v", err)
	}
}

func TestManifestValidatesEveryMetadataGroup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{name: "schema", mutate: func(m *Manifest) { m.SchemaVersion = 2 }, want: "schema_version"},
		{name: "package id", mutate: func(m *Manifest) { m.ID = "GitHub" }, want: "package id"},
		{name: "package version", mutate: func(m *Manifest) { m.Version = "latest" }, want: "version"},
		{name: "name", mutate: func(m *Manifest) { m.Name = "" }, want: "name"},
		{name: "description", mutate: func(m *Manifest) { m.Description = " bad" }, want: "description"},
		{name: "license", mutate: func(m *Manifest) { m.License = "" }, want: "license"},
		{name: "provenance source", mutate: func(m *Manifest) { m.Provenance.Source = "http://example.invalid" }, want: "provenance source"},
		{name: "provenance revision", mutate: func(m *Manifest) { m.Provenance.Revision = "" }, want: "revision"},
		{name: "compatibility endpoint", mutate: func(m *Manifest) { m.Compatibility.Before = "next" }, want: "compatibility endpoints"},
		{name: "compatibility order", mutate: func(m *Manifest) { m.Compatibility.Minimum = "2.0.0" }, want: "minimum must precede"},
		{name: "artifact id", mutate: func(m *Manifest) { m.Artifacts[0].ID = "Darwin" }, want: "artifact id"},
		{name: "artifact os", mutate: func(m *Manifest) { m.Artifacts[0].OS = "windows" }, want: "artifact os"},
		{name: "artifact architecture", mutate: func(m *Manifest) { m.Artifacts[0].Architecture = "386" }, want: "artifact architecture"},
		{name: "artifact format", mutate: func(m *Manifest) { m.Artifacts[0].Format = "directory" }, want: "artifact format"},
		{name: "artifact source", mutate: func(m *Manifest) { m.Artifacts[0].Source.Path = "../escape" }, want: "source path"},
		{name: "artifact size", mutate: func(m *Manifest) { m.Artifacts[0].Size = 0 }, want: "artifact size"},
		{name: "artifact checksum", mutate: func(m *Manifest) { m.Artifacts[0].SHA256 = "bad" }, want: "artifact sha256"},
		{name: "executable path", mutate: func(m *Manifest) { m.Artifacts[0].Executable.Path = "/bin/server" }, want: "executable path"},
		{name: "executable size", mutate: func(m *Manifest) { m.Artifacts[0].Executable.Size = 0 }, want: "executable size"},
		{name: "executable checksum", mutate: func(m *Manifest) { m.Artifacts[0].Executable.SHA256 = "bad" }, want: "executable sha256"},
		{name: "capability id", mutate: func(m *Manifest) { m.Capabilities[0].ID = "bad_id" }, want: "capability id"},
		{name: "server name", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.ServerName = "managed" }, want: "server_name"},
		{name: "collision", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Collision = "rename" }, want: "collision"},
		{name: "artifact reference", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Artifacts[0] = "missing" }, want: "not declared"},
		{name: "capability executable", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Executable = "bin/other" }, want: "does not match"},
		{name: "argument", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Arguments = []string{"${TOKEN}"} }, want: "placeholder"},
		{name: "working directory", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.WorkingDirectory = "../escape" }, want: "working_directory"},
		{name: "environment name", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Environment = map[string]string{"bad": "value"} }, want: "environment name"},
		{name: "environment value", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Environment = map[string]string{"MODE": "${SECRET}"} }, want: "placeholder"},
		{name: "required environment name", mutate: func(m *Manifest) {
			m.Capabilities[0].NativeMCP.RequiredEnvironment = []EnvironmentRequirement{{Name: "bad", Description: "Needed."}}
		}, want: "name is invalid"},
		{name: "required environment description", mutate: func(m *Manifest) {
			m.Capabilities[0].NativeMCP.RequiredEnvironment = []EnvironmentRequirement{{Name: "TOKEN", Description: ""}}
		}, want: "description"},
		{name: "required environment reference", mutate: func(m *Manifest) {
			m.Capabilities[0].NativeMCP.RequiredEnvironment = []EnvironmentRequirement{{Name: "TOKEN", Description: "Read ${TOKEN}."}}
		}, want: "reference syntax"},
		{name: "required environment default collision", mutate: func(m *Manifest) {
			m.Capabilities[0].NativeMCP.Environment = map[string]string{"TOKEN": "visible"}
			m.Capabilities[0].NativeMCP.RequiredEnvironment = []EnvironmentRequirement{{Name: "TOKEN", Description: "Needed."}}
		}, want: "also has a default"},
		{name: "harness name", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Harnesses[0].Name = "other" }, want: "name must be"},
		{name: "harness startup", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Harnesses[0].Startup = "sometimes" }, want: "startup"},
		{name: "harness trust", mutate: func(m *Manifest) { m.Capabilities[0].NativeMCP.Harnesses[0].Trust = "package" }, want: "trust"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(&manifest)
			if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompatibilityAndPlatformSelection(t *testing.T) {
	t.Parallel()
	compatibility := Compatibility{Minimum: "0.1.0-dev", Before: "1.0.0"}
	for _, test := range []struct {
		version string
		want    bool
	}{
		{version: "0.1.0-dev", want: true},
		{version: "0.1.0", want: true},
		{version: "1.0.0", want: false},
	} {
		got, err := compatibility.Contains(test.version)
		if err != nil || got != test.want {
			t.Fatalf("Contains(%q) = %t, %v", test.version, got, err)
		}
	}
	pkg := Package{manifest: validManifest(), sha256: strings.Repeat("d", 64)}
	if _, err := pkg.SelectNativeMCP("fixture", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported platform error = %v", err)
	}
}

func TestPackageManifestAndSelectionsAreDefensiveCopies(t *testing.T) {
	t.Parallel()
	pkg, err := Load(filepath.Join("testdata", "native-mcp-fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := pkg.Manifest()
	manifest.ID = "changed"
	manifest.Artifacts[0].Executable.Path = "bin/changed"
	manifest.Capabilities[0].NativeMCP.Arguments[0] = "changed"
	manifest.Capabilities[0].NativeMCP.Environment["FIXTURE_MODE"] = "changed"

	selection, err := pkg.SelectNativeMCP("fixture", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if selection.PackageID != "fixture-native-mcp" || selection.Artifact.Executable.Path != "bin/fixture-mcp" || selection.Capability.Arguments[0] != "serve" || selection.Capability.Environment["FIXTURE_MODE"] != "deterministic" {
		t.Fatalf("mutable copy changed immutable selection: %#v", selection)
	}
	selection.Capability.Arguments[0] = "changed-again"
	again, err := pkg.SelectNativeMCP("fixture", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if again.Capability.Arguments[0] != "serve" {
		t.Fatal("mutable selection changed retained package metadata")
	}
}

func TestInstallationStateBindsTrustEnablementAndExactIdentities(t *testing.T) {
	t.Parallel()
	pkg, err := Load(filepath.Join("testdata", "native-mcp-fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := pkg.Manifest()
	state := InstallationState{
		SchemaVersion:  InstallationStateVersion,
		PackageID:      manifest.ID,
		PackageVersion: manifest.Version,
		ManifestSHA256: pkg.Identity(),
		Trust:          TrustOperator,
		Enabled:        true,
		Artifacts: []InstalledArtifactIdentity{{
			ID:               manifest.Artifacts[0].ID,
			SHA256:           manifest.Artifacts[0].SHA256,
			ExecutableSHA256: manifest.Artifacts[0].Executable.SHA256,
		}},
		Capabilities: []InstalledCapabilityIdentity{{
			ID:      manifest.Capabilities[0].ID,
			Type:    manifest.Capabilities[0].Type,
			Version: manifest.Capabilities[0].Version,
		}},
	}
	if err := state.Validate(pkg); err != nil {
		t.Fatalf("valid installation state: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*InstallationState)
		want   string
	}{
		{name: "schema", mutate: func(s *InstallationState) { s.SchemaVersion = 2 }, want: "schema_version"},
		{name: "package", mutate: func(s *InstallationState) { s.PackageVersion = "2.0.0" }, want: "package identity"},
		{name: "trust", mutate: func(s *InstallationState) { s.Trust = "package" }, want: "trust"},
		{name: "artifact", mutate: func(s *InstallationState) { s.Artifacts[0].SHA256 = strings.Repeat("e", 64) }, want: "artifact"},
		{name: "capability", mutate: func(s *InstallationState) { s.Capabilities[0].Version = 2 }, want: "capability"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := state
			candidate.Artifacts = append([]InstalledArtifactIdentity(nil), state.Artifacts...)
			candidate.Capabilities = append([]InstalledCapabilityIdentity(nil), state.Capabilities...)
			test.mutate(&candidate)
			if err := candidate.Validate(pkg); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func validManifest() Manifest {
	checksum := strings.Repeat("a", 64)
	native := &NativeMCP{
		Type:             NativeMCPType,
		Version:          NativeMCPVersion,
		ID:               "fixture",
		ServerName:       "fixture",
		Collision:        "reject",
		Artifacts:        []string{"darwin-arm64"},
		Executable:       "bin/server",
		Arguments:        []string{"stdio"},
		WorkingDirectory: ".",
		Environment:      map[string]string{},
		Harnesses:        []NativeHarnessTarget{{Name: "codex", Startup: "optional", Trust: "native-project"}},
	}
	return Manifest{
		SchemaVersion: SchemaVersion,
		ID:            "fixture",
		Version:       "1.0.0",
		Name:          "Fixture",
		Description:   "Fixture package.",
		License:       "MIT",
		Provenance:    Provenance{Source: "https://example.invalid/source", Revision: "v1"},
		Compatibility: Compatibility{Minimum: "0.1.0-dev", Before: "1.0.0"},
		Artifacts: []Artifact{{
			ID:           "darwin-arm64",
			OS:           "darwin",
			Architecture: "arm64",
			Format:       "binary",
			Source:       ArtifactSource{Kind: "package", Path: "payload/server"},
			Size:         1,
			SHA256:       checksum,
			Executable:   Executable{Path: "bin/server", Size: 1, SHA256: checksum},
		}},
		Capabilities: []Capability{{Type: NativeMCPType, Version: NativeMCPVersion, ID: "fixture", NativeMCP: native}},
	}
}
