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
)

const (
	GeneratorVersion  = "hctl/0.1.0-dev"
	ManifestVersion   = 1
	maxSourceBytes    = 128 << 10
	maxSkills         = 8
	echoMaxInputBytes = 1024
)

var portableName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Skill struct {
	Name        string
	Description string
	Path        string
	Content     []byte
}

type SourceRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type ManifestSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	SHA256      string `json:"sha256"`
}

type ManifestManaged struct {
	Name        string `json:"name"`
	Transport   string `json:"transport"`
	Enforcement string `json:"enforcement"`
	MaxInput    int    `json:"max_input_bytes"`
}

type ManifestNative struct {
	Posture    string `json:"posture"`
	Governance string `json:"governance"`
}

type ManifestDriver struct {
	Executable string `json:"executable"`
	Protocol   string `json:"protocol"`
	Resume     string `json:"resume"`
}

type Manifest struct {
	SchemaVersion     int               `json:"schema_version"`
	Generator         string            `json:"generator"`
	Agent             string            `json:"agent"`
	Harness           string            `json:"harness"`
	SourceFingerprint string            `json:"source_fingerprint"`
	Sources           []SourceRecord    `json:"sources"`
	Instructions      SourceRecord      `json:"instructions"`
	Skills            []ManifestSkill   `json:"skills"`
	Managed           []ManifestManaged `json:"managed_capabilities"`
	Native            ManifestNative    `json:"native_capabilities"`
	Driver            ManifestDriver    `json:"driver"`
}

type Project struct {
	Root            string
	Name            string
	Instructions    []byte
	Skills          []Skill
	MaxManagedInput int
	Manifest        Manifest
}

func Load(root, harness string) (*Project, error) {
	if harness != "claude" && harness != "codex" {
		return nil, errors.New("harness must be claude or codex")
	}
	root, err := rootfs.CanonicalDir(root)
	if err != nil {
		return nil, err
	}
	name := nameFromRoot(root)
	instructionPath := "instructions.md"
	instructions, err := rootfs.ReadSource(root, instructionPath, maxSourceBytes)
	if err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}
	if len(bytes.TrimSpace(instructions)) == 0 {
		return nil, errors.New("instructions file is empty")
	}
	if !utf8.Valid(instructions) {
		return nil, errors.New("instructions file must be valid UTF-8")
	}

	skills, err := loadSkills(root)
	if err != nil {
		return nil, err
	}

	sources := []SourceRecord{{Path: instructionPath, SHA256: rootfs.SHA256(instructions)}}
	for _, skill := range skills {
		sources = append(sources, SourceRecord{Path: skill.Path, SHA256: rootfs.SHA256(skill.Content)})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	canonicalSources, _ := json.Marshal(struct {
		Agent   string         `json:"agent"`
		Sources []SourceRecord `json:"sources"`
	}{Agent: name, Sources: sources})
	fingerprint := rootfs.SHA256(canonicalSources)

	driver := ManifestDriver{Executable: harness}
	if harness == "claude" {
		driver.Executable = "claude"
		driver.Protocol = "stream-json"
		driver.Resume = "--resume"
	} else {
		driver.Executable = "codex"
		driver.Protocol = "app-server-v2-jsonl"
		driver.Resume = "thread/resume"
	}
	manifestSkills := make([]ManifestSkill, 0, len(skills))
	for _, skill := range skills {
		manifestSkills = append(manifestSkills, ManifestSkill{Name: skill.Name, Description: skill.Description, Source: skill.Path, SHA256: rootfs.SHA256(skill.Content)})
	}

	return &Project{
		Root:            root,
		Name:            name,
		Instructions:    instructions,
		Skills:          skills,
		MaxManagedInput: echoMaxInputBytes,
		Manifest: Manifest{
			SchemaVersion:     ManifestVersion,
			Generator:         GeneratorVersion,
			Agent:             name,
			Harness:           harness,
			SourceFingerprint: fingerprint,
			Sources:           sources,
			Instructions:      SourceRecord{Path: instructionPath, SHA256: rootfs.SHA256(instructions)},
			Skills:            manifestSkills,
			Managed:           []ManifestManaged{{Name: "echo", Transport: "stdio-mcp", Enforcement: "managed", MaxInput: echoMaxInputBytes}},
			Native:            ManifestNative{Posture: "allowed", Governance: "unmanaged"},
			Driver:            driver,
		},
	}, nil
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
