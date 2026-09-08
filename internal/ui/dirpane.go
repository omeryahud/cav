package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/omeryahud/cav/internal/claude"
)

// dirEntry is one row of the directory pane. path "" is the "all" row.
type dirEntry struct {
	path  string
	label string
	count int
}

// dirEntries lists the directories with sessions in the current window: the
// "all" row first, then one row per cwd in alphabetical order. Leaf names that
// collide are shown as parent/leaf so the rows stay distinguishable.
func (m *Model) dirEntries() []dirEntry {
	counts := map[string]int{}
	total := 0
	for _, s := range m.all {
		if !m.passesFilter(s) {
			continue
		}
		total++
		counts[s.CWD]++
	}
	leafSeen := map[string]int{}
	for p := range counts {
		leafSeen[dirBase(p)]++
	}
	entries := []dirEntry{{path: "", label: "all", count: total}}
	for p, n := range counts {
		label := dirBase(p)
		if label == "" {
			label = p
		}
		if leafSeen[dirBase(p)] > 1 {
			label = filepath.Base(filepath.Dir(p)) + "/" + label
		}
		entries = append(entries, dirEntry{path: p, label: label, count: n})
	}
	rest := entries[1:]
	sort.Slice(rest, func(i, j int) bool {
		a, b := strings.ToLower(rest[i].label), strings.ToLower(rest[j].label)
		if a != b {
			return a < b
		}
		return rest[i].path < rest[j].path
	})
	return entries
}

// dirPaneWidth is the directory pane's column width, or 0 when the pane is
// off (dirPane.widthPercent 0) or the terminal is too narrow to split.
func (m *Model) dirPaneWidth() int {
	pct := m.cfg.DirPane.WidthPercent
	if pct <= 0 || m.width < 60 {
		return 0
	}
	w := m.width * pct / 100
	if w < 14 {
		w = 14
	}
	return w
}

// selectedDirIndex is the row of the selected directory, or 0 (all) when the
// selection no longer exists.
func (m *Model) selectedDirIndex(entries []dirEntry) int {
	for i, e := range entries {
		if e.path == m.dirSel {
			return i
		}
	}
	return 0
}

// cycleDir moves the directory selection by d rows (wrapping) and re-scopes
// the session list to it.
func (m *Model) cycleDir(d int) {
	entries := m.dirEntries()
	i := m.selectedDirIndex(entries)
	i = ((i+d)%len(entries) + len(entries)) % len(entries)
	m.dirSel = entries[i].path
	m.cursor = 0
	m.recompute()
}

// dirPaneLines renders the directory pane: a header, then one row per
// directory with its session count, the selected row highlighted.
func (m *Model) dirPaneLines(h, width int) []string {
	entries := m.dirEntries()
	sel := m.selectedDirIndex(entries)
	lines := []string{cwdHeader.Render(padRight(" DIRECTORIES", width))}
	const countW = 3
	labelW := width - 3 - countW - 1
	if labelW < 4 {
		labelW = 4
	}
	for i, e := range entries {
		body := fmt.Sprintf("%-*s %*d", labelW, truncate(e.label, labelW), countW, e.count)
		if i == sel {
			lines = append(lines, cursorStyle.Background(selBg).Render(" ▸ ")+
				selName.Background(selBg).Render(padRight(body, width-3)))
			continue
		}
		lines = append(lines, "   "+nameStyle.Render(body))
	}
	top := windowTop(sel+1, len(lines), h)
	end := top + h
	if end > len(lines) {
		end = len(lines)
	}
	return fit(lines[top:end], h)
}

// dirTitleCount is the header count: "N" normally, "N of M" when a directory
// is selected (M = every session in the current window).
func (m *Model) dirTitleCount() string {
	if m.dirSel == "" {
		return fmt.Sprintf("%d", len(m.view))
	}
	return fmt.Sprintf("%d of %d", len(m.view), m.dirEntries()[0].count)
}

// hasSessionIn reports whether any session that passes the current window and
// filter lives in cwd.
func (m *Model) hasSessionIn(cwd string) bool {
	for _, s := range m.all {
		if s.CWD == cwd && m.passesFilter(s) {
			return true
		}
	}
	return false
}

// passesFilter reports whether s belongs to the current window and survives
// the active deep search and / filter. The directory pane and the session
// list share it, so the two views always agree on what is visible.
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

// nameStepPlaceholder names the target directory in the required-name step.
func nameStepPlaceholder(dir string) string {
	return "session name (required) · in " + homeShorten(dir) + "…"
}
