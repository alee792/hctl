package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyPolicyReachesManagedChildWithoutDiscordToken(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "managed-child.log")
	childPath := filepath.Join(directory, "fake-mcp-child")
	parentPath := filepath.Join(directory, "fake-harness")
	writeProcessTestExecutable(t, childPath, `#!/bin/sh
printf 'policy=%s token=%s\n' "$HCTL_EXECUTION_POLICY" "$HCTL_DISCORD_TOKEN" > "$FAKE_MCP_LOG"
`)
	writeProcessTestExecutable(t, parentPath, `#!/bin/sh
"$FAKE_MCP_CHILD"
`)
	t.Setenv("FAKE_MCP_CHILD", childPath)
	t.Setenv("FAKE_MCP_LOG", logPath)
	t.Setenv("HCTL_DISCORD_TOKEN", "must-not-reach-managed-child")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := StartProcessWithPolicy(ctx, directory, parentPath, PolicyReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Finish(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "policy=read-only token=" {
		t.Fatalf("managed child environment = %q", got)
	}
}

func writeProcessTestExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
