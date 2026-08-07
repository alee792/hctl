package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"hctl/internal/dispatch"
	"hctl/internal/harness"
	"hctl/internal/project"
)

var runtimeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

type Result struct {
	Name      string
	InputID   string
	Status    string
	Duplicate bool
	Reason    string
	SessionID string
	TurnID    string
}

func Trigger(ctx context.Context, p *project.Project, driver harness.Driver, name, inputID string) (Result, error) {
	return trigger(ctx, p, driver, name, inputID, 0)
}

// TriggerWithTurnTimeout runs one occurrence with a deadline that starts only
// when its native harness turn starts.
func TriggerWithTurnTimeout(ctx context.Context, p *project.Project, driver harness.Driver, name, inputID string, turnTimeout time.Duration) (Result, error) {
	if turnTimeout <= 0 {
		return Result{}, errors.New("schedule turn timeout must be positive")
	}
	return trigger(ctx, p, driver, name, inputID, turnTimeout)
}

func trigger(ctx context.Context, p *project.Project, driver harness.Driver, name, inputID string, turnTimeout time.Duration) (Result, error) {
	if err := dispatch.ValidateInputID(inputID); err != nil {
		return Result{}, err
	}
	source, ok := findSchedule(p.Schedules, name)
	if !ok {
		available := make([]string, len(p.Schedules))
		for index, item := range p.Schedules {
			available[index] = item.Name
		}
		if len(available) == 0 {
			return Result{}, errors.New("agent project defines no schedules")
		}
		return Result{}, fmt.Errorf("unknown schedule %q; available schedules: %s", name, strings.Join(available, ", "))
	}

	result := Result{Name: name, InputID: inputID}
	emit := func(event dispatch.Event) error {
		if event.InputID != "" && event.InputID != inputID {
			return nil
		}
		switch event.Type {
		case "input.rejected", "driver.process_failed":
			result.Status = event.Status
		case "input.duplicate":
			result.Status = event.Status
			result.Duplicate = true
			result.Reason = event.Reason
		case "turn.completed", "turn.failed", "turn.cancelled", "turn.uncertain":
			result.Status = strings.TrimPrefix(event.Type, "turn.")
			result.Reason = event.Reason
			if runtimeID.MatchString(event.SessionID) {
				result.SessionID = event.SessionID
			}
			if runtimeID.MatchString(event.TurnID) {
				result.TurnID = event.TurnID
			}
		}
		return nil
	}
	var err error
	if turnTimeout > 0 {
		err = dispatch.RunTaskWithTurnTimeout(ctx, p, driver, conversationID(name), dispatch.Submission{InputID: inputID, Text: string(source.Prompt)}, emit, turnTimeout)
	} else {
		err = dispatch.RunTask(ctx, p, driver, conversationID(name), dispatch.Submission{InputID: inputID, Text: string(source.Prompt)}, emit)
	}
	if err != nil {
		return result, err
	}
	if result.Status == "" {
		return result, errors.New("schedule trigger ended without a lifecycle status")
	}
	return result, nil
}

func findSchedule(schedules []project.Schedule, name string) (project.Schedule, bool) {
	for _, item := range schedules {
		if item.Name == name {
			return item, true
		}
	}
	return project.Schedule{}, false
}

func conversationID(name string) string {
	digest := sha256.Sum256([]byte(name))
	return "schedule-" + hex.EncodeToString(digest[:12])
}
