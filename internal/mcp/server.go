package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"hctl/internal/connection/github"
	"hctl/internal/friction"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/interaction"
	"hctl/internal/project"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

const maxLineBytes = 64 << 10

var portableToolName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func Serve(source, workspace, harnessName string, input io.Reader, output, audit io.Writer) error {
	return serve(source, workspace, harnessName, input, output, audit, github.NewClient(nil))
}

func serve(source, workspace, harnessName string, input io.Reader, output, audit io.Writer, githubClient *github.Client) error {
	return serveWithRuntime(source, workspace, harnessName, input, output, audit, githubClient, func(ctx context.Context, p *project.Project) (managedRuntime, error) {
		return tool.Open(ctx, p.SourceRoot, p.WorkspaceRoot, p.SourceFingerprint, p.Tools)
	})
}

type managedRuntime interface {
	List() []tool.Definition
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
	Close()
}

type runtimeOpener func(context.Context, *project.Project) (managedRuntime, error)

type frictionRecorder interface {
	Record(*project.Project, string) bool
}

func serveWithRuntime(source, workspace, harnessName string, input io.Reader, output, audit io.Writer, githubClient *github.Client, openRuntime runtimeOpener) error {
	p, err := project.Load(source, harnessName, workspace)
	if err != nil {
		return err
	}
	if err := setup.Verify(p); err != nil {
		return err
	}
	if os.Getenv("HCTL_EXECUTION_POLICY") != string(harness.PolicyReadOnly) {
		opened, openErr := openRuntime(context.Background(), p)
		if openErr != nil {
			return openErr
		}
		defer opened.Close()
		return serveRequestsWithFriction(p, opened, githubClient, friction.NewDefault(), input, output, audit)
	}
	return serveRequestsWithFriction(p, nil, githubClient, friction.NewDefault(), input, output, audit)
}

func serveRequestsWithFriction(p *project.Project, runtime managedRuntime, githubClient *github.Client, recorder frictionRecorder, input io.Reader, output, audit io.Writer) error {
	return serveRequestsWithInputAndFriction(p, runtime, githubClient, nil, recorder, input, output, audit)
}

// requestInputRuntime is intentionally process-local. Production MCP children
// do not receive one until a channel root, harness continuation strategy, and
// responder have established a trusted bridge to the dispatcher.
type requestInputRuntime interface {
	HarnessStrategyAvailable() bool
	ResponderAvailable() bool
	Capabilities() interaction.Capabilities
	Request(context.Context, string, interaction.Request) (harness.RequestInputToolResult, error)
}

func requestInputAvailable(requests requestInputRuntime) bool {
	return requests != nil && requests.HarnessStrategyAvailable() && requests.ResponderAvailable() ||
		claude.DeferredBrokerAvailable(os.Getenv(claude.DeferredBrokerEnv))
}

func serveRequestsWithInput(p *project.Project, runtime managedRuntime, githubClient *github.Client, requests requestInputRuntime, input io.Reader, output, audit io.Writer) error {
	return serveRequestsWithInputAndFriction(p, runtime, githubClient, requests, friction.NewDefault(), input, output, audit)
}

func serveRequestsWithInputAndFriction(p *project.Project, runtime managedRuntime, githubClient *github.Client, requests requestInputRuntime, recorder frictionRecorder, input io.Reader, output, audit io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxLineBytes)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || request.JSONRPC != "2.0" || request.Method == "" {
			writeError(encoder, nil, -32600, "invalid request")
			continue
		}
		if request.Method == "notifications/initialized" || len(request.ID) == 0 {
			continue
		}
		switch request.Method {
		case "initialize":
			writeResult(encoder, request.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": "hctl-managed", "version": "0.1.0-dev"},
			})
		case "tools/list":
			tools := []any{map[string]any{
				"name":         "echo",
				"description":  "Return bounded text through the managed boundary.",
				"inputSchema":  map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"text": map[string]any{"type": "string", "maxLength": p.MaxToolInput}}, "required": []string{"text"}},
				"outputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
				"annotations":  map[string]any{"readOnlyHint": true, "idempotentHint": true, "openWorldHint": false},
			}}
			if p.FrictionNotes {
				tools = append(tools, map[string]any{
					"name":         "record-friction",
					"description":  "Retain one concise friction note in private local hctl state for later human review. Use only after completing the primary task when concrete material friction could help improve the agent project or its hctl integration. This is not telemetry and is not loaded into future sessions.",
					"inputSchema":  map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"note": map[string]any{"type": "string", "maxLength": friction.MaxNoteBytes}}, "required": []string{"note"}},
					"outputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"recorded": map[string]any{"type": "boolean"}}, "required": []string{"recorded"}},
					"annotations":  map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
				})
			}
			if p.GitHubConnection != nil {
				tools = append(tools, github.Definitions(p.GitHubConnection.Description)...)
			}
			if runtime != nil {
				for _, definition := range runtime.List() {
					tools = append(tools, definition)
				}
			}
			if requestInputAvailable(requests) {
				tools = append(tools, requestInputDefinition())
			}
			writeResult(encoder, request.ID, map[string]any{"tools": tools})
		case "tools/call":
			result, requestID, toolName, err := callManagedWithInputAndFriction(p, runtime, githubClient, requests, recorder, request.ID, request.Params, audit)
			if err != nil {
				if auditErr := writeAudit(audit, p.AgentID, toolName, requestID, "failed"); auditErr != nil {
					return auditErr
				}
				writeResult(encoder, request.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": err.Error()}}, "isError": true})
				continue
			}
			if err := writeAudit(audit, p.AgentID, toolName, requestID, "completed"); err != nil {
				return err
			}
			writeResult(encoder, request.ID, result)
		default:
			writeError(encoder, request.ID, -32601, "method not found")
		}
	}
	if scanner.Err() != nil {
		return errors.New("input from MCP exceeded the bounded line size")
	}
	return nil
}

func callManaged(p *project.Project, runtime managedRuntime, githubClient *github.Client, id, params json.RawMessage, audit io.Writer) (map[string]any, string, string, error) {
	return callManagedWithInputAndFriction(p, runtime, githubClient, nil, friction.NewDefault(), id, params, audit)
}

func callManagedWithInput(p *project.Project, runtime managedRuntime, githubClient *github.Client, requests requestInputRuntime, id, params json.RawMessage, audit io.Writer) (map[string]any, string, string, error) {
	return callManagedWithInputAndFriction(p, runtime, githubClient, requests, friction.NewDefault(), id, params, audit)
}

func callManagedWithInputAndFriction(p *project.Project, runtime managedRuntime, githubClient *github.Client, requests requestInputRuntime, recorder frictionRecorder, id, params json.RawMessage, audit io.Writer) (map[string]any, string, string, error) {
	requestID := managedRequestID(id, nil)
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta"`
	}
	if err := decodeStrict(params, &call); err != nil {
		return nil, requestID, "unknown", errors.New("invalid managed tool call")
	}
	githubTool := p.GitHubConnection != nil && github.IsTool(call.Name)
	requestInput := call.Name == requestInputToolName
	if requestInput {
		// Semantic request bytes must not influence audit correlation.
		requestID = managedRequestID(id, []byte(requestInputToolName))
	} else {
		requestID = managedRequestID(id, params)
	}
	if !portableToolName.MatchString(call.Name) && !githubTool && !requestInput {
		return nil, requestID, "unknown", errors.New("invalid managed tool call")
	}
	if err := writeAudit(audit, p.AgentID, call.Name, requestID, "requested"); err != nil {
		return nil, requestID, call.Name, err
	}
	if requestInput {
		if brokerPath := os.Getenv(claude.DeferredBrokerEnv); brokerPath != "" {
			answer, err := claude.RequestDeferredBrokerResult(brokerPath, call.Arguments)
			if err != nil {
				return nil, requestID, call.Name, errors.New("deferred interactive input result was rejected")
			}
			if err := writeAudit(audit, p.AgentID, call.Name, requestID, "authorized"); err != nil {
				return nil, requestID, call.Name, err
			}
			encoded, err := json.Marshal(answer)
			if err != nil {
				return nil, requestID, call.Name, errors.New("cannot encode deferred interactive input result")
			}
			return map[string]any{
				"content": []any{map[string]any{"type": "text", "text": string(encoded)}}, "structuredContent": map[string]any{"answer": answer}, "isError": false,
			}, requestID, call.Name, nil
		}
		if !requestInputAvailable(requests) {
			return nil, requestID, call.Name, errors.New("interactive input is unavailable in this session")
		}
		request, err := interaction.DecodeRequest(call.Arguments)
		if err != nil {
			return nil, requestID, call.Name, errors.New("interactive input request does not match the managed contract")
		}
		resolution, err := interaction.Resolve(request, requests.Capabilities())
		if err != nil {
			return nil, requestID, call.Name, errors.New("interactive input request has no available rendering or fallback")
		}
		if err := writeAudit(audit, p.AgentID, call.Name, requestID, "authorized"); err != nil {
			return nil, requestID, call.Name, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = resolution // The dispatcher recomputes this from trusted capabilities.
		disposition, err := requests.Request(ctx, requestID, request)
		if err != nil || !disposition.Disposition.Valid() {
			return nil, requestID, call.Name, errors.New("interactive input request was rejected")
		}
		structured := map[string]any{"disposition": string(disposition.Disposition)}
		return map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": string(disposition.Disposition)}},
			"structuredContent": structured,
			"isError":           false,
		}, requestID, call.Name, nil
	}
	if call.Name == "record-friction" {
		if !p.FrictionNotes {
			return nil, requestID, call.Name, errors.New("record-friction is not enabled for this agent")
		}
		var arguments struct {
			Note string `json:"note"`
		}
		if err := decodeStrict(call.Arguments, &arguments); err != nil || !utf8.ValidString(arguments.Note) || strings.TrimSpace(arguments.Note) == "" || len([]byte(arguments.Note)) > friction.MaxNoteBytes {
			return nil, requestID, call.Name, errors.New("friction note must be non-empty and within the configured byte limit")
		}
		if err := writeAudit(audit, p.AgentID, call.Name, requestID, "authorized"); err != nil {
			return nil, requestID, call.Name, err
		}
		recorded := recorder != nil && recorder.Record(p, arguments.Note)
		structured := map[string]any{"recorded": recorded}
		encoded, _ := json.Marshal(structured)
		return map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": string(encoded)}},
			"structuredContent": structured,
			"isError":           false,
		}, requestID, call.Name, nil
	}
	if call.Name != "echo" {
		if err := writeAudit(audit, p.AgentID, call.Name, requestID, "authorized"); err != nil {
			return nil, requestID, call.Name, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var output []byte
		var err error
		if githubTool {
			output, err = githubClient.Call(ctx, call.Name, call.Arguments)
		} else if runtime == nil {
			return nil, requestID, call.Name, errors.New("managed authored tools are unavailable in a read-only channel session")
		} else {
			output, err = runtime.Call(ctx, call.Name, call.Arguments)
		}
		if err != nil {
			return nil, requestID, call.Name, err
		}
		var structured map[string]any
		if err := json.Unmarshal(output, &structured); err != nil {
			return nil, requestID, call.Name, errors.New("managed tool returned invalid structured output")
		}
		return map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": string(output)}},
			"structuredContent": structured,
			"isError":           false,
		}, requestID, call.Name, nil
	}
	var arguments struct {
		Text string `json:"text"`
	}
	if err := decodeStrict(call.Arguments, &arguments); err != nil || arguments.Text == "" || !utf8.ValidString(arguments.Text) || len([]byte(arguments.Text)) > p.MaxToolInput {
		return nil, requestID, call.Name, errors.New("echo text must be non-empty and within the configured byte limit")
	}
	if err := writeAudit(audit, p.AgentID, call.Name, requestID, "authorized"); err != nil {
		return nil, requestID, call.Name, err
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": arguments.Text}},
		"structuredContent": map[string]any{"text": arguments.Text},
		"isError":           false,
	}, requestID, call.Name, nil
}

func managedRequestID(id, correlation []byte) string {
	requestHash := sha256.Sum256(append(append([]byte{}, id...), correlation...))
	return hex.EncodeToString(requestHash[:8])
}

func writeAudit(audit io.Writer, agent, toolName, requestID, outcome string) error {
	if _, err := fmt.Fprintf(audit, "managed agent=%s tool=%s request=%s outcome=%s\n", agent, toolName, requestID, outcome); err != nil {
		return errors.New("cannot write managed tool audit")
	}
	return nil
}

func writeResult(encoder *json.Encoder, id json.RawMessage, result any) {
	_ = encoder.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result})
}

func writeError(encoder *json.Encoder, id json.RawMessage, code int, message string) {
	_ = encoder.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   any             `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: map[string]any{"code": code, "message": message}})
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
