package acquisition

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"hctl/internal/rootfs"
)

const (
	journalFilename      = ".hctl-dependency-transaction.json"
	maxJournalBytes      = 64 << 10
	maxAggregateEntries  = 16384
	maxAggregateTreeByte = 512 << 20
)

var operationMutexes sync.Map
var publicationInterruptionHook func() error

type ComponentInfo struct {
	Name             string
	Marker           string
	ManifestName     string
	ExecutableFiles  []string
	SkillNames       []string
	MCPServerNames   []string
	SkillCount       int
	MCPServerCount   int
	AdditionalCounts map[string]int
}

// Hooks retain semantic ownership in the component package. Acquisition only
// needs the marker/name result and a prospective collision check.
type Hooks struct {
	Inspect             func(root string) (ComponentInfo, error)
	ValidateProspective func(Prospective) error
}

type Prospective struct {
	AgentRoot     string
	CandidateRoot string
	Kind          Kind
	Name          string
	Destination   string
	Replacing     *Dependency
	Removing      bool
	CurrentLock   Lock
	NextLock      Lock
	Snapshot      Snapshot
}

type Manager struct {
	AgentRoot string
	Plugins   Hooks
	Skills    Hooks
	Approve   func(TrustSummary) error
}

type TrustSummary struct {
	Kind                    Kind
	Name                    string
	Source                  Source
	Destination             string
	TreeSHA256              string
	FileCount               uint64
	ByteCount               uint64
	ExecutablePaths         []string
	AdditionalExecutables   int
	PluginSkillCount        int
	PluginMCPServerCount    int
	AdditionalComponentInfo map[string]int
}

type SnapshotEntry struct {
	Path       string
	SHA256     string
	Executable bool
	Content    []byte
}

type Snapshot struct {
	Lock         Lock
	LockBytes    []byte
	Dependencies []VerifiedDependency
	Files        []SnapshotEntry
	Directories  []string
}

type VerifiedDependency struct {
	Dependency Dependency
	Tree       Tree
}

type State string

const (
	StateClean     State = "clean"
	StateDrifted   State = "drifted"
	StateMissing   State = "missing"
	StateUntracked State = "untracked"
)

type ComponentStatus struct {
	Kind         Kind
	Name         string
	ManifestName string
	State        State
	Dependency   *Dependency
	Detail       string
}

func (manager Manager) Add(ctx context.Context, kind Kind, selector Selector) (Dependency, error) {
	root, err := canonicalAgentRoot(manager.AgentRoot)
	if err != nil {
		return Dependency{}, err
	}
	var added Dependency
	err = withOperationLock(ctx, root, func() error {
		if err := recoverTransaction(root); err != nil {
			return err
		}
		snapshot, err := readSnapshotUnlocked(root)
		if err != nil {
			return err
		}
		candidate, err := Materialize(ctx, root, selector)
		if err != nil {
			return err
		}
		defer func() { _ = candidate.Close() }()
		hooks, err := manager.hooks(kind)
		if err != nil {
			return err
		}
		info, err := hooks.Inspect(candidate.Root)
		if err != nil {
			return err
		}
		if !validName(kind, info.Name) || info.Marker == "" {
			return errors.New("component validator returned an invalid name or marker")
		}
		if kind == Skill {
			if candidate.SelectedBasename == "" {
				return errors.New("skill source must have a selected directory basename")
			}
			if candidate.SelectedBasename != info.Name {
				return errors.New("skill name must match the selected source directory basename")
			}
		}
		destination := string(kind) + "s/" + info.Name
		if err := validateConventionalParent(root, kind); err != nil {
			return err
		}
		if err := requireAvailableDestination(root, destination); err != nil {
			return err
		}
		markerSHA, err := markerDigest(candidate.Tree, info.Marker)
		if err != nil {
			return err
		}
		added = Dependency{
			Kind: kind, Name: info.Name, Destination: destination, Source: candidate.Source,
			MarkerSHA256: markerSHA, TreeSHA256: candidate.Tree.SHA256,
			FileCount: candidate.Tree.FileCount, ByteCount: candidate.Tree.ByteCount,
		}
		next := append(append([]Dependency(nil), snapshot.Lock.Dependencies...), added)
		next = sortedDependencies(next)
		if err := validateProspectiveAggregate(snapshot, candidate.Tree, nil); err != nil {
			return err
		}
		nextLock := Lock{SchemaVersion: 1, Dependencies: next}
		if hooks.ValidateProspective != nil {
			if err := hooks.ValidateProspective(Prospective{AgentRoot: root, CandidateRoot: candidate.Root, Kind: kind, Name: info.Name, Destination: destination, CurrentLock: snapshot.Lock, NextLock: nextLock, Snapshot: snapshot}); err != nil {
				return err
			}
		}
		if err := manager.approve(candidate, added, info); err != nil {
			return err
		}
		return publish(root, "add", added.Destination, "", added.TreeSHA256, candidate, nextLock)
	})
	return added, err
}

func (manager Manager) Update(ctx context.Context, kind Kind, name string, selector *Selector) (Dependency, bool, error) {
	root, err := canonicalAgentRoot(manager.AgentRoot)
	if err != nil {
		return Dependency{}, false, err
	}
	var updated Dependency
	changed := false
	err = withOperationLock(ctx, root, func() error {
		if err := recoverTransaction(root); err != nil {
			return err
		}
		snapshot, err := readSnapshotUnlocked(root)
		if err != nil {
			return err
		}
		index := dependencyIndex(snapshot.Lock.Dependencies, kind, name)
		if index < 0 {
			return fmt.Errorf("acquired %s %q is not recorded", kind, name)
		}
		current := snapshot.Lock.Dependencies[index]
		selected := selectorFromSource(root, current.Source)
		if selector != nil {
			selected = *selector
		}
		candidate, err := Materialize(ctx, root, selected)
		if err != nil {
			return err
		}
		defer func() { _ = candidate.Close() }()
		hooks, err := manager.hooks(kind)
		if err != nil {
			return err
		}
		info, err := hooks.Inspect(candidate.Root)
		if err != nil {
			return err
		}
		if info.Name != current.Name || info.Marker == "" {
			return errors.New("dependency update cannot change component identity")
		}
		if kind == Skill {
			if candidate.SelectedBasename == "" {
				return errors.New("skill source must have a selected directory basename")
			}
			if candidate.SelectedBasename != info.Name {
				return errors.New("skill name must match the selected source directory basename")
			}
		}
		markerSHA, err := markerDigest(candidate.Tree, info.Marker)
		if err != nil {
			return err
		}
		updated = current
		updated.Source = candidate.Source
		updated.MarkerSHA256 = markerSHA
		updated.TreeSHA256 = candidate.Tree.SHA256
		updated.FileCount = candidate.Tree.FileCount
		updated.ByteCount = candidate.Tree.ByteCount
		next := append([]Dependency(nil), snapshot.Lock.Dependencies...)
		next[index] = updated
		if err := validateProspectiveAggregate(snapshot, candidate.Tree, &current); err != nil {
			return err
		}
		nextLock := Lock{SchemaVersion: 1, Dependencies: next}
		if hooks.ValidateProspective != nil {
			if err := hooks.ValidateProspective(Prospective{AgentRoot: root, CandidateRoot: candidate.Root, Kind: kind, Name: name, Destination: current.Destination, Replacing: &current, CurrentLock: snapshot.Lock, NextLock: nextLock, Snapshot: snapshot}); err != nil {
				return err
			}
		}
		if err := manager.approve(candidate, updated, info); err != nil {
			return err
		}
		if updated == current {
			return nil
		}
		changed = true
		return publish(root, "update", current.Destination, current.TreeSHA256, updated.TreeSHA256, candidate, nextLock)
	})
	return updated, changed, err
}

func (manager Manager) approve(candidate *Candidate, dependency Dependency, info ComponentInfo) error {
	if manager.Approve == nil {
		return nil
	}
	executables := append([]string(nil), info.ExecutableFiles...)
	sort.Strings(executables)
	omitted := 0
	if len(executables) > 128 {
		omitted = len(executables) - 128
		executables = executables[:128]
	}
	summary := TrustSummary{
		Kind: dependency.Kind, Name: dependency.Name, Source: candidate.Source, Destination: dependency.Destination,
		TreeSHA256: dependency.TreeSHA256, FileCount: dependency.FileCount, ByteCount: dependency.ByteCount,
		ExecutablePaths: executables, AdditionalExecutables: omitted,
		PluginSkillCount: info.SkillCount, PluginMCPServerCount: info.MCPServerCount,
		AdditionalComponentInfo: info.AdditionalCounts,
	}
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > 64<<10 {
		return errors.New("dependency trust summary exceeds its bound")
	}
	return manager.Approve(summary)
}

func (manager Manager) Remove(ctx context.Context, kind Kind, name string, force bool) error {
	root, err := canonicalAgentRoot(manager.AgentRoot)
	if err != nil {
		return err
	}
	return withOperationLock(ctx, root, func() error {
		if err := recoverTransaction(root); err != nil {
			return err
		}
		lock, _, err := readLock(root)
		if err != nil {
			return err
		}
		index := dependencyIndex(lock.Dependencies, kind, name)
		if index < 0 {
			return fmt.Errorf("acquired %s %q is not recorded", kind, name)
		}
		selected := lock.Dependencies[index]
		for otherIndex, dependency := range lock.Dependencies {
			state, _, inspectErr := inspectDependency(root, dependency)
			if otherIndex == index {
				if inspectErr != nil {
					return inspectErr
				}
				if state != StateClean && !force {
					return fmt.Errorf("acquired %s %q is %s; explicit destructive removal is required", kind, name, state)
				}
				continue
			}
			if inspectErr != nil || state != StateClean {
				return fmt.Errorf("another acquired dependency %s is not clean", dependency.Destination)
			}
		}
		next := make([]Dependency, 0, len(lock.Dependencies)-1)
		next = append(next, lock.Dependencies[:index]...)
		next = append(next, lock.Dependencies[index+1:]...)
		nextLock := Lock{SchemaVersion: 1, Dependencies: next}
		nextBytes, err := EncodeLock(nextLock)
		if err != nil {
			return err
		}
		snapshot, err := readSnapshotForLockUnlocked(root, nextLock, nextBytes)
		if err != nil {
			return err
		}
		hooks, err := manager.hooks(kind)
		if err != nil {
			return err
		}
		if hooks.ValidateProspective != nil {
			if err := hooks.ValidateProspective(Prospective{AgentRoot: root, Kind: kind, Name: name, Destination: selected.Destination, Replacing: &selected, Removing: true, CurrentLock: lock, NextLock: nextLock, Snapshot: snapshot}); err != nil {
				return err
			}
		}
		return publish(root, "remove", selected.Destination, selected.TreeSHA256, "", nil, nextLock)
	})
}

func (manager Manager) Status(ctx context.Context, kind *Kind, name string) ([]ComponentStatus, error) {
	root, err := canonicalAgentRoot(manager.AgentRoot)
	if err != nil {
		return nil, err
	}
	var result []ComponentStatus
	err = withOperationLock(ctx, root, func() error {
		if err := recoverTransaction(root); err != nil {
			return err
		}
		lock, _, err := readLock(root)
		if err != nil {
			return err
		}
		if err := validateStatusAggregate(root, lock); err != nil {
			return err
		}
		tracked := map[string]Dependency{}
		for _, dependency := range lock.Dependencies {
			tracked[dependency.Destination] = dependency
		}
		kinds := []Kind{Plugin, Skill}
		if kind != nil {
			kinds = []Kind{*kind}
		}
		for _, currentKind := range kinds {
			hooks, err := manager.hooks(currentKind)
			if err != nil {
				return err
			}
			statuses, err := statusKind(root, currentKind, name, hooks, tracked)
			if err != nil {
				return err
			}
			result = append(result, statuses...)
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].Kind != result[j].Kind {
				return result[i].Kind < result[j].Kind
			}
			return result[i].Name < result[j].Name
		})
		if name != "" && len(result) == 0 {
			return fmt.Errorf("component %q does not exist", name)
		}
		return nil
	})
	return result, err
}

func (manager Manager) hooks(kind Kind) (Hooks, error) {
	var hooks Hooks
	switch kind {
	case Plugin:
		hooks = manager.Plugins
	case Skill:
		hooks = manager.Skills
	default:
		return Hooks{}, errors.New("component kind must be plugin or skill")
	}
	if hooks.Inspect == nil {
		return Hooks{}, fmt.Errorf("%s acquisition validator is not configured", kind)
	}
	return hooks, nil
}

// ReadSnapshot serializes with mutations, recovers interrupted publication,
// verifies every acquired tree offline, and captures all fingerprint bytes.
func ReadSnapshot(ctx context.Context, agentRoot string) (Snapshot, error) {
	var snapshot Snapshot
	err := WithSnapshot(ctx, agentRoot, func(captured Snapshot) error {
		snapshot = captured
		return nil
	})
	return snapshot, err
}

// WithSnapshot holds reader ownership while the caller consumes authored
// component bytes, preventing a concurrent publication from mixing versions.
func WithSnapshot(ctx context.Context, agentRoot string, operation func(Snapshot) error) error {
	root, err := canonicalAgentRoot(agentRoot)
	if err != nil {
		return err
	}
	return withOperationLock(ctx, root, func() error {
		if err := recoverTransaction(root); err != nil {
			return err
		}
		snapshot, err := readSnapshotUnlocked(root)
		if err != nil {
			return err
		}
		return operation(snapshot)
	})
}

func readSnapshotUnlocked(root string) (Snapshot, error) {
	lock, lockBytes, err := readLock(root)
	if err != nil {
		return Snapshot{}, err
	}
	return readSnapshotForLockUnlocked(root, lock, lockBytes)
}

func readSnapshotForLockUnlocked(root string, lock Lock, lockBytes []byte) (Snapshot, error) {
	result := Snapshot{Lock: lock, LockBytes: lockBytes}
	var aggregateEntries, aggregateBytes uint64
	for _, dependency := range lock.Dependencies {
		state, tree, err := inspectDependency(root, dependency)
		if err != nil {
			return Snapshot{}, err
		}
		if state != StateClean {
			return Snapshot{}, fmt.Errorf("acquired dependency %s is %s", dependency.Destination, state)
		}
		if err := claimAggregateTree(&aggregateEntries, &aggregateBytes, tree); err != nil {
			return Snapshot{}, err
		}
		result.Dependencies = append(result.Dependencies, VerifiedDependency{Dependency: dependency, Tree: tree})
		for _, entry := range tree.Entries {
			path := dependency.Destination + "/" + entry.Path
			if entry.Directory {
				result.Directories = append(result.Directories, path)
				continue
			}
			digest := sha256.Sum256(entry.Content)
			result.Files = append(result.Files, SnapshotEntry{Path: path, SHA256: hex.EncodeToString(digest[:]), Executable: entry.Executable, Content: entry.Content})
		}
	}
	if len(lockBytes) > 0 {
		digest := sha256.Sum256(lockBytes)
		result.Files = append(result.Files, SnapshotEntry{Path: LockFilename, SHA256: hex.EncodeToString(digest[:]), Content: lockBytes})
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	sort.Strings(result.Directories)
	return result, nil
}

func readLock(root string) (Lock, []byte, error) {
	path := filepath.Join(root, LockFilename)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Lock{SchemaVersion: 1, Dependencies: []Dependency{}}, nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxLockBytes {
		return Lock{}, nil, errors.New("hctl-dependencies.json must be a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Lock{}, nil, errors.New("cannot read hctl-dependencies.json")
	}
	lock, err := ParseLock(data)
	return lock, data, err
}

func inspectDependency(root string, dependency Dependency) (State, Tree, error) {
	path := filepath.Join(root, filepath.FromSlash(dependency.Destination))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return StateMissing, Tree{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", Tree{}, fmt.Errorf("acquired dependency %s has an unsafe destination", dependency.Destination)
	}
	tree, err := ReadTree(path)
	if err != nil {
		return "", Tree{}, fmt.Errorf("acquired dependency %s is unsafe: %w", dependency.Destination, err)
	}
	marker := "plugin.json"
	if dependency.Kind == Skill {
		marker = "SKILL.md"
	}
	markerSHA, err := markerDigest(tree, marker)
	if err != nil {
		return StateDrifted, tree, nil
	}
	if tree.SHA256 != dependency.TreeSHA256 || markerSHA != dependency.MarkerSHA256 || tree.FileCount != dependency.FileCount || tree.ByteCount != dependency.ByteCount {
		return StateDrifted, tree, nil
	}
	return StateClean, tree, nil
}

func validateProspectiveAggregate(snapshot Snapshot, candidate Tree, replacing *Dependency) error {
	entries, bytes := uint64(len(candidate.Entries)), candidate.ByteCount
	for _, verified := range snapshot.Dependencies {
		if replacing != nil && verified.Dependency.Destination == replacing.Destination {
			continue
		}
		entries += uint64(len(verified.Tree.Entries))
		bytes += verified.Tree.ByteCount
	}
	if entries > maxAggregateEntries || bytes > maxAggregateTreeByte {
		return errors.New("acquired dependencies exceed aggregate project bounds")
	}
	return nil
}

func validateStatusAggregate(root string, lock Lock) error {
	var entries, bytes uint64
	for _, dependency := range lock.Dependencies {
		state, tree, err := inspectDependency(root, dependency)
		if err != nil {
			return err
		}
		if state == StateMissing {
			continue
		}
		if err := claimAggregateTree(&entries, &bytes, tree); err != nil {
			return err
		}
	}
	return nil
}

func claimAggregateTree(entries, bytes *uint64, tree Tree) error {
	*entries += uint64(len(tree.Entries))
	*bytes += tree.ByteCount
	if *entries > maxAggregateEntries || *bytes > maxAggregateTreeByte {
		return errors.New("acquired dependencies exceed aggregate project bounds")
	}
	return nil
}

func dependencyIndex(dependencies []Dependency, kind Kind, name string) int {
	for index, dependency := range dependencies {
		if dependency.Kind == kind && dependency.Name == name {
			return index
		}
	}
	return -1
}

func selectorFromSource(root string, source Source) Selector {
	selector := Selector{Type: source.Type, Path: source.Path, URL: source.URL, Ref: source.Ref, SHA256: source.SHA256, Format: source.Format, Subdirectory: source.Subdirectory}
	if source.Type == SourceLocal {
		selector.Path = filepath.Join(root, filepath.FromSlash(source.Path))
	}
	return selector
}

func statusKind(root string, kind Kind, selected string, hooks Hooks, tracked map[string]Dependency) ([]ComponentStatus, error) {
	directory := filepath.Join(root, string(kind)+"s")
	if info, err := os.Lstat(directory); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, fmt.Errorf("%ss must be a real directory", kind)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot inspect %s directory", kind)
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		entries = nil
	} else if err != nil {
		return nil, fmt.Errorf("cannot read %s directory", kind)
	}
	result := []ComponentStatus{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || (selected != "" && entry.Name() != selected) {
			continue
		}
		destination := string(kind) + "s/" + entry.Name()
		seen[destination] = true
		dependency, acquired := tracked[destination]
		status := ComponentStatus{Kind: kind, Name: entry.Name(), State: StateUntracked}
		if acquired {
			state, _, inspectErr := inspectDependency(root, dependency)
			if inspectErr != nil {
				return nil, inspectErr
			}
			status.State, status.Dependency = state, &dependency
		}
		info, inspectErr := hooks.Inspect(filepath.Join(directory, entry.Name()))
		if inspectErr == nil {
			status.ManifestName = info.Name
			if acquired && info.Name != dependency.Name {
				return nil, fmt.Errorf("acquired dependency %s does not match its validated component name", destination)
			}
		} else if !acquired || status.State == StateClean {
			return nil, inspectErr
		}
		result = append(result, status)
	}
	for destination, dependency := range tracked {
		if dependency.Kind != kind || seen[destination] || (selected != "" && dependency.Name != selected) {
			continue
		}
		state, _, inspectErr := inspectDependency(root, dependency)
		if inspectErr != nil {
			return nil, inspectErr
		}
		copy := dependency
		result = append(result, ComponentStatus{Kind: kind, Name: dependency.Name, State: state, Dependency: &copy})
	}
	return result, nil
}

func withOperationLock(ctx context.Context, root string, operation func() error) error {
	value, _ := operationMutexes.LoadOrStore(root, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
	config, err := os.UserConfigDir()
	if err != nil || config == "" {
		return errors.New("cannot resolve dependency operation lock directory")
	}
	if err := os.MkdirAll(config, 0o700); err != nil {
		return errors.New("cannot create user configuration directory")
	}
	configInfo, err := os.Lstat(config)
	if err != nil || !configInfo.IsDir() || configInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("user configuration directory is unsafe")
	}
	directory := filepath.Join(config, "hctl", "dependency-locks")
	if err := rootfs.EnsurePrivateDir(config, "hctl/dependency-locks"); err != nil {
		return errors.New("cannot protect dependency operation lock directory")
	}
	digest := sha256.Sum256([]byte(root))
	lockPath := filepath.Join(directory, hex.EncodeToString(digest[:])+".lock")
	if info, err := os.Lstat(lockPath); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("dependency operation lock is unsafe")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect dependency operation lock")
	}
	lock := flock.New(lockPath)
	lockContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockContext, 25*time.Millisecond)
	if err != nil || !locked {
		return errors.New("cannot acquire dependency operation lock")
	}
	defer func() { _ = lock.Unlock() }()
	if err := os.Chmod(lockPath, 0o600); err != nil {
		return errors.New("cannot protect dependency operation lock")
	}
	return operation()
}

type transactionJournal struct {
	Operation     string `json:"operation"`
	Destination   string `json:"destination"`
	OldTreeSHA256 string `json:"old_tree_sha256,omitempty"`
	NewTreeSHA256 string `json:"new_tree_sha256,omitempty"`
	StagedTree    string `json:"staged_tree,omitempty"`
	BackupTree    string `json:"backup_tree,omitempty"`
	NextLock      string `json:"next_lock"`
	BackupLock    string `json:"backup_lock"`
}

func publish(root, operation, destination, oldTree, newTree string, candidate *Candidate, next Lock) error {
	nextBytes, err := EncodeLock(next)
	if err != nil {
		return err
	}
	nextLock, err := writeTemporary(root, ".hctl-dependency-lock-", nextBytes, 0o644)
	if err != nil {
		return err
	}
	journalOwnsTemporary := false
	defer func() {
		if !journalOwnsTemporary {
			_ = os.Remove(nextLock)
		}
	}()
	backupLock, err := reserveTemporary(root, ".hctl-dependency-lock-backup-")
	if err != nil {
		return err
	}
	backupTree := ""
	if operation != "add" {
		backupTree, err = reserveDirectory(root, ".hctl-dependency-backup-")
		if err != nil {
			return err
		}
	}
	journal := transactionJournal{
		Operation: operation, Destination: destination, OldTreeSHA256: oldTree, NewTreeSHA256: newTree,
		BackupTree: relativeName(root, backupTree), NextLock: relativeName(root, nextLock), BackupLock: relativeName(root, backupLock),
	}
	if candidate != nil {
		journal.StagedTree = relativeName(root, candidate.Root)
	}
	if err := writeJournal(root, journal); err != nil {
		return err
	}
	journalOwnsTemporary = true
	if candidate != nil {
		candidate.Root = ""
	}
	if publicationInterruptionHook != nil {
		if err := publicationInterruptionHook(); err != nil {
			return err
		}
	}
	if err := recoverTransaction(root); err != nil {
		return err
	}
	return nil
}

func recoverTransaction(root string) error {
	journalPath := filepath.Join(root, journalFilename)
	info, err := os.Lstat(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > maxJournalBytes {
		return errors.New("dependency transaction journal is unsafe")
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		return errors.New("cannot read dependency transaction journal")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return errors.New("dependency transaction journal is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal transactionJournal
	if err := decoder.Decode(&journal); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validateJournal(journal) != nil {
		return errors.New("dependency transaction journal is invalid")
	}
	destination := filepath.Join(root, filepath.FromSlash(journal.Destination))
	staged := joinJournalPath(root, journal.StagedTree)
	backupTree := joinJournalPath(root, journal.BackupTree)
	nextLock := joinJournalPath(root, journal.NextLock)
	backupLock := joinJournalPath(root, journal.BackupLock)

	if journal.Operation == "remove" {
		if _, err := os.Lstat(destination); err == nil {
			if _, err := treeIdentity(destination); err != nil {
				return errors.New("interrupted dependency removal destination is unsafe")
			}
			if _, backupErr := os.Lstat(backupTree); !errors.Is(backupErr, os.ErrNotExist) {
				return errors.New("cannot recover dependency removal with two active trees")
			}
			if err := os.Rename(destination, backupTree); err != nil {
				return errors.New("cannot recover dependency removal")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect interrupted dependency removal")
		}
	} else {
		state, err := treeIdentity(destination)
		if err != nil {
			return err
		}
		if state != journal.NewTreeSHA256 {
			if staged == "" {
				return errors.New("interrupted dependency publication lost its staged tree")
			}
			stagedIdentity, err := treeIdentity(staged)
			if err != nil || stagedIdentity != journal.NewTreeSHA256 {
				return errors.New("interrupted dependency staged tree is invalid")
			}
			if state != "" {
				if journal.Operation != "update" || state != journal.OldTreeSHA256 {
					return errors.New("interrupted dependency destination has an unexpected identity")
				}
				if _, err := os.Lstat(backupTree); !errors.Is(err, os.ErrNotExist) {
					return errors.New("interrupted dependency backup already exists")
				}
				if err := os.Rename(destination, backupTree); err != nil {
					return errors.New("cannot preserve old dependency tree during recovery")
				}
			}
			if err := ensurePublicationParent(root, journal.Destination); err != nil || os.Rename(staged, destination) != nil {
				return errors.New("cannot install dependency tree during recovery")
			}
		}
	}
	if err := installRecoveredLock(root, nextLock, backupLock, journal); err != nil {
		return err
	}
	if backupTree != "" {
		if err := removeRecoveryTree(root, backupTree); err != nil {
			return err
		}
	}
	if backupLock != "" {
		if err := removeRecoveryFile(root, backupLock); err != nil {
			return err
		}
	}
	if err := os.Remove(journalPath); err != nil {
		return errors.New("cannot finish dependency transaction recovery")
	}
	return syncDirectory(root)
}

func validateConventionalParent(root string, kind Kind) error {
	parent := filepath.Join(root, string(kind)+"s")
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%ss must be a real directory", kind)
	}
	return nil
}

func requireAvailableDestination(root, destination string) error {
	target := filepath.Join(root, filepath.FromSlash(destination))
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("dependency destination %s already exists", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot inspect dependency destination %s", destination)
	}
	parent := filepath.Dir(target)
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect dependency destination siblings")
	}
	key := canonicalCaselessPath(destination)
	for _, entry := range entries {
		sibling := filepath.ToSlash(filepath.Join(filepath.Dir(destination), entry.Name()))
		if canonicalCaselessPath(sibling) == key {
			return fmt.Errorf("dependency destination %s collides under Unicode canonical caseless matching", destination)
		}
	}
	return nil
}

func ensurePublicationParent(root, destination string) error {
	parent := filepath.Join(root, filepath.FromSlash(filepath.Dir(destination)))
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return os.Mkdir(parent, 0o755)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("dependency destination parent is unsafe")
	}
	return nil
}

func installRecoveredLock(root, nextLock, backupLock string, journal transactionJournal) error {
	lockPath := filepath.Join(root, LockFilename)
	if lockMatchesTree(lockPath, journal.Destination, journal.NewTreeSHA256, journal.Operation == "remove") {
		return nil
	}
	if nextLock == "" {
		return errors.New("interrupted dependency publication lost its next lock")
	}
	if _, _, err := readLockFile(nextLock); err != nil {
		return errors.New("interrupted dependency next lock is invalid")
	}
	if _, err := os.Lstat(lockPath); err == nil {
		if _, backupErr := os.Lstat(backupLock); !errors.Is(backupErr, os.ErrNotExist) {
			return errors.New("interrupted dependency lock backup already exists")
		}
		if err := os.Rename(lockPath, backupLock); err != nil {
			return errors.New("cannot preserve old dependency lock during recovery")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect interrupted dependency lock")
	}
	if err := os.Rename(nextLock, lockPath); err != nil {
		return errors.New("cannot install dependency lock during recovery")
	}
	return syncDirectory(root)
}

func lockMatchesTree(path, destination, tree string, removed bool) bool {
	lock, _, err := readLockFile(path)
	if err != nil {
		return false
	}
	for _, dependency := range lock.Dependencies {
		if dependency.Destination == destination {
			return !removed && dependency.TreeSHA256 == tree
		}
	}
	return removed
}

func readLockFile(path string) (Lock, []byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxLockBytes {
		return Lock{}, nil, errors.New("dependency lock file is unsafe")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Lock{}, nil, err
	}
	lock, err := ParseLock(data)
	return lock, data, err
}

func validateJournal(journal transactionJournal) error {
	if journal.Operation != "add" && journal.Operation != "update" && journal.Operation != "remove" {
		return errors.New("operation is invalid")
	}
	if err := validateTreePath(journal.Destination); err != nil || (!strings.HasPrefix(journal.Destination, "plugins/") && !strings.HasPrefix(journal.Destination, "skills/")) {
		return errors.New("destination is invalid")
	}
	if journal.OldTreeSHA256 != "" && !hexSHA256Pattern.MatchString(journal.OldTreeSHA256) {
		return errors.New("old tree identity is invalid")
	}
	if journal.NewTreeSHA256 != "" && !hexSHA256Pattern.MatchString(journal.NewTreeSHA256) {
		return errors.New("new tree identity is invalid")
	}
	for _, path := range []string{journal.StagedTree, journal.BackupTree, journal.NextLock, journal.BackupLock} {
		if path == "" {
			continue
		}
		if strings.Contains(path, "/") || !strings.HasPrefix(path, ".hctl-dependency-") && !strings.HasPrefix(path, ".hctl-acquire-") {
			return errors.New("temporary path is invalid")
		}
	}
	if journal.NextLock == "" || journal.BackupLock == "" {
		return errors.New("lock paths are required")
	}
	if journal.Operation == "add" && (journal.StagedTree == "" || journal.NewTreeSHA256 == "") {
		return errors.New("add journal is incomplete")
	}
	if journal.Operation == "update" && (journal.StagedTree == "" || journal.BackupTree == "" || journal.OldTreeSHA256 == "" || journal.NewTreeSHA256 == "") {
		return errors.New("update journal is incomplete")
	}
	if journal.Operation == "remove" && (journal.StagedTree != "" || journal.BackupTree == "" || journal.OldTreeSHA256 == "" || journal.NewTreeSHA256 != "") {
		return errors.New("remove journal is incomplete")
	}
	return nil
}

func writeJournal(root string, journal transactionJournal) error {
	data, err := json.Marshal(journal)
	if err != nil || len(data) > maxJournalBytes {
		return errors.New("cannot encode dependency transaction journal")
	}
	data = append(data, '\n')
	temporary, err := writeTemporary(root, ".hctl-dependency-journal-", data, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	if err := os.Rename(temporary, filepath.Join(root, journalFilename)); err != nil {
		return errors.New("cannot publish dependency transaction journal")
	}
	return syncDirectory(root)
}

func writeTemporary(root, pattern string, data []byte, mode os.FileMode) (string, error) {
	file, err := os.CreateTemp(root, pattern)
	if err != nil {
		return "", errors.New("cannot stage dependency transaction file")
	}
	name := file.Name()
	fail := func() (string, error) {
		_ = file.Close()
		_ = os.Remove(name)
		return "", errors.New("cannot write dependency transaction file")
	}
	if file.Chmod(mode) != nil {
		return fail()
	}
	if _, err := file.Write(data); err != nil || file.Sync() != nil || file.Close() != nil {
		return fail()
	}
	return name, nil
}

func reserveTemporary(root, pattern string) (string, error) {
	file, err := os.CreateTemp(root, pattern)
	if err != nil {
		return "", errors.New("cannot reserve dependency transaction path")
	}
	name := file.Name()
	if file.Close() != nil || os.Remove(name) != nil {
		return "", errors.New("cannot reserve dependency transaction path")
	}
	return name, nil
}

func reserveDirectory(root, pattern string) (string, error) {
	path, err := os.MkdirTemp(root, pattern)
	if err != nil {
		return "", errors.New("cannot reserve dependency backup path")
	}
	if err := os.Remove(path); err != nil {
		return "", errors.New("cannot reserve dependency backup path")
	}
	return path, nil
}

func relativeName(root, path string) string {
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

func joinJournalPath(root, name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(root, name)
}

func treeIdentity(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("dependency recovery tree is unsafe")
	}
	tree, err := ReadTree(path)
	if err != nil {
		return "", err
	}
	return tree.SHA256, nil
}

func removeRecoveryTree(root, path string) error {
	if filepath.Dir(path) != root || !strings.HasPrefix(filepath.Base(path), ".hctl-dependency-backup-") {
		return errors.New("dependency recovery backup path is unsafe")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := treeIdentity(path); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return errors.New("cannot remove recovered dependency backup")
	}
	return nil
}

func removeRecoveryFile(root, path string) error {
	if filepath.Dir(path) != root || !strings.HasPrefix(filepath.Base(path), ".hctl-dependency-lock-backup-") {
		return errors.New("dependency recovery lock backup path is unsafe")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("dependency recovery lock backup is unsafe")
	}
	if err := os.Remove(path); err != nil {
		return errors.New("cannot remove dependency recovery lock backup")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open dependency filesystem for synchronization")
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return errors.New("cannot synchronize dependency filesystem")
	}
	return nil
}
