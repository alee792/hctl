package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hctl/internal/harness"
	"hctl/internal/secureenv"
)

type Driver struct{ executable string }

func New(executable string) *Driver { return &Driver{executable: executable} }

func (d *Driver) Name() string       { return "codex" }
func (d *Driver) Executable() string { return d.executable }

func (d *Driver) Verify(ctx context.Context) error {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(versionCtx, d.executable, "--version")
	command.Env = secureenv.Child()
	output, err := command.Output()
	if err != nil || len(output) > 4096 || !regexp.MustCompile(`\d+\.\d+\.\d+`).Match(output) {
		return errors.New("codex executable did not provide a compatible semantic version")
	}
	if !bytes.Contains(bytes.ToLower(output), []byte("codex")) {
		return errors.New("configured Codex executable did not identify as Codex CLI")
	}
	return nil
}

func (d *Driver) Open(ctx context.Context, request harness.OpenRequest) (harness.Session, error) {
	if request.Policy != harness.PolicyDefault && request.Policy != harness.PolicyReadOnly && request.Policy != harness.PolicyWorkspaceWrite {
		return nil, errors.New("codex does not support the requested execution policy")
	}
	process, err := harness.StartProcessWithPolicy(ctx, request.Root, d.executable, request.Policy, "app-server", "--stdio")
	if err != nil {
		return nil, err
	}
	client := &client{encoder: json.NewEncoder(process.Input()), process: process}
	result, _, err := client.request(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "hctl", "title": "hctl run", "version": "0.1.0-dev"}})
	if err != nil || len(result) == 0 {
		process.Abort()
		return nil, errors.New("codex initialize handshake failed")
	}
	if err := client.notify("initialized"); err != nil {
		process.Abort()
		return nil, errors.New("cannot complete Codex initialize handshake")
	}
	method := "thread/start"
	params := map[string]any{"cwd": request.Root}
	applyPolicy(params, request.Policy)
	if request.ResumeID != "" {
		method = "thread/resume"
		params = map[string]any{"threadId": request.ResumeID, "cwd": request.Root}
		applyPolicy(params, request.Policy)
	}
	threadResult, _, err := client.request(2, method, params)
	if err != nil {
		process.Abort()
		return nil, errors.New("codex thread start or resume failed")
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Sandbox struct {
			Type string `json:"type"`
		} `json:"sandbox"`
		ApprovalPolicy string `json:"approvalPolicy"`
	}
	if err := json.Unmarshal(threadResult, &response); err != nil || response.Thread.ID == "" {
		process.Abort()
		return nil, errors.New("codex returned an invalid thread response")
	}
	if request.ResumeID != "" && response.Thread.ID != request.ResumeID {
		process.Abort()
		return nil, errors.New("codex resumed an unexpected thread")
	}
	if err := validateEffectivePolicy(request.Policy, response.Sandbox.Type, response.ApprovalPolicy); err != nil {
		process.Abort()
		return nil, err
	}
	return &session{process: process, client: client, sessionID: response.Thread.ID, resumed: request.ResumeID != "", requestID: 3}, nil
}

func applyPolicy(params map[string]any, policy harness.ExecutionPolicy) {
	switch policy {
	case harness.PolicyReadOnly:
		params["sandbox"] = "read-only"
		params["approvalPolicy"] = "never"
	case harness.PolicyWorkspaceWrite:
		params["sandbox"] = "workspace-write"
		params["approvalPolicy"] = "never"
	}
}

func validateEffectivePolicy(policy harness.ExecutionPolicy, sandboxType, approvalPolicy string) error {
	if policy == harness.PolicyReadOnly && (sandboxType != "readOnly" || approvalPolicy != "never") {
		return errors.New("codex did not enforce the requested read-only policy")
	}
	if policy == harness.PolicyWorkspaceWrite && (sandboxType != "workspaceWrite" || approvalPolicy != "never") {
		return errors.New("codex did not enforce the requested workspace-write policy")
	}
	return nil
}

type session struct {
	process   *harness.Process
	client    *client
	sessionID string
	resumed   bool
	requestID int
}

func (s *session) InitialEvents() []harness.Event {
	typeName := "session.started"
	if s.resumed {
		typeName = "session.resumed"
	}
	return []harness.Event{
		{Type: "driver.ready", SessionID: s.sessionID, Status: "app-server-v2-jsonl"},
		{Type: typeName, SessionID: s.sessionID},
	}
}

func (s *session) RunTurn(ctx context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	select {
	case <-ctx.Done():
		return harness.TurnResult{}, ctx.Err()
	default:
	}
	result, buffered, err := s.client.request(s.requestID, "turn/start", map[string]any{"threadId": s.sessionID, "input": []any{map[string]any{"type": "text", "text": input.Text}}})
	s.requestID++
	if err != nil {
		return harness.TurnResult{}, errors.New("codex turn/start failed")
	}
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(result, &response); err != nil || response.Turn.ID == "" {
		return harness.TurnResult{}, errors.New("codex returned an invalid turn response")
	}
	turnID := response.Turn.ID
	emit(harness.Event{Type: "turn.started", SessionID: s.sessionID, TurnID: turnID})
	terminal := ""
	for _, message := range buffered {
		terminal = handleEvent(message, s.sessionID, turnID, emit, terminal)
	}
	for terminal == "" {
		message, err := s.client.next()
		if err != nil {
			return harness.TurnResult{}, err
		}
		terminal = handleEvent(message, s.sessionID, turnID, emit, terminal)
	}
	return harness.TurnResult{SessionID: s.sessionID, TurnID: turnID, Status: terminal}, nil
}

func (s *session) Close() error { return s.process.Finish() }
func (s *session) Abort()       { s.process.Abort() }

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type client struct {
	encoder *json.Encoder
	process *harness.Process
}

func (c *client) request(id int, method string, params any) (json.RawMessage, []rpcEnvelope, error) {
	request := struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: id, Method: method, Params: params}
	if err := c.encoder.Encode(request); err != nil {
		return nil, nil, errors.New("cannot write Codex request")
	}
	pending := []rpcEnvelope{}
	for c.process.Scan() {
		message, err := decodeRPC(c.process.Bytes())
		if err != nil {
			return nil, nil, err
		}
		if message.Method != "" {
			c.decline(message)
			pending = append(pending, message)
			continue
		}
		if strings.TrimSpace(string(message.ID)) != strconv.Itoa(id) {
			continue
		}
		if message.Error != nil {
			return nil, pending, errors.New("codex rejected a protocol request")
		}
		return message.Result, pending, nil
	}
	if err := c.process.ScanError(); err != nil {
		return nil, nil, err
	}
	return nil, nil, errors.New("codex process ended before a protocol response")
}

func (c *client) notify(method string) error {
	return c.encoder.Encode(struct {
		Method string `json:"method"`
	}{Method: method})
}

func (c *client) next() (rpcEnvelope, error) {
	for c.process.Scan() {
		message, err := decodeRPC(c.process.Bytes())
		if err != nil {
			return rpcEnvelope{}, err
		}
		if message.Method == "" {
			continue
		}
		c.decline(message)
		return message, nil
	}
	if err := c.process.ScanError(); err != nil {
		return rpcEnvelope{}, err
	}
	return rpcEnvelope{}, errors.New("codex process ended before a terminal event")
}

func (c *client) decline(message rpcEnvelope) {
	if len(message.ID) == 0 {
		return
	}
	_ = c.encoder.Encode(struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{ID: message.ID, Result: map[string]any{"decision": "decline"}})
}

func decodeRPC(line []byte) (rpcEnvelope, error) {
	var message rpcEnvelope
	if err := json.Unmarshal(line, &message); err != nil {
		return message, errors.New("codex emitted invalid app-server JSONL")
	}
	return message, nil
}

func handleEvent(message rpcEnvelope, sessionID, turnID string, emit func(harness.Event), terminal string) string {
	if len(message.ID) != 0 {
		emit(harness.Event{Type: "human_input.required", SessionID: sessionID, TurnID: turnID, Status: "declined_by_dispatcher"})
		return terminal
	}
	switch message.Method {
	case "item/agentMessage/delta":
		var params struct {
			Delta  string `json:"delta"`
			TurnID string `json:"turnId"`
			ItemID string `json:"itemId"`
		}
		if json.Unmarshal(message.Params, &params) == nil && params.Delta != "" && (params.TurnID == "" || params.TurnID == turnID) {
			emit(harness.Event{Type: "agent.output.delta", SessionID: sessionID, TurnID: turnID, ItemID: params.ItemID, Delta: params.Delta})
		}
	case "turn/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"turn"`
		}
		if json.Unmarshal(message.Params, &params) != nil {
			return "uncertain"
		}
		if (params.ThreadID != "" && params.ThreadID != sessionID) || (params.Turn.ID != "" && params.Turn.ID != turnID) {
			return terminal
		}
		switch params.Turn.Status {
		case "completed":
			return "completed"
		case "failed":
			return "failed"
		case "interrupted":
			return "cancelled"
		default:
			return "uncertain"
		}
	}
	return terminal
}
