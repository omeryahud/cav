package ui

import (
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
)

// resetWorktreeScan gives a test fresh worktree-scan state (it is package-global
// and shared with the refresh loop) and restores the original afterwards.
func resetWorktreeScan(tb testing.TB) {
	tb.Helper()
	origMainFn, origWtFn, origTopFn := mainRootFn, worktreesFn, topLevelFn
	origRepoCache, origTopCache, origWtCache := cwdRepoCache, cwdTopCache, wtCache
	origFetched, origStuck, origAt, origForce := wtFetchedAt, wtStuckDirs, wtScanAt, wtForceScan.Load()
	tb.Cleanup(func() {
		mainRootFn, worktreesFn, topLevelFn = origMainFn, origWtFn, origTopFn
		cwdRepoCache, cwdTopCache, wtCache = origRepoCache, origTopCache, origWtCache
		wtFetchedAt, wtStuckDirs, wtScanAt = origFetched, origStuck, origAt
		wtForceScan.Store(origForce)
	})
	cwdRepoCache = map[string]string{}
	cwdTopCache = map[string]string{}
	wtCache = map[string][]claude.Worktree{}
	wtFetchedAt = map[string]time.Time{}
	wtStuckDirs = map[string]bool{}
	wtScanAt = time.Time{}
	wtForceScan.Store(false)
	topLevelFn = func(cwd string) string { return cwd }
}

// pastCooldown makes the next scan of repo eligible for an early relist.
func pastCooldown(repo string) { wtFetchedAt[repo] = time.Now().Add(-wtRetryAfter - time.Second) }

// scanPastCooldown runs n non-full scans, each eligible for an early relist.
func scanPastCooldown(repo string, cwds []string, n int) {
	for i := 0; i < n; i++ {
		pastCooldown(repo)
		scanWorktrees(cwds)
	}
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
	scanPastCooldown(repo, []string{repo, wt}, 1)
	if listCalls != before {
		t.Error("once the worktree is listed, refreshes should use the cache again")
	}
}

func TestScanWorktreesDetectsWorktreeNestedInLinkedWorktree(t *testing.T) {
	resetWorktreeScan(t)
	repo := "/repo"
	outer := repo + "/.claude/worktrees/outer"
	inner := outer + "/.claude/worktrees/inner"
	mainRootFn = func(string) string { return repo }
	gitHasInner := false
	worktreesFn = func(string) []claude.Worktree {
		list := []claude.Worktree{{Path: repo}, {Path: outer}}
		if gitHasInner {
			list = append(list, claude.Worktree{Path: inner})
		}
		return list
	}

	scanWorktrees([]string{outer})
	gitHasInner = true
	pastCooldown(repo)
	_, lists := scanWorktrees([]string{outer, inner})
	if !hasWorktree(lists[repo], inner) {
		t.Errorf("a worktree nested in a linked worktree should be picked up early, got %+v", lists[repo])
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
	scanPastCooldown(repo, []string{repo, sub}, 5)
	if listCalls != 1 {
		t.Errorf("a plain subdirectory must not trigger relists, got %d list calls", listCalls)
	}
}

// cav launched through a symlink keeps the logical path, while git reports the
// physical one. The dir's toplevel is listed, so it must not count as stale.
func TestScanWorktreesSymlinkedDirIsNotStale(t *testing.T) {
	resetWorktreeScan(t)
	link, physical := "/link/repo", "/real/repo"
	mainRootFn = func(string) string { return physical }
	topLevelFn = func(string) string { return physical }
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree { listCalls++; return []claude.Worktree{{Path: physical}} }

	scanWorktrees([]string{link})
	scanPastCooldown(physical, []string{link}, 10)
	if listCalls != 1 {
		t.Errorf("a symlinked dir must not trigger relists, got %d list calls", listCalls)
	}
}

// A stopped session can linger in a worktree removed after cav cached its root.
// It may cost one early relist, but must stop for good after the next full scan.
func TestScanWorktreesRemovedWorktreeStopsRelisting(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/repo/.claude/worktrees/gone"
	mainRootFn = func(string) string { return repo }
	created, removed := false, false
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree {
		listCalls++
		if created && !removed {
			return []claude.Worktree{{Path: repo}, {Path: wt}}
		}
		return []claude.Worktree{{Path: repo}}
	}
	topLevelFn = func(cwd string) string {
		if cwd == wt && removed {
			return "" // the dir is gone
		}
		return cwd
	}
	cwds := []string{repo, wt}

	scanWorktrees([]string{repo})
	created = true
	scanPastCooldown(repo, cwds, 1) // picked up early; its root is now cached
	if listCalls != 2 {
		t.Fatalf("setup: expected an early relist, got %d list calls", listCalls)
	}

	removed = true
	wtForceScan.Store(true)
	scanWorktrees(cwds)
	scanPastCooldown(repo, cwds, 10)
	if listCalls != 4 {
		t.Errorf("after removal: one full scan plus at most one early relist, got %d list calls", listCalls)
	}

	wtForceScan.Store(true)
	scanWorktrees(cwds)
	scanPastCooldown(repo, cwds, 10)
	if listCalls != 5 {
		t.Errorf("after the next full scan the removed worktree must stop costing relists, got %d", listCalls)
	}
}

// A dir git calls a worktree root but never lists must cost at most one early
// relist per full-scan window, not one every cooldown.
func TestScanWorktreesUnplaceableDirRelistsOncePerFullScan(t *testing.T) {
	resetWorktreeScan(t)
	repo, odd := "/repo", "/elsewhere/odd"
	mainRootFn = func(string) string { return repo }
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree { listCalls++; return []claude.Worktree{{Path: repo}} }

	scanWorktrees([]string{odd})
	scanPastCooldown(repo, []string{odd}, 10)
	if listCalls != 2 {
		t.Errorf("expected one full scan plus one early relist, got %d list calls", listCalls)
	}
	wtForceScan.Store(true)
	scanWorktrees([]string{odd})
	scanPastCooldown(repo, []string{odd}, 10)
	if listCalls != 4 {
		t.Errorf("each full-scan window allows at most one early relist, got %d list calls", listCalls)
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

// A repo whose sessions all vanish for one refresh (say, a half-written
// state.json) must come back with its list, not an empty one.
func TestScanWorktreesRepoMissingOneRefreshKeepsItsList(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/repo/.claude/worktrees/feat"
	mainRootFn = func(cwd string) string {
		if cwd == "/elsewhere" {
			return ""
		}
		return repo
	}
	listCalls := 0
	worktreesFn = func(string) []claude.Worktree { listCalls++; return []claude.Worktree{{Path: repo}, {Path: wt}} }

	scanWorktrees([]string{wt})
	scanWorktrees([]string{"/elsewhere"})
	_, lists := scanWorktrees([]string{wt}) // back within the cooldown
	if !hasWorktree(lists[repo], wt) {
		t.Errorf("a repo back within the cooldown must keep its list, got %+v", lists[repo])
	}
	if listCalls != 1 {
		t.Errorf("the list should come from the cache, got %d list calls", listCalls)
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
	scanPastCooldown(repo, []string{repo, wt}, 1) // stale check runs; the toplevel lookup fails
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

// The UI may edit the lists it is handed (W/X do). Those edits must never reach
// the scan's private cache.
func TestScanWorktreesHandsOutCopies(t *testing.T) {
	resetWorktreeScan(t)
	repo, wt := "/repo", "/repo/.claude/worktrees/feat"
	mainRootFn = func(string) string { return repo }
	worktreesFn = func(string) []claude.Worktree { return []claude.Worktree{{Path: repo}, {Path: wt}} }

	_, lists := scanWorktrees([]string{wt})
	lists[repo][1].Path = "/edited"
	delete(lists, repo)
	if !hasWorktree(wtCache[repo], wt) {
		t.Errorf("UI edits leaked into the scan cache: %+v", wtCache[repo])
	}
}

// Run with -race. Mirrors the app: the refresh goroutine scans and hands each
// result to the UI, which adopts it and runs the W/X handlers (editing its lists
// in place) while the next scan already reads the cache. Memory shared between a
// handed-out list and the cache is flagged as a race.
func TestWorktreeHandlersDoNotRaceWithScan(t *testing.T) {
	resetWorktreeScan(t)
	repo := "/repo"
	a, b := repo+"/.claude/worktrees/a", repo+"/.claude/worktrees/b"
	mainRootFn = func(string) string { return repo }
	worktreesFn = func(string) []claude.Worktree { return []claude.Worktree{{Path: repo}, {Path: a}, {Path: b}} }
	cwds := []string{repo, a}
	m := treeModel(t, repo, nil, repo, a)

	results := make(chan refreshResult)
	go func() {
		defer close(results)
		for i := 0; i < 300; i++ {
			pastCooldown(repo)
			repoOf, lists := scanWorktrees(cwds)
			results <- refreshResult{repoOf: repoOf, worktrees: lists}
		}
	}()
	for rr := range results {
		m.repoOf, m.worktrees = rr.repoOf, rr.worktrees
		m.Update(worktreeRemovedMsg{path: b, repo: repo})
		m.Update(worktreeAddedMsg{path: b, repo: repo, branch: "b"})
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
