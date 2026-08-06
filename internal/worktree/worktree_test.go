package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"hctl/internal/project"
	"hctl/internal/setup"
)

func TestProvisionCreatesAndResolvesIsolatedBranchWorktree(t *testing.T) {
	repo, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}

	prepared, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Remove(context.Background(), assignment) })
	if prepared.WorkspaceRoot != assignment.Root || prepared.AgentID != base.AgentID {
		t.Fatalf("prepared project = %#v, assignment = %#v", prepared, assignment)
	}
	if filepath.Dir(assignment.Root) == repo || !strings.HasPrefix(assignment.Branch, "hctl/"+base.AgentID+"/") {
		t.Fatalf("unsafe assignment = %#v", assignment)
	}
	if err := setup.Verify(prepared); err != nil {
		t.Fatalf("generated setup was not prepared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("shared checkout was modified: %v", err)
	}

	resolved, err := manager.Resolve(context.Background(), "discord-conversation", assignment)
	if err != nil || resolved.WorkspaceRoot != assignment.Root || resolved.AgentID != base.AgentID {
		t.Fatalf("resolved project = %#v, %v", resolved, err)
	}
}

func TestProvisionFailureCleansWorktreeAndBranch(t *testing.T) {
	repo, _ := gitProject(t)
	if err := os.WriteFile(filepath.Join(repo, "instructions.md"), []byte("---\ndescription: changed\n---\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base, err := project.Load(repo, "claude")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}

	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err == nil {
		t.Fatal("provision unexpectedly accepted mismatched source")
	}
	if _, statErr := os.Lstat(manager.expected("discord-conversation").Root); !os.IsNotExist(statErr) {
		t.Fatalf("failed worktree remains: %v", statErr)
	}
	if assignment != (Assignment{}) {
		t.Fatalf("failed assignment escaped: %#v", assignment)
	}
	if command := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+manager.expected("discord-conversation").Branch); command.Run() == nil {
		t.Fatal("failed worktree branch remains")
	}
}

func TestResolveRefusesModifiedGeneratedSetup(t *testing.T) {
	_, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Remove(context.Background(), assignment) })
	if err := os.WriteFile(filepath.Join(assignment.Root, "CLAUDE.md"), []byte("user change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Resolve(context.Background(), "discord-conversation", assignment); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("resolve modified generated file = %v", err)
	}
}

func TestNewRejectsNonGitWorkspace(t *testing.T) {
	root := t.TempDir()
	writeInstructions(t, root)
	base, err := project.Load(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), base, "/usr/bin/true"); err == nil {
		t.Fatal("non-Git workspace was accepted")
	}
}

func gitProject(t *testing.T) (string, *project.Project) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructions(t, repo)
	git(t, repo, "init", "--quiet")
	git(t, repo, "config", "user.email", "hctl@example.invalid")
	git(t, repo, "config", "user.name", "hctl test")
	git(t, repo, "add", "instructions.md")
	git(t, repo, "commit", "--quiet", "-m", "fixture")
	base, err := project.Load(repo, "claude")
	if err != nil {
		t.Fatal(err)
	}
	return repo, base
}

func writeInstructions(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "instructions.md"), []byte("---\ndescription: test agent\n---\nHelp with tests.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
