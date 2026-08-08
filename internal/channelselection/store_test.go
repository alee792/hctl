package channelselection

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSelectionLifecycle(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "hctl", "channel-selections.json")}
	if err := store.Set("agent@one", "discord", "work"); err != nil {
		t.Fatal(err)
	}
	selection, err := store.Get("agent@one", "discord")
	if err != nil || selection != "work" {
		t.Fatalf("selection = %q, %v", selection, err)
	}
	info, err := os.Stat(store.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info.Mode(), err)
	}
	if err := store.Delete("agent@one", "discord", "other"); err != nil {
		t.Fatal(err)
	}
	selection, err = store.Get("agent@one", "discord")
	if err != nil || selection != "work" {
		t.Fatalf("mismatched delete changed selection = %q, %v", selection, err)
	}
	if err := store.Delete("agent@one", "discord", "work"); err != nil {
		t.Fatal(err)
	}
	selection, err = store.Get("agent@one", "discord")
	if err != nil || selection != "" {
		t.Fatalf("deleted selection = %q, %v", selection, err)
	}
}

func TestStoreRejectsUnsafeOrUnknownInput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "channel-selections.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"agents":{},"secret":"no"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Path: path}).Get("agent", "discord"); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"agents":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{Path: path}).Get("agent", "discord"); err == nil {
		t.Fatal("broad permissions accepted")
	}
}
