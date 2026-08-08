package discordadapter

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"hctl/channeladapter"

	"golang.org/x/term"
)

type Dependencies struct {
	Profiles    ProfileStore
	Credentials CredentialStore
	Discord     DiscordFactory
	Locks       LockFactory
	After       func(time.Duration) <-chan time.Time
	HTTP        HTTPClient
}

func DefaultDependencies() (Dependencies, error) {
	profiles, err := DefaultProfileStore()
	if err != nil {
		return Dependencies{}, err
	}
	return Dependencies{Profiles: profiles, Credentials: OSKeyring{}, Discord: NewDiscord, Locks: NewApplicationLock, After: time.After}, nil
}

func (dependencies Dependencies) validate() error {
	if dependencies.Profiles == nil || dependencies.Credentials == nil || dependencies.Discord == nil || dependencies.Locks == nil {
		return errors.New("Discord adapter dependencies are incomplete")
	}
	return nil
}

func setup(ctx context.Context, profileID string, input io.Reader, terminal io.Writer, dependencies Dependencies) (channeladapter.OperationResult, error) {
	if err := dependencies.validate(); err != nil {
		return channeladapter.OperationResult{}, err
	}
	reader := bufio.NewReader(input)
	token := os.Getenv("HCTL_DISCORD_TOKEN")
	if token == "" {
		if _, err := fmt.Fprint(terminal, "Discord bot token: "); err != nil {
			return channeladapter.OperationResult{}, err
		}
		if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			secret, err := term.ReadPassword(int(file.Fd()))
			_, _ = fmt.Fprintln(terminal)
			if err != nil {
				return channeladapter.OperationResult{}, errors.New("cannot read Discord bot token")
			}
			token = string(secret)
		} else {
			value, err := reader.ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return channeladapter.OperationResult{}, errors.New("cannot read Discord bot token")
			}
			token = strings.TrimSpace(value)
		}
	}
	if err := validateTokenShape(token); err != nil {
		return channeladapter.OperationResult{}, err
	}
	client, err := dependencies.Discord(token)
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	defer func() { _ = client.Close() }()
	validationContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := validateIdentity(validationContext, client)
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	invite := fmt.Sprintf("https://discord.com/oauth2/authorize?client_id=%s&scope=bot%%20applications.commands&permissions=274877975552", identity.ApplicationID)
	if _, err := fmt.Fprintf(terminal, "\nInstall the bot in the target server: %s\nEnable Message Content Intent, then enter the authorized scope.\n", invite); err != nil {
		return channeladapter.OperationResult{}, err
	}
	readID := func(label string) (string, error) {
		if _, err := fmt.Fprint(terminal, label+": "); err != nil {
			return "", err
		}
		value, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value = strings.TrimSpace(value)
		if !snowflake(value) {
			return "", fmt.Errorf("%s must be a Discord snowflake", label)
		}
		return value, nil
	}
	allowedUser, err := readID("Authorized user ID")
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	allowedGuild, err := readID("Guild ID")
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	allowedChannel, err := readID("Channel ID")
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	profile := Profile{ApplicationID: identity.ApplicationID, BotUserID: identity.BotUserID, BotName: identity.BotName, AllowedUserID: allowedUser, AllowedGuildID: allowedGuild, AllowedChannelID: allowedChannel}
	if err := validateScope(validationContext, client, profile); err != nil {
		return channeladapter.OperationResult{}, err
	}
	oldToken, oldTokenErr := dependencies.Credentials.Get(profileID)
	if err := dependencies.Credentials.Set(profileID, token); err != nil {
		return channeladapter.OperationResult{}, err
	}
	if err := dependencies.Profiles.Put(profileID, profile); err != nil {
		if oldTokenErr == nil {
			_ = dependencies.Credentials.Set(profileID, oldToken)
		} else {
			_ = dependencies.Credentials.Delete(profileID)
		}
		return channeladapter.OperationResult{}, err
	}
	return operationResult("setup", profileID, "ready", safeIdentity(profile), "Discord profile enrolled."), nil
}

func status(ctx context.Context, profileID string, dependencies Dependencies) (channeladapter.OperationResult, error) {
	profile, err := dependencies.Profiles.Get(profileID)
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	token, err := resolveCredential(dependencies.Credentials, profileID)
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	client, err := dependencies.Discord(token)
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	defer func() { _ = client.Close() }()
	validationContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := validateIdentity(validationContext, client)
	if err != nil {
		return channeladapter.OperationResult{}, err
	}
	if err := validateIdentityMatches(identity, profile); err != nil {
		return channeladapter.OperationResult{}, err
	}
	if err := validateScope(validationContext, client, profile); err != nil {
		return channeladapter.OperationResult{}, err
	}
	return operationResult("status", profileID, "ready", safeIdentity(profile), "Discord profile is valid."), nil
}

func remove(profileID string, dependencies Dependencies) (channeladapter.OperationResult, error) {
	profile, profileErr := dependencies.Profiles.Get(profileID)
	if profileErr != nil && !strings.Contains(profileErr.Error(), "not configured") {
		return channeladapter.OperationResult{}, profileErr
	}
	if err := dependencies.Profiles.Delete(profileID); err != nil {
		return channeladapter.OperationResult{}, err
	}
	if err := dependencies.Credentials.Delete(profileID); err != nil {
		if profileErr == nil {
			_ = dependencies.Profiles.Put(profileID, profile)
		}
		return channeladapter.OperationResult{}, err
	}
	return operationResult("remove", profileID, "removed", "", "Discord profile removed."), nil
}

func operationResult(operation, profileID, status, identity, message string) channeladapter.OperationResult {
	return channeladapter.OperationResult{SchemaVersion: 1, Operation: operation, ProfileID: profileID, Status: status, Identity: identity, Message: message}
}

func safeIdentity(profile Profile) string {
	if profile.BotName == "" {
		return "discord-bot"
	}
	return profile.BotName
}
