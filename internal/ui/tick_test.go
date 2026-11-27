package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
)

// previewModel is a model with the preview pane open on its first session.
func previewModel(t *testing.T) (m *Model, sid string) {
	t.Helper()
	m = openModel(t, "aaa")
	m.previewOn, m.width, m.height = true, 200, 40
	m.act = newActivity()
	return m, m.all[0].SessionID
}

// A refresh comes only when a file changes, so the tick is what moves the
// screen in between. It must run only while something needs it.
func TestTheTickRunsOnlyWhileTheScreenNeedsIt(t *testing.T) {
	quiet := openModel(t, "aaa")
	quiet.feedRefresh()
	if quiet.ticking {
		t.Fatal("no note, no preview, no young session: there must be no tick")
	}

	for name, setup := range map[string]func(m *Model){
		"a footer note":     func(m *Model) { m.status = "renamed" },
		"the preview":       func(m *Model) { m.previewOn, m.width, m.height = true, 200, 40 },
		"an age in seconds": func(m *Model) { m.all[0].StartedAt = time.Now().UnixMilli() },
	} {
		m := openModel(t, "aaa")
		setup(m)
		m.feedRefresh()
		if !m.ticking {
			t.Errorf("%s: the tick must run", name)
		}
	}
}

// An age counts in seconds for one minute. After that the tick must stop:
// nothing on the screen moves on time any more.
func TestTheTickStopsOnceEveryAgeIsPastAMinute(t *testing.T) {
	m := openModel(t, "aaa")
	m.all[0].StartedAt = time.Now().Add(-59 * time.Second).UnixMilli()
	m.feedRefresh()
	if !m.ticking {
		t.Fatal("an age of 59s counts in seconds: the tick must run")
	}

	m.view[0].StartedAt = time.Now().Add(-61 * time.Second).UnixMilli()
	if _, cmd := m.Update(tickMsg{}); cmd != nil || m.ticking {
		t.Error("every age is past a minute: the tick must stop")
	}
}

// Two ticks at once would double the preview loads.
func TestOnlyOneTickRunsAtATime(t *testing.T) {
	m := openModel(t, "aaa")
	m.status = "renamed"
	nothing := errMsg{} // a message that starts no command of its own

	if _, cmd := m.Update(nothing); cmd == nil || !m.ticking {
		t.Fatal("a note shows: the first message must start the tick")
	}
	if _, cmd := m.Update(nothing); cmd != nil {
		t.Error("a tick already runs: no second one")
	}
	if _, cmd := m.Update(tickMsg{}); cmd == nil || !m.ticking {
		t.Error("the note still shows: the tick must start again when it fires")
	}
}

func TestTheTickExpiresAFooterNote(t *testing.T) {
	m := openModel(t, "aaa")
	m.cfg.List.StatusTTL = time.Millisecond
	m.status = "renamed"

	m.Update(tickMsg{}) // the note is seen for the first time
	if m.status == "" {
		t.Fatal("a note must stay for its time")
	}
	time.Sleep(5 * time.Millisecond)
	if _, cmd := m.Update(tickMsg{}); m.status != "" || cmd != nil || m.ticking {
		t.Errorf("after its time: status = %q, tick running = %v; want the note gone and no tick", m.status, m.ticking)
	}
}

// The preview of a live session starts `claude logs`. Loads must not pile up
// on a slow machine, and a user who is away must not pay for one every 2s.
func TestPreviewReloadWaitsForARunningLoadAndBacksOffWhenIdle(t *testing.T) {
	m, sid := previewModel(t)
	m.feedRefresh()
	if !m.prevReq[sid] {
		t.Fatal("the first refresh must load the preview")
	}

	m.prevAt = time.Now().Add(-time.Hour)
	if m.reloadPreview() != nil {
		t.Error("a load is still running: no second one")
	}
	m.Update(previewMsg{id: sid, text: "screen"})
	if m.reloadPreview() == nil || !m.prevReq[sid] {
		t.Error("the load ended and the interval passed: the preview must reload")
	}
	m.Update(previewMsg{id: sid, text: "screen"})

	m.act.last.Store(time.Now().Add(-10 * time.Minute).UnixMilli()) // the user went away
	m.prevAt = time.Now().Add(-m.cfg.List.IdleRefresh / 2)
	if m.reloadPreview() != nil {
		t.Error("the user is away: the reload must wait for the idle interval")
	}
	m.prevAt = time.Now().Add(-m.cfg.List.IdleRefresh - time.Second)
	if m.reloadPreview() == nil {
		t.Error("the idle interval passed: the preview must reload")
	}
}

// list.idleAfterMs 0 turns the idle backoff off, for the preview too.
func TestPreviewNeverBacksOffWhenTheIdleBackoffIsOff(t *testing.T) {
	m, _ := previewModel(t)
	m.act.last.Store(time.Now().Add(-10 * time.Minute).UnixMilli())
	if got := m.previewInterval(); got != m.cfg.List.IdleRefresh {
		t.Fatalf("the user is away: interval = %v, want the idle interval", got)
	}
	m.cfg.List.IdleAfter = 0
	if got := m.previewInterval(); got != m.cfg.Preview.Refresh {
		t.Errorf("idle backoff off: interval = %v, want preview.refreshMs", got)
	}
}

func TestRAsksTheLoopForAFullRefresh(t *testing.T) {
	m := openModel(t, "aaa")
	m.act = newActivity()
	m.status = "old note"

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	select {
	case <-m.act.refresh:
	default:
		t.Error("r must ask the refresh loop for a refresh")
	}
	if m.status != "" {
		t.Errorf("r must clear the note, got %q", m.status)
	}
}

// `claude --bg` can return after the refresh that already lists the new
// session. No later refresh is due then, so the cursor must move at once.
func TestANewSessionAlreadyListedIsHighlightedAtOnce(t *testing.T) {
	m := openModel(t, "aaa", "fresh")
	fresh := m.all[1].SessionID
	listed := refreshResult{sessions: m.all, roster: claude.Roster{fresh: "job1"},
		states: map[string]string{}, live: map[string]bool{fresh: true}}

	m.Update(listed)
	m.cursor = 0
	m.Update(createdMsg{jobID: "job1", label: "fresh"})
	if cur := m.current(); cur == nil || cur.SessionID != fresh || m.selectJobID != "" {
		t.Errorf("cursor on %+v, selectJobID %q; want the new session, with nothing left to select", cur, m.selectJobID)
	}

	// `cav -n -a`: the same moment opens the session.
	t.Setenv("HOME", t.TempDir()) // opening a session may write the trust flag
	t.Setenv("TMUX", "")
	m = openModel(t, "aaa", "fresh")
	m.attachNew = true
	m.Update(listed)
	if _, cmd := m.Update(createdMsg{jobID: "job1", label: "fresh"}); cmd == nil || m.attachNew {
		t.Errorf("-a: command = %v, attachNew = %v; want an attach, used up", cmd != nil, m.attachNew)
	}
}

// A new job is listed from its job record before its worker runs. Opening it
// then would respawn a worker that is starting, so `cav -n -a` must wait.
func TestAttachNewWaitsForTheWorker(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // opening a session may write the trust flag
	t.Setenv("TMUX", "")
	m := openModel(t, "aaa", "fresh")
	fresh := m.all[1].SessionID
	listing := func(live bool) refreshResult {
		return refreshResult{sessions: m.all, roster: claude.Roster{fresh: "job1"},
			states: map[string]string{}, live: map[string]bool{fresh: live}}
	}
	m.attachNew = true
	m.Update(listing(false))

	m.Update(createdMsg{jobID: "job1", label: "fresh"})
	if !m.attachNew || m.selectJobID != "job1" {
		t.Fatalf("no worker yet: attachNew = %v, selectJobID = %q; want both kept for the next refresh", m.attachNew, m.selectJobID)
	}
	m.Update(listing(true))
	if cur := m.current(); m.attachNew || m.selectJobID != "" || cur == nil || cur.SessionID != fresh {
		t.Errorf("the worker runs: attachNew = %v, selectJobID = %q, cursor on %+v; want the session opened", m.attachNew, m.selectJobID, cur)
	}
}

// A fork can be listed under its parent's name before `claude --bg` returns.
// It must be hidden from then on, and shown once it carries the typed name.
func TestAForkAlreadyListedIsHiddenUntilItHasItsName(t *testing.T) {
	m := openModel(t, "parent", "parent")
	child := m.all[1].SessionID
	listed := refreshResult{sessions: m.all, roster: claude.Roster{child: "job2"},
		states: map[string]string{}, live: map[string]bool{child: true}}
	m.Update(listed)
	if len(m.view) != 2 {
		t.Fatalf("view = %d rows, want the parent and the fork under its name", len(m.view))
	}

	m.Update(forkedMsg{childJobID: "job2", parentSID: m.all[0].SessionID, cloneName: "child"})
	if len(m.view) != 1 || m.selectJobID != "job2" {
		t.Fatalf("view = %d rows, selectJobID %q; want the fork hidden and still waiting", len(m.view), m.selectJobID)
	}

	m.all[1].Name = "child"
	m.Update(refreshResult{sessions: m.all, roster: claude.Roster{child: "job2"},
		states: map[string]string{}, live: map[string]bool{child: true}})
	if cur := m.current(); len(m.view) != 2 || cur == nil || cur.SessionID != child {
		t.Errorf("view = %d rows, cursor on %+v; want the fork shown and highlighted", len(m.view), cur)
	}
}
