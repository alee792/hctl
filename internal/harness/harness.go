package harness

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
)

type Input struct {
	ID   string
	Text string
}

type Event struct {
	Type      string
	SessionID string
	TurnID    string
	Delta     string
	Status    string
}

type TurnResult struct {
	SessionID string
	TurnID    string
	Status    string
}

type Driver interface {
	Name() string
	Executable() string
	Verify(context.Context) error
	Open(context.Context, string, string) (Session, error)
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
