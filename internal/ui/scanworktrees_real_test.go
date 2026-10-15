package ui

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/omeryahud/cav/internal/claude"
)

func TestScanWorktreesRealGitPicksUpNewWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	resetWorktreeScan(t)
	mainRootFn, worktreesFn = claude.MainRoot, claude.Worktrees
	topLevelFn = func(cwd string) string { root, _ := claude.RepoRoot(cwd); return root }

	dir := t.TempDir()
	if d, err := filepath.EvalSymlinks(dir); err == nil {
		dir = d // git reports resolved paths (macOS /var -> /private/var)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")

	scanWorktrees([]string{dir}) // full scan: only the main checkout exists

	wt := filepath.Join(dir, ".claude", "worktrees", "new")
	git("worktree", "add", "-q", "-b", "new", wt)

	stale := treeModel(t, dir, wtCache[dir], wt)
	if n, _ := nodeAt(stale.buildDirTree(), wt); n.linked {
		t.Fatal("setup: with the stale list the session should not sit on a worktree node")
	}

	pastCooldown(dir)
	repoOf, lists := scanWorktrees([]string{dir, wt}) // not a full scan
	if repoOf[wt] != dir {
		t.Fatalf("repoOf[%s] = %q, want %q", wt, repoOf[wt], dir)
	}
	if !hasWorktree(lists[dir], wt) {
		t.Fatalf("the new worktree should be listed on this refresh, got %+v", lists[dir])
	}
	fixed := treeModel(t, dir, lists[dir], wt)
	if n, ok := nodeAt(fixed.buildDirTree(), wt); !ok || !n.linked {
		t.Errorf("the session should sit on its worktree node, got %+v ok=%v", n, ok)
	}
}
