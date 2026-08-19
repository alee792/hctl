package channelselection

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadProfileSelectionDoesNotRequireLegacyProfileFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte("schema_version=1\n[discord]\ndefault_profile='default'\n[discord.profiles.default]\ninvalid_vendor_field=true\n[agent_profiles]\n'agent@one'='work'\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	selection, err := LoadProfileSelection(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if selection.DefaultProfile != "default" || selection.AgentProfiles["agent@one"] != "work" {
		t.Fatalf("selection = %#v", selection)
	}
}
