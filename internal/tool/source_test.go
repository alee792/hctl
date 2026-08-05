package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverMixedToolsWithoutRegistry(t *testing.T) {
	root := t.TempDir()
	write(t, root, "tools/repeat.ts", "export default {}\n")
	write(t, root, "tools/add.py", "description = 'add'\n")
	write(t, root, "tools/hash_text/tool.go", "package hashtext\n")
	write(t, root, "deno.json", "{}\n")
	write(t, root, "deno.lock", "{}\n")
	write(t, root, "pyproject.toml", "[project]\n")
	write(t, root, "uv.lock", "version = 1\n")
	write(t, root, "go.mod", "module example.com/tools\n")

	inventory, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Sources) != 3 || inventory.Sources[0].Name != "add" || inventory.Sources[1].Name != "hash-text" || inventory.Sources[2].Name != "repeat" {
		t.Fatalf("sources = %#v", inventory.Sources)
	}
	if len(inventory.Files) != 8 {
		t.Fatalf("files = %#v", inventory.Files)
	}
}

func TestDiscoverRejectsDuplicateAndMissingNativeLock(t *testing.T) {
	duplicate := t.TempDir()
	write(t, duplicate, "tools/same.ts", "export default {}\n")
	write(t, duplicate, "tools/same.py", "description = 'same'\n")
	if _, err := Discover(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		t.Fatalf("duplicate was not rejected: %v", err)
	}

	missingLock := t.TempDir()
	write(t, missingLock, "tools/repeat.ts", "export default {}\n")
	if _, err := Discover(missingLock); err == nil || !strings.Contains(err.Error(), "deno.json") {
		t.Fatalf("missing Deno files were not rejected: %v", err)
	}
}

func TestDiscoverAllowsEmptyToolsDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	inventory, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Sources) != 0 || len(inventory.Files) != 0 {
		t.Fatalf("inventory = %#v", inventory)
	}
}

func write(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
