package claude

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestWorktreeLifecycleReal exercises the real git commands (add/list/remove)
// against a throwaway repo, covering the seam the stubbed tests can't.
func TestWorktreeLifecycleReal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d // git reports the resolved path (macOS /var -> /private/var)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")

	if root, ok := RepoRoot(filepath.Join(dir)); !ok || root != dir {
		t.Fatalf("RepoRoot = %q %v, want %q", root, ok, dir)
	}
	if b := DefaultBranch(dir); b != "main" {
		t.Errorf("DefaultBranch = %q, want main", b)
	}

	wtPath := filepath.Join(dir, ".claude", "worktrees", "feat")
	if err := AddWorktree(dir, wtPath, "feat", "main"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	wts := Worktrees(dir)
	if len(wts) != 2 {
		t.Fatalf("after add, worktrees = %+v", wts)
	}
	var found *Worktree
	for i := range wts {
		if filepathBase(wts[i].Path) == "feat" {
			found = &wts[i]
		}
	}
	if found == nil || found.Branch != "feat" {
		t.Fatalf("new worktree not found or wrong branch: %+v", wts)
	}
	if b := BranchAt(wtPath); b != "feat" {
		t.Errorf("BranchAt(feat) = %q", b)
	}
	// The real regression: from inside the linked worktree, show-toplevel gives
	// the worktree, but MainRoot must give the main checkout so the repo groups.
	if r, _ := RepoRoot(wtPath); r != wtPath {
		t.Errorf("RepoRoot(worktree) = %q, want the worktree itself %q", r, wtPath)
	}
	if got := MainRoot(wtPath); got != dir {
		t.Errorf("MainRoot(worktree) = %q, want the main checkout %q", got, dir)
	}

	// A second add on the same branch must fail (branch already exists).
	if err := AddWorktree(dir, filepath.Join(dir, ".claude", "worktrees", "feat2"), "feat", "main"); err == nil {
		t.Error("adding a worktree on an existing branch should fail")
	}

	if err := RemoveWorktree(dir, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if wts := Worktrees(dir); len(wts) != 1 {
		t.Errorf("after remove, worktrees = %+v", wts)
	}
}

func filepathBase(p string) string { return filepath.Base(p) }
