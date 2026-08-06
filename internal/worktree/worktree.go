package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"hctl/internal/project"
	"hctl/internal/secureenv"
	"hctl/internal/setup"
	"hctl/internal/tool"
)

type Assignment struct {
	Root   string
	Branch string
}

type Manager struct {
	base       *project.Project
	executable string
	repo       string
	parent     string
}

func New(ctx context.Context, base *project.Project, executable string) (*Manager, error) {
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
	parent := filepath.Join(filepath.Dir(repo), "."+filepath.Base(repo)+".hctl-worktrees")
	return &Manager{base: base, executable: executable, repo: repo, parent: parent}, nil
}

func (m *Manager) Provision(ctx context.Context, conversation string) (*project.Project, Assignment, error) {
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
	expected := m.expected(conversation)
	if assignment != expected {
		return nil, errors.New("durable conversation worktree assignment is invalid")
	}
	root, err := filepath.EvalSymlinks(assignment.Root)
	if err != nil || root != assignment.Root {
		return nil, errors.New("conversation worktree is missing or unsafe")
	}
	branch, err := gitOutput(ctx, assignment.Root, "branch", "--show-current")
	if err != nil || strings.TrimSpace(branch) != assignment.Branch {
		return nil, errors.New("conversation worktree branch does not match durable state")
	}
	return m.prepare(ctx, assignment)
}

func (m *Manager) Remove(ctx context.Context, assignment Assignment) {
	m.cleanupWithContext(ctx, assignment)
}

func (m *Manager) expected(conversation string) Assignment {
	digest := sha256.Sum256([]byte(m.base.AgentID + "\x00" + conversation))
	suffix := hex.EncodeToString(digest[:10])
	return Assignment{Root: filepath.Join(m.parent, suffix), Branch: "hctl/" + m.base.AgentID + "/" + suffix}
}

func (m *Manager) prepare(ctx context.Context, assignment Assignment) (*project.Project, error) {
	source := m.base.SourceRoot
	if relative, err := filepath.Rel(m.repo, source); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		source = filepath.Join(assignment.Root, relative)
	}
	p, err := project.LoadRelocated(source, m.base.Harness, assignment.Root, m.base)
	if err != nil {
		return nil, errors.New("conversation worktree does not contain the selected agent source")
	}
	if err := setup.VerifyWritableChannel(p); err == nil {
		return p, nil
	}
	prepareCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := tool.Prepare(prepareCtx, p.SourceRoot, p.WorkspaceRoot, p.SourceFingerprint, p.Tools); err != nil {
		return nil, err
	}
	if _, err := setup.ApplyWritableChannel(p, m.executable); err != nil {
		return nil, err
	}
	return p, nil
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
