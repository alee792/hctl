package harness

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"

	"hctl/internal/secureenv"
)

const maxHarnessLine = 1 << 20

type Process struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	scanner *bufio.Scanner
	mu      sync.Mutex
	closing bool
	exited  chan struct{}
	waitErr error
}

func StartProcess(ctx context.Context, dir, executable string, args ...string) (*Process, error) {
	return StartProcessWithPolicy(ctx, dir, executable, PolicyDefault, args...)
}

func StartProcessWithPolicy(ctx context.Context, dir, executable string, policy ExecutionPolicy, args ...string) (*Process, error) {
	return StartProcessWithPolicyAndEnv(ctx, dir, executable, policy, nil, args...)
}

func StartProcessWithPolicyAndEnv(ctx context.Context, dir, executable string, policy ExecutionPolicy, environment map[string]string, args ...string) (*Process, error) {
	if policy != PolicyDefault && policy != PolicyReadOnly && policy != PolicyWorkspaceWrite {
		return nil, errors.New("unsupported harness execution policy")
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = secureenv.With("HCTL_EXECUTION_POLICY", string(policy))
	for key, value := range environment {
		cmd.Env = replaceEnv(cmd.Env, key, value)
	}
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
	return &Process{cmd: cmd, input: input, scanner: scanner, exited: make(chan struct{})}, nil
}

func replaceEnv(environment []string, key, value string) []string {
	prefix := key + "="
	filtered := environment[:0]
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
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
	p.startWait()
	<-p.exited
	p.mu.Lock()
	err := p.waitErr
	p.mu.Unlock()
	if err != nil {
		return errors.New("harness process exited unsuccessfully")
	}
	return nil
}

func (p *Process) Abort() {
	p.startWait()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.exited
}

func (p *Process) startWait() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return
	}
	p.closing = true
	_ = p.input.Close()
	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		p.waitErr = err
		close(p.exited)
		p.mu.Unlock()
	}()
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
