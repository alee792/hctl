package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hctl/internal/rootfs"
)

func TestLoadMigratesLegacyStatePath(t *testing.T) {
	root := t.TempDir()
	writeStateAt(t, root, legacyStatePath, "reviewer@legacy", 0o600)

	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.GetOrCreate("reviewer@legacy", "claude", "local", "source-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyStatePath)); !os.IsNotExist(err) {
		t.Fatalf("legacy state was not removed: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, statePath))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("migrated state permissions = %o", got)
	}
}

func TestLoadPrefersDispatchState(t *testing.T) {
	root := t.TempDir()
	writeStateAt(t, root, legacyStatePath, "reviewer@legacy", 0o644)
	writeStateAt(t, root, statePath, "reviewer@dispatch", 0o600)

	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.GetOrCreate("reviewer@dispatch", "claude", "local", "source-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyStatePath)); err != nil {
		t.Fatalf("preferred dispatch state unexpectedly mutated legacy state: %v", err)
	}
}

func TestLoadRejectsBroadLegacyStatePermissions(t *testing.T) {
	root := t.TempDir()
	writeStateAt(t, root, legacyStatePath, "reviewer@legacy", 0o644)

	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("broad legacy state permissions were not rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, statePath)); !os.IsNotExist(err) {
		t.Fatalf("invalid legacy state created dispatch state: %v", err)
	}
}

func writeStateAt(t *testing.T, root, path, agentID string, mode os.FileMode) {
	t.Helper()
	conversation := &Conversation{
		ID:                "local",
		AgentID:           agentID,
		Harness:           "claude",
		SourceFingerprint: "source-1",
		Outcomes:          map[string]string{},
	}
	state := State{
		SchemaVersion: 2,
		Conversations: map[string]*Conversation{
			conversationKey(agentID, "claude", "local", "source-1"): conversation,
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := rootfs.WriteAtomic(root, path, append(data, '\n'), mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMigratesLegacyManifestFingerprint(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".hctl"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{
  "schema_version": 1,
  "conversations": {
    "claude:local": {
      "id": "local",
      "harness": "claude",
      "manifest_fingerprint": "source-1",
      "queue": [],
      "outcomes": {},
      "outcome_order": []
    }
  }
}
`
	if err := os.WriteFile(filepath.Join(root, legacyStatePath), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Conversations["claude:local"].SourceFingerprint; got != "source-1" {
		t.Fatalf("source fingerprint = %q", got)
	}
	if _, err := state.GetOrCreate("reviewer@123", "claude", "local", "source-1"); err != nil {
		t.Fatal(err)
	}
	if err := Save(root, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, statePath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "manifest_fingerprint") || !strings.Contains(string(data), `"schema_version": 2`) || !strings.Contains(string(data), `"agent_id": "reviewer@123"`) {
		t.Fatalf("legacy field was not migrated: %s", data)
	}
}

func TestConversationKeysSeparateAgentsAndSourceVersions(t *testing.T) {
	state := &State{SchemaVersion: 2, Conversations: map[string]*Conversation{}}
	first, err := state.GetOrCreate("reviewer@one", "claude", "local", "fingerprint-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := state.GetOrCreate("reviewer@two", "claude", "local", "fingerprint-1")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := state.GetOrCreate("reviewer@one", "claude", "local", "fingerprint-2")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == changed || len(state.Conversations) != 3 {
		t.Fatalf("conversations were not isolated: %#v", state.Conversations)
	}
}

func TestLoadRejectsConflictingLegacyFingerprint(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{
  "schema_version": 1,
  "conversations": {
    "claude:local": {
      "id": "local",
      "harness": "claude",
      "source_fingerprint": "source-2",
      "manifest_fingerprint": "source-1",
      "queue": [],
      "outcomes": {},
      "outcome_order": []
    }
  }
}
`)
	if err := rootfs.WriteAtomic(root, statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "fingerprints conflict") {
		t.Fatalf("conflicting fingerprints were not rejected: %v", err)
	}
}
