package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/unpark"
)

const testAfter = 5 * time.Minute

// autoStopModel returns a model with auto-stop on and one idle session that
// qualifies for a stop at `now`: sid "a-sid", job "aaaa0001", idle 10 minutes.
func autoStopModel(t *testing.T, now time.Time) *Model {
	t.Helper()
	m := openModel(t, "a")
	m.cfg.AutoStop.After = testAfter
	m.all[0].Status = "idle"
	m.roster = claude.Roster{"a-sid": "aaaa0001"}
	m.workers = map[string]workerActivity{"a-sid": {last: now.Add(-10 * time.Minute)}}
	m.attached = map[string]bool{}
	m.lastAttached = map[string]time.Time{}
	m.autoStopping = map[string]time.Time{}
	return m
}

func TestAutoStopTargetsRules(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		edit func(m *Model)
		want int
	}{
		{"idle past the threshold", func(*Model) {}, 1},
		{"feature off", func(m *Model) { m.cfg.AutoStop.After = 0 }, 0},
		{"attached set unknown", func(m *Model) { m.attached = nil }, 0},
		{"busy", func(m *Model) { m.all[0].Status = "busy" }, 0},
		{"no worker", func(m *Model) { m.all[0].Status = "" }, 0},
		{"active under the threshold", func(m *Model) {
			m.workers["a-sid"] = workerActivity{last: now.Add(-testAfter + time.Second)}
		}, 0},
		{"exactly at the threshold", func(m *Model) {
			m.workers["a-sid"] = workerActivity{last: now.Add(-testAfter)}
		}, 1},
		{"background work in flight", func(m *Model) {
			m.workers["a-sid"] = workerActivity{last: now.Add(-time.Hour), inFlight: 1}
		}, 0},
		{"a terminal is attached", func(m *Model) { m.attached["aaaa0001"] = true }, 0},
		{"activity unknown", func(m *Model) { delete(m.workers, "a-sid") }, 0},
		{"a stop is already in progress", func(m *Model) { m.autoStopping["aaaa0001"] = now }, 0},
		{"stepped out a minute ago", func(m *Model) {
			m.lastAttached["aaaa0001"] = now.Add(-time.Minute)
		}, 0},
		{"stepped out long ago", func(m *Model) {
			m.lastAttached["aaaa0001"] = now.Add(-testAfter - time.Second)
		}, 1},
	} {
		m := autoStopModel(t, now)
		tc.edit(m)
		if got := len(m.autoStopTargets(now)); got != tc.want {
			t.Errorf("%s: got %d targets, want %d", tc.name, got, tc.want)
		}
	}
}

func TestAutoStopIdleStopsOnceAndKeepsTheSessionInTheMainPane(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)

	if cmd := m.autoStopIdle(now); cmd == nil {
		t.Fatal("an idle session past the threshold should be stopped")
	}
	if !m.unparked.Has("a-sid") {
		t.Error("the session should be marked to stay in the main pane, like z")
	}
	if cmd := m.autoStopIdle(now.Add(time.Second)); cmd != nil {
		t.Error("the same session must not be stopped again while its stop is in progress")
	}
	if cmd := m.autoStopIdle(now.Add(autoStopRetry + time.Second)); cmd == nil {
		t.Error("a session still live after the retry window should be stopped again")
	}
}

// The moment a session stops being attached must count as activity, or it would
// be stopped the instant the user steps out of it.
func TestAutoStopIdleGivesASteppedOutSessionAFreshWindow(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)
	m.attached["aaaa0001"] = true
	if cmd := m.autoStopIdle(now); cmd != nil {
		t.Fatal("an attached session must not be stopped")
	}

	m.attached = map[string]bool{} // the user stepped out
	if cmd := m.autoStopIdle(now.Add(time.Second)); cmd != nil {
		t.Error("a session must not be stopped right after the user steps out of it")
	}
	if cmd := m.autoStopIdle(now.Add(testAfter + time.Second)); cmd == nil {
		t.Error("it should be stopped once it has been left alone for the full window")
	}
}

func TestWorkerActivities(t *testing.T) {
	updated := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	woke := updated.Add(time.Hour)
	sessions := []claude.Session{
		{SessionID: "idle", Status: "idle", StartedAt: updated.Add(-time.Hour).UnixMilli()},
		{SessionID: "woken", Status: "idle", StartedAt: woke.UnixMilli()},
		{SessionID: "noworker", StartedAt: woke.UnixMilli()},
		{SessionID: "norecord", Status: "idle"},
		{SessionID: "nodate", Status: "idle"},
	}
	roster := claude.Roster{"idle": "j1", "woken": "j2", "noworker": "j3", "norecord": "j4", "nodate": "j5"}
	recs := []claude.JobRecord{
		{JobID: "j1", UpdatedAt: updated, InFlight: 2},
		{JobID: "j2", UpdatedAt: updated},
		{JobID: "j3", UpdatedAt: updated},
		{JobID: "j5"},
	}
	got := workerActivities(sessions, roster, recs)

	if w := got["idle"]; !w.last.Equal(updated) || w.inFlight != 2 {
		t.Errorf("idle = %+v, want the record's update time and 2 in flight", w)
	}
	if w := got["woken"]; !w.last.Equal(woke) {
		t.Errorf("woken last = %v, want the later worker start %v", w.last, woke)
	}
	for _, sid := range []string{"noworker", "norecord", "nodate"} {
		if _, ok := got[sid]; ok {
			t.Errorf("%s must have no activity entry, so it is never auto-stopped", sid)
		}
	}
}

// agents --json also lists sessions that have no process. z and Z must not
// count them, whatever the live set says.
func TestKillTargetsNeedARealWorker(t *testing.T) {
	m := openModel(t, "busy", "idle", "noprocess")
	m.all[0].Status, m.all[1].Status = "busy", "idle"
	m.live = map[string]bool{"a-sid": true, "b-sid": true, "c-sid": true}

	if got := len(m.killTargets("all")); got != 2 {
		t.Errorf("Z targets = %d, want the 2 sessions with a worker", got)
	}
	if got := m.killTargets("idle"); len(got) != 1 || got[0].SessionID != "b-sid" {
		t.Errorf("z targets = %+v, want only the idle worker", got)
	}
}

// fakeClaude installs a claude stand-in that logs its arguments, and fails a
// stop of job "bad00000". It returns the log path.
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n[ \"$2\" = bad00000 ] && exit 1\nexit 0\n"
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_BIN", bin)
	return log
}

func TestAutoStopCmdStopsEachTargetAndSkipsStoppedJobs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	jobDir := filepath.Join(home, ".claude", "jobs", "done0000")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), []byte(`{"state":"stopped"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	log := fakeClaude(t)

	msg := autoStopCmd([]stopTarget{{jobID: "aaaa0001"}, {jobID: "done0000"}, {jobID: "bad00000"}, {jobID: "bbbb0001"}})().(autoStoppedMsg)

	if msg.stopped != 2 || msg.err == nil {
		t.Errorf("msg = %+v, want 2 stopped and the failure reported", msg)
	}
	calls, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(calls)); got != "stop aaaa0001\nstop bad00000\nstop bbbb0001" {
		t.Errorf("claude calls = %q: a job another cav already stopped must be skipped", got)
	}
}

func TestAutoStopNote(t *testing.T) {
	for _, tc := range []struct {
		msg  autoStoppedMsg
		want string
	}{
		{autoStoppedMsg{stopped: 2}, "auto-stopped 2 idle session(s)"},
		{autoStoppedMsg{stopped: 1, err: os.ErrNotExist}, "auto-stopped 1 idle session(s)"},
		{autoStoppedMsg{err: os.ErrNotExist}, "auto-stop failed: " + os.ErrNotExist.Error()},
		{autoStoppedMsg{}, ""},
	} {
		if got := autoStopNote(tc.msg); got != tc.want {
			t.Errorf("autoStopNote(%+v) = %q, want %q", tc.msg, got, tc.want)
		}
	}
}

// The refresh handler is where auto-stop runs: a refresh that carries an idle
// session past the threshold must issue its stop.
func TestRefreshRunsAutoStop(t *testing.T) {
	m := autoStopModel(t, time.Now())
	m.Update(refreshResult{
		sessions: m.all,
		roster:   m.roster,
		states:   map[string]string{},
		live:     map[string]bool{"a-sid": true},
		workers:  m.workers,
		attached: map[string]bool{},
	})
	if _, issued := m.autoStopping["aaaa0001"]; !issued {
		t.Error("the refresh should have issued a stop for the idle session")
	}

	m.Update(autoStoppedMsg{stopped: 1})
	if m.status != "auto-stopped 1 idle session(s)" {
		t.Errorf("footer note = %q", m.status)
	}
}

// Another cav's auto-stop writes its keep-here mark to the shared file. This
// cav must pick it up on its next refresh, or it would move the session to the
// stopped window while the other cav keeps it in the main pane.
func TestRefreshPicksUpMarksFromAnotherCav(t *testing.T) {
	m := autoStopModel(t, time.Now())
	otherCav := unpark.Load()
	if err := otherCav.Add("a-sid"); err != nil {
		t.Fatal(err)
	}
	m.feedRefresh()
	if !m.unparked.Has("a-sid") {
		t.Error("a mark written by another cav should be loaded on refresh")
	}
}
