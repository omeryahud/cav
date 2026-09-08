package claude

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is one entry from `git worktree list`: the checkout directory and
// the branch checked out there (empty when detached).
type Worktree struct {
	Path   string
	Branch string
}

// execGit runs git in dir. It is a package variable so tests can stub it.
var execGit = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Output()
}

// RepoRoot returns the top-level directory of the git repo containing cwd,
// and whether cwd is in a repo at all. Inside a linked worktree this is the
// worktree's own root, not the main checkout — use MainRoot to group by repo.
func RepoRoot(cwd string) (string, bool) {
	out, err := execGit(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	return root, root != ""
}

// MainRoot returns the repo's main checkout for cwd: the first entry of
// `git worktree list`, which git always reports as the main worktree. This is
// the same value for a session in the main checkout and for one inside any
// linked worktree, so every worktree of a repo groups under one node. Returns
// "" when cwd is not in a git repo.
func MainRoot(cwd string) string {
	out, err := execGit(cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			return strings.TrimPrefix(line, "worktree ")
		}
	}
	return ""
}

// Worktrees lists every worktree of the repo at repoRoot (the main checkout
// plus each linked worktree), each with its checked-out branch.
func Worktrees(repoRoot string) []Worktree {
	out, err := execGit(repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var wts []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path != "" {
			wts = append(wts, cur)
		}
		cur = Worktree{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur.Path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			// "branch refs/heads/foo" -> "foo"
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush()
	return wts
}

// DefaultBranch is the repo's default branch: origin/HEAD when known, else
// main, else master, else the branch currently checked out at the root.
func DefaultBranch(repoRoot string) string {
	if out, err := execGit(repoRoot, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" {
			return strings.TrimPrefix(b, "origin/")
		}
	}
	for _, b := range []string{"main", "master"} {
		if _, err := execGit(repoRoot, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b
		}
	}
	if out, err := execGit(repoRoot, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		return strings.TrimSpace(string(out))
	}
	return "main"
}

// BranchAt is the branch checked out in the worktree at path (for basing a new
// worktree on a linked worktree's branch), or "" if it can't be determined.
func BranchAt(path string) string {
	out, err := execGit(path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	if b := strings.TrimSpace(string(out)); b != "HEAD" {
		return b
	}
	return ""
}

// AddWorktree creates a new worktree at path on a new branch, based on base.
// It returns any git error output for the footer.
func AddWorktree(repoRoot, path, branch, base string) error {
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	cmd := exec.Command("git", "worktree", "add", "-b", branch, path, base)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return &gitError{op: "worktree add", detail: strings.TrimSpace(string(out))}
	}
	return nil
}

// RemoveWorktree removes the worktree at path (without --force, so git refuses
// a dirty worktree). The branch is left in place.
func RemoveWorktree(repoRoot, path string) error {
	cmd := exec.Command("git", "worktree", "remove", path)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return &gitError{op: "worktree remove", detail: strings.TrimSpace(string(out))}
	}
	return nil
}

type gitError struct {
	op     string
	detail string
}

func (e *gitError) Error() string {
	if e.detail != "" {
		return e.op + ": " + e.detail
	}
	return e.op
}
