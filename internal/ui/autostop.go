package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
)

const (
	// autoStopRetry is how long an issued stop blocks another one for the same
	// job, so a stop still in progress is not repeated on every refresh.
	autoStopRetry = time.Minute
	// autoStopMaxAge is how old a refresh may be when it is handled. An older
	// one waited while the UI was blocked (a full-screen attach, a pager).
	autoStopMaxAge = 5 * time.Second
	// autoStopGapSlack is added to the idle poll interval. A longer pause
	// between two refreshes means cav was not polling (suspended, or asleep).
	autoStopGapSlack = 30 * time.Second
	autoStopTimeout  = 10 * time.Second // for one claude call (list or stop)
)

// liveSessions and attachTargets are package variables so tests can stub the
// `claude agents` call and the process scan.
var (
	liveSessions  = claude.List
	attachTargets = claude.AttachTargets
)

// workerActivity is what auto-stop needs to know about a session's live worker.
type workerActivity struct {
	updated  time.Time // the job record's last update
	last     time.Time // latest of updated and the worker's start
	inFlight int       // background work still running
}

// stopTarget is a session picked for a stop, with what the refresh saw of it.
type stopTarget struct {
	sessionID, jobID, name string
	updated                time.Time // the job record's last update
	startedAt              int64     // the worker's start
	marked                 bool      // this round added the keep-here mark
}

// autoStoppedMsg reports how a round ended for its one target.
type autoStoppedMsg struct {
	target  stopTarget
	outcome int   // stillIdle (a stop was issued), alreadyDown or inUse
	err     error // the stop failed
}

// What the check right before a stop finds.
const (
	stillIdle   = iota // unchanged since the refresh: stop it
	alreadyDown        // another cav stopped it meanwhile
	inUse              // busy, attached, changed or unknown: leave it running
)

// idleWorker reports whether s has a running worker that is known to be idle.
// Any other status (busy, or one cav does not know) is left alone.
func idleWorker(s claude.Session) bool { return s.Status == "idle" }

// workerActivities returns, by session id, the activity of every session that
// has a live worker and a job record that tells when it was last updated and
// how much background work it runs. A session without an entry is never
// auto-stopped.
func workerActivities(sessions []claude.Session, roster claude.Roster, recs []claude.JobRecord) map[string]workerActivity {
	byJob := make(map[string]*claude.JobRecord, len(recs))
	for i := range recs {
		byJob[recs[i].JobID] = &recs[i]
	}
	out := map[string]workerActivity{}
	for _, s := range sessions {
		rec, ok := byJob[roster[s.SessionID]]
		if !hasLiveWorker(s) || !ok || rec.UpdatedAt.IsZero() || !rec.InFlightKnown {
			continue
		}
		last := rec.UpdatedAt
		if started := time.UnixMilli(s.StartedAt); started.After(last) {
			last = started // a wake counts as activity
		}
		out[s.SessionID] = workerActivity{updated: rec.UpdatedAt, last: last, inFlight: rec.InFlight}
	}
	return out
}

// targetsJob reports whether one attach target points at a job: by the start
// of its job or session id, or by part of its session's name (`claude attach`
// takes an id or a name). A loose match is fine: it can only keep a session
// alive.
func targetsJob(target, jobID, sessionID, name string) bool {
	return strings.HasPrefix(jobID, target) || strings.HasPrefix(sessionID, target) ||
		strings.Contains(strings.ToLower(name), target)
}

// attachedJobs returns the ids of the jobs the attach targets point at.
func attachedJobs(targets []string, sessions []claude.Session, roster claude.Roster) map[string]bool {
	out := map[string]bool{}
	for _, s := range sessions {
		jid := roster[s.SessionID]
		for _, t := range targets {
			if jid != "" && targetsJob(t, jid, s.SessionID, s.Name) {
				out[jid] = true
			}
		}
	}
	return out
}

// keepWatch records the refresh taken at `at` and reports whether auto-stop may
// act on it. cav knows who was attached only while it was polling. After a gap
// (a refresh that waited behind a full-screen attach, a failed process scan, a
// suspended cav, a laptop sleep) every session gets a full idle window again.
func (m *Model) keepWatch(at, now time.Time) bool {
	gap := at.Sub(m.lastWatch)
	m.lastWatch = at
	if m.attached == nil || now.Sub(at) > autoStopMaxAge+m.cfg.List.MinRefresh {
		m.autoStopFloor = now
		return false
	}
	if gap > m.cfg.List.IdleRefresh+autoStopGapSlack {
		m.autoStopFloor = at
	}
	return true
}

// markInUse records that a job was in use at t. A stamp only moves forward.
func (m *Model) markInUse(jobID string, t time.Time) {
	if jobID != "" && t.After(m.lastInUse[jobID]) {
		m.lastInUse[jobID] = t
	}
}

// stampInUse notes every job this refresh shows in use: a terminal is
// attached, its worker is not idle, or it runs background work. A job in use
// now or at the previous refresh was in use until about now, because what just
// ended did so somewhere between the two polls. So the idle window starts at
// the first poll that sees the job free, never earlier than the real moment.
func (m *Model) stampInUse(now time.Time) {
	inUse := map[string]bool{}
	for jid := range m.attached {
		inUse[jid] = true
	}
	for _, s := range m.all {
		if hasLiveWorker(s) && (!idleWorker(s) || m.workers[s.SessionID].inFlight > 0) {
			inUse[m.roster[s.SessionID]] = true
		}
	}
	for _, jobs := range []map[string]bool{m.wasInUse, inUse} {
		for jid := range jobs {
			m.markInUse(jid, now)
		}
	}
	m.wasInUse = inUse
}

// idleSince is the latest moment the session may have been in use: its own
// activity, the last time cav saw it in use, or the start of cav's unbroken
// watch.
func (m *Model) idleSince(jobID string, w workerActivity) time.Time {
	since := w.last
	for _, t := range []time.Time{m.lastInUse[jobID], m.autoStopFloor} {
		if t.After(since) {
			since = t
		}
	}
	return since
}

// autoStopTargets returns the sessions whose worker should be stopped, judged
// at the time of the refresh: idle for autoStop.afterMs, with no terminal
// attached and no background work.
func (m *Model) autoStopTargets(at time.Time) []stopTarget {
	after := m.cfg.AutoStop.After
	if after <= 0 || m.attached == nil {
		return nil // off, or we could not tell who is attached
	}
	var out []stopTarget
	for _, s := range m.all {
		jid := m.roster[s.SessionID]
		w, known := m.workers[s.SessionID]
		if !idleWorker(s) || !known || w.inFlight > 0 || m.attached[jid] || m.justStopped[s.SessionID] {
			continue
		}
		// Stop a job only when claude itself names it as this worker's job. A
		// job id guessed from the on-disk records can belong to another session.
		if s.JobID == "" || s.JobID != jid {
			continue
		}
		if _, stopping := m.autoStopping[jid]; stopping {
			continue
		}
		if at.Sub(m.idleSince(jid, w)) >= after {
			out = append(out, stopTarget{sessionID: s.SessionID, jobID: jid, name: s.Name,
				updated: w.updated, startedAt: s.StartedAt})
		}
	}
	return out
}

// autoStopIdle stops one worker autoStopTargets picks, the way z does: the
// session stays in the main pane as stopped, and ↵ respawns it. It stops one
// session per refresh, and none while a stop is still running, so every stop
// is picked from fresh data. at is when the refresh was taken and now is when
// it is handled.
func (m *Model) autoStopIdle(at, now time.Time) tea.Cmd {
	if m.cfg.AutoStop.After <= 0 || !m.keepWatch(at, now) {
		return nil
	}
	m.stampInUse(now)
	for jid, issued := range m.autoStopping {
		if now.Sub(issued) > autoStopRetry {
			delete(m.autoStopping, jid)
		}
	}
	targets := m.autoStopTargets(at)
	if m.autoStopBusy || len(targets) == 0 {
		return nil
	}
	t := targets[0]
	added, err := m.unparked.Mark(t.sessionID)
	if err != nil {
		m.err = err
		return nil
	}
	t.marked = added
	m.autoStopping[t.jobID] = now
	m.autoStopBusy = true
	return autoStopCmd(t)
}

// autoStopCmd checks the target once more and stops its worker.
func autoStopCmd(t stopTarget) tea.Cmd {
	return func() tea.Msg {
		msg := autoStoppedMsg{target: t, outcome: recheck(t)}
		if msg.outcome == stillIdle {
			ctx, cancel := context.WithTimeout(context.Background(), autoStopTimeout)
			defer cancel()
			msg.err = claude.Stop(ctx, t.jobID)
		}
		return msg
	}
}

// autoStopDone handles the end of a round. A target found in use keeps
// running, so the mark this round gave it must go, but only once a later
// refresh still shows its worker (see settleUnmarks). A failed stop keeps its
// mark: the stop may still land.
func (m *Model) autoStopDone(msg autoStoppedMsg, now time.Time) {
	m.autoStopBusy = false
	switch {
	case msg.outcome == inUse && msg.target.marked:
		m.unmarkPending[msg.target.sessionID] = now
	case msg.outcome == stillIdle && msg.err != nil:
		m.status = "auto-stop failed: " + msg.err.Error()
	case msg.outcome == stillIdle:
		t := msg.target
		m.status = "auto-stopped " + m.displayName(claude.Session{SessionID: t.sessionID, Name: t.name})
	}
}

// settleUnmarks drops the keep-here mark of each target a round left running,
// once a refresh taken after that round still shows its worker. A session that
// lost its worker meanwhile (x, z or another cav stopped it) keeps the mark,
// which is what keeps it in the main pane.
func (m *Model) settleUnmarks(at time.Time) {
	for sid, since := range m.unmarkPending {
		if !at.After(since) {
			continue
		}
		delete(m.unmarkPending, sid)
		if !m.hasWorker(sid) {
			continue
		}
		if err := m.unparked.Remove(sid); err != nil {
			m.err = err
		}
	}
}

func (m *Model) hasWorker(sessionID string) bool {
	for _, s := range m.all {
		if s.SessionID == sessionID {
			return hasLiveWorker(s)
		}
	}
	return false
}

// recheck looks at a target again just before its stop, because the refresh
// that picked it is a few seconds old and a job record can lag behind a turn
// that just started. The record must be unchanged, claude must still list the
// same worker as idle, and no terminal may be attached. Anything that cannot be
// read counts as in use. The attach scan runs last, so it is the freshest.
func recheck(t stopTarget) int {
	rec, ok := claude.ReadJob(t.jobID)
	switch {
	case ok && rec.State == "stopped":
		return alreadyDown
	case !ok || !rec.InFlightKnown || rec.InFlight > 0 || !rec.UpdatedAt.Equal(t.updated),
		!listedIdle(t) || attachedNow(t):
		return inUse
	}
	return stillIdle
}

// listedIdle asks claude for the live sessions again and reports whether the
// target still has the same worker and it is idle.
func listedIdle(t stopTarget) bool {
	ctx, cancel := context.WithTimeout(context.Background(), autoStopTimeout)
	defer cancel()
	sessions, err := liveSessions(ctx)
	if err != nil {
		return false
	}
	for _, s := range sessions {
		if s.SessionID == t.sessionID {
			return idleWorker(s) && s.StartedAt == t.startedAt && s.JobID == t.jobID
		}
	}
	return false
}

// attachedNow scans the processes again. A scan that cannot tell (it failed,
// or a native agents view is open) counts as attached.
func attachedNow(t stopTarget) bool {
	targets, err := attachTargets()
	if err != nil {
		return true
	}
	for _, target := range targets {
		if targetsJob(target, t.jobID, t.sessionID, t.name) {
			return true
		}
	}
	return false
}

// autoStopHint says, on a session's row, why auto-stop leaves an idle worker
// running: its job record counts background tasks. The count can be left over
// from work that has ended, and cav cannot tell, so it never stops on it.
func (m *Model) autoStopHint(s claude.Session) string {
	if n := m.workers[s.SessionID].inFlight; m.cfg.AutoStop.After > 0 && idleWorker(s) && n > 0 {
		return fmt.Sprintf("  %d tasks", n)
	}
	return ""
}
