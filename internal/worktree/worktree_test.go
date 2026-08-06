package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	if err := setup.VerifyWritableChannel(prepared); err != nil {
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

func TestProvisionConcurrentConversationsCreatesIsolatedWorktrees(t *testing.T) {
	_, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		conversation string
		project      *project.Project
		assignment   Assignment
		err          error
	}
	results := make(chan result, 2)
	for _, conversation := range []string{"discord-guild", "discord-dm"} {
		go func() {
			prepared, assignment, provisionErr := manager.Provision(context.Background(), conversation)
			results <- result{conversation: conversation, project: prepared, assignment: assignment, err: provisionErr}
		}()
	}
	created := map[string]result{}
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		created[got.conversation] = got
		t.Cleanup(func() { manager.Remove(context.Background(), got.assignment) })
	}
	guild, dm := created["discord-guild"], created["discord-dm"]
	if guild.assignment == dm.assignment || guild.assignment.Root == dm.assignment.Root || guild.assignment.Branch == dm.assignment.Branch {
		t.Fatalf("conversation assignments are not distinct: guild=%#v dm=%#v", guild.assignment, dm.assignment)
	}
	if err := os.WriteFile(filepath.Join(guild.assignment.Root, "guild-only.txt"), []byte("guild mutation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dm.assignment.Root, "guild-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("guild mutation visible from DM worktree: %v", err)
	}
	for conversation, got := range created {
		resolved, err := manager.Resolve(context.Background(), conversation, got.assignment)
		if err != nil || resolved.WorkspaceRoot != got.assignment.Root {
			t.Fatalf("resolve %s = %#v, %v", conversation, resolved, err)
		}
	}
	if reflect.DeepEqual(guild.project, dm.project) {
		t.Fatal("isolated worktrees resolved to identical projects")
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
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err == nil {
		t.Fatal("modified generated setup was retired")
	}
	if _, err := os.Stat(assignment.Root); err != nil {
		t.Fatalf("modified generated worktree was not preserved: %v", err)
	}
}

func TestInspectAndRetireCleanMergedManagedWorktree(t *testing.T) {
	repo, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := manager.Inspect(context.Background(), "discord-conversation", assignment)
	if err != nil || !inspection.Clean || !inspection.Merged {
		t.Fatalf("clean managed inspection = %+v, %v", inspection, err)
	}
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(assignment.Root); !os.IsNotExist(err) {
		t.Fatalf("retired worktree remains: %v", err)
	}
	if command := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+assignment.Branch); command.Run() == nil {
		t.Fatal("retired branch remains")
	}
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatalf("idempotent retirement = %v", err)
	}
}

func TestRetireDoesNotDeleteManagedFilesTrackedInMergedBase(t *testing.T) {
	repo, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	git(t, assignment.Root, "add", ".")
	git(t, assignment.Root, "commit", "--quiet", "-m", "track generated setup")
	git(t, repo, "merge", "--quiet", "--no-ff", "-m", "merge conversation", assignment.Branch)
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); err != nil {
		t.Fatalf("tracked generated file was deleted from merged base: %v", err)
	}
}

func TestInspectPreservesDirtyUntrackedAndUnmergedWorktrees(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
		clean  bool
		merged bool
	}{
		{name: "untracked", mutate: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, clean: false, merged: true},
		{name: "unmerged", mutate: func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "committed.txt"), []byte("work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(t, root, "add", ".")
			git(t, root, "commit", "--quiet", "-m", "conversation work")
		}, clean: true, merged: false},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			test.mutate(t, assignment.Root)
			inspection, err := manager.Inspect(context.Background(), "discord-conversation", assignment)
			if err != nil || inspection.Clean != test.clean || inspection.Merged != test.merged {
				t.Fatalf("inspection = %+v, %v", inspection, err)
			}
			if err := manager.Retire(context.Background(), "discord-conversation", assignment); err == nil {
				t.Fatal("non-disposable worktree was retired")
			}
			if _, err := os.Stat(assignment.Root); err != nil {
				t.Fatalf("preserved worktree missing: %v", err)
			}
		})
	}
}

func TestInspectPreservesModifiedTrackedFile(t *testing.T) {
	repo, base := gitProject(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "tracked.txt")
	git(t, repo, "commit", "--quiet", "-m", "tracked fixture")
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Remove(context.Background(), assignment) })
	if err := os.WriteFile(filepath.Join(assignment.Root, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection, err := manager.Inspect(context.Background(), "discord-conversation", assignment)
	if err != nil || inspection.Clean {
		t.Fatalf("modified tracked inspection = %+v, %v", inspection, err)
	}
}

func TestInspectPreservesIgnoredUntrackedWork(t *testing.T) {
	repo, base := gitProject(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".hctl/\n.claude/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "--quiet", "-m", "ignore fixture")
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Remove(context.Background(), assignment) })
	userWork := filepath.Join(assignment.Root, ".hctl", "user-work.txt")
	if err := os.WriteFile(userWork, []byte("must survive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection, err := manager.Inspect(context.Background(), "discord-conversation", assignment)
	if err != nil || inspection.Clean {
		t.Fatalf("ignored work inspection = %+v, %v", inspection, err)
	}
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err == nil {
		t.Fatal("worktree containing ignored user work was retired")
	}
	data, err := os.ReadFile(userWork)
	if err != nil || string(data) != "must survive\n" {
		t.Fatalf("ignored user work was not preserved: %q, %v", data, err)
	}
}

func TestRetireAllowsIgnoredGeneratedDirectories(t *testing.T) {
	repo, _ := gitProject(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".hctl/\n.claude/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "--quiet", "-m", "ignore generated directories")
	base, err := project.Load(repo, "claude")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Remove(context.Background(), assignment) })
	inspection, err := manager.Inspect(context.Background(), "discord-conversation", assignment)
	if err != nil || !inspection.Clean || !inspection.Merged {
		t.Fatalf("ignored generated setup inspection = %+v, %v", inspection, err)
	}
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatalf("retire ignored generated setup = %v", err)
	}
	if _, err := os.Lstat(assignment.Root); !os.IsNotExist(err) {
		t.Fatalf("retired worktree remains: %v", err)
	}
}

func TestRetireResumesAfterWorktreeRemovalWasInterrupted(t *testing.T) {
	repo, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "worktree", "remove", "--force", assignment.Root)
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatalf("resume interrupted retirement = %v", err)
	}
	if command := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+assignment.Branch); command.Run() == nil {
		t.Fatal("branch remains after resumed retirement")
	}
}

func TestRetireResumesAfterManagedSetupRemovalWasInterrupted(t *testing.T) {
	_, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	prepared, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.RemoveWritableChannel(prepared, nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatalf("resume after generated setup removal = %v", err)
	}
	if _, err := os.Lstat(assignment.Root); !os.IsNotExist(err) {
		t.Fatalf("retired worktree remains: %v", err)
	}
}

func TestRetireResumesAfterPartialManagedFileRemoval(t *testing.T) {
	_, base := gitProject(t)
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	prepared, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	paths, err := setup.WritableChannelFiles(prepared)
	if err != nil || len(paths) < 2 {
		t.Fatalf("managed setup paths = %v, %v", paths, err)
	}
	if err := os.Remove(filepath.Join(assignment.Root, filepath.FromSlash(paths[0]))); err != nil {
		t.Fatal(err)
	}
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatalf("resume after partial generated-file removal = %v", err)
	}
	if _, err := os.Lstat(assignment.Root); !os.IsNotExist(err) {
		t.Fatalf("retired worktree remains: %v", err)
	}
}

func TestInspectPreservesMovedDeletedAndMissingBranchAssignments(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, Assignment) string
	}{
		{name: "moved", mutate: func(t *testing.T, repo string, assignment Assignment) string {
			moved := assignment.Root + "-moved"
			git(t, repo, "worktree", "move", assignment.Root, moved)
			return moved
		}},
		{name: "deleted", mutate: func(t *testing.T, repo string, assignment Assignment) string {
			git(t, repo, "worktree", "remove", "--force", assignment.Root)
			return ""
		}},
		{name: "missing branch", mutate: func(t *testing.T, _ string, assignment Assignment) string {
			git(t, assignment.Root, "checkout", "--detach", "--quiet")
			git(t, assignment.Root, "branch", "-D", assignment.Branch)
			return assignment.Root
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, base := gitProject(t)
			manager, err := New(context.Background(), base, "/usr/bin/true")
			if err != nil {
				t.Fatal(err)
			}
			_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
			if err != nil {
				t.Fatal(err)
			}
			cleanupRoot := test.mutate(t, repo, assignment)
			if cleanupRoot != "" {
				t.Cleanup(func() {
					git(t, repo, "worktree", "remove", "--force", cleanupRoot)
				})
			}
			if _, err := manager.Inspect(context.Background(), "discord-conversation", assignment); err == nil {
				t.Fatal("unverifiable assignment was accepted")
			}
		})
	}
}

func TestReconciliationRejectsUnsafeOrForeignAssignments(t *testing.T) {
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
	if _, err := manager.Inspect(context.Background(), "other-conversation", assignment); err == nil {
		t.Fatal("ownership mismatch was accepted")
	}
	if err := manager.Retire(context.Background(), "other-conversation", assignment); err == nil {
		t.Fatal("foreign assignment retirement was accepted")
	}
	if _, err := manager.Inspect(context.Background(), "discord-conversation", Assignment{Root: filepath.Dir(assignment.Root), Branch: assignment.Branch}); err == nil {
		t.Fatal("broad retirement target was accepted")
	}

	manager.Remove(context.Background(), assignment)
	foreign := t.TempDir()
	if err := os.Symlink(foreign, assignment.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Inspect(context.Background(), "discord-conversation", assignment); err == nil {
		t.Fatal("symlink target was accepted")
	}
	if err := manager.Retire(context.Background(), "discord-conversation", assignment); err == nil {
		t.Fatal("symlink retirement target was accepted")
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

func TestManagedWorktreesSupportABaseThatIsItselfLinked(t *testing.T) {
	repo, _ := gitProject(t)
	linked := filepath.Join(filepath.Dir(repo), "linked-base")
	git(t, repo, "worktree", "add", "--quiet", "-b", "linked-base", linked, "HEAD")
	t.Cleanup(func() { git(t, repo, "worktree", "remove", "--force", linked) })
	base, err := project.Load(linked, "claude")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(context.Background(), base, "/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	_, assignment, err := manager.Provision(context.Background(), "discord-conversation")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Remove(context.Background(), assignment) })
	if _, err := manager.Inspect(context.Background(), "discord-conversation", assignment); err != nil {
		t.Fatalf("linked-base ownership validation = %v", err)
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
