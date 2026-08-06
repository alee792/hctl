package project

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	cronlib "github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"

	"hctl/internal/rootfs"
	"hctl/internal/tool"
)

const (
	GeneratorVersion   = "hctl/0.9.0-dev"
	maxSourceBytes     = 128 << 10
	maxSkills          = 8
	maxSkillFiles      = 128
	maxSkillFileBytes  = 1 << 20
	maxSkillBytes      = 8 << 20
	maxHarnessFiles    = 128
	maxHarnessBytes    = 8 << 20
	maxConnectionBytes = 8 << 10
	maxChannelBytes    = 8 << 10
	maxSchedules       = 32
	maxSchedulePrompt  = 32 << 10
	maxSubagents       = 8
	echoMaxInputBytes  = 1024
)

var (
	portableName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	skillName    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type Skill struct {
	Name                string
	Description         string
	License             string
	Compatibility       string
	Metadata            map[string]string
	AllowedTools        string
	AllowedToolsPresent bool
	ClaudeFields        []string
	Files               []File
}

type File struct {
	Path       string
	Content    []byte
	Executable bool
}

type Subagent struct {
	Name         string
	Description  string
	Effort       string
	Path         string
	Instructions []byte
	Source       []byte
}

type GitHubConnection struct {
	Description string
	Path        string
	Source      []byte
}

type DiscordChannel struct {
	Mode   string
	Policy []byte
	Path   string
	Source []byte
}

type Schedule struct {
	Name   string
	Cron   string
	Prompt []byte
	Path   string
	Source []byte
}

type SourceRecord struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Executable bool   `json:"executable,omitempty"`
}

type Project struct {
	SourceRoot        string
	WorkspaceRoot     string
	SourceReference   string
	AgentID           string
	Name              string
	Description       string
	Harness           string
	Instructions      []byte
	Skills            []Skill
	Subagents         []Subagent
	HarnessFiles      []File
	GitHubConnection  *GitHubConnection
	DiscordChannel    *DiscordChannel
	Schedules         []Schedule
	Tools             tool.Inventory
	SourceFingerprint string
	MaxToolInput      int
}

// Load reads an agent project. If workspace is omitted, the agent source is
// also its workspace.
func Load(source, harness string, workspace ...string) (*Project, error) {
	return load(source, harness, "", workspace...)
}

// LoadRelocated loads the same portable agent source from an isolated
// workspace while preserving the logical identity selected by the caller.
// The source fingerprint must still match exactly.
func LoadRelocated(source, harness, workspace string, selected *Project) (*Project, error) {
	if selected == nil || selected.Harness != harness {
		return nil, errors.New("relocated project identity is invalid")
	}
	p, err := load(source, harness, selected.Name, workspace)
	if err != nil {
		return nil, err
	}
	if p.SourceFingerprint != selected.SourceFingerprint {
		return nil, errors.New("relocated project source does not match selected agent")
	}
	p.AgentID = selected.AgentID
	return p, nil
}

func load(source, harness, logicalName string, workspace ...string) (*Project, error) {
	if harness != "claude" && harness != "codex" {
		return nil, errors.New("harness must be claude or codex")
	}
	if len(workspace) > 1 {
		return nil, errors.New("only one workspace may be selected")
	}
	sourceRoot, err := rootfs.CanonicalDir(source)
	if err != nil {
		return nil, err
	}
	workspaceRoot := sourceRoot
	if len(workspace) == 1 && workspace[0] != "" {
		workspaceRoot, err = rootfs.CanonicalDir(workspace[0])
		if err != nil {
			return nil, fmt.Errorf("workspace: %w", err)
		}
	}

	name := logicalName
	if name == "" {
		name = nameFromRoot(sourceRoot)
	}
	instructionPath := "instructions.md"
	instructionSource, err := rootfs.ReadSource(sourceRoot, instructionPath, maxSourceBytes)
	if err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}
	description, instructions, err := parseInstructions(instructionSource)
	if err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}

	skills, err := loadSkills(sourceRoot)
	if err != nil {
		return nil, err
	}
	tools, err := tool.Discover(sourceRoot)
	if err != nil {
		return nil, err
	}
	subagents, err := loadSubagents(sourceRoot)
	if err != nil {
		return nil, err
	}
	harnessFiles, err := loadHarnessFiles(sourceRoot, harness)
	if err != nil {
		return nil, err
	}
	githubConnection, err := loadGitHubConnection(sourceRoot)
	if err != nil {
		return nil, err
	}
	discordChannel, err := loadDiscordChannel(sourceRoot)
	if err != nil {
		return nil, err
	}
	schedules, err := loadSchedules(sourceRoot)
	if err != nil {
		return nil, err
	}
	toolNames := map[string]bool{"echo": true}
	for _, source := range tools.Sources {
		toolNames[source.Name] = true
	}
	for _, subagent := range subagents {
		if toolNames[subagent.Name] {
			return nil, fmt.Errorf("subagent %q conflicts with a tool of the same name", subagent.Name)
		}
	}

	sources := []SourceRecord{{Path: instructionPath, SHA256: rootfs.SHA256(instructionSource)}}
	for _, skill := range skills {
		for _, file := range skill.Files {
			sources = append(sources, SourceRecord{
				Path:       "skills/" + skill.Name + "/" + file.Path,
				SHA256:     rootfs.SHA256(file.Content),
				Executable: file.Executable,
			})
		}
	}
	for _, subagent := range subagents {
		sources = append(sources, SourceRecord{Path: subagent.Path, SHA256: rootfs.SHA256(subagent.Source)})
	}
	for _, file := range harnessFiles {
		sources = append(sources, SourceRecord{
			Path:       "harnesses/" + harness + "/" + file.Path,
			SHA256:     rootfs.SHA256(file.Content),
			Executable: file.Executable,
		})
	}
	if githubConnection != nil {
		sources = append(sources, SourceRecord{Path: githubConnection.Path, SHA256: rootfs.SHA256(githubConnection.Source)})
	}
	if discordChannel != nil {
		sources = append(sources, SourceRecord{Path: discordChannel.Path, SHA256: rootfs.SHA256(discordChannel.Source)})
	}
	for _, schedule := range schedules {
		sources = append(sources, SourceRecord{Path: schedule.Path, SHA256: rootfs.SHA256(schedule.Source)})
	}
	for _, file := range tools.Files {
		sources = append(sources, SourceRecord{Path: file.Path, SHA256: file.SHA256})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	canonicalSources, _ := json.Marshal(struct {
		Agent   string         `json:"agent"`
		Sources []SourceRecord `json:"sources"`
	}{Agent: name, Sources: sources})
	fingerprint := rootfs.SHA256(canonicalSources)
	sourceIdentity := rootfs.SHA256([]byte(sourceRoot))[:12]
	reference, err := filepath.Rel(workspaceRoot, sourceRoot)
	if err != nil {
		return nil, errors.New("cannot describe agent source relative to workspace")
	}

	return &Project{
		SourceRoot:        sourceRoot,
		WorkspaceRoot:     workspaceRoot,
		SourceReference:   filepath.ToSlash(reference),
		AgentID:           name + "@" + sourceIdentity,
		Name:              name,
		Description:       description,
		Harness:           harness,
		Instructions:      instructions,
		Skills:            skills,
		Subagents:         subagents,
		HarnessFiles:      harnessFiles,
		GitHubConnection:  githubConnection,
		DiscordChannel:    discordChannel,
		Schedules:         schedules,
		Tools:             tools,
		SourceFingerprint: fingerprint,
		MaxToolInput:      echoMaxInputBytes,
	}, nil
}

func loadSchedules(root string) ([]Schedule, error) {
	directory := filepath.Join(root, "schedules")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Schedule{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("schedules must be a real directory")
	}

	result := []Schedule{}
	err = filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot read schedules directory")
		}
		if path == directory {
			return nil
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entryInfo, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect schedule source")
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("schedules must not contain symlinks")
		}
		if entryInfo.IsDir() {
			return nil
		}
		if !entryInfo.Mode().IsRegular() || filepath.Ext(entry.Name()) != ".md" {
			return fmt.Errorf("schedules supports Markdown files only; found %q", entry.Name())
		}
		if len(result) == maxSchedules {
			return fmt.Errorf("agent may contain at most %d schedules", maxSchedules)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return errors.New("cannot describe schedule path")
		}
		relative = filepath.ToSlash(relative)
		if !utf8.ValidString(relative) {
			return errors.New("schedule paths must be valid UTF-8")
		}
		if _, err := rootfs.CleanRelative(relative); err != nil {
			return fmt.Errorf("schedule %q has an invalid path", relative)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(relative, "schedules/"), ".md")
		if len([]rune(name)) > 128 {
			return fmt.Errorf("schedule name %q exceeds 128 characters", name)
		}
		source, err := rootfs.ReadSource(root, relative, maxSourceBytes)
		if err != nil {
			return fmt.Errorf("schedule %q: %w", name, err)
		}
		cron, prompt, err := parseSchedule(source)
		if err != nil {
			return fmt.Errorf("schedule %q: %w", name, err)
		}
		result = append(result, Schedule{Name: name, Cron: cron, Prompt: prompt, Path: relative, Source: source})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func parseSchedule(content []byte) (string, []byte, error) {
	if !utf8.Valid(content) {
		return "", nil, errors.New("file must be valid UTF-8")
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) == 0 || string(bytes.TrimSuffix(lines[0], []byte("\r"))) != "---" {
		return "", nil, errors.New("file must start with YAML frontmatter")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if string(bytes.TrimSuffix(lines[index], []byte("\r"))) == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return "", nil, errors.New("frontmatter is not closed")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(bytes.Join(lines[1:closing], []byte("\n"))))
	if err := decoder.Decode(&document); err != nil {
		return "", nil, errors.New("frontmatter must be valid YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", nil, errors.New("frontmatter must contain one YAML document")
	}
	if err := validateYAMLTree(&document); err != nil {
		return "", nil, fmt.Errorf("frontmatter: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode || len(document.Content[0].Content) != 2 || document.Content[0].Content[0].Value != "cron" {
		return "", nil, errors.New("frontmatter supports one cron field only")
	}
	cron, err := yamlString(document.Content[0].Content[1], "cron")
	if err != nil {
		return "", nil, err
	}
	fields := strings.Fields(cron)
	if len(cron) > 256 || len(fields) != 5 || cron != strings.Join(fields, " ") {
		return "", nil, errors.New("frontmatter cron must be a bounded five-field string")
	}
	for _, character := range cron {
		if character < 0x20 || character > 0x7e {
			return "", nil, errors.New("frontmatter cron must contain printable ASCII")
		}
	}
	if _, err := cronlib.ParseStandard(cron); err != nil {
		return "", nil, errors.New("frontmatter cron must be a valid standard five-field expression")
	}
	prompt := bytes.Join(lines[closing+1:], []byte("\n"))
	if bytes.HasPrefix(prompt, []byte("\r\n")) {
		prompt = prompt[2:]
	} else if bytes.HasPrefix(prompt, []byte("\n")) {
		prompt = prompt[1:]
	}
	if strings.TrimSpace(string(prompt)) == "" {
		return "", nil, errors.New("markdown body must be non-empty")
	}
	if len(prompt) > maxSchedulePrompt {
		return "", nil, fmt.Errorf("markdown body exceeds %d bytes", maxSchedulePrompt)
	}
	return cron, prompt, nil
}

func loadDiscordChannel(root string) (*DiscordChannel, error) {
	directory := filepath.Join(root, "channels")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("channels must be a real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("cannot read channels directory")
	}
	for _, entry := range entries {
		if entry.Name() != "discord.md" {
			return nil, fmt.Errorf("channels supports discord.md only; found %q", entry.Name())
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}
	path := "channels/discord.md"
	source, err := rootfs.ReadSource(root, path, maxChannelBytes)
	if err != nil {
		return nil, fmt.Errorf("discord channel: %w", err)
	}
	mode, policy, err := parseDiscordChannel(source)
	if err != nil {
		return nil, fmt.Errorf("discord channel: %w", err)
	}
	return &DiscordChannel{Mode: mode, Policy: policy, Path: path, Source: source}, nil
}

func parseDiscordChannel(source []byte) (string, []byte, error) {
	if !utf8.Valid(source) {
		return "", nil, errors.New("file must be valid UTF-8")
	}
	scanner := bufio.NewScanner(bytes.NewReader(source))
	if !scanner.Scan() || strings.TrimSuffix(scanner.Text(), "\r") != "---" {
		return "", nil, errors.New("file must start with YAML frontmatter")
	}
	mode, closed := "", false
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "mode" || mode != "" {
			return "", nil, errors.New("frontmatter supports one plain mode only")
		}
		mode = strings.TrimSpace(value)
	}
	if !closed || mode != "ambient" {
		return "", nil, errors.New("frontmatter requires mode: ambient")
	}
	var body []string
	for scanner.Scan() {
		body = append(body, strings.TrimSuffix(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return "", nil, errors.New("cannot read channel policy")
	}
	policy := strings.TrimSpace(strings.Join(body, "\n"))
	if policy == "" || len([]rune(policy)) > 1024 {
		return "", nil, errors.New("markdown participation policy must contain 1-1024 characters")
	}
	return mode, []byte(policy + "\n"), nil
}

func loadGitHubConnection(root string) (*GitHubConnection, error) {
	directory := filepath.Join(root, "connections")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("connections must be a real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("cannot read connections directory")
	}
	for _, entry := range entries {
		if entry.Name() != "github.md" {
			return nil, fmt.Errorf("connections supports github.md only; found %q", entry.Name())
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}
	path := "connections/github.md"
	source, err := rootfs.ReadSource(root, path, maxConnectionBytes)
	if err != nil {
		return nil, fmt.Errorf("GitHub connection: %w", err)
	}
	if !utf8.Valid(source) {
		return nil, errors.New("GitHub connection description must be valid UTF-8")
	}
	description := strings.TrimSpace(string(source))
	if description == "" || len([]rune(description)) > 1024 {
		return nil, errors.New("GitHub connection description must contain 1-1024 characters")
	}
	return &GitHubConnection{Description: description, Path: path, Source: source}, nil
}

func loadHarnessFiles(root, harness string) ([]File, error) {
	harnessesDirectory := filepath.Join(root, "harnesses")
	info, err := os.Lstat(harnessesDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return []File{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("harnesses must be a real directory")
	}

	selectedDirectory := filepath.Join(harnessesDirectory, harness)
	info, err = os.Lstat(selectedDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return []File{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("harnesses/%s must be a real directory", harness)
	}

	nativeDirectoryName := "." + harness
	entries, err := os.ReadDir(selectedDirectory)
	if err != nil {
		return nil, fmt.Errorf("cannot read harnesses/%s", harness)
	}
	for _, entry := range entries {
		if entry.Name() != nativeDirectoryName {
			return nil, fmt.Errorf("harnesses/%s supports %s only; found %q", harness, nativeDirectoryName, entry.Name())
		}
	}
	if len(entries) == 0 {
		return []File{}, nil
	}

	nativeDirectory := filepath.Join(selectedDirectory, nativeDirectoryName)
	info, err = os.Lstat(nativeDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("harnesses/%s/%s must be a real directory", harness, nativeDirectoryName)
	}

	files := []File{}
	total := 0
	err = filepath.WalkDir(nativeDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("cannot read harnesses/%s/%s", harness, nativeDirectoryName)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect harness-specific file")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("harnesses/%s/%s must not contain symlinks", harness, nativeDirectoryName)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("harness-specific entries must be regular files or real directories")
		}
		if len(files) == maxHarnessFiles {
			return fmt.Errorf("harnesses/%s may contain at most %d files", harness, maxHarnessFiles)
		}
		relative, err := filepath.Rel(selectedDirectory, path)
		if err != nil {
			return errors.New("cannot describe harness-specific file path")
		}
		relative = filepath.ToSlash(relative)
		if !utf8.ValidString(relative) {
			return errors.New("harness-specific file paths must be valid UTF-8")
		}
		if _, err := rootfs.CleanRelative(relative); err != nil {
			return fmt.Errorf("harness-specific file %q has an invalid path", relative)
		}
		if reservedHarnessPath(harness, relative) {
			return fmt.Errorf("harness-specific file %q is reserved for hctl", relative)
		}
		sourcePath := "harnesses/" + harness + "/" + relative
		content, err := rootfs.ReadSource(root, sourcePath, maxSkillFileBytes)
		if err != nil {
			return err
		}
		total += len(content)
		if total > maxHarnessBytes {
			return fmt.Errorf("harnesses/%s files exceed %d bytes", harness, maxHarnessBytes)
		}
		files = append(files, File{Path: relative, Content: content, Executable: info.Mode().Perm()&0o111 != 0})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// IsHarnessFilePath reports whether path is an author-owned native project file.
func IsHarnessFilePath(harness, path string) bool {
	if harness != "claude" && harness != "codex" || !strings.HasPrefix(path, "."+harness+"/") {
		return false
	}
	_, err := rootfs.CleanRelative(path)
	return err == nil && !reservedHarnessPath(harness, path)
}

func reservedHarnessPath(harness, path string) bool {
	path = strings.ToLower(path)
	if harness == "claude" {
		return path == ".claude/skills" || strings.HasPrefix(path, ".claude/skills/") ||
			path == ".claude/agents" || strings.HasPrefix(path, ".claude/agents/")
	}
	return path == ".codex/config.toml" || path == ".codex/agents" || strings.HasPrefix(path, ".codex/agents/")
}

func parseInstructions(content []byte) (string, []byte, error) {
	if !utf8.Valid(content) {
		return "", nil, errors.New("file must be valid UTF-8")
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	if !scanner.Scan() || strings.TrimSuffix(scanner.Text(), "\r") != "---" {
		return "", nil, errors.New("file must start with YAML frontmatter")
	}
	description := ""
	closed := false
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "description" || description != "" {
			return "", nil, errors.New("frontmatter supports one plain description only")
		}
		description = strings.TrimSpace(value)
		if description == "" || len(description) > 1024 {
			return "", nil, errors.New("frontmatter description must be non-empty and bounded")
		}
	}
	if !closed || description == "" {
		return "", nil, errors.New("frontmatter requires one plain description")
	}
	var body []string
	for scanner.Scan() {
		body = append(body, strings.TrimSuffix(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return "", nil, errors.New("cannot read instructions")
	}
	trimmed := strings.TrimSpace(strings.Join(body, "\n"))
	if trimmed == "" {
		return "", nil, errors.New("markdown body must be non-empty")
	}
	return description, []byte(trimmed + "\n"), nil
}

func loadSubagents(root string) ([]Subagent, error) {
	directory := filepath.Join(root, "subagents")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Subagent{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("subagents must be a real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("cannot read subagents directory")
	}
	result := make([]Subagent, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if len(result) == maxSubagents {
			return nil, fmt.Errorf("agent may contain at most %d subagents", maxSubagents)
		}
		if !portableName.MatchString(entry.Name()) {
			return nil, fmt.Errorf("subagent directory %q must use lowercase letters, numbers, and hyphens", entry.Name())
		}
		childDirectory := filepath.Join(directory, entry.Name())
		childInfo, err := os.Lstat(childDirectory)
		if err != nil || !childInfo.IsDir() || childInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("subagent %q must be a real directory", entry.Name())
		}
		children, err := os.ReadDir(childDirectory)
		if err != nil {
			return nil, fmt.Errorf("cannot read subagent %q", entry.Name())
		}
		for _, child := range children {
			if strings.HasPrefix(child.Name(), ".") {
				continue
			}
			if child.Name() != "instructions.md" {
				return nil, fmt.Errorf("subagent %q supports instructions.md only; found %q", entry.Name(), child.Name())
			}
		}
		path := "subagents/" + entry.Name() + "/instructions.md"
		source, err := rootfs.ReadSource(root, path, maxSourceBytes)
		if err != nil {
			return nil, fmt.Errorf("subagent %q: %w", entry.Name(), err)
		}
		description, effort, instructions, err := parseSubagentInstructions(source)
		if err != nil {
			return nil, fmt.Errorf("subagent %q instructions: %w", entry.Name(), err)
		}
		result = append(result, Subagent{Name: entry.Name(), Description: description, Effort: effort, Path: path, Instructions: instructions, Source: source})
	}
	return result, nil
}

func parseSubagentInstructions(content []byte) (string, string, []byte, error) {
	if !utf8.Valid(content) {
		return "", "", nil, errors.New("file must be valid UTF-8")
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	if !scanner.Scan() || strings.TrimSuffix(scanner.Text(), "\r") != "---" {
		return "", "", nil, errors.New("file must start with YAML frontmatter")
	}
	description, effort := "", ""
	seen := map[string]bool{}
	closed := false
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return "", "", nil, errors.New("frontmatter fields must use plain key: value lines")
		}
		if seen[key] {
			return "", "", nil, fmt.Errorf("frontmatter field %q is duplicated", key)
		}
		seen[key] = true
		switch key {
		case "description":
			description = strings.TrimSpace(value)
		case "effort":
			var err error
			effort, err = parseEffort(value)
			if err != nil {
				return "", "", nil, err
			}
		default:
			return "", "", nil, fmt.Errorf("frontmatter field %q is not supported", key)
		}
	}
	if !closed {
		return "", "", nil, errors.New("frontmatter is not closed")
	}
	if description == "" || len(description) > 1024 {
		return "", "", nil, errors.New("frontmatter description must be non-empty and bounded")
	}

	var body []string
	for scanner.Scan() {
		body = append(body, strings.TrimSuffix(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return "", "", nil, errors.New("cannot read instructions")
	}
	trimmed := strings.TrimSpace(strings.Join(body, "\n"))
	if trimmed == "" {
		return "", "", nil, errors.New("markdown body must be non-empty")
	}
	return description, effort, []byte(trimmed + "\n"), nil
}

func parseEffort(raw string) (string, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(strings.TrimSpace(raw)), &document); err != nil {
		return "", errors.New("frontmatter field \"effort\" must be a string")
	}
	if err := validateYAMLTree(&document); err != nil {
		return "", fmt.Errorf("frontmatter field \"effort\": %w", err)
	}
	if len(document.Content) != 1 {
		return "", errors.New("frontmatter field \"effort\" must be a string")
	}
	effort, err := yamlString(document.Content[0], "effort")
	if err != nil {
		return "", err
	}
	if effort != "low" && effort != "medium" && effort != "high" {
		return "", errors.New("frontmatter field \"effort\" must be low, medium, or high")
	}
	return effort, nil
}

func loadSkills(root string) ([]Skill, error) {
	directory := filepath.Join(root, "skills")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Skill{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("skills must be a real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, errors.New("cannot read skills directory")
	}
	skills := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".md") {
			name := strings.TrimSuffix(entry.Name(), ".md")
			return nil, fmt.Errorf("skill %q uses the removed flat layout; move it to %q", "skills/"+entry.Name(), "skills/"+name+"/SKILL.md")
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.IsDir() || entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("skills may contain real skill directories only; found %q", entry.Name())
		}
		if len(skills) == maxSkills {
			return nil, fmt.Errorf("agent may contain at most %d skills", maxSkills)
		}
		name := entry.Name()
		if !validSkillName(name) {
			return nil, fmt.Errorf("skill directory %q must be 1-64 lowercase ASCII letters, numbers, and single hyphens", name)
		}
		files, err := loadSkillFiles(root, name)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", name, err)
		}
		frontmatter, err := parseSkill(files[0].Content)
		if err != nil {
			return nil, fmt.Errorf("skill %q SKILL.md: %w", name, err)
		}
		if frontmatter.Name != name {
			return nil, fmt.Errorf("skill %q name must match its parent directory", name)
		}
		skills = append(skills, Skill{
			Name:                name,
			Description:         frontmatter.Description,
			License:             frontmatter.License,
			Compatibility:       frontmatter.Compatibility,
			Metadata:            frontmatter.Metadata,
			AllowedTools:        frontmatter.AllowedTools,
			AllowedToolsPresent: frontmatter.AllowedToolsPresent,
			ClaudeFields:        frontmatter.ClaudeFields,
			Files:               files,
		})
	}
	return skills, nil
}

func loadSkillFiles(root, name string) ([]File, error) {
	directory := filepath.Join(root, "skills", name)
	files := []File{}
	total := 0
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot read skill directory")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("cannot inspect skill resource")
		}
		sourcePath, err := filepath.Rel(root, path)
		if err != nil {
			return errors.New("cannot describe skill resource path")
		}
		sourcePath = filepath.ToSlash(sourcePath)
		if !utf8.ValidString(sourcePath) {
			return errors.New("skill resource paths must be valid UTF-8")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s must not contain symlinks", sourcePath)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", sourcePath)
		}
		if len(files) == maxSkillFiles {
			return fmt.Errorf("may contain at most %d files", maxSkillFiles)
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return errors.New("cannot describe skill resource path")
		}
		relative = filepath.ToSlash(relative)
		limit := int64(maxSkillFileBytes)
		if relative == "SKILL.md" {
			limit = maxSourceBytes
		}
		content, err := rootfs.ReadSource(root, sourcePath, limit)
		if err != nil {
			return err
		}
		total += len(content)
		if total > maxSkillBytes {
			return fmt.Errorf("resources exceed %d bytes", maxSkillBytes)
		}
		files = append(files, File{Path: relative, Content: content, Executable: info.Mode().Perm()&0o111 != 0})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Path == "SKILL.md" && files[j].Path != "SKILL.md" {
			return true
		}
		if files[j].Path == "SKILL.md" {
			return false
		}
		return files[i].Path < files[j].Path
	})
	if len(files) == 0 || files[0].Path != "SKILL.md" {
		return nil, errors.New("SKILL.md is required")
	}
	if !utf8.Valid(files[0].Content) {
		return nil, errors.New("SKILL.md must be valid UTF-8")
	}
	return files, nil
}

func nameFromRoot(root string) string {
	var name strings.Builder
	separator := false
	for _, character := range strings.ToLower(filepath.Base(root)) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			if separator && name.Len() > 0 {
				name.WriteByte('-')
			}
			name.WriteRune(character)
			separator = false
		} else {
			separator = true
		}
	}
	result := strings.Trim(name.String(), "-")
	if result == "" {
		return "agent"
	}
	if result[0] >= '0' && result[0] <= '9' {
		result = "agent-" + result
	}
	if len(result) > 63 {
		result = strings.TrimRight(result[:63], "-")
	}
	return result
}

type skillFrontmatter struct {
	Name                string
	Description         string
	License             string
	Compatibility       string
	Metadata            map[string]string
	AllowedTools        string
	AllowedToolsPresent bool
	ClaudeFields        []string
}

var claudeSkillFields = map[string]bool{
	"when_to_use":              true,
	"argument-hint":            true,
	"arguments":                true,
	"disable-model-invocation": true,
	"user-invocable":           true,
	"disallowed-tools":         true,
	"model":                    true,
	"effort":                   true,
	"context":                  true,
	"agent":                    true,
	"background":               true,
	"hooks":                    true,
	"paths":                    true,
	"shell":                    true,
}

func parseSkill(content []byte) (skillFrontmatter, error) {
	block, err := yamlFrontmatter(content)
	if err != nil {
		return skillFrontmatter{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(block))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return skillFrontmatter{}, errors.New("frontmatter must be valid YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return skillFrontmatter{}, errors.New("frontmatter must contain one YAML document")
	}
	if err := validateYAMLTree(&document); err != nil {
		return skillFrontmatter{}, fmt.Errorf("frontmatter: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return skillFrontmatter{}, errors.New("frontmatter must be a YAML mapping")
	}

	result := skillFrontmatter{}
	compatibilityPresent := false
	root := document.Content[0]
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index].Value, root.Content[index+1]
		switch key {
		case "name":
			result.Name, err = yamlString(value, key)
		case "description":
			result.Description, err = yamlString(value, key)
		case "license":
			result.License, err = yamlString(value, key)
		case "compatibility":
			compatibilityPresent = true
			result.Compatibility, err = yamlString(value, key)
		case "metadata":
			result.Metadata, err = yamlStringMap(value, key)
		case "allowed-tools":
			result.AllowedToolsPresent = true
			result.AllowedTools, err = yamlString(value, key)
		default:
			if !claudeSkillFields[key] {
				return skillFrontmatter{}, fmt.Errorf("frontmatter field %q is not supported", key)
			}
			if value.Tag == "!!null" {
				return skillFrontmatter{}, fmt.Errorf("frontmatter field %q must not be null", key)
			}
			result.ClaudeFields = append(result.ClaudeFields, key)
		}
		if err != nil {
			return skillFrontmatter{}, err
		}
	}
	if !validSkillName(result.Name) {
		return skillFrontmatter{}, errors.New("frontmatter name must be 1-64 lowercase ASCII letters, numbers, and single hyphens")
	}
	if strings.TrimSpace(result.Description) == "" || len([]rune(result.Description)) > 1024 {
		return skillFrontmatter{}, errors.New("frontmatter description must contain 1-1024 characters")
	}
	if compatibilityPresent && (strings.TrimSpace(result.Compatibility) == "" || len([]rune(result.Compatibility)) > 500) {
		return skillFrontmatter{}, errors.New("frontmatter compatibility must contain 1-500 characters when provided")
	}
	sort.Strings(result.ClaudeFields)
	return result, nil
}

func yamlFrontmatter(content []byte) ([]byte, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("SKILL.md must be valid UTF-8")
	}
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) == 0 || string(bytes.TrimSuffix(lines[0], []byte("\r"))) != "---" {
		return nil, errors.New("SKILL.md must start with YAML frontmatter")
	}
	for index := 1; index < len(lines); index++ {
		if string(bytes.TrimSuffix(lines[index], []byte("\r"))) == "---" {
			return bytes.Join(lines[1:index], []byte("\n")), nil
		}
	}
	return nil, errors.New("SKILL.md frontmatter is not closed")
}

func validateYAMLTree(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("YAML mapping keys must be strings")
			}
			if seen[key.Value] {
				return fmt.Errorf("YAML field %q is duplicated", key.Value)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLTree(child); err != nil {
			return err
		}
	}
	return nil
}

func yamlString(node *yaml.Node, field string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("frontmatter field %q must be a string", field)
	}
	return node.Value, nil
}

func yamlStringMap(node *yaml.Node, field string) (map[string]string, error) {
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("frontmatter field %q must map strings to strings", field)
	}
	result := make(map[string]string, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		value := node.Content[index+1]
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return nil, fmt.Errorf("frontmatter field %q must map strings to strings", field)
		}
		result[node.Content[index].Value] = value.Value
	}
	return result, nil
}

func validSkillName(name string) bool {
	return len(name) <= 64 && skillName.MatchString(name)
}
