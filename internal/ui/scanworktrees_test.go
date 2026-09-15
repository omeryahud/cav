package ui

import (
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
)

func TestScanWorktreesRetriesEmptyAfterTransientGitFailure(t *testing.T) {
	origMain, origWt := mainRootFn, worktreesFn
	origCache, origWtCache, origAt := cwdRepoCache, wtCache, wtScanAt
	t.Cleanup(func() {
		mainRootFn, worktreesFn = origMain, origWt
		cwdRepoCache, wtCache, wtScanAt = origCache, origWtCache, origAt
	})

	cwdRepoCache = map[string]string{}
	wtCache = map[string][]claude.Worktree{}
	wtScanAt = time.Time{}

	gitUp := false
	mainCalls := 0
	mainRootFn = func(cwd string) string {
		mainCalls++
		if gitUp {
			return "/repo"
		}
		return ""
	}
	worktreesFn = func(repo string) []claude.Worktree {
		return []claude.Worktree{{Path: repo, Branch: "main"}}
	}

	cwd := "/repo/.claude/worktrees/feat"

	repoOf, _ := scanWorktrees([]string{cwd})
	if repoOf[cwd] != "" {
		t.Fatalf("git down: repo should be empty, got %q", repoOf[cwd])
	}

	gitUp = true
	callsBefore := mainCalls
	repoOf, _ = scanWorktrees([]string{cwd})
	if repoOf[cwd] != "" {
		t.Errorf("within 15s the empty result is cached, got %q", repoOf[cwd])
	}
	if mainCalls != callsBefore {
		t.Errorf("MainRoot should not re-run within the 15s window")
	}

	wtScanAt = time.Now().Add(-20 * time.Second) // force a full scan
	repoOf, wt := scanWorktrees([]string{cwd})
	if repoOf[cwd] != "/repo" {
		t.Errorf("after a full scan the repo should re-resolve, got %q", repoOf[cwd])
	}
	if len(wt["/repo"]) == 0 {
		t.Errorf("worktrees should be fetched for the recovered repo")
	}

	callsBefore = mainCalls
	wtScanAt = time.Now().Add(-20 * time.Second)
	scanWorktrees([]string{cwd})
	if mainCalls != callsBefore {
		t.Errorf("a resolved repo must not be re-resolved on later full scans")
	}
}
