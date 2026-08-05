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
	"regexp"
	"time"
	"unicode/utf8"

	"hctl/internal/project"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

const maxLineBytes = 64 << 10

var portableToolName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func Serve(source, workspace, harness string, input io.Reader, output, audit io.Writer) error {
	p, err := project.Load(source, harness, workspace)
	if err != nil {
		return err
	}
	if err := setup.Verify(p); err != nil {
		return err
	}
	runtime, err := tool.Open(context.Background(), p.SourceRoot, p.WorkspaceRoot, p.SourceFingerprint, p.Tools)
	if err != nil {
		return err
	}
	defer runtime.Close()
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
			for _, definition := range runtime.List() {
				tools = append(tools, definition)
			}
			writeResult(encoder, request.ID, map[string]any{"tools": tools})
		case "tools/call":
			result, requestID, toolName, err := callManaged(p, runtime, request.ID, request.Params, audit)
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

func callManaged(p *project.Project, runtime *tool.Runtime, id, params json.RawMessage, audit io.Writer) (map[string]any, string, string, error) {
	requestHash := sha256.Sum256(append(append([]byte{}, id...), params...))
	requestID := hex.EncodeToString(requestHash[:8])
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeStrict(params, &call); err != nil || !portableToolName.MatchString(call.Name) {
		return nil, requestID, "unknown", errors.New("invalid managed tool call")
	}
	if err := writeAudit(audit, p.AgentID, call.Name, requestID, "requested"); err != nil {
		return nil, requestID, call.Name, err
	}
	if call.Name != "echo" {
		if err := writeAudit(audit, p.AgentID, call.Name, requestID, "authorized"); err != nil {
			return nil, requestID, call.Name, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		output, err := runtime.Call(ctx, call.Name, call.Arguments)
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
