package harness

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"

	"hctl/internal/secureenv"
)

const maxHarnessLine = 1 << 20

type Process struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	scanner *bufio.Scanner
	done    bool
}

func StartProcess(ctx context.Context, dir, executable string, args ...string) (*Process, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = secureenv.Child()
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("cannot open harness input")
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("cannot open harness output")
	}
	cmd.Stderr = &limitedWriter{remaining: 16 << 10}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("cannot start harness process")
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), maxHarnessLine)
	return &Process{cmd: cmd, input: input, scanner: scanner}, nil
}

func (p *Process) Input() io.Writer { return p.input }
func (p *Process) Scan() bool       { return p.scanner.Scan() }
func (p *Process) Bytes() []byte    { return p.scanner.Bytes() }

func (p *Process) ScanError() error {
	if p.scanner.Err() != nil {
		return errors.New("harness output exceeded the bounded line size")
	}
	return nil
}

func (p *Process) Finish() error {
	if p.done {
		return nil
	}
	p.done = true
	_ = p.input.Close()
	if err := p.cmd.Wait(); err != nil {
		return errors.New("harness process exited unsuccessfully")
	}
	return nil
}

func (p *Process) Abort() {
	if p.done {
		return
	}
	p.done = true
	_ = p.input.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	_ = p.cmd.Wait()
}

type limitedWriter struct{ remaining int }

func (w *limitedWriter) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > w.remaining {
		data = data[:max(w.remaining, 0)]
	}
	w.remaining -= len(data)
	return original, nil
}
