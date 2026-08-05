package tool

import (
	"os"
	"path/filepath"
	"testing"

	"hctl/internal/rootfs"
)

func TestLocalModuleVersionFollowsGoMajorPath(t *testing.T) {
	tests := map[string]string{
		"example.com/agent":    "v0.0.0",
		"example.com/agent/v2": "v2.0.0",
		"gopkg.in/agent.v3":    "v3.0.0",
	}
	for module, want := range tests {
		if got := localModuleVersion(module); got != want {
			t.Errorf("localModuleVersion(%q) = %q, want %q", module, got, want)
		}
	}
}

func TestHostCommandUsesPreparedExecutableWithoutPATH(t *testing.T) {
	workspace := t.TempDir()
	source := t.TempDir()
	fingerprint := "test-fingerprint"
	host := cacheRelative(fingerprint) + "/typescript.ts"
	if err := rootfs.WriteAtomic(workspace, host, typescriptHost, 0o644); err != nil {
		t.Fatal(err)
	}
	deno := filepath.Join(t.TempDir(), "deno")
	if err := os.WriteFile(deno, []byte("prepared runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	command, _, _, err := hostCommand(source, workspace, fingerprint, preparedRuntime{Deno: deno}, TypeScript)
	if err != nil {
		t.Fatal(err)
	}
	if command != deno {
		t.Fatalf("host command = %q, want %q", command, deno)
	}
}
