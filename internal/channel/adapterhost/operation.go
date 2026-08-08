package adapterhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
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
	command.Stderr = &boundedWriter{remaining: channeladapter.MaxStderrBytes, writer: terminal}
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
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return channeladapter.OperationResult{}, ctx.Err()
	case <-timer.C:
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation timed out")
	}
	// A successful parent must not leave descendants in its private process
	// group. This is harmless when the group is already empty.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if waitErr != nil {
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation failed; run status or setup for the selected profile")
	}
	data := bytes.TrimSpace(output.data)
	result, err := channeladapter.DecodeOperationResult(data)
	if err != nil {
		return channeladapter.OperationResult{}, errors.New("channel-adapter operation returned an invalid non-secret result")
	}
	return result, nil
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

type boundedWriter struct {
	remaining int
	writer    io.Writer
}

func (writer *boundedWriter) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > writer.remaining {
		data = data[:max(writer.remaining, 0)]
	}
	writer.remaining -= len(data)
	if len(data) > 0 && writer.writer != nil {
		_, _ = writer.writer.Write(data)
	}
	return original, nil
}
