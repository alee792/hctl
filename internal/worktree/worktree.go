package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"hctl/internal/integration"
	"hctl/internal/project"
	"hctl/internal/secureenv"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

type Assignment struct {
	Root   string
	Branch string
}

type Inspection struct {
	Clean  bool
	Merged bool
	Reason string
}

// NativeMCPResolver returns current offline-verified native process metadata
// for a relocated project. It is capability-generic and resolves no ambient
// environment value.
type NativeMCPResolver func(context.Context, *project.Project) ([]integration.NativeMCPLaunchDescriptor, error)

type Manager struct {
	mu         sync.Mutex
	base       *project.Project
	executable string
	repo       string
	common     string
	parent     string
	nativeMCP  NativeMCPResolver
}

func New(ctx context.Context, base *project.Project, executable string) (*Manager, error) {
	return NewWithNativeMCP(ctx, base, executable, nil)
}

func NewWithNativeMCP(ctx context.Context, base *project.Project, executable string, nativeMCP NativeMCPResolver) (*Manager, error) {
	if base == nil || executable == "" || !filepath.IsAbs(executable) {
		return nil, errors.New("writable worktrees require a project and hctl executable")
	}
	repo, err := gitOutput(ctx, base.WorkspaceRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, errors.New("selected workspace is not a supported Git checkout")
	}
	repo, err = filepath.EvalSymlinks(strings.TrimSpace(repo))
	if err != nil || repo != base.WorkspaceRoot {
		return nil, errors.New("selected workspace must be the root of a Git checkout")
	}
	common, err := gitOutput(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, errors.New("cannot resolve selected Git checkout ownership")
	}
	common, err = filepath.EvalSymlinks(strings.TrimSpace(common))
	if err != nil {
		return nil, errors.New("cannot resolve selected Git checkout ownership")
	}
	parent := filepath.Join(filepath.Dir(repo), "."+filepath.Base(repo)+".hctl-worktrees")
	return &Manager{base: base, executable: executable, repo: repo, common: common, parent: parent, nativeMCP: nativeMCP}, nil
}

func (m *Manager) Provision(ctx context.Context, conversation string) (*project.Project, Assignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	assignment := m.expected(conversation)
	if err := ensurePrivateDirectory(m.parent); err != nil {
		return nil, Assignment{}, err
	}
	if _, err := os.Lstat(assignment.Root); !os.IsNotExist(err) {
		return nil, Assignment{}, errors.New("conversation worktree path already exists without a durable assignment")
	}
	if _, err := gitOutput(ctx, m.repo, "show-ref", "--verify", "--quiet", "refs/heads/"+assignment.Branch); err == nil {
		return nil, Assignment{}, errors.New("conversation worktree branch already exists without a durable assignment")
	}
	if _, err := gitOutput(ctx, m.repo, "worktree", "add", "-b", assignment.Branch, assignment.Root, "HEAD"); err != nil {
		return nil, Assignment{}, errors.New("cannot create conversation worktree")
	}
	prepared, err := m.prepare(ctx, assignment)
	if err != nil {
		m.cleanup(assignment)
		return nil, Assignment{}, err
	}
	return prepared, assignment, nil
}

func (m *Manager) Resolve(ctx context.Context, conversation string, assignment Assignment) (*project.Project, error) {
	if err := m.validateCheckout(ctx, conversation, assignment); err != nil {
		return nil, err
	}
	return m.prepare(ctx, assignment)
}

func (m *Manager) Inspect(ctx context.Context, conversation string, assignment Assignment) (Inspection, error) {
	if err := m.validateCheckout(ctx, conversation, assignment); err != nil {
		return Inspection{}, err
	}
	p, err := m.relocatedProject(assignment)
	if err != nil {
		return Inspection{}, err
	}
	managedFiles, err := (setup.WritableChannel{Project: p}).OwnedFiles()
	if err != nil {
		return Inspection{}, errors.New("cannot verify managed setup before worktree reconciliation")
	}
	clean, err := worktreeContainsOnlyManagedFiles(ctx, assignment.Root, managedFiles)
	if err != nil {
		return Inspection{}, errors.New("cannot verify conversation worktree cleanliness")
	}
	merged, err := gitSuccess(ctx, m.repo, "merge-base", "--is-ancestor", assignment.Branch, "HEAD")
	if err != nil {
		return Inspection{}, errors.New("cannot verify whether conversation work is merged")
	}
	reason := "clean and merged"
	if !clean {
		reason = "dirty or untracked work"
	} else if !merged {
		reason = "unmerged commits"
	}
	return Inspection{Clean: clean, Merged: merged, Reason: reason}, nil
}

// Retire removes one exact managed assignment after the caller has durably
// recorded retirement intent. A missing path or branch is accepted only when
// the remaining Git evidence proves an earlier cleanup step already finished.
func (m *Manager) Retire(ctx context.Context, conversation string, assignment Assignment) error {
	if assignment != m.expected(conversation) {
		return errors.New("durable conversation worktree assignment is invalid")
	}
	info, statErr := os.Lstat(assignment.Root)
	switch {
	case statErr == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("conversation worktree retirement target is unsafe")
		}
		inspection, err := m.Inspect(ctx, conversation, assignment)
		if err != nil {
			if validateErr := m.validateCheckout(ctx, conversation, assignment); validateErr != nil {
				return validateErr
			}
			managedFiles := []string(nil)
			p, projectErr := m.relocatedProject(assignment)
			if projectErr == nil {
				managedFiles, _ = (setup.WritableChannel{Project: p}).RetirementFiles()
			}
			clean, statusErr := worktreeContainsOnlyManagedFiles(ctx, assignment.Root, managedFiles)
			merged, mergeErr := gitSuccess(ctx, m.repo, "merge-base", "--is-ancestor", assignment.Branch, "HEAD")
			if statusErr != nil || !clean || mergeErr != nil || !merged {
				return errors.New("interrupted conversation worktree cleanup is not safely resumable")
			}
			if managedFiles != nil {
				retained, retainErr := trackedPaths(ctx, assignment.Root, managedFiles)
				if retainErr != nil {
					return retainErr
				}
				if err := (setup.WritableChannel{Project: p}).Remove(retained); err != nil {
					return errors.New("cannot resume managed setup cleanup; durable ownership was preserved")
				}
			}
		} else {
			if !inspection.Clean || !inspection.Merged {
				return fmt.Errorf("conversation worktree is not disposable: %s", inspection.Reason)
			}
			p, err := m.relocatedProject(assignment)
			if err != nil {
				return err
			}
			channel := setup.WritableChannel{Project: p}
			managedFiles, err := channel.RetirementFiles()
			if err != nil {
				return err
			}
			retained, err := trackedPaths(ctx, assignment.Root, managedFiles)
			if err != nil {
				return err
			}
			if err := channel.Remove(retained); err != nil {
				return errors.New("cannot remove managed setup; durable ownership was preserved")
			}
		}
		if _, err := gitOutput(ctx, m.repo, "worktree", "remove", assignment.Root); err != nil {
			return errors.New("cannot retire conversation worktree; durable ownership was preserved")
		}
	case !os.IsNotExist(statErr):
		return errors.New("cannot inspect conversation worktree retirement target")
	default:
		otherRoot, err := m.branchWorktree(ctx, assignment.Branch)
		if err != nil {
			return err
		}
		if otherRoot != "" {
			return errors.New("conversation branch is attached to a foreign worktree")
		}
	}
	exists, err := m.branchExists(ctx, assignment.Branch)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	merged, err := gitSuccess(ctx, m.repo, "merge-base", "--is-ancestor", assignment.Branch, "HEAD")
	if err != nil || !merged {
		return errors.New("conversation worktree branch is not provably merged")
	}
	if _, err := gitOutput(ctx, m.repo, "branch", "-d", assignment.Branch); err != nil {
		return errors.New("cannot retire conversation branch; durable ownership was preserved")
	}
	return nil
}

func (m *Manager) Remove(ctx context.Context, assignment Assignment) {
	m.cleanupWithContext(ctx, assignment)
}

func (m *Manager) expected(conversation string) Assignment {
	digest := sha256.Sum256([]byte(m.base.AgentID + "\x00" + conversation))
	suffix := hex.EncodeToString(digest[:10])
	return Assignment{Root: filepath.Join(m.parent, suffix), Branch: "hctl/" + m.base.AgentID + "/" + suffix}
}

func (m *Manager) validateCheckout(ctx context.Context, conversation string, assignment Assignment) error {
	if assignment != m.expected(conversation) {
		return errors.New("durable conversation worktree assignment is invalid")
	}
	info, err := os.Lstat(assignment.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("conversation worktree is missing or unsafe")
	}
	root, err := filepath.EvalSymlinks(assignment.Root)
	if err != nil || root != assignment.Root {
		return errors.New("conversation worktree is missing or unsafe")
	}
	top, err := gitOutput(ctx, assignment.Root, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(top) != assignment.Root {
		return errors.New("conversation worktree root does not match durable state")
	}
	common, err := gitOutput(ctx, assignment.Root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return errors.New("cannot verify conversation worktree ownership")
	}
	actualCommon, err := filepath.EvalSymlinks(strings.TrimSpace(common))
	if err != nil || actualCommon != m.common {
		return errors.New("conversation worktree belongs to a different repository")
	}
	branch, err := gitOutput(ctx, assignment.Root, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != assignment.Branch {
		return errors.New("conversation worktree branch does not match durable state")
	}
	registered, err := m.branchWorktree(ctx, assignment.Branch)
	if err != nil || registered != assignment.Root {
		return errors.New("conversation worktree registration does not match durable state")
	}
	return nil
}

func (m *Manager) branchExists(ctx context.Context, branch string) (bool, error) {
	exists, err := gitSuccess(ctx, m.repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return false, errors.New("cannot inspect conversation worktree branch")
	}
	return exists, nil
}

func (m *Manager) branchWorktree(ctx context.Context, branch string) (string, error) {
	output, err := gitOutput(ctx, m.repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", errors.New("cannot inspect managed Git worktrees")
	}
	var current string
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return current, nil
		}
	}
	return "", nil
}

func (m *Manager) prepare(ctx context.Context, assignment Assignment) (*project.Project, error) {
	p, err := m.relocatedProject(assignment)
	if err != nil {
		return nil, err
	}
	var nativeMCP []integration.NativeMCPLaunchDescriptor
	if m.nativeMCP != nil {
		nativeMCP, err = m.nativeMCP(ctx, p)
		if err != nil {
			return nil, err
		}
	}
	if err := setup.ValidateNativeMCP(p, nativeMCP); err != nil {
		return nil, err
	}
	channel := setup.WritableChannel{Project: p}
	if err := channel.Verify(); err == nil && len(nativeMCP) == 0 {
		return p, nil
	}
	prepareCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := tool.Prepare(prepareCtx, p.SourceRoot, p.WorkspaceRoot, p.SourceFingerprint, p.Tools); err != nil {
		return nil, err
	}
	if _, err := channel.Apply(m.executable, nativeMCP); err != nil {
		return nil, err
	}
	return p, nil
}

func (m *Manager) relocatedProject(assignment Assignment) (*project.Project, error) {
	source := m.base.SourceRoot
	if relative, err := filepath.Rel(m.repo, source); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		source = filepath.Join(assignment.Root, relative)
	}
	p, err := project.LoadRelocated(source, m.base.Harness, assignment.Root, m.base)
	if err != nil {
		return nil, errors.New("conversation worktree does not contain the selected agent source")
	}
	return p, nil
}

func worktreeContainsOnlyManagedFiles(ctx context.Context, root string, managedFiles []string) (bool, error) {
	status, err := gitOutput(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	ignored, err := gitOutput(ctx, root, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return false, err
	}
	managed := make(map[string]bool, len(managedFiles))
	for _, path := range managedFiles {
		managed[path] = true
	}
	for _, record := range strings.Split(status, "\x00") {
		if record == "" {
			continue
		}
		if len(record) < 4 || strings.ContainsAny(record[:2], "RC") || !managed[record[3:]] {
			return false, nil
		}
	}
	for _, path := range strings.Split(ignored, "\x00") {
		if path != "" && !managed[path] {
			return false, nil
		}
	}
	return true, nil
}

func trackedPaths(ctx context.Context, root string, paths []string) (map[string]bool, error) {
	tracked := map[string]bool{}
	for _, path := range paths {
		yes, err := gitSuccess(ctx, root, "ls-files", "--error-unmatch", "--", path)
		if err != nil {
			return nil, errors.New("cannot verify tracked managed setup files")
		}
		tracked[path] = yes
	}
	return tracked, nil
}

func (m *Manager) cleanup(assignment Assignment) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.cleanupWithContext(ctx, assignment)
}

func (m *Manager) cleanupWithContext(ctx context.Context, assignment Assignment) {
	if assignment != m.expectedFromAssignment(assignment) {
		return
	}
	_, _ = gitOutput(ctx, m.repo, "worktree", "remove", "--force", assignment.Root)
	_, _ = gitOutput(ctx, m.repo, "branch", "-D", assignment.Branch)
}

func (m *Manager) expectedFromAssignment(assignment Assignment) Assignment {
	if filepath.Dir(assignment.Root) != m.parent || !strings.HasPrefix(assignment.Branch, "hctl/"+m.base.AgentID+"/") || filepath.Base(assignment.Root) != filepath.Base(assignment.Branch) {
		return Assignment{}
	}
	return assignment
}

func ensurePrivateDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("conversation worktree parent is unsafe")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect conversation worktree parent")
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return errors.New("cannot create conversation worktree parent")
	}
	return nil
}

func gitOutput(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	command.Env = secureenv.Child()
	output, err := command.Output()
	if err != nil || len(output) > 64<<10 {
		return "", errors.New("git operation failed")
	}
	return string(output), nil
}

func gitSuccess(ctx context.Context, directory string, args ...string) (bool, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", directory}, args...)...)
	command.Env = secureenv.Child()
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, errors.New("git operation failed")
}
