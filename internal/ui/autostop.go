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
	autoStopTimeout  = 10 * time.Second // for one `claude stop`
)

// attachTargets is a package variable so tests can stub the process scan.
var attachTargets = claude.AttachTargets

// workerActivity is what auto-stop needs to know about a session's live worker.
type workerActivity struct {
	updated  time.Time // the job record's last update
	last     time.Time // latest of updated and the worker's start
	inFlight int       // background work still running
}

type stopTarget struct {
	sessionID, jobID, name string
	updated                time.Time // the job record's last update when the target was picked
}

// autoStoppedMsg reports a finished auto-stop round.
type autoStoppedMsg struct {
	stopped int
	err     error // the first failure, if any
}

// idleWorker reports whether s has a running worker that is known to be idle.
// Any other status (busy, or one cav does not know) is left alone.
func idleWorker(s claude.Session) bool { return s.Status == "idle" }

// workerActivities returns, by session id, the activity of every session that
// has a live worker and a job record with a known update time.
func workerActivities(sessions []claude.Session, roster claude.Roster, recs []claude.JobRecord) map[string]workerActivity {
	byJob := make(map[string]claude.JobRecord, len(recs))
	for _, r := range recs {
		byJob[r.JobID] = r
	}
	out := map[string]workerActivity{}
	for _, s := range sessions {
		rec, ok := byJob[roster[s.SessionID]]
		if !hasLiveWorker(s) || !ok || rec.UpdatedAt.IsZero() {
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

// targetsJob reports whether one attach target points at a job: by its id, or
// by part of its session's name (`claude attach` takes both).
func targetsJob(target, jobID, name string) bool {
	return target == jobID || (name != "" && strings.Contains(strings.ToLower(name), target))
}

// attachedJobs returns the ids of the jobs the attach targets point at.
func attachedJobs(targets []string, sessions []claude.Session, roster claude.Roster) map[string]bool {
	out := map[string]bool{}
	for _, s := range sessions {
		jid := roster[s.SessionID]
		for _, t := range targets {
			if jid != "" && targetsJob(t, jid, s.Name) {
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
	if m.attached == nil || now.Sub(at) > autoStopMaxAge {
		m.autoStopFloor = now
		return false
	}
	if gap > m.cfg.List.IdleRefresh+autoStopGapSlack {
		m.autoStopFloor = at
	}
	return true
}

// idleSince is the latest moment the session may have been in use: its own
// activity, a terminal seen attached, or the start of cav's unbroken watch.
func (m *Model) idleSince(jobID string, w workerActivity) time.Time {
	since := w.last
	for _, t := range []time.Time{m.lastAttached[jobID], m.autoStopFloor} {
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
		if !idleWorker(s) || !known || w.inFlight > 0 || m.attached[jid] {
			continue
		}
		if _, stopping := m.autoStopping[jid]; stopping {
			continue
		}
		if at.Sub(m.idleSince(jid, w)) >= after {
			out = append(out, stopTarget{sessionID: s.SessionID, jobID: jid, name: s.Name, updated: w.updated})
		}
	}
	return out
}

// autoStopIdle stops the workers autoStopTargets picks, the way z does: each
// session stays in the main pane as stopped, and ↵ respawns it. at is when the
// refresh was taken and now is when it is handled.
func (m *Model) autoStopIdle(at, now time.Time) tea.Cmd {
	if m.cfg.AutoStop.After <= 0 || !m.keepWatch(at, now) {
		return nil
	}
	for jid := range m.attached {
		m.lastAttached[jid] = at
	}
	for jid, issued := range m.autoStopping {
		if now.Sub(issued) > autoStopRetry {
			delete(m.autoStopping, jid)
		}
	}
	targets := m.autoStopTargets(at)
	for i, t := range targets {
		if err := m.unparked.Add(t.sessionID); err != nil {
			m.err = err
			targets = targets[:i]
			break
		}
		m.autoStopping[t.jobID] = now
	}
	if len(targets) == 0 {
		return nil
	}
	return autoStopCmd(targets)
}

// autoStopCmd stops each target's worker, after checking it once more.
func autoStopCmd(targets []stopTarget) tea.Cmd {
	return func() tea.Msg {
		var msg autoStoppedMsg
		for _, t := range targets {
			if !stillIdle(t) {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), autoStopTimeout)
			err := claude.Stop(ctx, t.jobID)
			cancel()
			switch {
			case err == nil:
				msg.stopped++
			case msg.err == nil:
				msg.err = err
			}
		}
		return msg
	}
}

// stillIdle checks a target again just before its stop, because the refresh
// that picked it is a few seconds old. The job record must be unchanged since
// then (also true when another cav already stopped it) and no terminal may be
// attached. Anything that cannot be read counts as not idle.
func stillIdle(t stopTarget) bool {
	rec, ok := claude.ReadJob(t.jobID)
	if !ok || rec.State == "stopped" || rec.InFlight > 0 || !rec.UpdatedAt.Equal(t.updated) {
		return false
	}
	targets, ok := attachTargets()
	if !ok {
		return false
	}
	for _, target := range targets {
		if targetsJob(target, t.jobID, t.name) {
			return false
		}
	}
	return true
}

// autoStopNote is the footer note for a finished round ("" when nothing to say).
func autoStopNote(msg autoStoppedMsg) string {
	switch {
	case msg.stopped > 0:
		return fmt.Sprintf("auto-stopped %d idle session(s)", msg.stopped)
	case msg.err != nil:
		return "auto-stop failed: " + msg.err.Error()
	}
	return ""
}
