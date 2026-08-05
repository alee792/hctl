package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type host struct {
	name   string
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
	stderr bytes.Buffer
}

type reply struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

type catalog struct {
	InstanceID string `json:"instanceId"`
	Tools      []struct {
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		InputSchema  json.RawMessage `json:"inputSchema"`
		OutputSchema json.RawMessage `json:"outputSchema"`
	} `json:"tools"`
}

type callResult struct {
	InstanceID string         `json:"instanceId"`
	Output     map[string]any `json:"output"`
}

func main() {
	root, err := os.Getwd()
	must(err)
	fixture := filepath.Join(root, "spikes", "polyglot-tools", "fixture")
	hostDir := filepath.Join(root, "spikes", "polyglot-tools", "host")

	run(root, "deno", "check", "--config", filepath.Join(fixture, "deno.json"), "--frozen", filepath.Join(hostDir, "typescript.ts"), filepath.Join(fixture, "tools", "repeat.ts"))
	run(root, "uv", "sync", "--locked", "--project", fixture)
	goHost := prepareGoHost(root, fixture, hostDir)

	hosts := []*host{
		start("typescript", root, "deno", "run", "--quiet", "--frozen", "--config", filepath.Join(fixture, "deno.json"), "--allow-read="+fixture, filepath.Join(hostDir, "typescript.ts"), fixture),
		start("python", root, "uv", "run", "--locked", "--project", fixture, "python", filepath.Join(hostDir, "python.py"), fixture),
		start("go", root, goHost),
	}
	defer func() {
		for _, host := range hosts {
			host.stop()
		}
	}()

	byTool := map[string]*host{}
	instances := map[*host]string{}
	var toolNames []string
	for _, host := range hosts {
		var listed catalog
		host.request("list", nil, &listed)
		if listed.InstanceID == "" {
			fail("%s host returned no instance identity", host.name)
		}
		instances[host] = listed.InstanceID
		for _, tool := range listed.Tools {
			if len(tool.InputSchema) == 0 || len(tool.OutputSchema) == 0 {
				fail("%s tool %s returned incomplete schemas", host.name, tool.Name)
			}
			if prior := byTool[tool.Name]; prior != nil {
				fail("duplicate tool name %q from %s and %s", tool.Name, prior.name, host.name)
			}
			byTool[tool.Name] = host
			toolNames = append(toolNames, tool.Name)
		}
	}
	sort.Strings(toolNames)
	wantNames := []string{"add", "hash-text", "repeat"}
	if strings.Join(toolNames, ",") != strings.Join(wantNames, ",") {
		fail("combined catalog = %v, want %v", toolNames, wantNames)
	}

	calls := []struct {
		name string
		args map[string]any
	}{
		{name: "repeat", args: map[string]any{"text": "hello"}},
		{name: "add", args: map[string]any{"left": 2, "right": 3}},
		{name: "hash-text", args: map[string]any{"text": "hello"}},
	}
	for _, call := range calls {
		host := byTool[call.name]
		for invocation := 1; invocation <= 2; invocation++ {
			var result callResult
			host.request("call", map[string]any{"name": call.name, "arguments": call.args}, &result)
			if result.InstanceID != instances[host] {
				fail("%s restarted between inspection and call", host.name)
			}
			if int(result.Output["calls"].(float64)) != invocation {
				fail("%s call count = %v, want %d", call.name, result.Output["calls"], invocation)
			}
		}
	}

	if err := byTool["repeat"].requestError("call", map[string]any{"name": "repeat", "arguments": map[string]any{"text": "", "extra": true}}); err == nil {
		fail("invalid TypeScript input was accepted")
	}
	if err := byTool["add"].requestError("call", map[string]any{"name": "add", "arguments": map[string]any{"left": "2", "right": 3}}); err == nil {
		fail("invalid Python input was accepted")
	}
	if err := byTool["hash-text"].requestError("call", map[string]any{"name": "hash-text", "arguments": map[string]any{"text": ""}}); err == nil {
		fail("invalid Go input was accepted")
	}

	proveFailures(root, fixture, hostDir)

	goProcess := byTool["hash-text"]
	must(goProcess.cmd.Process.Kill())
	_ = goProcess.cmd.Wait()
	if err := goProcess.requestError("call", map[string]any{"name": "hash-text", "arguments": map[string]any{"text": "hello"}}); err == nil {
		fail("terminated Go host did not produce a process failure")
	}

	fmt.Printf("polyglot proof passed: %s; persistent hosts; definition, duplicate, validation, process, and timeout failures\n", strings.Join(toolNames, ", "))
}

func proveFailures(root, fixture, hostDir string) {
	cases := filepath.Join(root, "spikes", "polyglot-tools", "cases")

	badOutput := start("invalid-output", root, "uv", "run", "--locked", "--project", fixture, "python", filepath.Join(hostDir, "python.py"), filepath.Join(cases, "invalid-output"))
	defer badOutput.stop()
	var listed catalog
	badOutput.request("list", nil, &listed)
	if err := badOutput.requestError("call", map[string]any{"name": "bad", "arguments": map[string]any{}}); err == nil || !strings.Contains(err.Error(), "validation error") {
		fail("invalid Python output did not produce a validation diagnostic: %v", err)
	}

	badSignature := start("invalid-signature", root, "deno", "run", "--quiet", "--frozen", "--config", filepath.Join(fixture, "deno.json"), "--allow-read="+filepath.Join(cases, "invalid-signature"), filepath.Join(hostDir, "typescript.ts"), filepath.Join(cases, "invalid-signature"))
	defer badSignature.stop()
	if err := badSignature.requestError("list", nil); err == nil || !strings.Contains(err.Error(), "must default-export") {
		fail("invalid TypeScript definition did not produce a contract diagnostic: %v", err)
	}

	duplicateTS := start("duplicate-typescript", root, "deno", "run", "--quiet", "--frozen", "--config", filepath.Join(fixture, "deno.json"), "--allow-read="+filepath.Join(cases, "duplicate"), filepath.Join(hostDir, "typescript.ts"), filepath.Join(cases, "duplicate"))
	defer duplicateTS.stop()
	duplicatePython := start("duplicate-python", root, "uv", "run", "--locked", "--project", fixture, "python", filepath.Join(hostDir, "python.py"), filepath.Join(cases, "duplicate"))
	defer duplicatePython.stop()
	var tsCatalog, pythonCatalog catalog
	duplicateTS.request("list", nil, &tsCatalog)
	duplicatePython.request("list", nil, &pythonCatalog)
	seen := map[string]string{}
	if err := mergeCatalog(seen, "typescript", tsCatalog); err != nil {
		fail("unexpected first-catalog error: %v", err)
	}
	if err := mergeCatalog(seen, "python", pythonCatalog); err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		fail("cross-language duplicate was not rejected: %v", err)
	}

	timeout := start("timeout", root, "deno", "run", "--quiet", "--frozen", "--config", filepath.Join(fixture, "deno.json"), "--allow-read="+filepath.Join(cases, "timeout"), filepath.Join(hostDir, "typescript.ts"), filepath.Join(cases, "timeout"))
	defer timeout.stop()
	timeout.request("list", nil, &listed)
	if err := timeout.requestWithin("call", map[string]any{"name": "slow", "arguments": map[string]any{}}, nil, 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "exceeded") {
		fail("timed-out Python call did not terminate its host: %v", err)
	}
}

func mergeCatalog(seen map[string]string, language string, catalog catalog) error {
	for _, tool := range catalog.Tools {
		if prior := seen[tool.Name]; prior != "" {
			return fmt.Errorf("duplicate tool name %q from %s and %s", tool.Name, prior, language)
		}
		seen[tool.Name] = language
	}
	return nil
}

func prepareGoHost(root, fixture, hostDir string) string {
	moduleBytes, err := os.ReadFile(filepath.Join(fixture, "go.mod"))
	must(err)
	module := ""
	for _, line := range strings.Split(string(moduleBytes), "\n") {
		if value, ok := strings.CutPrefix(line, "module "); ok {
			module = strings.TrimSpace(value)
			break
		}
	}
	if module == "" {
		fail("fixture go.mod has no module directive")
	}

	entries, err := os.ReadDir(filepath.Join(fixture, "tools"))
	must(err)
	var imports, registrations []string
	var source bytes.Buffer
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		toolFile := filepath.Join(fixture, "tools", entry.Name(), "tool.go")
		if _, err := os.Stat(toolFile); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			fail("inspect %s: %v", toolFile, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), toolFile, nil, parser.PackageClauseOnly)
		if err != nil {
			fail("parse %s: %v", toolFile, err)
		}
		contents, err := os.ReadFile(toolFile)
		must(err)
		source.Write(contents)
		alias := fmt.Sprintf("tool%d", len(imports))
		imports = append(imports, fmt.Sprintf("\t%s %q", alias, module+"/tools/"+entry.Name()))
		name := strings.ReplaceAll(entry.Name(), "_", "-")
		registrations = append(registrations, fmt.Sprintf("\tnewTool[%s.Input, %s.Output](%q, %s.Description, %s.Execute),", alias, alias, name, alias, alias))
		_ = parsed.Name.Name
	}
	if len(imports) == 0 {
		fail("no Go tools discovered")
	}

	templateBytes, err := os.ReadFile(filepath.Join(hostDir, "go-main.go.tmpl"))
	must(err)
	source.Write(templateBytes)
	fingerprint := sha256.Sum256(source.Bytes())
	cacheDir := filepath.Join(root, ".tools", "cache", "polyglot-tools", hex.EncodeToString(fingerprint[:8]))
	must(os.MkdirAll(cacheDir, 0o755))

	mainSource := strings.ReplaceAll(string(templateBytes), "{{IMPORTS}}", strings.Join(imports, "\n"))
	mainSource = strings.ReplaceAll(mainSource, "{{TOOLS}}", strings.Join(registrations, "\n"))
	must(os.WriteFile(filepath.Join(cacheDir, "main.go"), []byte(mainSource), 0o644))
	goMod := fmt.Sprintf("module hctl.local/polyglot-host\n\ngo 1.24.0\n\nrequire (\n\tgithub.com/invopop/jsonschema v0.14.0\n\tgithub.com/santhosh-tekuri/jsonschema/v6 v6.0.2\n\t%s v0.0.0\n)\n\nreplace %s => %s\n", module, module, fixture)
	must(os.WriteFile(filepath.Join(cacheDir, "go.mod"), []byte(goMod), 0o644))
	run(cacheDir, "go", "mod", "tidy")
	binary := filepath.Join(cacheDir, "host")
	run(cacheDir, "go", "build", "-mod=readonly", "-o", binary, ".")
	return binary
}

func start(name, directory, command string, args ...string) *host {
	cmd := exec.Command(command, args...)
	cmd.Dir = directory
	input, err := cmd.StdinPipe()
	must(err)
	output, err := cmd.StdoutPipe()
	must(err)
	host := &host{name: name, cmd: cmd, input: input, output: bufio.NewScanner(output)}
	host.output.Buffer(make([]byte, 4096), 64<<10)
	cmd.Stderr = &host.stderr
	must(cmd.Start())
	return host
}

func (host *host) request(method string, params map[string]any, target any) {
	if err := host.requestInto(method, params, target); err != nil {
		fail("%s host %s: %v", host.name, method, err)
	}
}

func (host *host) requestError(method string, params map[string]any) error {
	return host.requestInto(method, params, nil)
}

func (host *host) requestInto(method string, params map[string]any, target any) error {
	return host.requestWithin(method, params, target, 5*time.Second)
}

func (host *host) requestWithin(method string, params map[string]any, target any, timeout time.Duration) error {
	id := host.name + "-request"
	encoded, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := host.input.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write request: %w: %s", err, strings.TrimSpace(host.stderr.String()))
	}
	type scanResult struct {
		line []byte
		err  error
	}
	result := make(chan scanResult, 1)
	go func() {
		if host.output.Scan() {
			result <- scanResult{line: append([]byte(nil), host.output.Bytes()...)}
			return
		}
		result <- scanResult{err: host.output.Err()}
	}()
	var scanned scanResult
	select {
	case scanned = <-result:
	case <-time.After(timeout):
		_ = host.cmd.Process.Kill()
		return fmt.Errorf("request exceeded %s; %s host terminated", timeout, host.name)
	}
	if scanned.line == nil {
		if scanned.err != nil {
			return fmt.Errorf("read response: %w", scanned.err)
		}
		return fmt.Errorf("process ended without a response: %s", strings.TrimSpace(host.stderr.String()))
	}
	var response reply
	if err := json.Unmarshal(scanned.line, &response); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if response.ID != id {
		return fmt.Errorf("response ID %q does not match %q", response.ID, id)
	}
	if target != nil {
		if err := json.Unmarshal(response.Result, target); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}

func (host *host) stop() {
	_ = host.input.Close()
	if err := host.cmd.Wait(); err != nil && host.cmd.ProcessState == nil {
		_ = host.cmd.Process.Kill()
	}
}

func run(directory, command string, args ...string) {
	cmd := exec.Command(command, args...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		fail("%s %s: %v\n%s", command, strings.Join(args, " "), err, output)
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
