package schedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	cronlib "github.com/robfig/cron/v3"

	"hctl/internal/dispatch"
	"hctl/internal/project"
)

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}

type realClock struct{}

func (realClock) Now() time.Time                 { return time.Now().UTC() }
func (realClock) NewTimer(d time.Duration) Timer { return clockTimer{time.NewTimer(d)} }

type clockTimer struct{ *time.Timer }

func (t clockTimer) C() <-chan time.Time { return t.Timer.C }

type TaskCoordinator interface {
	Recover(string) ([]string, error)
	Start(context.Context, string, dispatch.Submission, func(dispatch.Event) error) (<-chan error, error)
	StopAdmission()
	Wait()
}

type Diagnostic struct {
	Kind         string
	Schedule     string
	OccurrenceID string
	Status       string
	Reason       string
}

type RunnerOptions struct {
	Clock Clock
	Emit  func(Diagnostic) error
}

type parsedSchedule struct {
	source project.Schedule
	cron   cronlib.Schedule
	next   time.Time
}

type occurrenceResult struct {
	name         string
	occurrenceID string
	status       string
	reason       string
	fatal        bool
}

type runnerLifecycle struct {
	runtime      TaskCoordinator
	emit         func(Diagnostic) error
	stopping     bool
	done         <-chan struct{}
	reason       string
	err          error
	outputBroken bool
}

func (s *runnerLifecycle) stop(reason string, cause error) {
	if cause != nil && s.err == nil {
		s.err = cause
	}
	if s.stopping {
		return
	}
	s.stopping = true
	s.done = nil
	s.reason = reason
	s.runtime.StopAdmission()
	if s.outputBroken {
		return
	}
	if err := s.emit(Diagnostic{Kind: "runtime", Status: "stopping", Reason: reason}); err != nil {
		s.outputBroken = true
		s.err = errors.New("cannot write schedule lifecycle diagnostics")
	}
}

func (s *runnerLifecycle) outputFailure() {
	s.outputBroken = true
	s.stop("event_output_failure", errors.New("cannot write schedule lifecycle diagnostics"))
}

// RunForeground evaluates the schedules loaded by the caller once. It does no
// source reload, setup mutation, or downtime backfill.
func RunForeground(ctx context.Context, schedules []project.Schedule, runtime TaskCoordinator, options RunnerOptions) error {
	if len(schedules) == 0 {
		return errors.New("agent project defines no schedules")
	}
	if runtime == nil {
		return errors.New("schedule task runtime is required")
	}
	clock := options.Clock
	if clock == nil {
		clock = realClock{}
	}
	emit := options.Emit
	if emit == nil {
		return errors.New("schedule lifecycle receiver is required")
	}
	startup := clock.Now().UTC()
	parsed := make([]parsedSchedule, 0, len(schedules))
	for _, source := range schedules {
		expression, err := cronlib.ParseStandard(source.Cron)
		if err != nil {
			return errors.New("compiled schedule has an invalid cron expression")
		}
		parsed = append(parsed, parsedSchedule{source: source, cron: expression, next: expression.Next(startup)})
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].source.Name < parsed[j].source.Name })
	for _, item := range parsed {
		recovered, err := runtime.Recover(conversationID(item.source.Name))
		if err != nil {
			return errors.New("cannot recover durable schedule task state")
		}
		for _, id := range recovered {
			if err := emit(Diagnostic{Kind: "occurrence", Schedule: item.source.Name, OccurrenceID: id, Status: "uncertain", Reason: "dispatcher_restarted"}); err != nil {
				return errors.New("cannot write schedule lifecycle diagnostics")
			}
		}
	}
	if err := emit(Diagnostic{Kind: "runtime", Status: "started"}); err != nil {
		return errors.New("cannot write schedule lifecycle diagnostics")
	}

	results := make(chan occurrenceResult, len(parsed))
	inFlight := make(map[string]bool, len(parsed))
	lastAdmitted := make(map[string]time.Time, len(parsed))
	lifecycle := &runnerLifecycle{runtime: runtime, emit: emit, done: ctx.Done()}

	for {
		if lifecycle.stopping && len(inFlight) == 0 {
			runtime.Wait()
			if !lifecycle.outputBroken {
				if err := emit(Diagnostic{Kind: "runtime", Status: "stopped", Reason: lifecycle.reason}); err != nil && lifecycle.err == nil {
					lifecycle.err = errors.New("cannot write schedule lifecycle diagnostics")
				}
			}
			return lifecycle.err
		}

		var timer Timer
		var timerC <-chan time.Time
		if !lifecycle.stopping {
			now := clock.Now().UTC()
			earliest := parsed[0].next
			for i := 1; i < len(parsed); i++ {
				if parsed[i].next.Before(earliest) {
					earliest = parsed[i].next
				}
			}
			delay := earliest.Sub(now)
			if delay < 0 {
				delay = 0
			}
			timer = clock.NewTimer(delay)
			timerC = timer.C()
		}

		select {
		case <-lifecycle.done:
			if timer != nil {
				timer.Stop()
			}
			lifecycle.stop("signal", nil)
		case result := <-results:
			if timer != nil {
				timer.Stop()
			}
			delete(inFlight, result.name)
			if !lifecycle.outputBroken {
				if err := emit(Diagnostic{Kind: "occurrence", Schedule: result.name, OccurrenceID: result.occurrenceID, Status: result.status, Reason: result.reason}); err != nil {
					result.fatal = true
					result.reason = "event_output_failure"
					lifecycle.outputFailure()
				}
			}
			if result.fatal {
				lifecycle.stop(result.reason, errors.New("schedule task coordinator failed"))
			}
		case <-timerC:
			if ctx.Err() != nil {
				lifecycle.stop("signal", nil)
				continue
			}
			now := clock.Now().UTC()
			minute := now.Truncate(time.Minute)
			for i := range parsed {
				if ctx.Err() != nil {
					lifecycle.stop("signal", nil)
					break
				}
				item := &parsed[i]
				if item.next.After(now) {
					continue
				}
				current := item.cron.Next(minute.Add(-time.Nanosecond))
				item.next = item.cron.Next(now)
				if !current.Equal(minute) || !current.After(startup) || !current.After(lastAdmitted[item.source.Name]) {
					continue
				}
				id := OccurrenceID(item.source.Name, current)
				if inFlight[item.source.Name] {
					if err := emit(Diagnostic{Kind: "occurrence", Schedule: item.source.Name, OccurrenceID: id, Status: "skipped", Reason: "overlap"}); err != nil {
						lifecycle.outputFailure()
						break
					}
					continue
				}
				inFlight[item.source.Name] = true
				lastAdmitted[item.source.Name] = current
				if err := startOccurrence(item.source, id, runtime, results); err != nil {
					delete(inFlight, item.source.Name)
					lifecycle.stop("coordinator_failure", errors.New("schedule task coordinator failed"))
					break
				}
				if err := emit(Diagnostic{Kind: "occurrence", Schedule: item.source.Name, OccurrenceID: id, Status: "started"}); err != nil {
					lifecycle.outputFailure()
					break
				}
			}
		}
	}
}

func startOccurrence(source project.Schedule, id string, runtime TaskCoordinator, results chan<- occurrenceResult) error {
	result := occurrenceResult{name: source.Name, occurrenceID: id}
	driverFailure := false
	terminal := false
	emit := func(event dispatch.Event) error {
		if event.InputID != "" && event.InputID != id {
			return nil
		}
		switch event.Type {
		case "input.duplicate":
			result.status = event.Status
			result.reason = event.Reason
			terminal = true
		case "driver.process_failed":
			driverFailure = true
			result.status = "failed"
			result.reason = boundedFailureReason(event.Status)
		case "turn.completed", "turn.failed", "turn.cancelled", "turn.uncertain":
			terminal = true
			result.status = event.Type[len("turn."):]
			result.reason = boundedFailureReason(event.Reason)
			if result.reason == "" {
				result.reason = boundedFailureReason(event.Status)
			}
		}
		return nil
	}
	done, err := runtime.Start(context.Background(), conversationID(source.Name), dispatch.Submission{InputID: id, Text: string(source.Prompt)}, emit)
	if err != nil {
		return err
	}
	go func() {
		err := <-done
		if result.status == "" {
			result.status = "failed"
			result.reason = "coordinator_failure"
			result.fatal = !driverFailure
		} else if err != nil && !driverFailure && !terminal && !errors.Is(err, dispatch.ErrTurnDeadlineExceeded) {
			result.fatal = true
			result.reason = "coordinator_failure"
		}
		results <- result
	}()
	return nil
}

func boundedFailureReason(reason string) string {
	switch reason {
	case "deadline_exceeded", "startup_failure", "process_failure", "cancelled", "failed":
		return reason
	default:
		return ""
	}
}

func OccurrenceID(name string, scheduled time.Time) string {
	minute := scheduled.UTC().Truncate(time.Minute).Format("2006-01-02T15:04Z")
	digest := sha256.Sum256([]byte(name + "\x00" + minute))
	return "occ-" + hex.EncodeToString(digest[:])
}
