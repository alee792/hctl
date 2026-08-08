package discordadapter

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const maxProfileFileBytes = 64 << 10

var profileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

// Profile contains only non-secret Discord routing and identity metadata.
type Profile struct {
	ApplicationID    string `toml:"application_id"`
	BotUserID        string `toml:"bot_user_id"`
	BotName          string `toml:"bot_name"`
	AllowedUserID    string `toml:"allowed_user_id"`
	AllowedGuildID   string `toml:"allowed_guild_id"`
	AllowedChannelID string `toml:"allowed_channel_id"`
	DirectChannelID  string `toml:"direct_channel_id,omitempty"`
}

type profileDocument struct {
	SchemaVersion           int                `toml:"schema_version"`
	Profiles                map[string]Profile `toml:"profiles"`
	LegacyRemovalTombstones map[string]bool    `toml:"legacy_removal_tombstones,omitempty"`
}

// ProfileStore is deliberately narrower than a configuration service. The
// adapter is the sole owner of its non-secret profiles.
type ProfileStore interface {
	Get(string) (Profile, error)
	Put(string, Profile) error
	Delete(string) error
}

// FileProfileStore persists adapter-owned profiles and can migrate the exact
// profile shape used by the former in-process Discord implementation.
type FileProfileStore struct {
	Path       string
	LegacyPath string
}

func DefaultProfileStore() (FileProfileStore, error) {
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		return FileProfileStore{}, errors.New("cannot resolve the user configuration directory")
	}
	return FileProfileStore{
		Path:       filepath.Join(root, "hctl", "integrations", "discord", "profiles.toml"),
		LegacyPath: filepath.Join(root, "hctl", "config.toml"),
	}, nil
}

func (store FileProfileStore) Get(id string) (Profile, error) {
	if !validProfileID(id) {
		return Profile{}, errors.New("discord profile id is invalid")
	}
	document, exists, err := store.load()
	if err != nil {
		return Profile{}, err
	}
	if exists {
		profile, ok := document.Profiles[id]
		if ok {
			return profile, validateProfile(profile)
		}
		if document.LegacyRemovalTombstones[id] {
			return Profile{}, errors.New("discord profile is not configured; run setup")
		}
	}
	profile, found, err := store.loadLegacy(id)
	if err != nil {
		return Profile{}, err
	}
	if !found {
		return Profile{}, errors.New("discord profile is not configured; run setup")
	}
	if err := store.Put(id, profile); err != nil {
		return Profile{}, errors.New("cannot migrate the existing Discord profile")
	}
	return profile, nil
}

func (store FileProfileStore) Put(id string, profile Profile) error {
	if !validProfileID(id) {
		return errors.New("discord profile id is invalid")
	}
	if err := validateProfile(profile); err != nil {
		return err
	}
	document, exists, err := store.load()
	if err != nil {
		return err
	}
	if !exists {
		document = newProfileDocument()
	}
	document.Profiles[id] = profile
	delete(document.LegacyRemovalTombstones, id)
	return store.save(document)
}

func (store FileProfileStore) Delete(id string) error {
	if !validProfileID(id) {
		return errors.New("discord profile id is invalid")
	}
	document, exists, err := store.load()
	if err != nil {
		return err
	}
	if !exists {
		document = newProfileDocument()
	}
	delete(document.Profiles, id)
	if document.LegacyRemovalTombstones == nil {
		document.LegacyRemovalTombstones = map[string]bool{}
	}
	document.LegacyRemovalTombstones[id] = true
	return store.save(document)
}

func (store FileProfileStore) load() (profileDocument, bool, error) {
	data, exists, err := readPrivateFile(store.Path)
	if err != nil || !exists {
		return profileDocument{}, exists, err
	}
	var document profileDocument
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || document.SchemaVersion != 1 {
		return profileDocument{}, false, errors.New("Discord profile store is invalid")
	}
	if document.Profiles == nil {
		document.Profiles = map[string]Profile{}
	}
	if document.LegacyRemovalTombstones == nil {
		document.LegacyRemovalTombstones = map[string]bool{}
	}
	for id, profile := range document.Profiles {
		if !validProfileID(id) || validateProfile(profile) != nil {
			return profileDocument{}, false, errors.New("Discord profile store is invalid")
		}
	}
	for id, removed := range document.LegacyRemovalTombstones {
		if !validProfileID(id) || !removed {
			return profileDocument{}, false, errors.New("Discord profile store is invalid")
		}
	}
	return document, true, nil
}

func newProfileDocument() profileDocument {
	return profileDocument{SchemaVersion: 1, Profiles: map[string]Profile{}, LegacyRemovalTombstones: map[string]bool{}}
}

func (store FileProfileStore) loadLegacy(id string) (Profile, bool, error) {
	if store.LegacyPath == "" {
		return Profile{}, false, nil
	}
	data, exists, err := readPrivateFile(store.LegacyPath)
	if err != nil || !exists {
		return Profile{}, false, err
	}
	var legacy struct {
		Discord struct {
			Profiles map[string]struct {
				ApplicationID    string `toml:"application_id"`
				BotUserID        string `toml:"bot_user_id"`
				AllowedUserID    string `toml:"allowed_user_id"`
				AllowedGuildID   string `toml:"allowed_guild_id"`
				AllowedChannelID string `toml:"allowed_channel_id"`
			} `toml:"profiles"`
		} `toml:"discord"`
	}
	if err := toml.Unmarshal(data, &legacy); err != nil {
		return Profile{}, false, errors.New("existing hctl configuration is invalid")
	}
	old, found := legacy.Discord.Profiles[id]
	if !found {
		return Profile{}, false, nil
	}
	profile := Profile{ApplicationID: old.ApplicationID, BotUserID: old.BotUserID, AllowedUserID: old.AllowedUserID, AllowedGuildID: old.AllowedGuildID, AllowedChannelID: old.AllowedChannelID}
	if err := validateProfile(profile); err != nil {
		return Profile{}, false, errors.New("existing Discord profile is invalid")
	}
	return profile, true, nil
}

func (store FileProfileStore) save(document profileDocument) error {
	data, err := toml.Marshal(document)
	if err != nil || len(data) > maxProfileFileBytes {
		return errors.New("cannot encode Discord profiles")
	}
	directory := filepath.Dir(store.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil || os.Chmod(directory, 0o700) != nil {
		return errors.New("cannot protect Discord profile directory")
	}
	if info, err := os.Lstat(store.Path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing to replace unsafe Discord profile store")
	}
	temporary, err := os.CreateTemp(directory, ".profiles-*")
	if err != nil {
		return errors.New("cannot stage Discord profiles")
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if temporary.Chmod(0o600) != nil {
		_ = temporary.Close()
		return errors.New("cannot protect Discord profiles")
	}
	if _, err := temporary.Write(data); err != nil || temporary.Sync() != nil || temporary.Close() != nil || os.Rename(name, store.Path) != nil {
		return errors.New("cannot save Discord profiles")
	}
	return nil
}

func readPrivateFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > maxProfileFileBytes {
		return nil, false, errors.New("Discord profile file must be a bounded owner-only regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, errors.New("cannot read Discord profile file")
	}
	return data, true, nil
}

func validProfileID(id string) bool { return len(id) <= 64 && profileIDPattern.MatchString(id) }

func validateProfile(profile Profile) error {
	for label, value := range map[string]string{
		"application": profile.ApplicationID, "bot": profile.BotUserID, "authorized user": profile.AllowedUserID,
		"guild": profile.AllowedGuildID, "channel": profile.AllowedChannelID,
	} {
		if !snowflake(value) {
			return fmt.Errorf("Discord profile %s id is invalid", label)
		}
	}
	if profile.DirectChannelID != "" && !snowflake(profile.DirectChannelID) {
		return errors.New("Discord profile direct channel id is invalid")
	}
	if len(profile.BotName) > 100 || strings.ContainsAny(profile.BotName, "\r\n\x00") {
		return errors.New("Discord bot name is invalid")
	}
	return nil
}

func snowflake(value string) bool {
	if value == "" || value == "0" || len(value) > 20 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
