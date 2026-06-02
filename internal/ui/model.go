// Package ui implements the cav bubbletea terminal interface.
package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/dirs"
	"github.com/omeryahud/cav/internal/dismiss"
	"github.com/omeryahud/cav/internal/names"
	"github.com/omeryahud/cav/internal/order"
	"github.com/omeryahud/cav/internal/preview"
	"github.com/omeryahud/cav/internal/search"
)

// previewMinWidth is the terminal width below which the preview pane is hidden.
const previewMinWidth = 100

// recentDays bounds how far back non-live on-disk sessions are shown.
const recentDays = 7

type mode int

const (
	modeList mode = iota
	modeFilter
	modeSearch
	modeNew
	modeConfirm
	modeRename
	modePickDir
)

// lastMsg is the most recent message of a session, shown on its list row.
type lastMsg struct{ role, text string }

// messages
type (
	refreshResult struct {
		sessions []claude.Session
		roster   claude.Roster
		states   map[string]string
		last     map[string]lastMsg
	}
	errMsg    struct{ err error }
	actionMsg struct {
		note string
		err  error
	}
	searchDoneMsg struct {
		q   string
		ids map[string]bool
	}
	dirsMsg    []string
	previewMsg struct {
		id   string
		text string // markdown rendered to ANSI at load time
	}
	tickMsg struct{}
)

// Model is the cav application state.
type Model struct {
	all         []claude.Session  // full list in display order
	view        []claude.Session  // filtered/searched subset shown
	roster      claude.Roster     // sessionId -> job id (attachable iff present)
	states      map[string]string // sessionId -> job lifecycle state (working/done/blocked)
	names       *names.Store      // cav-local display-name overrides
	dismissed   *dismiss.Store    // cav-local set of sessions hidden with d (survives restart)
	group       bool              // true: group by cwd then status; false: manual order
	stoppedView bool              // true: showing the stopped-sessions window (s toggles)
	justStopped map[string]bool   // just stopped from the main window; kept in the stopped window until reconciled
	cursor      int
	order       *order.Store
	mode        mode
	input       textinput.Model
	filter      string          // active metadata filter
	matchIDs    map[string]bool // active deep-search result set (nil = inactive)
	newCWD      string          // cwd for a pending new session
	pending     *claude.Session // session awaiting delete confirmation

	// new-session directory picker
	pickAll []string
	pickHit []string
	pickCur int

	// last message per session (shown on list rows)
	last map[string]lastMsg

	// preview pane
	previewOn bool
	prevCache map[string]string // sessionId -> markdown-rendered recent conversation
	prevReq   map[string]bool   // sessionId -> preview load requested

	status string
	err    error
	width  int
	height int
}

// New constructs the initial model.
func New() (*Model, error) {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 512
	ti.Width = 60
	return &Model{
		order:       order.Load(),
		names:       names.Load(),
		dismissed:   dismiss.Load(),
		input:       ti,
		mode:        modeList,
		group:       true,
		previewOn:   true,
		prevCache:   map[string]string{},
		prevReq:     map[string]bool{},
		states:      map[string]string{},
		last:        map[string]lastMsg{},
		justStopped: map[string]bool{},
	}, nil
}

// Init kicks off the first refresh and the periodic tick.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(refreshCmd, tickCmd())
}

// ---- commands ----

func refreshCmd() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	live, _ := claude.List(ctx) // ignore errors: still show durable on-disk jobs
	daemon := claude.LoadRoster()

	roster := claude.Roster{}
	states := map[string]string{}
	seen := map[string]bool{}
	liveJobs := map[string]bool{}
	var sessions []claude.Session

	// 1) Live sessions — resolve job ids from the daemon roster, which holds the
	//    CURRENT sessionId even after a /branch (a job's state.json can lag).
	for _, s := range live {
		sessions = append(sessions, s)
		seen[s.SessionID] = true
		if jid := daemon[s.SessionID]; jid != "" {
			roster[s.SessionID] = jid
			states[s.SessionID] = claude.JobState(jid)
			liveJobs[jid] = true
		}
	}

	// 2) On-disk jobs the daemon isn't currently serving (stopped, or dropped
	//    after a laptop sleep). Dedup by sessionId and by job id — a branched
	//    session shares its job with a live one, so skip that stale record.
	cutoff := time.Now().Add(-recentDays * 24 * time.Hour)
	for _, j := range claude.ScanJobs() {
		if seen[j.SessionID] || liveJobs[j.JobID] {
			continue
		}
		if !j.UpdatedAt.IsZero() && j.UpdatedAt.Before(cutoff) {
			continue // prune old, non-live jobs to bound clutter
		}
		sessions = append(sessions, claude.Session{
			SessionID: j.SessionID,
			CWD:       j.CWD,
			Name:      j.Name,
			Kind:      "background",
			StartedAt: j.UpdatedAt.UnixMilli(),
		})
		roster[j.SessionID] = j.JobID
		states[j.SessionID] = j.State
		seen[j.SessionID] = true
	}

	last := make(map[string]lastMsg, len(sessions))
	for _, s := range sessions {
		role, text := preview.Last(s.SessionID)
		last[s.SessionID] = lastMsg{role: role, text: text}
	}
	return refreshResult{sessions: sessions, roster: roster, states: states, last: last}
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func searchCmd(q string) tea.Cmd {
	return func() tea.Msg {
		ids, err := search.Match(q)
		if err != nil {
			return errMsg{err}
		}
		return searchDoneMsg{q: q, ids: ids}
	}
}

func createCmd(cwd, prompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := claude.Create(ctx, cwd, "", prompt); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{note: "session created"}
	}
}

func dirsCmd() tea.Cmd {
	return func() tea.Msg { return dirsMsg(dirs.Candidates()) }
}

func previewCmd(id string, width int) tea.Cmd {
	return func() tea.Msg {
		return previewMsg{id: id, text: renderSnippets(preview.Recent(id, 14), width)}
	}
}

func stopCmd(id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := claude.Stop(ctx, id); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{note: "stopped " + short(id)}
	}
}

// ---- helpers ----

func short(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func lastIndex(n int) int {
	if n <= 0 {
		return 0
	}
	return n - 1
}

func (m *Model) current() *claude.Session {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return nil
	}
	return &m.view[m.cursor]
}

func (m *Model) jobID(s *claude.Session) string {
	if s == nil {
		return ""
	}
	return m.roster[s.SessionID]
}

func (m *Model) canAttach(s *claude.Session) bool {
	return m.jobID(s) != ""
}

func notAttachableReason(s claude.Session) string {
	if s.Kind == "interactive" {
		return "interactive session (open it in its own terminal)"
	}
	return "not registered with the daemon"
}

// displayName returns the cav-local rename override if set, else the daemon name.
func (m *Model) displayName(s claude.Session) string {
	if n := m.names.Get(s.SessionID); n != "" {
		return n
	}
	return s.Display()
}

func (m *Model) showPreview() bool { return m.previewOn && m.width >= previewMinWidth }

// previewWidth is the column width used for the preview pane (and the wrap
// width markdown is rendered at) — half the screen.
func (m *Model) previewWidth() int {
	return m.width / 2
}

// ensurePreview lazily loads the selected session's transcript preview.
func (m *Model) ensurePreview() tea.Cmd {
	if !m.showPreview() {
		return nil
	}
	s := m.current()
	if s == nil {
		return nil
	}
	if _, ok := m.prevCache[s.SessionID]; ok {
		return nil // already loaded
	}
	if m.prevReq[s.SessionID] {
		return nil // load already in flight
	}
	m.prevReq[s.SessionID] = true
	return previewCmd(s.SessionID, m.previewWidth())
}

// recomputePick filters + ranks the directory candidates by the input value.
// Ranking: substring in the basename beats substring anywhere, which beats a
// fuzzy subsequence match; ties break toward shorter paths.
func (m *Model) recomputePick() {
	q := strings.ToLower(strings.TrimSpace(m.input.Value()))
	if q == "" {
		m.pickHit = m.pickAll
		m.pickCur = clamp(m.pickCur, 0, lastIndex(len(m.pickHit)))
		return
	}
	type scored struct {
		d string
		s int
	}
	hits := make([]scored, 0, len(m.pickAll))
	for _, d := range m.pickAll {
		if s := pickScore(d, q); s >= 0 {
			hits = append(hits, scored{d, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		return len(hits[i].d) < len(hits[j].d)
	})
	m.pickHit = make([]string, len(hits))
	for i, h := range hits {
		m.pickHit[i] = h.d
	}
	m.pickCur = clamp(m.pickCur, 0, lastIndex(len(m.pickHit)))
}

func pickScore(path, q string) int {
	p := strings.ToLower(path)
	base := strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	penalty := len(path) / 20
	switch {
	case strings.Contains(base, q):
		return 100 - penalty
	case strings.Contains(p, q):
		return 50 - penalty
	case subseq(p, q):
		return 10 - penalty
	default:
		return -1
	}
}

// subseq reports whether q is a (fuzzy) subsequence of s.
func subseq(s, q string) bool {
	i := 0
	for j := 0; i < len(q) && j < len(s); j++ {
		if q[i] == s[j] {
			i++
		}
	}
	return i == len(q)
}

// applyOrder sorts sessions by the saved order; sessions not in the saved
// order fall to the bottom, newest first.
func applyOrder(ss []claude.Session, ids []string) []claude.Session {
	pos := make(map[string]int, len(ids))
	for i, id := range ids {
		pos[id] = i
	}
	out := append([]claude.Session(nil), ss...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, oki := pos[out[i].SessionID]
		pj, okj := pos[out[j].SessionID]
		switch {
		case oki && okj:
			return pi < pj
		case oki != okj:
			return oki
		default:
			return out[i].StartedAt > out[j].StartedAt
		}
	})
	return out
}

// recompute rebuilds the visible view from the full list + active filters.
func (m *Model) recompute() {
	v := make([]claude.Session, 0, len(m.all))
	for _, s := range m.all {
		if m.matchIDs != nil && !m.matchIDs[s.SessionID] {
			continue
		}
		if m.filter != "" && !m.sessionMatches(s, m.filter) {
			continue
		}
		if m.isStopped(s) != m.stoppedView {
			continue // main window shows active sessions; the stopped window shows stopped ones (s toggles)
		}
		v = append(v, s)
	}
	if m.group {
		sort.SliceStable(v, func(i, j int) bool {
			a, b := v[i], v[j]
			if a.CWD != b.CWD {
				return a.CWD < b.CWD
			}
			if ra, rb := statusRank(m.statusOf(a)), statusRank(m.statusOf(b)); ra != rb {
				return ra < rb
			}
			return a.StartedAt > b.StartedAt
		})
	}
	m.view = v
	m.cursor = clamp(m.cursor, 0, lastIndex(len(v)))
}

// statusOf computes a normalized session status by combining the live
// agents --json status (busy/idle) with the job lifecycle state. Crucially,
// state "working" only means the session is alive — NOT that it is executing;
// only agents-status "busy" (or an in-flight task) means actually running. So
// an idle session reads "idle", not "running".
func (m *Model) statusOf(s claude.Session) string {
	if s.Status == "busy" {
		return "running"
	}
	switch m.states[s.SessionID] {
	case "blocked", "waiting", "needs_input", "needs input", "paused":
		return "waiting"
	case "error", "failed":
		return "error"
	case "done", "complete", "completed":
		return "complete"
	case "stopped":
		return "stopped"
	case "working", "running", "active", "ready", "idle":
		return "idle"
	}
	if s.Status == "idle" {
		return "idle"
	}
	return "" // interactive / unknown
}

// isStopped reports whether s belongs in the stopped-sessions window: either its
// job state is "stopped", or it was just stopped from the main window and is
// awaiting reconciliation (its state.json hasn't caught up yet).
func (m *Model) isStopped(s claude.Session) bool {
	return m.statusOf(s) == "stopped" || m.justStopped[s.SessionID]
}

// countStopped returns how many sessions currently live in the stopped window.
func (m *Model) countStopped() int {
	n := 0
	for _, s := range m.all {
		if m.isStopped(s) {
			n++
		}
	}
	return n
}

// filterDismissed drops sessions the user has hidden with d. Dismissed sessions
// stay on disk (still resumable via the claude CLI); cav just never lists them.
func (m *Model) filterDismissed(ss []claude.Session) []claude.Session {
	if m.dismissed.Len() == 0 {
		return ss
	}
	out := make([]claude.Session, 0, len(ss))
	for _, s := range ss {
		if !m.dismissed.Has(s.SessionID) {
			out = append(out, s)
		}
	}
	return out
}

// hasLiveWorker reports whether s is a running daemon worker that `claude stop`
// can actually act on. `agents --json` reports a status for live background
// workers; on-disk-only records (finished, stopped, or sleep-dropped) carry no
// status, so d hides them cav-locally instead of issuing a no-op stop.
func hasLiveWorker(s claude.Session) bool { return s.Status != "" }

// removeSession returns ss without the session whose id matches.
func removeSession(ss []claude.Session, id string) []claude.Session {
	out := ss[:0]
	for _, s := range ss {
		if s.SessionID != id {
			out = append(out, s)
		}
	}
	return out
}

// statusRank orders the status buckets. Input is a normalized status from statusOf.
func statusRank(status string) int {
	switch status {
	case "running":
		return 0
	case "waiting":
		return 1
	case "error":
		return 2
	case "idle":
		return 3
	case "complete":
		return 4
	case "stopped":
		return 5
	default:
		return 6
	}
}

func bucketLabel(rank int) string {
	switch rank {
	case 0:
		return "running"
	case 1:
		return "waiting for input"
	case 2:
		return "error"
	case 3:
		return "idle"
	case 4:
		return "complete"
	case 5:
		return "stopped"
	default:
		return "other"
	}
}

func (m *Model) sessionMatches(s claude.Session, f string) bool {
	hay := strings.ToLower(m.displayName(s) + " " + s.CWD + " " + s.Status + " " + s.Kind + " " + s.SessionID)
	return strings.Contains(hay, strings.ToLower(f))
}

// reorder moves the selected row by delta and persists the new order.
// Disabled while a filter/search is active (view != full list).
func (m *Model) reorder(delta int) {
	if m.stoppedView {
		m.status = "reordering is only available in the active window"
		return
	}
	if m.group {
		m.status = "press o for manual order, then reorder"
		return
	}
	if m.filter != "" || m.matchIDs != nil {
		m.status = "clear filter (esc) to reorder"
		return
	}
	i := m.cursor
	j := i + delta
	if i < 0 || i >= len(m.all) || j < 0 || j >= len(m.all) {
		return
	}
	m.all[i], m.all[j] = m.all[j], m.all[i]
	m.cursor = j
	ids := make([]string, len(m.all))
	for k, s := range m.all {
		ids[k] = s.SessionID
	}
	if err := m.order.Set(ids); err != nil {
		m.err = err
	}
	m.recompute()
}
