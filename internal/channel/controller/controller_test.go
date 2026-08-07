package controller

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"hctl/internal/channelconfig"
	"hctl/internal/dispatch"
)

func TestControllerBuffersSeparateItemsAndDeliversNormalReply(t *testing.T) {
	c, managed, delivery := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "target")
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "one", Delta: "I'll check that."})
	c.handleDispatch("conversation", dispatch.Event{Type: "input.accepted", InputID: "input"})
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "two", Delta: "Yes, origin is"})
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "two", Delta: " configured."})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})

	if len(managed.submitted) != 1 || managed.submitted[0].conversation != "conversation" {
		t.Fatalf("submissions = %#v", managed.submitted)
	}
	want := Outcome{InputID: "input", Target: "target", Parts: []string{"I'll check that.", "Yes, origin is configured."}}
	if !reflect.DeepEqual(delivery.outcomes, []Outcome{want}) {
		t.Fatalf("outcomes = %#v, want %#v", delivery.outcomes, want)
	}
}

func TestControllerSuppressesOnlyExactNoReply(t *testing.T) {
	for _, test := range []struct {
		name       string
		output     string
		deliveries int
	}{
		{name: "exact", output: " \n" + channelconfig.NoReplyResult + "\t", deliveries: 0},
		{name: "explanation", output: channelconfig.NoReplyResult + " because", deliveries: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _, delivery := testController(t, 100)
			submit(t, c, "surface", "conversation", "input", "target")
			c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: test.output})
			c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})
			if len(delivery.outcomes) != test.deliveries {
				t.Fatalf("deliveries = %#v", delivery.outcomes)
			}
		})
	}
}

func TestControllerWaitsToTypeUntilVisibleReplyIsDecided(t *testing.T) {
	c, _, delivery := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "target")
	for _, delta := range []string{"HCTL_", "NO_", "REPLY"} {
		c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: delta})
	}
	if len(delivery.typing) != 0 {
		t.Fatalf("typing started for control output: %#v", delivery.typing)
	}

	submit(t, c, "other", "other-conversation", "other-input", "other-target")
	c.handleDispatch("other-conversation", dispatch.Event{Type: "agent.output.delta", InputID: "other-input", ItemID: "output", Delta: "Hi"})
	if !reflect.DeepEqual(delivery.typing, []any{"other-target"}) {
		t.Fatalf("typing targets = %#v", delivery.typing)
	}
}

func TestControllerElevatesExactWriteRequestOnce(t *testing.T) {
	c, managed, delivery := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "target")
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: channelconfig.RequestWriteAccessResult})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})

	select {
	case elevated := <-managed.elevated:
		if elevated.conversation != "conversation" || elevated.submission.InputID != "input:write" || elevated.submission.Text != channelconfig.WriteContinuationPrompt {
			t.Fatalf("elevation = %#v", elevated)
		}
	case <-time.After(time.Second):
		t.Fatal("write continuation was not submitted")
	}
	if len(delivery.outcomes) != 0 {
		t.Fatalf("control output was delivered: %#v", delivery.outcomes)
	}
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input:write", ItemID: "output", Delta: "done"})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input:write"})
	if len(delivery.outcomes) != 1 || !reflect.DeepEqual(delivery.outcomes[0].Parts, []string{"done"}) {
		t.Fatalf("continuation outcome = %#v", delivery.outcomes)
	}
}

func TestControllerDoesNotTreatCallerWriteSuffixAsAlreadyElevated(t *testing.T) {
	c, managed, _ := testController(t, 100)
	submit(t, c, "surface", "conversation", "input:write", "target")
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input:write", ItemID: "output", Delta: channelconfig.RequestWriteAccessResult})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input:write"})
	select {
	case elevated := <-managed.elevated:
		if elevated.submission.InputID != "input:write:write" {
			t.Fatalf("continuation input = %q", elevated.submission.InputID)
		}
	case <-time.After(time.Second):
		t.Fatal("caller-owned input suffix prevented elevation")
	}
}

func TestControllerDoesNotElevateFailedWriteRequest(t *testing.T) {
	c, managed, delivery := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "target")
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: channelconfig.RequestWriteAccessResult})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.failed", InputID: "input"})
	select {
	case elevated := <-managed.elevated:
		t.Fatalf("failed turn elevated: %#v", elevated)
	default:
	}
	if len(delivery.outcomes) != 1 || delivery.outcomes[0].Failure != FailureProcess {
		t.Fatalf("failed outcome = %#v", delivery.outcomes)
	}
}

func TestControllerBoundsOutputBeforeDelivery(t *testing.T) {
	c, _, delivery := testController(t, 5)
	submit(t, c, "surface", "conversation", "input", "target")
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: "123456789"})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})
	if len(delivery.outcomes) != 1 || !delivery.outcomes[0].Truncated || !reflect.DeepEqual(delivery.outcomes[0].Parts, []string{"12345"}) {
		t.Fatalf("bounded outcome = %#v", delivery.outcomes)
	}
}

func TestControllerIgnoresDuplicateAndLateEvents(t *testing.T) {
	c, managed, delivery := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "first")
	result, err := c.Submit(context.Background(), Inbound{SurfaceID: "surface", ConversationID: "conversation", InputID: "input", Text: "duplicate", Target: "second"})
	if err != nil || !result.Duplicate || len(managed.submitted) != 1 {
		t.Fatalf("duplicate result=%#v err=%v submissions=%#v", result, err, managed.submitted)
	}
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: "reply"})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})
	c.handleDispatch("missing", dispatch.Event{Type: "turn.failed", InputID: "input"})
	if len(delivery.outcomes) != 1 || delivery.outcomes[0].Target != "first" {
		t.Fatalf("outcomes = %#v", delivery.outcomes)
	}
}

func TestControllerClassifiesTerminalFailures(t *testing.T) {
	tests := map[string]Failure{
		"turn.failed": FailureProcess, "driver.process_failed": FailureProcess,
		"turn.cancelled": FailureCancelled, "turn.uncertain": FailureUncertain,
	}
	for eventType, want := range tests {
		t.Run(eventType, func(t *testing.T) {
			c, _, delivery := testController(t, 100)
			submit(t, c, "surface", "conversation", "input", "target")
			c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: "unsafe partial output"})
			c.handleDispatch("conversation", dispatch.Event{Type: eventType, InputID: "input"})
			if len(delivery.outcomes) != 1 || delivery.outcomes[0].Failure != want || len(delivery.outcomes[0].Parts) != 0 {
				t.Fatalf("outcome = %#v, want failure %q", delivery.outcomes, want)
			}
		})
	}
}

func TestControllerClassifiesCompletedTurnWithoutOutput(t *testing.T) {
	c, _, delivery := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "target")
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})
	if len(delivery.outcomes) != 1 || delivery.outcomes[0].Failure != FailureNoOutput {
		t.Fatalf("outcome = %#v", delivery.outcomes)
	}
}

func TestControllerDelegatesStatusAndIdleReset(t *testing.T) {
	c, managed, _ := testController(t, 100)
	managed.statuses["conversation"] = dispatch.ConversationStatus{State: dispatch.LifecycleHibernated}
	managed.capacity = dispatch.CapacityStatus{ActiveLimit: 2, ResidentLimit: 4}
	if got := c.Status("conversation"); !reflect.DeepEqual(got, Status{Conversation: managed.statuses["conversation"], Capacity: managed.capacity}) {
		t.Fatalf("status = %#v", got)
	}
	if err := c.Reset("surface", "conversation"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(managed.resets, []string{"conversation"}) {
		t.Fatalf("resets = %#v", managed.resets)
	}
}

func TestControllerRejectsBusyResetAndStopsAdmissionOnClose(t *testing.T) {
	c, managed, _ := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "target")
	if err := c.Reset("surface", "conversation"); !errors.Is(err, dispatch.ErrConversationBusy) {
		t.Fatalf("busy reset error = %v", err)
	}
	c.Close()
	if !managed.closed {
		t.Fatal("manager was not closed")
	}
	if _, err := c.Submit(context.Background(), Inbound{SurfaceID: "other", ConversationID: "other", InputID: "other", Text: "text"}); !errors.Is(err, dispatch.ErrManagerClosed) {
		t.Fatalf("post-close admission error = %v", err)
	}
}

func TestControllerCloseSuppressesTerminalEventsForPendingWork(t *testing.T) {
	c, managed, delivery := testController(t, 100)
	managed.submitResults = []dispatch.SubmissionResult{{Status: "active"}, {Status: "queued"}}
	submit(t, c, "surface-active", "conversation-active", "input-active", "active-target")
	submit(t, c, "surface-queued", "conversation-queued", "input-queued", "queued-target")
	managed.closeStarted = make(chan struct{})
	managed.closeRelease = make(chan struct{})
	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	select {
	case <-managed.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("manager close did not start")
	}
	c.handleDispatch("conversation-active", dispatch.Event{Type: "agent.output.delta", InputID: "input-active", ItemID: "output", Delta: "partial"})
	c.handleDispatch("conversation-active", dispatch.Event{Type: "turn.failed", InputID: "input-active"})
	c.handleDispatch("conversation-queued", dispatch.Event{Type: "turn.cancelled", InputID: "input-queued"})
	close(managed.closeRelease)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("controller close did not finish")
	}
	if len(delivery.outcomes) != 0 || len(delivery.typing) != 0 {
		t.Fatalf("post-close delivery = %#v typing = %#v", delivery.outcomes, delivery.typing)
	}
}

func TestControllerPropagatesManagerDoneAndErr(t *testing.T) {
	c, managed, _ := testController(t, 100)
	want := errors.New("dispatcher event delivery failed")
	managed.err = want
	close(managed.done)
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("manager completion was not propagated")
	}
	if !errors.Is(c.Err(), want) {
		t.Fatalf("controller error = %v", c.Err())
	}
}

func TestControllerRejectsSurfaceConversationRemapping(t *testing.T) {
	c, _, _ := testController(t, 100)
	submit(t, c, "surface", "conversation", "input", "target")
	if _, err := c.Submit(context.Background(), Inbound{SurfaceID: "surface", ConversationID: "changed", InputID: "other", Text: "text"}); err == nil {
		t.Fatal("surface remapping was accepted")
	}
	if _, err := c.Submit(context.Background(), Inbound{SurfaceID: "other", ConversationID: "conversation", InputID: "other", Text: "text"}); err == nil {
		t.Fatal("conversation remapping was accepted")
	}
}

func TestControllerClassifiesAdmissionFailureAndDoesNotCancelRuntime(t *testing.T) {
	c, managed, delivery := testController(t, 100)
	managed.submitErr = errors.New("assigned worktree cannot be resolved")
	_, err := c.Submit(context.Background(), Inbound{SurfaceID: "surface", ConversationID: "conversation", InputID: "input", Text: "text", Target: "target"})
	if err == nil {
		t.Fatal("submission failure was hidden")
	}
	if len(delivery.outcomes) != 1 || delivery.outcomes[0].Failure != FailureAdmission || delivery.outcomes[0].Target != "target" {
		t.Fatalf("admission outcome = %#v", delivery.outcomes)
	}
	select {
	case <-c.ctx.Done():
		t.Fatal("conversation-local failure cancelled the controller")
	default:
	}
}

func testController(t *testing.T, limit int) (*Controller, *fakeManager, *fakeTransport) {
	t.Helper()
	managed := newFakeManager()
	delivery := &fakeTransport{}
	c, err := NewWithManager(context.Background(), managed, delivery, limit, io.Discard, "Test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, managed, delivery
}

func submit(t *testing.T, c *Controller, surface, conversation, input string, target any) {
	t.Helper()
	if _, err := c.Submit(context.Background(), Inbound{SurfaceID: surface, ConversationID: conversation, InputID: input, Text: "text", Target: target}); err != nil {
		t.Fatal(err)
	}
}

// fakeTransport is intentionally vendor-free: it proves the controller seam
// can serve a second transport without importing Discord payload types.
type fakeTransport struct {
	mu       sync.Mutex
	typing   []any
	outcomes []Outcome
	err      error
}

func (f *fakeTransport) Typing(target any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing = append(f.typing, target)
	return nil
}

func (f *fakeTransport) Deliver(outcome Outcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomes = append(f.outcomes, outcome)
	return f.err
}

type fakeSubmission struct {
	conversation string
	submission   dispatch.Submission
}

type fakeManager struct {
	mu            sync.Mutex
	submitted     []fakeSubmission
	elevated      chan fakeSubmission
	statuses      map[string]dispatch.ConversationStatus
	capacity      dispatch.CapacityStatus
	resets        []string
	submitErr     error
	elevateErr    error
	elevateDone   dispatch.SubmissionResult
	done          chan struct{}
	err           error
	closed        bool
	closeStarted  chan struct{}
	closeRelease  chan struct{}
	closeOnce     sync.Once
	submitResults []dispatch.SubmissionResult
}

func newFakeManager() *fakeManager {
	return &fakeManager{elevated: make(chan fakeSubmission, 1), statuses: map[string]dispatch.ConversationStatus{}, done: make(chan struct{})}
}

func (m *fakeManager) Submit(_ context.Context, conversation string, submission dispatch.Submission) (dispatch.SubmissionResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submitted = append(m.submitted, fakeSubmission{conversation: conversation, submission: submission})
	if m.submitErr != nil {
		return dispatch.SubmissionResult{}, m.submitErr
	}
	if len(m.submitResults) != 0 {
		result := m.submitResults[0]
		m.submitResults = m.submitResults[1:]
		return result, nil
	}
	return dispatch.SubmissionResult{Status: "queued"}, nil
}

func (m *fakeManager) Elevate(_ context.Context, conversation string, submission dispatch.Submission) (dispatch.SubmissionResult, error) {
	got := fakeSubmission{conversation: conversation, submission: submission}
	m.elevated <- got
	if m.elevateErr != nil {
		return dispatch.SubmissionResult{}, m.elevateErr
	}
	if m.elevateDone.Status != "" {
		return m.elevateDone, nil
	}
	return dispatch.SubmissionResult{Status: "queued"}, nil
}

func (m *fakeManager) Status(conversation string) dispatch.ConversationStatus {
	return m.statuses[conversation]
}
func (m *fakeManager) Capacity() dispatch.CapacityStatus { return m.capacity }
func (m *fakeManager) Reset(conversation string) error {
	m.resets = append(m.resets, conversation)
	return nil
}
func (m *fakeManager) Done() <-chan struct{} { return m.done }
func (m *fakeManager) Err() error            { return m.err }
func (m *fakeManager) Close() {
	m.closed = true
	if m.closeStarted != nil {
		m.closeOnce.Do(func() { close(m.closeStarted) })
	}
	if m.closeRelease != nil {
		<-m.closeRelease
	}
}

func TestVisibleReplyDecisionPrefixes(t *testing.T) {
	for _, output := range []string{"", "  ", "H", "HCTL_NO_", channelconfig.NoReplyResult, "HCTL_REQUEST_", channelconfig.RequestWriteAccessResult} {
		if visibleReplyDecided(output) {
			t.Fatalf("typing started for possible control output %q", output)
		}
	}
	for _, output := range []string{"Hi", "Sure", channelconfig.NoReplyResult + " because"} {
		if !visibleReplyDecided(output) {
			t.Fatalf("typing did not start for visible output %q", output)
		}
	}
}

func TestDeliveryFailureIsNotRetried(t *testing.T) {
	var audit strings.Builder
	managed := newFakeManager()
	delivery := &fakeTransport{err: errors.New("ambiguous")}
	c, err := NewWithManager(context.Background(), managed, delivery, 100, &audit, "Test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	submit(t, c, "surface", "conversation", "input", "target")
	c.handleDispatch("conversation", dispatch.Event{Type: "agent.output.delta", InputID: "input", ItemID: "output", Delta: "reply"})
	c.handleDispatch("conversation", dispatch.Event{Type: "turn.completed", InputID: "input"})
	if len(delivery.outcomes) != 1 || !strings.Contains(audit.String(), "class=uncertain") {
		t.Fatalf("outcomes=%#v audit=%q", delivery.outcomes, audit.String())
	}
}

func TestContinuationTurnParkingAuditIsContentFree(t *testing.T) {
	var audit strings.Builder
	delivery := &fakeTransport{}
	c, err := NewWithManager(context.Background(), newFakeManager(), delivery, 100, &audit, "Test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	submit(t, c, "surface", "guild", "message-secret", "target")
	c.handleDispatch("guild", dispatch.Event{Type: "turn.started", InputID: "message-secret", TurnID: "old-turn"})
	c.handleDispatch("guild", dispatch.Event{Type: "agent.output.delta", InputID: "message-secret", TurnID: "old-turn", ItemID: "before", Delta: "I need to ask."})
	c.handleDispatch("guild", dispatch.Event{Type: "interaction.parked", Status: "continuation_turn", InputID: "message-secret", TurnID: "old-turn", Delta: "semantic secret"})
	c.handleDispatch("guild", dispatch.Event{Type: "agent.output.delta", InputID: "message-secret", TurnID: "old-turn", ItemID: "late-old", Delta: "Old control output."})
	c.handleDispatch("guild", dispatch.Event{Type: "turn.started", InputID: "message-secret", TurnID: "new-turn"})
	c.handleDispatch("guild", dispatch.Event{Type: "agent.output.delta", InputID: "message-secret", TurnID: "old-turn", ItemID: "later-old", Delta: "Still old."})
	c.handleDispatch("guild", dispatch.Event{Type: "agent.output.delta", InputID: "message-secret", TurnID: "new-turn", ItemID: "after", Delta: "Final answer."})
	c.handleDispatch("guild", dispatch.Event{Type: "turn.completed", InputID: "message-secret", TurnID: "new-turn"})
	got := audit.String()
	if got != "Test interaction parked class=continuation_turn\n" || strings.Contains(got, "message-secret") || strings.Contains(got, "semantic secret") {
		t.Fatalf("audit = %q", got)
	}
	if len(delivery.outcomes) != 1 || strings.Join(delivery.outcomes[0].Parts, "") != "Final answer." {
		t.Fatalf("outcomes = %#v", delivery.outcomes)
	}
}
