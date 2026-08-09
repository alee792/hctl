package acquisition

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	maxTreeEntries = 8192
	maxTreeFiles   = 8192
	maxFileBytes   = 64 << 20
	maxTreeBytes   = 256 << 20
)

type TreeEntry struct {
	Path       string
	Directory  bool
	Executable bool
	Content    []byte
}

type Tree struct {
	Entries   []TreeEntry
	SHA256    string
	FileCount uint64
	ByteCount uint64
}

// ReadTree captures one immutable, bounded directory snapshot and computes
// the exact hctl-dependency-tree-v1 identity.
func ReadTree(root string) (Tree, error) {
	if err := requireUnicode15(); err != nil {
		return Tree{}, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Tree{}, errors.New("dependency root does not exist")
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return Tree{}, errors.New("cannot resolve dependency root")
	}
	rootInfo, err := os.Lstat(canonical)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return Tree{}, errors.New("dependency root must be a real directory")
	}
	entries := []TreeEntry{}
	comparisonPaths := map[string]string{}
	var fileCount, byteCount uint64
	err = filepath.WalkDir(canonical, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot read dependency tree")
		}
		if path == canonical {
			return nil
		}
		relative, err := filepath.Rel(canonical, path)
		if err != nil {
			return errors.New("cannot describe dependency path")
		}
		relative = filepath.ToSlash(relative)
		if err := validateTreePath(relative); err != nil {
			return fmt.Errorf("dependency path %q: %w", relative, err)
		}
		if containsGitMetadata(relative) {
			return fmt.Errorf("dependency path %q contains Git metadata", relative)
		}
		key := canonicalCaselessPath(relative)
		if previous, exists := comparisonPaths[key]; exists && previous != relative {
			return fmt.Errorf("dependency paths %q and %q collide under Unicode canonical caseless matching", previous, relative)
		}
		comparisonPaths[key] = relative
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect dependency tree")
		}
		if len(entries) == maxTreeEntries {
			return fmt.Errorf("dependency tree exceeds %d entries", maxTreeEntries)
		}
		if info.Mode()&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeCharDevice|os.ModeIrregular|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("dependency path %q has an unsupported file type or mode", relative)
		}
		if info.IsDir() {
			entries = append(entries, TreeEntry{Path: relative, Directory: true})
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("dependency path %q must be a directory or regular file", relative)
		}
		if hasMultipleLinks(info) {
			return fmt.Errorf("dependency path %q must not be a hard link", relative)
		}
		if fileCount == maxTreeFiles || info.Size() < 0 || info.Size() > maxFileBytes || byteCount+uint64(info.Size()) > maxTreeBytes {
			return fmt.Errorf("dependency path %q exceeds dependency tree bounds", relative)
		}
		content, err := readExactFile(path, info.Size())
		if err != nil {
			return fmt.Errorf("dependency path %q changed while being read", relative)
		}
		fileCount++
		byteCount += uint64(len(content))
		entries = append(entries, TreeEntry{Path: relative, Executable: info.Mode().Perm()&0o111 != 0, Content: content})
		return nil
	})
	if err != nil {
		return Tree{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	tree := Tree{Entries: entries, FileCount: fileCount, ByteCount: byteCount}
	tree.SHA256 = hashTree(entries)
	return tree, nil
}

func canonicalCaselessPath(path string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFD.String(path)))
}

func containsGitMetadata(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

func requireUnicode15() error {
	if norm.Version != "15.0.0" || cases.UnicodeVersion != "15.0.0" {
		return errors.New("dependency path matching requires Unicode 15.0.0 tables")
	}
	return nil
}

func validateTreePath(path string) error {
	if path == "" || len(path) > 1024 || !utf8.ValidString(path) || strings.ContainsAny(path, "\\\x00") || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return errors.New("path must be bounded normalized relative UTF-8")
	}
	parts := strings.Split(path, "/")
	if len(parts) > 64 {
		return errors.New("path exceeds 64 components")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return errors.New("path contains an unsafe component")
		}
	}
	return nil
}

func hashTree(entries []TreeEntry) string {
	hasher := sha256.New()
	_, _ = io.WriteString(hasher, "hctl-dependency-tree-v1")
	_, _ = hasher.Write([]byte{0})
	writeUint32(hasher, uint32(len(entries)))
	for _, entry := range entries {
		writeUint32(hasher, uint32(len(entry.Path)))
		_, _ = io.WriteString(hasher, entry.Path)
		if entry.Directory {
			_, _ = hasher.Write([]byte{0})
			writeUint32(hasher, 0o755)
			writeUint64(hasher, 0)
			continue
		}
		_, _ = hasher.Write([]byte{1})
		mode := uint32(0o644)
		if entry.Executable {
			mode = 0o755
		}
		writeUint32(hasher, mode)
		writeUint64(hasher, uint64(len(entry.Content)))
		_, _ = hasher.Write(entry.Content)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func writeUint32(writer hash.Hash, value uint32) {
	var buffer [4]byte
	binary.BigEndian.PutUint32(buffer[:], value)
	_, _ = writer.Write(buffer[:])
}

func writeUint64(writer hash.Hash, value uint64) {
	var buffer [8]byte
	binary.BigEndian.PutUint64(buffer[:], value)
	_, _ = writer.Write(buffer[:])
}

func readExactFile(path string, expected int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	content, err := io.ReadAll(io.LimitReader(file, expected+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || int64(len(content)) != expected {
		return nil, errors.New("file size changed")
	}
	return content, nil
}

func hasMultipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}

func markerDigest(tree Tree, marker string) (string, error) {
	for _, entry := range tree.Entries {
		if entry.Path == marker && !entry.Directory {
			digest := sha256.Sum256(entry.Content)
			return hex.EncodeToString(digest[:]), nil
		}
	}
	return "", fmt.Errorf("dependency marker %s is missing", marker)
}
