package schedule

import (
	"os"
	"path/filepath"
	"testing"

	"hctl/internal/project"
)

func TestRuntimeLockCanonicalizesWorkspaceAndScopesIdentity(t *testing.T) {
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	base := &project.Project{WorkspaceRoot: workspace, AgentID: "agent@one", Harness: "claude"}
	lock, err := acquireRuntimeLock(base, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, err := acquireRuntimeLock(&project.Project{WorkspaceRoot: alias, AgentID: base.AgentID, Harness: base.Harness}, directory); err == nil {
		t.Fatal("workspace alias acquired a second clock lock")
	}
	for _, candidate := range []*project.Project{
		{WorkspaceRoot: workspace, AgentID: "agent@two", Harness: "claude"},
		{WorkspaceRoot: workspace, AgentID: base.AgentID, Harness: "codex"},
		{WorkspaceRoot: t.TempDir(), AgentID: base.AgentID, Harness: base.Harness},
	} {
		other, err := acquireRuntimeLock(candidate, directory)
		if err != nil {
			t.Fatalf("distinct runtime lock collided: %v", err)
		}
		if err := other.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	reacquired, err := acquireRuntimeLock(base, directory)
	if err != nil {
		t.Fatalf("released lock could not be reacquired: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatal(err)
	}
}
