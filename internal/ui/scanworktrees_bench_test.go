package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
)

// BenchmarkScanWorktreesSteadyState measures one refresh's worktree scan with a
// warm cache, past the relist cooldown, so the stale check runs for every repo
// (its worst case). Ten repos with six worktrees and six session dirs each,
// roughly the shape of a busy machine. Git is stubbed out.
func BenchmarkScanWorktreesSteadyState(b *testing.B) {
	origMain, origWt, origTop := mainRootFn, worktreesFn, topLevelFn
	b.Cleanup(func() { mainRootFn, worktreesFn, topLevelFn = origMain, origWt, origTop })
	cwdRepoCache, cwdTopCache = map[string]string{}, map[string]string{}
	wtCache, wtFetchedAt = map[string][]claude.Worktree{}, map[string]time.Time{}

	var cwds []string
	repoOfCwd := map[string]string{}
	lists := map[string][]claude.Worktree{}
	for r := 0; r < 10; r++ {
		repo := fmt.Sprintf("/src/repo%d", r)
		list := []claude.Worktree{{Path: repo}}
		for w := 0; w < 5; w++ {
			list = append(list, claude.Worktree{Path: fmt.Sprintf("%s/.claude/worktrees/w%d", repo, w)})
		}
		lists[repo] = list
		for _, c := range []string{repo, repo + "/pkg/api", list[1].Path, list[2].Path, list[3].Path, list[4].Path} {
			cwds = append(cwds, c)
			repoOfCwd[c] = repo
		}
	}
	mainRootFn = func(cwd string) string { return repoOfCwd[cwd] }
	worktreesFn = func(repo string) []claude.Worktree { return lists[repo] }
	topLevelFn = func(cwd string) string { return repoOfCwd[cwd] }

	wtForceScan.Store(false)
	wtScanAt = time.Time{}
	scanWorktrees(cwds) // warm every cache with one full scan
	wtScanAt = time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		past := time.Now().Add(-wtRetryAfter - time.Second)
		for r := range lists {
			wtFetchedAt[r] = past
		}
		scanWorktrees(cwds)
	}
}
