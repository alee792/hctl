// Package rootfs provides the filesystem safety primitives used for portable
// agent sources and hctl-owned generated state.
//
// Source reads are bounded and reject symlinks in every path component.
// Generated writes use normalized relative paths, refuse symlink traversal,
// and replace files atomically. Private directories are created or verified
// with owner-only permissions. Callers remain responsible for restricting an
// operation to paths that hctl is allowed to own.
package rootfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CanonicalDir resolves path to an existing real directory.
func CanonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("cannot resolve agent directory")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("agent directory does not exist")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("agent path must be a directory")
	}
	return resolved, nil
}

// CleanRelative accepts only normalized portable paths beneath a project root.
func CleanRelative(path string) (string, error) {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return "", errors.New("path must be a non-empty portable relative path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != path {
		return "", errors.New("path must be normalized and remain inside the agent directory")
	}
	return cleaned, nil
}

// ReadSource reads a bounded regular file and rejects symlinks in every path component.
func ReadSource(root, relative string, limit int64) ([]byte, error) {
	relative, err := CleanRelative(relative)
	if err != nil {
		return nil, err
	}
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("%s is missing", relative)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%s must not contain symlinks", relative)
		}
	}
	info, err := os.Stat(current)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", relative)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", relative, limit)
	}
	return os.ReadFile(current)
}

// ReadOptional reads a bounded generated or state file without following a symlink.
func ReadOptional(root, relative string, limit int64) ([]byte, os.FileMode, bool, error) {
	relative, err := CleanRelative(relative)
	if err != nil {
		return nil, 0, false, err
	}
	path, info, exists, err := inspect(root, relative)
	if err != nil || !exists {
		return nil, 0, exists, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, 0, false, fmt.Errorf("%s must be a bounded regular file", relative)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("cannot read %s", relative)
	}
	return data, info.Mode(), true, nil
}

// WriteAtomic writes a file beneath root without traversing symlinked parents.
func WriteAtomic(root, relative string, data []byte, mode os.FileMode) error {
	relative, err := CleanRelative(relative)
	if err != nil {
		return errors.New("internal output path is invalid")
	}
	if err := secureMkdirAll(root, filepath.ToSlash(filepath.Dir(relative)), 0o755); err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symlink %s", relative)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".hctl-write-*")
	if err != nil {
		return fmt.Errorf("cannot stage file %s", relative)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot set file mode for %s", relative)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot write file %s", relative)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot sync file %s", relative)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("cannot close file %s", relative)
	}
	if err := os.Rename(tempName, target); err != nil {
		return fmt.Errorf("cannot install file %s", relative)
	}
	return nil
}

// WriteAtomicExclusive publishes a new file beneath root without traversing
// symlinked parents or replacing an existing path. The temporary file and
// final hard link live in the same directory, so publication is atomic.
func WriteAtomicExclusive(root, relative string, data []byte, mode os.FileMode) error {
	relative, err := CleanRelative(relative)
	if err != nil {
		return errors.New("internal output path is invalid")
	}
	if err := secureMkdirAll(root, filepath.ToSlash(filepath.Dir(relative)), 0o755); err != nil {
		return err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("refusing to replace existing file %s", relative)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot inspect file %s", relative)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".hctl-write-*")
	if err != nil {
		return fmt.Errorf("cannot stage file %s", relative)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot set file mode for %s", relative)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot write file %s", relative)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("cannot sync file %s", relative)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("cannot close file %s", relative)
	}
	if err := os.Link(tempName, target); err != nil {
		if _, inspectErr := os.Lstat(target); inspectErr == nil {
			return fmt.Errorf("refusing to replace existing file %s", relative)
		}
		return fmt.Errorf("cannot install file %s", relative)
	}
	return nil
}

// EnsurePrivateDir creates a persistent private directory beneath root without
// traversing symlinks.
func EnsurePrivateDir(root, relative string) error {
	relative, err := CleanRelative(relative)
	if err != nil {
		return errors.New("internal directory path is invalid")
	}
	if err := secureMkdirAll(root, relative, 0o700); err != nil {
		return err
	}
	path, info, exists, err := inspect(root, relative)
	if err != nil || !exists || !info.IsDir() {
		return fmt.Errorf("directory %s is missing or unsafe", relative)
	}
	if info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("cannot make directory %s private", relative)
		}
	}
	return nil
}

// RequirePrivateDir verifies that a private directory beneath root exists
// without traversing symlinks.
func RequirePrivateDir(root, relative string) error {
	relative, err := CleanRelative(relative)
	if err != nil {
		return errors.New("internal directory path is invalid")
	}
	_, info, exists, err := inspect(root, relative)
	if err != nil || !exists || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("directory %s is missing or unsafe", relative)
	}
	return nil
}

// RemoveRegular removes an owned regular file without following symlinks.
func RemoveRegular(root, relative string) error {
	relative, err := CleanRelative(relative)
	if err != nil {
		return errors.New("internal removal path is invalid")
	}
	path, info, exists, err := inspect(root, relative)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to remove non-regular file %s", relative)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("cannot remove obsolete generated file %s", relative)
	}
	return nil
}

func inspect(root, relative string) (string, os.FileInfo, bool, error) {
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return current, nil, false, nil
		}
		if err != nil {
			return current, nil, false, fmt.Errorf("cannot inspect %s", relative)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return current, nil, false, fmt.Errorf("%s must not contain symlinks", relative)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return current, nil, false, fmt.Errorf("%s parent must be a real directory", relative)
		}
		if index == len(parts)-1 {
			return current, info, true, nil
		}
	}
	return current, nil, false, nil
}

func secureMkdirAll(root, relative string, mode os.FileMode) error {
	if relative == "." || relative == "" {
		return nil
	}
	current := root
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, mode); err != nil {
				return errors.New("cannot create output directory")
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("output parent must be a real directory")
		}
	}
	return nil
}

func SHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
