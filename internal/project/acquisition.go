package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hctl/internal/acquisition"
	"hctl/internal/rootfs"
)

// AcquisitionManager binds the component-neutral acquisition engine to the
// existing Agent Plugin and Agent Skill semantic validators.
func AcquisitionManager(agentRoot string) acquisition.Manager {
	return acquisition.Manager{
		AgentRoot: agentRoot,
		Plugins: acquisition.Hooks{
			Inspect:             inspectAcquiredPlugin,
			ValidateProspective: validateAcquiredProspective,
		},
		Skills: acquisition.Hooks{
			Inspect:             inspectAcquiredSkill,
			ValidateProspective: validateAcquiredProspective,
		},
	}
}

func inspectAcquiredSkill(root string) (acquisition.ComponentInfo, error) {
	marker, err := rootfs.ReadSource(root, "SKILL.md", maxSourceBytes)
	if err != nil {
		return acquisition.ComponentInfo{}, fmt.Errorf("SKILL.md: %w", err)
	}
	frontmatter, err := parseSkill(marker)
	if err != nil {
		return acquisition.ComponentInfo{}, fmt.Errorf("SKILL.md: %w", err)
	}
	budget := &skillSetBudget{maxFiles: maxSkillSetFiles, maxBytes: maxSkillSetBytes}
	skill, err := loadSkill(root, ".", frontmatter.Name, budget)
	if err != nil {
		return acquisition.ComponentInfo{}, err
	}
	executables := []string{}
	for _, file := range skill.Files {
		if file.Executable {
			executables = append(executables, file.Path)
		}
	}
	return acquisition.ComponentInfo{Name: skill.Name, Marker: "SKILL.md", ManifestName: skill.Name, ExecutableFiles: executables}, nil
}

func inspectAcquiredPlugin(root string) (acquisition.ComponentInfo, error) {
	marker, err := rootfs.ReadSource(root, "plugin.json", maxSourceBytes)
	if err != nil {
		return acquisition.ComponentInfo{}, fmt.Errorf("plugin.json: %w", err)
	}
	if _, err := validatePluginManifest("plugin.json", marker); err != nil {
		return acquisition.ComponentInfo{}, fmt.Errorf("plugin.json: %w", err)
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(marker, &manifest) != nil || manifest.Name == "" {
		return acquisition.ComponentInfo{}, errors.New("plugin.json: cannot read validated name")
	}
	inspectionRoot, err := os.MkdirTemp(filepath.Dir(root), ".hctl-plugin-inspect-")
	if err != nil {
		return acquisition.ComponentInfo{}, errors.New("cannot stage acquired Plugin inspection")
	}
	defer func() { _ = os.RemoveAll(inspectionRoot) }()
	if err := os.Chmod(inspectionRoot, 0o700); err != nil {
		return acquisition.ComponentInfo{}, errors.New("cannot protect acquired Plugin inspection")
	}
	if err := rootfs.WriteAtomic(inspectionRoot, "instructions.md", []byte("---\ndescription: Plugin inspection.\n---\n\nInspect.\n"), 0o644); err != nil {
		return acquisition.ComponentInfo{}, err
	}
	pluginPath := "plugins/" + manifest.Name
	if err := copyProspectiveCandidate(root, inspectionRoot, pluginPath); err != nil {
		return acquisition.ComponentInfo{}, err
	}
	loaded, err := Load(inspectionRoot, "codex")
	if err != nil {
		return acquisition.ComponentInfo{}, fmt.Errorf("cannot validate acquired Plugin components: %w", err)
	}
	skillNames := []string{}
	for _, skill := range loaded.Skills {
		if strings.HasPrefix(skill.SourcePath, pluginPath+"/skills/") {
			skillNames = append(skillNames, skill.Name)
		}
	}
	serverNames := make([]string, 0, len(loaded.PluginMCPServers))
	for _, server := range loaded.PluginMCPServers {
		serverNames = append(serverNames, server.Name)
	}
	executables := []string{}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || path == root {
			return walkErr
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			relative, _ := filepath.Rel(root, path)
			executables = append(executables, filepath.ToSlash(relative))
		}
		return err
	})
	sort.Strings(executables)
	return acquisition.ComponentInfo{
		Name: manifest.Name, Marker: "plugin.json", ManifestName: manifest.Name,
		ExecutableFiles: executables, SkillNames: skillNames, MCPServerNames: serverNames,
		SkillCount: len(skillNames), MCPServerCount: len(serverNames),
	}, nil
}

const (
	maxProspectiveEntries = 65536
	maxProspectiveFile    = 384 << 20
	maxProspectiveBytes   = 1 << 30
)

func validateAcquiredProspective(request acquisition.Prospective) error {
	prospectiveRoot, err := os.MkdirTemp(request.AgentRoot, ".hctl-prospective-")
	if err != nil {
		return errors.New("cannot stage prospective agent project")
	}
	defer func() { _ = os.RemoveAll(prospectiveRoot) }()
	if err := os.Chmod(prospectiveRoot, 0o700); err != nil {
		return errors.New("cannot protect prospective agent project")
	}
	excluded := ""
	if request.Replacing != nil {
		excluded = request.Replacing.Destination
	}
	baseLock := request.CurrentLock
	if excluded != "" {
		baseLock.Dependencies = dependenciesWithoutDestination(baseLock.Dependencies, excluded)
	}
	baselineSnapshot, err := prospectiveSnapshot(request.Snapshot, baseLock, excluded)
	if err != nil {
		return err
	}
	projects := make([]*Project, 0, 2)
	for _, harness := range []string{"codex", "claude"} {
		loaded, err := loadCapturedExcluding(request.AgentRoot, request.AgentRoot, harness, "", baselineSnapshot, excluded)
		if err != nil {
			return fmt.Errorf("current project is invalid for %s before dependency mutation: %w", harness, err)
		}
		projects = append(projects, loaded)
	}
	if err := copyProspectiveProjectSources(request.AgentRoot, prospectiveRoot, projects...); err != nil {
		return err
	}
	if err := writeProspectiveLock(prospectiveRoot, baseLock); err != nil {
		return err
	}
	baseline, err := Load(prospectiveRoot, "codex")
	if err != nil {
		return fmt.Errorf("current project is invalid before dependency mutation: %w", err)
	}
	if request.Removing {
		if _, err := Load(prospectiveRoot, "claude"); err != nil {
			return fmt.Errorf("prospective project is invalid: %w", err)
		}
		return nil
	}
	info, err := inspectAcquiredSkill(request.CandidateRoot)
	if request.Kind == acquisition.Plugin {
		info, err = inspectAcquiredPlugin(request.CandidateRoot)
	}
	if err != nil {
		return err
	}
	if err := rejectProspectiveComponentCollisions(baseline, request.Kind, info); err != nil {
		return err
	}
	if err := copyProspectiveCandidate(request.CandidateRoot, prospectiveRoot, request.Destination); err != nil {
		return err
	}
	if err := writeProspectiveLock(prospectiveRoot, request.NextLock); err != nil {
		return err
	}
	for _, harness := range []string{"claude", "codex"} {
		if _, err := Load(prospectiveRoot, harness); err != nil {
			return fmt.Errorf("prospective %s project is invalid: %w", harness, err)
		}
	}
	return nil
}

func dependenciesWithoutDestination(dependencies []acquisition.Dependency, destination string) []acquisition.Dependency {
	result := make([]acquisition.Dependency, 0, len(dependencies))
	for _, dependency := range dependencies {
		if dependency.Destination != destination {
			result = append(result, dependency)
		}
	}
	return result
}

func rejectProspectiveComponentCollisions(baseline *Project, kind acquisition.Kind, candidate acquisition.ComponentInfo) error {
	skills := make(map[string]bool, len(baseline.Skills))
	for _, skill := range baseline.Skills {
		skills[skill.Name] = true
	}
	if kind == acquisition.Skill {
		if skills[candidate.Name] {
			return fmt.Errorf("acquired Skill name %q collides with an authored component", candidate.Name)
		}
		return nil
	}
	for _, name := range candidate.SkillNames {
		if skills[name] {
			return fmt.Errorf("acquired Plugin skill %q collides with an authored component", name)
		}
	}
	servers := map[string]bool{"managed": true}
	for _, server := range baseline.PluginMCPServers {
		servers[server.Name] = true
	}
	for _, name := range candidate.MCPServerNames {
		if servers[name] {
			return fmt.Errorf("acquired Plugin MCP server %q collides with an authored component", name)
		}
	}
	return nil
}

func prospectiveSnapshot(snapshot acquisition.Snapshot, lock acquisition.Lock, excluded string) (acquisition.Snapshot, error) {
	encoded, err := acquisition.EncodeLock(lock)
	if err != nil {
		return acquisition.Snapshot{}, err
	}
	result := acquisition.Snapshot{Lock: lock, LockBytes: encoded}
	prefix := excluded + "/"
	for _, dependency := range snapshot.Dependencies {
		if dependency.Dependency.Destination != excluded {
			result.Dependencies = append(result.Dependencies, dependency)
		}
	}
	for _, file := range snapshot.Files {
		if file.Path == acquisition.LockFilename || (excluded != "" && strings.HasPrefix(file.Path, prefix)) {
			continue
		}
		result.Files = append(result.Files, file)
	}
	result.Files = append(result.Files, acquisition.SnapshotEntry{Path: acquisition.LockFilename, SHA256: rootfs.SHA256(encoded), Content: encoded})
	for _, directory := range snapshot.Directories {
		if directory != excluded && (excluded == "" || !strings.HasPrefix(directory, prefix)) {
			result.Directories = append(result.Directories, directory)
		}
	}
	return result, nil
}

func copyProspectiveProjectSources(sourceRoot, destinationRoot string, projects ...*Project) error {
	records := map[string]SourceRecord{}
	contents := map[string][]byte{}
	directories := map[string]bool{}
	for _, project := range projects {
		for _, source := range project.Sources {
			if source.Path == acquisition.LockFilename {
				continue
			}
			if previous, exists := records[source.Path]; exists && previous != source {
				return fmt.Errorf("prospective source %s differs between harnesses", source.Path)
			}
			records[source.Path] = source
			if content, exists := project.SourceContents[source.Path]; exists {
				contents[source.Path] = content
			}
		}
		for _, directory := range project.SourceDirectories {
			directories[directory] = true
		}
	}
	if len(records)+len(directories) > maxProspectiveEntries {
		return errors.New("prospective project exceeds its entry bound")
	}
	paths := make([]string, 0, len(records))
	for path := range records {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var total int64
	for _, path := range paths {
		record := records[path]
		content, captured := contents[path]
		if !captured {
			info, err := os.Lstat(filepath.Join(sourceRoot, filepath.FromSlash(path)))
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxProspectiveFile {
				return fmt.Errorf("prospective source %s changed before validation", path)
			}
			content, err = rootfs.ReadSource(sourceRoot, path, maxProspectiveFile)
			if err != nil {
				return err
			}
		}
		if rootfs.SHA256(content) != record.SHA256 || total+int64(len(content)) > maxProspectiveBytes {
			return fmt.Errorf("prospective source %s changed or exceeds aggregate bounds", path)
		}
		total += int64(len(content))
		mode := os.FileMode(0o644)
		if record.Executable {
			mode = 0o755
		}
		if err := rootfs.WriteAtomic(destinationRoot, path, content, mode); err != nil {
			return err
		}
	}
	for directory := range directories {
		if err := os.MkdirAll(filepath.Join(destinationRoot, filepath.FromSlash(directory)), 0o755); err != nil {
			return errors.New("cannot stage prospective source directory")
		}
	}
	return nil
}

func copyProspectiveCandidate(candidateRoot, prospectiveRoot, destination string) error {
	tree, err := acquisition.ReadTree(candidateRoot)
	if err != nil {
		return err
	}
	for _, entry := range tree.Entries {
		relative := destination + "/" + entry.Path
		if entry.Directory {
			if err := os.MkdirAll(filepath.Join(prospectiveRoot, filepath.FromSlash(relative)), 0o755); err != nil {
				return errors.New("cannot stage prospective dependency directory")
			}
			continue
		}
		mode := os.FileMode(0o644)
		if entry.Executable {
			mode = 0o755
		}
		if err := rootfs.WriteAtomic(prospectiveRoot, relative, entry.Content, mode); err != nil {
			return err
		}
	}
	return nil
}

func writeProspectiveLock(root string, lock acquisition.Lock) error {
	data, err := acquisition.EncodeLock(lock)
	if err != nil {
		return err
	}
	return rootfs.WriteAtomic(root, acquisition.LockFilename, data, 0o644)
}
