package acquisition

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxArchiveBytes  = 128 << 20
	maxArchiveExpand = 256 << 20
)

var archiveTransport http.RoundTripper = http.DefaultTransport

// Selector is an unresolved operator source choice. Commit is populated only
// on the resolved Source returned by Materialize.
type Selector struct {
	Type         SourceType
	Path         string
	URL          string
	Ref          string
	SHA256       string
	Format       string
	Subdirectory string
}

// Candidate is a complete normalized dependency tree staged on the agent
// filesystem. Close removes it until Publish takes ownership.
type Candidate struct {
	Root             string
	SelectedBasename string
	Source           Source
	Tree             Tree
}

func (candidate *Candidate) Close() error {
	if candidate == nil || candidate.Root == "" {
		return nil
	}
	root := candidate.Root
	candidate.Root = ""
	return os.RemoveAll(root)
}

// Materialize resolves a selector only for this explicit operation and stages
// a normalized complete tree beneath the agent root's filesystem.
func Materialize(ctx context.Context, agentRoot string, selector Selector) (*Candidate, error) {
	root, err := canonicalAgentRoot(agentRoot)
	if err != nil {
		return nil, err
	}
	if selector.Subdirectory != "" {
		if err := validateTreePath(selector.Subdirectory); err != nil {
			return nil, fmt.Errorf("source subdirectory: %w", err)
		}
		if containsGitMetadata(selector.Subdirectory) {
			return nil, errors.New("source subdirectory must not select Git metadata")
		}
	}
	staging, err := os.MkdirTemp(root, ".hctl-acquire-")
	if err != nil {
		return nil, errors.New("cannot stage dependency on the agent filesystem")
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return nil, errors.New("cannot protect staged dependency")
	}
	candidate := &Candidate{Root: staging}
	fail := func(err error) (*Candidate, error) {
		_ = candidate.Close()
		return nil, err
	}

	switch selector.Type {
	case SourceLocal:
		sourceRoot, source, err := resolveLocal(root, selector)
		if err != nil {
			return fail(err)
		}
		tree, err := ReadTree(sourceRoot)
		if err != nil {
			return fail(err)
		}
		if err := writeTree(staging, tree); err != nil {
			return fail(err)
		}
		candidate.Source, candidate.Tree = source, tree
		candidate.SelectedBasename = filepath.Base(sourceRoot)
	case SourceGit:
		source, entries, err := resolveGit(ctx, staging, selector)
		if err != nil {
			return fail(err)
		}
		if err := writeArchiveEntries(staging, entries); err != nil {
			return fail(err)
		}
		candidate.Source = source
		if selector.Subdirectory != "" {
			candidate.SelectedBasename = filepath.Base(filepath.FromSlash(selector.Subdirectory))
		} else {
			candidate.SelectedBasename = remoteRootBasename(selector.URL, "git")
		}
	case SourceArchive:
		source, entries, err := resolveArchive(ctx, selector)
		if err != nil {
			return fail(err)
		}
		if err := writeArchiveEntries(staging, entries); err != nil {
			return fail(err)
		}
		candidate.Source = source
		if selector.Subdirectory != "" {
			candidate.SelectedBasename = filepath.Base(filepath.FromSlash(selector.Subdirectory))
		} else {
			candidate.SelectedBasename = remoteRootBasename(selector.URL, "archive")
		}
	default:
		return fail(errors.New("source type must be local, git, or archive"))
	}
	if candidate.Tree.SHA256 == "" {
		candidate.Tree, err = ReadTree(staging)
		if err != nil {
			return fail(err)
		}
	}
	return candidate, nil
}

func remoteRootBasename(value, sourceType string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	name := filepath.Base(filepath.FromSlash(strings.TrimSuffix(parsed.Path, "/")))
	if sourceType == "git" {
		name = strings.TrimSuffix(name, ".git")
	} else {
		for _, suffix := range []string{".tar.gz", ".tgz", ".zip"} {
			if strings.HasSuffix(strings.ToLower(name), suffix) {
				name = name[:len(name)-len(suffix)]
				break
			}
		}
	}
	if name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func canonicalAgentRoot(root string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("agent root does not exist")
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", errors.New("cannot resolve agent root")
	}
	info, err := os.Lstat(canonical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("agent root must be a real directory")
	}
	marker, err := os.Lstat(filepath.Join(canonical, "instructions.md"))
	if err == nil && marker.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("agent instructions must not contain symlinks")
	}
	if err != nil || !marker.Mode().IsRegular() {
		return "", errors.New("agent root must contain a regular instructions.md")
	}
	return canonical, nil
}

func resolveLocal(agentRoot string, selector Selector) (string, Source, error) {
	if selector.Path == "" || len(selector.Path) > 4096 || !utf8.ValidString(selector.Path) || strings.ContainsRune(selector.Path, 0) {
		return "", Source{}, errors.New("local source path is invalid")
	}
	base, err := filepath.Abs(selector.Path)
	if err != nil {
		return "", Source{}, errors.New("cannot resolve local source path")
	}
	base, err = realDirectoryWithoutLinks(base)
	if err != nil {
		return "", Source{}, err
	}
	selected := base
	if selector.Subdirectory != "" {
		selected, err = descendRealDirectory(base, selector.Subdirectory)
		if err != nil {
			return "", Source{}, err
		}
	}
	if relative, relErr := filepath.Rel(selected, agentRoot); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", Source{}, errors.New("local dependency source must not contain the destination agent root")
	}
	if strings.EqualFold(filepath.Base(selected), ".git") {
		return "", Source{}, errors.New("local dependency source must not select Git metadata")
	}
	relative, err := filepath.Rel(agentRoot, base)
	if err != nil {
		return "", Source{}, errors.New("cannot record local source path")
	}
	relative = filepath.ToSlash(relative)
	if err := validateLocalLocator(relative); err != nil {
		return "", Source{}, err
	}
	source := Source{Type: SourceLocal, Path: relative, Subdirectory: selector.Subdirectory}
	if err := validateSource(source); err != nil {
		return "", Source{}, err
	}
	return selected, source, nil
}

func realDirectoryWithoutLinks(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("cannot resolve source directory")
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("source path must be an existing real directory")
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("cannot resolve source directory")
	}
	return canonical, nil
}

func descendRealDirectory(root, relative string) (string, error) {
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("selected source subdirectory must be a real directory")
		}
	}
	return current, nil
}

func writeTree(root string, tree Tree) error {
	for _, entry := range tree.Entries {
		target := filepath.Join(root, filepath.FromSlash(entry.Path))
		if entry.Directory {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return errors.New("cannot materialize dependency directory")
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return errors.New("cannot materialize dependency parent")
		}
		mode := os.FileMode(0o644)
		if entry.Executable {
			mode = 0o755
		}
		if err := os.WriteFile(target, entry.Content, mode); err != nil {
			return errors.New("cannot materialize dependency file")
		}
	}
	return nil
}

type archiveEntry struct {
	path       string
	directory  bool
	executable bool
	content    []byte
}

func writeArchiveEntries(root string, entries []archiveEntry) error {
	treeEntries := make([]TreeEntry, 0, len(entries))
	for _, entry := range entries {
		treeEntries = append(treeEntries, TreeEntry{Path: entry.path, Directory: entry.directory, Executable: entry.executable, Content: entry.content})
	}
	return writeTree(root, Tree{Entries: treeEntries})
}

func resolveArchive(ctx context.Context, selector Selector) (Source, []archiveEntry, error) {
	if err := validateHTTPSURL(selector.URL); err != nil {
		return Source{}, nil, err
	}
	if !hexSHA256Pattern.MatchString(selector.SHA256) {
		return Source{}, nil, errors.New("archive source digest is invalid")
	}
	if selector.Format != "" && selector.Format != "zip" && selector.Format != "tar.gz" {
		return Source{}, nil, errors.New("archive source format is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, selector.URL, nil)
	if err != nil {
		return Source{}, nil, errors.New("cannot prepare archive request")
	}
	request.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{
		Timeout:   2 * time.Minute,
		Transport: archiveTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("archive redirects are not allowed")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return Source{}, nil, errors.New("cannot download dependency archive")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.ContentLength > maxArchiveBytes || (response.Header.Get("Content-Encoding") != "" && !strings.EqualFold(response.Header.Get("Content-Encoding"), "identity")) {
		return Source{}, nil, errors.New("dependency archive response is invalid or too large")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxArchiveBytes+1))
	if err != nil || len(payload) > maxArchiveBytes {
		return Source{}, nil, errors.New("cannot read bounded dependency archive")
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != selector.SHA256 {
		return Source{}, nil, errors.New("dependency archive SHA-256 does not match")
	}
	format := selector.Format
	if format == "" {
		format = detectArchiveFormat(payload)
	}
	if format == "" {
		return Source{}, nil, errors.New("dependency archive must be ZIP or gzip-compressed TAR")
	}
	var entries []archiveEntry
	if format == "zip" {
		entries, err = readZIP(payload)
	} else {
		entries, err = readTarGzip(bytes.NewReader(payload))
	}
	if err != nil {
		return Source{}, nil, err
	}
	entries, err = selectArchiveSubdirectory(entries, selector.Subdirectory)
	if err != nil {
		return Source{}, nil, err
	}
	source := Source{Type: SourceArchive, URL: selector.URL, SHA256: selector.SHA256, Format: format, Subdirectory: selector.Subdirectory}
	return source, entries, nil
}

func detectArchiveFormat(payload []byte) string {
	if len(payload) >= 4 && (bytes.Equal(payload[:4], []byte{'P', 'K', 3, 4}) || bytes.Equal(payload[:4], []byte{'P', 'K', 5, 6}) || bytes.Equal(payload[:4], []byte{'P', 'K', 7, 8})) {
		return "zip"
	}
	if len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		return "tar.gz"
	}
	return ""
}

func resolveGit(ctx context.Context, staging string, selector Selector) (Source, []archiveEntry, error) {
	unresolved := Source{Type: SourceGit, URL: selector.URL, Ref: selector.Ref, Commit: strings.Repeat("0", 40), Subdirectory: selector.Subdirectory}
	if err := validateSource(unresolved); err != nil {
		return Source{}, nil, err
	}
	repository := filepath.Join(staging, ".git-source")
	if err := runGit(ctx, "", "init", "--bare", repository); err != nil {
		return Source{}, nil, err
	}
	if err := runGit(ctx, repository, "-c", "http.followRedirects=false", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "core.hooksPath=/dev/null", "fetch", "--no-tags", "--depth=1", "--", selector.URL, selector.Ref); err != nil {
		return Source{}, nil, err
	}
	commitOutput, err := gitOutput(ctx, repository, 256, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return Source{}, nil, err
	}
	commit := strings.TrimSpace(string(commitOutput))
	if !commitPattern.MatchString(commit) {
		return Source{}, nil, errors.New("git source did not resolve to one supported commit identity")
	}
	arguments := []string{"ls-tree", "-rz", "-r", "--full-tree", commit}
	if selector.Subdirectory != "" {
		arguments = append(arguments, "--", selector.Subdirectory)
	}
	treeOutput, err := gitOutput(ctx, repository, 16<<20, arguments...)
	if err != nil {
		return Source{}, nil, err
	}
	blobs, entries, err := parseGitTree(treeOutput, selector.Subdirectory)
	if err != nil {
		return Source{}, nil, err
	}
	if err := readGitBlobs(ctx, repository, blobs, entries); err != nil {
		return Source{}, nil, err
	}
	entries, err = validateArchiveEntries(entries)
	if err != nil {
		return Source{}, nil, err
	}
	if err := os.RemoveAll(repository); err != nil {
		return Source{}, nil, errors.New("cannot remove temporary Git object database")
	}
	source := Source{Type: SourceGit, URL: selector.URL, Ref: selector.Ref, Commit: commit, Subdirectory: selector.Subdirectory}
	return source, entries, nil
}

func gitCommand(ctx context.Context, repository string, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, "git", arguments...)
	if repository != "" {
		command.Dir = repository
	}
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never")
	command.Stderr = io.Discard
	return command
}

func runGit(ctx context.Context, repository string, arguments ...string) error {
	command := gitCommand(ctx, repository, arguments...)
	command.Stdout = io.Discard
	if err := command.Run(); err != nil {
		return errors.New("git source operation failed")
	}
	return nil
}

func gitOutput(ctx context.Context, repository string, limit int64, arguments ...string) ([]byte, error) {
	command := gitCommand(ctx, repository, arguments...)
	pipe, err := command.StdoutPipe()
	if err != nil || command.Start() != nil {
		return nil, errors.New("git source operation failed")
	}
	output, readErr := io.ReadAll(io.LimitReader(pipe, limit+1))
	waitErr := command.Wait()
	if readErr != nil || waitErr != nil || int64(len(output)) > limit {
		return nil, errors.New("git source operation failed or exceeded its bound")
	}
	return output, nil
}

type gitBlob struct {
	object     string
	entryIndex int
}

func parseGitTree(data []byte, subdirectory string) ([]gitBlob, []archiveEntry, error) {
	if len(data) == 0 {
		return nil, nil, errors.New("selected Git tree is empty or missing")
	}
	blobs := []gitBlob{}
	entries := []archiveEntry{}
	directories := map[string]bool{}
	prefix := subdirectory + "/"
	for _, record := range bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0}) {
		metadata, path, ok := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !ok || len(fields) != 3 || !utf8.Valid(path) {
			return nil, nil, errors.New("selected Git tree metadata is invalid")
		}
		mode, kind, object := string(fields[0]), string(fields[1]), string(fields[2])
		if kind != "blob" || (mode != "100644" && mode != "100755") {
			return nil, nil, errors.New("selected Git tree contains a symlink, gitlink, or unsupported entry")
		}
		if !commitPattern.MatchString(object) {
			return nil, nil, errors.New("selected Git tree contains an invalid object identity")
		}
		relative := string(path)
		if subdirectory != "" {
			if relative == subdirectory {
				return nil, nil, errors.New("selected Git subdirectory is a file")
			}
			if !strings.HasPrefix(relative, prefix) {
				continue
			}
			relative = strings.TrimPrefix(relative, prefix)
		}
		if err := validateTreePath(relative); err != nil {
			return nil, nil, fmt.Errorf("selected Git path %q: %w", relative, err)
		}
		if containsGitMetadata(relative) {
			return nil, nil, fmt.Errorf("selected Git path %q contains Git metadata", relative)
		}
		for parent := filepath.ToSlash(filepath.Dir(relative)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			directories[parent] = true
		}
		entries = append(entries, archiveEntry{path: relative, executable: mode == "100755"})
		blobs = append(blobs, gitBlob{object: object, entryIndex: len(entries) - 1})
	}
	if len(blobs) == 0 {
		return nil, nil, errors.New("selected Git tree is empty or missing")
	}
	directoryNames := make([]string, 0, len(directories))
	for directory := range directories {
		directoryNames = append(directoryNames, directory)
	}
	sort.Strings(directoryNames)
	files := entries
	entries = make([]archiveEntry, 0, len(directoryNames)+len(files))
	for _, directory := range directoryNames {
		entries = append(entries, archiveEntry{path: directory, directory: true})
	}
	offset := len(entries)
	entries = append(entries, files...)
	for index := range blobs {
		blobs[index].entryIndex += offset
	}
	return blobs, entries, nil
}

func readGitBlobs(ctx context.Context, repository string, blobs []gitBlob, entries []archiveEntry) error {
	command := gitCommand(ctx, repository, "cat-file", "--batch")
	stdin, err := command.StdinPipe()
	if err != nil {
		return errors.New("git source operation failed")
	}
	stdout, err := command.StdoutPipe()
	if err != nil || command.Start() != nil {
		return errors.New("git source operation failed")
	}
	writer := bufio.NewWriter(stdin)
	reader := bufio.NewReader(stdout)
	var total uint64
	fail := func() error {
		_ = command.Process.Kill()
		_ = command.Wait()
		return errors.New("git source object data is invalid or exceeds its bound")
	}
	for _, blob := range blobs {
		if _, err := writer.WriteString(blob.object + "\n"); err != nil || writer.Flush() != nil {
			return fail()
		}
		header, err := reader.ReadString('\n')
		fields := strings.Fields(header)
		if err != nil || len(fields) != 3 || fields[0] != blob.object || fields[1] != "blob" {
			return fail()
		}
		size, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil || size > maxFileBytes || total+size > maxTreeBytes {
			return fail()
		}
		content := make([]byte, int(size))
		if _, err := io.ReadFull(reader, content); err != nil {
			return fail()
		}
		separator, err := reader.ReadByte()
		if err != nil || separator != '\n' {
			return fail()
		}
		entries[blob.entryIndex].content = content
		total += size
	}
	if err := stdin.Close(); err != nil || command.Wait() != nil {
		return errors.New("git source object data is invalid")
	}
	return nil
}

func readZIP(payload []byte) ([]archiveEntry, error) {
	reader, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return nil, errors.New("dependency ZIP is invalid")
	}
	entries := make([]archiveEntry, 0, len(reader.File))
	var expanded uint64
	for _, file := range reader.File {
		name := strings.TrimSuffix(file.Name, "/")
		if name == "" {
			continue
		}
		info := file.FileInfo()
		if info.Mode()&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeCharDevice|os.ModeIrregular|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("dependency archive entry %q is unsupported", name)
		}
		entry := archiveEntry{path: name, directory: info.IsDir(), executable: info.Mode().Perm()&0o111 != 0}
		if !entry.directory {
			if file.UncompressedSize64 > maxFileBytes || expanded+file.UncompressedSize64 > maxArchiveExpand {
				return nil, errors.New("dependency ZIP exceeds expansion bounds")
			}
			stream, err := file.Open()
			if err != nil {
				return nil, errors.New("cannot read dependency ZIP entry")
			}
			entry.content, err = io.ReadAll(io.LimitReader(stream, maxFileBytes+1))
			closeErr := stream.Close()
			if err != nil || closeErr != nil || uint64(len(entry.content)) != file.UncompressedSize64 {
				return nil, errors.New("dependency ZIP entry is invalid or changed size")
			}
			expanded += uint64(len(entry.content))
		}
		entries = append(entries, entry)
	}
	return validateArchiveEntries(entries)
}

func readTarGzip(payload io.Reader) ([]archiveEntry, error) {
	gzipReader, err := gzip.NewReader(payload)
	if err != nil {
		return nil, errors.New("dependency tar.gz is invalid")
	}
	entries, readErr := readTAR(tar.NewReader(io.LimitReader(gzipReader, maxArchiveExpand+1)))
	closeErr := gzipReader.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.New("dependency tar.gz is invalid or exceeds expansion bounds")
	}
	return entries, nil
}

func readTAR(reader *tar.Reader) ([]archiveEntry, error) {
	entries := []archiveEntry{}
	var expanded uint64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("dependency TAR is invalid")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" {
			continue
		}
		entry := archiveEntry{path: name}
		switch header.Typeflag {
		case tar.TypeXHeader, tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			entry.directory = true
		case tar.TypeReg, 0:
			if header.Size < 0 || header.Size > maxFileBytes || expanded+uint64(header.Size) > maxArchiveExpand {
				return nil, errors.New("dependency TAR exceeds expansion bounds")
			}
			entry.executable = header.Mode&0o111 != 0
			entry.content, err = io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(entry.content)) != header.Size {
				return nil, errors.New("dependency TAR entry is truncated")
			}
			expanded += uint64(len(entry.content))
		default:
			return nil, fmt.Errorf("dependency archive entry %q is a link or unsupported type", name)
		}
		if header.Mode&^0o777 != 0 {
			return nil, fmt.Errorf("dependency archive entry %q has special permission bits", name)
		}
		entries = append(entries, entry)
	}
	return validateArchiveEntries(entries)
}

func validateArchiveEntries(entries []archiveEntry) ([]archiveEntry, error) {
	if err := requireUnicode15(); err != nil {
		return nil, err
	}
	if len(entries) > maxTreeEntries {
		return nil, errors.New("dependency archive contains too many entries")
	}
	seen := map[string]bool{}
	caseless := map[string]string{}
	files := map[string]bool{}
	for _, entry := range entries {
		if err := validateTreePath(entry.path); err != nil {
			return nil, fmt.Errorf("dependency archive path %q: %w", entry.path, err)
		}
		if seen[entry.path] {
			return nil, fmt.Errorf("dependency archive path %q is duplicated", entry.path)
		}
		seen[entry.path] = true
		key := canonicalCaselessPath(entry.path)
		if previous, exists := caseless[key]; exists && previous != entry.path {
			return nil, fmt.Errorf("dependency archive paths %q and %q collide under Unicode canonical caseless matching", previous, entry.path)
		}
		caseless[key] = entry.path
		for parent := filepath.ToSlash(filepath.Dir(entry.path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if files[parent] {
				return nil, fmt.Errorf("dependency archive file %q is also a parent", parent)
			}
		}
		if !entry.directory {
			files[entry.path] = true
			for path := range seen {
				if strings.HasPrefix(path, entry.path+"/") {
					return nil, fmt.Errorf("dependency archive file %q is also a parent", entry.path)
				}
			}
		}
	}
	return entries, nil
}

func selectArchiveSubdirectory(entries []archiveEntry, subdirectory string) ([]archiveEntry, error) {
	if subdirectory == "" {
		return entries, nil
	}
	prefix := subdirectory + "/"
	selected := make([]archiveEntry, 0)
	foundRoot := false
	for _, entry := range entries {
		if entry.path == subdirectory {
			if !entry.directory {
				return nil, errors.New("selected archive subdirectory is a file")
			}
			foundRoot = true
			continue
		}
		if strings.HasPrefix(entry.path, prefix) {
			foundRoot = true
			entry.path = strings.TrimPrefix(entry.path, prefix)
			selected = append(selected, entry)
		}
	}
	if !foundRoot || len(selected) == 0 {
		return nil, errors.New("selected archive subdirectory is missing or empty")
	}
	return validateArchiveEntries(selected)
}
