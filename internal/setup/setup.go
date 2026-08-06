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

	"hctl/internal/channelconfig"
	"hctl/internal/project"
	"hctl/internal/rootfs"
)

const maxMetadataBytes = 8 << 20

var (
	portableName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	skillName    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
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
	ChannelWritable   bool        `json:"channel_writable,omitempty"`
	Files             []ownedFile `json:"files"`
}

func Apply(p *project.Project, executable string) (Result, error) {
	return apply(p, executable, false)
}

func ApplyWritableChannel(p *project.Project, executable string) (Result, error) {
	return apply(p, executable, true)
}

func apply(p *project.Project, executable string, channelWritable bool) (Result, error) {
	if !filepath.IsAbs(executable) {
		return Result{}, errors.New("managed MCP executable path must be absolute")
	}
	generated, err := filesForPolicy(p, executable, channelWritable)
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
	meta := applyRecord{SchemaVersion: 3, Generator: project.GeneratorVersion, Harness: p.Harness, AgentID: p.AgentID, Source: p.SourceReference, SourceFingerprint: p.SourceFingerprint, ChannelWritable: channelWritable, Files: owned}
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
	if err := verify(p, channelWritable); err != nil {
		return Result{}, err
	}
	return Result{Files: append(paths, recordPath), Diagnostics: generated.Diagnostics}, nil
}

func Verify(p *project.Project) error {
	return verify(p, false)
}

func VerifyWritableChannel(p *project.Project) error {
	return verify(p, true)
}

func verify(p *project.Project, channelWritable bool) error {
	meta, exists, err := readApplyRecord(p.WorkspaceRoot, applyRecordPath(p.Harness))
	if err != nil {
		return err
	}
	if !exists || meta.SchemaVersion != 3 || meta.Generator != project.GeneratorVersion || meta.AgentID != p.AgentID || meta.Source != p.SourceReference || meta.SourceFingerprint != p.SourceFingerprint || meta.Harness != p.Harness || meta.ChannelWritable != channelWritable {
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
	return filesForPolicy(p, executable, false)
}

func filesForPolicy(p *project.Project, executable string, channelWritable bool) (generatedSetup, error) {
	files := map[string]generatedFile{}
	diagnostics := []Diagnostic{}
	header := fmt.Sprintf("<!-- Generated by %s from source %s. Safe to discard; edit portable source instead. -->\n\n", project.GeneratorVersion, p.SourceFingerprint)
	instructions := header + "# " + p.Name + "\n\n" + strings.TrimSpace(string(p.Instructions)) + "\n\n## Tool boundary\n\nTools exposed by the hctl MCP server are managed. Native harness tools remain allowed and unmanaged.\n"
	if p.DiscordChannel != nil {
		instructions += "\n## Discord channel participation\n\nFor inputs explicitly marked as Discord messages, apply this participation policy before answering:\n\n" + strings.TrimSpace(string(p.DiscordChannel.Policy)) + "\n\nA Discord input marked `direct=true` is explicitly addressed to you; respond normally unless the user asks for silence. For other ambient guild messages, if no visible response is warranted, return exactly `" + channelconfig.NoReplyResult + "` and nothing else."
		if channelWritable {
			instructions += " This conversation already has workspace-write access in its isolated worktree. Complete requests normally; write access has already been granted."
		} else {
			instructions += " Channel sessions are enforced read-only. If the user's request genuinely requires changing the workspace and cannot be completed with read-only operations, return exactly `" + channelconfig.RequestWriteAccessResult + "` and nothing else; do not ask the native harness for permission or attempt to bypass the policy."
		}
		instructions += " These control results apply only to Discord inputs.\n"
	}
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
					diagnostics = append(diagnostics, warning("skills/"+skill.Name+"/"+file.Path, "", "claude", "OpenAI metadata is not documented by Claude; copied unchanged but may have no effect"))
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
			effort := ""
			if subagent.Effort != "" {
				effort = "effort: " + subagent.Effort + "\n"
			}
			files[fmt.Sprintf(".claude/agents/%s.md", subagent.Name)] = generatedFile{Content: []byte("---\nname: " + subagent.Name + "\ndescription: " + strconv.Quote(subagent.Description) + "\n" + effort + "---\n\n" + strings.TrimSpace(string(subagent.Instructions)) + "\n"), Mode: 0o644}
		}
	} else {
		files["AGENTS.md"] = generatedFile{Content: []byte(instructions), Mode: 0o644}
		quotedArgs := make([]string, len(mcpArgs))
		for index, argument := range mcpArgs {
			quotedArgs[index] = strconv.Quote(argument)
		}
		files[".codex/config.toml"] = generatedFile{Content: []byte("# Generated by " + project.GeneratorVersion + "; safe to discard.\n[mcp_servers.managed]\ncommand = " + strconv.Quote(executable) + "\nargs = [" + strings.Join(quotedArgs, ", ") + "]\nrequired = true\ndefault_tools_approval_mode = \"approve\"\n"), Mode: 0o644}
		for _, skill := range p.Skills {
			if skill.AllowedToolsPresent {
				diagnostics = append(diagnostics, warning("skills/"+skill.Name+"/SKILL.md", "allowed-tools", "codex", "support is not documented; copied unchanged but may have no effect"))
			}
			for _, field := range skill.ClaudeFields {
				diagnostics = append(diagnostics, warning("skills/"+skill.Name+"/SKILL.md", field, "codex", "Claude-specific behavior is not documented; copied unchanged but may have no effect"))
			}
			for _, file := range skill.Files {
				content := file.Content
				if file.Path == "SKILL.md" {
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
			codexName := strings.ReplaceAll(subagent.Name, "-", "_")
			effort := ""
			if subagent.Effort != "" {
				effort = "model_reasoning_effort = " + strconv.Quote(subagent.Effort) + "\n"
			}
			files[fmt.Sprintf(".codex/agents/%s.toml", subagent.Name)] = generatedFile{Content: []byte("name = " + strconv.Quote(codexName) + "\ndescription = " + strconv.Quote(subagent.Description) + "\n" + effort + "developer_instructions = " + strconv.Quote(strings.TrimSpace(string(subagent.Instructions))) + "\n"), Mode: 0o644}
		}
	}
	for _, file := range p.HarnessFiles {
		if _, reserved := files[file.Path]; reserved {
			return generatedSetup{}, fmt.Errorf("harness-specific file %s conflicts with hctl setup", file.Path)
		}
		files[file.Path] = generatedFile{Content: file.Content, Mode: generatedMode(file.Executable)}
	}
	return generatedSetup{Files: files, Diagnostics: diagnostics}, nil
}

func warning(path, field, harness, message string) Diagnostic {
	return Diagnostic{Severity: "warning", Path: path, Field: field, Harness: harness, Message: message}
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
	position := 0
	for lineNumber := 0; position < len(content); lineNumber++ {
		newline := bytes.IndexByte(content[position:], '\n')
		end := len(content)
		if newline >= 0 {
			end = position + newline + 1
		}
		line := bytes.TrimSuffix(content[position:end], []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if lineNumber == 0 && !bytes.Equal(line, []byte("---")) {
			return 0, errors.New("SKILL.md frontmatter is missing")
		}
		if lineNumber > 0 && bytes.Equal(line, []byte("---")) {
			return end, nil
		}
		position = end
	}
	return 0, errors.New("SKILL.md frontmatter is not closed")
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
		return generatedSkillPath(path, ".claude/skills/") || generatedSubagentPath(path, ".claude/agents/", ".md") || project.IsHarnessFilePath(harness, path)
	}
	if path == "AGENTS.md" || path == ".codex/config.toml" {
		return true
	}
	return generatedSkillPath(path, ".agents/skills/") || generatedSubagentPath(path, ".codex/agents/", ".toml") || project.IsHarnessFilePath(harness, path)
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
