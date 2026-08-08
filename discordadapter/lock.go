package discordadapter

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type ApplicationLock interface {
	TryLock() (bool, error)
	Unlock() error
}

type LockFactory func(string) (ApplicationLock, error)

func NewApplicationLock(applicationID string) (ApplicationLock, error) {
	if !snowflake(applicationID) {
		return nil, errors.New("Discord application id is invalid")
	}
	root, err := os.UserCacheDir()
	if err != nil || root == "" {
		return nil, errors.New("cannot resolve Discord lock directory")
	}
	directory := filepath.Join(root, "hctl", "locks")
	if err := os.MkdirAll(directory, 0o700); err != nil || os.Chmod(directory, 0o700) != nil {
		return nil, errors.New("cannot protect Discord lock directory")
	}
	return flock.New(filepath.Join(directory, "discord-"+applicationID+".lock")), nil
}
