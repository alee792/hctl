package schedule

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"hctl/internal/harness"
	"hctl/internal/project"
)

func TestTriggerUsesPromptFreshSessionsAndDeduplicates(t *testing.T) {
	p := scheduledProject(t)
	driver := &fakeDriver{}

	first, err := Trigger(context.Background(), p, driver, "billing/sweep", "occurrence-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Trigger(context.Background(), p, driver, "billing/sweep", "occurrence-2")
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := Trigger(context.Background(), p, driver, "billing/sweep", "occurrence-2")
	if err != nil {
		t.Fatal(err)
	}

	if first.Status != "completed" || second.Status != "completed" || duplicate.Status != "completed" || !duplicate.Duplicate {
		t.Fatalf("results = %#v, %#v, %#v", first, second, duplicate)
	}
	if !reflect.DeepEqual(driver.resumed, []string{"", ""}) {
		t.Fatalf("resume ids = %v", driver.resumed)
	}
	wantPrompts := []string{"Sweep stale billing work.\n", "Sweep stale billing work.\n"}
	if !reflect.DeepEqual(driver.prompts, wantPrompts) {
		t.Fatalf("prompts = %q", driver.prompts)
	}
	if first.SessionID == second.SessionID || first.SessionID == "" || second.SessionID == "" {
		t.Fatalf("task sessions were not fresh: %#v, %#v", first, second)
	}
}

func TestTriggerRejectsUnknownScheduleWithoutOpeningHarness(t *testing.T) {
	p := scheduledProject(t)
	driver := &fakeDriver{}
	if _, err := Trigger(context.Background(), p, driver, "missing", "occurrence-1"); err == nil {
		t.Fatal("unknown schedule was accepted")
	}
	if len(driver.resumed) != 0 {
		t.Fatal("unknown schedule opened the harness")
	}
}

type fakeDriver struct {
	resumed []string
	prompts []string
}

func (d *fakeDriver) Name() string                 { return "claude" }
func (d *fakeDriver) Executable() string           { return "/fake/claude" }
func (d *fakeDriver) Verify(context.Context) error { return nil }
func (d *fakeDriver) Open(_ context.Context, _ string, sessionID string) (harness.Session, error) {
	d.resumed = append(d.resumed, sessionID)
	return &fakeSession{driver: d, id: "session-" + string(rune('0'+len(d.resumed)))}, nil
}

type fakeSession struct {
	driver *fakeDriver
	id     string
}

func (s *fakeSession) InitialEvents() []harness.Event {
	return []harness.Event{{Type: "session.started", SessionID: s.id}}
}
func (s *fakeSession) RunTurn(_ context.Context, input harness.Input, emit func(harness.Event)) (harness.TurnResult, error) {
	s.driver.prompts = append(s.driver.prompts, input.Text)
	emit(harness.Event{Type: "agent.output.delta", SessionID: s.id, TurnID: input.ID, Delta: "model text must be discarded"})
	return harness.TurnResult{SessionID: s.id, TurnID: input.ID, Status: "completed"}, nil
}
func (s *fakeSession) Close() error { return nil }
func (s *fakeSession) Abort()       {}

func scheduledProject(t *testing.T) *project.Project {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "instructions.md"), "---\ndescription: Test agent.\n---\n\nBe concise.\n")
	write(t, filepath.Join(root, "schedules", "billing", "sweep.md"), "---\ncron: '0 9 * * 1-5'\n---\n\nSweep stale billing work.\n")
	p, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
