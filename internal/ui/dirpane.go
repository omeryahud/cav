package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/omeryahud/cav/internal/claude"
)

type nodeKind int

const (
	nodeAll      nodeKind = iota
	nodeRepo              // repo grouping node (>1 worktree)
	nodeWorktree          // a git worktree (main or linked)
	nodeSubdir            // a session subdirectory within a worktree
	nodePlain             // a non-git session directory (flat)
)

// dirNode is one row of the directory tree.
type dirNode struct {
	path   string // cwd this node represents ("" for the all row)
	label  string
	depth  int
	count  int // sessions in this node's subtree (current window + filter)
	kind   nodeKind
	repo   string // repo root this node belongs to (for W base ref)
	branch string // worktree node: its branch (for W base ref)
	linked bool   // worktree node that is not the main checkout
	parent bool   // has children (foldable)
}

// under reports whether cwd is at or below base (path-component-wise).
func under(cwd, base string) bool {
	return cwd == base || strings.HasPrefix(cwd, strings.TrimRight(base, "/")+"/")
}

// passesFilter reports whether s belongs to the current window and survives the
// active deep search and / filter. The tree and the session list share it.
func (m *Model) passesFilter(s claude.Session) bool {
	if m.isStopped(s) != m.stoppedView || m.hiddenPendingClone(s) {
		return false
	}
	if m.matchIDs != nil && !m.matchIDs[s.SessionID] {
		return false
	}
	q := strings.ToLower(strings.TrimSpace(m.filter))
	return q == "" || m.sessionMatches(s, q)
}

// buildDirTree builds the whole directory tree: an "all" row, then per-repo
// groupings with their worktrees and session subdirectories nested, then any
// non-git session directories as flat rows.
func (m *Model) buildDirTree() []dirNode {
	cnt := map[string]int{}
	total := 0
	for _, s := range m.all {
		if m.passesFilter(s) {
			total++
			cnt[s.CWD]++
		}
	}
	subtree := func(path string) int { // sessions in a subtree
		n := 0
		for cwd, c := range cnt {
			if under(cwd, path) {
				n += c
			}
		}
		return n
	}

	nodes := []dirNode{{label: "all", kind: nodeAll, count: total, parent: total > 0}}

	byRepo := map[string]map[string]bool{} // repo -> set of session cwds
	var nonGit []string
	nonGitSeen := map[string]bool{}
	for cwd := range cnt {
		if r := m.repoOf[cwd]; r != "" {
			if byRepo[r] == nil {
				byRepo[r] = map[string]bool{}
			}
			byRepo[r][cwd] = true
		} else if !nonGitSeen[cwd] {
			nonGit = append(nonGit, cwd)
			nonGitSeen[cwd] = true
		}
	}

	repos := make([]string, 0, len(byRepo))
	for r := range byRepo {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return leafLower(repos[i]) < leafLower(repos[j]) })

	for _, repo := range repos {
		nodes = append(nodes, m.repoNodes(repo, byRepo[repo], cnt, subtree)...)
	}

	sort.Slice(nonGit, func(i, j int) bool { return leafLower(nonGit[i]) < leafLower(nonGit[j]) })
	for _, cwd := range nonGit {
		nodes = append(nodes, dirNode{path: cwd, label: dirBase(cwd), kind: nodePlain, count: cnt[cwd]})
	}
	return nodes
}

// repoNodes builds the nodes for one repo: its worktrees and the session
// subdirectories nested under them. A repo with a single worktree collapses to
// that worktree (no separate grouping row); with several it gets a grouping row.
func (m *Model) repoNodes(repo string, cwds map[string]bool, cnt map[string]int, subtree func(string) int) []dirNode {
	wts := m.worktrees[repo]
	if len(wts) == 0 {
		wts = []claude.Worktree{{Path: repo}}
	}
	// Significant node paths: every worktree, plus every session cwd.
	sigKind := map[string]nodeKind{}
	sigBranch := map[string]string{}
	for _, wt := range wts {
		sigKind[wt.Path] = nodeWorktree
		sigBranch[wt.Path] = wt.Branch
	}
	for cwd := range cwds {
		if _, ok := sigKind[cwd]; !ok {
			sigKind[cwd] = nodeSubdir
		}
	}
	paths := make([]string, 0, len(sigKind))
	for p := range sigKind {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// parentOf: the longest other significant path that is a strict prefix.
	parentOf := map[string]string{}
	kids := map[string]bool{}
	for _, p := range paths {
		best := ""
		for _, q := range paths {
			if q != p && under(p, q) && len(q) > len(best) {
				best = q
			}
		}
		parentOf[p] = best
		if best != "" {
			kids[best] = true
		}
	}

	single := len(wts) == 1
	var out []dirNode
	if !single {
		out = append(out, dirNode{path: repo, label: dirBase(repo), kind: nodeRepo,
			count: subtree(repo), repo: repo, parent: true})
	}
	baseDepth := 0
	if !single {
		baseDepth = 1
	}

	var emit func(path string, depth int)
	emit = func(path string, depth int) {
		label := dirBase(path)
		kind := sigKind[path]
		linked := false
		if kind == nodeWorktree {
			if b := sigBranch[path]; b != "" {
				label = b
			}
			linked = path != repo
		}
		if single && path == repo {
			label = dirBase(repo) // the collapsed repo row keeps the repo's name
		}
		out = append(out, dirNode{path: path, label: label, depth: depth, count: subtree(path),
			kind: kind, repo: repo, branch: sigBranch[path], linked: linked, parent: kids[path]})
		var childs []string
		for _, q := range paths {
			if parentOf[q] == path {
				childs = append(childs, q)
			}
		}
		sort.Slice(childs, func(i, j int) bool { return leafLower(childs[i]) < leafLower(childs[j]) })
		for _, q := range childs {
			emit(q, depth+1)
		}
	}
	// Roots: significant paths with no significant parent (the worktrees).
	var roots []string
	for _, p := range paths {
		if parentOf[p] == "" {
			roots = append(roots, p)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i] == repo != (roots[j] == repo) {
			return roots[i] == repo // the main checkout first
		}
		return leafLower(roots[i]) < leafLower(roots[j])
	})
	for _, r := range roots {
		emit(r, baseDepth)
	}
	return out
}

func leafLower(p string) string { return strings.ToLower(dirBase(p)) }

// visibleNodes is the tree with collapsed subtrees hidden.
func (m *Model) visibleNodes() []dirNode {
	all := m.buildDirTree()
	var out []dirNode
	hideBelow := -1
	for _, n := range all {
		if hideBelow >= 0 {
			if n.depth > hideBelow {
				continue
			}
			hideBelow = -1
		}
		out = append(out, n)
		if n.parent && n.path != "" && m.dirCollapsed[n.path] {
			hideBelow = n.depth
		}
	}
	return out
}

// dirPaneWidth is the tree pane's width, or 0 when it is off or the terminal is
// too narrow to split.
func (m *Model) dirPaneWidth() int {
	if m.cfg.DirPane.WidthPercent <= 0 || m.width < 60 {
		return 0
	}
	if w := m.width * m.cfg.DirPane.WidthPercent / 100; w >= 14 {
		return w
	}
	return 14
}

func (m *Model) selectedNodeIndex(nodes []dirNode) int {
	for i, n := range nodes {
		if n.path == m.dirSel {
			return i
		}
	}
	return 0
}

// selectedNode returns the currently selected tree node.
func (m *Model) selectedNode() dirNode {
	nodes := m.visibleNodes()
	return nodes[m.selectedNodeIndex(nodes)]
}

// cycleDir moves the selection by d visible rows (wrapping) and re-scopes the
// session list.
func (m *Model) cycleDir(d int) {
	nodes := m.visibleNodes()
	i := m.selectedNodeIndex(nodes)
	i = ((i+d)%len(nodes) + len(nodes)) % len(nodes)
	m.dirSel = nodes[i].path
	m.cursor = 0
	m.recompute()
}

// toggleFold collapses or expands the selected node.
func (m *Model) toggleFold() {
	n := m.selectedNode()
	if !n.parent || n.path == "" {
		return
	}
	if m.dirCollapsed == nil {
		m.dirCollapsed = map[string]bool{}
	}
	m.dirCollapsed[n.path] = !m.dirCollapsed[n.path]
}

// sessionsUnder returns the sessions in the current window under cwd (subtree).
func (m *Model) sessionsUnder(cwd string) []claude.Session {
	var out []claude.Session
	for _, s := range m.all {
		if m.isStopped(s) == m.stoppedView && !m.hiddenPendingClone(s) && under(s.CWD, cwd) {
			out = append(out, s)
		}
	}
	return out
}

// anySessionUnder reports whether any session at all (active or stopped) has a
// cwd under path — used to refuse deleting a worktree that sessions depend on.
func (m *Model) anySessionUnder(path string) bool {
	for _, s := range m.all {
		if under(s.CWD, path) {
			return true
		}
	}
	return false
}

// isWorktreePath reports whether p is a known git worktree (so an empty one
// stays selectable in the tree).
func (m *Model) isWorktreePath(p string) bool {
	for _, wts := range m.worktrees {
		for _, wt := range wts {
			if wt.Path == p {
				return true
			}
		}
	}
	return false
}

// hasSessionIn reports whether any session that passes the filter is under cwd.
func (m *Model) hasSessionIn(cwd string) bool {
	for _, s := range m.all {
		if under(s.CWD, cwd) && m.passesFilter(s) {
			return true
		}
	}
	return false
}

// dirTitleCount is "N" normally, "N of M" when a subtree is selected.
func (m *Model) dirTitleCount() string {
	if m.dirSel == "" {
		return fmt.Sprintf("%d", len(m.view))
	}
	total := 0
	for _, s := range m.all {
		if m.passesFilter(s) {
			total++
		}
	}
	return fmt.Sprintf("%d of %d", len(m.view), total)
}

// nameStepPlaceholder names the target directory in the required-name step.
func nameStepPlaceholder(dir string) string {
	return "session name (required) · in " + homeShorten(dir) + "…"
}

// dirPaneLines renders the tree: a header then one row per visible node,
// indented, the selected row highlighted.
func (m *Model) dirPaneLines(h, width int) []string {
	nodes := m.visibleNodes()
	sel := m.selectedNodeIndex(nodes)
	lines := []string{cwdHeader.Render(padRight(" DIRECTORIES", width))}
	const countW = 3
	for i, n := range nodes {
		indent := strings.Repeat("  ", n.depth)
		marker := " "
		if n.parent && n.path != "" {
			if m.dirCollapsed[n.path] {
				marker = "▸"
			} else {
				marker = "▾"
			}
		}
		name := indent + marker + " "
		if n.linked {
			name += "⑂ "
		}
		name += n.label
		labelW := width - 3 - countW - 1
		if labelW < 4 {
			labelW = 4
		}
		count := fmt.Sprintf("%*d", countW, n.count)
		body := fmt.Sprintf("%-*s %s", labelW, truncate(name, labelW), count)
		if i == sel {
			lines = append(lines, cursorStyle.Background(selBg).Render(" ▸ ")+
				selName.Background(selBg).Render(padRight(body, width-3)))
			continue
		}
		style := nameStyle
		if n.count == 0 {
			style = dimStyle
		}
		lines = append(lines, "   "+style.Render(body))
	}
	top := windowTop(sel+1, len(lines), h)
	end := top + h
	if end > len(lines) {
		end = len(lines)
	}
	return fit(lines[top:end], h)
}

// worktreeBase returns the repo root and base branch for creating a worktree
// from the selected node: a worktree node bases on its own branch, anything
// else bases on the repo's default branch. ok is false when the node is not in
// a git repo.
func (m *Model) worktreeBase(n dirNode) (repo, base string, ok bool) {
	repo = n.repo
	if repo == "" {
		return "", "", false
	}
	if n.kind == nodeWorktree && n.branch != "" {
		return repo, n.branch, true
	}
	return repo, claude.DefaultBranch(repo), true
}
