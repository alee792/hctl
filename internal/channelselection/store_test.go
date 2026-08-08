package channelselection

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
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

func TestStoreWriteWaitsForInterprocessLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "hctl", "channel-selections.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	guard := flock.New(path + ".lock")
	if err := guard.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".lock", 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "started")
	command := exec.Command(os.Args[0], "-test.run=^TestSelectionStoreProcess$")
	command.Env = append(os.Environ(), "HCTL_SELECTION_HELPER=1", "HCTL_SELECTION_PATH="+path, "HCTL_SELECTION_MARKER="+marker)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selection helper did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("selection write bypassed held process lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := guard.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("selection write did not resume after unlock")
	}
	selection, err := (Store{Path: path}).Get("agent@process", "discord")
	if err != nil || selection != "default" {
		t.Fatalf("process selection = %q, %v", selection, err)
	}
}

func TestSelectionStoreProcess(t *testing.T) {
	if os.Getenv("HCTL_SELECTION_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	if err := os.WriteFile(os.Getenv("HCTL_SELECTION_MARKER"), []byte("started\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Store{Path: os.Getenv("HCTL_SELECTION_PATH")}).Set("agent@process", "discord", "default"); err != nil {
		t.Fatal(err)
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
