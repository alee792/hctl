package discordadapter

import (
	"errors"
	"os"

	"github.com/zalando/go-keyring"
)

// KeyringService is retained exactly so existing enrolled profiles continue
// to resolve after extraction from the hctl root module.
const KeyringService = "hctl.discord"

// ErrCredentialNotFound distinguishes an unenrolled profile from a credential
// store failure that must not be treated as permission to replace state.
var ErrCredentialNotFound = errors.New("Discord credential not found")

type CredentialStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}

type OSKeyring struct{}

func (OSKeyring) Get(profile string) (string, error) {
	value, err := keyring.Get(KeyringService, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrCredentialNotFound
	}
	if err != nil {
		return "", errors.New("Discord credential is unavailable; run setup or inject HCTL_DISCORD_TOKEN")
	}
	return value, nil
}

func (OSKeyring) Set(profile, token string) error {
	if err := keyring.Set(KeyringService, profile, token); err != nil {
		return errors.New("cannot store Discord credential in the OS credential store")
	}
	return nil
}

func (OSKeyring) Delete(profile string) error {
	if err := keyring.Delete(KeyringService, profile); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return errors.New("cannot remove Discord credential from the OS credential store")
	}
	return nil
}

func resolveCredential(store CredentialStore, profile string) (string, error) {
	if token := os.Getenv("HCTL_DISCORD_TOKEN"); token != "" {
		if err := validateTokenShape(token); err != nil {
			return "", err
		}
		return token, nil
	}
	token, err := store.Get(profile)
	if err != nil {
		return "", errors.New("Discord credential is unavailable; run setup or inject HCTL_DISCORD_TOKEN")
	}
	if err := validateTokenShape(token); err != nil {
		return "", errors.New("stored Discord credential is malformed; run setup")
	}
	return token, nil
}

func validateTokenShape(token string) error {
	if token == "" || len(token) > 4096 {
		return errors.New("Discord bot token is empty or malformed")
	}
	for _, character := range token {
		if character <= 0x20 || character == 0x7f {
			return errors.New("Discord bot token is empty or malformed")
		}
	}
	return nil
}
