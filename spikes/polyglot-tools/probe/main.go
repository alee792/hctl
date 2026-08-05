package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"hctl/internal/project"
	"hctl/internal/tool"
)

type mcpConfig struct {
	Command string
	Args    []string
}

func main() {
	repository, err := os.Getwd()
	must(err)
	temporary, err := os.MkdirTemp("", "hctl-polyglot-")
	must(err)
	defer func() { _ = os.RemoveAll(temporary) }()

	binary := filepath.Join(temporary, "hctl")
	run(repository, "go", "build", "-o", binary, "./cmd/hctl")
	fixture := filepath.Join(repository, "spikes", "polyglot-tools", "fixture")
	agent := filepath.Join(temporary, "agent")
	workspace := filepath.Join(temporary, "workspace")
	copyProject(fixture, agent)
	must(os.MkdirAll(workspace, 0o755))
	agent, err = filepath.EvalSymlinks(agent)
	must(err)
	workspace, err = filepath.EvalSymlinks(workspace)
	must(err)
	claude := fakeHarness(temporary, "claude", "Claude Code")
	codex := fakeHarness(temporary, "codex", "Codex CLI")

	run(repository, binary, "apply", agent, "--workspace", workspace, "--harness", "claude", "--command", claude)
	run(repository, binary, "apply", agent, "--workspace", workspace, "--harness", "codex", "--command", codex)
	proveMCP("claude", agent, workspace, readClaudeConfig(workspace))
	proveMCP("codex", agent, workspace, readCodexConfig(workspace))
	provePortableSource(agent)
	proveSubagents(workspace)
	proveSwitch(repository, temporary, binary, claude, workspace)
	proveApplyFailures(repository, temporary, binary, claude, fixture)
	proveRuntimeFailures(temporary, fixture)

	fmt.Println("polyglot proof passed through apply and generated Claude/Codex MCP configs")
}

func provePortableSource(agent string) {
	for _, generated := range []string{".hctl", ".venv", "CLAUDE.md", "AGENTS.md", ".mcp.json", ".claude", ".codex", ".agents"} {
		if _, err := os.Lstat(filepath.Join(agent, generated)); !os.IsNotExist(err) {
			fail("apply wrote runtime state into portable agent source: %s", generated)
		}
	}
}

func proveMCP(harness, agent, workspace string, config mcpConfig) {
	if config.Command == "" || len(config.Args) != 7 || config.Args[0] != "mcp" || config.Args[1] != "serve" || config.Args[2] != agent || config.Args[3] != "--workspace" || config.Args[4] != workspace || config.Args[5] != "--harness" || config.Args[6] != harness {
		fail("%s generated an invalid MCP command: %#v", harness, config)
	}
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"repeat","arguments":{"text":"hello"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"repeat","arguments":{"text":"again"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"add","arguments":{"left":2,"right":3}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"add","arguments":{"left":3,"right":4}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"hash-text","arguments":{"text":"hello"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"hash-text","arguments":{"text":"again"}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"repeat","arguments":{"text":"","extra":true}}}`,
	}
	command := exec.Command(config.Command, config.Args...)
	command.Dir = workspace
	command.Stdin = strings.NewReader(strings.Join(requests, "\n") + "\n")
	var output, audit bytes.Buffer
	command.Stdout = &output
	command.Stderr = &audit
	must(command.Run())

	responses := map[int]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var response map[string]any
		must(json.Unmarshal([]byte(line), &response))
		responses[int(response["id"].(float64))] = response
	}
	tools := responses[2]["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, value := range tools {
		names = append(names, value.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "add,echo,hash-text,repeat" {
		fail("%s tools/list = %v", harness, names)
	}
	for id, expected := range map[int]int{3: 1, 4: 2, 5: 1, 6: 2, 7: 1, 8: 2} {
		result := responses[id]["result"].(map[string]any)
		if result["isError"] != false || int(result["structuredContent"].(map[string]any)["calls"].(float64)) != expected {
			fail("%s call %d did not reuse its language host: %#v", harness, id, result)
		}
	}
	if responses[9]["result"].(map[string]any)["isError"] != true {
		fail("%s accepted invalid tool input", harness)
	}
	log := audit.String()
	if strings.Contains(log, "hello") || !strings.Contains(log, "outcome=requested") || !strings.Contains(log, "outcome=authorized") || !strings.Contains(log, "outcome=completed") || !strings.Contains(log, "outcome=failed") {
		fail("%s emitted unsafe or incomplete audit output: %q", harness, log)
	}
}

func proveSubagents(workspace string) {
	for _, path := range []string{".claude/agents/verifier.md", ".codex/agents/verifier.toml"} {
		data, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(path)))
		must(err)
		if !strings.Contains(string(data), "Verify tool results") {
			fail("generated subagent %s is incomplete", path)
		}
	}
}

func proveSwitch(repository, temporary, binary, claude, workspace string) {
	second := filepath.Join(temporary, "second-agent")
	must(os.MkdirAll(second, 0o755))
	must(os.WriteFile(filepath.Join(second, "instructions.md"), []byte("---\ndescription: A second fixture agent.\n---\n\nBe concise.\n"), 0o644))
	run(repository, binary, "apply", second, "--workspace", workspace, "--harness", "claude", "--command", claude)
	if _, err := os.Stat(filepath.Join(workspace, ".claude", "agents", "verifier.md")); !os.IsNotExist(err) {
		fail("switching agents did not remove obsolete Claude subagent")
	}
}

func proveApplyFailures(repository, temporary, binary, claude, fixture string) {
	duplicate := caseProject(temporary, fixture, "duplicate", "")
	expectFailure(repository, "duplicate tool name", binary, "apply", duplicate, "--harness", "claude", "--command", claude)

	badSignature := caseProject(temporary, fixture, "invalid-signature", "typescript")
	expectFailure(repository, "must default-export", binary, "apply", badSignature, "--harness", "claude", "--command", claude)
}

func proveRuntimeFailures(temporary, fixture string) {
	tests := []struct {
		name      string
		language  string
		toolName  string
		want      string
		deadline  time.Duration
		arguments json.RawMessage
	}{
		{name: "invalid-output", language: "python", toolName: "bad", want: "validation", deadline: 5 * time.Second, arguments: json.RawMessage(`{}`)},
		{name: "timeout", language: "typescript", toolName: "slow", want: "exceeded", deadline: 100 * time.Millisecond, arguments: json.RawMessage(`{}`)},
		{name: "process", language: "typescript", toolName: "crash", want: "exited", deadline: 5 * time.Second, arguments: json.RawMessage(`{}`)},
	}
	for _, test := range tests {
		root := caseProject(temporary, fixture, test.name, test.language)
		loaded, err := project.Load(root, "claude")
		must(err)
		prepareContext, cancelPrepare := context.WithTimeout(context.Background(), 2*time.Minute)
		must(tool.Prepare(prepareContext, loaded.SourceRoot, loaded.WorkspaceRoot, loaded.SourceFingerprint, loaded.Tools))
		cancelPrepare()
		runtime, err := tool.Open(context.Background(), loaded.SourceRoot, loaded.WorkspaceRoot, loaded.SourceFingerprint, loaded.Tools)
		must(err)
		callContext, cancelCall := context.WithTimeout(context.Background(), test.deadline)
		_, err = runtime.Call(callContext, test.toolName, test.arguments)
		cancelCall()
		runtime.Close()
		if err == nil || !strings.Contains(err.Error(), test.want) {
			fail("%s failure = %v, want diagnostic containing %q", test.name, err, test.want)
		}
	}
}

func readClaudeConfig(root string) mcpConfig {
	data, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	must(err)
	var document struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	must(json.Unmarshal(data, &document))
	server := document.Servers["managed"]
	return mcpConfig{Command: server.Command, Args: server.Args}
}

func readCodexConfig(root string) mcpConfig {
	data, err := os.ReadFile(filepath.Join(root, ".codex", "config.toml"))
	must(err)
	var config mcpConfig
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "command = "); ok {
			config.Command, err = strconv.Unquote(value)
			must(err)
		}
		if value, ok := strings.CutPrefix(line, "args = "); ok {
			must(json.Unmarshal([]byte(value), &config.Args))
		}
	}
	return config
}

func caseProject(temporary, fixture, name, language string) string {
	root := filepath.Join(temporary, "case-"+name)
	must(os.RemoveAll(root))
	must(os.MkdirAll(root, 0o755))
	copyFile(filepath.Join(fixture, "instructions.md"), filepath.Join(root, "instructions.md"))
	switch language {
	case "typescript":
		copyFile(filepath.Join(fixture, "deno.json"), filepath.Join(root, "deno.json"))
		copyFile(filepath.Join(fixture, "deno.lock"), filepath.Join(root, "deno.lock"))
	case "python":
		copyFile(filepath.Join(fixture, "pyproject.toml"), filepath.Join(root, "pyproject.toml"))
		copyFile(filepath.Join(fixture, "uv.lock"), filepath.Join(root, "uv.lock"))
	}
	caseTools := filepath.Join(filepath.Dir(fixture), "cases", name, "tools")
	copyProject(caseTools, filepath.Join(root, "tools"))
	return root
}

func copyProject(source, target string) {
	must(filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(target, 0o755)
		}
		name := entry.Name()
		if entry.IsDir() && (name == ".hctl" || name == ".venv" || name == ".claude" || name == ".codex" || name == ".agents" || name == "__pycache__") {
			return filepath.SkipDir
		}
		if name == "CLAUDE.md" || name == "AGENTS.md" || name == ".mcp.json" || strings.HasSuffix(name, ".pyc") {
			return nil
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		copyFile(path, destination)
		return nil
	}))
}

func copyFile(source, target string) {
	data, err := os.ReadFile(source)
	must(err)
	must(os.MkdirAll(filepath.Dir(target), 0o755))
	must(os.WriteFile(target, data, 0o644))
}

func fakeHarness(directory, name, title string) string {
	path := filepath.Join(directory, name)
	source := fmt.Sprintf("#!/bin/sh\nif [ \"${1:-}\" = \"--version\" ]; then\n  echo '%s 1.2.3'\n  exit 0\nfi\nexit 1\n", title)
	must(os.WriteFile(path, []byte(source), 0o755))
	return path
}

func expectFailure(directory, diagnostic, command string, arguments ...string) {
	cmd := exec.Command(command, arguments...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), diagnostic) {
		fail("%s failure = %v\n%s", diagnostic, err, output)
	}
}

func run(directory, command string, arguments ...string) {
	cmd := exec.Command(command, arguments...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		fail("%s %s: %v\n%s", command, strings.Join(arguments, " "), err, output)
	}
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "polyglot proof failed: "+format+"\n", arguments...)
	os.Exit(1)
}
