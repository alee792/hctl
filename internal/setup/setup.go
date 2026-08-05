package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"hctl/internal/project"
	"hctl/internal/rootfs"
)

const maxMetadataBytes = 8 << 20

var (
	portableName          = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	skillName             = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	omittableClaudeFields = map[string]bool{
		"argument-hint": true,
		"when_to_use":   true,
	}
	omittableOpenAIInterfaceFields = map[string]bool{
		"brand_color":       true,
		"display_name":      true,
		"icon_large":        true,
		"icon_small":        true,
		"short_description": true,
	}
)

type generatedFile struct {
	Content []byte
	Mode    os.FileMode
}

type Diagnostic struct {
	Severity string
	Path     string
	Field    string
	Harness  string
	Message  string
}

func (diagnostic Diagnostic) String() string {
	location := diagnostic.Path
	if diagnostic.Field != "" {
		location += ": field " + strconv.Quote(diagnostic.Field)
	}
	return diagnostic.Severity + ": " + location + ": " + diagnostic.Message + " for " + diagnostic.Harness
}

type Result struct {
	Files       []string
	Diagnostics []Diagnostic
}

type generatedSetup struct {
	Files       map[string]generatedFile
	Diagnostics []Diagnostic
}

type ownedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode,omitempty"`
}

type applyRecord struct {
	SchemaVersion     int         `json:"schema_version"`
	Generator         string      `json:"generator"`
	Harness           string      `json:"harness"`
	AgentID           string      `json:"agent_id,omitempty"`
	Source            string      `json:"source,omitempty"`
	SourceFingerprint string      `json:"source_fingerprint"`
	Files             []ownedFile `json:"files"`
}

func Apply(p *project.Project, executable string) (Result, error) {
	if !filepath.IsAbs(executable) {
		return Result{}, errors.New("managed MCP executable path must be absolute")
	}
	generated, err := filesFor(p, executable)
	if err != nil {
		return Result{}, err
	}
	files := generated.Files
	recordPath := applyRecordPath(p.Harness)
	prior, exists, legacy, err := loadApplyRecord(p.WorkspaceRoot, p.Harness)
	if err != nil {
		return Result{}, err
	}
	priorFiles := map[string]ownedFile{}
	if exists {
		compatible := prior.SchemaVersion == 3 || prior.SchemaVersion == 2 || prior.SchemaVersion == 1 && p.SourceRoot == p.WorkspaceRoot
		if !compatible || prior.Harness != p.Harness {
			return Result{}, errors.New("apply record is incompatible; remove the generated harness files manually")
		}
		for _, owned := range prior.Files {
			if !allowedOwnedPath(p.Harness, owned.Path, legacy) || priorFiles[owned.Path].Path != "" {
				return Result{}, errors.New("apply record contains an invalid path")
			}
			priorFiles[owned.Path] = owned
			actual, mode, present, err := generatedState(p.WorkspaceRoot, owned.Path)
			if err != nil {
				return Result{}, err
			}
			if present && actual != owned.SHA256 {
				return Result{}, fmt.Errorf("generated file %s was changed; refusing to overwrite it", owned.Path)
			}
			if present && prior.SchemaVersion == 3 && uint32(mode.Perm()) != owned.Mode {
				return Result{}, fmt.Errorf("generated file %s mode was changed; refusing to overwrite it", owned.Path)
			}
		}
	}
	for path := range files {
		if !allowedPath(p.Harness, path) {
			return Result{}, errors.New("generated harness path is not allowed")
		}
		_, _, present, err := generatedState(p.WorkspaceRoot, path)
		if err != nil {
			return Result{}, err
		}
		if present && priorFiles[path].Path == "" {
			return Result{}, fmt.Errorf("native file %s already exists without hctl ownership; refusing to overwrite it", path)
		}
	}

	paths := sortedKeys(files)
	owned := make([]ownedFile, 0, len(paths))
	for _, path := range paths {
		file := files[path]
		if err := rootfs.WriteAtomic(p.WorkspaceRoot, path, file.Content, file.Mode); err != nil {
			return Result{}, err
		}
		owned = append(owned, ownedFile{Path: path, SHA256: rootfs.SHA256(file.Content), Mode: uint32(file.Mode.Perm())})
	}
	for path := range priorFiles {
		if _, retained := files[path]; !retained {
			if err := rootfs.RemoveRegular(p.WorkspaceRoot, path); err != nil {
				return Result{}, err
			}
		}
	}
	meta := applyRecord{SchemaVersion: 3, Generator: project.GeneratorVersion, Harness: p.Harness, AgentID: p.AgentID, Source: p.SourceReference, SourceFingerprint: p.SourceFingerprint, Files: owned}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Result{}, errors.New("cannot encode apply record")
	}
	if err := rootfs.WriteAtomic(p.WorkspaceRoot, recordPath, append(metaBytes, '\n'), 0o644); err != nil {
		return Result{}, err
	}
	if legacy {
		if err := rootfs.RemoveRegular(p.WorkspaceRoot, legacyRecordPath(p.Harness)); err != nil {
			return Result{}, err
		}
	}
	if err := Verify(p); err != nil {
		return Result{}, err
	}
	return Result{Files: append(paths, recordPath), Diagnostics: generated.Diagnostics}, nil
}

func Verify(p *project.Project) error {
	meta, exists, err := readApplyRecord(p.WorkspaceRoot, applyRecordPath(p.Harness))
	if err != nil {
		return err
	}
	if !exists || meta.SchemaVersion != 3 || meta.Generator != project.GeneratorVersion || meta.AgentID != p.AgentID || meta.Source != p.SourceReference || meta.SourceFingerprint != p.SourceFingerprint || meta.Harness != p.Harness {
		return fmt.Errorf("%s setup is missing or stale; run hctl apply first", p.Harness)
	}
	if len(meta.Files) == 0 {
		return fmt.Errorf("%s setup is incomplete; run hctl apply first", p.Harness)
	}
	seen := map[string]bool{}
	for _, owned := range meta.Files {
		if seen[owned.Path] || !allowedPath(p.Harness, owned.Path) {
			return errors.New("apply record contains an invalid path")
		}
		seen[owned.Path] = true
		actual, mode, present, err := generatedState(p.WorkspaceRoot, owned.Path)
		if err != nil || !present || actual != owned.SHA256 || uint32(mode.Perm()) != owned.Mode {
			return fmt.Errorf("%s generated file %s is missing or changed; run hctl apply first", p.Harness, owned.Path)
		}
	}
	return nil
}

func filesFor(p *project.Project, executable string) (generatedSetup, error) {
	files := map[string]generatedFile{}
	diagnostics := []Diagnostic{}
	header := fmt.Sprintf("<!-- Generated by %s from source %s. Safe to discard; edit portable source instead. -->\n\n", project.GeneratorVersion, p.SourceFingerprint)
	instructions := header + "# " + p.Name + "\n\n" + strings.TrimSpace(string(p.Instructions)) + "\n\n## Tool boundary\n\nTools exposed by the hctl MCP server are managed. Native harness tools remain allowed and unmanaged.\n"
	mcpArgs := []string{"mcp", "serve", p.SourceRoot, "--workspace", p.WorkspaceRoot, "--harness", p.Harness}
	if p.Harness == "claude" {
		files["CLAUDE.md"] = generatedFile{Content: []byte(instructions), Mode: 0o644}
		config := map[string]any{"mcpServers": map[string]any{"managed": map[string]any{"type": "stdio", "command": executable, "args": mcpArgs}}}
		configBytes, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return generatedSetup{}, errors.New("cannot encode Claude MCP configuration")
		}
		files[".mcp.json"] = generatedFile{Content: append(configBytes, '\n'), Mode: 0o644}
		for _, skill := range p.Skills {
			for _, file := range skill.Files {
				if file.Path == "agents/openai.yaml" {
					fields, classifyErr := omittableOpenAIFields(file.Content)
					if classifyErr != nil {
						return generatedSetup{}, fmt.Errorf("skill %q (skills/%s/%s): %w", skill.Name, skill.Name, file.Path, classifyErr)
					}
					for _, field := range fields {
						diagnostics = append(diagnostics, warning("skills/"+skill.Name+"/"+file.Path, field, "claude", "OpenAI presentation metadata is not supported and was omitted"))
					}
					continue
				}
				content := file.Content
				if file.Path == "SKILL.md" {
					marked, markErr := markGeneratedSkill(content, p.SourceFingerprint)
					if markErr != nil {
						return generatedSetup{}, fmt.Errorf("skill %q: %w", skill.Name, markErr)
					}
					content = marked
				}
				files[fmt.Sprintf(".claude/skills/%s/%s", skill.Name, file.Path)] = generatedFile{Content: content, Mode: generatedMode(file.Executable)}
			}
		}
		for _, subagent := range p.Subagents {
			files[fmt.Sprintf(".claude/agents/%s.md", subagent.Name)] = generatedFile{Content: []byte("---\nname: " + subagent.Name + "\ndescription: " + strconv.Quote(subagent.Description) + "\n---\n\n" + strings.TrimSpace(string(subagent.Instructions)) + "\n"), Mode: 0o644}
		}
	} else {
		files["AGENTS.md"] = generatedFile{Content: []byte(instructions), Mode: 0o644}
		quotedArgs := make([]string, len(mcpArgs))
		for index, argument := range mcpArgs {
			quotedArgs[index] = strconv.Quote(argument)
		}
		files[".codex/config.toml"] = generatedFile{Content: []byte("# Generated by " + project.GeneratorVersion + "; safe to discard.\n[mcp_servers.managed]\ncommand = " + strconv.Quote(executable) + "\nargs = [" + strings.Join(quotedArgs, ", ") + "]\n"), Mode: 0o644}
		for _, skill := range p.Skills {
			if skill.AllowedToolsPresent {
				return generatedSetup{}, fmt.Errorf("skill %q (skills/%s/SKILL.md): field %q changes tool approval and is not supported by Codex; remove it or apply to Claude", skill.Name, skill.Name, "allowed-tools")
			}
			omitted, blocked := classifyClaudeFields(skill.ClaudeFields)
			if len(blocked) > 0 {
				return generatedSetup{}, fmt.Errorf("skill %q (skills/%s/SKILL.md): Claude-only fields %s change behavior and are not supported by Codex; remove them or apply to Claude", skill.Name, skill.Name, strings.Join(blocked, ", "))
			}
			for _, field := range omitted {
				diagnostics = append(diagnostics, warning("skills/"+skill.Name+"/SKILL.md", field, "codex", "Claude presentation or discovery metadata is not supported and was omitted"))
			}
			for _, file := range skill.Files {
				content := file.Content
				if file.Path == "SKILL.md" {
					if len(omitted) > 0 {
						omittedContent, omitErr := omitSkillFields(content, omitted)
						if omitErr != nil {
							return generatedSetup{}, fmt.Errorf("skill %q: cannot omit unsupported metadata: %w", skill.Name, omitErr)
						}
						content = omittedContent
					}
					marked, markErr := markGeneratedSkill(content, p.SourceFingerprint)
					if markErr != nil {
						return generatedSetup{}, fmt.Errorf("skill %q: %w", skill.Name, markErr)
					}
					content = marked
				}
				files[fmt.Sprintf(".agents/skills/%s/%s", skill.Name, file.Path)] = generatedFile{Content: content, Mode: generatedMode(file.Executable)}
			}
		}
		for _, subagent := range p.Subagents {
			files[fmt.Sprintf(".codex/agents/%s.toml", subagent.Name)] = generatedFile{Content: []byte("name = " + strconv.Quote(subagent.Name) + "\ndescription = " + strconv.Quote(subagent.Description) + "\ndeveloper_instructions = " + strconv.Quote(strings.TrimSpace(string(subagent.Instructions))) + "\n"), Mode: 0o644}
		}
	}
	return generatedSetup{Files: files, Diagnostics: diagnostics}, nil
}

func warning(path, field, harness, message string) Diagnostic {
	return Diagnostic{Severity: "warning", Path: path, Field: field, Harness: harness, Message: message}
}

func classifyClaudeFields(fields []string) ([]string, []string) {
	omitted := []string{}
	blocked := []string{}
	for _, field := range fields {
		if omittableClaudeFields[field] {
			omitted = append(omitted, field)
		} else {
			blocked = append(blocked, field)
		}
	}
	return omitted, blocked
}

func omittableOpenAIFields(content []byte) ([]string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("agents/openai.yaml must contain valid YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("agents/openai.yaml must contain one YAML document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("agents/openai.yaml must be a YAML mapping")
	}
	root := document.Content[0]
	seenRoot := map[string]bool{}
	fields := []string{}
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index], root.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seenRoot[key.Value] {
			return nil, errors.New("agents/openai.yaml must use unique string fields")
		}
		seenRoot[key.Value] = true
		if key.Value != "interface" {
			return nil, fmt.Errorf("field %q may change OpenAI behavior and cannot be omitted for Claude", key.Value)
		}
		if value.Kind != yaml.MappingNode {
			return nil, errors.New("field \"interface\" must be a mapping")
		}
		seenInterface := map[string]bool{}
		for child := 0; child < len(value.Content); child += 2 {
			childKey, childValue := value.Content[child], value.Content[child+1]
			if childKey.Kind != yaml.ScalarNode || childKey.Tag != "!!str" || seenInterface[childKey.Value] {
				return nil, errors.New("field \"interface\" must use unique string fields")
			}
			seenInterface[childKey.Value] = true
			field := "interface." + childKey.Value
			if !omittableOpenAIInterfaceFields[childKey.Value] {
				return nil, fmt.Errorf("field %q may change OpenAI behavior and cannot be omitted for Claude", field)
			}
			if childValue.Kind != yaml.ScalarNode || childValue.Tag != "!!str" {
				return nil, fmt.Errorf("field %q must be a string", field)
			}
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil, errors.New("agents/openai.yaml contains no safely omittable presentation metadata")
	}
	sort.Strings(fields)
	return fields, nil
}

func omitSkillFields(content []byte, fields []string) ([]byte, error) {
	frontmatterStart, frontmatterClose, bodyStart, err := frontmatterBounds(content)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content[frontmatterStart:frontmatterClose]))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("SKILL.md frontmatter must be a YAML mapping")
	}
	omit := map[string]bool{}
	for _, field := range fields {
		omit[field] = true
	}
	root := document.Content[0]
	filtered := make([]*yaml.Node, 0, len(root.Content))
	for index := 0; index < len(root.Content); index += 2 {
		if !omit[root.Content[index].Value] {
			filtered = append(filtered, root.Content[index], root.Content[index+1])
		}
	}
	root.Content = filtered
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(root); err != nil {
		return nil, errors.New("cannot encode portable skill frontmatter")
	}
	if err := encoder.Close(); err != nil {
		return nil, errors.New("cannot finish portable skill frontmatter")
	}
	result := []byte("---\n")
	result = append(result, encoded.Bytes()...)
	result = append(result, []byte("---\n")...)
	result = append(result, content[bodyStart:]...)
	return result, nil
}

func markGeneratedSkill(content []byte, fingerprint string) ([]byte, error) {
	insert, err := frontmatterEnd(content)
	if err != nil {
		return nil, err
	}
	out := append([]byte{}, content[:insert]...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, []byte(fmt.Sprintf("<!-- Generated by %s from source %s. Safe to discard. -->\n", project.GeneratorVersion, fingerprint))...)
	out = append(out, content[insert:]...)
	return out, nil
}

func frontmatterEnd(content []byte) (int, error) {
	_, _, end, err := frontmatterBounds(content)
	return end, err
}

func frontmatterBounds(content []byte) (int, int, int, error) {
	position := 0
	frontmatterStart := 0
	for lineNumber := 0; position < len(content); lineNumber++ {
		newline := bytes.IndexByte(content[position:], '\n')
		end := len(content)
		if newline >= 0 {
			end = position + newline + 1
		}
		line := bytes.TrimSuffix(content[position:end], []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if lineNumber == 0 && !bytes.Equal(line, []byte("---")) {
			return 0, 0, 0, errors.New("SKILL.md frontmatter is missing")
		}
		if lineNumber == 0 {
			frontmatterStart = end
		}
		if lineNumber > 0 && bytes.Equal(line, []byte("---")) {
			return frontmatterStart, position, end, nil
		}
		position = end
	}
	return 0, 0, 0, errors.New("SKILL.md frontmatter is not closed")
}

func generatedMode(executable bool) os.FileMode {
	if executable {
		return 0o755
	}
	return 0o644
}

func applyRecordPath(harness string) string {
	return fmt.Sprintf(".hctl/apply/%s.json", harness)
}

func legacyRecordPath(harness string) string {
	return fmt.Sprintf(".hctl/projections/%s.json", harness)
}

func loadApplyRecord(root, harness string) (applyRecord, bool, bool, error) {
	meta, exists, err := readApplyRecord(root, applyRecordPath(harness))
	if err != nil || exists {
		return meta, exists, false, err
	}
	meta, exists, err = readApplyRecord(root, legacyRecordPath(harness))
	return meta, exists, exists, err
}

func readApplyRecord(root, relative string) (applyRecord, bool, error) {
	var meta applyRecord
	data, _, exists, err := rootfs.ReadOptional(root, relative, maxMetadataBytes)
	if err != nil || !exists {
		return meta, exists, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&meta); err != nil {
		return meta, false, errors.New("apply record is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return meta, false, errors.New("apply record is invalid")
	}
	return meta, true, nil
}

func allowedPath(harness, path string) bool {
	if harness == "claude" {
		if path == "CLAUDE.md" || path == ".mcp.json" {
			return true
		}
		return generatedSkillPath(path, ".claude/skills/") || generatedSubagentPath(path, ".claude/agents/", ".md")
	}
	if path == "AGENTS.md" || path == ".codex/config.toml" {
		return true
	}
	return generatedSkillPath(path, ".agents/skills/") || generatedSubagentPath(path, ".codex/agents/", ".toml")
}

func allowedOwnedPath(harness, path string, legacy bool) bool {
	return allowedPath(harness, path) || legacy && path == fmt.Sprintf(".hctl/manifests/%s.json", harness)
}

func generatedSkillPath(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(path, prefix)
	name, resource, ok := strings.Cut(remainder, "/")
	if !ok || len(name) > 64 || !skillName.MatchString(name) {
		return false
	}
	_, err := rootfs.CleanRelative(resource)
	return err == nil
}

func generatedSubagentPath(path, prefix, suffix string) bool {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return portableName.MatchString(name) && !strings.Contains(name, "/")
}

func generatedState(root, relative string) (string, os.FileMode, bool, error) {
	data, mode, exists, err := rootfs.ReadOptional(root, relative, 1<<20)
	if err != nil || !exists {
		return "", mode, exists, err
	}
	return rootfs.SHA256(data), mode, true, nil
}

func sortedKeys(values map[string]generatedFile) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
