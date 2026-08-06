package credential

import (
	"errors"
	"os"

	"github.com/zalando/go-keyring"
)

const discordService = "hctl.discord"

type Store interface {
	Get(profile string) (string, error)
	Set(profile, token string) error
	Delete(profile string) error
}

type OSStore struct{}

func (OSStore) Get(profile string) (string, error) {
	value, err := keyring.Get(discordService, profile)
	if err != nil {
		return "", errors.New("discord credential is unavailable; run hctl channel setup discord or inject HCTL_DISCORD_TOKEN")
	}
	return value, nil
}

func (OSStore) Set(profile, token string) error {
	if err := keyring.Set(discordService, profile, token); err != nil {
		return errors.New("cannot store Discord credential in the OS credential store")
	}
	return nil
}

func (OSStore) Delete(profile string) error {
	if err := keyring.Delete(discordService, profile); err != nil && err != keyring.ErrNotFound {
		return errors.New("cannot remove Discord credential from the OS credential store")
	}
	return nil
}

func Resolve(store Store, profile string) (string, error) {
	if value := os.Getenv("HCTL_DISCORD_TOKEN"); value != "" {
		return value, nil
	}
	return store.Get(profile)
}
