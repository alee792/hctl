package channelconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const maxConfigBytes = 64 << 10

const (
	NoReplyResult            = "HCTL_NO_REPLY"
	RequestWriteAccessResult = "HCTL_REQUEST_WRITE_ACCESS"
)

var profileName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type Config struct {
	SchemaVersion int               `toml:"schema_version"`
	Discord       Discord           `toml:"discord"`
	AgentProfiles map[string]string `toml:"agent_profiles,omitempty"`
}

type Discord struct {
	DefaultProfile string             `toml:"default_profile"`
	Profiles       map[string]Profile `toml:"profiles"`
}

type Profile struct {
	ApplicationID    string `toml:"application_id"`
	BotUserID        string `toml:"bot_user_id"`
	AllowedUserID    string `toml:"allowed_user_id"`
	AllowedGuildID   string `toml:"allowed_guild_id"`
	AllowedChannelID string `toml:"allowed_channel_id"`
}

func DefaultPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		return "", errors.New("cannot resolve the user configuration directory")
	}
	return filepath.Join(root, "hctl", "config.toml"), nil
}

func SelectedPath(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if value := os.Getenv("HCTL_CONFIG"); value != "" {
		return filepath.Abs(value)
	}
	return DefaultPath()
}

func Load(path string, optional bool) (Config, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && optional {
		return Config{SchemaVersion: 1, Discord: Discord{Profiles: map[string]Profile{}}, AgentProfiles: map[string]string{}}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxConfigBytes {
		return Config{}, errors.New("hctl configuration must be a bounded regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, errors.New("hctl configuration permissions are too broad; require owner-only access")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, errors.New("cannot read hctl configuration")
	}
	var config Config
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, errors.New("hctl configuration is invalid")
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func Validate(config Config) error {
	if config.SchemaVersion != 1 {
		return errors.New("hctl configuration schema_version must be 1")
	}
	if config.Discord.Profiles == nil {
		return errors.New("hctl configuration requires discord profiles")
	}
	if config.Discord.DefaultProfile != "" {
		if !profileName.MatchString(config.Discord.DefaultProfile) || config.Discord.Profiles[config.Discord.DefaultProfile].ApplicationID == "" {
			return errors.New("discord default_profile does not select a configured profile")
		}
	}
	for name, profile := range config.Discord.Profiles {
		if !profileName.MatchString(name) {
			return fmt.Errorf("invalid Discord profile %q", name)
		}
		if !Snowflake(profile.ApplicationID) || !Snowflake(profile.BotUserID) || !Snowflake(profile.AllowedUserID) || !Snowflake(profile.AllowedGuildID) || !Snowflake(profile.AllowedChannelID) {
			return fmt.Errorf("Discord profile %q contains an invalid ID", name)
		}
	}
	for agentID, name := range config.AgentProfiles {
		if agentID == "" || !profileName.MatchString(name) || config.Discord.Profiles[name].ApplicationID == "" {
			return errors.New("hctl configuration contains an invalid agent profile selection")
		}
	}
	return nil
}

func Resolve(config Config, agentID, explicit string) (string, Profile, error) {
	name := explicit
	if name == "" {
		name = os.Getenv("HCTL_DISCORD_PROFILE")
	}
	if name == "" {
		name = config.AgentProfiles[agentID]
	}
	if name == "" {
		name = config.Discord.DefaultProfile
	}
	profile, ok := config.Discord.Profiles[name]
	if name == "" || !ok {
		return "", Profile{}, errors.New("discord is not configured; run hctl channel setup discord first")
	}
	return name, profile, nil
}

func Save(path string, config Config) error {
	if err := Validate(config); err != nil {
		return err
	}
	data, err := toml.Marshal(config)
	if err != nil || len(data) > maxConfigBytes {
		return errors.New("cannot encode hctl configuration")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.New("cannot create hctl configuration directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return errors.New("cannot protect hctl configuration directory")
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return errors.New("refusing to replace non-regular hctl configuration")
	}
	temp, err := os.CreateTemp(directory, ".hctl-config-*")
	if err != nil {
		return errors.New("cannot stage hctl configuration")
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return errors.New("cannot protect hctl configuration")
	}
	if _, err := temp.Write(data); err != nil || temp.Sync() != nil || temp.Close() != nil {
		return errors.New("cannot write hctl configuration")
	}
	if err := os.Rename(tempName, path); err != nil {
		return errors.New("cannot install hctl configuration")
	}
	return nil
}

func Snowflake(value string) bool {
	if value == "" || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != "0"
}
