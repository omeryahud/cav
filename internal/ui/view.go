package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/preview"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("254")).Background(lipgloss.Color("238")).Padding(0, 1)
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	nameStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	selName     = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	selBg       = lipgloss.Color("238") // background for the highlighted (selected) row
	workDot     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	idleDot     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	warnDot     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	hintStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	cwdHeader   = lipgloss.NewStyle().Foreground(lipgloss.Color("147")).Bold(true)
	cwdPath     = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true) // "smaller" = faint
	statHeader  = lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true)

	// status dot + sub-header colors (kept in sync)
	doneDot = lipgloss.NewStyle().Foreground(lipgloss.Color("44"))               // complete ✓
	runHdr  = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Italic(true)  // running
	waitHdr = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Italic(true) // waiting for input
	errHdr  = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Italic(true) // error
	idleHdr = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true) // idle
	doneHdr = lipgloss.NewStyle().Foreground(lipgloss.Color("44")).Italic(true)  // complete

	// preview role labels (user vs assistant); message bodies are markdown-rendered
	userLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	asstLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("147")).Bold(true)
)

// renderSnippets renders the recent conversation: a color-coded role label per
// message followed by its body rendered from markdown to ANSI (via glamour),
// wrapped to width. Done once at load time (async), not per frame.
func renderSnippets(snips []preview.Snippet, width int) string {
	if len(snips) == 0 {
		return ""
	}
	if width < 10 {
		width = 10
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
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
		mid = joinColumns(m.listLines(midH, lw), lw, m.previewLines(midH), midH)
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
	}
	if m.stoppedView {
		parts = append(parts, "s: back to active")
	} else if n := m.countStopped(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d stopped (s)", n))
	}
	if m.previewOn && m.width < previewMinWidth {
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
	if m.groupMode != groupNone {
		vlines, sel := m.groupedVisual(width)
		top := windowTop(sel, len(vlines), h)
		end := top + h
		if end > len(vlines) {
			end = len(vlines)
		}
		return fit(vlines[top:end], h)
	}
	var lines []string
	top := windowTop(m.cursor, len(m.view), h)
	for i := top; i < len(m.view) && i < top+h; i++ {
		s := m.view[i]
		lines = append(lines, m.rowLine(s, i == m.cursor, m.roster[s.SessionID] != "", width))
	}
	return fit(lines, h)
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
	for i := range m.view {
		s := m.view[i]
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

func (m *Model) rowLine(s claude.Session, sel, attach bool, width int) string {
	st := m.statusOf(s)
	// Widen the name column to use the freed space (the message snippet is gone),
	// but cap it and never crowd the status/age columns on narrow layouts.
	nameW := clamp(width-18, 18, 36)
	body := fmt.Sprintf("%-*s %-8s %4s",
		nameW, truncate(m.rowName(s), nameW), statusLabelFor(st), humanAge(s.Started()))
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
	case m.mode == modeConfirm && m.pending != nil:
		prompt := fmt.Sprintf("Stop %q? (y/n — y confirms)", m.displayName(*m.pending))
		if !hasLiveWorker(*m.pending) {
			prompt = fmt.Sprintf("Hide %q from cav? (y/n — survives restart; still resumable via claude)", m.displayName(*m.pending))
		}
		status = warnDot.Render(prompt)
	case m.mode == modePickDir:
		status = dimStyle.Render("↑/↓ or ctrl+j/k select · ↵ choose · esc cancel")
	case m.mode == modeFilter:
		status = dimStyle.Render("↑/↓ or ctrl+j/k navigate · ↵ open · esc clear")
	case m.mode == modeRename:
		status = dimStyle.Render("↵ save · esc cancel · empty name clears the override")
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
		{"n", "new"}, {"N", "new project"}, {"R", "rename"},
		{"d", "remove"}, {"l", "logs"}, {"o", "group"}, {"s", stopped},
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

func joinColumns(left []string, lw int, right []string, h int) []string {
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
		out[i] = padRight(l, lw) + sep + r
	}
	return out
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
