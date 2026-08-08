package channelconfig

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pelletier/go-toml/v2"
)

const maxConfigBytes = 64 << 10

const (
	NoReplyResult            = "HCTL_NO_REPLY"
	RequestWriteAccessResult = "HCTL_REQUEST_WRITE_ACCESS"
	WriteContinuationPrompt  = "Write access is now available in this conversation's isolated worktree. Continue the original user request now."
)

var profileName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ProfileSelection is the only legacy configuration data still consumed by
// hctl core after channel ownership moved out of process. It intentionally
// omits all Discord profile and credential fields.
type ProfileSelection struct {
	DefaultProfile string
	AgentProfiles  map[string]string
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

func LoadProfileSelection(path string, optional bool) (ProfileSelection, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && optional {
		return ProfileSelection{AgentProfiles: map[string]string{}}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxConfigBytes {
		return ProfileSelection{}, errors.New("hctl configuration must be a bounded regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return ProfileSelection{}, errors.New("hctl configuration permissions are too broad; require owner-only access")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfileSelection{}, errors.New("cannot read hctl configuration")
	}
	var legacy struct {
		Discord struct {
			DefaultProfile string `toml:"default_profile"`
		} `toml:"discord"`
		AgentProfiles map[string]string `toml:"agent_profiles"`
	}
	if err := toml.Unmarshal(data, &legacy); err != nil {
		return ProfileSelection{}, errors.New("hctl configuration is invalid")
	}
	selection := ProfileSelection{DefaultProfile: legacy.Discord.DefaultProfile, AgentProfiles: legacy.AgentProfiles}
	if selection.AgentProfiles == nil {
		selection.AgentProfiles = map[string]string{}
	}
	if selection.DefaultProfile != "" && !profileName.MatchString(selection.DefaultProfile) {
		return ProfileSelection{}, errors.New("hctl configuration contains an invalid default profile selection")
	}
	for agentID, name := range selection.AgentProfiles {
		if agentID == "" || !profileName.MatchString(name) {
			return ProfileSelection{}, errors.New("hctl configuration contains an invalid agent profile selection")
		}
	}
	return selection, nil
}
