// Package integration defines metadata-only contracts for process-isolated
// third-party integration packages. It validates package manifests without
// loading or executing package code. Installation, artifact fetching, and
// capability runtime behavior belong to separate consumers.
package integration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"hctl/internal/rootfs"
	"hctl/internal/version"
)

const (
	SchemaVersion                                    = 1
	InstallationStateVersion                         = 1
	NativeMCPType                                    = "native-mcp"
	NativeMCPVersion                                 = 1
	ChannelAdapterType                               = "channel-adapter"
	ChannelAdapterVersion                            = 1
	ChannelAdapterProtocolVersion                    = 1
	OSDarwin                      OperatingSystem    = "darwin"
	OSLinux                       OperatingSystem    = "linux"
	ArchitectureARM64             Architecture       = "arm64"
	ArchitectureAMD64             Architecture       = "amd64"
	FormatBinary                  ArtifactFormat     = "binary"
	FormatTarGZ                   ArtifactFormat     = "tar.gz"
	FormatZIP                     ArtifactFormat     = "zip"
	SourcePackage                 ArtifactSourceKind = "package"
	SourceHTTPS                   ArtifactSourceKind = "https"
	CollisionReject               CollisionPolicy    = "reject"
	StartupOptional               StartupPolicy      = "optional"
	StartupRequired               StartupPolicy      = "required"
	TrustNativeProject            NativeTrust        = "native-project"
	TrustOperator                 InstallationTrust  = "operator"
	ProfileOpaqueID               ProfileSelector    = "opaque-id-v1"
	FeatureTyping                 ChannelFeature     = "typing"
	FeatureReplies                ChannelFeature     = "replies"
	FeatureEdits                  ChannelFeature     = "edits"
	FeatureReactions              ChannelFeature     = "reactions"
	FeatureAttachments            ChannelFeature     = "attachments"
	FeatureInteractive            ChannelFeature     = "interactive-components"
	FeatureTextFallback           ChannelFeature     = "text-fallback"
	maxManifestBytes                                 = 256 << 10
	maxArtifacts                                     = 64
	maxCapabilities                                  = 64
	maxArguments                                     = 128
	maxEnvironment                                   = 128
	maxRequiredEnv                                   = 128
	maxArtifactBytes                                 = int64(2 << 30)
	maxExecutableBytes                               = int64(512 << 20)
	maxArgumentBytes                                 = 4096
	maxEnvironmentBytes                              = 4096
)

var (
	packageIDPattern       = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)
	capabilityID           = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	artifactIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	serverNamePattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	environmentName        = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
	environmentDescription = regexp.MustCompile(`^[A-Za-z][A-Za-z ,.;()'\-]{0,511}$`)
	checksumPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var channelProfileID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func ValidatePackageID(value string) error {
	if !packageIDPattern.MatchString(value) || len(value) > 128 {
		return errors.New("integration package id is invalid")
	}
	return nil
}

func ValidateCapabilityID(value string) error {
	if !capabilityID.MatchString(value) || len(value) > 64 {
		return errors.New("integration capability id is invalid")
	}
	return nil
}

// ValidateChannelProfileID applies the closed opaque-id-v1 selector grammar.
// A profile is a non-secret identity, never a path, command, environment name,
// or credential reference.
func ValidateChannelProfileID(value string) error {
	if !channelProfileID.MatchString(value) || len(value) > 64 {
		return errors.New("channel-adapter profile id is invalid")
	}
	return nil
}

type OperatingSystem string
type Architecture string
type ArtifactFormat string
type ArtifactSourceKind string
type CollisionPolicy string
type StartupPolicy string
type NativeTrust string
type InstallationTrust string
type ProfileSelector string
type ChannelFeature string

// Package is one immutable validated manifest and its exact-byte identity.
// Accessors return defensive copies so capability selection always remains
// bound to the bytes decoded originally.
type Package struct {
	manifest Manifest
	sha256   string
}

// Manifest is the common metadata envelope. Capability declarations remain
// tagged, closed schemas and never carry executable Go behavior.
type Manifest struct {
	SchemaVersion int
	ID            string
	Version       string
	Name          string
	Description   string
	License       string
	Provenance    Provenance
	Compatibility Compatibility
	Artifacts     []Artifact
	Capabilities  []Capability
}

type Provenance struct {
	Source   string `json:"source"`
	Revision string `json:"revision"`
}

// Compatibility is a half-open hctl semantic-version interval.
type Compatibility struct {
	Minimum string `json:"minimum"`
	Before  string `json:"before"`
}

type Artifact struct {
	ID           string          `json:"id"`
	OS           OperatingSystem `json:"os"`
	Architecture Architecture    `json:"architecture"`
	Format       ArtifactFormat  `json:"format"`
	Source       ArtifactSource  `json:"source"`
	Size         int64           `json:"size"`
	SHA256       string          `json:"sha256"`
	Executable   Executable      `json:"executable"`
}

// ArtifactSource is either a package-relative payload or an exact
// checksum-and-size-pinned HTTPS payload. The tag is deliberately closed.
type ArtifactSource struct {
	Kind ArtifactSourceKind `json:"kind"`
	Path string             `json:"path,omitempty"`
	URL  string             `json:"url,omitempty"`
}

// Executable is the identity expected after a platform artifact is prepared.
type Executable struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Capability contains exactly one recognized versioned capability schema.
type Capability struct {
	Type           string
	Version        int
	ID             string
	NativeMCP      *NativeMCP
	ChannelAdapter *ChannelAdapter
}

// NativeMCP is the native stdio MCP capability v1 contract. The listed
// artifacts are its selective runtime/staging closure. Environment defaults
// are literal non-secret metadata; ambient requirements contain names and safe
// descriptions only.
type NativeMCP struct {
	Type                string                   `json:"type"`
	Version             int                      `json:"version"`
	ID                  string                   `json:"id"`
	ServerName          string                   `json:"server_name"`
	Collision           CollisionPolicy          `json:"collision"`
	Artifacts           []string                 `json:"artifacts"`
	Executable          string                   `json:"executable"`
	Arguments           []string                 `json:"arguments"`
	WorkingDirectory    string                   `json:"working_directory"`
	Environment         map[string]string        `json:"environment"`
	RequiredEnvironment []EnvironmentRequirement `json:"required_environment"`
	Harnesses           []NativeHarnessTarget    `json:"harnesses"`
}

type EnvironmentRequirement struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type NativeHarnessTarget struct {
	Name    string        `json:"name"`
	Startup StartupPolicy `json:"startup"`
	Trust   NativeTrust   `json:"trust"`
}

// ChannelAdapter is the metadata-only channel-adapter v1 declaration. It
// identifies one executable and its exact fixed mode arguments; runtime
// behavior is governed by the separate bounded channel-adapter protocol.
// Every mode runs with the verified package root as its working directory.
// Profile ids are non-secret opaque selectors. Hctl appends the standardized
// "--profile", PROFILE pair for setup, status, and remove and sends the same
// selector in runtime initialization.
type ChannelAdapter struct {
	Type            string                      `json:"type"`
	Version         int                         `json:"version"`
	ID              string                      `json:"id"`
	ChannelKind     string                      `json:"channel_kind"`
	Artifacts       []string                    `json:"artifacts"`
	Executable      string                      `json:"executable"`
	Runtime         ChannelAdapterCommand       `json:"runtime"`
	Setup           ChannelAdapterCommand       `json:"setup"`
	Status          ChannelAdapterCommand       `json:"status"`
	Remove          ChannelAdapterCommand       `json:"remove"`
	Protocol        ChannelAdapterProtocolRange `json:"protocol"`
	ProfileSelector ProfileSelector             `json:"profile_selector"`
	Features        []ChannelFeature            `json:"features"`
}

// ChannelAdapterCommand contains only fixed literal non-secret arguments. The
// package cannot ask hctl to perform shell lookup or interpolate runtime
// values.
type ChannelAdapterCommand struct {
	Arguments []string `json:"arguments"`
}

// ChannelAdapterProtocolRange is half-open. A live handshake may select only
// a version inside this manifest declaration and hctl's own supported range.
type ChannelAdapterProtocolRange struct {
	Minimum int `json:"minimum"`
	Before  int `json:"before"`
}

// NativeMCPSelection is content-free evidence binding one capability to an
// exact package manifest, platform artifact, and executable identity. An
// installer may add an absolute verified path; this contract does not.
type NativeMCPSelection struct {
	PackageID      string
	PackageVersion string
	ManifestSHA256 string
	Capability     NativeMCP
	Artifact       Artifact
}

// ChannelAdapterSelection is content-free metadata binding one adapter
// capability to an immutable manifest, platform artifact, and executable.
type ChannelAdapterSelection struct {
	PackageID      string
	PackageVersion string
	ManifestSHA256 string
	Capability     ChannelAdapter
	Artifact       Artifact
}

// InstallationState is the package-level operator-owned contract that later
// storage and CLI work persists. It records no paths, credential material, or
// runtime values.
type InstallationState struct {
	SchemaVersion  int                           `json:"schema_version"`
	PackageID      string                        `json:"package_id"`
	PackageVersion string                        `json:"package_version"`
	ManifestSHA256 string                        `json:"manifest_sha256"`
	Trust          InstallationTrust             `json:"trust"`
	Enabled        bool                          `json:"enabled"`
	Artifacts      []InstalledArtifactIdentity   `json:"artifacts"`
	Capabilities   []InstalledCapabilityIdentity `json:"capabilities"`
}

type InstalledArtifactIdentity struct {
	ID               string `json:"id"`
	SHA256           string `json:"sha256"`
	ExecutableSHA256 string `json:"executable_sha256"`
}

type InstalledCapabilityIdentity struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Version int    `json:"version"`
}

// UnsupportedCapabilityError reports a recognized tag whose schema hctl does
// not implement. No package executable is consulted to reach this result.
type UnsupportedCapabilityError struct {
	Type    string
	Version int
}

func (err UnsupportedCapabilityError) Error() string {
	return fmt.Sprintf("integration capability %q version %d is not supported", err.Type, err.Version)
}

type manifestDocument struct {
	SchemaVersion int               `json:"schema_version"`
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license"`
	Provenance    Provenance        `json:"provenance"`
	Compatibility Compatibility     `json:"compatibility"`
	Artifacts     []Artifact        `json:"artifacts"`
	Capabilities  []json.RawMessage `json:"capabilities"`
}

type capabilityHeader struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	ID      string `json:"id"`
}

// Load reads and validates one bounded regular manifest without executing any
// package content.
func Load(path string) (Package, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Package{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxManifestBytes {
		return Package{}, errors.New("integration manifest must be a bounded regular file without symlinks")
	}
	file, err := os.Open(path)
	if err != nil {
		return Package{}, err
	}
	defer func() { _ = file.Close() }()
	return Decode(file)
}

// Decode validates one bounded JSON manifest and returns its exact-byte hash.
func Decode(reader io.Reader) (Package, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil {
		return Package{}, errors.New("cannot read integration manifest")
	}
	if len(content) > maxManifestBytes {
		return Package{}, fmt.Errorf("integration manifest exceeds %d bytes", maxManifestBytes)
	}
	var document manifestDocument
	if err := decodeStrict(content, &document); err != nil {
		return Package{}, fmt.Errorf("decode integration manifest: %w", err)
	}
	manifest := Manifest{
		SchemaVersion: document.SchemaVersion,
		ID:            document.ID,
		Version:       document.Version,
		Name:          document.Name,
		Description:   document.Description,
		License:       document.License,
		Provenance:    document.Provenance,
		Compatibility: document.Compatibility,
		Artifacts:     document.Artifacts,
		Capabilities:  make([]Capability, 0, len(document.Capabilities)),
	}
	for index, raw := range document.Capabilities {
		capability, err := decodeCapability(raw)
		if err != nil {
			return Package{}, fmt.Errorf("capabilities[%d]: %w", index, err)
		}
		manifest.Capabilities = append(manifest.Capabilities, capability)
	}
	if err := manifest.Validate(); err != nil {
		return Package{}, err
	}
	digest := sha256.Sum256(content)
	return Package{manifest: manifest, sha256: hex.EncodeToString(digest[:])}, nil
}

// Manifest returns a defensive copy of the validated package metadata.
func (pkg Package) Manifest() Manifest {
	return cloneManifest(pkg.manifest)
}

// Identity returns the SHA-256 of the exact manifest bytes originally decoded.
func (pkg Package) Identity() string {
	return pkg.sha256
}

func decodeCapability(raw []byte) (Capability, error) {
	var header capabilityHeader
	if err := decodeStrict(raw, &header); err != nil {
		// A header-only strict decode rejects capability-specific fields. Decode
		// just the three tags once, then apply the selected closed schema below.
		var tags map[string]json.RawMessage
		if json.Unmarshal(raw, &tags) != nil {
			return Capability{}, errors.New("capability must be an object")
		}
		if value, ok := tags["type"]; !ok || json.Unmarshal(value, &header.Type) != nil {
			return Capability{}, errors.New("field type is required and must be a string")
		}
		if value, ok := tags["version"]; !ok || json.Unmarshal(value, &header.Version) != nil {
			return Capability{}, errors.New("field version is required and must be an integer")
		}
		if value, ok := tags["id"]; !ok || json.Unmarshal(value, &header.ID) != nil {
			return Capability{}, errors.New("field id is required and must be a string")
		}
	}
	switch {
	case header.Type == NativeMCPType && header.Version == NativeMCPVersion:
		var native NativeMCP
		if err := decodeStrict(raw, &native); err != nil {
			return Capability{}, err
		}
		return Capability{Type: native.Type, Version: native.Version, ID: native.ID, NativeMCP: &native}, nil
	case header.Type == ChannelAdapterType && header.Version == ChannelAdapterVersion:
		var adapter ChannelAdapter
		if err := decodeStrict(raw, &adapter); err != nil {
			return Capability{}, err
		}
		return Capability{Type: adapter.Type, Version: adapter.Version, ID: adapter.ID, ChannelAdapter: &adapter}, nil
	default:
		return Capability{}, UnsupportedCapabilityError{Type: header.Type, Version: header.Version}
	}
}

func decodeStrict(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("expected one JSON document")
	}
	return nil
}

// Validate validates every envelope and capability field without reading or
// running an artifact.
func (manifest Manifest) Validate() error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("integration manifest schema_version must be %d", SchemaVersion)
	}
	if err := ValidatePackageID(manifest.ID); err != nil {
		return err
	}
	if err := version.Validate(manifest.Version); err != nil {
		return errors.New("integration package version must be an exact semantic version")
	}
	if err := validateText("integration package name", manifest.Name, 1, 128); err != nil {
		return err
	}
	if err := validateText("integration package description", manifest.Description, 1, 1024); err != nil {
		return err
	}
	if err := validateText("integration package license", manifest.License, 1, 256); err != nil {
		return err
	}
	if err := validateHTTPS("integration package provenance source", manifest.Provenance.Source); err != nil {
		return err
	}
	if err := validateText("integration package provenance revision", manifest.Provenance.Revision, 1, 256); err != nil {
		return err
	}
	if err := manifest.Compatibility.Validate(); err != nil {
		return err
	}
	if len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > maxArtifacts {
		return fmt.Errorf("integration manifest must contain 1-%d artifacts", maxArtifacts)
	}
	artifacts := make(map[string]Artifact, len(manifest.Artifacts))
	for index, artifact := range manifest.Artifacts {
		if err := artifact.Validate(); err != nil {
			return fmt.Errorf("artifacts[%d]: %w", index, err)
		}
		if _, exists := artifacts[artifact.ID]; exists {
			return fmt.Errorf("integration artifact id %q is duplicated", artifact.ID)
		}
		artifacts[artifact.ID] = artifact
	}
	if len(manifest.Capabilities) == 0 || len(manifest.Capabilities) > maxCapabilities {
		return fmt.Errorf("integration manifest must contain 1-%d capabilities", maxCapabilities)
	}
	capabilities := make(map[string]bool, len(manifest.Capabilities))
	for index, capability := range manifest.Capabilities {
		if ValidateCapabilityID(capability.ID) != nil {
			return fmt.Errorf("capabilities[%d]: capability id is invalid", index)
		}
		if capabilities[capability.ID] {
			return fmt.Errorf("integration capability id %q is duplicated", capability.ID)
		}
		capabilities[capability.ID] = true
		switch {
		case capability.Type == NativeMCPType && capability.Version == NativeMCPVersion && capability.NativeMCP != nil && capability.ChannelAdapter == nil:
			if capability.NativeMCP.ID != capability.ID || capability.NativeMCP.Type != capability.Type || capability.NativeMCP.Version != capability.Version {
				return fmt.Errorf("capabilities[%d]: capability tags are inconsistent", index)
			}
			if err := capability.NativeMCP.Validate(artifacts); err != nil {
				return fmt.Errorf("capabilities[%d]: %w", index, err)
			}
		case capability.Type == ChannelAdapterType && capability.Version == ChannelAdapterVersion && capability.ChannelAdapter != nil && capability.NativeMCP == nil:
			if capability.ChannelAdapter.ID != capability.ID || capability.ChannelAdapter.Type != capability.Type || capability.ChannelAdapter.Version != capability.Version {
				return fmt.Errorf("capabilities[%d]: capability tags are inconsistent", index)
			}
			if err := capability.ChannelAdapter.Validate(artifacts); err != nil {
				return fmt.Errorf("capabilities[%d]: %w", index, err)
			}
		default:
			return fmt.Errorf("capabilities[%d]: %w", index, UnsupportedCapabilityError{Type: capability.Type, Version: capability.Version})
		}
	}
	return nil
}

func (compatibility Compatibility) Validate() error {
	if version.Validate(compatibility.Minimum) != nil || version.Validate(compatibility.Before) != nil {
		return errors.New("integration hctl compatibility endpoints must be exact semantic versions")
	}
	compared, err := version.Compare(compatibility.Minimum, compatibility.Before)
	if err != nil || compared >= 0 {
		return errors.New("integration hctl compatibility minimum must precede before")
	}
	return nil
}

// Contains reports whether an exact hctl version is inside the half-open
// compatibility interval.
func (compatibility Compatibility) Contains(value string) (bool, error) {
	if err := compatibility.Validate(); err != nil {
		return false, err
	}
	minimum, err := version.Compare(value, compatibility.Minimum)
	if err != nil {
		return false, err
	}
	before, err := version.Compare(value, compatibility.Before)
	if err != nil {
		return false, err
	}
	return minimum >= 0 && before < 0, nil
}

func (artifact Artifact) Validate() error {
	if !artifactIDPattern.MatchString(artifact.ID) || len(artifact.ID) > 64 {
		return errors.New("artifact id is invalid")
	}
	if artifact.OS != OSDarwin && artifact.OS != OSLinux {
		return errors.New("artifact os must be darwin or linux")
	}
	if artifact.Architecture != ArchitectureARM64 && artifact.Architecture != ArchitectureAMD64 {
		return errors.New("artifact architecture must be arm64 or amd64")
	}
	if artifact.Format != FormatBinary && artifact.Format != FormatTarGZ && artifact.Format != FormatZIP {
		return errors.New("artifact format must be binary, tar.gz, or zip")
	}
	if err := artifact.Source.Validate(); err != nil {
		return err
	}
	if artifact.Size <= 0 || artifact.Size > maxArtifactBytes {
		return errors.New("artifact size is invalid")
	}
	if !checksumPattern.MatchString(artifact.SHA256) {
		return errors.New("artifact sha256 must be a lowercase SHA-256")
	}
	if _, err := rootfs.CleanRelative(artifact.Executable.Path); err != nil {
		return errors.New("artifact executable path must be package-relative")
	}
	if artifact.Executable.Size <= 0 || artifact.Executable.Size > maxExecutableBytes {
		return errors.New("artifact executable size is invalid")
	}
	if !checksumPattern.MatchString(artifact.Executable.SHA256) {
		return errors.New("artifact executable sha256 must be a lowercase SHA-256")
	}
	if artifact.Format == FormatBinary && (artifact.Size != artifact.Executable.Size || artifact.SHA256 != artifact.Executable.SHA256) {
		return errors.New("binary artifact and executable identities must match")
	}
	return nil
}

func (source ArtifactSource) Validate() error {
	switch source.Kind {
	case SourcePackage:
		if source.URL != "" {
			return errors.New("package artifact source must not contain url")
		}
		if _, err := rootfs.CleanRelative(source.Path); err != nil {
			return errors.New("package artifact source path must be package-relative")
		}
	case SourceHTTPS:
		if source.Path != "" {
			return errors.New("HTTPS artifact source must not contain path")
		}
		if err := validateHTTPS("artifact source url", source.URL); err != nil {
			return err
		}
	default:
		return errors.New("artifact source kind must be package or https")
	}
	return nil
}

func (native NativeMCP) Validate(artifacts map[string]Artifact) error {
	if native.Type != NativeMCPType || native.Version != NativeMCPVersion {
		return UnsupportedCapabilityError{Type: native.Type, Version: native.Version}
	}
	if !capabilityID.MatchString(native.ID) || len(native.ID) > 64 {
		return errors.New("native-mcp id is invalid")
	}
	if !serverNamePattern.MatchString(native.ServerName) || native.ServerName == "managed" {
		return errors.New("native-mcp server_name is invalid or reserved")
	}
	if native.Collision != CollisionReject {
		return errors.New("native-mcp collision must be reject")
	}
	if len(native.Artifacts) == 0 || len(native.Artifacts) > maxArtifacts {
		return fmt.Errorf("native-mcp artifacts must contain 1-%d ids", maxArtifacts)
	}
	if _, err := rootfs.CleanRelative(native.Executable); err != nil {
		return errors.New("native-mcp executable must be package-relative")
	}
	seenArtifacts := map[string]bool{}
	seenPlatforms := map[string]bool{}
	for _, id := range native.Artifacts {
		if seenArtifacts[id] {
			return fmt.Errorf("native-mcp artifact %q is duplicated", id)
		}
		seenArtifacts[id] = true
		artifact, ok := artifacts[id]
		if !ok {
			return fmt.Errorf("native-mcp artifact %q is not declared", id)
		}
		if artifact.Executable.Path != native.Executable {
			return fmt.Errorf("native-mcp executable does not match artifact %q", id)
		}
		platform := string(artifact.OS) + "/" + string(artifact.Architecture)
		if seenPlatforms[platform] {
			return fmt.Errorf("native-mcp has ambiguous artifacts for %s", platform)
		}
		seenPlatforms[platform] = true
	}
	if len(native.Arguments) > maxArguments {
		return fmt.Errorf("native-mcp arguments may contain at most %d values", maxArguments)
	}
	for index, argument := range native.Arguments {
		if err := validateLiteral("native-mcp argument", argument, maxArgumentBytes); err != nil {
			return fmt.Errorf("arguments[%d]: %w", index, err)
		}
	}
	if native.WorkingDirectory != "." {
		if _, err := rootfs.CleanRelative(native.WorkingDirectory); err != nil {
			return errors.New("native-mcp working_directory must be package-relative or the package root")
		}
	}
	if len(native.Environment) > maxEnvironment {
		return fmt.Errorf("native-mcp environment may contain at most %d values", maxEnvironment)
	}
	for name, value := range native.Environment {
		if !environmentName.MatchString(name) {
			return fmt.Errorf("native-mcp environment name %q is invalid", name)
		}
		if err := validateLiteral("native-mcp environment value", value, maxEnvironmentBytes); err != nil {
			return fmt.Errorf("environment.%s: %w", name, err)
		}
	}
	if len(native.RequiredEnvironment) > maxRequiredEnv {
		return fmt.Errorf("native-mcp required_environment may contain at most %d values", maxRequiredEnv)
	}
	seenEnvironment := map[string]bool{}
	for index, requirement := range native.RequiredEnvironment {
		if !environmentName.MatchString(requirement.Name) {
			return fmt.Errorf("required_environment[%d]: name is invalid", index)
		}
		if _, exists := native.Environment[requirement.Name]; exists {
			return fmt.Errorf("required environment %q also has a default", requirement.Name)
		}
		if seenEnvironment[requirement.Name] {
			return fmt.Errorf("required environment %q is duplicated", requirement.Name)
		}
		seenEnvironment[requirement.Name] = true
		if err := validateEnvironmentDescription(requirement.Description); err != nil {
			return fmt.Errorf("required_environment[%d]: %w", index, err)
		}
	}
	if len(native.Harnesses) == 0 || len(native.Harnesses) > 2 {
		return errors.New("native-mcp harnesses must contain one or two targets")
	}
	seenHarnesses := map[string]bool{}
	for index, target := range native.Harnesses {
		if target.Name != "claude" && target.Name != "codex" {
			return fmt.Errorf("harnesses[%d]: name must be claude or codex", index)
		}
		if seenHarnesses[target.Name] {
			return fmt.Errorf("native-mcp harness %q is duplicated", target.Name)
		}
		seenHarnesses[target.Name] = true
		if target.Startup != StartupOptional && target.Startup != StartupRequired {
			return fmt.Errorf("harnesses[%d]: startup must be optional or required", index)
		}
		if target.Trust != TrustNativeProject {
			return fmt.Errorf("harnesses[%d]: trust must be native-project", index)
		}
	}
	return nil
}

func (adapter ChannelAdapter) Validate(artifacts map[string]Artifact) error {
	if adapter.Type != ChannelAdapterType || adapter.Version != ChannelAdapterVersion {
		return UnsupportedCapabilityError{Type: adapter.Type, Version: adapter.Version}
	}
	if !capabilityID.MatchString(adapter.ID) || len(adapter.ID) > 64 {
		return errors.New("channel-adapter id is invalid")
	}
	if !capabilityID.MatchString(adapter.ChannelKind) || len(adapter.ChannelKind) > 64 {
		return errors.New("channel-adapter channel_kind is invalid")
	}
	if len(adapter.Artifacts) == 0 || len(adapter.Artifacts) > maxArtifacts {
		return fmt.Errorf("channel-adapter artifacts must contain 1-%d ids", maxArtifacts)
	}
	if _, err := rootfs.CleanRelative(adapter.Executable); err != nil {
		return errors.New("channel-adapter executable must be package-relative")
	}
	seenArtifacts := map[string]bool{}
	seenPlatforms := map[string]bool{}
	for _, id := range adapter.Artifacts {
		if seenArtifacts[id] {
			return fmt.Errorf("channel-adapter artifact %q is duplicated", id)
		}
		seenArtifacts[id] = true
		artifact, ok := artifacts[id]
		if !ok {
			return fmt.Errorf("channel-adapter artifact %q is not declared", id)
		}
		if artifact.Executable.Path != adapter.Executable {
			return fmt.Errorf("channel-adapter executable does not match artifact %q", id)
		}
		platform := string(artifact.OS) + "/" + string(artifact.Architecture)
		if seenPlatforms[platform] {
			return fmt.Errorf("channel-adapter has ambiguous artifacts for %s", platform)
		}
		seenPlatforms[platform] = true
	}
	commands := []struct {
		name    string
		command ChannelAdapterCommand
	}{
		{name: "runtime", command: adapter.Runtime},
		{name: "setup", command: adapter.Setup},
		{name: "status", command: adapter.Status},
		{name: "remove", command: adapter.Remove},
	}
	for _, entry := range commands {
		if len(entry.command.Arguments) == 0 || len(entry.command.Arguments) > maxArguments {
			return fmt.Errorf("channel-adapter %s arguments must contain 1-%d values", entry.name, maxArguments)
		}
		for index, argument := range entry.command.Arguments {
			if argument == "--profile" {
				return fmt.Errorf("channel-adapter %s arguments reserve --profile for hctl", entry.name)
			}
			if err := validateLiteral("channel-adapter argument", argument, maxArgumentBytes); err != nil || argument == "" {
				if err == nil {
					err = errors.New("channel-adapter argument must not be empty")
				}
				return fmt.Errorf("%s.arguments[%d]: %w", entry.name, index, err)
			}
		}
	}
	if adapter.Protocol.Minimum < 1 || adapter.Protocol.Before <= adapter.Protocol.Minimum || adapter.Protocol.Minimum > ChannelAdapterProtocolVersion || adapter.Protocol.Before <= ChannelAdapterProtocolVersion {
		return fmt.Errorf("channel-adapter protocol range must include version %d", ChannelAdapterProtocolVersion)
	}
	if adapter.ProfileSelector != ProfileOpaqueID {
		return errors.New("channel-adapter profile_selector must be opaque-id-v1")
	}
	if len(adapter.Features) == 0 || len(adapter.Features) > 7 {
		return errors.New("channel-adapter features must contain 1-7 values")
	}
	allowedFeatures := map[ChannelFeature]bool{
		FeatureTyping: true, FeatureReplies: true, FeatureEdits: true,
		FeatureReactions: true, FeatureAttachments: true,
		FeatureInteractive: true, FeatureTextFallback: true,
	}
	seenFeatures := map[ChannelFeature]bool{}
	for _, feature := range adapter.Features {
		if !allowedFeatures[feature] {
			return fmt.Errorf("channel-adapter feature %q is unsupported", feature)
		}
		if seenFeatures[feature] {
			return fmt.Errorf("channel-adapter feature %q is duplicated", feature)
		}
		seenFeatures[feature] = true
	}
	return nil
}

// SelectNativeMCP resolves exact immutable metadata for one supported platform
// without finding, opening, or executing an installed artifact.
func (pkg Package) SelectNativeMCP(id, targetOS, targetArchitecture string) (NativeMCPSelection, error) {
	if !checksumPattern.MatchString(pkg.sha256) {
		return NativeMCPSelection{}, errors.New("integration package manifest identity is invalid")
	}
	for _, capability := range pkg.manifest.Capabilities {
		if capability.ID != id || capability.NativeMCP == nil {
			continue
		}
		for _, artifactID := range capability.NativeMCP.Artifacts {
			for _, artifact := range pkg.manifest.Artifacts {
				if artifact.ID == artifactID && string(artifact.OS) == targetOS && string(artifact.Architecture) == targetArchitecture {
					return NativeMCPSelection{
						PackageID:      pkg.manifest.ID,
						PackageVersion: pkg.manifest.Version,
						ManifestSHA256: pkg.sha256,
						Capability:     cloneNativeMCP(*capability.NativeMCP),
						Artifact:       artifact,
					}, nil
				}
			}
		}
		return NativeMCPSelection{}, fmt.Errorf("native-mcp capability %q does not support %s/%s", id, targetOS, targetArchitecture)
	}
	return NativeMCPSelection{}, fmt.Errorf("native-mcp capability %q is not declared", id)
}

// SelectChannelAdapter resolves exact immutable metadata for one supported
// platform without finding, opening, or executing an installed artifact.
func (pkg Package) SelectChannelAdapter(id, targetOS, targetArchitecture string) (ChannelAdapterSelection, error) {
	if !checksumPattern.MatchString(pkg.sha256) {
		return ChannelAdapterSelection{}, errors.New("integration package manifest identity is invalid")
	}
	for _, capability := range pkg.manifest.Capabilities {
		if capability.ID != id || capability.ChannelAdapter == nil {
			continue
		}
		for _, artifactID := range capability.ChannelAdapter.Artifacts {
			for _, artifact := range pkg.manifest.Artifacts {
				if artifact.ID == artifactID && string(artifact.OS) == targetOS && string(artifact.Architecture) == targetArchitecture {
					return ChannelAdapterSelection{
						PackageID: pkg.manifest.ID, PackageVersion: pkg.manifest.Version,
						ManifestSHA256: pkg.sha256, Capability: cloneChannelAdapter(*capability.ChannelAdapter), Artifact: artifact,
					}, nil
				}
			}
		}
		return ChannelAdapterSelection{}, fmt.Errorf("channel-adapter capability %q does not support %s/%s", id, targetOS, targetArchitecture)
	}
	return ChannelAdapterSelection{}, fmt.Errorf("channel-adapter capability %q is not declared", id)
}

// Validate binds operator-owned install, trust, and enable state to exact
// identities from one immutable package manifest.
func (state InstallationState) Validate(pkg Package) error {
	if state.SchemaVersion != InstallationStateVersion {
		return fmt.Errorf("integration installation schema_version must be %d", InstallationStateVersion)
	}
	if state.PackageID != pkg.manifest.ID || state.PackageVersion != pkg.manifest.Version || state.ManifestSHA256 != pkg.sha256 {
		return errors.New("integration installation package identity does not match manifest")
	}
	if state.Trust != TrustOperator {
		return errors.New("integration installation trust must be operator")
	}
	if len(state.Artifacts) == 0 || len(state.Artifacts) > len(pkg.manifest.Artifacts) {
		return errors.New("integration installation artifacts are empty or exceed the manifest")
	}
	manifestArtifacts := make(map[string]Artifact, len(pkg.manifest.Artifacts))
	for _, artifact := range pkg.manifest.Artifacts {
		manifestArtifacts[artifact.ID] = artifact
	}
	seenArtifacts := map[string]bool{}
	for _, installed := range state.Artifacts {
		if seenArtifacts[installed.ID] {
			return fmt.Errorf("integration installation artifact %q is duplicated", installed.ID)
		}
		seenArtifacts[installed.ID] = true
		artifact, ok := manifestArtifacts[installed.ID]
		if !ok || installed.SHA256 != artifact.SHA256 || installed.ExecutableSHA256 != artifact.Executable.SHA256 {
			return fmt.Errorf("integration installation artifact %q does not match manifest", installed.ID)
		}
	}
	if len(state.Capabilities) == 0 || len(state.Capabilities) != len(pkg.manifest.Capabilities) {
		return errors.New("integration installation capabilities do not match the manifest")
	}
	manifestCapabilities := make(map[string]Capability, len(pkg.manifest.Capabilities))
	for _, capability := range pkg.manifest.Capabilities {
		manifestCapabilities[capability.ID] = capability
	}
	seenCapabilities := map[string]bool{}
	for _, installed := range state.Capabilities {
		if seenCapabilities[installed.ID] {
			return fmt.Errorf("integration installation capability %q is duplicated", installed.ID)
		}
		seenCapabilities[installed.ID] = true
		capability, ok := manifestCapabilities[installed.ID]
		if !ok || installed.Type != capability.Type || installed.Version != capability.Version {
			return fmt.Errorf("integration installation capability %q does not match manifest", installed.ID)
		}
	}
	return nil
}

func cloneManifest(manifest Manifest) Manifest {
	cloned := manifest
	cloned.Artifacts = append([]Artifact(nil), manifest.Artifacts...)
	cloned.Capabilities = make([]Capability, len(manifest.Capabilities))
	for index, capability := range manifest.Capabilities {
		cloned.Capabilities[index] = capability
		if capability.NativeMCP != nil {
			native := cloneNativeMCP(*capability.NativeMCP)
			cloned.Capabilities[index].NativeMCP = &native
		}
		if capability.ChannelAdapter != nil {
			adapter := cloneChannelAdapter(*capability.ChannelAdapter)
			cloned.Capabilities[index].ChannelAdapter = &adapter
		}
	}
	return cloned
}

func cloneChannelAdapter(adapter ChannelAdapter) ChannelAdapter {
	cloned := adapter
	cloned.Artifacts = append([]string(nil), adapter.Artifacts...)
	cloned.Runtime.Arguments = append([]string(nil), adapter.Runtime.Arguments...)
	cloned.Setup.Arguments = append([]string(nil), adapter.Setup.Arguments...)
	cloned.Status.Arguments = append([]string(nil), adapter.Status.Arguments...)
	cloned.Remove.Arguments = append([]string(nil), adapter.Remove.Arguments...)
	cloned.Features = append([]ChannelFeature(nil), adapter.Features...)
	return cloned
}

func cloneNativeMCP(native NativeMCP) NativeMCP {
	cloned := native
	cloned.Artifacts = append([]string(nil), native.Artifacts...)
	cloned.Arguments = append([]string(nil), native.Arguments...)
	cloned.Environment = make(map[string]string, len(native.Environment))
	for name, value := range native.Environment {
		cloned.Environment[name] = value
	}
	cloned.RequiredEnvironment = append([]EnvironmentRequirement(nil), native.RequiredEnvironment...)
	cloned.Harnesses = append([]NativeHarnessTarget(nil), native.Harnesses...)
	return cloned
}

func validateHTTPS(label, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be an HTTPS URL without credentials, query, or fragment", label)
	}
	return nil
}

func validateText(label, value string, minimum, maximum int) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be trimmed valid UTF-8", label)
	}
	length := len([]rune(value))
	if length < minimum || length > maximum {
		return fmt.Errorf("%s must contain %d-%d characters", label, minimum, maximum)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s must not contain control characters", label)
		}
	}
	return nil
}

func validateLiteral(label, value string, maximum int) error {
	if !utf8.ValidString(value) || len(value) > maximum {
		return fmt.Errorf("%s must be valid UTF-8 and at most %d bytes", label, maximum)
	}
	if strings.Contains(value, "${") {
		return fmt.Errorf("%s must not contain environment placeholders", label)
	}
	for _, character := range value {
		if character == 0 || character == '\r' || character == '\n' {
			return fmt.Errorf("%s must not contain NUL or line breaks", label)
		}
	}
	return nil
}

func validateEnvironmentDescription(value string) error {
	if !environmentDescription.MatchString(value) || strings.TrimSpace(value) != value {
		return errors.New("required environment description must use safe prose without value or reference syntax")
	}
	return nil
}
