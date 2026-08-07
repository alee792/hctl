package rootfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedIORejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".hctl")); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(root, ".hctl/state.json", []byte("{}\n"), 0o600); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("write through symlinked parent was not rejected: %v", err)
	}
	if _, _, _, err := ReadOptional(root, ".hctl/state.json", 1024); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("read through symlinked parent was not rejected: %v", err)
	}
}

func TestEnsurePrivateDirCreatesAndVerifiesRealDirectories(t *testing.T) {
	root := t.TempDir()
	if err := EnsurePrivateDir(root, ".hctl/plugin-data/example/state"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".hctl", ".hctl/plugin-data", ".hctl/plugin-data/example", ".hctl/plugin-data/example/state"} {
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory %s = %v, %v", path, info, err)
		}
	}
	if err := RequirePrivateDir(root, ".hctl/plugin-data/example/state"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, ".hctl", "plugin-data", "example", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RequirePrivateDir(root, ".hctl/plugin-data/example/state"); err == nil {
		t.Fatal("permissive plugin data directory verified as private")
	}
	if err := EnsurePrivateDir(root, ".hctl/plugin-data/example/state"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, ".hctl", "plugin-data", "example", "state")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("existing plugin data mode was not normalized: %v, %v", info, err)
	}
	if err := os.Remove(filepath.Join(root, ".hctl", "plugin-data", "example", "state")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".hctl", "plugin-data", "example", "state")); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(root, ".hctl/plugin-data/example/state/child"); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("private directory followed a symlink: %v", err)
	}
	if err := RequirePrivateDir(root, ".hctl/plugin-data/example/state"); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("symlinked private directory verified: %v", err)
	}
}
