package schedule

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"

	"hctl/internal/project"
)

type RuntimeLock struct{ lock *flock.Flock }

func AcquireRuntimeLock(p *project.Project) (*RuntimeLock, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, errors.New("cannot resolve schedule runtime lock directory")
	}
	return acquireRuntimeLock(p, filepath.Join(cache, "hctl", "locks"))
}

func acquireRuntimeLock(p *project.Project, directory string) (*RuntimeLock, error) {
	if p == nil {
		return nil, errors.New("cannot identify schedule runtime")
	}
	workspace, err := filepath.EvalSymlinks(p.WorkspaceRoot)
	if err != nil {
		return nil, errors.New("cannot identify schedule runtime workspace")
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, errors.New("cannot identify schedule runtime workspace")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("cannot create schedule runtime lock directory")
	}
	digest := sha256.Sum256([]byte(workspace + "\x00" + p.AgentID + "\x00" + p.Harness))
	lock := flock.New(filepath.Join(directory, "schedule-"+hex.EncodeToString(digest[:])+".lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		return nil, errors.New("another schedule clock is already running for this agent, workspace, and harness")
	}
	return &RuntimeLock{lock: lock}, nil
}

func (l *RuntimeLock) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}
	return l.lock.Unlock()
}
