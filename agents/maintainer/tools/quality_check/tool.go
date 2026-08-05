package qualitycheck

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

const Description = "Run the repository's fixed quality gate, ./scripts/check.sh."

type Input struct{}

type Output struct {
	Passed     bool   `json:"passed"`
	DurationMS int64  `json:"duration_ms"`
	Summary    string `json:"summary"`
}

func Execute(ctx context.Context, _ Input) (Output, error) {
	started := time.Now()
	command := exec.CommandContext(ctx, "./scripts/check.sh")
	output := &boundedBuffer{remaining: 64 << 10}
	command.Stdout, command.Stderr = output, output
	err := command.Run()
	summary := strings.TrimSpace(output.String())
	if len(summary) > 4000 {
		summary = summary[len(summary)-4000:]
	}
	if summary == "" {
		if err != nil {
			summary = err.Error()
		} else {
			summary = "quality checks passed"
		}
	}
	return Output{Passed: err == nil, DurationMS: time.Since(started).Milliseconds(), Summary: summary}, nil
}

type boundedBuffer struct {
	bytes.Buffer
	remaining int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > buffer.remaining {
		data = data[:max(buffer.remaining, 0)]
	}
	buffer.remaining -= len(data)
	_, _ = buffer.Buffer.Write(data)
	return original, nil
}
