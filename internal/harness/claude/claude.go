package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"hctl/internal/harness"
	"hctl/internal/interaction"
	"hctl/internal/secureenv"
)

type Driver struct {
	executable string
	policyMu   sync.Mutex
	readOnlyOK bool
	writableOK bool
}

func New(executable string) *Driver { return &Driver{executable: executable} }

func (d *Driver) Name() string       { return "claude" }
func (d *Driver) Executable() string { return d.executable }

func (d *Driver) Verify(ctx context.Context) error {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(versionCtx, d.executable, "--version")
	command.Env = secureenv.Child()
	output, err := command.Output()
	if err != nil || len(output) > 4096 || !regexp.MustCompile(`\d+\.\d+\.\d+`).Match(output) {
		return errors.New("claude executable did not provide a compatible semantic version")
	}
	if !bytes.Contains(bytes.ToLower(output), []byte("claude")) {
		return errors.New("configured Claude executable did not identify as Claude Code")
	}
	return nil
}

func (d *Driver) Open(ctx context.Context, request harness.OpenRequest) (harness.Session, error) {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--replay-user-messages"}
	settingsPath := filepath.Join(request.Root, ".claude", "hctl-settings.json")
	if info, err := os.Lstat(settingsPath); err == nil && info.Mode().IsRegular() {
		args = append(args, "--settings", settingsPath)
	}
	if request.Policy == harness.PolicyReadOnly {
		if err := d.verifyMode(ctx, "plan", harness.PolicyReadOnly, &d.readOnlyOK); err != nil {
			return nil, err
		}
		args = append(args, "--permission-mode", "plan")
	} else if request.Policy == harness.PolicyWorkspaceWrite {
		if err := d.verifyMode(ctx, "acceptEdits", harness.PolicyWorkspaceWrite, &d.writableOK); err != nil {
			return nil, err
		}
		args = append(args, "--permission-mode", "acceptEdits")
	} else if request.Policy != harness.PolicyDefault {
		return nil, errors.New("claude does not support the requested execution policy")
	}
	if request.ResumeID != "" {
		args = append(args, "--resume", request.ResumeID)
	}
	environment := map[string]string{}
	deferredResume := false
	var broker *deferredBroker
	var err error
	if request.Deferred != nil {
		if request.ResumeID == "" || !validToolUseID(request.Deferred.ToolUseID) || request.Deferred.ToolName != ManagedRequestInputTool ||
			len(request.Deferred.UpdatedInput) == 0 || len(request.Deferred.UpdatedInput) > interaction.MaxRequestBytes+interaction.MaxAnswerBytes+4096 {
			return nil, errors.New("claude deferred resume request is invalid")
		}
		deferredResume = true
	}
	if request.ManagedRequestInput || request.Deferred != nil {
		broker, err = startDeferredBroker(request.Deferred)
		if err != nil {
			return nil, err
		}
		environment[DeferredBrokerEnv] = broker.path
	}
	process, err := harness.StartProcessWithPolicyAndEnv(ctx, request.Root, d.executable, request.Policy, environment, args...)
	if err != nil {
		if broker != nil {
			broker.Close()
		}
		return nil, err
	}
	return &session{process: process, encoder: json.NewEncoder(process.Input()), sessionID: request.ResumeID, resumed: request.ResumeID != "", deferredResume: deferredResume, broker: broker}, nil
}

func (d *Driver) verifyMode(ctx context.Context, mode string, policy harness.ExecutionPolicy, verified *bool) error {
	d.policyMu.Lock()
	defer d.policyMu.Unlock()
	if *verified {
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(checkCtx, d.executable, "--permission-mode", mode, "--help")
	command.Env = secureenv.With("HCTL_EXECUTION_POLICY", string(policy))
	output, err := command.Output()
	if err != nil || len(output) > 64<<10 || !bytes.Contains(output, []byte("--permission-mode")) || !bytes.Contains(output, []byte(mode)) {
		return fmt.Errorf("claude did not confirm %s permission mode support", mode)
	}
	*verified = true
	return nil
}

type session struct {
	process        *harness.Process
	encoder        *json.Encoder
	sessionID      string
	resumed        bool
	ready          bool
	deferredResume bool
	broker         *deferredBroker
	brokerOnce     sync.Once
}

func (s *session) InitialEvents() []harness.Event {
	return []harness.Event{{Type: "driver.ready", SessionID: s.sessionID, Status: "stream-json"}}
}

func (s *session) RunTurn(ctx context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	select {
	case <-ctx.Done():
		return harness.TurnResult{}, ctx.Err()
	default:
	}
	if !s.deferredResume {
		message := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": input.Text}, "parent_tool_use_id": nil}
		if err := s.encoder.Encode(message); err != nil {
			return harness.TurnResult{}, errors.New("cannot submit Claude input")
		}
	}
	started := false
	terminal := ""
	toolUseCount := 0
	managedToolSeen := false
	for terminal == "" && s.process.Scan() {
		var event map[string]any
		if err := json.Unmarshal(s.process.Bytes(), &event); err != nil {
			return harness.TurnResult{}, errors.New("claude emitted invalid stream JSON")
		}
		sessionID, _ := event["session_id"].(string)
		if sessionID != "" && !s.ready {
			if s.sessionID != "" && s.sessionID != sessionID {
				return harness.TurnResult{}, errors.New("claude resumed an unexpected session")
			}
			s.sessionID = sessionID
			typeName := "session.started"
			if s.resumed {
				typeName = "session.resumed"
			}
			emit(harness.Event{Type: typeName, SessionID: sessionID})
			s.ready = true
		}
		typeName, _ := event["type"].(string)
		if typeName == "system" && event["subtype"] == "init" {
			continue
		}
		if s.ready && !started {
			emit(harness.Event{Type: "turn.started", SessionID: s.sessionID, TurnID: input.ID})
			started = true
		}
		if typeName == "stream_event" {
			if protocolEvent, ok := event["event"].(map[string]any); ok {
				if protocolEvent["type"] == "content_block_start" {
					if block, ok := protocolEvent["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
						toolUseCount++
						if block["name"] == ManagedRequestInputTool {
							managedToolSeen = true
						}
					}
				}
				if delta, ok := protocolEvent["delta"].(map[string]any); ok && delta["type"] == "text_delta" {
					if text, ok := delta["text"].(string); ok && text != "" {
						emit(harness.Event{Type: "agent.output.delta", SessionID: s.sessionID, TurnID: input.ID, Delta: text})
					}
				}
			}
		}
		if typeName == "result" {
			stopReason, _ := event["stop_reason"].(string)
			if stopReason == "tool_deferred_unavailable" {
				return harness.TurnResult{}, ErrDeferredUnavailable
			}
			if stopReason == "tool_deferred" {
				if s.deferredResume {
					return harness.TurnResult{}, ErrDeferredMismatch
				}
				deferred, err := parseDeferredTool(event["deferred_tool_use"])
				if err != nil {
					return harness.TurnResult{}, err
				}
				if s.broker == nil || !s.broker.consumeDeferred(deferred.ID, deferred.Input) {
					return harness.TurnResult{}, ErrDeferredMismatch
				}
				request, err := interaction.DecodeRequest(deferred.Input)
				if err != nil {
					return harness.TurnResult{}, errors.New("claude deferred tool input is invalid")
				}
				reply := make(chan harness.RequestInputAcknowledgement, 1)
				emit(harness.Event{RequestInput: harness.NewDeferredRootRequestInputEvent(deferred.ID, deferred.ID, request, reply)})
				select {
				case acknowledgement := <-reply:
					if !acknowledgement.Accepted {
						return harness.TurnResult{}, errors.New("claude deferred tool request was rejected")
					}
				case <-ctx.Done():
					return harness.TurnResult{}, ctx.Err()
				}
				terminal = "waiting_for_input"
				continue
			}
			if event["deferred_tool_use"] != nil {
				return harness.TurnResult{}, errors.New("claude emitted deferred tool state without a deferred stop")
			}
			if managedToolSeen && toolUseCount != 1 {
				return harness.TurnResult{}, ErrDeferredParallel
			}
			terminal = "failed"
			if event["subtype"] == "success" {
				terminal = "completed"
			}
		}
	}
	if terminal == "" {
		if err := s.process.ScanError(); err != nil {
			return harness.TurnResult{}, err
		}
		if s.deferredResume && !s.ready {
			return harness.TurnResult{}, ErrDeferredSessionLost
		}
		return harness.TurnResult{}, errors.New("claude process ended before a terminal result")
	}
	if !s.ready || s.sessionID == "" {
		return harness.TurnResult{}, errors.New("claude did not provide a resumable session id")
	}
	if s.deferredResume {
		switch s.broker.resumeDisposition() {
		case resumeBrokerComplete:
		case resumeBrokerAmbiguous:
			return harness.TurnResult{}, ErrDeferredDelivery
		default:
			return harness.TurnResult{}, ErrDeferredMismatch
		}
	}
	if !started {
		emit(harness.Event{Type: "turn.started", SessionID: s.sessionID, TurnID: input.ID})
	}
	return harness.TurnResult{SessionID: s.sessionID, TurnID: input.ID, Status: terminal}, nil
}

func (s *session) Close() error {
	err := s.process.Finish()
	s.closeBroker()
	return err
}
func (s *session) Abort() {
	s.process.Abort()
	s.closeBroker()
}

func (s *session) closeBroker() {
	s.brokerOnce.Do(func() {
		if s.broker != nil {
			s.broker.Close()
		}
	})
}

func (d *Driver) String() string {
	return fmt.Sprintf("claude(%s)", strings.TrimSpace(d.executable))
}
