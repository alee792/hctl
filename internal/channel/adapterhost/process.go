package adapterhost

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"hctl/internal/secureenv"
)

type childProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  io.ReadCloser
	done    chan error
	groupID int
}

func (process *childProcess) Input() io.WriteCloser { return process.input }
func (process *childProcess) Output() io.ReadCloser { return process.output }
func (process *childProcess) Done() <-chan error    { return process.done }
func (process *childProcess) KillTree()             { process.killTree() }

func startChild(descriptor Launch, environment []string, stderr io.Writer) (*childProcess, error) {
	command := exec.Command(descriptor.Command, descriptor.Arguments...)
	command.Dir = descriptor.WorkingDirectory
	command.Env = append([]string(nil), environment...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	input, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("cannot open channel-adapter input")
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, errors.New("cannot open channel-adapter output")
	}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, errors.New("cannot start channel-adapter process")
	}
	process := &childProcess{command: command, input: input, output: output, done: make(chan error, 1), groupID: command.Process.Pid}
	go func() { process.done <- command.Wait(); close(process.done) }()
	return process, nil
}

func (process *childProcess) killTree() {
	if process == nil || process.groupID <= 0 {
		return
	}
	_ = syscall.Kill(-process.groupID, syscall.SIGKILL)
}

// AdapterEnvironment returns the scrubbed child environment plus the one
// explicitly selected compatibility input. Callers never parse its value.
func AdapterEnvironment(compatibilityName string) []string {
	environment := secureenv.Child()
	if compatibilityName == "" {
		return environment
	}
	prefix := compatibilityName + "="
	for _, entry := range os.Environ() {
		if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
			return append(environment, entry)
		}
	}
	return environment
}
