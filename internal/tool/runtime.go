package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"hctl/internal/rootfs"
	"hctl/internal/secureenv"
)

const (
	maxHostCallLine    = 64 << 10
	maxHostCatalogLine = 8 << 20
	maxDescription     = 1024
	shutdownWait       = 2 * time.Second
)

var errHostResponseTooLarge = errors.New("tool host output exceeded the bounded response size")

type Definition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

type Runtime struct {
	clients []*client
	tools   map[string]hostedTool
}

type hostedTool struct {
	definition Definition
	client     *client
}

type hostCatalog struct {
	Tools []Definition `json:"tools"`
}

type hostCall struct {
	Output json.RawMessage `json:"output"`
}

func Open(ctx context.Context, sourceRoot, workspaceRoot, sourceFingerprint string, inventory Inventory) (*Runtime, error) {
	return open(ctx, sourceRoot, workspaceRoot, sourceFingerprint, inventory, nil)
}

func open(ctx context.Context, sourceRoot, workspaceRoot, sourceFingerprint string, inventory Inventory, baseEnvironment []string) (*Runtime, error) {
	runtime := &Runtime{tools: map[string]hostedTool{}}
	if len(inventory.Sources) == 0 {
		return runtime, nil
	}
	prepared, err := readPreparedRuntime(workspaceRoot, sourceFingerprint)
	if err != nil {
		return nil, err
	}
	for _, language := range []Language{TypeScript, Python, Go} {
		if !hasLanguage(inventory, language) {
			continue
		}
		command, arguments, environment, err := hostCommand(sourceRoot, workspaceRoot, sourceFingerprint, prepared, language, baseEnvironment)
		if err != nil {
			runtime.Close()
			return nil, err
		}
		client, err := startClient(ctx, workspaceRoot, string(language), environment, command, arguments...)
		if err != nil {
			runtime.Close()
			return nil, err
		}
		runtime.clients = append(runtime.clients, client)
		var catalog hostCatalog
		inspectionContext, cancelInspection := context.WithTimeout(ctx, 10*time.Second)
		err = client.request(inspectionContext, "list", nil, &catalog)
		cancelInspection()
		if err != nil {
			runtime.Close()
			return nil, fmt.Errorf("%s tool inspection failed: %w", language, err)
		}
		expected := map[string]bool{}
		for _, source := range inventory.Sources {
			if source.Language == language {
				expected[source.Name] = true
			}
		}
		if len(catalog.Tools) != len(expected) {
			runtime.Close()
			return nil, fmt.Errorf("%s tool host reported an unexpected catalog", language)
		}
		for _, definition := range catalog.Tools {
			if !expected[definition.Name] {
				runtime.Close()
				return nil, fmt.Errorf("%s tool host reported unexpected tool %q", language, definition.Name)
			}
			if _, exists := runtime.tools[definition.Name]; exists {
				runtime.Close()
				return nil, fmt.Errorf("duplicate tool name %q", definition.Name)
			}
			if err := validateDefinition(definition); err != nil {
				runtime.Close()
				return nil, fmt.Errorf("tool %q: %w", definition.Name, err)
			}
			runtime.tools[definition.Name] = hostedTool{definition: definition, client: client}
		}
	}
	return runtime, nil
}

func (runtime *Runtime) List() []Definition {
	definitions := make([]Definition, 0, len(runtime.tools))
	for _, tool := range runtime.tools {
		definitions = append(definitions, tool.definition)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions
}

func (runtime *Runtime) Call(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	tool, exists := runtime.tools[name]
	if !exists {
		return nil, fmt.Errorf("unknown managed tool %q", name)
	}
	if len(arguments) == 0 || len(arguments) > maxHostCallLine || !json.Valid(arguments) {
		return nil, errors.New("tool arguments must be bounded JSON")
	}
	var result hostCall
	if err := tool.client.request(ctx, "call", map[string]any{"name": name, "arguments": json.RawMessage(arguments)}, &result); err != nil {
		return nil, err
	}
	if len(result.Output) == 0 || len(result.Output) > maxHostCallLine || !json.Valid(result.Output) {
		return nil, errors.New("tool host returned invalid or oversized output")
	}
	return result.Output, nil
}

func (runtime *Runtime) Close() {
	for _, client := range runtime.clients {
		client.close()
	}
	runtime.clients = nil
}

func hostCommand(sourceRoot, workspaceRoot, sourceFingerprint string, prepared preparedRuntime, language Language, baseEnvironment []string) (string, []string, []string, error) {
	cache := cacheRelative(sourceFingerprint)
	switch language {
	case TypeScript:
		host := cache + "/typescript.ts"
		if err := verifyCachedSource(workspaceRoot, host, typescriptHost); err != nil {
			return "", nil, nil, err
		}
		deno, err := preparedExecutable(prepared.Deno, "deno")
		environment := environmentWith(baseEnvironment, "DENO_DIR", filepath.Join(workspaceRoot, filepath.FromSlash(cache+"/deno-dir")))
		return deno, []string{"run", "--quiet", "--cached-only", "--frozen", "--config", filepath.Join(sourceRoot, "deno.json"), "--allow-read=" + sourceRoot + "," + workspaceRoot, filepath.Join(workspaceRoot, filepath.FromSlash(host)), sourceRoot}, environment, err
	case Python:
		host := cache + "/python.py"
		if err := verifyCachedSource(workspaceRoot, host, pythonHost); err != nil {
			return "", nil, nil, err
		}
		if prepared.Python != "" {
			python, err := preparedExecutable(prepared.Python, "python")
			return python, []string{filepath.Join(workspaceRoot, filepath.FromSlash(host)), sourceRoot}, runtimeEnvironment(baseEnvironment, map[string]string{"PYTHONDONTWRITEBYTECODE": "1"}), err
		}
		uv, err := preparedExecutable(prepared.UV, "uv")
		environment := runtimeEnvironment(baseEnvironment, map[string]string{"PYTHONDONTWRITEBYTECODE": "1", "UV_PROJECT_ENVIRONMENT": filepath.Join(workspaceRoot, filepath.FromSlash(cache+"/python-venv"))})
		return uv, []string{"run", "--locked", "--no-sync", "--project", sourceRoot, "python", filepath.Join(workspaceRoot, filepath.FromSlash(host)), sourceRoot}, environment, err
	case Go:
		binary := filepath.Join(workspaceRoot, filepath.FromSlash(cache+"/go/host"))
		info, err := os.Lstat(binary)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return "", nil, nil, errors.New("go tool runtime is missing; run hctl apply")
		}
		return binary, nil, runtimeEnvironment(baseEnvironment, nil), nil
	default:
		return "", nil, nil, errors.New("unsupported tool language")
	}
}

func runtimeEnvironment(base []string, values map[string]string) []string {
	if base == nil {
		return secureenv.WithValues(values)
	}
	return secureenv.Replace(base, values)
}

func readPreparedRuntime(workspaceRoot, sourceFingerprint string) (preparedRuntime, error) {
	path := cacheRelative(sourceFingerprint) + "/executables.json"
	data, _, exists, err := rootfs.ReadOptional(workspaceRoot, path, 4096)
	if err != nil || !exists {
		return preparedRuntime{}, errors.New("tool runtime is missing or changed; run hctl apply")
	}
	var prepared preparedRuntime
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&prepared); err != nil {
		return preparedRuntime{}, errors.New("tool runtime is missing or changed; run hctl apply")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return preparedRuntime{}, errors.New("tool runtime is missing or changed; run hctl apply")
	}
	return prepared, nil
}

func preparedExecutable(path, name string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("prepared %s executable is missing; run hctl apply", name)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("prepared %s executable is missing; run hctl apply", name)
	}
	return path, nil
}

func verifyCachedSource(root, relative string, expected []byte) error {
	data, _, exists, err := rootfs.ReadOptional(root, relative, int64(len(expected)+1))
	if err != nil || !exists || !bytes.Equal(data, expected) {
		return errors.New("tool runtime is missing or changed; run hctl apply")
	}
	return nil
}

func validateDefinition(definition Definition) error {
	if !portableName.MatchString(definition.Name) || definition.Description == "" || len(definition.Description) > maxDescription || !utf8.ValidString(definition.Description) {
		return errors.New("name or description is invalid")
	}
	for _, schema := range []json.RawMessage{definition.InputSchema, definition.OutputSchema} {
		if len(schema) == 0 || len(schema) > maxHostCallLine || !json.Valid(schema) {
			return errors.New("schema is invalid or oversized")
		}
		var object map[string]any
		if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
			return errors.New("input and output schemas must describe objects")
		}
	}
	return nil
}

type client struct {
	name    string
	command *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Reader
	encoder *json.Encoder
	mu      sync.Mutex
	nextID  int
	done    bool
}

func startClient(ctx context.Context, directory, name string, environment []string, executable string, arguments ...string) (*client, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	command.Env = environment
	input, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("cannot open %s tool host input", name)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cannot open %s tool host output", name)
	}
	stderr := &boundedBuffer{remaining: 16 << 10}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %s tool host", name)
	}
	reader := bufio.NewReaderSize(output, 4096)
	return &client{name: name, command: command, input: input, output: reader, encoder: json.NewEncoder(input)}, nil
}

func readHostLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, min(limit, 4096))
	for {
		fragment, err := reader.ReadSlice('\n')
		switch {
		case err == nil:
			fragment = bytes.TrimSuffix(fragment, []byte{'\n'})
			fragment = bytes.TrimSuffix(fragment, []byte{'\r'})
			if len(line)+len(fragment) > limit {
				return nil, errHostResponseTooLarge
			}
			return append(line, fragment...), nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(line)+len(fragment) > limit {
				return nil, errHostResponseTooLarge
			}
			line = append(line, fragment...)
		case errors.Is(err, io.EOF):
			fragment = bytes.TrimSuffix(fragment, []byte{'\r'})
			if len(line)+len(fragment) > limit {
				return nil, errHostResponseTooLarge
			}
			if len(line)+len(fragment) == 0 {
				return nil, io.EOF
			}
			return append(line, fragment...), nil
		default:
			return nil, err
		}
	}
}

func (client *client) request(ctx context.Context, method string, params any, target any) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.done {
		return errors.New("tool host is not running")
	}
	client.nextID++
	id := fmt.Sprintf("%s-%d", client.name, client.nextID)
	if err := client.encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		client.abort()
		return errors.New("cannot write tool host request")
	}
	type scanResult struct {
		line []byte
		err  error
	}
	result := make(chan scanResult, 1)
	limit := maxHostCallLine
	if method == "list" {
		limit = maxHostCatalogLine
	}
	go func() {
		line, err := readHostLine(client.output, limit)
		result <- scanResult{line: line, err: err}
	}()
	var scanned scanResult
	select {
	case scanned = <-result:
	case <-ctx.Done():
		client.abort()
		return errors.New("tool call exceeded its deadline; language host terminated")
	}
	if scanned.line == nil {
		client.abort()
		if errors.Is(scanned.err, errHostResponseTooLarge) {
			return errHostResponseTooLarge
		}
		if scanned.err != nil {
			return errors.New("tool host output exceeded the bounded line size")
		}
		return fmt.Errorf("%s tool host exited without a response", client.name)
	}
	var response struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(scanned.line, &response); err != nil || response.ID != id {
		client.abort()
		return errors.New("tool host returned an invalid response")
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if target != nil {
		if err := json.Unmarshal(response.Result, target); err != nil {
			return errors.New("tool host returned an invalid result")
		}
	}
	return nil
}

func (client *client) close() {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.done {
		return
	}
	client.done = true
	_ = client.input.Close()
	waited := make(chan struct{})
	go func() {
		_ = client.command.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(shutdownWait):
		if client.command.Process != nil {
			_ = client.command.Process.Kill()
		}
		<-waited
	}
}

func (client *client) abort() {
	if client.done {
		return
	}
	client.done = true
	_ = client.input.Close()
	if client.command.Process != nil {
		_ = client.command.Process.Kill()
	}
	_ = client.command.Wait()
}
