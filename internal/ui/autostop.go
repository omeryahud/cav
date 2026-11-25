package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
)

// autoStopRetry is how long an issued stop blocks another one for the same job,
// so a stop still in progress is not repeated on every refresh.
const autoStopRetry = time.Minute

// workerActivity is what auto-stop needs to know about a session's live worker.
type workerActivity struct {
	last     time.Time // latest of the job record's last update and the worker's start
	inFlight int       // background work still running or queued
}

type stopTarget struct{ sessionID, jobID string }

// autoStoppedMsg reports a finished auto-stop round.
type autoStoppedMsg struct {
	stopped int
	err     error // the first failure, if any
}

// idleWorker reports whether s has a running worker that is not executing a turn.
func idleWorker(s claude.Session) bool { return hasLiveWorker(s) && s.Status != "busy" }

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
		out[s.SessionID] = workerActivity{last: last, inFlight: rec.InFlight}
	}
	return out
}

// autoStopTargets returns the sessions whose worker should be stopped now: idle
// for autoStop.afterMs, with no terminal attached and no background work.
func (m *Model) autoStopTargets(now time.Time) []stopTarget {
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
		last := w.last
		if seen := m.lastAttached[jid]; seen.After(last) {
			last = seen // stepping out of a session counts as activity
		}
		if now.Sub(last) >= after {
			out = append(out, stopTarget{sessionID: s.SessionID, jobID: jid})
		}
	}
	return out
}

// autoStopIdle stops the workers autoStopTargets picks, the way z does: each
// session stays in the main pane as stopped, and ↵ respawns it.
func (m *Model) autoStopIdle(now time.Time) tea.Cmd {
	for jid := range m.attached {
		m.lastAttached[jid] = now
	}
	for jid, at := range m.autoStopping {
		if now.Sub(at) > autoStopRetry {
			delete(m.autoStopping, jid)
		}
	}
	targets := m.autoStopTargets(now)
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

// autoStopCmd stops each target's worker. A job another cav already stopped is
// skipped, so two cavs racing for the same session do not report a failure.
func autoStopCmd(targets []stopTarget) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var msg autoStoppedMsg
		for _, t := range targets {
			if claude.JobState(t.jobID) == "stopped" {
				continue
			}
			if err := claude.Stop(ctx, t.jobID); err != nil {
				if msg.err == nil {
					msg.err = err
				}
				continue
			}
			msg.stopped++
		}
		return msg
	}
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
