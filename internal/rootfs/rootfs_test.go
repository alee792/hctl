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
