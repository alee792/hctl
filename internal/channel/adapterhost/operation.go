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
	"unicode/utf8"

	"hctl/channeladapter"
	"hctl/internal/integration"
)

const setupOperationTimeout = 10 * time.Minute

// RunOperation executes one exact bounded setup/status/remove mode. Setup and
// remove may use trusted input and the diagnostic terminal, while stdout
// remains one closed non-secret result object.
func RunOperation(ctx context.Context, mode integration.ChannelAdapterMode, launch Launch, environment []string, input io.Reader, terminal io.Writer) (channeladapter.OperationResult, error) {
	if err := ctx.Err(); err != nil {
		return channeladapter.OperationResult{}, err
	}
	timeout := channeladapter.CommandTimeout
	switch mode {
	case integration.ChannelAdapterSetup:
		timeout = setupOperationTimeout
	case integration.ChannelAdapterStatus:
		input = nil
	case integration.ChannelAdapterRemove:
	default:
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation mode is invalid")
	}
	command := exec.Command(launch.Command, launch.Arguments...)
	command.Dir = launch.WorkingDirectory
	command.Env = append([]string(nil), environment...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdin = input
	diagnostics := newDiagnosticWriter(terminal, environment)
	defer diagnostics.Flush()
	command.Stderr = diagnostics
	var output boundedBuffer
	output.maximum = channeladapter.MaxOperationResultBytes + 1
	command.Stdout = &output
	if err := command.Start(); err != nil {
		return channeladapter.OperationResult{}, errors.New("cannot start channel-adapter operation")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	timer := time.NewTimer(timeout)
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
	redactions          [][]byte
	onProtocolViolation func()
	secretPending       []byte
	protocolLine        []byte
	linePrefix          []byte
	utf8Pending         []byte
	atLineStart         bool
	outputLineStart     bool
	flushed             bool
}

func newDiagnosticWriter(writer io.Writer, environment []string) *diagnosticWriter {
	if writer == nil {
		writer = io.Discard
	}
	var redactions [][]byte
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if found && value != "" && (strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "CREDENTIAL")) {
			redactions = append(redactions, []byte(value))
		}
	}
	return &diagnosticWriter{remaining: channeladapter.MaxStderrBytes, writer: writer, redactions: redactions, atLineStart: true, outputLineStart: true}
}

func (writer *diagnosticWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	original := len(data)
	if !writer.flushed {
		writer.consume(data)
	}
	return original, nil
}

func (writer *diagnosticWriter) consume(data []byte) {
	for _, value := range data {
		if writer.protocolLine != nil {
			if len(writer.protocolLine) < channeladapter.MaxFrameBytes {
				writer.protocolLine = append(writer.protocolLine, value)
			}
			if value == '\n' {
				writer.finishProtocolLine()
			}
			continue
		}
		if writer.atLineStart {
			if value == ' ' || value == '\t' || value == '\r' {
				writer.linePrefix = append(writer.linePrefix, value)
				continue
			}
			if value == '{' || value == '[' {
				writer.protocolLine = append(writer.protocolLine, writer.linePrefix...)
				writer.protocolLine = append(writer.protocolLine, value)
				writer.linePrefix = nil
				writer.atLineStart = false
				continue
			}
			writer.feedSecret(writer.linePrefix)
			writer.linePrefix = nil
			writer.atLineStart = false
		}
		writer.feedSecret([]byte{value})
		if value == '\n' {
			writer.atLineStart = true
		}
	}
}

func (writer *diagnosticWriter) finishProtocolLine() {
	protocolLike := bytes.Contains(writer.protocolLine, []byte(`"protocol_version"`)) && bytes.Contains(writer.protocolLine, []byte(`"payload"`))
	if protocolLike {
		writer.feedSecret([]byte("[protocol-like stderr redacted]\n"))
		if writer.onProtocolViolation != nil {
			writer.onProtocolViolation()
		}
	} else {
		writer.feedSecret(writer.protocolLine)
	}
	writer.protocolLine = nil
	writer.atLineStart = true
}

func (writer *diagnosticWriter) feedSecret(data []byte) {
	if len(writer.redactions) == 0 {
		writer.feedUTF8(data)
		return
	}
	for _, value := range data {
		writer.secretPending = append(writer.secretPending, value)
		for len(writer.secretPending) > 0 {
			matched, prefix := false, false
			for _, secret := range writer.redactions {
				if bytes.Equal(writer.secretPending, secret) {
					matched = true
					break
				}
				if bytes.HasPrefix(secret, writer.secretPending) {
					prefix = true
				}
			}
			if matched {
				writer.feedUTF8([]byte("[redacted]"))
				writer.secretPending = nil
				break
			}
			if prefix {
				break
			}
			writer.feedUTF8(writer.secretPending[:1])
			writer.secretPending = writer.secretPending[1:]
		}
	}
}

func (writer *diagnosticWriter) feedUTF8(data []byte) {
	writer.utf8Pending = append(writer.utf8Pending, data...)
	for len(writer.utf8Pending) > 0 {
		if !utf8.FullRune(writer.utf8Pending) {
			return
		}
		value, size := utf8.DecodeRune(writer.utf8Pending)
		if value == utf8.RuneError && size == 1 {
			writer.emitRune(utf8.RuneError)
			writer.utf8Pending = writer.utf8Pending[1:]
			continue
		}
		writer.emitRune(value)
		writer.utf8Pending = writer.utf8Pending[size:]
	}
}

func (writer *diagnosticWriter) emitRune(value rune) {
	if value < 0x20 && value != '\n' && value != '\t' || value == 0x7f {
		value = ' '
	}
	if writer.outputLineStart {
		writer.writeBounded([]byte("channel-adapter stderr: "))
		writer.outputLineStart = false
	}
	encoded := []byte(string(value))
	if len(encoded) <= writer.remaining {
		writer.writeBounded(encoded)
	} else {
		writer.remaining = 0
	}
	if value == '\n' {
		writer.outputLineStart = true
	}
}

func (writer *diagnosticWriter) writeBounded(data []byte) {
	if writer.remaining <= 0 {
		return
	}
	if len(data) > writer.remaining {
		data = data[:writer.remaining]
	}
	writer.remaining -= len(data)
	_, _ = writer.writer.Write(data)
}

// Flush emits the last safe diagnostic fragment after the child exits. A
// fragment that is still a credential prefix is suppressed rather than
// retaining part of the inherited value.
func (writer *diagnosticWriter) Flush() {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.flushed {
		return
	}
	writer.flushed = true
	if writer.protocolLine != nil {
		writer.finishProtocolLine()
	}
	writer.feedSecret(writer.linePrefix)
	writer.linePrefix = nil
	if len(writer.secretPending) > 0 {
		prefix := false
		for _, secret := range writer.redactions {
			if bytes.HasPrefix(secret, writer.secretPending) {
				prefix = true
				break
			}
		}
		if prefix {
			writer.feedUTF8([]byte("[redacted]"))
		} else {
			writer.feedUTF8(writer.secretPending)
		}
		writer.secretPending = nil
	}
	if len(writer.utf8Pending) > 0 {
		writer.emitRune(utf8.RuneError)
		writer.utf8Pending = nil
	}
}
