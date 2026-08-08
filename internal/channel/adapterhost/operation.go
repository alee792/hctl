package adapterhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"hctl/channeladapter"
)

// RunOperation executes one exact bounded setup/status/remove mode. Setup may
// use the trusted input and diagnostic terminal, while stdout remains one
// closed non-secret result object.
func RunOperation(ctx context.Context, launch Launch, environment []string, input io.Reader, terminal io.Writer) (channeladapter.OperationResult, error) {
	command := exec.Command(launch.Command, launch.Arguments...)
	command.Dir = launch.WorkingDirectory
	command.Env = append([]string(nil), environment...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdin = input
	command.Stderr = newDiagnosticWriter(terminal, environment)
	var output boundedBuffer
	output.maximum = channeladapter.MaxOperationResultBytes + 1
	command.Stdout = &output
	if err := command.Start(); err != nil {
		return channeladapter.OperationResult{}, errors.New("cannot start channel-adapter operation")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(channeladapter.CommandTimeout)
	defer timer.Stop()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		killOperationTree(command, done, terminal)
		return channeladapter.OperationResult{}, ctx.Err()
	case <-timer.C:
		killOperationTree(command, done, terminal)
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation timed out")
	}
	// A successful parent must not leave descendants in its private process
	// group. This is harmless when the group is already empty.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if waitErr != nil {
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation failed; run status or setup for the selected profile")
	}
	if output.overflow {
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation returned an invalid non-secret result")
	}
	data := bytes.TrimSpace(output.data)
	result, err := channeladapter.DecodeOperationResult(data)
	if err != nil {
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation returned an invalid non-secret result")
	}
	return result, nil
}

func killOperationTree(command *exec.Cmd, done <-chan error, diagnostic io.Writer) {
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	timer := time.NewTimer(channeladapter.ForcedExitTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_, _ = fmt.Fprintln(diagnostic, "channel-adapter operation tree was killed without a prompt reap")
	}
}

type boundedBuffer struct {
	data     []byte
	maximum  int
	overflow bool
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := buffer.maximum - len(buffer.data)
	if remaining < len(data) {
		buffer.overflow = true
		data = data[:max(remaining, 0)]
	}
	buffer.data = append(buffer.data, data...)
	return original, nil
}

type diagnosticWriter struct {
	mu                  sync.Mutex
	remaining           int
	writer              io.Writer
	redactions          []string
	onProtocolViolation func()
}

func newDiagnosticWriter(writer io.Writer, environment []string) *diagnosticWriter {
	if writer == nil {
		writer = io.Discard
	}
	var redactions []string
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if found && value != "" && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL")) {
			redactions = append(redactions, value)
		}
	}
	return &diagnosticWriter{remaining: channeladapter.MaxStderrBytes, writer: writer, redactions: redactions}
}

func (writer *diagnosticWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	original := len(data)
	if len(data) > writer.remaining {
		data = data[:max(writer.remaining, 0)]
	}
	writer.remaining -= len(data)
	if len(data) > 0 {
		message := strings.ToValidUTF8(string(data), "�")
		for _, secret := range writer.redactions {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
		if strings.Contains(message, `"protocol_version"`) && strings.Contains(message, `"payload"`) {
			message = "[protocol-like stderr redacted]\n"
			if writer.onProtocolViolation != nil {
				writer.onProtocolViolation()
			}
		}
		message = strings.Map(func(value rune) rune {
			if value < 0x20 && value != '\n' && value != '\t' || value == 0x7f {
				return ' '
			}
			return value
		}, message)
		_, _ = fmt.Fprintf(writer.writer, "channel-adapter stderr: %s", message)
	}
	return original, nil
}
