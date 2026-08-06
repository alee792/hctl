package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/session"
)

func TestAcceptsAndDeduplicatesInputDuringActiveTurn(t *testing.T) {
	p := testProject(t)
	started := make(chan string, 2)
	release := make(chan struct{})
	driver := &fakeDriver{started: started, release: release}
	reader, writer := io.Pipe()
	lines := newLineOutput()
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { done <- Run(ctx, p, driver, "thread-a", reader, lines) }()

	writeJSON(t, writer, map[string]string{"input_id": "message-1", "text": "first"})
	if got := <-started; got != "message-1" {
		t.Fatalf("first active input = %s", got)
	}
	writeJSON(t, writer, map[string]string{"input_id": "message-2", "text": "second"})
	writeJSON(t, writer, map[string]string{"input_id": "message-1", "text": "first"})

	waitForType(t, lines.events, "input.accepted", "message-2")
	waitForType(t, lines.events, "input.duplicate", "message-1")
	close(release)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(driver.inputs, []string{"message-1", "message-2"}) {
		t.Fatalf("turn order = %v", driver.inputs)
	}
	events := lines.all()
	acceptedSecond := eventIndex(events, "input.accepted", "message-2")
	completedFirst := eventIndex(events, "turn.completed", "message-1")
	if acceptedSecond < 0 || completedFirst < 0 || acceptedSecond > completedFirst {
		t.Fatalf("second input was not accepted during the active first turn: %#v", events)
	}
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation := findConversation(state, "thread-a")
	if conversation == nil || len(conversation.Queue) != 0 || conversation.Outcomes["message-1"] != "completed" || conversation.Outcomes["message-2"] != "completed" {
		t.Fatalf("durable state = %#v", conversation)
	}
}

func TestRecoversActiveInputAsUncertain(t *testing.T) {
	p := testProject(t)
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := state.GetOrCreate(p.AgentID, "claude", "thread-a", p.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Accept("message-1", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.StartNext(); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(p.WorkspaceRoot, state); err != nil {
		t.Fatal(err)
	}

	lines := newLineOutput()
	if err := Run(context.Background(), p, &fakeDriver{}, "thread-a", strings.NewReader(""), lines); err != nil {
		t.Fatal(err)
	}
	events := lines.all()
	if eventIndex(events, "turn.uncertain", "message-1") < 0 {
		t.Fatalf("missing uncertain recovery event: %#v", events)
	}
}

func TestGatewayRunsHarnessAndStateInSelectedWorkspace(t *testing.T) {
	standalone := testProject(t)
	workspace := t.TempDir()
	p, err := project.Load(standalone.SourceRoot, "claude", workspace)
	if err != nil {
		t.Fatal(err)
	}
	driver := &fakeDriver{}
	input := strings.NewReader("{\"input_id\":\"message-1\",\"text\":\"review\"}\n")
	if err := Run(context.Background(), p, driver, "thread-a", input, io.Discard); err != nil {
		t.Fatal(err)
	}
	if driver.openedRoot != p.WorkspaceRoot {
		t.Fatalf("harness opened in %q, want workspace %q", driver.openedRoot, p.WorkspaceRoot)
	}
	if _, err := os.Stat(filepath.Join(p.WorkspaceRoot, ".hctl", "gateway.json")); err != nil {
		t.Fatalf("workspace state missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.SourceRoot, ".hctl")); !os.IsNotExist(err) {
		t.Fatalf("gateway wrote state into agent source: %v", err)
	}
}

func TestTaskInputsUseFreshSessionsAndDeduplicate(t *testing.T) {
	p := testProject(t)
	driver := &taskDriver{}
	var events []Event
	emit := func(event Event) error {
		events = append(events, event)
		return nil
	}

	for _, inputID := range []string{"occurrence-1", "occurrence-2"} {
		if err := RunTask(context.Background(), p, driver, "schedule-daily", Submission{InputID: inputID, Text: "Run the task."}, emit); err != nil {
			t.Fatal(err)
		}
	}
	if err := RunTask(context.Background(), p, driver, "schedule-daily", Submission{InputID: "occurrence-2", Text: "Run the task."}, emit); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(driver.resumed, []string{"", ""}) {
		t.Fatalf("task session resume ids = %v", driver.resumed)
	}
	if !reflect.DeepEqual(driver.inputs, []string{"occurrence-1", "occurrence-2"}) {
		t.Fatalf("task inputs = %v", driver.inputs)
	}
	if eventIndex(events, "input.duplicate", "occurrence-2") < 0 {
		t.Fatalf("duplicate task input was not reported: %#v", events)
	}
	state, err := session.Load(p.WorkspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	conversation := findConversation(state, "schedule-daily")
	if conversation == nil || conversation.SessionID != "" || len(conversation.Outcomes) != 2 {
		t.Fatalf("task conversation state = %#v", conversation)
	}
}

type fakeDriver struct {
	started    chan string
	release    chan struct{}
	inputs     []string
	openedRoot string
	mu         sync.Mutex
}

func (d *fakeDriver) Name() string                 { return "claude" }
func (d *fakeDriver) Executable() string           { return "/fake/claude" }
func (d *fakeDriver) Verify(context.Context) error { return nil }
func (d *fakeDriver) Open(_ context.Context, root, _ string) (harness.Session, error) {
	d.openedRoot = root
	return &fakeSession{driver: d}, nil
}

type fakeSession struct{ driver *fakeDriver }

func (s *fakeSession) InitialEvents() []harness.Event {
	return []harness.Event{{Type: "driver.ready", SessionID: "session-1"}, {Type: "session.started", SessionID: "session-1"}}
}
func (s *fakeSession) RunTurn(_ context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	s.driver.mu.Lock()
	s.driver.inputs = append(s.driver.inputs, input.ID)
	index := len(s.driver.inputs)
	s.driver.mu.Unlock()
	emit(harness.Event{Type: "turn.started", SessionID: "session-1", TurnID: input.ID})
	if s.driver.started != nil {
		s.driver.started <- input.ID
	}
	if index == 1 && s.driver.release != nil {
		<-s.driver.release
	}
	emit(harness.Event{Type: "agent.output.delta", SessionID: "session-1", TurnID: input.ID, Delta: "ok"})
	return harness.TurnResult{SessionID: "session-1", TurnID: input.ID, Status: "completed"}, nil
}
func (s *fakeSession) Close() error { return nil }
func (s *fakeSession) Abort()       {}

type taskDriver struct {
	resumed []string
	inputs  []string
}

func (d *taskDriver) Name() string                 { return "claude" }
func (d *taskDriver) Executable() string           { return "/fake/claude" }
func (d *taskDriver) Verify(context.Context) error { return nil }
func (d *taskDriver) Open(_ context.Context, _ string, sessionID string) (harness.Session, error) {
	d.resumed = append(d.resumed, sessionID)
	return &taskSession{driver: d, sessionID: fmt.Sprintf("task-session-%d", len(d.resumed))}, nil
}

type taskSession struct {
	driver    *taskDriver
	sessionID string
}

func (s *taskSession) InitialEvents() []harness.Event {
	return []harness.Event{{Type: "session.started", SessionID: s.sessionID}}
}
func (s *taskSession) RunTurn(_ context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	s.driver.inputs = append(s.driver.inputs, input.ID)
	emit(harness.Event{Type: "agent.output.delta", SessionID: s.sessionID, TurnID: input.ID, Delta: "discard me"})
	return harness.TurnResult{SessionID: s.sessionID, TurnID: input.ID, Status: "completed"}, nil
}
func (s *taskSession) Close() error { return nil }
func (s *taskSession) Abort()       {}

type lineOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	events chan Event
	allEv  []Event
}

func newLineOutput() *lineOutput { return &lineOutput{events: make(chan Event, 64)} }

func (w *lineOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(data)
	_, _ = w.buffer.Write(data)
	for {
		line, err := w.buffer.ReadString('\n')
		if err != nil {
			if len(line) > 0 {
				_, _ = w.buffer.WriteString(line)
			}
			break
		}
		var event Event
		if json.Unmarshal([]byte(line), &event) == nil {
			w.allEv = append(w.allEv, event)
			w.events <- event
		}
	}
	return original, nil
}

func (w *lineOutput) all() []Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Event{}, w.allEv...)
}

func writeJSON(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func waitForType(t *testing.T, events <-chan Event, typeName, inputID string) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == typeName && event.InputID == inputID {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s %s", typeName, inputID)
		}
	}
}

func eventIndex(events []Event, typeName, inputID string) int {
	for index, event := range events {
		if event.Type == typeName && event.InputID == inputID {
			return index
		}
	}
	return -1
}

func findConversation(state *session.State, id string) *session.Conversation {
	for _, conversation := range state.Conversations {
		if conversation.ID == id {
			return conversation
		}
	}
	return nil
}

func testProject(t *testing.T) *project.Project {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"instructions.md":      "---\ndescription: Test agent.\n---\n\nBe concise.\n",
		"skills/echo/SKILL.md": "---\nname: echo\ndescription: Repeat safely.\n---\n\nUse echo.\n",
	}
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
