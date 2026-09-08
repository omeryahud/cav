package claude

import (
	"errors"
	"strings"
	"testing"
)

// withGit swaps execGit for a stub that dispatches on the joined args.
func withGit(t *testing.T, fn func(dir string, args []string) ([]byte, error)) {
	t.Helper()
	orig := execGit
	execGit = func(dir string, args ...string) ([]byte, error) { return fn(dir, args) }
	t.Cleanup(func() { execGit = orig })
}

func TestWorktreesParse(t *testing.T) {
	withGit(t, func(_ string, args []string) ([]byte, error) {
		if strings.Join(args, " ") == "worktree list --porcelain" {
			return []byte("worktree /r/cav\nHEAD abc\nbranch refs/heads/master\n\n" +
				"worktree /r/cav/.claude/worktrees/dp\nHEAD def\nbranch refs/heads/dp\n\n" +
				"worktree /r/cav/detached\nHEAD 999\ndetached\n"), nil
		}
		return nil, errors.New("unexpected")
	})
	wts := Worktrees("/r/cav")
	if len(wts) != 3 {
		t.Fatalf("got %d worktrees: %+v", len(wts), wts)
	}
	if wts[0] != (Worktree{Path: "/r/cav", Branch: "master"}) {
		t.Errorf("main = %+v", wts[0])
	}
	if wts[1] != (Worktree{Path: "/r/cav/.claude/worktrees/dp", Branch: "dp"}) {
		t.Errorf("linked = %+v", wts[1])
	}
	if wts[2].Branch != "" {
		t.Errorf("detached worktree should have no branch, got %q", wts[2].Branch)
	}
}

func TestRepoRoot(t *testing.T) {
	withGit(t, func(_ string, args []string) ([]byte, error) {
		if strings.Join(args, " ") == "rev-parse --show-toplevel" {
			return []byte("/r/cav\n"), nil
		}
		return nil, errors.New("not a repo")
	})
	if root, ok := RepoRoot("/r/cav/pkg"); !ok || root != "/r/cav" {
		t.Errorf("RepoRoot = %q %v", root, ok)
	}

	withGit(t, func(string, []string) ([]byte, error) { return nil, errors.New("fatal: not a git repository") })
	if _, ok := RepoRoot("/tmp/nope"); ok {
		t.Error("non-repo should report ok=false")
	}
}

func TestDefaultBranch(t *testing.T) {
	// origin/HEAD known -> that branch.
	withGit(t, func(_ string, args []string) ([]byte, error) {
		if strings.HasPrefix(strings.Join(args, " "), "symbolic-ref --short refs/remotes/origin/HEAD") {
			return []byte("origin/develop\n"), nil
		}
		return nil, errors.New("x")
	})
	if b := DefaultBranch("/r"); b != "develop" {
		t.Errorf("origin/HEAD develop -> %q", b)
	}

	// No origin, main exists.
	withGit(t, func(_ string, args []string) ([]byte, error) {
		j := strings.Join(args, " ")
		if strings.Contains(j, "origin/HEAD") {
			return nil, errors.New("no origin")
		}
		if strings.Contains(j, "refs/heads/main") {
			return []byte(""), nil
		}
		return nil, errors.New("no such ref")
	})
	if b := DefaultBranch("/r"); b != "main" {
		t.Errorf("main fallback -> %q", b)
	}
}
