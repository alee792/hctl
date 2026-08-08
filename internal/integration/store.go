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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"hctl/internal/rootfs"
	"hctl/internal/version"
)

const (
	packageManifestName = "integration.json"
	maxPackageArchive   = int64(2 << 30)
	maxPreparedEntries  = 65536
	maxPreparedBytes    = int64(2 << 30)
	preparedReceipt     = ".hctl-artifact.json"
)

// Store owns machine-local integration installation state and immutable
// content-addressed package artifacts. It never loads or executes package
// code. One store is shared by every agent and workspace of the OS user.
type Store struct {
	mutex       sync.Mutex
	root        string
	client      *http.Client
	targetOS    string
	targetArch  string
	hctlVersion string
}

// InstallOptions makes machine trust and replacement intent explicit.
type InstallOptions struct {
	Source string
	Trust  InstallationTrust
	Update string
}

// Installed describes one verified catalog entry without credential or
// runtime values.
type Installed struct {
	Package Package
	State   InstallationState
}

// NewStore returns a store rooted at an explicit path. A nil client uses a
// bounded HTTPS client. Explicit roots make credential-free tests and
// operator-managed deployments deterministic.
func NewStore(root string, client *http.Client) *Store {
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	if clientCopy.Timeout == 0 {
		clientCopy.Timeout = 10 * time.Minute
	}
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return errors.New("integration artifact redirects are not allowed")
	}
	return &Store{root: root, client: &clientCopy, targetOS: runtime.GOOS, targetArch: runtime.GOARCH, hctlVersion: version.Value}
}

// NewDefaultStore uses owner-only state beneath the OS user configuration
// directory. It does not consult portable agent source or a workspace.
func NewDefaultStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		return nil, errors.New("cannot resolve integration package state directory")
	}
	return NewStore(filepath.Join(base, "hctl", "integrations"), nil), nil
}

// Install validates and prepares the exact current-platform artifacts before
// atomically publishing operator-owned installation metadata. Existing ids
// never drift: a different identity requires Update to name that id.
func (s *Store) Install(ctx context.Context, options InstallOptions) (Installed, error) {
	if options.Trust != TrustOperator {
		return Installed{}, errors.New("integration install requires explicit --trust operator")
	}
	if strings.TrimSpace(options.Source) == "" {
		return Installed{}, errors.New("integration package source is required")
	}
	var result Installed
	err := s.locked(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, err := openPackageSource(ctx, options.Source)
		if err != nil {
			return err
		}
		defer source.close()
		pkg, err := Decode(bytes.NewReader(source.manifest))
		if err != nil {
			return err
		}
		manifest := pkg.Manifest()
		compatible, err := manifest.Compatibility.Contains(s.hctlVersion)
		if err != nil || !compatible {
			return fmt.Errorf("integration package %s@%s does not support hctl %s", manifest.ID, manifest.Version, s.hctlVersion)
		}
		current, found, err := s.loadInstalled(manifest.ID)
		if err != nil {
			return err
		}
		if options.Update != "" {
			if options.Update != manifest.ID {
				return errors.New("integration update source id does not match the selected package")
			}
			if !found {
				return fmt.Errorf("integration package %q is not installed; use install", manifest.ID)
			}
		} else if found && current.Package.Identity() != pkg.Identity() {
			return fmt.Errorf("integration package %q already has a different exact identity; use update", manifest.ID)
		}

		artifacts := currentPlatformArtifacts(manifest, s.targetOS, s.targetArch)
		if len(artifacts) == 0 {
			return fmt.Errorf("integration package %s@%s does not support %s/%s", manifest.ID, manifest.Version, s.targetOS, s.targetArch)
		}
		installedArtifacts := make([]InstalledArtifactIdentity, 0, len(artifacts))
		for _, artifact := range artifacts {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.cacheArtifact(ctx, source.root, artifact); err != nil {
				return fmt.Errorf("artifact %q: %w", artifact.ID, err)
			}
			installedArtifacts = append(installedArtifacts, InstalledArtifactIdentity{ID: artifact.ID, SHA256: artifact.SHA256, ExecutableSHA256: artifact.Executable.SHA256})
		}
		capabilities := make([]InstalledCapabilityIdentity, 0, len(manifest.Capabilities))
		for _, capability := range manifest.Capabilities {
			capabilities = append(capabilities, InstalledCapabilityIdentity{ID: capability.ID, Type: capability.Type, Version: capability.Version})
		}
		state := InstallationState{
			SchemaVersion: InstallationStateVersion,
			PackageID:     manifest.ID, PackageVersion: manifest.Version, ManifestSHA256: pkg.Identity(),
			Trust: TrustOperator, Enabled: true, Artifacts: installedArtifacts, Capabilities: capabilities,
		}
		if err := state.Validate(pkg); err != nil {
			return err
		}
		if err := s.publishImmutable("manifests/"+pkg.Identity()+".json", source.manifest, 0o400); err != nil {
			return err
		}
		if err := s.writeState(state); err != nil {
			return err
		}
		result = Installed{Package: pkg, State: cloneInstallationState(state)}
		return nil
	})
	return result, err
}

// List returns all exact installed entries in package-id order. It verifies
// their metadata bindings but does not execute or contact a source.
func (s *Store) List(ctx context.Context) ([]Installed, error) {
	var entries []Installed
	err := s.locked(ctx, func() error {
		directory := filepath.Join(s.root, "installed")
		items, err := os.ReadDir(directory)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return errors.New("cannot read integration package catalog")
		}
		for _, item := range items {
			if item.IsDir() || filepath.Ext(item.Name()) != ".json" {
				return errors.New("integration package catalog contains an unsafe entry")
			}
			id := strings.TrimSuffix(item.Name(), ".json")
			entry, found, err := s.loadInstalled(id)
			if err != nil {
				return err
			}
			if !found {
				return errors.New("integration package catalog changed during inspection")
			}
			entries = append(entries, entry)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].State.PackageID < entries[j].State.PackageID })
		return nil
	})
	return entries, err
}

// Inspect returns one installed entry without verifying artifact contents.
func (s *Store) Inspect(ctx context.Context, id string) (Installed, error) {
	var result Installed
	err := s.locked(ctx, func() error {
		entry, found, err := s.loadInstalled(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("integration package %q is not installed", id)
		}
		result = entry
		return nil
	})
	return result, err
}

// Verify proves that manifest, raw artifacts, prepared closure files, and
// executable identities still match the exact operator-owned catalog entry.
// It performs no network request.
func (s *Store) Verify(ctx context.Context, id string) (Installed, error) {
	var result Installed
	err := s.locked(ctx, func() error {
		entry, found, err := s.loadInstalled(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("integration package %q is not installed", id)
		}
		for _, identity := range entry.State.Artifacts {
			artifact, ok := artifactByID(entry.Package.Manifest(), identity.ID)
			if !ok {
				return errors.New("integration installation artifact no longer matches its manifest")
			}
			if err := s.verifyCachedArtifact(artifact); err != nil {
				return fmt.Errorf("integration package %q is corrupt: %w; reinstall or update from the exact trusted source", id, err)
			}
		}
		result = entry
		return nil
	})
	return result, err
}

// SetEnabled changes only operator-owned enablement after offline
// verification. Disabled packages cannot resolve into future apply/stage
// closures.
func (s *Store) SetEnabled(ctx context.Context, id string, enabled bool) error {
	return s.locked(ctx, func() error {
		entry, found, err := s.loadInstalled(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("integration package %q is not installed", id)
		}
		if enabled {
			for _, identity := range entry.State.Artifacts {
				artifact, _ := artifactByID(entry.Package.Manifest(), identity.ID)
				if err := s.verifyCachedArtifact(artifact); err != nil {
					return fmt.Errorf("cannot enable corrupt integration package %q; reinstall or update it", id)
				}
			}
		}
		entry.State.Enabled = enabled
		return s.writeState(entry.State)
	})
}

// Remove retires exact catalog metadata and future resolution. Shared
// content-addressed blobs remain available to other entries and later trusted
// installs.
func (s *Store) Remove(ctx context.Context, id string) error {
	return s.locked(ctx, func() error {
		_, found, err := s.loadInstalled(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("integration package %q is not installed", id)
		}
		if err := s.removeConsumption(id); err != nil {
			return err
		}
		path := filepath.Join(s.root, "installed", id+".json")
		if err := os.Remove(path); err != nil {
			return errors.New("cannot remove integration package metadata")
		}
		return nil
	})
}

func (s *Store) locked(ctx context.Context, operation func() error) error {
	if s == nil {
		return errors.New("integration package store is not configured")
	}
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if strings.TrimSpace(s.root) == "" {
		return errors.New("integration package store is not configured")
	}
	root, err := filepath.Abs(s.root)
	if err != nil || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return errors.New("integration package store path is invalid")
	}
	s.root = root
	if err := os.MkdirAll(root, 0o700); err != nil {
		return errors.New("cannot create integration package store")
	}
	if err := requireOwnedDirectory(root, true); err != nil {
		return err
	}
	lockPath := filepath.Join(root, ".lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("integration package store lock is unsafe")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect integration package store lock")
	}
	guard := flock.New(lockPath)
	locked, err := guard.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil || !locked {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("cannot lock integration package store")
	}
	defer func() { _ = guard.Unlock() }()
	if err := os.Chmod(lockPath, 0o600); err != nil {
		return errors.New("cannot protect integration package store lock")
	}
	return operation()
}

func (s *Store) loadInstalled(id string) (Installed, bool, error) {
	if !packageIDPattern.MatchString(id) || len(id) > 128 {
		return Installed{}, false, errors.New("integration package id is invalid")
	}
	data, mode, found, err := rootfs.ReadOptional(s.root, "installed/"+id+".json", maxManifestBytes)
	if err != nil || !found {
		return Installed{}, found, err
	}
	if mode.Perm()&0o077 != 0 {
		return Installed{}, false, errors.New("integration installation state permissions are too broad")
	}
	var state InstallationState
	if err := decodeStrict(data, &state); err != nil {
		return Installed{}, false, errors.New("integration installation state is invalid")
	}
	manifestBytes, manifestMode, exists, err := rootfs.ReadOptional(s.root, "manifests/"+state.ManifestSHA256+".json", maxManifestBytes)
	if err != nil || !exists {
		return Installed{}, false, errors.New("integration installation manifest is missing")
	}
	if manifestMode.Perm()&0o077 != 0 {
		return Installed{}, false, errors.New("integration installation manifest permissions are too broad")
	}
	pkg, err := Decode(bytes.NewReader(manifestBytes))
	if err != nil || pkg.Identity() != state.ManifestSHA256 || state.Validate(pkg) != nil {
		return Installed{}, false, errors.New("integration installation state does not match its immutable manifest")
	}
	return Installed{Package: pkg, State: cloneInstallationState(state)}, true, nil
}

func (s *Store) writeState(state InstallationState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errors.New("cannot encode integration installation state")
	}
	if err := rootfs.EnsurePrivateDir(s.root, "installed"); err != nil {
		return err
	}
	return rootfs.WriteAtomic(s.root, "installed/"+state.PackageID+".json", append(data, '\n'), 0o600)
}

func (s *Store) publishImmutable(relative string, data []byte, mode os.FileMode) error {
	if existing, existingMode, found, err := rootfs.ReadOptional(s.root, relative, int64(len(data))); err == nil && found {
		if existingMode.Perm()&0o077 != 0 || !bytes.Equal(existing, data) {
			return errors.New("immutable integration cache entry is corrupt")
		}
		return nil
	} else if err != nil {
		return err
	}
	parent := filepath.ToSlash(filepath.Dir(relative))
	if err := rootfs.EnsurePrivateDir(s.root, parent); err != nil {
		return err
	}
	if err := rootfs.WriteAtomicExclusive(s.root, relative, data, mode); err != nil {
		return errors.New("cannot publish immutable integration cache entry")
	}
	return nil
}

func (s *Store) cacheArtifact(ctx context.Context, packageRoot string, artifact Artifact) error {
	blobRelative := "blobs/" + artifact.SHA256
	if err := s.verifyBlob(artifact); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var data []byte
		var readErr error
		switch artifact.Source.Kind {
		case SourcePackage:
			if packageRoot == "" {
				return errors.New("package payload is unavailable; provide the exact local package source")
			}
			data, readErr = readOwnedSource(packageRoot, artifact.Source.Path, artifact.Size)
		case SourceHTTPS:
			data, readErr = s.fetch(ctx, artifact)
		default:
			readErr = errors.New("artifact source is unsupported")
		}
		if readErr != nil {
			return readErr
		}
		if err := verifyBytes(data, artifact.Size, artifact.SHA256); err != nil {
			return err
		}
		if err := s.publishImmutable(blobRelative, data, 0o400); err != nil {
			return err
		}
	}
	if err := s.verifyPreparedArtifact(artifact); err == nil {
		return nil
	}
	data, _, _, err := rootfs.ReadOptional(s.root, blobRelative, artifact.Size)
	if err != nil {
		return errors.New("cannot read verified integration artifact")
	}
	return s.prepareArtifact(ctx, artifact, data)
}

func (s *Store) fetch(ctx context.Context, artifact Artifact) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.Source.URL, nil)
	if err != nil {
		return nil, errors.New("cannot construct integration artifact request")
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, errors.New("cannot fetch pinned integration artifact")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.Request == nil || response.Request.URL.String() != artifact.Source.URL {
		return nil, errors.New("pinned integration artifact request was not an exact successful response")
	}
	if response.ContentLength >= 0 && response.ContentLength != artifact.Size {
		return nil, errors.New("integration artifact size does not match manifest")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, artifact.Size+1))
	if err != nil {
		return nil, errors.New("cannot read pinned integration artifact")
	}
	return data, nil
}

func (s *Store) verifyBlob(artifact Artifact) error {
	data, mode, found, err := rootfs.ReadOptional(s.root, "blobs/"+artifact.SHA256, artifact.Size)
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	if mode.Perm()&0o077 != 0 {
		return errors.New("cached integration artifact permissions are too broad")
	}
	return verifyBytes(data, artifact.Size, artifact.SHA256)
}

type preparedFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

type preparedArtifactReceipt struct {
	SchemaVersion int            `json:"schema_version"`
	Artifact      string         `json:"artifact_sha256"`
	Files         []preparedFile `json:"files"`
}

func (s *Store) prepareArtifact(ctx context.Context, artifact Artifact, data []byte) error {
	parent := filepath.Join(s.root, "prepared")
	if err := rootfs.EnsurePrivateDir(s.root, "prepared"); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".prepare-")
	if err != nil {
		return errors.New("cannot create integration artifact preparation directory")
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if err := os.Chmod(temporary, 0o700); err != nil {
		return errors.New("cannot protect integration artifact preparation directory")
	}
	switch artifact.Format {
	case FormatBinary:
		if err := writePreparedFile(temporary, artifact.Executable.Path, data, 0o500); err != nil {
			return err
		}
	case FormatTarGZ:
		if err := extractTarGZ(ctx, temporary, data); err != nil {
			return err
		}
	case FormatZIP:
		if err := extractZIP(ctx, temporary, data); err != nil {
			return err
		}
	default:
		return errors.New("integration artifact format is unsupported")
	}
	if err := normalizeExecutable(temporary, artifact.Executable); err != nil {
		return err
	}
	receipt, err := collectPrepared(temporary, artifact.SHA256)
	if err != nil {
		return err
	}
	receiptBytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return errors.New("cannot encode integration artifact receipt")
	}
	if err := writePreparedFile(temporary, preparedReceipt, append(receiptBytes, '\n'), 0o400); err != nil {
		return err
	}
	target := filepath.Join(parent, artifact.SHA256)
	if _, err := os.Lstat(target); err == nil {
		if err := os.RemoveAll(target); err != nil {
			return errors.New("cannot retire corrupt prepared integration artifact")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect prepared integration artifact")
	}
	if err := os.Rename(temporary, target); err != nil {
		return errors.New("cannot publish prepared integration artifact")
	}
	return s.verifyPreparedArtifact(artifact)
}

func (s *Store) verifyCachedArtifact(artifact Artifact) error {
	if err := s.verifyBlob(artifact); err != nil {
		return err
	}
	return s.verifyPreparedArtifact(artifact)
}

func (s *Store) verifyPreparedArtifact(artifact Artifact) error {
	root := filepath.Join(s.root, "prepared", artifact.SHA256)
	if err := requireOwnedDirectory(root, false); err != nil {
		return errors.New("prepared integration artifact is missing or unsafe")
	}
	receiptData, err := rootfs.ReadSource(root, preparedReceipt, maxManifestBytes)
	if err != nil {
		return errors.New("prepared integration artifact receipt is missing")
	}
	var receipt preparedArtifactReceipt
	if err := decodeStrict(receiptData, &receipt); err != nil || receipt.SchemaVersion != 1 || receipt.Artifact != artifact.SHA256 || len(receipt.Files) == 0 || len(receipt.Files) > maxPreparedEntries {
		return errors.New("prepared integration artifact receipt is invalid")
	}
	seen := map[string]bool{}
	for _, file := range receipt.Files {
		if seen[file.Path] || file.Path == preparedReceipt {
			return errors.New("prepared integration artifact receipt contains an invalid path")
		}
		seen[file.Path] = true
		data, mode, found, err := rootfs.ReadOptional(root, file.Path, file.Size)
		if err != nil || !found || int64(len(data)) != file.Size || rootfs.SHA256(data) != file.SHA256 || uint32(mode.Perm()) != file.Mode || mode.Perm()&0o022 != 0 {
			return fmt.Errorf("prepared integration artifact file %q is corrupt", file.Path)
		}
	}
	count := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == preparedReceipt {
			return nil
		}
		if !seen[relative] {
			return errors.New("prepared integration artifact contains an unrecorded file")
		}
		count++
		return nil
	})
	if err != nil || count != len(receipt.Files) {
		return errors.New("prepared integration artifact closure is corrupt")
	}
	executable, ok := findPrepared(receipt.Files, artifact.Executable.Path)
	if !ok || executable.Size != artifact.Executable.Size || executable.SHA256 != artifact.Executable.SHA256 || executable.Mode&0o111 == 0 {
		return errors.New("prepared integration executable does not match manifest")
	}
	return nil
}

func collectPrepared(root, artifactHash string) (preparedArtifactReceipt, error) {
	receipt := preparedArtifactReceipt{SchemaVersion: 1, Artifact: artifactHash}
	total := int64(0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot inspect prepared integration artifact")
		}
		if path == root || entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("prepared integration artifact contains an unsafe entry")
		}
		total += info.Size()
		if len(receipt.Files) >= maxPreparedEntries || total > maxPreparedBytes {
			return errors.New("prepared integration artifact exceeds closure limits")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("cannot hash prepared integration artifact")
		}
		relative, _ := filepath.Rel(root, path)
		receipt.Files = append(receipt.Files, preparedFile{Path: filepath.ToSlash(relative), Size: info.Size(), SHA256: rootfs.SHA256(data), Mode: uint32(info.Mode().Perm())})
		return nil
	})
	if err != nil {
		return preparedArtifactReceipt{}, err
	}
	sort.Slice(receipt.Files, func(i, j int) bool { return receipt.Files[i].Path < receipt.Files[j].Path })
	return receipt, nil
}

func extractTarGZ(ctx context.Context, root string, data []byte) error {
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return errors.New("integration tar.gz artifact is invalid")
	}
	defer func() { _ = compressed.Close() }()
	reader := tar.NewReader(compressed)
	entries := 0
	total := int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("integration tar.gz artifact is invalid")
		}
		entries++
		if entries > maxPreparedEntries {
			return errors.New("integration artifact contains too many entries")
		}
		path := strings.TrimSuffix(header.Name, "/")
		if path == "" {
			continue
		}
		if _, err := rootfs.CleanRelative(path); err != nil || path == preparedReceipt {
			return errors.New("integration artifact contains an unsafe path")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := mkdirPrepared(root, path); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maxPreparedBytes-total {
				return errors.New("integration artifact expands beyond its limit")
			}
			total += header.Size
			content, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(content)) != header.Size {
				return errors.New("integration artifact entry size is invalid")
			}
			mode := os.FileMode(0o400)
			if header.FileInfo().Mode().Perm()&0o111 != 0 {
				mode = 0o500
			}
			if err := writePreparedFile(root, path, content, mode); err != nil {
				return err
			}
		default:
			return errors.New("integration artifact must contain only regular files and directories")
		}
	}
	return nil
}

func extractZIP(ctx context.Context, root string, data []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return errors.New("integration zip artifact is invalid")
	}
	if len(reader.File) > maxPreparedEntries {
		return errors.New("integration artifact contains too many entries")
	}
	total := int64(0)
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := strings.TrimSuffix(entry.Name, "/")
		if path == "" {
			continue
		}
		mode := entry.Mode()
		if _, err := rootfs.CleanRelative(path); err != nil || path == preparedReceipt || mode&os.ModeSymlink != 0 {
			return errors.New("integration artifact contains an unsafe path")
		}
		if entry.FileInfo().IsDir() {
			if err := mkdirPrepared(root, path); err != nil {
				return err
			}
			continue
		}
		if mode.Type() != 0 {
			return errors.New("integration artifact must contain only regular files and directories")
		}
		total += int64(entry.UncompressedSize64)
		if entry.UncompressedSize64 > uint64(maxPreparedBytes) || total > maxPreparedBytes {
			return errors.New("integration artifact expands beyond its limit")
		}
		handle, err := entry.Open()
		if err != nil {
			return errors.New("cannot open integration artifact entry")
		}
		content, readErr := io.ReadAll(io.LimitReader(handle, int64(entry.UncompressedSize64)+1))
		closeErr := handle.Close()
		if readErr != nil || closeErr != nil || uint64(len(content)) != entry.UncompressedSize64 {
			return errors.New("integration artifact entry size is invalid")
		}
		fileMode := os.FileMode(0o400)
		if mode.Perm()&0o111 != 0 {
			fileMode = 0o500
		}
		if err := writePreparedFile(root, path, content, fileMode); err != nil {
			return err
		}
	}
	return nil
}

func normalizeExecutable(root string, executable Executable) error {
	data, mode, found, err := rootfs.ReadOptional(root, executable.Path, executable.Size)
	if err != nil || !found {
		return errors.New("integration artifact executable is missing")
	}
	if err := verifyBytes(data, executable.Size, executable.SHA256); err != nil {
		return errors.New("integration artifact executable does not match manifest")
	}
	if mode.Perm() != 0o500 {
		if err := os.Chmod(filepath.Join(root, filepath.FromSlash(executable.Path)), 0o500); err != nil {
			return errors.New("cannot protect integration artifact executable")
		}
	}
	return nil
}

func writePreparedFile(root, relative string, data []byte, mode os.FileMode) error {
	if err := rootfs.WriteAtomicExclusive(root, relative, data, mode); err != nil {
		return errors.New("integration artifact contains duplicate or unsafe paths")
	}
	return nil
}

func mkdirPrepared(root, relative string) error {
	if _, err := rootfs.CleanRelative(relative); err != nil {
		return errors.New("integration artifact contains an unsafe directory")
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("integration artifact contains conflicting paths")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect integration artifact directory")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("cannot create integration artifact directory")
	}
	return nil
}

func verifyBytes(data []byte, size int64, expected string) error {
	if int64(len(data)) != size {
		return errors.New("integration artifact size does not match manifest")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expected {
		return errors.New("integration artifact checksum does not match manifest")
	}
	return nil
}

func currentPlatformArtifacts(manifest Manifest, targetOS, targetArch string) []Artifact {
	result := make([]Artifact, 0)
	for _, artifact := range manifest.Artifacts {
		if string(artifact.OS) == targetOS && string(artifact.Architecture) == targetArch {
			result = append(result, artifact)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func artifactByID(manifest Manifest, id string) (Artifact, bool) {
	for _, artifact := range manifest.Artifacts {
		if artifact.ID == id {
			return artifact, true
		}
	}
	return Artifact{}, false
}

func findPrepared(files []preparedFile, path string) (preparedFile, bool) {
	for _, file := range files {
		if file.Path == path {
			return file, true
		}
	}
	return preparedFile{}, false
}

func cloneInstallationState(state InstallationState) InstallationState {
	state.Artifacts = append([]InstalledArtifactIdentity(nil), state.Artifacts...)
	state.Capabilities = append([]InstalledCapabilityIdentity(nil), state.Capabilities...)
	return state
}

func requireOwnedDirectory(path string, protect bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("integration package directory is missing or unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("integration package directory is not owned by the current user")
	}
	if protect && info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return errors.New("cannot protect integration package directory")
		}
	} else if !protect && info.Mode().Perm()&0o077 != 0 {
		return errors.New("integration package directory permissions are too broad")
	}
	return nil
}

type packageSource struct {
	root     string
	manifest []byte
	cleanup  func()
}

func (source packageSource) close() {
	if source.cleanup != nil {
		source.cleanup()
	}
}

func openPackageSource(ctx context.Context, path string) (packageSource, error) {
	abs, err := filepath.Abs(path)
	if err != nil || filepath.Clean(abs) != abs {
		return packageSource{}, errors.New("integration package source path is invalid")
	}
	info, err := os.Lstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return packageSource{}, errors.New("integration package source must be a real local directory or archive")
	}
	if err := requireOwned(info); err != nil {
		return packageSource{}, err
	}
	if info.IsDir() {
		manifest, err := readOwnedSource(abs, packageManifestName, maxManifestBytes)
		if err != nil {
			return packageSource{}, fmt.Errorf("cannot read %s: %w", packageManifestName, err)
		}
		return packageSource{root: abs, manifest: manifest}, nil
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPackageArchive {
		return packageSource{}, errors.New("integration package archive must be a bounded regular file")
	}
	temporary, err := os.MkdirTemp("", "hctl-integration-source-")
	if err != nil {
		return packageSource{}, errors.New("cannot isolate integration package archive")
	}
	cleanup := func() { _ = os.RemoveAll(temporary) }
	if err := os.Chmod(temporary, 0o700); err != nil {
		cleanup()
		return packageSource{}, errors.New("cannot protect integration package archive extraction")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		cleanup()
		return packageSource{}, errors.New("cannot read integration package archive")
	}
	switch {
	case len(data) >= 4 && bytes.Equal(data[:2], []byte{'P', 'K'}):
		err = extractZIP(ctx, temporary, data)
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		err = extractTarGZ(ctx, temporary, data)
	default:
		err = errors.New("integration package archive must be zip or tar.gz")
	}
	if err != nil {
		cleanup()
		return packageSource{}, err
	}
	manifest, err := readOwnedSource(temporary, packageManifestName, maxManifestBytes)
	if err != nil {
		cleanup()
		return packageSource{}, fmt.Errorf("cannot read %s from integration package archive", packageManifestName)
	}
	return packageSource{root: temporary, manifest: manifest, cleanup: cleanup}, nil
}

func readOwnedSource(root, relative string, size int64) ([]byte, error) {
	if size < 0 {
		return nil, errors.New("integration package source size is invalid")
	}
	clean, err := rootfs.CleanRelative(relative)
	if err != nil {
		return nil, errors.New("integration package source path is invalid")
	}
	current := root
	for _, component := range strings.Split(clean, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("integration package source contains a missing or symlinked path")
		}
		if err := requireOwned(info); err != nil {
			return nil, err
		}
	}
	info, err := os.Stat(current)
	if err != nil || !info.Mode().IsRegular() || info.Size() > size {
		return nil, errors.New("integration package source file exceeds its bound")
	}
	return os.ReadFile(current)
}

func requireOwned(info os.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("integration package source is writable by another user")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("integration package source is not owned by the current user")
	}
	return nil
}
