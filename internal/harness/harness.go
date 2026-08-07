package harness

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"

	"hctl/internal/interaction"
)

type Input struct {
	ID   string
	Text string
}

type Event struct {
	Type      string
	SessionID string
	TurnID    string
	ItemID    string
	Delta     string
	Status    string
	// RequestInput is internal structured control data, never a public dispatch
	// payload. The dispatcher acknowledges it only after durable handoff.
	RequestInput *RequestInputEvent
}

type RequestInputEvent struct {
	CorrelationID string
	Request       interaction.Request
	Reply         chan<- RequestInputAcknowledgement
	rootProof     *requestInputRootProof
}

type RequestInputAcknowledgement struct {
	Accepted bool
	Status   string
	Result   RequestInputToolResult
}

// RequestInputToolResult is chosen by a harness continuation strategy after a
// successful durable acknowledgement. Generic MCP code only validates and
// encodes its bounded disposition.
type RequestInputToolResult struct {
	Disposition RequestInputDisposition
}

type RequestInputDisposition string

const (
	RequestInputDeferred         RequestInputDisposition = "deferred"
	RequestInputContinuationTurn RequestInputDisposition = "continuation_turn"
)

func (d RequestInputDisposition) Valid() bool {
	return d == RequestInputDeferred || d == RequestInputContinuationTurn
}

type requestInputRootProof struct{}

// NewRootRequestInputEvent is the harness-owned construction boundary for a
// request whose root ancestry has already been proven by the adapter. A zero
// RequestInputEvent carries no proof and is always rejected by dispatch.
func NewRootRequestInputEvent(correlationID string, request interaction.Request, reply chan<- RequestInputAcknowledgement) *RequestInputEvent {
	return &RequestInputEvent{CorrelationID: correlationID, Request: request, Reply: reply, rootProof: &requestInputRootProof{}}
}

// ProvenRoot is checked only by the dispatcher. Callers cannot set the proof
// through a struct literal or serialized payload.
func (e *RequestInputEvent) ProvenRoot() bool { return e != nil && e.rootProof != nil }

type TurnResult struct {
	SessionID string
	TurnID    string
	Status    string
}

type ExecutionPolicy string

const (
	PolicyDefault        ExecutionPolicy = "default"
	PolicyReadOnly       ExecutionPolicy = "read-only"
	PolicyWorkspaceWrite ExecutionPolicy = "workspace-write"
)

type OpenRequest struct {
	Root                string
	ResumeID            string
	Policy              ExecutionPolicy
	ManagedRequestInput bool
}

type Driver interface {
	Name() string
	Executable() string
	Verify(context.Context) error
	Open(context.Context, OpenRequest) (Session, error)
}

// ContinuationTurnDriver starts one later turn in an already persisted native
// session. Manager owns capacity and lifecycle around this narrow side effect.
type ContinuationTurnDriver interface {
	ContinueTurn(context.Context, OpenRequest, string, interaction.ContinuationIntent, func(Event)) interaction.ContinuationResult
}

type Session interface {
	InitialEvents() []Event
	RunTurn(context.Context, Input, func(Event)) (TurnResult, error)
	Close() error
	Abort()
}

func ResolveExecutable(name, override string) (string, error) {
	executable := override
	if executable == "" {
		executable = name
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return "", fmt.Errorf("%s executable was not found", name)
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("cannot resolve harness executable")
	}
	return abs, nil
}
