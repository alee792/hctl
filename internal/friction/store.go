// Package friction stores bounded, model-authored friction notes in private
// local hctl state. The store is deliberately write-only to the model-facing
// runtime and reports every store-specific failure as a simple no-op.
package friction

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gofrs/flock"

	"hctl/internal/project"
	"hctl/internal/rootfs"
)

const (
	MaxNoteBytes = 1024
	MaxRecords   = 256
)

type Store struct {
	root   string
	now    func() time.Time
	random io.Reader
}

type entry struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	Agent         struct {
		ID                string `json:"id"`
		Name              string `json:"name"`
		SourceFingerprint string `json:"source_fingerprint"`
	} `json:"agent"`
	Runtime struct {
		HCTLVersion string `json:"hctl_version"`
		Harness     string `json:"harness"`
	} `json:"runtime"`
	Note string `json:"note"`
}

// NewStore returns a store rooted at an existing directory. It is intended
// for tests and future explicit operator plumbing; production uses NewDefault.
func NewStore(root string) *Store {
	return &Store{root: root, now: time.Now, random: rand.Reader}
}

// NewDefault returns a lazily resolved platform-local store.
func NewDefault() *Store {
	return NewStore("")
}

// Record publishes one note. False covers every validation, capacity, lock,
// path, and storage failure so callers never need to retry or surface details.
func (s *Store) Record(p *project.Project, note string) bool {
	if s == nil || p == nil || !validProject(p) || !utf8.ValidString(note) || strings.TrimSpace(note) == "" || len([]byte(note)) > MaxNoteBytes {
		return false
	}
	root, prefix, err := s.location()
	if err != nil {
		return false
	}
	agentDir := joinRelative(prefix, "friction", "agents", p.AgentID)
	if _, err := rootfs.CleanRelative(agentDir); err != nil || rootfs.EnsurePrivateDir(root, agentDir) != nil {
		return false
	}
	lockPath := filepath.Join(root, filepath.FromSlash(agentDir), ".lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) || err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	guard := flock.New(lockPath)
	locked, err := guard.TryLock()
	if err != nil || !locked {
		return false
	}
	defer func() { _ = guard.Unlock() }()
	if err := os.Chmod(lockPath, 0o600); err != nil {
		return false
	}
	if !hasCapacity(filepath.Dir(lockPath)) {
		return false
	}

	created := s.now().UTC()
	randomBytes := make([]byte, 8)
	if _, err := io.ReadFull(s.random, randomBytes); err != nil {
		return false
	}
	id := created.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(randomBytes)
	record := entry{SchemaVersion: 1, ID: id, CreatedAt: created, Note: note}
	record.Agent.ID = p.AgentID
	record.Agent.Name = p.Name
	record.Agent.SourceFingerprint = p.SourceFingerprint
	record.Runtime.HCTLVersion = project.GeneratorVersion
	record.Runtime.Harness = p.Harness
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return false
	}
	data = append(data, '\n')
	return rootfs.WriteAtomicExclusive(root, agentDir+"/"+id+".json", data, 0o600) == nil
}

func (s *Store) location() (string, string, error) {
	if s.root != "" {
		root, err := rootfs.CanonicalDir(s.root)
		return root, "", err
	}
	base, prefix, err := defaultStateBase(runtime.GOOS, os.Getenv("XDG_STATE_HOME"))
	if err != nil {
		return "", "", err
	}
	root, missing, err := existingAnchor(base)
	if err != nil {
		return "", "", err
	}
	return root, joinRelative(missing, prefix), nil
}

func defaultStateBase(goos, xdgStateHome string) (string, string, error) {
	switch goos {
	case "darwin":
		config, err := os.UserConfigDir()
		if err != nil || config == "" {
			return "", "", errors.New("cannot resolve user application support")
		}
		return config, "hctl/state", nil
	case "linux":
		if xdgStateHome != "" {
			if !filepath.IsAbs(xdgStateHome) || filepath.Clean(xdgStateHome) != xdgStateHome {
				return "", "", errors.New("XDG state home must be a clean absolute path")
			}
			return xdgStateHome, "hctl", nil
		}
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", "", errors.New("cannot resolve user home")
		}
		return filepath.Join(home, ".local", "state"), "hctl", nil
	default:
		config, err := os.UserConfigDir()
		if err != nil || config == "" {
			return "", "", errors.New("cannot resolve user configuration")
		}
		return config, "hctl/state", nil
	}
}

func existingAnchor(path string) (string, string, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", "", errors.New("state base must be absolute")
	}
	candidate := path
	for {
		info, err := os.Lstat(candidate)
		if err == nil {
			if !info.IsDir() {
				return "", "", errors.New("state base parent must be a directory")
			}
			root, err := rootfs.CanonicalDir(candidate)
			if err != nil {
				return "", "", err
			}
			missing, err := filepath.Rel(candidate, path)
			if err != nil {
				return "", "", errors.New("cannot resolve state base")
			}
			if missing == "." {
				missing = ""
			}
			return root, filepath.ToSlash(missing), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", errors.New("cannot inspect state base")
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", "", errors.New("cannot find state base parent")
		}
		candidate = parent
	}
}

func hasCapacity(directory string) bool {
	handle, err := os.Open(directory)
	if err != nil {
		return false
	}
	defer func() { _ = handle.Close() }()
	entries, err := handle.ReadDir(MaxRecords + 2)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	count := 0
	for _, candidate := range entries {
		if candidate.Name() == ".lock" {
			continue
		}
		info, err := candidate.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || filepath.Ext(candidate.Name()) != ".json" {
			return false
		}
		count++
	}
	return count < MaxRecords
}

func validProject(p *project.Project) bool {
	name, identity, found := strings.Cut(p.AgentID, "@")
	_, identityErr := hex.DecodeString(identity)
	_, fingerprintErr := hex.DecodeString(p.SourceFingerprint)
	return found && name == p.Name && name != "" && len(identity) == 12 && identityErr == nil && len(p.SourceFingerprint) == 64 && fingerprintErr == nil &&
		(p.Harness == "claude" || p.Harness == "codex") && !strings.ContainsAny(name, `/\\`)
}

func joinRelative(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return filepath.ToSlash(filepath.Join(kept...))
}
