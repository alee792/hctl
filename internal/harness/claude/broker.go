package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"hctl/internal/harness"
	"hctl/internal/interaction"
)

type brokerRequest struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value,omitempty"`
}

type brokerResponse struct {
	Available bool                `json:"available,omitempty"`
	Hook      json.RawMessage     `json:"hook,omitempty"`
	Answer    *interaction.Answer `json:"answer,omitempty"`
	Error     bool                `json:"error,omitempty"`
}

type deferredBroker struct {
	directory string
	path      string
	listener  net.Listener
	resume    *deferredResumeEnvelope

	mu            sync.Mutex
	deferReceipts map[string]struct{}
	hookAttempted bool
	hookAllowed   bool
	mcpAttempted  bool
	mcpDelivered  bool
	done          chan struct{}
}

func startDeferredBroker(resume *harness.DeferredToolResume) (*deferredBroker, error) {
	directory, err := os.MkdirTemp("", "hctl-claude-deferred-")
	if err != nil {
		return nil, errors.New("cannot create claude deferred broker directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.Remove(directory)
		return nil, errors.New("cannot protect claude deferred broker directory")
	}
	var envelope *deferredResumeEnvelope
	if resume != nil {
		validated, err := validateDeferredResume(resume.ToolUseID, resume.InputDigest, resume.UpdatedInput)
		if err != nil || resume.ToolName != ManagedRequestInputTool {
			_ = os.Remove(directory)
			return nil, errors.New("claude deferred resume request is invalid")
		}
		envelope = &validated
	}
	path := filepath.Join(directory, "broker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		_ = os.Remove(directory)
		return nil, errors.New("cannot start claude deferred broker")
	}
	broker := &deferredBroker{directory: directory, path: path, listener: listener, resume: envelope, deferReceipts: map[string]struct{}{}, done: make(chan struct{})}
	go broker.serve()
	return broker, nil
}

func (b *deferredBroker) serve() {
	defer close(b.done)
	for {
		connection, err := b.listener.Accept()
		if err != nil {
			return
		}
		b.handle(connection)
	}
}

func (b *deferredBroker) handle(connection net.Conn) {
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), maxHookInputBytes)
	if scanner.Scan() {
		var request brokerRequest
		if decodeBounded(scanner.Bytes(), maxHookInputBytes, &request) == nil {
			_ = b.writeResponse(connection, request)
			return
		}
	}
	_ = json.NewEncoder(connection).Encode(brokerResponse{Error: true})
}

type brokerCommit struct {
	deferReceipt string
	hookAllowed  bool
	mcpDelivered bool
}

func (b *deferredBroker) writeResponse(output io.Writer, request brokerRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	response, commit := b.responseLocked(request)
	if err := json.NewEncoder(output).Encode(response); err != nil {
		return err
	}
	if commit.deferReceipt != "" {
		b.deferReceipts[commit.deferReceipt] = struct{}{}
	}
	if commit.hookAllowed {
		b.hookAllowed = true
	}
	if commit.mcpDelivered {
		b.mcpDelivered = true
	}
	return nil
}

func (b *deferredBroker) responseLocked(request brokerRequest) (brokerResponse, brokerCommit) {
	switch request.Kind {
	case "available":
		return brokerResponse{Available: true}, brokerCommit{}
	case "hook":
		hook, outcome := hookResponse(request.Value, b.resume, b.hookAttempted)
		if outcome.allowed || outcome.deferReceipt != "" {
			b.hookAttempted = true
		}
		return brokerResponse{Hook: hook, Error: len(hook) == 0}, brokerCommit{deferReceipt: outcome.deferReceipt, hookAllowed: outcome.allowed}
	case "mcp":
		if b.resume == nil || !b.hookAllowed || b.mcpAttempted {
			return brokerResponse{Error: true}, brokerCommit{}
		}
		answer, err := decodeDeferredToolResult(request.Value, *b.resume)
		if err != nil {
			return brokerResponse{Error: true}, brokerCommit{}
		}
		b.mcpAttempted = true
		return brokerResponse{Answer: &answer}, brokerCommit{mcpDelivered: true}
	default:
		return brokerResponse{Error: true}, brokerCommit{}
	}
}

type hookOutcome struct {
	deferReceipt string
	allowed      bool
}

func hookResponse(data []byte, resume *deferredResumeEnvelope, alreadyAttempted bool) (json.RawMessage, hookOutcome) {
	decision := map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "interactive input request was rejected"}
	var hook hookInput
	if json.Unmarshal(data, &hook) != nil || hook.HookEventName != "PreToolUse" || hook.ToolName != ManagedRequestInputTool || !validToolUseID(hook.ToolUseID) || hook.AgentID != "" {
		encoded, _ := json.Marshal(map[string]any{"hookSpecificOutput": decision})
		return encoded, hookOutcome{}
	}
	digest, err := RequestDigest(hook.ToolInput)
	if err != nil {
		encoded, _ := json.Marshal(map[string]any{"hookSpecificOutput": decision})
		return encoded, hookOutcome{}
	}
	if alreadyAttempted {
		encoded, _ := json.Marshal(map[string]any{"hookSpecificOutput": decision})
		return encoded, hookOutcome{}
	}
	if resume == nil {
		delete(decision, "permissionDecisionReason")
		decision["permissionDecision"] = "defer"
		encoded, _ := json.Marshal(map[string]any{"hookSpecificOutput": decision})
		return encoded, hookOutcome{deferReceipt: deferredReceiptKey(hook.ToolUseID, digest)}
	}
	if resume.ToolUseID != hook.ToolUseID || resume.ToolName != hook.ToolName || resume.InputDigest != digest {
		encoded, _ := json.Marshal(map[string]any{"hookSpecificOutput": decision})
		return encoded, hookOutcome{}
	}
	var updated any
	if json.Unmarshal(resume.UpdatedInput, &updated) != nil {
		encoded, _ := json.Marshal(map[string]any{"hookSpecificOutput": decision})
		return encoded, hookOutcome{}
	}
	delete(decision, "permissionDecisionReason")
	decision["permissionDecision"] = "allow"
	decision["updatedInput"] = updated
	encoded, _ := json.Marshal(map[string]any{"hookSpecificOutput": decision})
	return encoded, hookOutcome{allowed: true}
}

func deferredReceiptKey(toolUseID, digest string) string { return toolUseID + "\x00" + digest }

func (b *deferredBroker) consumeDeferred(toolUseID string, input []byte) bool {
	digest, err := RequestDigest(input)
	if err != nil {
		return false
	}
	key := deferredReceiptKey(toolUseID, digest)
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.deferReceipts[key]; !ok {
		return false
	}
	delete(b.deferReceipts, key)
	return true
}

type resumeBrokerDisposition int

const (
	resumeBrokerIncomplete resumeBrokerDisposition = iota
	resumeBrokerComplete
	resumeBrokerAmbiguous
)

func (b *deferredBroker) resumeDisposition() resumeBrokerDisposition {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.resume != nil && b.hookAllowed && b.mcpDelivered {
		return resumeBrokerComplete
	}
	if b.hookAttempted || b.mcpAttempted {
		return resumeBrokerAmbiguous
	}
	return resumeBrokerIncomplete
}

func (b *deferredBroker) Close() {
	_ = b.listener.Close()
	<-b.done
	_ = os.Remove(b.path)
	_ = os.Remove(b.directory)
}

func brokerRoundTrip(path string, request brokerRequest) (brokerResponse, error) {
	if path == "" || !filepath.IsAbs(path) {
		return brokerResponse{}, errors.New("claude deferred broker is unavailable")
	}
	connection, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return brokerResponse{}, errors.New("claude deferred broker is unavailable")
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return brokerResponse{}, errors.New("cannot request claude deferred broker")
	}
	var response brokerResponse
	decoder := json.NewDecoder(connection)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil || response.Error {
		return brokerResponse{}, errors.New("claude deferred broker rejected the request")
	}
	return response, nil
}

func DeferredBrokerAvailable(path string) bool {
	response, err := brokerRoundTrip(path, brokerRequest{Kind: "available"})
	return err == nil && response.Available
}

func RequestDeferredBrokerResult(path string, arguments []byte) (interaction.Answer, error) {
	response, err := brokerRoundTrip(path, brokerRequest{Kind: "mcp", Value: append([]byte(nil), arguments...)})
	if err != nil || response.Answer == nil {
		return interaction.Answer{}, errors.New("claude deferred broker result is unavailable")
	}
	return *response.Answer, nil
}
