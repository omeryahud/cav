// Package ui implements the cav bubbletea terminal interface.
package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/dirs"
	"github.com/omeryahud/cav/internal/dismiss"
	"github.com/omeryahud/cav/internal/forks"
	"github.com/omeryahud/cav/internal/names"
	"github.com/omeryahud/cav/internal/preview"
	"github.com/omeryahud/cav/internal/search"
	"github.com/omeryahud/cav/internal/termview"
)

// previewMinWidth is the terminal width below which the preview pane is hidden.
const previewMinWidth = 100

// Size the live-session terminal emulator generously — it must be >= the
// session's own terminal so its pre-wrapped lines don't re-wrap; the screen is
// then cropped to the (smaller) preview pane.
const (
	previewEmuCols = 220
	previewEmuRows = 64
)

// recentDays bounds how far back non-live on-disk sessions are shown.
const recentDays = 7

// previewRefresh throttles background preview reloads (claude logs is ~0.5s) now
// that the list refreshes continuously; a selection change loads immediately.
const previewRefresh = 2 * time.Second

// grouping is how the session list is organized; the o key cycles through these
// in order.
type grouping int

const (
	groupNone      grouping = iota // ungrouped: a flat alphabetical list
	groupDirStatus                 // by cwd, then status (the default)
	groupStatusDir                 // by status, then cwd
)

type mode int

const (
	modeList mode = iota
	modeFilter
	modeSearch
	modeNew
	modeConfirm
	modeRename
	modePickDir
	modeNewProject
	modeNewName
)

// messages
type (
	refreshResult struct {
		sessions []claude.Session
		roster   claude.Roster
		states   map[string]string
		live     map[string]bool // sessionIds with a live daemon worker (vs on-disk only)
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
	// createdMsg follows a session create (n or N): the UI selects (highlights)
	// the new session in the list once it appears, keyed by its job id.
	createdMsg struct {
		jobID string
		label string
	}
	// forkedMsg follows F (fork): record childJobID -> parentSID in the fork store
	// and highlight the new child once it appears.
	forkedMsg struct {
		childJobID string
		parentSID  string
		label      string
	}
)

// Model is the cav application state.
type Model struct {
	all          []claude.Session  // full list in display order
	view         []claude.Session  // filtered/searched subset shown
	roster       claude.Roster     // sessionId -> job id (attachable iff present)
	states       map[string]string // sessionId -> job lifecycle state (working/done/blocked)
	live         map[string]bool   // sessionId -> has a live daemon worker (else respawn to attach)
	names        *names.Store      // cav-local display-name overrides
	dismissed    *dismiss.Store    // cav-local set of sessions hidden with d (survives restart)
	forks        *forks.Store      // cav-local child-jobId -> parent-sessionId (fork tree)
	depth        map[string]int    // sessionId -> fork-tree depth (0 = top-level), set by recompute
	groupMode    grouping          // none (alphabetical) | dir→status | status→dir (o cycles)
	stoppedView  bool              // true: showing the stopped-sessions window (s toggles)
	justStopped  map[string]bool   // just stopped from the main window; kept in the stopped window until reconciled
	cursor       int
	mode         mode
	input        textinput.Model
	filter       string          // active metadata filter
	matchIDs     map[string]bool // active deep-search result set (nil = inactive)
	newCWD       string          // cwd for a pending new session
	newName      string          // session name entered in the create wizard
	newIsProject bool            // create wizard: N (make a new dir) vs n (existing dir)
	selectJobID  string          // job id of a just-created session to highlight once it appears
	pending      *claude.Session // session awaiting delete confirmation

	// new-session directory picker
	pickAll []string
	pickHit []string
	pickCur int

	// preview pane
	previewOn     bool
	prevCache     map[string]string // sessionId -> markdown-rendered recent conversation
	prevReq       map[string]bool   // sessionId -> preview load requested
	previewScroll int               // preview lines scrolled up from the bottom (0 = latest)
	scrollFor     string            // sessionId previewScroll applies to (reset when selection changes)
	prevAt        time.Time         // last preview (re)load time, to throttle background reloads

	refreshes chan refreshResult // continuous background refresh results (see refreshLoop)

	status string
	err    error
	width  int
	height int
}

// New constructs the initial model. initialFilter (the cav CLI argument)
// pre-applies the / metadata filter, so `cav <term>` opens filtered to <term>.
func New(initialFilter string) (*Model, error) {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 512
	ti.Width = 60
	return &Model{
		filter:      initialFilter,
		names:       names.Load(),
		dismissed:   dismiss.Load(),
		forks:       forks.Load(),
		input:       ti,
		mode:        modeList,
		groupMode:   groupDirStatus,
		previewOn:   true,
		prevCache:   map[string]string{},
		prevReq:     map[string]bool{},
		states:      map[string]string{},
		justStopped: map[string]bool{},
	}, nil
}

// Init starts the background refresh loop and begins consuming its results.
func (m *Model) Init() tea.Cmd {
	m.refreshes = make(chan refreshResult)
	go refreshLoop(m.refreshes)
	return waitRefresh(m.refreshes)
}

// ---- commands ----

func doRefresh() refreshResult {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	live, _ := claude.List(ctx) // ignore errors: still show durable on-disk jobs
	daemon := claude.LoadRoster()
	jobRecs := claude.ScanJobs()
	// sessionId -> jobId from on-disk job dirs. The daemon roster is authoritative
	// (it has the CURRENT id after a /branch) but is often incomplete — it lists
	// only some live workers — so this fills the gaps, keeping every session with a
	// job dir attachable instead of "not registered with the daemon".
	jobByS := map[string]string{}
	for _, j := range jobRecs {
		if _, ok := jobByS[j.SessionID]; !ok {
			jobByS[j.SessionID] = j.JobID
		}
	}

	roster := claude.Roster{}
	states := map[string]string{}
	seen := map[string]bool{}
	liveJobs := map[string]bool{}
	liveSet := map[string]bool{} // sessionIds with a live daemon worker
	var sessions []claude.Session

	// 1) Live sessions — resolve job ids from the daemon roster, which holds the
	//    CURRENT sessionId even after a /branch (a job's state.json can lag).
	for _, s := range live {
		if s.Kind == "interactive" {
			// cav manages background sessions. Interactive workers (a `!` bash
			// command, an agent-mode/stream-json child, or a plain `claude` REPL)
			// aren't nameable, statusable, or attachable here — they're noise.
			continue
		}
		sessions = append(sessions, s)
		seen[s.SessionID] = true
		liveSet[s.SessionID] = true
		jid := daemon[s.SessionID]
		if jid == "" {
			jid = jobByS[s.SessionID] // roster gap → fall back to the on-disk job dir
		}
		if jid != "" {
			roster[s.SessionID] = jid
			states[s.SessionID] = claude.JobState(jid)
			liveJobs[jid] = true
		}
	}

	// 2) On-disk jobs the daemon isn't currently serving (stopped, or dropped
	//    after a laptop sleep). Dedup by sessionId and by job id — a branched
	//    session shares its job with a live one, so skip that stale record.
	cutoff := time.Now().Add(-recentDays * 24 * time.Hour)
	for _, j := range jobRecs {
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

	return refreshResult{sessions: sessions, roster: roster, states: states, live: liveSet}
}

// refreshLoop runs the session refresh continuously in the background — no fixed
// poll delay — so the list (status, ages, new sessions) updates as fast as a
// refresh completes (~0.5s, bounded by `claude agents --json`). minRefresh is a
// small floor that only matters if a refresh returns very fast (e.g. the daemon
// is down), to avoid a hot spin.
func refreshLoop(ch chan<- refreshResult) {
	const minRefresh = 250 * time.Millisecond
	for {
		start := time.Now()
		rr := doRefresh()
		if d := time.Since(start); d < minRefresh {
			time.Sleep(minRefresh - d)
		}
		ch <- rr
	}
}

// waitRefresh delivers the next background refresh result to the update loop.
func waitRefresh(ch <-chan refreshResult) tea.Cmd {
	return func() tea.Msg { return <-ch }
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

func createCmd(cwd, name, prompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		jobID, err := claude.Create(ctx, cwd, name, prompt)
		if err != nil {
			return actionMsg{err: err}
		}
		label := name
		if label == "" {
			label = filepath.Base(cwd)
		}
		return createdMsg{jobID: jobID, label: label}
	}
}

// forkCmd forks parentSID — continuing its conversation in a new child bg session
// (see claude.Fork) — and reports the child's job id so the UI can record the
// link and highlight the child.
func forkCmd(parentSID, parentJobID, cwd, label string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		childJob, err := claude.Fork(ctx, parentSID, parentJobID, cwd)
		if err != nil {
			return actionMsg{err: err}
		}
		return forkedMsg{childJobID: childJob, parentSID: parentSID, label: label}
	}
}

// projectRoot is where the "new project" shortcut (N) creates directories.
func projectRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "go", "src", "github.com", "omeryahud")
}

// newProjectCmd creates the directory cwd (which must be under projectRoot),
// starts a background session in it named name (falling back to the dir's base
// name) with an optional prompt, and asks the UI to open that session.
func newProjectCmd(cwd, name, prompt string) tea.Cmd {
	return func() tea.Msg {
		root := projectRoot()
		cwd = filepath.Clean(cwd)
		if cwd != root && !strings.HasPrefix(cwd, root+string(os.PathSeparator)) {
			return actionMsg{err: fmt.Errorf("invalid project path %q", cwd)}
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			return actionMsg{err: err}
		}
		if name == "" {
			name = filepath.Base(cwd)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		jobID, err := claude.Create(ctx, cwd, name, prompt)
		if err != nil {
			return actionMsg{err: err}
		}
		return createdMsg{jobID: jobID, label: name}
	}
}

func dirsCmd() tea.Cmd {
	return func() tea.Msg { return dirsMsg(dirs.Candidates()) }
}

// previewCmd loads the preview for a session. For a session with a live worker
// it shows the actual terminal screen (claude logs → emulated at width×height);
// otherwise (and on any logs failure) it falls back to the transcript text.
func previewCmd(id, jobID string, live bool, width, height int) tea.Cmd {
	return func() tea.Msg {
		if live && jobID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if raw, err := claude.Logs(ctx, jobID); err == nil && len(raw) > 0 {
				emuCols, emuRows := max(width, previewEmuCols), max(height, previewEmuRows)
				return previewMsg{id: id, text: termview.Render(raw, emuCols, emuRows, width)}
			}
		}
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

// viewIndexByJobID returns the index in the visible view of the session with the
// given job id, or -1 if it isn't currently shown.
func (m *Model) viewIndexByJobID(jobID string) int {
	for i := range m.view {
		if m.roster[m.view[i].SessionID] == jobID {
			return i
		}
	}
	return -1
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

// dirBase is the leaf directory name of a cwd (".../agent-sandbox" →
// "agent-sandbox"), or "" if there isn't a meaningful one.
func dirBase(cwd string) string {
	switch b := filepath.Base(strings.TrimRight(cwd, "/")); b {
	case "", ".", "/":
		return ""
	default:
		return b
	}
}

// rowName is the label shown for a session in the list: its display name prefixed
// with the cwd's leaf dir ("dirname/name"), so the directory is visible inline on
// every row. The prefix is a display decoration computed from the cwd — not stored
// and not part of the editable (rename) name — so it applies uniformly to every
// session (new and existing) with no double-prefixing.
func (m *Model) rowName(s claude.Session) string {
	if d := dirBase(s.CWD); d != "" {
		return d + "/" + m.displayName(s)
	}
	return m.displayName(s)
}

func (m *Model) showPreview() bool { return m.previewOn && m.width >= previewMinWidth }

// previewWidth is the column width used for the preview pane (and the wrap
// width markdown is rendered at) — half the screen.
func (m *Model) previewWidth() int {
	return m.width / 2
}

// midHeight is the height of the middle list/preview region (everything between
// the 2-line header and the footer), mirroring the layout math in View.
func (m *Model) midHeight() int {
	h := m.height - 2 - len(strings.Split(m.footerBlock(), "\n"))
	if h < 1 {
		h = 1
	}
	return h
}

// previewBodyHeight is the row count available to preview content, below the
// pane's "─ preview ─" header. Used to size the emulated terminal screen.
func (m *Model) previewBodyHeight() int {
	if h := m.midHeight() - 1; h > 0 {
		return h
	}
	return 1
}

// previewCmdFor builds the preview load command for s, supplying the job id and
// live-worker flag so previewCmd can choose the live-terminal or text path.
func (m *Model) previewCmdFor(s *claude.Session) tea.Cmd {
	return previewCmd(s.SessionID, m.jobID(s), hasLiveWorker(*s), m.previewWidth(), m.previewBodyHeight())
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
	if s.SessionID != m.scrollFor { // moved to a different session → show its latest
		m.previewScroll = 0
		m.scrollFor = s.SessionID
	}
	if _, ok := m.prevCache[s.SessionID]; ok {
		return nil // already loaded
	}
	if m.prevReq[s.SessionID] {
		return nil // load already in flight
	}
	m.prevReq[s.SessionID] = true
	m.prevAt = time.Now()
	return m.previewCmdFor(s)
}

// previewBodyLines is the number of lines in the current preview's cached text
// (the scrollable body below the header).
func (m *Model) previewBodyLines() int {
	s := m.current()
	if s == nil {
		return 0
	}
	if txt, ok := m.prevCache[s.SessionID]; ok && txt != "" {
		return strings.Count(txt, "\n") + 1
	}
	return 0
}

// scrollPreview scrolls the preview by delta lines (positive = toward older
// content), clamped so it can't move past the top or the latest content.
func (m *Model) scrollPreview(delta int) {
	if !m.showPreview() {
		return
	}
	maxOff := m.previewBodyLines() - m.previewBodyHeight()
	if maxOff < 0 {
		maxOff = 0
	}
	m.previewScroll = clamp(m.previewScroll+delta, 0, maxOff)
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

// recompute rebuilds the visible view from the full list + active filters.
func (m *Model) recompute() {
	q := strings.ToLower(strings.TrimSpace(m.filter))
	v := make([]claude.Session, 0, len(m.all))
	for _, s := range m.all {
		if m.matchIDs != nil && !m.matchIDs[s.SessionID] {
			continue
		}
		if q != "" && !m.sessionMatches(s, q) {
			continue // doesn't fuzzy-match the filter
		}
		if m.isStopped(s) != m.stoppedView {
			continue // main window shows active sessions; the stopped window shows stopped ones (s toggles)
		}
		v = append(v, s)
	}
	// The fuzzy filter only narrows the set; the active sort below still orders it
	// (grouped matches stay in their groups; ungrouped stays alphabetical), so a
	// search keeps its structure instead of collapsing into one list.
	switch m.groupMode {
	case groupDirStatus:
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
	case groupStatusDir:
		sort.SliceStable(v, func(i, j int) bool {
			a, b := v[i], v[j]
			if ra, rb := statusRank(m.statusOf(a)), statusRank(m.statusOf(b)); ra != rb {
				return ra < rb
			}
			if a.CWD != b.CWD {
				return a.CWD < b.CWD
			}
			return a.StartedAt > b.StartedAt
		})
	case groupNone:
		// Ungrouped: a flat, case-insensitive alphabetical list by the displayed
		// label (dirname/name).
		sort.SliceStable(v, func(i, j int) bool {
			ni, nj := strings.ToLower(m.rowName(v[i])), strings.ToLower(m.rowName(v[j]))
			if ni != nj {
				return ni < nj
			}
			return v[i].SessionID < v[j].SessionID
		})
	}
	// Nest forked children directly under their parent (a tree), overriding the
	// flat sort for the children; top-level sessions keep their sorted order.
	v, m.depth = m.applyForkTree(v)
	m.view = v
	m.cursor = clamp(m.cursor, 0, lastIndex(len(v)))
}

// applyForkTree reorders v so each forked child follows its parent, recording a
// depth per session (0 = top-level, 1 = child, …) for indented rendering.
// Children ride with their parent regardless of the sort; a child whose parent
// isn't in v stays top-level.
func (m *Model) applyForkTree(v []claude.Session) ([]claude.Session, map[string]int) {
	if m.forks == nil || len(v) == 0 {
		return v, nil
	}
	inView := make(map[string]bool, len(v))
	for i := range v {
		inView[v[i].SessionID] = true
	}
	bySID := make(map[string]claude.Session, len(v))
	parentOf := make(map[string]string, len(v))
	childrenOf := map[string][]string{}
	for _, s := range v {
		bySID[s.SessionID] = s
		if p := m.forks.Parent(m.roster[s.SessionID]); p != "" && p != s.SessionID && inView[p] {
			parentOf[s.SessionID] = p
			childrenOf[p] = append(childrenOf[p], s.SessionID)
		}
	}
	if len(parentOf) == 0 {
		return v, nil // no fork relationships visible — leave the sort as-is
	}
	depth := make(map[string]int, len(v))
	ordered := make([]claude.Session, 0, len(v))
	seen := make(map[string]bool, len(v))
	var emit func(sid string, d int)
	emit = func(sid string, d int) {
		if seen[sid] {
			return // guard against a cycle
		}
		seen[sid] = true
		depth[sid] = d
		ordered = append(ordered, bySID[sid])
		for _, c := range childrenOf[sid] {
			emit(c, d+1)
		}
	}
	for _, s := range v {
		if parentOf[s.SessionID] == "" {
			emit(s.SessionID, 0)
		}
	}
	for _, s := range v { // any leftover (e.g. cyclic chain) → top-level
		if !seen[s.SessionID] {
			depth[s.SessionID] = 0
			ordered = append(ordered, s)
			seen[s.SessionID] = true
		}
	}
	return ordered, depth
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

// isStopped reports whether s belongs in the stopped-sessions window: its job
// state is "stopped", it was just stopped from the main window and is awaiting
// reconciliation (state.json hasn't caught up), or it was removed with d (a
// cav-local dismiss) while having no live worker. So d-removed sessions move to
// the stopped window instead of vanishing — visible and resumable there — and a
// resumed one (live again) leaves it for the main window.
func (m *Model) isStopped(s claude.Session) bool {
	return m.statusOf(s) == "stopped" || m.justStopped[s.SessionID] ||
		(m.dismissed.Has(s.SessionID) && !hasLiveWorker(s))
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

// hasLiveWorker reports whether s is a running daemon worker that `claude stop`
// can actually act on. `agents --json` reports a status for live background
// workers; on-disk-only records (finished, stopped, or sleep-dropped) carry no
// status, so d hides them cav-locally instead of issuing a no-op stop.
func hasLiveWorker(s claude.Session) bool { return s.Status != "" }

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

// sessionMatches reports whether the session fuzzy-matches the filter query q
// (q already lower-cased, trimmed, and non-empty). It matches as a contiguous
// substring of the name, cwd, or status/kind/id, or as a scattered subsequence
// of the name or the cwd's leaf dir. Subsequence matching is deliberately
// limited to the name and leaf dir — subsequence-matching the full cwd path
// would match almost any short query (a long path contains nearly any few
// letters in order); status/kind/id match only as a substring (no hex-id noise).
func (m *Model) sessionMatches(s claude.Session, q string) bool {
	name := strings.ToLower(m.displayName(s))
	cwd := strings.ToLower(s.CWD)
	base := cwd
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	meta := strings.ToLower(s.Status + " " + s.Kind + " " + s.SessionID)
	return strings.Contains(name, q) ||
		strings.Contains(base, q) ||
		strings.Contains(cwd, q) ||
		strings.Contains(meta, q) ||
		subseq(name, q) ||
		subseq(base, q)
}
