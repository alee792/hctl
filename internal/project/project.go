package project

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"hctl/internal/rootfs"
)

const (
	GeneratorVersion = "hctl/0.1.0-dev"
	ManifestVersion  = 1
	maxConfigBytes   = 64 << 10
	maxSourceBytes   = 128 << 10
)

var portableName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Config struct {
	SchemaVersion     int               `json:"schema_version"`
	Name              string            `json:"name"`
	Instructions      string            `json:"instructions"`
	Skills            []string          `json:"skills"`
	ManagedCapability ManagedCapability `json:"managed_capability"`
}

type ManagedCapability struct {
	Name          string `json:"name"`
	MaxInputBytes int    `json:"max_input_bytes"`
}

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
	Root         string
	Config       Config
	Instructions []byte
	Skills       []Skill
	Manifest     Manifest
}

func Load(root, harness string) (*Project, error) {
	if harness != "claude" && harness != "codex" {
		return nil, errors.New("harness must be claude or codex")
	}
	root, err := rootfs.CanonicalDir(root)
	if err != nil {
		return nil, err
	}
	configBytes, err := rootfs.ReadSource(root, "agent.json", maxConfigBytes)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(configBytes) {
		return nil, errors.New("agent.json must be valid UTF-8")
	}
	var cfg Config
	if err := decodeStrict(configBytes, &cfg); err != nil {
		return nil, fmt.Errorf("agent.json is invalid: %w", err)
	}
	if cfg.SchemaVersion != 1 {
		return nil, errors.New("agent.json schema_version must be 1")
	}
	if !portableName.MatchString(cfg.Name) {
		return nil, errors.New("agent name must match [a-z][a-z0-9-]{0,62}")
	}
	if cfg.ManagedCapability.Name != "echo" {
		return nil, errors.New("MVP managed_capability.name must be echo")
	}
	if cfg.ManagedCapability.MaxInputBytes < 1 || cfg.ManagedCapability.MaxInputBytes > 4096 {
		return nil, errors.New("managed_capability.max_input_bytes must be between 1 and 4096")
	}
	if len(cfg.Skills) == 0 || len(cfg.Skills) > 8 {
		return nil, errors.New("agent must declare between 1 and 8 portable skills")
	}

	instructionPath, err := rootfs.CleanRelative(cfg.Instructions)
	if err != nil {
		return nil, fmt.Errorf("instructions: %w", err)
	}
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

	skills := make([]Skill, 0, len(cfg.Skills))
	seenNames := map[string]bool{}
	seenPaths := map[string]bool{}
	for _, path := range cfg.Skills {
		path, err = rootfs.CleanRelative(path)
		if err != nil {
			return nil, fmt.Errorf("skill path: %w", err)
		}
		if seenPaths[path] {
			return nil, fmt.Errorf("duplicate skill path %q", path)
		}
		seenPaths[path] = true
		content, err := rootfs.ReadSource(root, path, maxSourceBytes)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", path, err)
		}
		if !utf8.Valid(content) {
			return nil, fmt.Errorf("skill %q must be valid UTF-8", path)
		}
		name, description, err := parseSkill(content)
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", path, err)
		}
		if seenNames[name] {
			return nil, fmt.Errorf("duplicate skill name %q", name)
		}
		seenNames[name] = true
		skills = append(skills, Skill{Name: name, Description: description, Path: path, Content: content})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })

	sources := []SourceRecord{{Path: "agent.json", SHA256: rootfs.SHA256(configBytes)}, {Path: instructionPath, SHA256: rootfs.SHA256(instructions)}}
	for _, skill := range skills {
		sources = append(sources, SourceRecord{Path: skill.Path, SHA256: rootfs.SHA256(skill.Content)})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Path < sources[j].Path })
	canonicalSources, _ := json.Marshal(sources)
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
		Root:         root,
		Config:       cfg,
		Instructions: instructions,
		Skills:       skills,
		Manifest: Manifest{
			SchemaVersion:     ManifestVersion,
			Generator:         GeneratorVersion,
			Agent:             cfg.Name,
			Harness:           harness,
			SourceFingerprint: fingerprint,
			Sources:           sources,
			Instructions:      SourceRecord{Path: instructionPath, SHA256: rootfs.SHA256(instructions)},
			Skills:            manifestSkills,
			Managed:           []ManifestManaged{{Name: "echo", Transport: "stdio-mcp", Enforcement: "managed", MaxInput: cfg.ManagedCapability.MaxInputBytes}},
			Native:            ManifestNative{Posture: "allowed", Governance: "unmanaged"},
			Driver:            driver,
		},
	}, nil
}

func parseSkill(content []byte) (string, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	if !scanner.Scan() || scanner.Text() != "---" {
		return "", "", errors.New("SKILL.md must start with YAML frontmatter")
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

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values are not allowed")
	}
	return nil
}
