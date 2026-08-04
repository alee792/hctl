package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"hctl/internal/harness"
)

type Driver struct{ executable string }

func New(executable string) *Driver { return &Driver{executable: executable} }

func (d *Driver) Name() string       { return "claude" }
func (d *Driver) Executable() string { return d.executable }

func (d *Driver) Verify(ctx context.Context) error {
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, d.executable, "--version").Output()
	if err != nil || len(output) > 4096 || !regexp.MustCompile(`\d+\.\d+\.\d+`).Match(output) {
		return errors.New("claude executable did not provide a compatible semantic version")
	}
	if !bytes.Contains(bytes.ToLower(output), []byte("claude")) {
		return errors.New("configured Claude executable did not identify as Claude Code")
	}
	return nil
}

func (d *Driver) Open(ctx context.Context, root, resumeID string) (harness.Session, error) {
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--replay-user-messages"}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	process, err := harness.StartProcess(ctx, root, d.executable, args...)
	if err != nil {
		return nil, err
	}
	return &session{process: process, encoder: json.NewEncoder(process.Input()), sessionID: resumeID, resumed: resumeID != ""}, nil
}

type session struct {
	process   *harness.Process
	encoder   *json.Encoder
	sessionID string
	resumed   bool
	ready     bool
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
	message := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": input.Text}, "parent_tool_use_id": nil}
	if err := s.encoder.Encode(message); err != nil {
		return harness.TurnResult{}, errors.New("cannot submit Claude input")
	}
	started := false
	terminal := ""
	for terminal == "" && s.process.Scan() {
		var event map[string]any
		if err := json.Unmarshal(s.process.Bytes(), &event); err != nil {
			return harness.TurnResult{}, errors.New("Claude emitted invalid stream JSON")
		}
		sessionID, _ := event["session_id"].(string)
		if sessionID != "" && !s.ready {
			if s.sessionID != "" && s.sessionID != sessionID {
				return harness.TurnResult{}, errors.New("Claude resumed an unexpected session")
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
				if delta, ok := protocolEvent["delta"].(map[string]any); ok && delta["type"] == "text_delta" {
					if text, ok := delta["text"].(string); ok && text != "" {
						emit(harness.Event{Type: "agent.output.delta", SessionID: s.sessionID, TurnID: input.ID, Delta: text})
					}
				}
			}
		}
		if typeName == "result" {
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
		return harness.TurnResult{}, errors.New("Claude process ended before a terminal result")
	}
	if !s.ready || s.sessionID == "" {
		return harness.TurnResult{}, errors.New("Claude did not provide a resumable session id")
	}
	if !started {
		emit(harness.Event{Type: "turn.started", SessionID: s.sessionID, TurnID: input.ID})
	}
	return harness.TurnResult{SessionID: s.sessionID, TurnID: input.ID, Status: terminal}, nil
}

func (s *session) Close() error { return s.process.Finish() }
func (s *session) Abort()       { s.process.Abort() }

func (d *Driver) String() string {
	return fmt.Sprintf("claude(%s)", strings.TrimSpace(d.executable))
}
