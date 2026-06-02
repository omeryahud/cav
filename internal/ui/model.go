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
	"github.com/omeryahud/cav/internal/names"
	"github.com/omeryahud/cav/internal/order"
	"github.com/omeryahud/cav/internal/preview"
	"github.com/omeryahud/cav/internal/search"
)

// previewMinWidth is the terminal width below which the preview pane is hidden.
const previewMinWidth = 100

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
	all      []claude.Session  // full list in display order
	view     []claude.Session  // filtered/searched subset shown
	roster   claude.Roster     // sessionId -> job id (attachable iff present)
	states   map[string]string // sessionId -> job lifecycle state (working/done/blocked)
	names    *names.Store      // cav-local display-name overrides
	group    bool              // true: group by cwd then status; false: manual order
	cursor   int
	order    *order.Store
	mode     mode
	input    textinput.Model
	filter   string          // active metadata filter
	matchIDs map[string]bool // active deep-search result set (nil = inactive)
	newCWD   string          // cwd for a pending new session
	pending  *claude.Session // session awaiting delete confirmation

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
		order:     order.Load(),
		names:     names.Load(),
		input:     ti,
		mode:      modeList,
		group:     true,
		previewOn: true,
		prevCache: map[string]string{},
		prevReq:   map[string]bool{},
		states:    map[string]string{},
		last:      map[string]lastMsg{},
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
	ss, err := claude.List(ctx)
	if err != nil {
		return errMsg{err}
	}
	r := claude.LoadRoster()
	states := make(map[string]string, len(ss))
	last := make(map[string]lastMsg, len(ss))
	for _, s := range ss {
		states[s.SessionID] = claude.JobState(r[s.SessionID])
		role, text := preview.Last(s.SessionID)
		last[s.SessionID] = lastMsg{role: role, text: text}
	}
	return refreshResult{sessions: ss, roster: r, states: states, last: last}
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
// width markdown is rendered at).
func (m *Model) previewWidth() int {
	pw := m.width / 2
	if pw > 60 {
		pw = 60
	}
	return pw
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
		v = append(v, s)
	}
	if m.group {
		sort.SliceStable(v, func(i, j int) bool {
			a, b := v[i], v[j]
			if a.CWD != b.CWD {
				return a.CWD < b.CWD
			}
			if ra, rb := statusRank(m.effectiveStatus(a)), statusRank(m.effectiveStatus(b)); ra != rb {
				return ra < rb
			}
			return a.StartedAt > b.StartedAt
		})
	}
	m.view = v
	m.cursor = clamp(m.cursor, 0, lastIndex(len(v)))
}

// effectiveStatus prefers the rich job state; falls back to the agents --json status.
func (m *Model) effectiveStatus(s claude.Session) string {
	if st := m.states[s.SessionID]; st != "" {
		return st
	}
	return s.Status
}

// statusRank orders the status buckets: running, waiting, complete, error, other.
func statusRank(state string) int {
	switch strings.ToLower(state) {
	case "working", "running", "busy", "active":
		return 0
	case "blocked", "waiting", "needs_input", "needs input", "paused":
		return 1
	case "done", "complete", "completed", "idle", "ready":
		return 2
	case "error", "failed":
		return 3
	default:
		return 4
	}
}

func bucketLabel(rank int) string {
	switch rank {
	case 0:
		return "running"
	case 1:
		return "waiting for input"
	case 2:
		return "complete"
	case 3:
		return "error"
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
