package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/session"
)

const (
	maxInputBytes = 32 << 10
	maxInputLine  = maxInputBytes + 4096
)

var (
	conversationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	inputName        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)
)

type inbound struct {
	InputID string `json:"input_id"`
	Text    string `json:"text"`
}

type readResult struct {
	input  inbound
	bytes  int
	status string
	err    error
}

type turnMessage struct {
	event  *harness.Event
	result harness.TurnResult
	err    error
	done   bool
}

type Event struct {
	SchemaVersion int    `json:"schema_version"`
	Sequence      int    `json:"sequence"`
	Type          string `json:"type"`
	Harness       string `json:"harness"`
	Conversation  string `json:"conversation"`
	InputID       string `json:"input_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	TurnID        string `json:"turn_id,omitempty"`
	Delta         string `json:"delta,omitempty"`
	Bytes         int    `json:"bytes,omitempty"`
	Status        string `json:"status,omitempty"`
}

type eventSink struct {
	encoder      *json.Encoder
	next         int
	harness      string
	conversation string
	err          error
}

func (s *eventSink) emit(event Event) {
	if s.err != nil {
		return
	}
	event.SchemaVersion = 1
	event.Sequence = s.next
	event.Harness = s.harness
	event.Conversation = s.conversation
	s.next++
	s.err = s.encoder.Encode(event)
}

func Run(ctx context.Context, p *project.Project, driver harness.Driver, conversationID string, input io.Reader, output io.Writer) error {
	if !conversationName.MatchString(conversationID) {
		return errors.New("conversation must use only letters, digits, dot, underscore, and dash")
	}
	state, err := session.Load(p.Root)
	if err != nil {
		return err
	}
	conversation, err := state.GetOrCreate(driver.Name(), conversationID, p.Manifest.SourceFingerprint)
	if err != nil {
		return err
	}
	sink := &eventSink{encoder: json.NewEncoder(output), next: 1, harness: driver.Name(), conversation: conversationID}
	if uncertain := conversation.RecoverUncertain(); len(uncertain) > 0 {
		if err := session.Save(p.Root, state); err != nil {
			return err
		}
		for _, id := range uncertain {
			sink.emit(Event{Type: "turn.uncertain", InputID: id, SessionID: conversation.SessionID, TurnID: id, Status: "gateway_restarted"})
		}
	}

	reads := make(chan readResult)
	go readInput(input, reads)
	inputOpen := true
	var active *session.Input
	var process harness.Session
	turns := make(chan turnMessage, 64)

	abort := func() {
		if process != nil {
			process.Abort()
		}
	}
	defer abort()

	for {
		if sink.err != nil {
			return errors.New("cannot write gateway events")
		}
		if active == nil && len(conversation.Queue) > 0 {
			if process == nil {
				process, err = driver.Open(ctx, p.Root, conversation.SessionID)
				if err != nil {
					sink.emit(Event{Type: "driver.process_failed", SessionID: conversation.SessionID, Status: "startup_failure"})
					return err
				}
				for _, event := range process.InitialEvents() {
					if event.SessionID != "" {
						conversation.SessionID = event.SessionID
					}
					sink.emit(fromHarness(event, ""))
				}
				if err := session.Save(p.Root, state); err != nil {
					return err
				}
			}
			next, err := conversation.StartNext()
			if err != nil {
				return err
			}
			if err := session.Save(p.Root, state); err != nil {
				return err
			}
			active = &next
			go runTurn(ctx, process, next, turns)
		}
		if !inputOpen && active == nil && len(conversation.Queue) == 0 {
			if process != nil {
				if err := process.Close(); err != nil {
					return err
				}
				process = nil
			}
			return nil
		}

		select {
		case <-ctx.Done():
			status := "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = "deadline_exceeded"
			}
			sink.emit(Event{Type: "driver.process_failed", SessionID: conversation.SessionID, Status: status})
			return ctx.Err()
		case result, ok := <-reads:
			if !ok {
				inputOpen = false
				reads = nil
				continue
			}
			if result.err != nil {
				return result.err
			}
			if result.status != "" {
				sink.emit(Event{Type: "input.rejected", InputID: result.input.InputID, Bytes: result.bytes, Status: result.status})
				continue
			}
			status, duplicate, err := conversation.Accept(result.input.InputID, result.input.Text)
			if err != nil {
				sink.emit(Event{Type: "input.rejected", InputID: result.input.InputID, Bytes: result.bytes, Status: err.Error()})
				continue
			}
			if duplicate {
				sink.emit(Event{Type: "input.duplicate", InputID: result.input.InputID, Bytes: result.bytes, Status: status})
				continue
			}
			if err := session.Save(p.Root, state); err != nil {
				return err
			}
			sink.emit(Event{Type: "input.accepted", InputID: result.input.InputID, Bytes: result.bytes})
			sink.emit(Event{Type: "turn.queued", InputID: result.input.InputID})
		case message := <-turns:
			if active == nil {
				return errors.New("received a harness event without an active input")
			}
			if message.event != nil {
				if message.event.SessionID != "" && conversation.SessionID != message.event.SessionID {
					conversation.SessionID = message.event.SessionID
					if err := session.Save(p.Root, state); err != nil {
						return err
					}
				}
				sink.emit(fromHarness(*message.event, active.ID))
				continue
			}
			if !message.done {
				continue
			}
			if message.err != nil {
				sink.emit(Event{Type: "driver.process_failed", InputID: active.ID, SessionID: conversation.SessionID, Status: "process_failure"})
				return message.err
			}
			if message.result.SessionID != "" {
				conversation.SessionID = message.result.SessionID
			}
			if err := conversation.Complete(active.ID, message.result.Status); err != nil {
				return err
			}
			if err := session.Save(p.Root, state); err != nil {
				return err
			}
			sink.emit(Event{Type: "turn." + message.result.Status, InputID: active.ID, SessionID: conversation.SessionID, TurnID: message.result.TurnID})
			active = nil
		}
	}
}

func readInput(input io.Reader, results chan<- readResult) {
	defer close(results)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxInputLine)
	for scanner.Scan() {
		line := append([]byte{}, scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var value inbound
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			results <- readResult{bytes: len(line), status: "invalid_json"}
			continue
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			results <- readResult{input: value, bytes: len(line), status: "invalid_json"}
			continue
		}
		status := validateInput(value)
		results <- readResult{input: value, bytes: len([]byte(value.Text)), status: status}
	}
	if scanner.Err() != nil {
		results <- readResult{err: errors.New("gateway input exceeded the bounded JSONL line size")}
	}
}

func validateInput(value inbound) string {
	if !inputName.MatchString(value.InputID) {
		return "invalid_input_id"
	}
	if value.Text == "" || strings.TrimSpace(value.Text) == "" {
		return "empty_text"
	}
	if !utf8.ValidString(value.Text) {
		return "invalid_utf8"
	}
	if len([]byte(value.Text)) > maxInputBytes {
		return "input_too_large"
	}
	return ""
}

func runTurn(ctx context.Context, process harness.Session, input session.Input, messages chan<- turnMessage) {
	emit := func(event harness.Event) {
		copy := event
		messages <- turnMessage{event: &copy}
	}
	result, err := process.RunTurn(ctx, harness.Input{ID: input.ID, Text: input.Text}, emit)
	messages <- turnMessage{result: result, err: err, done: true}
}

func fromHarness(event harness.Event, inputID string) Event {
	return Event{Type: event.Type, InputID: inputID, SessionID: event.SessionID, TurnID: event.TurnID, Delta: event.Delta, Status: event.Status}
}

func ValidateConversation(value string) error {
	if !conversationName.MatchString(value) {
		return fmt.Errorf("invalid conversation %q", value)
	}
	return nil
}
