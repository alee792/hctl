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

	"hctl/internal/friction"
	"hctl/internal/harness"
	"hctl/internal/harness/claude"
	"hctl/internal/project"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

const maxLineBytes = 64 << 10

var portableToolName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func Serve(source, workspace, harnessName string, input io.Reader, output, audit io.Writer) error {
	return serve(source, workspace, harnessName, input, output, audit)
}

func serve(source, workspace, harnessName string, input io.Reader, output, audit io.Writer) error {
	return serveWithRuntime(source, workspace, harnessName, input, output, audit, func(ctx context.Context, p *project.Project) (managedRuntime, error) {
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

func serveWithRuntime(source, workspace, harnessName string, input io.Reader, output, audit io.Writer, openRuntime runtimeOpener) error {
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
		return serveRequestsWithFriction(p, opened, friction.NewDefault(), input, output, audit)
	}
	return serveRequestsWithFriction(p, nil, friction.NewDefault(), input, output, audit)
}

// requestInputAvailable reports whether this managed child may advertise and
// serve interactive input. The only production owner is the Claude deferred
// broker started by the session that launched this process.
func requestInputAvailable() bool {
	return claude.DeferredBrokerAvailable(os.Getenv(claude.DeferredBrokerEnv))
}

func serveRequestsWithFriction(p *project.Project, runtime managedRuntime, recorder frictionRecorder, input io.Reader, output, audit io.Writer) error {
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
			if runtime != nil {
				for _, definition := range runtime.List() {
					tools = append(tools, definition)
				}
			}
			if requestInputAvailable() {
				tools = append(tools, requestInputDefinition())
			}
			writeResult(encoder, request.ID, map[string]any{"tools": tools})
		case "tools/call":
			result, requestID, toolName, err := callManagedWithFriction(p, runtime, recorder, request.ID, request.Params, audit)
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

func callManaged(p *project.Project, runtime managedRuntime, id, params json.RawMessage, audit io.Writer) (map[string]any, string, string, error) {
	return callManagedWithFriction(p, runtime, friction.NewDefault(), id, params, audit)
}

// managedCall carries one decoded tool call to its handler. The handler owns
// every audit line after "requested" and returns only the JSON-RPC result.
type managedCall struct {
	project   *project.Project
	runtime   managedRuntime
	recorder  frictionRecorder
	name      string
	arguments json.RawMessage
	requestID string
	audit     io.Writer
}

type managedHandler func(managedCall) (map[string]any, error)

// managedHandlers routes the fixed managed tools. Any other portable name is
// an authored tool served by the managed runtime.
var managedHandlers = map[string]managedHandler{
	requestInputToolName: callRequestInput,
	"record-friction":    callRecordFriction,
	"echo":               callEcho,
}

func callManagedWithFriction(p *project.Project, runtime managedRuntime, recorder frictionRecorder, id, params json.RawMessage, audit io.Writer) (map[string]any, string, string, error) {
	requestID := managedRequestID(id, nil)
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta"`
	}
	if err := decodeStrict(params, &call); err != nil {
		return nil, requestID, "unknown", errors.New("invalid managed tool call")
	}
	requestInput := call.Name == requestInputToolName
	if requestInput {
		// Semantic request bytes must not influence audit correlation.
		requestID = managedRequestID(id, []byte(requestInputToolName))
	} else {
		requestID = managedRequestID(id, params)
	}
	if !portableToolName.MatchString(call.Name) && !requestInput {
		return nil, requestID, "unknown", errors.New("invalid managed tool call")
	}
	if err := writeAudit(audit, p.AgentID, call.Name, requestID, "requested"); err != nil {
		return nil, requestID, call.Name, err
	}
	handler, known := managedHandlers[call.Name]
	if !known {
		handler = callAuthored
	}
	result, err := handler(managedCall{
		project: p, runtime: runtime, recorder: recorder,
		name: call.Name, arguments: call.Arguments, requestID: requestID, audit: audit,
	})
	if err != nil {
		return nil, requestID, call.Name, err
	}
	return result, requestID, call.Name, nil
}

func (c managedCall) authorize() error {
	return writeAudit(c.audit, c.project.AgentID, c.name, c.requestID, "authorized")
}

func callRequestInput(call managedCall) (map[string]any, error) {
	brokerPath := os.Getenv(claude.DeferredBrokerEnv)
	if brokerPath == "" {
		return nil, errors.New("interactive input is unavailable in this session")
	}
	answer, err := claude.RequestDeferredBrokerResult(brokerPath, call.arguments)
	if err != nil {
		return nil, errors.New("deferred interactive input result was rejected")
	}
	if err := call.authorize(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(answer)
	if err != nil {
		return nil, errors.New("cannot encode deferred interactive input result")
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(encoded)}}, "structuredContent": map[string]any{"answer": answer}, "isError": false,
	}, nil
}

func callRecordFriction(call managedCall) (map[string]any, error) {
	if !call.project.FrictionNotes {
		return nil, errors.New("record-friction is not enabled for this agent")
	}
	var arguments struct {
		Note string `json:"note"`
	}
	if err := decodeStrict(call.arguments, &arguments); err != nil || !utf8.ValidString(arguments.Note) || strings.TrimSpace(arguments.Note) == "" || len([]byte(arguments.Note)) > friction.MaxNoteBytes {
		return nil, errors.New("friction note must be non-empty and within the configured byte limit")
	}
	if err := call.authorize(); err != nil {
		return nil, err
	}
	recorded := call.recorder != nil && call.recorder.Record(call.project, arguments.Note)
	structured := map[string]any{"recorded": recorded}
	encoded, _ := json.Marshal(structured)
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(encoded)}},
		"structuredContent": structured,
		"isError":           false,
	}, nil
}

func callAuthored(call managedCall) (map[string]any, error) {
	if err := call.authorize(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if call.runtime == nil {
		return nil, errors.New("managed authored tools are unavailable in a read-only channel session")
	}
	output, err := call.runtime.Call(ctx, call.name, call.arguments)
	if err != nil {
		return nil, err
	}
	var structured map[string]any
	if err := json.Unmarshal(output, &structured); err != nil {
		return nil, errors.New("managed tool returned invalid structured output")
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(output)}},
		"structuredContent": structured,
		"isError":           false,
	}, nil
}

func callEcho(call managedCall) (map[string]any, error) {
	var arguments struct {
		Text string `json:"text"`
	}
	if err := decodeStrict(call.arguments, &arguments); err != nil || arguments.Text == "" || !utf8.ValidString(arguments.Text) || len([]byte(arguments.Text)) > call.project.MaxToolInput {
		return nil, errors.New("echo text must be non-empty and within the configured byte limit")
	}
	if err := call.authorize(); err != nil {
		return nil, err
	}
	return map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": arguments.Text}},
		"structuredContent": map[string]any{"text": arguments.Text},
		"isError":           false,
	}, nil
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
