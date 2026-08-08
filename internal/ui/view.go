package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/config"
	"github.com/omeryahud/cav/internal/preview"
)

// The palette is applied once at startup from config (applyPalette), so every
// render site can keep using these styles by name. Text attributes (bold,
// italic, faint) stay fixed here — only the colors are configurable.
var (
	titleStyle  lipgloss.Style
	cursorStyle lipgloss.Style
	dimStyle    lipgloss.Style
	nameStyle   lipgloss.Style
	selName     lipgloss.Style
	selBg       lipgloss.Color // background for the highlighted (selected) row
	workDot     lipgloss.Style
	idleDot     lipgloss.Style
	warnDot     lipgloss.Style
	errStyle    lipgloss.Style
	helpStyle   lipgloss.Style
	hintStyle   lipgloss.Style
	cwdHeader   lipgloss.Style
	cwdPath     lipgloss.Style // "smaller" = faint
	statHeader  lipgloss.Style

	// status dot + sub-header colors (kept in sync by sharing a palette role)
	doneDot lipgloss.Style // complete ✓
	runHdr  lipgloss.Style // running
	waitHdr lipgloss.Style // waiting for input
	errHdr  lipgloss.Style // error
	idleHdr lipgloss.Style // idle
	doneHdr lipgloss.Style // complete

	// preview role labels (user vs assistant); message bodies are markdown-rendered
	userLabel lipgloss.Style
	asstLabel lipgloss.Style
)

// Styles exist before New runs (tests, and any render before the model loads).
func init() { applyPalette(config.Defaults().Colors) }

// applyPalette rebuilds every style from the configured colors. A role drives
// both a status dot and its bucket header, so the two can't drift apart.
func applyPalette(c config.Colors) {
	fg := func(s string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(s)) }
	titleStyle = lipgloss.NewStyle().Bold(true).
		Foreground(lipgloss.Color(c.TitleFg)).Background(lipgloss.Color(c.TitleBg)).Padding(0, 1)
	cursorStyle = fg(c.Accent).Bold(true)
	dimStyle = fg(c.Dim)
	nameStyle = fg(c.Name)
	selName = fg(c.Accent).Bold(true)
	selBg = lipgloss.Color(c.SelectionBg)
	workDot = fg(c.Running)
	idleDot = fg(c.Idle)
	warnDot = fg(c.Waiting)
	errStyle = fg(c.Error)
	helpStyle = fg(c.Help)
	hintStyle = fg(c.Accent)
	cwdHeader = fg(c.DirHeader).Bold(true)
	cwdPath = fg(c.DirPath).Faint(true)
	statHeader = fg(c.StatusHeader).Italic(true)

	doneDot = fg(c.Complete)
	runHdr = fg(c.Running).Italic(true)
	waitHdr = fg(c.Waiting).Italic(true)
	errHdr = fg(c.Error).Italic(true)
	idleHdr = fg(c.IdleHeader).Italic(true)
	doneHdr = fg(c.Complete).Italic(true)

	userLabel = fg(c.UserLabel).Bold(true)
	asstLabel = fg(c.AssistantLabel).Bold(true)
}

// renderSnippets renders the recent conversation: a color-coded role label per
// message followed by its body rendered from markdown to ANSI (via glamour),
// wrapped to width. Done once at load time (async), not per frame.
func renderSnippets(snips []preview.Snippet, width int, style string) string {
	if len(snips) == 0 {
		return ""
	}
	if width < 10 {
		width = 10
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(width),
	)
	var b strings.Builder
	for i, sn := range snips {
		label := userLabel
		if sn.Role != "user" {
			label = asstLabel
		}
		b.WriteString(label.Render(sn.Role) + "\n")
		body := sn.Text
		if err == nil {
			if out, e := r.Render(sn.Text); e == nil {
				body = strings.Trim(out, "\n")
			}
		}
		b.WriteString(body)
		if i < len(snips)-1 {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

func bucketStyle(rank int) lipgloss.Style {
	switch rank {
	case 0:
		return runHdr // running
	case 1:
		return waitHdr // waiting for input
	case 2:
		return errHdr // error
	case 3:
		return idleHdr // idle
	case 4:
		return doneHdr // complete
	case 5:
		return statHeader // stopped
	default:
		return statHeader // other
	}
}

// View lays the screen out as: header (2 lines) · middle (list, optionally
// split with a preview pane) · footer (status + persistent help bar). The
// middle is sized to fill the remaining height so the footer sits at the bottom.
func (m *Model) View() string {
	header := m.headerLines()
	footer := strings.Split(m.footerBlock(), "\n")

	if m.height <= 0 || m.width <= 0 { // pre-size fallback (before first WindowSizeMsg)
		lines := append([]string{}, header...)
		for i, s := range m.view {
			lines = append(lines, m.rowLine(s, i == m.cursor, m.roster[s.SessionID] != "", 80))
		}
		return strings.Join(append(lines, footer...), "\n")
	}

	midH := m.height - len(header) - len(footer)
	if midH < 1 {
		midH = 1
	}

	var mid []string
	switch {
	case m.mode == modePickDir:
		mid = m.pickerLines(midH, m.width)
	case m.showPreview():
		pw := m.previewWidth()
		lw := m.width - pw - 3
		mid = joinColumns(m.listLines(midH, lw), lw, m.previewLines(midH), pw, midH)
	default:
		mid = m.listLines(midH, m.width)
	}

	lines := append([]string{}, header...)
	lines = append(lines, mid...)
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

func (m *Model) headerLines() []string {
	label := "Claude Sessions"
	if m.stoppedView {
		label = "Stopped Sessions"
	}
	title := titleStyle.Render(fmt.Sprintf(" %s (%d) ", label, len(m.view)))
	if ind := m.indicators(); ind != "" {
		title += " " + hintStyle.Render(ind)
	}
	second := ""
	switch m.mode {
	case modeFilter:
		second = hintStyle.Render("/ ") + m.input.View()
	case modeSearch:
		second = hintStyle.Render("search: ") + m.input.View()
	case modeRename:
		second = hintStyle.Render("rename: ") + m.input.View()
	case modeLabel:
		second = hintStyle.Render("labels: ") + m.input.View()
	case modeNew:
		second = hintStyle.Render("new in "+homeShorten(m.newCWD)+": ") + m.input.View()
	case modeNewProject:
		second = hintStyle.Render("new project: ") + m.input.View()
	case modeNewName:
		second = hintStyle.Render("session name: ") + m.input.View()
	}
	return []string{title, second}
}

func (m *Model) indicators() string {
	var parts []string
	if m.filter != "" {
		parts = append(parts, "filter:"+m.filter)
	}
	if m.matchIDs != nil {
		parts = append(parts, "search")
	}
	switch m.groupMode {
	case groupNone:
		parts = append(parts, "alphabetical")
	case groupStatusDir:
		parts = append(parts, "group:status→dir")
	case groupRecent:
		parts = append(parts, "recently entered")
	}
	if m.stoppedView {
		parts = append(parts, "s: back to active")
	} else if n := m.countStopped(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped (s)", n))
	}
	if m.previewOn && m.width < m.cfg.Preview.MinWidth {
		parts = append(parts, "preview:too-narrow")
	}
	return strings.Join(parts, "  ")
}

func (m *Model) listLines(h, width int) []string {
	if len(m.view) == 0 {
		empty := "  no sessions — press n to create one"
		if m.stoppedView {
			empty = "  no stopped sessions — press s to go back"
		}
		return fit([]string{dimStyle.Render(empty)}, h)
	}
	var vlines []string
	var sel int
	if m.groupMode.grouped() {
		vlines, sel = m.groupedVisual(width)
	} else {
		vlines, sel = m.flatVisual(width)
	}
	top := windowTop(sel, len(vlines), h)
	end := top + h
	if end > len(vlines) {
		end = len(vlines)
	}
	return fit(vlines[top:end], h)
}

// flatVisual renders the ungrouped (alphabetical) view: one row per session, no
// headers, with fork children still indented and any ghost-parent context row
// drawn just above its branch. Returns the lines and the selected row's index.
func (m *Model) flatVisual(width int) ([]string, int) {
	var lines []string
	sel := 0
	for i := range m.view {
		s := m.view[i]
		if g, ok := m.ghostParent[s.SessionID]; ok {
			lines = append(lines, m.ghostRowLine(g, width))
		}
		if i == m.cursor {
			sel = len(lines)
		}
		lines = append(lines, m.rowLine(s, i == m.cursor, m.roster[s.SessionID] != "", width))
	}
	return lines, sel
}

// groupedVisual renders the view with two header levels, ordered by groupMode:
// dir→status shows a cwd header then a status sub-header; status→dir shows a
// status header then a cwd sub-header. Returns the lines and the visual index of
// the selected row (so the caller can scroll it into view).
func (m *Model) groupedVisual(width int) ([]string, int) {
	var lines []string
	sel := 0
	byDir := m.groupMode == groupDirStatus
	lastCWD, lastRank := "\x00", -1
	// emitHeaders appends the dir/status headers for s's group, in the order set by
	// groupMode, tracking the last-emitted cwd/rank so they aren't repeated.
	emitHeaders := func(s claude.Session) {
		rank := statusRank(m.statusOf(s))
		if byDir {
			if s.CWD != lastCWD {
				if len(lines) > 0 {
					lines = append(lines, "")
				}
				lines = append(lines, cwdHeaderLines(s.CWD, width, 0)...)
				lastCWD, lastRank = s.CWD, -1
			}
			if rank != lastRank {
				lines = append(lines, statusHeaderLine(rank, width, 2))
				lastRank = rank
			}
		} else {
			if rank != lastRank {
				if len(lines) > 0 {
					lines = append(lines, "")
				}
				lines = append(lines, statusHeaderLine(rank, width, 0))
				lastRank, lastCWD = rank, "\x00"
			}
			if s.CWD != lastCWD {
				lines = append(lines, cwdHeaderLines(s.CWD, width, 2)...)
				lastCWD = s.CWD
			}
		}
	}
	for i := range m.view {
		s := m.view[i]
		// Ghost parent: a stopped fork child whose parent is active (main pane).
		// Anchor the group headers off the child itself, then draw the parent as a
		// faint context row right above the branch.
		if g, ok := m.ghostParent[s.SessionID]; ok {
			emitHeaders(s)
			lines = append(lines, m.ghostRowLine(g, width))
			if i == m.cursor {
				sel = len(lines)
			}
			lines = append(lines, m.rowLine(s, i == m.cursor, m.roster[s.SessionID] != "", width))
			continue
		}
		if m.depth[s.SessionID] > 0 {
			// Forked child: nests under its parent — no dir/status header of its own.
			if i == m.cursor {
				sel = len(lines)
			}
			lines = append(lines, m.rowLine(s, i == m.cursor, m.roster[s.SessionID] != "", width))
			continue
		}
		emitHeaders(s)
		if i == m.cursor {
			sel = len(lines)
		}
		lines = append(lines, m.rowLine(s, i == m.cursor, m.roster[s.SessionID] != "", width))
	}
	return lines, sel
}

// cwdHeaderLines renders a directory header — bold base name, then the faint full
// path on its own line — each indented and clipped to the column width.
func cwdHeaderLines(cwd string, width, indent int) []string {
	clean := strings.TrimRight(cwd, "/")
	name := filepath.Base(clean)
	if name == "" || name == "." {
		name = clean
	}
	pad := strings.Repeat(" ", indent)
	return []string{
		cwdHeader.Render(pad + truncate(name, width-indent)),
		cwdPath.Render(pad + "  " + truncate(homeShorten(clean), width-indent-2)),
	}
}

// statusHeaderLine renders a status bucket label, indented and color-coded.
func statusHeaderLine(rank, width, indent int) string {
	return bucketStyle(rank).Render(truncate(strings.Repeat(" ", indent)+bucketLabel(rank), width))
}

// rowLabel is the text shown in a row's name cell: dirname/name (or the
// tree-indented bare name for a forked child — the parent row already shows
// the dir) plus any #labels.
func (m *Model) rowLabel(s claude.Session) string {
	name := m.rowName(s)
	if d := m.depth[s.SessionID]; d > 0 {
		name = strings.Repeat("  ", d-1) + "└─ " + m.displayName(s)
	}
	return name + m.labelSuffix(s.SessionID)
}

// nameColWidth is the width of the row name column. With the preview pane on,
// space is tight, so the column is capped and long names truncate; with it
// off, the column stretches to the longest visible row label so every name
// fits — bounded by width-18 so the status and age columns keep their room.
func (m *Model) nameColWidth(width int) int {
	if m.showPreview() {
		return clamp(width-m.cfg.List.NameColReserve, 18, m.cfg.List.NameColMax)
	}
	longest := 0
	for i := range m.view {
		if n := len([]rune(m.rowLabel(m.view[i]))); n > longest {
			longest = n
		}
	}
	for _, g := range m.ghostParent { // ghost context rows share the column
		if n := len([]rune(m.displayName(g))); n > longest {
			longest = n
		}
	}
	return clamp(longest, 18, width-m.cfg.List.NameColReserve)
}

func (m *Model) rowLine(s claude.Session, sel, attach bool, width int) string {
	st := m.statusOf(s)
	nameW := m.nameColWidth(width)
	name := m.rowLabel(s)
	body := fmt.Sprintf("%-*s %-8s %4s",
		nameW, truncate(name, nameW), statusLabelFor(st), humanAge(s.Started()))
	avail := width - 4 // marker(2) + dot(1) + space(1)
	if avail < 1 {
		avail = 1
	}
	body = truncate(body, avail)
	glyph, dotStyle := statusGlyphStyle(st)

	if sel {
		// Full-width highlight bar: pad the body so the background spans the row,
		// and tint every segment (cursor, dot, text) with the same background.
		body = padRight(body, avail)
		return cursorStyle.Background(selBg).Render("▸ ") +
			dotStyle.Background(selBg).Render(glyph) +
			selName.Background(selBg).Render(" "+body)
	}

	switch {
	case !attach:
		body = dimStyle.Render(body)
	default:
		body = nameStyle.Render(body)
	}
	return "  " + dotStyle.Render(glyph) + " " + body
}

// ghostRowLine renders a forked child's parent as a faint, non-selectable context
// row in the stopped window — the parent itself lives in the main pane. It mirrors
// rowLine's columns so the branch below lines up, but is dimmed throughout and
// tagged "↑ main" to show the parent is active elsewhere.
func (m *Model) ghostRowLine(s claude.Session, width int) string {
	st := m.statusOf(s)
	glyph, _ := statusGlyphStyle(st)
	nameW := m.nameColWidth(width)
	body := fmt.Sprintf("%-*s %-8s", nameW, truncate(m.displayName(s), nameW), statusLabelFor(st))
	body += "  ↑ main"
	avail := width - 4
	if avail < 1 {
		avail = 1
	}
	body = truncate(body, avail)
	return "  " + dimStyle.Render(glyph) + " " + dimStyle.Render(body)
}

func (m *Model) previewLines(h int) []string {
	var body []string
	s := m.current()
	switch {
	case s == nil:
		body = []string{dimStyle.Render("no selection")}
	default:
		switch txt, ok := m.prevCache[s.SessionID]; {
		case !ok:
			body = []string{dimStyle.Render("loading…")}
		case txt == "":
			body = []string{dimStyle.Render("no conversation yet")}
		default:
			for _, ln := range strings.Split(txt, "\n") {
				body = append(body, strings.TrimRight(ln, " "))
			}
		}
	}
	// Window the body to the pane: bottom-anchored, offset upward by previewScroll
	// (ctrl+u/ctrl+d, pgup/pgdn). The header arrows show which way more exists.
	hdr := "─ preview ─"
	if avail := h - 1; avail > 0 && len(body) > avail {
		maxOff := len(body) - avail
		off := clamp(m.previewScroll, 0, maxOff)
		end := len(body) - off
		body = body[end-avail : end]
		switch {
		case off >= maxOff:
			hdr = "─ preview ↓ ─" // at top; more below
		case off > 0:
			hdr = "─ preview ↑↓ ─" // middle
		default:
			hdr = "─ preview ↑ ─" // at bottom; more above
		}
	}
	return fit(append([]string{hintStyle.Render(hdr)}, body...), h)
}

func (m *Model) pickerLines(h, width int) []string {
	lines := []string{hintStyle.Render("pick dir: ") + m.input.View(), ""}
	rows := h - len(lines)
	if rows < 1 {
		rows = 1
	}
	if len(m.pickHit) == 0 {
		lines = append(lines, dimStyle.Render("  (no matching directories)"))
	} else {
		top := windowTop(m.pickCur, len(m.pickHit), rows)
		for i := top; i < len(m.pickHit) && i < top+rows; i++ {
			d := truncate(homeShorten(m.pickHit[i]), width-2)
			if i == m.pickCur {
				lines = append(lines, cursorStyle.Render("▸ ")+selName.Render(d))
			} else {
				lines = append(lines, "  "+nameStyle.Render(d))
			}
		}
	}
	return fit(lines, h)
}

func (m *Model) footerBlock() string {
	var status string
	switch {
	case m.mode == modeConfirm && m.pendingKill != "":
		// Bulk power-save (z/Z). Counts render fresh each frame, so the prompt
		// tracks the continuously-refreshing list.
		targets := m.killTargets(m.pendingKill)
		prompt := fmt.Sprintf("Stop %d idle session process(es)? (y/n — ↵ on a stopped session respawns it)", len(targets))
		if m.pendingKill == "all" {
			busy := len(targets) - len(m.killTargets("idle"))
			prompt = fmt.Sprintf("Stop all %d session process(es)? (%d busy) (y/n — ↵ respawns)", len(targets), busy)
		}
		status = warnDot.Render(prompt)
	case m.mode == modeConfirm && m.pending != nil:
		prompt := fmt.Sprintf("Stop %q? (y/n — y confirms)", m.displayName(*m.pending))
		switch {
		case m.unparked.Has(m.pending.SessionID):
			prompt = fmt.Sprintf("Move %q back to the stopped window? (y/n)", m.displayName(*m.pending))
		case !hasLiveWorker(*m.pending):
			prompt = fmt.Sprintf("Remove %q to the stopped window? (y/n — survives restart; resume it there)", m.displayName(*m.pending))
		}
		status = warnDot.Render(prompt)
	case m.mode == modePickDir:
		status = dimStyle.Render("↑/↓ or ctrl+j/k select · ↵ choose · esc cancel")
	case m.mode == modeFilter:
		status = dimStyle.Render("↑/↓ or ctrl+j/k navigate · ↵ open · tab keep filter · esc clear")
	case m.mode == modeRename:
		status = dimStyle.Render("↵ save · esc cancel · empty name clears the override")
	case m.mode == modeLabel:
		status = dimStyle.Render("↵ save · esc cancel · space-separated tags · empty clears · / finds them")
	case m.err != nil:
		status = errStyle.Render("error: " + m.err.Error())
	case m.status != "":
		status = dimStyle.Render(m.status)
	}
	return status + "\n" + m.helpBar()
}

func (m *Model) helpBar() string {
	stopped := "stopped"
	if m.stoppedView {
		stopped = "back"
	}
	binds := []struct{ k, d string }{
		{"n", "new"}, {"N", "new project"}, {"R", "rename"}, {"L", "label"},
		{"F", "fork"}, {"C", "clone"},
		{"d", "remove"}, {"b", "bring back"}, {"z/Z", "stop idle/all"},
		{"l", "logs"}, {"o", "group"}, {"s", stopped},
		{"p", "preview"}, {"^u/^d", "scroll"}, {"/", "filter"}, {"f", "search"},
		{"esc", "clear"}, {"r", "refresh"}, {"q", "quit"},
	}
	parts := make([]string, len(binds))
	for i, b := range binds {
		parts[i] = hintStyle.Render(b.k) + " " + helpStyle.Render(b.d)
	}
	return wrapJoin(parts, "   ", m.width)
}

// ---- layout helpers ----

// windowTop returns the first visible index for a list of n items shown in h
// rows, keeping the cursor roughly centred and in view.
func windowTop(cursor, n, h int) int {
	if h <= 0 || n <= h {
		return 0
	}
	top := cursor - h/2
	if top < 0 {
		top = 0
	}
	if top > n-h {
		top = n - h
	}
	return top
}

func joinColumns(left []string, lw int, right []string, rw, h int) []string {
	sep := dimStyle.Render(" │ ")
	out := make([]string, h)
	for i := 0; i < h; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		// Hard-clip the right column: a preview line wider than the pane (an
		// unwrapped code-block line, tab-expanded text, …) would overflow the
		// terminal row and shear the whole layout. ANSI-aware, and reset after the
		// cut so a truncated color run can't bleed into the next line.
		if lipgloss.Width(r) > rw {
			r = ansi.Truncate(r, rw, "…") + "\x1b[0m"
		}
		out[i] = padRight(l, lw) + sep + r
	}
	return out
}

// sanitizePreview neutralizes control characters that break terminal layout in
// preview text before it's cached: tabs become spaces (the terminal would expand
// them past the measured width) and stray CR/BS/FF/VT are dropped. ANSI escapes
// (colors from termview/glamour) pass through untouched.
func sanitizePreview(s string) string {
	return strings.NewReplacer("\t", "    ", "\r", "", "\b", "", "\f", "", "\v", "").Replace(s)
}

// fit pads with blank lines or truncates so the slice is exactly n long.
func fit(lines []string, n int) []string {
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines[:n]
}

func padRight(s string, w int) string {
	if g := w - lipgloss.Width(s); g > 0 {
		return s + strings.Repeat(" ", g)
	}
	return s
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func wrapJoin(parts []string, sep string, width int) string {
	if width <= 0 {
		return strings.Join(parts, sep)
	}
	sepW := lipgloss.Width(sep)
	var lines []string
	cur, curW := "", 0
	for _, p := range parts {
		pw := lipgloss.Width(p)
		switch {
		case cur == "":
			cur, curW = p, pw
		case curW+sepW+pw > width:
			lines = append(lines, cur)
			cur, curW = p, pw
		default:
			cur += sep + p
			curW += sepW + pw
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n")
}

// statusGlyphStyle returns the dot glyph and its color style for a status, so a
// caller can render it plain or with an added background (the selected row).
func statusGlyphStyle(status string) (string, lipgloss.Style) {
	switch status {
	case "running":
		return "●", workDot
	case "waiting":
		return "◆", warnDot
	case "error":
		return "✗", errStyle
	case "idle":
		return "○", idleDot
	case "complete":
		return "✓", doneDot
	case "stopped":
		return "◌", dimStyle
	default: // interactive / unknown
		return "·", dimStyle
	}
}

func statusDotFor(status string) string {
	g, st := statusGlyphStyle(status)
	return st.Render(g)
}

func statusLabelFor(status string) string {
	if status == "" {
		return "-"
	}
	return status
}

func humanAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func homeShorten(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}
