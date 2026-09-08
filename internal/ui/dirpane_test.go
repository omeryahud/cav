package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
)

// treeModel builds a model with sessions at the given cwds, all mapped to one
// repo, plus the given worktrees for that repo.
func treeModel(t *testing.T, repo string, wts []claude.Worktree, cwds ...string) *Model {
	t.Helper()
	m := openModel(t)
	m.input = textinput.New()
	m.repoOf = map[string]string{}
	m.worktrees = map[string][]claude.Worktree{repo: wts}
	for i, c := range cwds {
		m.all = append(m.all, claude.Session{
			SessionID: string(rune('a'+i)) + "-sid",
			Name:      string(rune('a' + i)),
			Kind:      "background",
			Status:    "idle",
			CWD:       c,
		})
		m.repoOf[c] = repo
	}
	m.recompute()
	return m
}

func labelsOf(nodes []dirNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.label
	}
	return out
}

func nodeAt(nodes []dirNode, path string) (dirNode, bool) {
	for _, n := range nodes {
		if n.path == path {
			return n, true
		}
	}
	return dirNode{}, false
}

func TestTreeRepoWithWorktrees(t *testing.T) {
	repo := "/r/cav"
	dp := "/r/cav/.claude/worktrees/dp"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "master"}, {Path: dp, Branch: "dp"}},
		repo, repo, dp)
	nodes := m.buildDirTree()

	// The repo's top node is the main checkout (unique path), labeled with the
	// repo name; the linked worktree nests under it.
	top, ok := nodeAt(nodes, repo)
	if !ok || top.kind != nodeWorktree || top.linked || top.label != "cav" || top.count != 3 {
		t.Fatalf("repo top node = %+v (want main worktree 'cav' count 3); tree=%v", top, labelsOf(nodes))
	}
	dpNode, ok := nodeAt(nodes, dp)
	if !ok || !dpNode.linked || dpNode.branch != "dp" || dpNode.count != 1 || dpNode.depth != top.depth+1 {
		t.Errorf("worktree node = %+v, want linked dp branch count 1 nested under the repo", dpNode)
	}
	// Every node path is unique, so selection can't get stuck.
	seen := map[string]bool{}
	for _, n := range nodes {
		if seen[n.path] {
			t.Errorf("duplicate node path %q — selection would get stuck", n.path)
		}
		seen[n.path] = true
	}
}

func TestTabReachesLinkedWorktree(t *testing.T) {
	repo := "/r/cav"
	dp := "/r/cav/.claude/worktrees/dp"
	dd := "/r/cav/.claude/worktrees/dd"
	m := treeModel(t, repo,
		[]claude.Worktree{{Path: repo, Branch: "master"}, {Path: dp, Branch: "dp"}, {Path: dd, Branch: "dd"}},
		repo, dp)
	// Tab from "all" through every visible row and confirm each linked worktree
	// is reached (the stuck-on-repo bug never advanced past the main checkout).
	visited := map[string]bool{}
	for i := 0; i < 8; i++ {
		m.cycleDir(1)
		visited[m.dirSel] = true
	}
	for _, want := range []string{repo, dp, dd} {
		if !visited[want] {
			t.Errorf("tab never selected %q; visited=%v", want, visited)
		}
	}
	// dd has no session, but as a worktree it must stay selected (not fall back).
	m.dirSel = dd
	m.recompute()
	if m.dirSel != dd {
		t.Errorf("an empty worktree should stay selectable, dirSel=%q", m.dirSel)
	}
}

func TestTreeSingleWorktreeCollapses(t *testing.T) {
	repo := "/r/ai-courses"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "main"}}, repo, repo)
	nodes := m.buildDirTree()
	// all + the single repo row, nothing else.
	if len(nodes) != 2 {
		t.Fatalf("single-worktree repo should be one row, got %v", labelsOf(nodes))
	}
	if nodes[1].label != "ai-courses" || nodes[1].count != 2 {
		t.Errorf("row = %+v, want ai-courses count 2", nodes[1])
	}
}

func TestTreeSubdirNesting(t *testing.T) {
	repo := "/s/substrate"
	sched := "/s/substrate/scheduling"
	research := "/s/substrate/research"
	agents := "/s/substrate/research/agents"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "main"}},
		sched, sched, research, agents)
	nodes := m.buildDirTree()

	rn, _ := nodeAt(nodes, research)
	an, _ := nodeAt(nodes, agents)
	sn, _ := nodeAt(nodes, sched)
	if rn.count != 2 || rn.depth != an.depth-1 {
		t.Errorf("research=%+v agents=%+v: research should be agents' parent with count 2", rn, an)
	}
	if sn.count != 2 || sn.depth != rn.depth {
		t.Errorf("scheduling=%+v should sit beside research", sn)
	}
	repoNode, _ := nodeAt(nodes, repo)
	if repoNode.count != 4 {
		t.Errorf("substrate subtree count = %d, want 4", repoNode.count)
	}
}

func TestTreeScopingIsSubtreePrefix(t *testing.T) {
	repo := "/s/substrate"
	research := "/s/substrate/research"
	agents := "/s/substrate/research/agents"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "main"}}, research, agents)
	m.dirSel = research
	m.recompute()
	if len(m.view) != 2 {
		t.Errorf("selecting research should scope to its subtree (2), got %d", len(m.view))
	}
	m.dirSel = agents
	m.recompute()
	if len(m.view) != 1 {
		t.Errorf("selecting agents should scope to 1, got %d", len(m.view))
	}
}

func TestFoldHidesChildren(t *testing.T) {
	repo := "/s/substrate"
	research := "/s/substrate/research"
	agents := "/s/substrate/research/agents"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "main"}}, research, agents)
	if _, ok := nodeAt(m.visibleNodes(), agents); !ok {
		t.Fatal("agents should be visible when expanded")
	}
	m.dirSel = research
	m.toggleFold()
	if _, ok := nodeAt(m.visibleNodes(), agents); ok {
		t.Error("agents should be hidden when research is folded")
	}
}

func TestWorktreeBaseFromWorktreeNode(t *testing.T) {
	repo := "/r/cav"
	dp := "/r/cav/.claude/worktrees/dp"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "master"}, {Path: dp, Branch: "dp"}}, dp)
	n, _ := nodeAt(m.buildDirTree(), dp)
	gotRepo, base, ok := m.worktreeBase(n)
	if !ok || gotRepo != repo || base != "dp" {
		t.Errorf("worktreeBase(dp) = %q %q %v, want %q dp true", gotRepo, base, ok, repo)
	}
}

func TestWKeyStartsWorktreeWizard(t *testing.T) {
	repo := "/r/cav"
	dp := "/r/cav/.claude/worktrees/dp"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "master"}, {Path: dp, Branch: "dp"}}, dp)
	m.dirSel = dp
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("W")})
	if m.newKind != kindWorktree || m.wtRepo != repo || m.wtBase != "dp" || m.mode != modeNewName {
		t.Errorf("W → kind=%v repo=%q base=%q mode=%v", m.newKind, m.wtRepo, m.wtBase, m.mode)
	}
}

func TestXKeyGuards(t *testing.T) {
	repo := "/r/cav"
	dp := "/r/cav/.claude/worktrees/dp"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "master"}, {Path: dp, Branch: "dp"}}, repo, dp)

	// On the main checkout: refused.
	m.dirSel = repo
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if m.pendingWT.path != "" || m.mode == modeConfirm {
		t.Error("X on the main checkout should be refused")
	}
	// On a linked worktree that still has a session: refused.
	m.dirSel = dp
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if m.pendingWT.path != "" {
		t.Error("X on a worktree with sessions should be refused")
	}
	// Empty the worktree, then X arms the confirm.
	m.all = m.all[:1] // drop the dp session
	m.recompute()
	m.dirSel = dp
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if m.pendingWT.path != dp || m.mode != modeConfirm {
		t.Errorf("X on an empty linked worktree should arm the confirm, pendingWT=%q mode=%v", m.pendingWT.path, m.mode)
	}
}

// A worktree with only a STOPPED session under it (none active) is still
// deletable: stopped sessions don't block, they only warn.
func TestXAllowsWorktreeWithOnlyStoppedSessions(t *testing.T) {
	repo := "/r/cav"
	dp := "/r/cav/.claude/worktrees/dp"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "master"}, {Path: dp, Branch: "dp"}}, repo, dp)
	// Send dp's session to the stopped window (dismiss it: no live worker).
	m.all[1].Status = ""
	if err := m.dismissed.Add(m.all[1].SessionID); err != nil {
		t.Fatal(err)
	}
	m.recompute()
	if active, stopped := m.sessionsUnderByState(dp); active != 0 || stopped != 1 {
		t.Fatalf("dp should have 0 active / 1 stopped, got %d/%d", active, stopped)
	}
	m.dirSel = dp
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	if m.pendingWT.path != dp || m.mode != modeConfirm {
		t.Errorf("X should arm the confirm despite a stopped session, pendingWT=%q mode=%v", m.pendingWT.path, m.mode)
	}
}

func TestDotKeyTargetsSelectedThenLaunchDir(t *testing.T) {
	dot := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")}
	m := treeModel(t, "/r/x", []claude.Worktree{{Path: "/r/x", Branch: "main"}}, "/r/x")
	m.launchDir = "/launch/here"
	m.dirSel = "/r/x"
	m.handleListKey(dot)
	if m.newCWD != "/r/x" || m.mode != modeNewName {
		t.Errorf("selected: cwd=%q mode=%v", m.newCWD, m.mode)
	}
	m.dirSel = ""
	m.mode = modeList
	m.handleListKey(dot)
	if m.newCWD != "/launch/here" {
		t.Errorf("on all, . should fall back to the launch dir, got %q", m.newCWD)
	}
}

func TestRemoveDirectoryMovesSubtreeToStopped(t *testing.T) {
	repo := "/s/substrate"
	research := "/s/substrate/research"
	agents := "/s/substrate/research/agents"
	m := treeModel(t, repo, []claude.Worktree{{Path: repo, Branch: "main"}}, research, agents)
	m.all[0].Status, m.all[1].Status = "", "" // both dead → dismiss
	m.recompute()
	m.dirSel = research
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if m.pendingDir != research {
		t.Fatalf("D should arm the confirm for the subtree, got %q", m.pendingDir)
	}
	m.handleConfirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.dismissed.Has(m.all[0].SessionID) || !m.dismissed.Has(m.all[1].SessionID) {
		t.Error("D should move the whole subtree (research + agents) to the stopped window")
	}
}

func TestDirPaneWidth(t *testing.T) {
	m := treeModel(t, "/r/x", nil, "/r/x")
	m.width = 160
	if got := m.dirPaneWidth(); got != 40 {
		t.Errorf("25%% of 160 = %d, want 40", got)
	}
	m.cfg.DirPane.WidthPercent = 0
	if got := m.dirPaneWidth(); got != 0 {
		t.Errorf("0 should hide the pane, got %d", got)
	}
	m.cfg.DirPane.WidthPercent = 25
	m.width = 50
	if got := m.dirPaneWidth(); got != 0 {
		t.Errorf("narrow terminal should hide the pane, got %d", got)
	}
}

func TestLaunchDirFocusedOnFirstRefresh(t *testing.T) {
	m := treeModel(t, "/r/x", []claude.Worktree{{Path: "/r/x", Branch: "main"}}, "/r/x")
	m.launchDir, m.focusLaunchDir = "/r/x", true
	m.feedRefresh()
	if m.dirSel != "/r/x" {
		t.Errorf("launch dir with sessions should be focused, got %q", m.dirSel)
	}
	if m.focusLaunchDir {
		t.Error("focus should be one-shot")
	}
}
