// Package project loads and validates portable hctl agent projects.
//
// Loading reads bounded project sources, rejects paths that escape the source
// tree, imports supported Agent Plugin skills and MCP declarations, and
// computes the fingerprint used to bind generated state to an exact source
// revision. A load also serializes with acquired-dependency operations and may
// complete an interrupted hctl-owned publication before reading source; normal
// loading does not change authored files. Invalid optional plugin MCP
// declarations are reported as diagnostics so the rest of a valid plugin can
// remain available.
//
// Package project describes plugin MCP servers but does not start, authorize,
// supervise, or communicate with them. Native harness configuration and
// persistent plugin data directories are handled by package setup.
package project

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	cronlib "github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v3"

	"hctl/internal/integration"
	"hctl/internal/rootfs"
	"hctl/internal/tool"
	"hctl/internal/version"
)

const (
	maxSourceBytes        = 128 << 10
	maxSkills             = 256
	maxPlugins            = 128
	maxPluginSkills       = 1024
	maxPluginMCPServers   = 128
	maxPluginCommandBytes = 8 << 20
	maxSkillFiles         = 1024
	maxSkillSetFiles      = 8192
	maxSkillFileBytes     = 16 << 20
	maxSkillBytes         = 64 << 20
	maxSkillSetBytes      = 64 << 20
	maxHarnessFiles       = 1024
	maxHarnessFileBytes   = 1 << 20
	maxHarnessBytes       = 8 << 20
	maxConnections        = 128
	maxConnectionBytes    = 8 << 10
	maxChannelBytes       = 8 << 10
	maxSchedules          = 256
	maxScheduleBytes      = 16 << 20
	maxSchedulePrompt     = 32 << 10
	maxSubagents          = 128
	maxSubagentBytes      = 16 << 20
	echoMaxInputBytes     = 1024
)

var GeneratorVersion = "hctl/" + version.Value

var (
	portableName   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	skillName      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	pluginName     = regexp.MustCompile(`^(?:[a-z0-9]|[a-z0-9](?:[a-z0-9.-]*[a-z0-9]))$`)
	connectionName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
)

const pluginSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
const pluginMCPSchema = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"

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
	SourcePath          string
}

type Diagnostic struct {
	Path    string
	Field   string
	Message string
}

type File struct {
	Path       string
	Content    []byte
	Executable bool
}

type skillSetBudget struct {
	files    int
	bytes    int64
	maxFiles int
	maxBytes int64
}

type byteBudget struct {
	used  int64
	max   int64
	label string
}

func (budget *byteBudget) claim(path string, size int) error {
	if budget.used+int64(size) > budget.max {
		return fmt.Errorf("%s exceeds the aggregate %s limit of %d bytes", path, budget.label, budget.max)
	}
	budget.used += int64(size)
	return nil
}

func (budget *skillSetBudget) claim(path string, size int64) error {
	if budget.files == budget.maxFiles {
		return fmt.Errorf("%s exceeds the aggregate %d-file skill-set limit", path, budget.maxFiles)
	}
	if budget.bytes+size > budget.maxBytes {
		return fmt.Errorf("%s exceeds the aggregate %d-byte skill-set limit", path, budget.maxBytes)
	}
	budget.files++
	budget.bytes += size
	return nil
}

type Subagent struct {
	Name         string
	Description  string
	Effort       string
	Path         string
	Instructions []byte
	Source       []byte
}

type Connection struct {
	Name       string
	Package    string
	Capability string
	Transport  string
	URL        string
	Context    string
	Path       string
	Source     []byte
}

func (connection Connection) Installed() bool { return connection.Package != "" }
func (connection Connection) Remote() bool    { return connection.URL != "" }

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

type PluginMCPServer struct {
	Name       string
	Type       string
	Command    string
	Args       []string
	Env        map[string]string
	CWD        string
	URL        string
	Headers    map[string]string
	PluginPath string
	DataPath   string
	SourcePath string
	sources    []SourceRecord
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
	PluginMCPServers  []PluginMCPServer
	Diagnostics       []Diagnostic
	Subagents         []Subagent
	HarnessFiles      []File
	Connections       []Connection
	DiscordChannel    *DiscordChannel
	Schedules         []Schedule
	Tools             tool.Inventory
	Sources           []SourceRecord
	SourceFingerprint string
	MaxToolInput      int
	FrictionNotes     bool
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
	setPluginDataPaths(p.PluginMCPServers, selected.AgentID)
	return p, nil
}

// WithRuntimeRoots returns the same validated project with the canonical paths
// it will use at runtime. It does not read either path. Staging uses this after
// preparing source and workspace files beneath a temporary physical root so
// generated native configuration never captures that temporary location.
func WithRuntimeRoots(selected *Project, sourceRoot, workspaceRoot string) (*Project, error) {
	if selected == nil || !filepath.IsAbs(sourceRoot) || !filepath.IsAbs(workspaceRoot) || filepath.Clean(sourceRoot) != sourceRoot || filepath.Clean(workspaceRoot) != workspaceRoot {
		return nil, errors.New("runtime project roots must be clean absolute paths")
	}
	reference, err := filepath.Rel(workspaceRoot, sourceRoot)
	if err != nil {
		return nil, errors.New("cannot describe runtime agent source relative to workspace")
	}
	result := *selected
	result.SourceRoot = sourceRoot
	result.WorkspaceRoot = workspaceRoot
	result.SourceReference = filepath.ToSlash(reference)
	result.AgentID = result.Name + "@" + rootfs.SHA256([]byte(sourceRoot))[:12]
	result.PluginMCPServers = append([]PluginMCPServer(nil), selected.PluginMCPServers...)
	setPluginDataPaths(result.PluginMCPServers, result.AgentID)
	return &result, nil
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
	return loadProject(sourceRoot, workspaceRoot, harness, logicalName)
}

func loadProject(sourceRoot, workspaceRoot, harness, logicalName string) (*Project, error) {
	name := logicalName
	if name == "" {
		name = nameFromRoot(sourceRoot)
	}
	sourceIdentity := rootfs.SHA256([]byte(sourceRoot))[:12]
	agentID := name + "@" + sourceIdentity
	instructionPath := "instructions.md"
	instructionSource, err := rootfs.ReadSource(sourceRoot, instructionPath, maxSourceBytes)
	if err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}
	description, frictionNotes, instructions, err := parseInstructions(instructionSource)
	if err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}

	skillBudget := &skillSetBudget{maxFiles: maxSkillSetFiles, maxBytes: maxSkillSetBytes}
	skills, err := loadSkills(sourceRoot, skillBudget)
	if err != nil {
		return nil, err
	}
	skills, pluginMCPServers, pluginSources, diagnostics, err := loadPlugins(sourceRoot, workspaceRoot, harness, agentID, skills, skillBudget)
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
	connections, err := loadConnections(sourceRoot)
	if err != nil {
		return nil, err
	}
	for _, connection := range connections {
		for _, server := range pluginMCPServers {
			if connection.Name == server.Name {
				return nil, fmt.Errorf("%s: standalone MCP connection name %q collides with an authored plugin server", connection.Path, connection.Name)
			}
		}
	}
	discordChannel, err := loadDiscordChannel(sourceRoot)
	if err != nil {
		return nil, err
	}
	schedules, err := loadSchedules(sourceRoot)
	if err != nil {
		return nil, err
	}
	toolNames := map[string]bool{"echo": true, "record-friction": true}
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
				Path:       skill.SourcePath + "/" + file.Path,
				SHA256:     rootfs.SHA256(file.Content),
				Executable: file.Executable,
			})
		}
	}
	sources = append(sources, pluginSources...)
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
	for _, connection := range connections {
		sources = append(sources, SourceRecord{Path: connection.Path, SHA256: rootfs.SHA256(connection.Source)})
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
	sourceByPath := make(map[string]SourceRecord, len(sources))
	for _, source := range sources {
		if previous, exists := sourceByPath[source.Path]; exists && previous != source {
			return nil, fmt.Errorf("agent source path %s has conflicting identities", source.Path)
		}
		sourceByPath[source.Path] = source
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	canonicalSources, _ := json.Marshal(struct {
		Agent   string         `json:"agent"`
		Sources []SourceRecord `json:"sources"`
	}{Agent: name, Sources: sources})
	fingerprint := rootfs.SHA256(canonicalSources)
	reference, err := filepath.Rel(workspaceRoot, sourceRoot)
	if err != nil {
		return nil, errors.New("cannot describe agent source relative to workspace")
	}

	return &Project{
		SourceRoot:        sourceRoot,
		WorkspaceRoot:     workspaceRoot,
		SourceReference:   filepath.ToSlash(reference),
		AgentID:           agentID,
		Name:              name,
		Description:       description,
		Harness:           harness,
		Instructions:      instructions,
		Skills:            skills,
		PluginMCPServers:  pluginMCPServers,
		Diagnostics:       diagnostics,
		Subagents:         subagents,
		HarnessFiles:      harnessFiles,
		Connections:       connections,
		DiscordChannel:    discordChannel,
		Schedules:         schedules,
		Tools:             tools,
		Sources:           sources,
		SourceFingerprint: fingerprint,
		MaxToolInput:      echoMaxInputBytes,
		FrictionNotes:     frictionNotes,
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
	budget := &byteBudget{max: maxScheduleBytes, label: "schedule-source"}
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
		if len(result) == maxSchedules {
			return fmt.Errorf("%s exceeds the agent limit of at most %d schedules", relative, maxSchedules)
		}
		if len([]rune(name)) > 128 {
			return fmt.Errorf("schedule name %q exceeds 128 characters", name)
		}
		source, err := rootfs.ReadSource(root, relative, maxSourceBytes)
		if err != nil {
			return fmt.Errorf("schedule %q: %w", name, err)
		}
		if err := budget.claim(relative, len(source)); err != nil {
			return err
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

func loadConnections(root string) ([]Connection, error) {
	directory := filepath.Join(root, "connections")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Connection{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("connections must be a real directory")
	}
	entries, err := readDirectory(directory, maxConnections)
	if err != nil {
		return nil, fmt.Errorf("connections: %w", err)
	}
	connections := make([]Connection, 0, len(entries))
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("connections/%s must be a real regular file without symlinks", entry.Name())
		}
		if filepath.Ext(entry.Name()) != ".md" {
			return nil, fmt.Errorf("connections supports Markdown files only; found %q", entry.Name())
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		if !ValidConnectionName(name) {
			return nil, fmt.Errorf("connections/%s: connection name must match ^[a-z][a-z0-9_-]{0,63}$ and must not be reserved", entry.Name())
		}
		path := "connections/" + entry.Name()
		source, err := rootfs.ReadSource(root, path, maxConnectionBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		connection, err := parseConnection(name, path, source)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		connections = append(connections, connection)
	}
	return connections, nil
}

// LoadConnections validates an exact agent root and returns its standalone MCP
// inventory without selecting a harness or workspace.
func LoadConnections(root string) (string, []Connection, error) {
	canonical, err := AgentRoot(root)
	if err != nil {
		return "", nil, err
	}
	connections, err := loadConnections(canonical)
	if err != nil {
		return "", nil, err
	}
	return canonical, connections, nil
}

// AgentRoot proves that root is the exact selected agent directory. It never
// searches ancestors, infers an agents directory, or selects a harness.
func AgentRoot(root string) (string, error) {
	canonical, err := rootfs.CanonicalDir(root)
	if err != nil {
		return "", err
	}
	instructions, err := rootfs.ReadSource(canonical, "instructions.md", maxSourceBytes)
	if err != nil {
		return "", fmt.Errorf("instructions: %w", err)
	}
	if _, _, _, err := parseInstructions(instructions); err != nil {
		return "", fmt.Errorf("instructions: %w", err)
	}
	return canonical, nil
}

// LoadConnection reads one exact authored source without requiring other
// connection files to be healthy.
func LoadConnection(root, name string) (string, Connection, error) {
	canonical, err := AgentRoot(root)
	if err != nil {
		return "", Connection{}, err
	}
	if !ValidConnectionName(name) {
		return "", Connection{}, errors.New("connection name must match ^[a-z][a-z0-9_-]{0,63}$ and must not be reserved")
	}
	path := "connections/" + name + ".md"
	source, err := rootfs.ReadSource(canonical, path, maxConnectionBytes)
	if err != nil {
		return "", Connection{}, fmt.Errorf("%s: %w", path, err)
	}
	connection, err := parseConnection(name, path, source)
	if err != nil {
		return "", Connection{}, fmt.Errorf("%s: %w", path, err)
	}
	return canonical, connection, nil
}

func NewInstalledConnection(name, packageID, capabilityID, context string) (Connection, error) {
	if !ValidConnectionName(name) {
		return Connection{}, errors.New("connection name must match ^[a-z][a-z0-9_-]{0,63}$ and must not be reserved")
	}
	source := "---\ntype: mcp\npackage: " + packageID + "\ncapability: " + capabilityID + "\n---\n"
	if context = strings.TrimSpace(context); context != "" {
		source += "\n" + context + "\n"
	}
	return parseConnection(name, "connections/"+name+".md", []byte(source))
}

func NewRemoteConnection(name, endpoint, context string) (Connection, error) {
	if !ValidConnectionName(name) {
		return Connection{}, errors.New("connection name must match ^[a-z][a-z0-9_-]{0,63}$ and must not be reserved")
	}
	source := "---\ntype: mcp\ntransport: streamable-http\nurl: " + endpoint + "\n---\n"
	if context = strings.TrimSpace(context); context != "" {
		source += "\n" + context + "\n"
	}
	return parseConnection(name, "connections/"+name+".md", []byte(source))
}

// ValidateConnectionNameAvailable rejects collisions with existing standalone
// sources and accepted Plugin MCP declarations without selecting a harness.
func ValidateConnectionNameAvailable(root, name string) error {
	if !ValidConnectionName(name) {
		return errors.New("connection name must match ^[a-z][a-z0-9_-]{0,63}$ and must not be reserved")
	}
	connections, err := loadConnections(root)
	if err != nil {
		return err
	}
	for _, connection := range connections {
		if connection.Name == name {
			return fmt.Errorf("%s already exists", connection.Path)
		}
	}
	names, err := acceptedPluginMCPNames(root)
	if err != nil {
		return err
	}
	if names[name] {
		return fmt.Errorf("connection name %q collides with an authored plugin server", name)
	}
	return nil
}

func acceptedPluginMCPNames(root string) (map[string]bool, error) {
	result := map[string]bool{"managed": true}
	directory := filepath.Join(root, "plugins")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("plugins must be a real directory")
	}
	entries, err := readDirectory(directory, maxPlugins)
	if err != nil {
		return nil, fmt.Errorf("plugins: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.IsDir() || entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("plugins may contain real plugin directories only; found %q", entry.Name())
		}
		pluginPath := "plugins/" + entry.Name()
		manifest, err := rootfs.ReadSource(root, pluginPath+"/plugin.json", maxSourceBytes)
		if err != nil {
			continue
		}
		if _, err := validatePluginManifest(pluginPath+"/plugin.json", manifest); err != nil {
			continue
		}
		servers, _, _ := loadPluginMCP(root, pluginPath)
		for _, server := range servers {
			if !result[server.Name] {
				result[server.Name] = true
			}
		}
	}
	return result, nil
}

func ValidConnectionName(name string) bool {
	return name != "managed" && connectionName.MatchString(name)
}

func parseConnection(name, path string, source []byte) (Connection, error) {
	if len(source) > maxConnectionBytes {
		return Connection{}, fmt.Errorf("connection file must contain at most %d bytes", maxConnectionBytes)
	}
	if !utf8.Valid(source) {
		return Connection{}, errors.New("connection file must be valid UTF-8")
	}
	lines := bytes.Split(source, []byte("\n"))
	if len(lines) == 0 || string(bytes.TrimSuffix(lines[0], []byte("\r"))) != "---" {
		return Connection{}, errors.New("connection must start with YAML frontmatter declaring \"type: mcp\" and one supported target; body-only connection files are no longer supported")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		line := string(bytes.TrimSuffix(lines[index], []byte("\r")))
		if line == "..." {
			return Connection{}, errors.New("connection frontmatter must contain one YAML document")
		}
		if line == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return Connection{}, errors.New("connection frontmatter is not closed")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(bytes.Join(lines[1:closing], []byte("\n"))))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return Connection{}, errors.New("connection frontmatter must be valid YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Connection{}, errors.New("connection frontmatter must contain one YAML document")
	}
	if err := validateYAMLTree(&document); err != nil {
		return Connection{}, fmt.Errorf("connection frontmatter: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode || document.Content[0].Tag != "!!map" {
		return Connection{}, errors.New("connection frontmatter must be a plain YAML mapping")
	}
	fields := map[string]string{}
	root := document.Content[0]
	for index := 0; index < len(root.Content); index += 2 {
		key := root.Content[index].Value
		value, err := yamlString(root.Content[index+1], key)
		if err != nil {
			return Connection{}, err
		}
		fields[key] = value
	}
	if fields["type"] != "mcp" {
		return Connection{}, errors.New("connection frontmatter field \"type\" must equal \"mcp\"")
	}
	connection := Connection{Name: name, Path: path, Source: source}
	switch {
	case len(fields) == 3 && fields["package"] != "" && fields["capability"] != "":
		if integration.ValidatePackageID(fields["package"]) != nil {
			return Connection{}, errors.New("connection frontmatter field \"package\" is invalid")
		}
		if integration.ValidateCapabilityID(fields["capability"]) != nil {
			return Connection{}, errors.New("connection frontmatter field \"capability\" is invalid")
		}
		for key := range fields {
			if key != "type" && key != "package" && key != "capability" {
				return Connection{}, fmt.Errorf("connection frontmatter field %q is not supported", key)
			}
		}
		connection.Package, connection.Capability = fields["package"], fields["capability"]
	case len(fields) == 3 && fields["transport"] == "streamable-http" && fields["url"] != "":
		for key := range fields {
			if key != "type" && key != "transport" && key != "url" {
				return Connection{}, fmt.Errorf("connection frontmatter field %q is not supported", key)
			}
		}
		if err := validateConnectionURL(fields["url"]); err != nil {
			return Connection{}, err
		}
		connection.Transport, connection.URL = fields["transport"], fields["url"]
	default:
		return Connection{}, errors.New("connection frontmatter must contain exactly type plus package/capability or transport/url")
	}
	body := strings.TrimSpace(string(bytes.Join(lines[closing+1:], []byte("\n"))))
	if len([]rune(body)) > 1024 {
		return Connection{}, errors.New("connection Markdown context must contain at most 1024 characters")
	}
	connection.Context = body
	return connection, nil
}

func validateConnectionURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("connection frontmatter field \"url\" must be an absolute HTTPS URL with a host and no user information, query, or fragment")
	}
	return nil
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
	budget := &byteBudget{max: maxHarnessBytes, label: "harness-specific source"}
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
		if len(files) == maxHarnessFiles {
			return fmt.Errorf("harness-specific file %q exceeds the limit of at most %d files", sourcePath, maxHarnessFiles)
		}
		content, err := rootfs.ReadSource(root, sourcePath, maxHarnessFileBytes)
		if err != nil {
			return err
		}
		if err := budget.claim(sourcePath, len(content)); err != nil {
			return err
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

func parseInstructions(content []byte) (string, bool, []byte, error) {
	if !utf8.Valid(content) {
		return "", false, nil, errors.New("file must be valid UTF-8")
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	if !scanner.Scan() || strings.TrimSuffix(scanner.Text(), "\r") != "---" {
		return "", false, nil, errors.New("file must start with YAML frontmatter")
	}
	description := ""
	frictionNotes := false
	frictionNotesSeen := false
	closed := false
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return "", false, nil, errors.New("frontmatter supports description and optional friction-notes only")
		}
		switch strings.TrimSpace(key) {
		case "description":
			if description != "" {
				return "", false, nil, errors.New("frontmatter description is duplicated")
			}
			description = strings.TrimSpace(value)
			if description == "" || len(description) > 1024 {
				return "", false, nil, errors.New("frontmatter description must be non-empty and bounded")
			}
		case "friction-notes":
			if frictionNotesSeen {
				return "", false, nil, errors.New("frontmatter friction-notes is duplicated")
			}
			frictionNotesSeen = true
			switch strings.TrimSpace(value) {
			case "true":
				frictionNotes = true
			case "false":
				frictionNotes = false
			default:
				return "", false, nil, errors.New("frontmatter friction-notes must be true or false")
			}
		default:
			return "", false, nil, errors.New("frontmatter supports description and optional friction-notes only")
		}
	}
	if !closed || description == "" {
		return "", false, nil, errors.New("frontmatter requires one plain description")
	}
	var body []string
	for scanner.Scan() {
		body = append(body, strings.TrimSuffix(scanner.Text(), "\r"))
	}
	if err := scanner.Err(); err != nil {
		return "", false, nil, errors.New("cannot read instructions")
	}
	trimmed := strings.TrimSpace(strings.Join(body, "\n"))
	if trimmed == "" {
		return "", false, nil, errors.New("markdown body must be non-empty")
	}
	return description, frictionNotes, []byte(trimmed + "\n"), nil
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
	budget := &byteBudget{max: maxSubagentBytes, label: "subagent-source"}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if len(result) == maxSubagents {
			return nil, fmt.Errorf("subagents/%s exceeds the agent limit of at most %d subagents", entry.Name(), maxSubagents)
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
		if err := budget.claim(path, len(source)); err != nil {
			return nil, err
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

func loadSkills(root string, budget *skillSetBudget) ([]Skill, error) {
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
		sourcePath := "skills/" + entry.Name()
		if strings.HasSuffix(entry.Name(), ".md") {
			name := strings.TrimSuffix(entry.Name(), ".md")
			return nil, fmt.Errorf("skill %q uses the removed flat layout; move it to %q", "skills/"+entry.Name(), "skills/"+name+"/SKILL.md")
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.IsDir() || entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("skills may contain real skill directories only; found %q", entry.Name())
		}
		if len(skills) == maxSkills {
			return nil, fmt.Errorf("skills/%s exceeds the agent limit of at most %d skills", entry.Name(), maxSkills)
		}
		name := entry.Name()
		if !validSkillName(name) {
			return nil, fmt.Errorf("skill directory %q must be 1-64 lowercase ASCII letters, numbers, and single hyphens", name)
		}
		skill, err := loadSkill(root, sourcePath, name, budget)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", name, err)
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

func loadSkill(root, sourcePath, name string, budget *skillSetBudget) (Skill, error) {
	files, err := loadSkillFiles(root, sourcePath, budget)
	if err != nil {
		return Skill{}, err
	}
	frontmatter, err := parseSkill(files[0].Content)
	if err != nil {
		return Skill{}, fmt.Errorf("SKILL.md: %w", err)
	}
	if frontmatter.Name != name {
		return Skill{}, errors.New("name must match its parent directory")
	}
	return Skill{
		Name:                name,
		Description:         frontmatter.Description,
		License:             frontmatter.License,
		Compatibility:       frontmatter.Compatibility,
		Metadata:            frontmatter.Metadata,
		AllowedTools:        frontmatter.AllowedTools,
		AllowedToolsPresent: frontmatter.AllowedToolsPresent,
		ClaudeFields:        frontmatter.ClaudeFields,
		Files:               files,
		SourcePath:          sourcePath,
	}, nil
}

func loadSkillFiles(root, sourcePath string, budget *skillSetBudget) ([]File, error) {
	directory := filepath.Join(root, filepath.FromSlash(sourcePath))
	files := []File{}
	var total int64
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
			return fmt.Errorf("%s exceeds the per-skill limit of at most %d files", sourcePath, maxSkillFiles)
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
		if info.Size() > limit {
			return fmt.Errorf("%s exceeds %d bytes", sourcePath, limit)
		}
		if total+info.Size() > maxSkillBytes {
			return fmt.Errorf("%s exceeds the per-skill aggregate of %d bytes", sourcePath, maxSkillBytes)
		}
		if err := budget.claim(sourcePath, info.Size()); err != nil {
			return err
		}
		content, err := rootfs.ReadSource(root, sourcePath, limit)
		if err != nil {
			return err
		}
		total += int64(len(content))
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

func loadPlugins(root, workspace, harness, agentID string, skills []Skill, budget *skillSetBudget) ([]Skill, []PluginMCPServer, []SourceRecord, []Diagnostic, error) {
	directory := filepath.Join(root, "plugins")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return skills, nil, nil, nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, nil, nil, errors.New("plugins must be a real directory")
	}
	entries, err := readDirectory(directory, maxPlugins)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("plugins: %w", err)
	}
	seen := make(map[string]bool, len(skills))
	for _, skill := range skills {
		seen[skill.Name] = true
	}
	sources := []SourceRecord{}
	diagnostics := []Diagnostic{}
	mcpServers := []PluginMCPServer{}
	seenMCP := map[string]bool{"managed": true}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		pluginPath := "plugins/" + entry.Name()
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.IsDir() || entryInfo.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, nil, fmt.Errorf("plugins may contain real plugin directories only; found %q", entry.Name())
		}
		manifestPath := pluginPath + "/plugin.json"
		manifest, err := rootfs.ReadSource(root, manifestPath, maxSourceBytes)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: manifestPath, Message: "plugin rejected: " + err.Error()})
			continue
		}
		manifestDiagnostics, err := validatePluginManifest(manifestPath, manifest)
		diagnostics = append(diagnostics, manifestDiagnostics...)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: manifestPath, Message: "plugin rejected: " + err.Error()})
			continue
		}
		sources = append(sources, SourceRecord{Path: manifestPath, SHA256: rootfs.SHA256(manifest)})
		pluginServers, mcpDiagnostics, mcpSource := loadPluginMCP(root, pluginPath)
		diagnostics = append(diagnostics, mcpDiagnostics...)
		for _, server := range pluginServers {
			server.DataPath = pluginDataPath(agentID, server.PluginPath)
			if harness == "claude" && !claudeMCPServerExpansionSafe(root, workspace, server) {
				diagnostics = append(diagnostics, Diagnostic{Path: server.SourcePath, Field: "mcpServers." + server.Name, Message: "plugin MCP server skipped: Claude project configuration would expand unsupported placeholder-like text"})
				continue
			}
			if seenMCP[server.Name] {
				diagnostics = append(diagnostics, Diagnostic{Path: server.SourcePath, Field: "mcpServers." + server.Name, Message: fmt.Sprintf("plugin MCP server skipped: server name %q is already provided by an earlier source", server.Name)})
				continue
			}
			if len(mcpServers) == maxPluginMCPServers {
				diagnostics = append(diagnostics, Diagnostic{Path: server.SourcePath, Field: "mcpServers." + server.Name, Message: fmt.Sprintf("plugin MCP server skipped: agent may contain at most %d plugin MCP servers", maxPluginMCPServers)})
				continue
			}
			seenMCP[server.Name] = true
			mcpServers = append(mcpServers, server)
			sources = append(sources, server.sources...)
		}
		if mcpSource != nil {
			sources = append(sources, *mcpSource)
		}

		skillRoot := filepath.Join(directory, entry.Name(), "skills")
		skillRootInfo, err := os.Lstat(skillRoot)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !skillRootInfo.IsDir() || skillRootInfo.Mode()&os.ModeSymlink != 0 {
			diagnostics = append(diagnostics, Diagnostic{Path: pluginPath + "/skills", Message: "plugin skills skipped: must be a real directory"})
			continue
		}
		skillEntries, err := readDirectory(skillRoot, maxPluginSkills)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: pluginPath + "/skills", Message: "plugin skills skipped: " + err.Error()})
			continue
		}
		for _, skillEntry := range skillEntries {
			if strings.HasPrefix(skillEntry.Name(), ".") {
				continue
			}
			sourcePath := pluginPath + "/skills/" + skillEntry.Name()
			skillInfo, err := skillEntry.Info()
			if err != nil || !skillInfo.IsDir() || skillInfo.Mode()&os.ModeSymlink != 0 {
				diagnostics = append(diagnostics, Diagnostic{Path: sourcePath, Message: "plugin skill skipped: must be a real directory"})
				continue
			}
			name := skillEntry.Name()
			if !validSkillName(name) {
				diagnostics = append(diagnostics, Diagnostic{Path: sourcePath, Message: "plugin skill skipped: directory name must be 1-64 lowercase ASCII letters, numbers, and single hyphens"})
				continue
			}
			if seen[name] {
				diagnostics = append(diagnostics, Diagnostic{Path: sourcePath, Message: fmt.Sprintf("plugin skill skipped: skill name %q is already provided by an earlier source", name)})
				continue
			}
			if len(skills) == maxSkills {
				diagnostics = append(diagnostics, Diagnostic{Path: sourcePath, Message: fmt.Sprintf("plugin skill skipped: agent may contain at most %d skills", maxSkills)})
				continue
			}
			budgetBefore := *budget
			skill, err := loadSkill(root, sourcePath, name, budget)
			if err != nil {
				*budget = budgetBefore
				diagnostics = append(diagnostics, Diagnostic{Path: sourcePath, Message: "plugin skill skipped: " + err.Error()})
				continue
			}
			seen[name] = true
			skills = append(skills, skill)
		}
	}
	return skills, mcpServers, sources, diagnostics, nil
}

func loadPluginMCP(root, pluginPath string) ([]PluginMCPServer, []Diagnostic, *SourceRecord) {
	path := pluginRelativePath(pluginPath, "mcp.json")
	abs := filepath.Join(root, filepath.FromSlash(path))
	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, []Diagnostic{{Path: path, Message: "plugin MCP component skipped: must be a bounded regular file without symlinks"}}, nil
	}
	content, err := rootfs.ReadSource(root, path, maxSourceBytes)
	if err != nil {
		return nil, []Diagnostic{{Path: path, Message: "plugin MCP component skipped: " + err.Error()}}, nil
	}
	fields, err := decodeJSONObject(content)
	if err != nil {
		return nil, []Diagnostic{{Path: path, Message: "plugin MCP component skipped: " + err.Error()}}, nil
	}
	if len(fields) != 2 || fields["$schema"] == nil || fields["mcpServers"] == nil {
		return nil, []Diagnostic{{Path: path, Message: "plugin MCP component skipped: top-level object must contain only $schema and mcpServers"}}, nil
	}
	var schema string
	if json.Unmarshal(fields["$schema"], &schema) != nil || schema != pluginMCPSchema {
		return nil, []Diagnostic{{Path: path, Field: "$schema", Message: fmt.Sprintf("plugin MCP component skipped: must equal %q", pluginMCPSchema)}}, nil
	}
	var rawServers map[string]json.RawMessage
	if json.Unmarshal(fields["mcpServers"], &rawServers) != nil || rawServers == nil {
		return nil, []Diagnostic{{Path: path, Field: "mcpServers", Message: "plugin MCP component skipped: must be an object"}}, nil
	}
	if len(rawServers) > maxPluginMCPServers {
		return nil, []Diagnostic{{Path: path, Field: "mcpServers", Message: fmt.Sprintf("plugin MCP component skipped: may contain at most %d servers", maxPluginMCPServers)}}, nil
	}
	names := make([]string, 0, len(rawServers))
	for name := range rawServers {
		names = append(names, name)
	}
	sort.Strings(names)
	servers := []PluginMCPServer{}
	diagnostics := []Diagnostic{}
	for _, name := range names {
		server, commandSource, err := validatePluginMCPServer(root, pluginPath, path, name, rawServers[name])
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Field: "mcpServers." + name, Message: "plugin MCP server skipped: " + err.Error()})
			continue
		}
		if server.Type == "sse" {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Field: "mcpServers." + name, Message: "plugin MCP server skipped: SSE transport is not supported"})
			continue
		}
		if commandSource != nil {
			server.sources = append(server.sources, *commandSource)
		}
		servers = append(servers, server)
	}
	return servers, diagnostics, &SourceRecord{Path: path, SHA256: rootfs.SHA256(content)}
}

func setPluginDataPaths(servers []PluginMCPServer, agentID string) {
	for index := range servers {
		servers[index].DataPath = pluginDataPath(agentID, servers[index].PluginPath)
	}
}

func pluginDataPath(agentID, pluginPath string) string {
	identity := rootfs.SHA256([]byte(agentID + "\x00" + pluginPath))[:16]
	return ".hctl/plugin-data/" + identity
}

func claudeMCPServerExpansionSafe(root, workspace string, server PluginMCPServer) bool {
	values := []string{}
	if server.Type == "streamable-http" {
		values = append(values, server.URL)
		for _, value := range server.Headers {
			values = append(values, value)
		}
	} else {
		pluginRoot := filepath.Join(root, filepath.FromSlash(server.PluginPath))
		pluginData := filepath.Join(workspace, filepath.FromSlash(server.DataPath))
		expand := strings.NewReplacer("${PLUGIN_ROOT}", pluginRoot, "${PLUGIN_DATA}", pluginData)
		command := server.Command
		if strings.HasPrefix(command, "./") {
			command = filepath.Join(pluginRoot, filepath.FromSlash(strings.TrimPrefix(command, "./")))
		}
		values = append(values, pluginRoot, pluginData, command, expand.Replace(server.CWD))
		for _, value := range server.Args {
			values = append(values, expand.Replace(value))
		}
		for _, value := range server.Env {
			values = append(values, expand.Replace(value))
		}
	}
	for _, value := range values {
		if strings.Contains(value, "${") {
			return false
		}
	}
	return true
}

func validatePluginMCPServer(root, pluginPath, sourcePath, name string, raw json.RawMessage) (PluginMCPServer, *SourceRecord, error) {
	fields, err := decodeJSONObject(raw)
	if err != nil {
		return PluginMCPServer{}, nil, err
	}
	var transport string
	if value, ok := fields["type"]; !ok || json.Unmarshal(value, &transport) != nil {
		return PluginMCPServer{}, nil, errors.New("field \"type\" is required and must be a string")
	}
	server := PluginMCPServer{Name: name, Type: transport, PluginPath: pluginPath, SourcePath: sourcePath}
	switch transport {
	case "stdio":
		if err := requireOnlyFields(fields, "type", "command", "args", "env", "cwd"); err != nil {
			return PluginMCPServer{}, nil, err
		}
		if value, ok := fields["command"]; !ok || json.Unmarshal(value, &server.Command) != nil || server.Command == "" {
			return PluginMCPServer{}, nil, errors.New("field \"command\" is required and must be a non-empty string")
		}
		var commandSource *SourceRecord
		if strings.HasPrefix(server.Command, "./") {
			relative, err := rootfs.CleanRelative(strings.TrimPrefix(server.Command, "./"))
			if err != nil {
				return PluginMCPServer{}, nil, errors.New("field \"command\" must remain inside the plugin directory")
			}
			commandPath := pluginRelativePath(pluginPath, relative)
			content, err := rootfs.ReadSource(root, commandPath, maxPluginCommandBytes)
			if err != nil {
				return PluginMCPServer{}, nil, fmt.Errorf("field \"command\": %w", err)
			}
			info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(commandPath)))
			if err != nil {
				return PluginMCPServer{}, nil, errors.New("cannot inspect plugin-relative command")
			}
			commandSource = &SourceRecord{Path: commandPath, SHA256: rootfs.SHA256(content), Executable: info.Mode().Perm()&0o111 != 0}
		} else if strings.ContainsAny(server.Command, "/\\ \t\r\n") {
			return PluginMCPServer{}, nil, errors.New("field \"command\" must be a bare executable name or start with ./")
		}
		if value, ok := fields["args"]; ok {
			if json.Unmarshal(value, &server.Args) != nil || server.Args == nil {
				return PluginMCPServer{}, nil, errors.New("field \"args\" must be an array of strings")
			}
		}
		if value, ok := fields["env"]; ok {
			if err := json.Unmarshal(value, &server.Env); err != nil || server.Env == nil {
				return PluginMCPServer{}, nil, errors.New("field \"env\" must be an object of string values")
			}
			if _, ok := server.Env["PLUGIN_ROOT"]; ok {
				return PluginMCPServer{}, nil, errors.New("field \"env\" must not configure PLUGIN_ROOT")
			}
			if _, ok := server.Env["PLUGIN_DATA"]; ok {
				return PluginMCPServer{}, nil, errors.New("field \"env\" must not configure PLUGIN_DATA")
			}
		}
		if value, ok := fields["cwd"]; ok {
			if json.Unmarshal(value, &server.CWD) != nil || server.CWD == "" {
				return PluginMCPServer{}, nil, errors.New("field \"cwd\" must be a non-empty string")
			}
			if err := validatePluginCWD(root, pluginPath, server.CWD); err != nil {
				return PluginMCPServer{}, nil, fmt.Errorf("field \"cwd\": %w", err)
			}
		}
		return server, commandSource, nil
	case "streamable-http", "sse":
		if err := requireOnlyFields(fields, "type", "url", "headers"); err != nil {
			return PluginMCPServer{}, nil, err
		}
		if value, ok := fields["url"]; !ok || json.Unmarshal(value, &server.URL) != nil || server.URL == "" {
			return PluginMCPServer{}, nil, errors.New("field \"url\" is required and must be a non-empty string")
		}
		if err := validatePluginMCPURL(server.URL); err != nil {
			return PluginMCPServer{}, nil, fmt.Errorf("field \"url\": %w", err)
		}
		if value, ok := fields["headers"]; ok {
			server.Headers, err = decodePluginHeaders(value)
			if err != nil {
				return PluginMCPServer{}, nil, fmt.Errorf("field \"headers\": %w", err)
			}
			for key, value := range server.Headers {
				if !validHTTPHeaderName(key) || !validHTTPHeaderValue(value) {
					return PluginMCPServer{}, nil, fmt.Errorf("field \"headers\" contains invalid HTTP header %q", key)
				}
			}
		}
		return server, nil, nil
	default:
		return PluginMCPServer{}, nil, fmt.Errorf("unsupported transport %q", transport)
	}
}

func pluginRelativePath(pluginPath, relative string) string {
	if pluginPath == "" {
		return relative
	}
	return pluginPath + "/" + relative
}

func decodePluginHeaders(raw json.RawMessage) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("must be an object of string values")
	}
	result := map[string]string{}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("must be an object of string values")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("must be an object of string values")
		}
		folded := strings.ToLower(key)
		if seen[folded] {
			return nil, fmt.Errorf("contains duplicate name %q ignoring case", key)
		}
		seen[folded] = true
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("must be an object of string values")
		}
		result[key] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, errors.New("must be an object of string values")
	}
	return result, nil
}

func decodeJSONObject(content []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, errors.New("must be one JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain one JSON value")
	}
	return fields, nil
}

func requireOnlyFields(fields map[string]json.RawMessage, allowed ...string) error {
	want := map[string]bool{}
	for _, field := range allowed {
		want[field] = true
	}
	for field := range fields {
		if !want[field] {
			return fmt.Errorf("unsupported field %q", field)
		}
	}
	return nil
}

func validatePluginCWD(root, pluginPath, value string) error {
	if value != "./" && strings.HasSuffix(value, "/") {
		return errors.New("must be normalized and remain inside its selected root")
	}
	data := false
	relative := ""
	switch {
	case strings.HasPrefix(value, "./"):
		relative = strings.TrimPrefix(value, "./")
	case value == "${PLUGIN_ROOT}":
	case strings.HasPrefix(value, "${PLUGIN_ROOT}/"):
		relative = strings.TrimPrefix(value, "${PLUGIN_ROOT}/")
	case value == "${PLUGIN_DATA}":
		data = true
	case strings.HasPrefix(value, "${PLUGIN_DATA}/"):
		data = true
		relative = strings.TrimPrefix(value, "${PLUGIN_DATA}/")
	default:
		return errors.New("must start with ./, ${PLUGIN_ROOT}, or ${PLUGIN_DATA}")
	}
	if relative != "" {
		cleaned, err := rootfs.CleanRelative(relative)
		if err != nil || cleaned != relative {
			return errors.New("must be normalized and remain inside its selected root")
		}
	}
	if data {
		return nil
	}
	directory := pluginPath
	if relative != "" {
		directory += "/" + relative
	}
	current := root
	for _, part := range strings.Split(directory, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("must resolve to a real directory without symlinks")
		}
	}
	return nil
}

func validatePluginMCPURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("must be an absolute HTTP(S) URL without user information or a fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || scheme != "http" && scheme != "https" {
		return errors.New("must be an absolute HTTP(S) URL without user information or a fragment")
	}
	if scheme == "http" {
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return errors.New("must use HTTPS unless the host is localhost or a loopback IP literal")
		}
	}
	return nil
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		allowed := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character))
		if !allowed {
			return false
		}
	}
	return true
}

func validHTTPHeaderValue(value string) bool {
	for _, character := range []byte(value) {
		if character == '\r' || character == '\n' || character == 0x7f || character < 0x20 && character != '\t' {
			return false
		}
	}
	return true
}

func validatePluginManifest(path string, content []byte) ([]Diagnostic, error) {
	if !utf8.Valid(content) {
		return nil, errors.New("must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, errors.New("must be one JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain one JSON value")
	}
	allowed := map[string]bool{"$schema": true, "name": true, "version": true, "description": true, "author": true, "homepage": true, "repository": true, "license": true, "keywords": true, "extensions": true}
	diagnostics := []Diagnostic{}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowed[key] {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Field: key, Message: "unsupported manifest field ignored"})
		}
	}
	var schema string
	if raw, ok := fields["$schema"]; !ok || json.Unmarshal(raw, &schema) != nil || schema != pluginSchema {
		return diagnostics, fmt.Errorf("field %q must equal %q", "$schema", pluginSchema)
	}
	var name string
	if raw, ok := fields["name"]; !ok || json.Unmarshal(raw, &name) != nil || len(name) > 64 || !pluginName.MatchString(name) || strings.Contains(name, "--") || strings.Contains(name, "..") {
		return diagnostics, errors.New("field \"name\" must match the Agent Plugins v1 name format")
	}
	for _, key := range []string{"version", "description", "homepage", "repository", "license"} {
		if raw, ok := fields[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return diagnostics, fmt.Errorf("field %q must be a string", key)
			}
		}
	}
	if raw, ok := fields["keywords"]; ok {
		var value []string
		if json.Unmarshal(raw, &value) != nil || value == nil {
			return diagnostics, errors.New("field \"keywords\" must be an array of strings")
		}
	}
	if raw, ok := fields["author"]; ok {
		if err := validatePluginObject(raw, map[string]bool{"name": true, "email": true, "url": true}); err != nil {
			return diagnostics, fmt.Errorf("field \"author\": %w", err)
		}
	}
	if raw, ok := fields["extensions"]; ok {
		var extensions map[string]json.RawMessage
		if json.Unmarshal(raw, &extensions) != nil || extensions == nil {
			diagnostics = append(diagnostics, Diagnostic{Path: path, Field: "extensions", Message: "non-object extensions value ignored"})
		} else {
			namespaces := make([]string, 0, len(extensions))
			for namespace := range extensions {
				namespaces = append(namespaces, namespace)
			}
			sort.Strings(namespaces)
			for _, namespace := range namespaces {
				diagnostics = append(diagnostics, Diagnostic{Path: path, Field: "extensions." + namespace, Message: "unsupported extension namespace ignored"})
			}
		}
	}
	return diagnostics, nil
}

func validatePluginObject(raw json.RawMessage, allowed map[string]bool) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("must be an object")
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := fields[key]
		if !allowed[key] {
			return fmt.Errorf("unsupported field %q", key)
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			return fmt.Errorf("field %q must be a string", key)
		}
	}
	return nil
}

func readDirectory(path string, maxEntries int) ([]os.DirEntry, error) {
	directory, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read directory")
	}
	entries, readErr := directory.ReadDir(maxEntries + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return nil, errors.New("cannot read directory")
	}
	if len(entries) > maxEntries {
		return nil, fmt.Errorf("may contain at most %d entries", maxEntries)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
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
