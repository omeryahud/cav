package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
)

// Update is the bubbletea message handler.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width != m.width || msg.Height != m.height { // re-render previews for the new size (live terminal view depends on both)
			m.prevCache = map[string]string{}
			m.prevReq = map[string]bool{}
		}
		m.width, m.height = msg.Width, msg.Height
		return m, m.ensurePreview()

	case refreshResult:
		m.all = msg.sessions
		m.roster = msg.roster
		m.states = msg.states
		m.live = msg.live
		// Remember each session's name so a later refresh that momentarily lacks it
		// (the daemon drops it from agents --json, and state.json often has none)
		// doesn't blank the row to the short id. Record real names only; never clear.
		for i := range m.all {
			if n := m.all[i].Name; n != "" && n != m.all[i].Short() {
				m.lastName[m.all[i].SessionID] = n
			}
		}
		// Drop optimistic stop-hides once confirmed (state is stopped) or the
		// session is gone; statusOf then keeps genuinely-stopped ones hidden.
		if len(m.justStopped) > 0 {
			present := make(map[string]bool, len(msg.sessions))
			for _, s := range msg.sessions {
				present[s.SessionID] = true
			}
			for sid := range m.justStopped {
				if !present[sid] || m.states[sid] == "stopped" {
					delete(m.justStopped, sid)
				}
			}
		}
		m.recompute()
		// A just-created session (n/N) registers asynchronously; once it appears
		// in the list, move the cursor to it (highlight it) instead of attaching.
		if m.selectJobID != "" {
			if i := m.viewIndexByJobID(m.selectJobID); i >= 0 {
				m.cursor = i
				m.previewScroll = 0
				m.selectJobID = ""
				m.prevAt = time.Time{} // force a preview load for the newly-selected session
			}
		}
		// Pump the next background refresh, and reload the selected preview —
		// throttled (the list now refreshes continuously; selection changes load
		// immediately via ensurePreview).
		cmds := []tea.Cmd{waitRefresh(m.refreshes)}
		if m.showPreview() {
			if s := m.current(); s != nil && time.Since(m.prevAt) >= previewRefresh {
				m.prevAt = time.Now()
				m.prevReq[s.SessionID] = true
				cmds = append(cmds, m.previewCmdFor(s))
			}
		}
		return m, tea.Batch(cmds...)

	case dirsMsg:
		m.pickAll = []string(msg)
		m.recomputePick()
		if m.status == "loading directories…" {
			m.status = ""
		}
		return m, nil

	case previewMsg:
		m.prevCache[msg.id] = msg.text
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case actionMsg:
		if msg.err != nil {
			m.err = msg.err
		} else if msg.note != "" {
			m.status, m.err = msg.note, nil
		}
		return m, nil // the background loop refreshes continuously

	case searchDoneMsg:
		m.matchIDs = msg.ids
		m.status = fmt.Sprintf("%d live session(s) mention %q  (esc clears)", countLive(m.all, msg.ids), msg.q)
		m.recompute()
		return m, nil

	case createdMsg:
		// Freshly-created session (n or N). Don't attach — refresh, and once the
		// new session shows up, highlight it (cursor moves to it; see refreshResult).
		m.status = "created " + msg.label
		m.selectJobID = msg.jobID // "" if the id couldn't be parsed → just won't auto-select
		return m, nil             // the background loop will see it and highlight it

	case forkedMsg:
		// Fork (F) created a child continuing parentSID's conversation under a new
		// session id. Record child→parent so it nests under the parent, and
		// highlight the child once it appears (like a create).
		if err := m.forks.Set(msg.childJobID, msg.parentSID); err != nil {
			m.err = err
		}
		m.status = "forked " + msg.label
		m.selectJobID = msg.childJobID
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Non-key messages while typing (e.g. cursor blink) go to the input.
	if m.mode == modeFilter || m.mode == modeSearch || m.mode == modeNew || m.mode == modeNewProject || m.mode == modeNewName {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeFilter:
		return m.handleFilterKey(msg)
	case modeSearch:
		return m.handleSearchKey(msg)
	case modeNew:
		return m.handleNewKey(msg)
	case modeConfirm:
		return m.handleConfirmKey(msg)
	case modeRename:
		return m.handleRenameKey(msg)
	case modePickDir:
		return m.handlePickKey(msg)
	case modeNewProject:
		return m.handleNewProjectKey(msg)
	case modeNewName:
		return m.handleNewNameKey(msg)
	}
	return m.handleListKey(msg)
}

func (m *Model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.cursor = clamp(m.cursor+1, 0, lastIndex(len(m.view)))
	case "k", "up":
		m.cursor = clamp(m.cursor-1, 0, lastIndex(len(m.view)))
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = lastIndex(len(m.view))
	case "p":
		m.previewOn = !m.previewOn
	case "ctrl+u":
		m.scrollPreview(max(1, m.previewBodyHeight()/2)) // half page toward older
	case "ctrl+d":
		m.scrollPreview(-max(1, m.previewBodyHeight()/2)) // half page toward latest
	case "pgup":
		m.scrollPreview(max(1, m.previewBodyHeight()-1))
	case "pgdown":
		m.scrollPreview(-max(1, m.previewBodyHeight()-1))
	case "o":
		m.groupMode = (m.groupMode + 1) % 3 // cycle: none → dir→status → status→dir
		m.recompute()
	case "s":
		m.stoppedView = !m.stoppedView
		m.cursor = 0
		m.recompute()
	case "r":
		// The list refreshes continuously in the background; r just clears any
		// stale status/error and forces an immediate preview reload.
		m.status, m.err, m.prevAt = "", nil, time.Time{}
	case "/":
		m.mode = modeFilter
		m.filter = "" // start a fresh filter — don't carry the previous query
		m.input.SetValue("")
		m.input.Placeholder = "filter name / cwd / status…"
		m.recompute()
		return m, tea.Batch(m.input.Focus(), m.ensurePreview())
	case "f":
		m.mode = modeSearch
		m.input.SetValue("")
		m.input.Placeholder = "search conversation text…"
		return m, m.input.Focus()
	case "R":
		if s := m.current(); s != nil {
			m.mode = modeRename
			m.input.SetValue(m.displayName(*s))
			m.input.Placeholder = "new name (empty clears override)…"
			m.input.CursorEnd()
			return m, m.input.Focus()
		}
	case "F":
		// Fork the highlighted session: a new child bg session continuing its
		// conversation, nested under it in the list.
		if s := m.current(); s != nil {
			m.status = "forking " + m.displayName(*s) + "…"
			return m, forkCmd(s.SessionID, m.jobID(s), s.CWD, m.displayName(*s))
		}
	case "b":
		// Bring a stopped session back to the main pane (relocated as-is, keeping
		// its status); it nests into the fork tree there. d returns it — inverse of d.
		s := m.current()
		if s == nil {
			break
		}
		if !m.isStopped(*s) {
			m.status = m.displayName(*s) + " is already in the main list"
			break
		}
		sid, name := s.SessionID, m.displayName(*s)
		if err := m.unparked.Add(sid); err != nil {
			m.err = err
			break
		}
		m.stoppedView = false
		m.status = "brought " + name + " back to the main list"
		m.recompute()
		for i := range m.view {
			if m.view[i].SessionID == sid {
				m.cursor = i
				break
			}
		}
	case "n":
		m.mode = modePickDir
		m.pickAll, m.pickHit, m.pickCur = nil, nil, 0
		m.input.SetValue("")
		m.input.Placeholder = "fuzzy-find a directory…"
		m.status = "loading directories…"
		return m, tea.Batch(m.input.Focus(), dirsCmd())
	case "N":
		m.mode = modeNewProject
		m.input.SetValue("")
		m.input.Placeholder = "new project name (creates " + homeShorten(projectRoot()) + "/<name>)…"
		return m, m.input.Focus()
	case "d":
		s := m.current()
		if s == nil {
			break
		}
		// d removes a session via the confirm prompt: one with a live worker is
		// stopped (→ stopped window); any other (finished, or a non-attachable
		// interactive session) is hidden cav-locally. Dismissing needs only the
		// session id, so — unlike open/logs — this works without a job id.
		cp := *s
		m.pending = &cp
		m.mode = modeConfirm
	case "l":
		s := m.current()
		if s == nil {
			break
		}
		if !m.canAttach(s) {
			m.status = "no logs for " + m.displayName(*s) + " — " + notAttachableReason(*s)
			break
		}
		return m, tea.ExecProcess(claude.LogsShellCmd(m.jobID(s)), func(error) tea.Msg {
			return actionMsg{} // less exit status is not meaningful here
		})
	case "enter", "right":
		if cmd := m.openCurrent(); cmd != nil {
			return m, cmd
		}
		// Nothing to open (empty list, or not attachable — openCurrent set the
		// status); fall through to refresh the preview.
	case "esc":
		m.filter, m.matchIDs, m.status = "", nil, ""
		m.recompute()
	}
	return m, m.ensurePreview()
}

// openCurrent attaches to the selected session — the enter/right action, shared
// by the list and the filter prompt. It hands the terminal to `claude attach`
// (resuming a stopped session, and leaving the stopped window if we were in it)
// and returns that command, or nil if there's nothing to open or the selection
// isn't attachable (a status message is set in that case). When the user leaves
// the conversation, ExecProcess resumes cav in place with all state intact; the
// attach exit code is ignored — leaving via Ctrl+Z/Ctrl+C is a normal exit.
func (m *Model) openCurrent() tea.Cmd {
	s := m.current()
	if s == nil {
		return nil
	}
	if !m.canAttach(s) {
		m.status = "can't open " + m.displayName(*s) + " — " + notAttachableReason(*s)
		return nil
	}
	id, label := m.jobID(s), m.displayName(*s) // attach by job id, not session id
	note := "← back from " + label
	// A live worker attaches directly. A stopped or sleep-dropped session has no
	// live worker — the daemon has released the job, so `claude attach` alone
	// errors ("job not found" / "not known to the daemon"). Respawn it first
	// (same job id, from the stored respawn flags), then attach — exactly what
	// the native agents view does.
	cmd := claude.AttachCmd(id)
	if !m.live[s.SessionID] {
		cmd = claude.ResumeAttachCmd(id)
	}
	if m.stoppedView {
		// Resuming leaves the stopped window; the post-attach refresh reclassifies
		// the now-active session into the main one.
		m.stoppedView = false
		m.cursor = 0
		m.recompute()
		note = "↩ resumed " + label
	}
	return tea.ExecProcess(cmd, func(error) tea.Msg {
		return actionMsg{note: note}
	})
}

func (m *Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.filter = ""
		m.input.Blur()
		m.recompute()
		return m, nil
	case "enter":
		// Open the selected session directly — a single enter from the filter,
		// rather than confirming the filter and pressing enter again in the list.
		cmd := m.openCurrent()
		m.mode = modeList
		m.input.Blur()
		return m, cmd
	case "tab":
		// Confirm the filter and drop to the list WITHOUT opening anything; the
		// filter stays applied. (shift+enter can't be detected by bubbletea v1, so
		// tab is the "confirm but don't open" key — enter opens, tab doesn't.)
		m.mode = modeList
		m.input.Blur()
		return m, nil
	// Navigate the live-filtered list without leaving the prompt (fuzzy-finder
	// style), so the user needn't press enter just to move the selection.
	case "up", "ctrl+k", "ctrl+p":
		m.cursor = clamp(m.cursor-1, 0, lastIndex(len(m.view)))
		return m, m.ensurePreview()
	case "down", "ctrl+j", "ctrl+n":
		m.cursor = clamp(m.cursor+1, 0, lastIndex(len(m.view)))
		return m, m.ensurePreview()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.filter = m.input.Value()
	m.recompute()
	return m, tea.Batch(cmd, m.ensurePreview())
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		q := m.input.Value()
		m.mode = modeList
		m.input.Blur()
		if q == "" {
			m.matchIDs = nil
			m.recompute()
			return m, nil
		}
		m.status = "searching transcripts…"
		return m, searchCmd(q)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) handleNewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		prompt := m.input.Value()
		m.mode = modeList
		m.input.Blur()
		if m.newIsProject {
			m.status = "creating project…"
			return m, newProjectCmd(m.newCWD, m.newName, prompt)
		}
		m.status = "creating session…"
		return m, createCmd(m.newCWD, m.newName, prompt)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleNewNameKey is the create wizard's session-name step: capture the name,
// then advance to the prompt step (modeNew). esc cancels the whole wizard.
func (m *Model) handleNewNameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		m.newName = strings.TrimSpace(m.input.Value())
		m.mode = modeNew
		m.input.SetValue("")
		m.input.Placeholder = "initial prompt (optional)…"
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// handleNewProjectKey handles the N flow: type a name → make the directory under
// projectRoot, start a session there, and open it.
func (m *Model) handleNewProjectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		project := strings.TrimSpace(m.input.Value())
		if project == "" {
			m.mode = modeList
			m.input.Blur()
			return m, nil
		}
		m.newCWD = filepath.Join(projectRoot(), project)
		m.newIsProject = true
		m.mode = modeNewName
		m.input.SetValue(filepath.Base(m.newCWD)) // default session name = the new dir's name
		m.input.Placeholder = "session name…"
		m.input.CursorEnd()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		s := m.pending
		m.mode = modeList
		m.pending = nil
		if s == nil {
			return m, nil
		}
		if m.unparked.Has(s.SessionID) {
			// It was brought back to the main pane (b); d returns it to the stopped window.
			if err := m.unparked.Remove(s.SessionID); err != nil {
				m.err = err
				return m, nil
			}
			m.status = "moved " + m.displayName(*s) + " back to the stopped window (s)"
			m.recompute()
			return m, nil
		}
		// Persist the display name cav-locally so it survives the session going
		// on-disk: a live session's daemon name (from `agents --json`) often isn't
		// written to the job's state.json, so without this the stopped-window entry
		// would revert to the short id. Only when there's no rename override yet and
		// the name is a real one (not just the short id).
		if m.names.Get(s.SessionID) == "" {
			if n := m.displayName(*s); n != s.Short() { // current or last-seen name
				_ = m.names.Set(s.SessionID, n)
			}
		}
		if hasLiveWorker(*s) {
			m.justStopped[s.SessionID] = true // hide now; refresh reconciles once state.json updates
			m.status = "stopping " + m.displayName(*s) + "…"
			m.recompute()
			return m, stopCmd(m.jobID(s))
		}
		// No live worker: `claude stop` would be a no-op (and the session would
		// reappear in the main list on restart), so mark it cav-locally. That moves
		// it to the stopped window (see isStopped) — out of the main list but still
		// visible and resumable there — and the mark survives restart.
		if err := m.dismissed.Add(s.SessionID); err != nil {
			m.err = err
			return m, nil
		}
		m.status = "moved " + m.displayName(*s) + " to the stopped window (press s)"
		m.recompute()
		return m, nil
	default:
		m.mode = modeList
		m.pending = nil
		return m, nil
	}
}

func (m *Model) handleRenameKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		return m, nil
	case "enter":
		if s := m.current(); s != nil {
			if err := m.names.Set(s.SessionID, strings.TrimSpace(m.input.Value())); err != nil {
				m.err = err
			} else {
				m.status = "renamed"
			}
		}
		m.mode = modeList
		m.input.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) handlePickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		m.input.Blur()
		m.status = ""
		return m, nil
	case "up", "ctrl+k", "ctrl+p":
		m.pickCur = clamp(m.pickCur-1, 0, lastIndex(len(m.pickHit)))
		return m, nil
	case "down", "ctrl+j", "ctrl+n":
		m.pickCur = clamp(m.pickCur+1, 0, lastIndex(len(m.pickHit)))
		return m, nil
	case "enter":
		if m.pickCur < 0 || m.pickCur >= len(m.pickHit) {
			return m, nil
		}
		m.newCWD = m.pickHit[m.pickCur]
		m.newIsProject = false
		m.mode = modeNewName
		m.input.SetValue("")
		m.input.Placeholder = "session name (optional)…"
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.recomputePick()
	return m, cmd
}

func countLive(all []claude.Session, ids map[string]bool) int {
	n := 0
	for _, s := range all {
		if ids[s.SessionID] {
			n++
		}
	}
	return n
}
