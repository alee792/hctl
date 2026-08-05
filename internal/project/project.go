package project

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"hctl/internal/rootfs"
	"hctl/internal/tool"
)

const (
	GeneratorVersion  = "hctl/0.2.0-dev"
	maxSourceBytes    = 128 << 10
	maxSkills         = 8
	maxSubagents      = 8
	echoMaxInputBytes = 1024
)

var portableName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Skill struct {
	Name        string
	Description string
	Path        string
	Content     []byte
}

type Subagent struct {
	Name         string
	Description  string
	Path         string
	Instructions []byte
	Source       []byte
}

type SourceRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
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
	Tools             tool.Inventory
	SourceFingerprint string
	MaxToolInput      int
}

// Load reads an agent project. If workspace is omitted, the agent source is
// also its workspace.
func Load(source, harness string, workspace ...string) (*Project, error) {
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

	name := nameFromRoot(sourceRoot)
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
		sources = append(sources, SourceRecord{Path: skill.Path, SHA256: rootfs.SHA256(skill.Content)})
	}
	for _, subagent := range subagents {
		sources = append(sources, SourceRecord{Path: subagent.Path, SHA256: rootfs.SHA256(subagent.Source)})
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
		Tools:             tools,
		SourceFingerprint: fingerprint,
		MaxToolInput:      echoMaxInputBytes,
	}, nil
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
		description, instructions, err := parseInstructions(source)
		if err != nil {
			return nil, fmt.Errorf("subagent %q instructions: %w", entry.Name(), err)
		}
		result = append(result, Subagent{Name: entry.Name(), Description: description, Path: path, Instructions: instructions, Source: source})
	}
	return result, nil
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
		if !strings.HasSuffix(entry.Name(), ".md") {
			return nil, fmt.Errorf("skills may contain Markdown files only; found %q", entry.Name())
		}
		if len(skills) == maxSkills {
			return nil, fmt.Errorf("agent may contain at most %d skills", maxSkills)
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		if !portableName.MatchString(name) {
			return nil, fmt.Errorf("skill filename %q must use lowercase letters, numbers, and hyphens", entry.Name())
		}
		path := "skills/" + entry.Name()
		content, err := rootfs.ReadSource(root, path, maxSourceBytes)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", path, err)
		}
		if !utf8.Valid(content) {
			return nil, fmt.Errorf("skill %q must be valid UTF-8", path)
		}
		declaredName, description, err := parseSkill(content)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", path, err)
		}
		if declaredName != name {
			return nil, fmt.Errorf("skill %q name must match its filename", path)
		}
		skills = append(skills, Skill{Name: name, Description: description, Path: path, Content: content})
	}
	return skills, nil
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

func parseSkill(content []byte) (string, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	if !scanner.Scan() || scanner.Text() != "---" {
		return "", "", errors.New("skill file must start with YAML frontmatter")
	}
	fields := map[string]string{}
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || (key != "name" && key != "description") || value == "" || fields[key] != "" {
			return "", "", errors.New("frontmatter supports one plain name and description only")
		}
		fields[key] = value
	}
	if !closed || !portableName.MatchString(fields["name"]) || fields["description"] == "" {
		return "", "", errors.New("frontmatter requires a portable name and non-empty description")
	}
	return fields["name"], fields["description"], nil
}
