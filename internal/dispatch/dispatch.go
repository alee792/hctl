package dispatch

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
	"time"
	"unicode/utf8"

	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/session"
)

const (
	maxInputBytes       = 32 << 10
	maxInputLine        = maxInputBytes + 4096
	harnessCloseTimeout = 5 * time.Second
)

var (
	conversationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	inputName        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)
)

type Submission struct {
	InputID string                  `json:"input_id"`
	Text    string                  `json:"text"`
	Reply   chan<- SubmissionResult `json:"-"`
	bytes   int
	status  string
	err     error
}

type SubmissionResult struct {
	Status    string
	Duplicate bool
}

type turnMessage struct {
	event  *harness.Event
	result harness.TurnResult
	err    error
	done   bool
}

type idleTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realIdleTimer struct{ timer *time.Timer }

func (t realIdleTimer) C() <-chan time.Time { return t.timer.C }
func (t realIdleTimer) Stop() bool          { return t.timer.Stop() }

type idleTimerFactory func(time.Duration) idleTimer

func newIdleTimer(after time.Duration) idleTimer { return realIdleTimer{timer: time.NewTimer(after)} }

type Event struct {
	SchemaVersion int    `json:"schema_version"`
	Sequence      int    `json:"sequence"`
	Type          string `json:"type"`
	Harness       string `json:"harness"`
	Conversation  string `json:"conversation"`
	InputID       string `json:"input_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	TurnID        string `json:"turn_id,omitempty"`
	ItemID        string `json:"item_id,omitempty"`
	Delta         string `json:"delta,omitempty"`
	Bytes         int    `json:"bytes,omitempty"`
	Status        string `json:"status,omitempty"`
}

func (e Event) Terminal() bool {
	switch e.Type {
	case "turn.completed", "turn.failed", "turn.cancelled", "turn.uncertain", "driver.process_failed":
		return true
	default:
		return false
	}
}

type eventSink struct {
	emitEvent    func(Event) error
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
	s.err = s.emitEvent(event)
}

func Run(ctx context.Context, p *project.Project, driver harness.Driver, conversationID string, input io.Reader, output io.Writer) error {
	submissions := make(chan Submission)
	go readInput(input, submissions)
	encoder := json.NewEncoder(output)
	return RunSubmissions(ctx, p, driver, conversationID, submissions, func(event Event) error { return encoder.Encode(event) })
}

// RunSubmissions drives one durable conversation from typed input. The caller
// owns the input transport and must close submissions when it stops accepting
// new input.
func RunSubmissions(ctx context.Context, p *project.Project, driver harness.Driver, conversationID string, submissions <-chan Submission, emit func(Event) error) error {
	if err := validateDispatch(conversationID, emit); err != nil {
		return err
	}
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		return err
	}
	return runSubmissions(ctx, p, driver, conversationID, submissions, emit, false, 0, 0, nil, harness.PolicyDefault, store)
}

// RunSubmissionsWithTurnTimeout drives a long-lived channel conversation while
// bounding each native harness turn independently.
func RunSubmissionsWithTurnTimeout(ctx context.Context, p *project.Project, driver harness.Driver, conversationID string, submissions <-chan Submission, emit func(Event) error, timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("turn timeout must be positive")
	}
	if err := validateDispatch(conversationID, emit); err != nil {
		return err
	}
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		return err
	}
	return runSubmissions(ctx, p, driver, conversationID, submissions, emit, false, timeout, 0, nil, harness.PolicyDefault, store)
}

// RunTask drives bounded task input while opening a fresh native harness
// session for every accepted input. Durable dispatch outcomes still deduplicate
// retries within the supplied conversation.
func RunTask(ctx context.Context, p *project.Project, driver harness.Driver, conversationID string, submission Submission, emit func(Event) error) error {
	if err := validateDispatch(conversationID, emit); err != nil {
		return err
	}
	submissions := make(chan Submission, 1)
	submissions <- submission
	close(submissions)
	store, err := openConversationStore(p.WorkspaceRoot)
	if err != nil {
		return err
	}
	return runSubmissions(ctx, p, driver, conversationID, submissions, emit, true, 0, 0, nil, harness.PolicyDefault, store)
}

func runSubmissions(ctx context.Context, p *project.Project, driver harness.Driver, conversationID string, submissions <-chan Submission, emit func(Event) error, freshSessions bool, turnTimeout, idleTimeout time.Duration, timers idleTimerFactory, policy harness.ExecutionPolicy, store *conversationStore) error {
	if err := validateDispatch(conversationID, emit); err != nil {
		return err
	}
	ref := conversationRef{agentID: p.AgentID, harness: driver.Name(), id: conversationID, fingerprint: p.SourceFingerprint}
	sink := &eventSink{emitEvent: emit, next: 1, harness: driver.Name(), conversation: conversationID}
	uncertain, recoveredSessionID, err := store.recover(ref)
	if err != nil {
		return err
	}
	if len(uncertain) > 0 {
		for _, id := range uncertain {
			sink.emit(Event{Type: "turn.uncertain", InputID: id, SessionID: recoveredSessionID, TurnID: id, Status: "dispatcher_restarted"})
		}
	}

	inputOpen := true
	var active *session.Input
	var process harness.Session
	var idle idleTimer
	var idleC <-chan time.Time
	turns := make(chan turnMessage, 64)
	defer func() {
		if idle != nil {
			idle.Stop()
		}
	}()

	abort := func() {
		if process != nil {
			process.Abort()
		}
	}
	defer abort()

	for {
		if sink.err != nil {
			return errors.New("cannot write dispatch events")
		}
		snapshot, err := store.snapshot(ref)
		if err != nil {
			return err
		}
		if active == nil && snapshot.queueLen > 0 {
			if idle != nil {
				idle.Stop()
				idle = nil
				idleC = nil
			}
			if process == nil {
				if freshSessions {
					if err := store.setSessionID(ref, ""); err != nil {
						return err
					}
					snapshot.sessionID = ""
				}
				process, err = driver.Open(ctx, harness.OpenRequest{Root: p.WorkspaceRoot, ResumeID: snapshot.sessionID, Policy: policy})
				if err != nil {
					sink.emit(Event{Type: "driver.process_failed", InputID: snapshot.firstID, SessionID: snapshot.sessionID, Status: "startup_failure"})
					return err
				}
				if idleTimeout > 0 {
					sink.emit(Event{Type: "driver.process_opened", SessionID: snapshot.sessionID})
				}
				for _, event := range process.InitialEvents() {
					if event.SessionID != "" {
						if err := store.setSessionID(ref, event.SessionID); err != nil {
							return err
						}
					}
					sink.emit(fromHarness(event, ""))
				}
			}
			next, err := store.startNext(ref)
			if err != nil {
				return err
			}
			active = &next
			go runTurn(ctx, process, next, turns, turnTimeout)
		}
		if inputOpen && active == nil && snapshot.queueLen == 0 && process != nil && idleTimeout > 0 && idle == nil {
			idle = timers(idleTimeout)
			idleC = idle.C()
		}
		if !inputOpen && active == nil && snapshot.queueLen == 0 {
			if process != nil {
				if idle != nil {
					idle.Stop()
					idle = nil
				}
				var closeErr error
				if idleTimeout > 0 {
					closeErr = closeHarness(process, harnessCloseTimeout, timers)
				} else {
					closeErr = process.Close()
				}
				if closeErr != nil {
					return closeErr
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
			inputID := ""
			if active != nil {
				inputID = active.ID
			}
			snapshot, _ := store.snapshot(ref)
			sink.emit(Event{Type: "driver.process_failed", InputID: inputID, SessionID: snapshot.sessionID, Status: status})
			return ctx.Err()
		case <-idleC:
			idle = nil
			idleC = nil
			if active != nil || process == nil {
				continue
			}
			snapshot, err := store.snapshot(ref)
			if err != nil {
				return err
			}
			if snapshot.queueLen != 0 {
				continue
			}
			if err := closeHarness(process, harnessCloseTimeout, timers); err != nil {
				sink.emit(Event{Type: "driver.process_failed", SessionID: snapshot.sessionID, Status: "hibernate_failure"})
				return err
			}
			process = nil
			sink.emit(Event{Type: "driver.process_hibernated", SessionID: snapshot.sessionID, Status: "idle_timeout"})
		case result, ok := <-submissions:
			if !ok {
				inputOpen = false
				submissions = nil
				continue
			}
			if result.err != nil {
				return result.err
			}
			if result.bytes == 0 {
				result.bytes = len([]byte(result.Text))
			}
			if result.status == "" {
				result.status = validateInput(result)
			}
			if result.status != "" {
				replySubmission(ctx, result, SubmissionResult{Status: result.status})
				sink.emit(Event{Type: "input.rejected", InputID: result.InputID, Bytes: result.bytes, Status: result.status})
				continue
			}
			status, duplicate, err := store.accept(ref, result.InputID, result.Text)
			if err != nil {
				replySubmission(ctx, result, SubmissionResult{Status: err.Error()})
				sink.emit(Event{Type: "input.rejected", InputID: result.InputID, Bytes: result.bytes, Status: err.Error()})
				continue
			}
			if duplicate {
				replySubmission(ctx, result, SubmissionResult{Status: status, Duplicate: true})
				sink.emit(Event{Type: "input.duplicate", InputID: result.InputID, Bytes: result.bytes, Status: status})
				continue
			}
			replySubmission(ctx, result, SubmissionResult{Status: status})
			sink.emit(Event{Type: "input.accepted", InputID: result.InputID, Bytes: result.bytes})
			sink.emit(Event{Type: "turn.queued", InputID: result.InputID})
		case message := <-turns:
			if active == nil {
				return errors.New("received a harness event without an active input")
			}
			if message.event != nil {
				if message.event.SessionID != "" {
					if err := store.setSessionID(ref, message.event.SessionID); err != nil {
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
				snapshot, _ := store.snapshot(ref)
				sink.emit(Event{Type: "driver.process_failed", InputID: active.ID, SessionID: snapshot.sessionID, Status: "process_failure"})
				return message.err
			}
			terminalSessionID, err := store.complete(ref, active.ID, message.result.Status, message.result.SessionID, freshSessions)
			if err != nil {
				return err
			}
			completedID := active.ID
			active = nil
			sink.emit(Event{Type: "turn." + message.result.Status, InputID: completedID, SessionID: terminalSessionID, TurnID: message.result.TurnID})
			if freshSessions {
				if err := process.Close(); err != nil {
					return err
				}
				process = nil
			}
		}
	}
}

func closeHarness(process harness.Session, timeout time.Duration, timers idleTimerFactory) error {
	closed := make(chan error, 1)
	go func() { closed <- process.Close() }()
	timer := timers(timeout)
	select {
	case err := <-closed:
		timer.Stop()
		return err
	case <-timer.C():
		process.Abort()
		return errors.New("harness process did not close before the hibernation deadline")
	}
}

func validateDispatch(conversationID string, emit func(Event) error) error {
	if !conversationName.MatchString(conversationID) {
		return errors.New("conversation must use only letters, digits, dot, underscore, and dash")
	}
	if emit == nil {
		return errors.New("dispatch event receiver is required")
	}
	return nil
}

func replySubmission(ctx context.Context, submission Submission, result SubmissionResult) {
	if submission.Reply == nil {
		return
	}
	select {
	case submission.Reply <- result:
	case <-ctx.Done():
	}
}

func readInput(input io.Reader, results chan<- Submission) {
	defer close(results)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxInputLine)
	for scanner.Scan() {
		line := append([]byte{}, scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var value Submission
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil {
			results <- Submission{bytes: len(line), status: "invalid_json"}
			continue
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			results <- Submission{InputID: value.InputID, Text: value.Text, bytes: len(line), status: "invalid_json"}
			continue
		}
		status := validateInput(value)
		value.bytes = len([]byte(value.Text))
		value.status = status
		results <- value
	}
	if scanner.Err() != nil {
		results <- Submission{err: errors.New("run input exceeded the bounded JSONL line size")}
	}
}

func validateInput(value Submission) string {
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

func runTurn(ctx context.Context, process harness.Session, input session.Input, messages chan<- turnMessage, timeout time.Duration) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	emit := func(event harness.Event) {
		copy := event
		messages <- turnMessage{event: &copy}
	}
	result, err := process.RunTurn(ctx, harness.Input{ID: input.ID, Text: input.Text}, emit)
	messages <- turnMessage{result: result, err: err, done: true}
}

func fromHarness(event harness.Event, inputID string) Event {
	return Event{Type: event.Type, InputID: inputID, SessionID: event.SessionID, TurnID: event.TurnID, ItemID: event.ItemID, Delta: event.Delta, Status: event.Status}
}

func ValidateConversation(value string) error {
	if !conversationName.MatchString(value) {
		return fmt.Errorf("invalid conversation %q", value)
	}
	return nil
}

func ValidateInputID(value string) error {
	if !inputName.MatchString(value) {
		return fmt.Errorf("invalid input id %q", value)
	}
	return nil
}
