package ui

import (
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
)

// resetWorktreeScan gives a test fresh worktree-scan state (it is package-global
// and shared with the refresh loop) and restores the original afterwards.
func resetWorktreeScan(t *testing.T) {
	t.Helper()
	origMain, origWt, origTop := mainRootFn, worktreesFn, topLevelFn
	origRepo, origTop2, origWtCache := cwdRepoCache, cwdTopCache, wtCache
	origFetched, origAt, origForce := wtFetchedAt, wtScanAt, wtForceScan.Load()
	t.Cleanup(func() {
		mainRootFn, worktreesFn, topLevelFn = origMain, origWt, origTop
		cwdRepoCache, cwdTopCache, wtCache = origRepo, origTop2, origWtCache
		wtFetchedAt, wtScanAt = origFetched, origAt
		wtForceScan.Store(origForce)
	})
	cwdRepoCache = map[string]string{}
	cwdTopCache = map[string]string{}
	wtCache = map[string][]claude.Worktree{}
	wtFetchedAt = map[string]time.Time{}
	wtScanAt = time.Time{}
	wtForceScan.Store(false)
	topLevelFn = func(cwd string) string { return cwd }
}

// pastCooldown makes the next scan of repo eligible for an early relist.
func pastCooldown(repo string) { wtFetchedAt[repo] = time.Now().Add(-wtRetryAfter - time.Second) }

func hasWorktree(list []claude.Worktree, path string) bool {
	for _, wt := range list {
		if wt.Path == path {
			return true
		}
	}
	return false
}

func TestScanWorktreesRetriesEmptyAfterTransientGitFailure(t *testing.T) {
	resetWorktreeScan(t)
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
		return []claude.Worktree{{Path: repo, Branch: "main"}, {Path: "/repo/.claude/worktrees/feat", Branch: "feat"}}
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

// A session in a worktree the cached list lacks (created, or briefly unlistable,
// since the last full scan) must get its worktree on the next refresh, not after
// the 15s full scan. Otherwise it shows under the main checkout until then.
func TestScanWorktreesRelistsWhenSessionInUnlistedWorktree(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/repo/.claude/worktrees/new"
	mainRootFn = func(string) string { return repo }
	gitHasWorktree := false
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree {
		listCalls++
		if gitHasWorktree {
			return []claude.Worktree{{Path: repo}, {Path: wt, Branch: "new"}}
		}
		return []claude.Worktree{{Path: repo}}
	}

	_, lists := scanWorktrees([]string{repo})
	if hasWorktree(lists[repo], wt) {
		t.Fatal("setup: the worktree should not be listed yet")
	}

	gitHasWorktree = true
	pastCooldown(repo)
	_, lists = scanWorktrees([]string{repo, wt})
	if !hasWorktree(lists[repo], wt) {
		t.Errorf("a session in an unlisted worktree should trigger a relist on this refresh, got %+v", lists[repo])
	}
	if listCalls != 2 {
		t.Errorf("expected exactly one early relist, got %d list calls", listCalls)
	}

	before := listCalls
	pastCooldown(repo)
	scanWorktrees([]string{repo, wt})
	if listCalls != before {
		t.Error("once the worktree is listed, refreshes should use the cache again")
	}
}

func TestScanWorktreesPlainSubdirIsNotStale(t *testing.T) {
	resetWorktreeScan(t)
	repo, sub := "/repo", "/repo/pkg/api"
	mainRootFn = func(string) string { return repo }
	topLevelFn = func(string) string { return repo } // git: a plain subdir of the main checkout
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree { listCalls++; return []claude.Worktree{{Path: repo}} }

	scanWorktrees([]string{repo, sub})
	pastCooldown(repo)
	scanWorktrees([]string{repo, sub})
	if listCalls != 1 {
		t.Errorf("a plain subdirectory must not trigger relists, got %d list calls", listCalls)
	}
}

func TestScanWorktreesFailedListingKeepsPrevious(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/repo/.claude/worktrees/feat"
	mainRootFn = func(string) string { return repo }
	gitOK := true
	worktreesFn = func(string) []claude.Worktree {
		if gitOK {
			return []claude.Worktree{{Path: repo}, {Path: wt}}
		}
		return nil
	}

	scanWorktrees([]string{wt})
	gitOK = false
	wtForceScan.Store(true) // the next scan is full and relists
	_, lists := scanWorktrees([]string{wt})
	if !hasWorktree(lists[repo], wt) {
		t.Errorf("a failed listing must not wipe a good one, got %+v", lists[repo])
	}
}

func TestScanWorktreesRetriesFailedFirstListingAfterCooldown(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/repo/.claude/worktrees/feat"
	mainRootFn = func(string) string { return repo }
	gitOK := false
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree {
		listCalls++
		if gitOK {
			return []claude.Worktree{{Path: repo}, {Path: wt}}
		}
		return nil
	}

	_, lists := scanWorktrees([]string{wt})
	if len(lists[repo]) != 0 {
		t.Fatalf("setup: first listing should fail, got %+v", lists[repo])
	}
	gitOK = true
	scanWorktrees([]string{wt})
	if listCalls != 1 {
		t.Errorf("a retry must wait for the cooldown, got %d list calls", listCalls)
	}
	pastCooldown(repo)
	_, lists = scanWorktrees([]string{wt})
	if !hasWorktree(lists[repo], wt) {
		t.Errorf("after the cooldown a failed first listing should be retried, got %+v", lists[repo])
	}
}

func TestScanWorktreesStaleRelistIsRateLimited(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/elsewhere/ghost-worktree"
	mainRootFn = func(string) string { return repo }
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree { listCalls++; return []claude.Worktree{{Path: repo}} } // git never lists it

	scanWorktrees([]string{wt})
	for i := 0; i < 20; i++ {
		scanWorktrees([]string{wt})
	}
	if listCalls != 1 {
		t.Errorf("relists inside the cooldown must be suppressed, got %d list calls", listCalls)
	}
	pastCooldown(repo)
	scanWorktrees([]string{wt})
	if listCalls != 2 {
		t.Errorf("after the cooldown exactly one more relist is allowed, got %d", listCalls)
	}
}

func TestScanWorktreesRetriesFailedTopLevelAfterFullScan(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/repo/.claude/worktrees/new"
	mainRootFn = func(string) string { return repo }
	gitOK := false
	topLevelFn = func(cwd string) string {
		if gitOK {
			return cwd
		}
		return ""
	}
	worktreesFn = func(string) []claude.Worktree { return []claude.Worktree{{Path: repo}} }

	scanWorktrees([]string{repo, wt})
	pastCooldown(repo)
	scanWorktrees([]string{repo, wt}) // stale check runs; the toplevel lookup fails
	if top, ok := cwdTopCache[wt]; !ok || top != "" {
		t.Fatalf("setup: expected a cached failed lookup, got %q ok=%v", top, ok)
	}
	gitOK = true
	wtForceScan.Store(true)
	scanWorktrees([]string{repo, wt}) // full scan forgets failed lookups
	if _, ok := cwdTopCache[wt]; ok {
		t.Error("a failed toplevel lookup should be forgotten on a full scan so it is retried")
	}
}

func TestWorktreeForceScanFlagForcesFullScan(t *testing.T) {
	resetWorktreeScan(t)
	mainRootFn = func(string) string { return "/repo" }
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree { listCalls++; return []claude.Worktree{{Path: "/repo"}} }

	scanWorktrees([]string{"/repo"})
	scanWorktrees([]string{"/repo"})
	if listCalls != 1 {
		t.Fatalf("setup: second scan should use the cache, got %d", listCalls)
	}
	wtForceScan.Store(true)
	scanWorktrees([]string{"/repo"})
	if listCalls != 2 {
		t.Errorf("the force flag should relist, got %d", listCalls)
	}
	if wtForceScan.Load() {
		t.Error("the force flag must be cleared once consumed")
	}
}
