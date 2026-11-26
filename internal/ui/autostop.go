package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/unpark"
)

const (
	autoStopRetry    = time.Minute      // a stop round blocks another one for the same job this long
	autoStopMaxAge   = 5 * time.Second  // an older refresh waited behind a blocked UI
	autoStopGapSlack = 30 * time.Second // on top of the idle poll interval, before a pause counts as a gap
	autoStopTimeout  = 10 * time.Second // for one claude call (list or stop)
)

// Tests stub the `claude agents` call and the process scan.
var (
	liveSessions  = claude.List
	attachTargets = claude.AttachTargets
)

// errListFailed pauses auto-stop when `claude agents` fails: with no statuses
// cav cannot tell which session is busy.
var errListFailed = errors.New("claude cannot list the sessions")

// autoStopState is what auto-stop keeps between refreshes.
type autoStopState struct {
	workers   map[string]workerActivity // sessionId -> its live worker's activity
	attached  map[string]bool           // job ids with a terminal attached; nil = unknown
	watchErr  error                     // why the last refresh cannot be trusted: auto-stop is paused
	hadWorker map[string]bool           // job ids with a live worker at the previous refresh
	wasInUse  map[string]bool           // job ids in use at the previous refresh
	lastInUse map[string]time.Time      // job id -> when it was last in use
	stopping  map[string]time.Time      // job id -> when a stop round started for it
	busy      bool                      // a stop round is running; the next waits for it
	lastWatch time.Time                 // when the previous refresh was taken
	floor     time.Time                 // start of cav's unbroken watch: idle time before it does not count
}

func newAutoStopState() autoStopState {
	return autoStopState{lastInUse: map[string]time.Time{}, stopping: map[string]time.Time{}}
}

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
}

// autoStoppedMsg reports how a round ended for its one target.
type autoStoppedMsg struct {
	target  stopTarget
	outcome int   // what the check right before the stop found
	err     error // the stop was due and failed
}

// What the check right before a stop finds.
const (
	stillIdle   = iota // unchanged since the refresh: stop it
	alreadyDown        // another cav stopped it meanwhile
	inUse              // busy, attached, changed or unknown: leave it running
)

// idleWorker reports whether s has a worker known to be idle. Any other status
// (busy, or one cav does not know) is left alone.
func idleWorker(s claude.Session) bool { return s.Status == "idle" }

// workerActivities returns the activity of each live worker whose job record
// tells its last update and its background work. Without an entry, no stop.
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

// targetsJob reports whether an attach target points at a job, by id or name.
// The match is loose on purpose: it can only keep a session alive.
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

// watchedJobs returns the jobs with a terminal attached, or why this refresh
// cannot tell. Such a refresh is one cav did not watch.
func watchedJobs(listErr error, sessions []claude.Session, roster claude.Roster) (map[string]bool, error) {
	if listErr != nil {
		return nil, errListFailed
	}
	targets, err := attachTargets()
	if err != nil {
		return nil, err
	}
	return attachedJobs(targets, sessions, roster), nil
}

// keepWatch reports whether auto-stop may act on the refresh taken at `at`
// and finished at `done`. After a gap in cav's watch every window starts again.
func (m *Model) keepWatch(at, done, now time.Time) bool {
	a := &m.auto
	gap := at.Sub(a.lastWatch)
	a.lastWatch = at
	// Staleness is the wait after the refresh was done, not the refresh's own
	// length: a slow machine makes refreshes long, and must not block auto-stop.
	if a.attached == nil || now.Sub(done) > autoStopMaxAge+m.cfg.List.MinRefresh {
		a.floor = now // cannot tell, or the refresh waited behind a blocked UI
		return false
	}
	if gap > m.cfg.List.IdleRefresh+autoStopGapSlack {
		a.floor = at // cav was suspended, or the laptop slept
	}
	return true
}

// steppedOut is the message every way out of a session ends in. It names the
// job that was left, so its idle window starts again.
func steppedOut(note, jobID string) actionMsg {
	return actionMsg{note: note, leftJob: jobID}
}

// markInUse records that a job was in use at t. A stamp only moves forward.
func (m *Model) markInUse(jobID string, t time.Time) {
	if jobID != "" && t.After(m.auto.lastInUse[jobID]) {
		m.auto.lastInUse[jobID] = t
	}
}

// stampInUse stamps each job in use now or at the previous refresh: what just
// ended did so between the two polls, so the window starts at the later one.
func (m *Model) stampInUse(now time.Time) {
	a := &m.auto
	inUse, workers := map[string]bool{}, map[string]bool{}
	for jid := range a.attached {
		inUse[jid] = true
	}
	for _, s := range m.all {
		if !hasLiveWorker(s) {
			continue
		}
		jid := m.roster[s.SessionID]
		workers[jid] = true
		// A worker the previous refresh did not show was not watched then.
		if !idleWorker(s) || a.workers[s.SessionID].inFlight > 0 || !a.hadWorker[jid] {
			inUse[jid] = true
		}
	}
	for _, jobs := range []map[string]bool{a.wasInUse, inUse} {
		for jid := range jobs {
			m.markInUse(jid, now)
		}
	}
	a.wasInUse, a.hadWorker = inUse, workers
}

// idleSince is the latest moment the session may have been in use.
func (m *Model) idleSince(jobID string, w workerActivity) time.Time {
	since := w.last
	for _, t := range []time.Time{m.auto.lastInUse[jobID], m.auto.floor} {
		if t.After(since) {
			since = t
		}
	}
	return since
}

// autoStopTargets returns the sessions whose worker should be stopped, judged
// at the time of the refresh.
func (m *Model) autoStopTargets(at time.Time) []stopTarget {
	a := &m.auto
	after := m.cfg.AutoStop.After
	if after <= 0 || a.attached == nil {
		return nil // off, or cav cannot tell who is attached
	}
	var out []stopTarget
	for _, s := range m.all {
		jid := m.roster[s.SessionID]
		w, known := a.workers[s.SessionID]
		if !idleWorker(s) || !known || w.inFlight > 0 || a.attached[jid] || m.justStopped[s.SessionID] {
			continue
		}
		// Only a job id claude itself lists for the worker is safe to stop: one
		// guessed from the on-disk records can belong to another session.
		if s.JobID == "" || s.JobID != jid {
			continue
		}
		if _, stopping := a.stopping[jid]; stopping {
			continue
		}
		if at.Sub(m.idleSince(jid, w)) >= after {
			out = append(out, stopTarget{sessionID: s.SessionID, jobID: jid, name: s.Name,
				updated: w.updated, startedAt: s.StartedAt})
		}
	}
	return out
}

// autoStopIdle starts a stop round for one idle session. One per refresh, and
// none while a round runs, so every stop is picked from fresh data.
func (m *Model) autoStopIdle(at, done, now time.Time) tea.Cmd {
	a := &m.auto
	if m.cfg.AutoStop.After <= 0 || !m.keepWatch(at, done, now) {
		return nil
	}
	m.stampInUse(now)
	for jid, started := range a.stopping {
		if now.Sub(started) > autoStopRetry {
			delete(a.stopping, jid)
		}
	}
	targets := m.autoStopTargets(at)
	if a.busy || len(targets) == 0 {
		return nil
	}
	a.stopping[targets[0].jobID] = now
	a.busy = true
	return autoStopCmd(targets[0])
}

// autoStopCmd checks the target once more and stops its worker.
func autoStopCmd(t stopTarget) tea.Cmd {
	return func() tea.Msg {
		msg := autoStoppedMsg{target: t, outcome: recheck(t)}
		if msg.outcome == stillIdle {
			msg.err = stopInPlace(t)
		}
		return msg
	}
}

// stopInPlace stops the worker the way z does. The keep-here mark is saved
// first: without it the stopped session would jump to the stopped window.
func stopInPlace(t stopTarget) error {
	if err := unpark.Load().Add(t.sessionID); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), autoStopTimeout)
	defer cancel()
	return claude.Stop(ctx, t.jobID)
}

// autoStopDone handles the end of a round.
func (m *Model) autoStopDone(msg autoStoppedMsg, now time.Time) {
	m.auto.busy = false
	t := msg.target
	switch {
	case msg.outcome == inUse:
		m.markInUse(t.jobID, now) // only this check saw the use: no refresh will
	case msg.outcome == stillIdle && msg.err != nil:
		m.status = "auto-stop failed: " + msg.err.Error()
	case msg.outcome == stillIdle:
		m.unparked.Reload() // the round saved the mark through its own store
		m.status = "auto-stopped " + m.displayName(claude.Session{SessionID: t.sessionID, Name: t.name})
	}
}

// recheck looks at a target again just before its stop: the refresh is a few
// seconds old. Cheap checks first, the attach scan last so it is the freshest.
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

// listedIdle asks claude again whether the target still has the same idle
// worker under the same job. A job record can lag behind a turn that just started.
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

// attachedNow scans the processes again. A scan that cannot tell counts as attached.
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

// autoStopHint says on a row why an idle worker is kept: its record counts
// background tasks. The count can be left over, and cav cannot tell.
func (m *Model) autoStopHint(s claude.Session) string {
	n := m.auto.workers[s.SessionID].inFlight
	switch {
	case m.cfg.AutoStop.After <= 0 || !idleWorker(s) || n == 0:
		return ""
	case n == 1:
		return "  1 task"
	}
	return fmt.Sprintf("  %d tasks", n)
}

// hintWidth is the width of the longest auto-stop hint among the visible rows.
func (m *Model) hintWidth() int {
	w := 0
	for i := range m.view {
		w = max(w, len(m.autoStopHint(m.view[i])))
	}
	return w
}
