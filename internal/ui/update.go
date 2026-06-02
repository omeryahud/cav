package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
)

// Update is the bubbletea message handler.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width != m.width { // re-render previews wrapped to the new width
			m.prevCache = map[string]string{}
			m.prevReq = map[string]bool{}
		}
		m.width, m.height = msg.Width, msg.Height
		return m, m.ensurePreview()

	case tickMsg:
		return m, tea.Batch(refreshCmd, tickCmd())

	case refreshResult:
		m.all = applyOrder(msg.sessions, m.order.IDs())
		m.roster = msg.roster
		m.states = msg.states
		m.last = msg.last
		m.recompute()
		// Keep the selected session's preview fresh: reload it each tick and
		// overwrite the cache silently (no "loading…" flicker).
		if m.showPreview() {
			if s := m.current(); s != nil {
				m.prevReq[s.SessionID] = true
				return m, previewCmd(s.SessionID, m.previewWidth())
			}
		}
		return m, nil

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
		return m, refreshCmd

	case searchDoneMsg:
		m.matchIDs = msg.ids
		m.status = fmt.Sprintf("%d live session(s) mention %q  (esc clears)", countLive(m.all, msg.ids), msg.q)
		m.recompute()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	// Non-key messages while typing (e.g. cursor blink) go to the input.
	if m.mode == modeFilter || m.mode == modeSearch || m.mode == modeNew {
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
	case "J":
		m.reorder(1)
	case "K":
		m.reorder(-1)
	case "p":
		m.previewOn = !m.previewOn
	case "o":
		m.group = !m.group
		m.recompute()
	case "r":
		m.status = "refreshing…"
		return m, refreshCmd
	case "/":
		m.mode = modeFilter
		m.input.SetValue(m.filter)
		m.input.Placeholder = "filter name / cwd / status…"
		m.input.CursorEnd()
		return m, m.input.Focus()
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
	case "n":
		m.mode = modePickDir
		m.pickAll, m.pickHit, m.pickCur = nil, nil, 0
		m.input.SetValue("")
		m.input.Placeholder = "fuzzy-find a directory…"
		m.status = "loading directories…"
		return m, tea.Batch(m.input.Focus(), dirsCmd())
	case "d":
		s := m.current()
		if s == nil {
			break
		}
		if !m.canAttach(s) {
			m.status = "can't stop " + m.displayName(*s) + " — " + notAttachableReason(*s)
			break
		}
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
		s := m.current()
		if s == nil {
			break
		}
		if !m.canAttach(s) {
			m.status = "can't open " + m.displayName(*s) + " — " + notAttachableReason(*s)
			break
		}
		id, label := m.jobID(s), m.displayName(*s) // attach by job id, not session id
		// Hand the current terminal to `claude attach`. When the user leaves
		// the conversation, ExecProcess resumes cav in place with all state
		// intact. The attach exit code is ignored — leaving via Ctrl+Z/Ctrl+C
		// is a normal, non-error exit.
		return m, tea.ExecProcess(claude.AttachCmd(id), func(error) tea.Msg {
			return actionMsg{note: "← back from " + label}
		})
	case "esc":
		m.filter, m.matchIDs, m.status = "", nil, ""
		m.recompute()
	}
	return m, m.ensurePreview()
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
		m.mode = modeList
		m.input.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.filter = m.input.Value()
	m.recompute()
	return m, cmd
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
		cwd := m.newCWD
		m.mode = modeList
		m.input.Blur()
		m.status = "creating session…"
		return m, createCmd(cwd, prompt)
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
		m.status = "stopping " + m.displayName(*s) + "…"
		return m, stopCmd(m.jobID(s))
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
		m.mode = modeNew
		m.input.SetValue("")
		m.input.Placeholder = "initial prompt (optional)…"
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
