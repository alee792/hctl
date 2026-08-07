// Package controller coordinates transport-neutral channel conversations.
package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"hctl/internal/channelconfig"
	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/project"
	"hctl/internal/worktree"
)

const DefaultMaxOutputRunes = 6*2000 - 64

type Inbound struct {
	SurfaceID      string
	ConversationID string
	InputID        string
	Text           string
	Target         any
}

type Failure string

const (
	FailureNone        Failure = ""
	FailureAdmission   Failure = "admission_failed"
	FailureProcess     Failure = "process_failed"
	FailureCancelled   Failure = "cancelled"
	FailureUncertain   Failure = "uncertain"
	FailureNoOutput    Failure = "no_output"
	FailureWriteAccess Failure = "write_access_failed"
	FailureWorkspace   Failure = "workspace_failed"
)

type Outcome struct {
	InputID   string
	Target    any
	Parts     []string
	Truncated bool
	Failure   Failure
}

// Delivery is implemented by a vendor adapter. Target values are opaque to the
// controller and are never sent to dispatch or durable state.
type Delivery interface {
	Typing(any) error
	Deliver(Outcome) error
}

type Config struct {
	Project        *project.Project
	Driver         harness.Driver
	TurnTimeout    time.Duration
	IdleTimeout    time.Duration
	MaxResident    int
	MaxActive      int
	MaxOutputRunes int
	Executable     string
	Audit          io.Writer
	AuditPrefix    string
}

type Status struct {
	Conversation dispatch.ConversationStatus
	Capacity     dispatch.CapacityStatus
}

type Manager interface {
	Submit(context.Context, string, dispatch.Submission) (dispatch.SubmissionResult, error)
	Elevate(context.Context, string, dispatch.Submission) (dispatch.SubmissionResult, error)
	Status(string) dispatch.ConversationStatus
	Capacity() dispatch.CapacityStatus
	Reset(string) error
	Done() <-chan struct{}
	Err() error
	Close()
}

type pendingTurn struct {
	target            any
	writeContinuation bool
	outputs           []*bufferedOutput
	byItem            map[string]*bufferedOutput
	runes             int
	truncated         bool
}

type bufferedOutput struct {
	text strings.Builder
}

type surface struct {
	conversation string
	turns        map[string]*pendingTurn
}

type Controller struct {
	ctx            context.Context
	cancel         context.CancelFunc
	manager        Manager
	delivery       Delivery
	audit          io.Writer
	auditPrefix    string
	maxOutputRunes int

	mu             sync.Mutex
	surfaces       map[string]*surface
	byConversation map[string]*surface
	resetting      map[string]bool
	closed         bool
}

func New(ctx context.Context, config Config, delivery Delivery) (*Controller, error) {
	if config.Project == nil || config.Driver == nil {
		return nil, errors.New("channel controller requires a project and harness driver")
	}
	if config.TurnTimeout <= 0 || config.IdleTimeout <= 0 {
		return nil, errors.New("channel controller timeouts must be positive")
	}
	if delivery == nil {
		return nil, errors.New("channel controller requires a delivery adapter")
	}
	controllerCtx, cancel := context.WithCancel(ctx)
	c := newWithManager(controllerCtx, cancel, nil, delivery, config.MaxOutputRunes, config.Audit, config.AuditPrefix)
	emit := func(conversation string, event dispatch.Event) error {
		c.handleDispatch(conversation, event)
		return nil
	}
	workspaceManager, _ := worktree.New(controllerCtx, config.Project, config.Executable)
	var managed *dispatch.Manager
	var err error
	if workspaceManager != nil {
		managed, err = dispatch.NewManagerWithWorkspaceAndLimits(controllerCtx, config.Project, config.Driver, config.TurnTimeout, config.IdleTimeout, config.MaxResident, config.MaxActive, emit, workspaceManager)
	} else {
		managed, err = dispatch.NewManagerWithLimits(controllerCtx, config.Project, config.Driver, config.TurnTimeout, config.IdleTimeout, config.MaxResident, config.MaxActive, emit)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	c.manager = managed
	for _, diagnostic := range managed.Diagnostics() {
		_, _ = fmt.Fprintf(c.audit, "%s worktree reconciliation: %s\n", c.auditPrefix, diagnostic)
	}
	return c, nil
}

// NewWithManager exposes the controller seam to internal adapters and tests.
// Production runtimes should use New so the controller owns dispatcher setup.
func NewWithManager(ctx context.Context, managed Manager, delivery Delivery, maxOutputRunes int, audit io.Writer, auditPrefix string) (*Controller, error) {
	if managed == nil || delivery == nil {
		return nil, errors.New("channel controller requires a manager and delivery adapter")
	}
	controllerCtx, cancel := context.WithCancel(ctx)
	return newWithManager(controllerCtx, cancel, managed, delivery, maxOutputRunes, audit, auditPrefix), nil
}

func newWithManager(ctx context.Context, cancel context.CancelFunc, managed Manager, delivery Delivery, maxOutputRunes int, audit io.Writer, auditPrefix string) *Controller {
	if maxOutputRunes == 0 {
		maxOutputRunes = DefaultMaxOutputRunes
	}
	if audit == nil {
		audit = io.Discard
	}
	if auditPrefix == "" {
		auditPrefix = "Channel"
	}
	return &Controller{
		ctx: ctx, cancel: cancel, manager: managed, delivery: delivery, audit: audit,
		auditPrefix: auditPrefix, maxOutputRunes: maxOutputRunes,
		surfaces: map[string]*surface{}, byConversation: map[string]*surface{}, resetting: map[string]bool{},
	}
}

func (c *Controller) Submit(ctx context.Context, incoming Inbound) (dispatch.SubmissionResult, error) {
	if incoming.SurfaceID == "" || incoming.ConversationID == "" || incoming.InputID == "" {
		return dispatch.SubmissionResult{}, errors.New("channel input requires stable surface, conversation, and input identifiers")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return dispatch.SubmissionResult{}, dispatch.ErrManagerClosed
	}
	if c.resetting[incoming.SurfaceID] {
		c.mu.Unlock()
		return dispatch.SubmissionResult{}, dispatch.ErrConversationBusy
	}
	current := c.surfaces[incoming.SurfaceID]
	if current == nil {
		if existing := c.byConversation[incoming.ConversationID]; existing != nil {
			c.mu.Unlock()
			return dispatch.SubmissionResult{}, errors.New("channel conversation is already registered to another surface")
		}
		current = &surface{conversation: incoming.ConversationID, turns: map[string]*pendingTurn{}}
		c.surfaces[incoming.SurfaceID] = current
		c.byConversation[incoming.ConversationID] = current
	} else if current.conversation != incoming.ConversationID {
		c.mu.Unlock()
		return dispatch.SubmissionResult{}, errors.New("channel surface changed conversation identity")
	}
	if _, exists := current.turns[incoming.InputID]; exists {
		c.mu.Unlock()
		return dispatch.SubmissionResult{Status: "queued", Duplicate: true}, nil
	}
	current.turns[incoming.InputID] = &pendingTurn{target: incoming.Target}
	c.mu.Unlock()

	result, err := c.manager.Submit(ctx, incoming.ConversationID, dispatch.Submission{InputID: incoming.InputID, Text: incoming.Text})
	if err != nil || result.Status != "queued" && result.Status != "active" {
		c.drop(current, incoming.InputID)
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, dispatch.ErrManagerClosed) {
		c.deliver(Outcome{InputID: incoming.InputID, Target: incoming.Target, Failure: FailureAdmission})
	}
	return result, err
}

func (c *Controller) Status(conversationID string) Status {
	return Status{Conversation: c.manager.Status(conversationID), Capacity: c.manager.Capacity()}
}

func (c *Controller) Reset(surfaceID, conversationID string) error {
	c.mu.Lock()
	current := c.surfaces[surfaceID]
	if c.resetting[surfaceID] || current != nil && (current.conversation != conversationID || len(current.turns) != 0) {
		c.mu.Unlock()
		return dispatch.ErrConversationBusy
	}
	c.resetting[surfaceID] = true
	c.mu.Unlock()
	if err := c.manager.Reset(conversationID); err != nil {
		c.mu.Lock()
		delete(c.resetting, surfaceID)
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	delete(c.resetting, surfaceID)
	if c.surfaces[surfaceID] == current {
		delete(c.surfaces, surfaceID)
		delete(c.byConversation, conversationID)
	}
	c.mu.Unlock()
	return nil
}

func (c *Controller) Done() <-chan struct{} { return c.manager.Done() }
func (c *Controller) Err() error            { return c.manager.Err() }

func (c *Controller) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.manager.Close()
}

func (c *Controller) handleDispatch(conversation string, event dispatch.Event) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	current := c.byConversation[conversation]
	if current == nil {
		c.mu.Unlock()
		return
	}
	turn := current.turns[event.InputID]
	if turn == nil {
		c.mu.Unlock()
		return
	}
	if event.Type == "agent.output.delta" {
		appendBounded(turn, event.ItemID, event.Delta, c.maxOutputRunes)
		showTyping := visibleReplyDecided(combinedOutput(turn))
		target := turn.target
		c.mu.Unlock()
		if showTyping {
			_ = c.delivery.Typing(target)
		}
		return
	}
	if !event.Terminal() {
		c.mu.Unlock()
		return
	}
	content := strings.TrimSpace(combinedOutput(turn))
	parts := outputParts(turn)
	truncated := turn.truncated
	if event.Type == "turn.completed" && suppressedControl(content) == channelconfig.RequestWriteAccessResult && !turn.writeContinuation {
		delete(current.turns, event.InputID)
		continuationID := event.InputID + ":write"
		current.turns[continuationID] = &pendingTurn{target: turn.target, writeContinuation: true}
		c.mu.Unlock()
		_, _ = fmt.Fprintf(c.audit, "%s turn suppressed input_id=%s class=write_access_requested\n", c.auditPrefix, event.InputID)
		go c.continueWritable(current, conversation, continuationID)
		return
	}
	delete(current.turns, event.InputID)
	c.mu.Unlock()

	if event.Type != "turn.completed" {
		c.deliver(Outcome{InputID: event.InputID, Target: turn.target, Failure: terminalFailure(event.Type)})
		return
	}
	if suppressedControl(content) == channelconfig.NoReplyResult {
		_, _ = fmt.Fprintf(c.audit, "%s turn suppressed input_id=%s class=no_reply\n", c.auditPrefix, event.InputID)
		return
	}
	outcome := Outcome{InputID: event.InputID, Target: turn.target, Parts: parts, Truncated: truncated}
	if suppressedControl(content) == channelconfig.RequestWriteAccessResult {
		outcome.Parts = nil
		outcome.Failure = FailureWriteAccess
	} else if content == "" {
		_, _ = fmt.Fprintf(c.audit, "%s turn empty input_id=%s class=%s\n", c.auditPrefix, event.InputID, event.Type)
		outcome.Failure = terminalFailure(event.Type)
	}
	c.deliver(outcome)
}

func (c *Controller) continueWritable(current *surface, conversation, inputID string) {
	result, err := c.manager.Elevate(c.ctx, conversation, dispatch.Submission{InputID: inputID, Text: channelconfig.WriteContinuationPrompt})
	if err == nil && (result.Status == "queued" || result.Status == "active") {
		return
	}
	c.mu.Lock()
	turn := current.turns[inputID]
	delete(current.turns, inputID)
	c.mu.Unlock()
	if err == nil && result.Status == "completed" {
		return
	}
	_, _ = fmt.Fprintf(c.audit, "%s elevation failed input_id=%s class=workspace_failure\n", c.auditPrefix, inputID)
	if turn != nil {
		c.deliver(Outcome{InputID: inputID, Target: turn.target, Failure: FailureWorkspace})
	}
}

func (c *Controller) deliver(outcome Outcome) {
	if err := c.delivery.Deliver(outcome); err != nil {
		_, _ = fmt.Fprintf(c.audit, "%s delivery failed input_id=%s class=uncertain\n", c.auditPrefix, outcome.InputID)
	}
}

func (c *Controller) drop(current *surface, inputID string) {
	c.mu.Lock()
	delete(current.turns, inputID)
	c.mu.Unlock()
}

func suppressedControl(output string) string {
	switch strings.TrimSpace(output) {
	case channelconfig.NoReplyResult:
		return channelconfig.NoReplyResult
	case channelconfig.RequestWriteAccessResult:
		return channelconfig.RequestWriteAccessResult
	default:
		return ""
	}
}

func terminalFailure(eventType string) Failure {
	switch eventType {
	case "turn.failed", "driver.process_failed":
		return FailureProcess
	case "turn.cancelled":
		return FailureCancelled
	case "turn.uncertain":
		return FailureUncertain
	default:
		return FailureNoOutput
	}
}

func visibleReplyDecided(output string) bool {
	candidate := strings.TrimLeftFunc(output, unicode.IsSpace)
	if candidate == "" {
		return false
	}
	for _, control := range []string{channelconfig.NoReplyResult, channelconfig.RequestWriteAccessResult} {
		if strings.HasPrefix(control, candidate) {
			return false
		}
	}
	return true
}

func appendBounded(turn *pendingTurn, itemID, value string, limit int) {
	if turn.truncated || value == "" {
		return
	}
	if itemID == "" {
		itemID = "default"
	}
	if turn.byItem == nil {
		turn.byItem = map[string]*bufferedOutput{}
	}
	output := turn.byItem[itemID]
	if output == nil {
		output = &bufferedOutput{}
		turn.byItem[itemID] = output
		turn.outputs = append(turn.outputs, output)
	}
	remaining := limit - turn.runes
	for index := range value {
		if remaining == 0 {
			output.text.WriteString(value[:index])
			turn.truncated = true
			return
		}
		remaining--
		turn.runes++
	}
	output.text.WriteString(value)
}

func combinedOutput(turn *pendingTurn) string {
	values := make([]string, 0, len(turn.outputs))
	for _, output := range turn.outputs {
		values = append(values, output.text.String())
	}
	return strings.Join(values, "\n\n")
}

func outputParts(turn *pendingTurn) []string {
	values := make([]string, 0, len(turn.outputs))
	for _, output := range turn.outputs {
		if value := strings.TrimSpace(output.text.String()); value != "" {
			values = append(values, value)
		}
	}
	return values
}
