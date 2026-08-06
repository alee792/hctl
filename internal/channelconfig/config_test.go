package channelconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func validConfig() Config {
	return Config{SchemaVersion: 1, Discord: Discord{DefaultProfile: "default", Profiles: map[string]Profile{"default": {ApplicationID: "111", BotUserID: "222", AllowedUserID: "333", AllowedGuildID: "444", AllowedChannelID: "555"}}}, AgentProfiles: map[string]string{"agent@abc": "default"}}
}

func TestSaveLoadAndResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hctl", "config.toml")
	if err := Save(path, validConfig()); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	name, profile, err := Resolve(loaded, "agent@abc", "")
	if err != nil || name != "default" || profile.ApplicationID != "111" {
		t.Fatalf("resolved %q %#v: %v", name, profile, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info.Mode(), err)
	}
}

func TestLoadRejectsUnknownAndBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("schema_version=1\nunknown=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, false); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := os.WriteFile(path, []byte("schema_version=1\n[discord]\nprofiles={}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, false); err == nil {
		t.Fatal("broad permissions accepted")
	}
}

func TestResolvePrecedence(t *testing.T) {
	config := validConfig()
	config.Discord.Profiles["other"] = Profile{ApplicationID: "999", BotUserID: "888", AllowedUserID: "777", AllowedGuildID: "666", AllowedChannelID: "555"}
	t.Setenv("HCTL_DISCORD_PROFILE", "other")
	name, _, err := Resolve(config, "agent@abc", "")
	if err != nil || name != "other" {
		t.Fatalf("environment profile = %q, %v", name, err)
	}
	name, _, err = Resolve(config, "agent@abc", "default")
	if err != nil || name != "default" {
		t.Fatalf("explicit profile = %q, %v", name, err)
	}
}
