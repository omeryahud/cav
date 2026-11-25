package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/unpark"
)

const testAfter = 5 * time.Minute

// autoStopModel returns a model with auto-stop on and one idle session that
// qualifies for a stop at `now`: sid "a-sid", job "aaaa0001", idle 10 minutes,
// watched by this cav for an hour.
func autoStopModel(t *testing.T, now time.Time) *Model {
	t.Helper()
	m := openModel(t, "a")
	m.cfg.AutoStop.After = testAfter
	m.all[0].Status = "idle"
	m.roster = claude.Roster{"a-sid": "aaaa0001"}
	m.workers = map[string]workerActivity{"a-sid": {last: now.Add(-10 * time.Minute)}}
	m.attached = map[string]bool{}
	m.lastWatch = now.Add(-time.Second)
	m.autoStopFloor = now.Add(-time.Hour)
	return m
}

// stubAttachTargets replaces the process scan.
func stubAttachTargets(t *testing.T, targets []string, ok bool) {
	t.Helper()
	orig := attachTargets
	attachTargets = func() ([]string, error) {
		if !ok {
			return nil, errors.New("the scan failed")
		}
		return targets, nil
	}
	t.Cleanup(func() { attachTargets = orig })
}

// stubLiveSessions replaces the `claude agents` call.
func stubLiveSessions(t *testing.T, sessions []claude.Session, err error) {
	t.Helper()
	orig := liveSessions
	liveSessions = func(context.Context) ([]claude.Session, error) { return sessions, err }
	t.Cleanup(func() { liveSessions = orig })
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
		{"a status cav does not know", func(m *Model) { m.all[0].Status = "starting" }, 0},
		{"waiting for input counts as idle", func(m *Model) { m.states["a-sid"] = "blocked" }, 1},
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
		{"cav started watching a minute ago", func(m *Model) {
			m.autoStopFloor = now.Add(-time.Minute)
		}, 0},
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

	if cmd := m.autoStopIdle(now, now); cmd == nil {
		t.Fatal("an idle session past the threshold should be stopped")
	}
	if !m.unparked.Has("a-sid") {
		t.Error("the session should be marked to stay in the main pane, like z")
	}
	later := now.Add(time.Second)
	if cmd := m.autoStopIdle(later, later); cmd != nil {
		t.Error("the same session must not be stopped again while its stop is in progress")
	}
	later = now.Add(autoStopRetry + time.Second)
	m.lastWatch = later.Add(-time.Second)
	if cmd := m.autoStopIdle(later, later); cmd == nil {
		t.Error("a session still live after the retry window should be stopped again")
	}
}

// The moment a session stops being attached must count as activity, or it would
// be stopped the instant the user steps out of it.
func TestAutoStopIdleGivesASteppedOutSessionAFreshWindow(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)
	m.attached["aaaa0001"] = true
	if cmd := m.autoStopIdle(now, now); cmd != nil {
		t.Fatal("an attached session must not be stopped")
	}

	m.attached = map[string]bool{} // the user stepped out
	later := now.Add(time.Second)
	if cmd := m.autoStopIdle(later, later); cmd != nil {
		t.Error("a session must not be stopped right after the user steps out of it")
	}
	later = now.Add(testAfter + time.Second)
	m.lastWatch = later.Add(-time.Second)
	if cmd := m.autoStopIdle(later, later); cmd == nil {
		t.Error("it should be stopped once it has been left alone for the full window")
	}
}

// An attach from another terminal is seen only by a poll. The step-out must be
// dated at the first poll that no longer sees it, not at the last one that
// did, or the window would be short by up to one poll interval.
func TestAutoStopIdleDatesAStepOutAtThePollThatMissesTheAttach(t *testing.T) {
	seen := time.Now()
	m := autoStopModel(t, seen)
	m.attached["aaaa0001"] = true
	m.autoStopIdle(seen, seen)

	missed := seen.Add(10 * time.Second) // the next idle poll: the attach is gone
	m.attached = map[string]bool{}
	m.autoStopIdle(missed, missed)

	early := seen.Add(testAfter + time.Second) // a full window after the last sighting
	m.lastWatch = early.Add(-time.Second)
	if cmd := m.autoStopIdle(early, early); cmd != nil {
		t.Error("the window must run from the poll that missed the attach")
	}
	due := missed.Add(testAfter)
	m.lastWatch = due.Add(-time.Second)
	if cmd := m.autoStopIdle(due, due); cmd == nil {
		t.Error("the session should be stopped a full window after that poll")
	}
}

// A refresh that waited while the UI was blocked (a full-screen attach) shows
// the world as it was before. Nothing may be stopped from it, and the idle
// window starts again when the UI is back.
func TestAutoStopIdleIgnoresAStaleRefresh(t *testing.T) {
	taken := time.Now()
	m := autoStopModel(t, taken)
	handled := taken.Add(time.Hour)

	if cmd := m.autoStopIdle(taken, handled); cmd != nil {
		t.Fatal("a refresh handled an hour late must not stop anything")
	}
	fresh := handled.Add(time.Second)
	if cmd := m.autoStopIdle(fresh, fresh); cmd != nil {
		t.Error("the first fresh refresh after the block must not stop anything either")
	}
	fresh = handled.Add(testAfter + time.Second)
	m.lastWatch = fresh.Add(-time.Second)
	if cmd := m.autoStopIdle(fresh, fresh); cmd == nil {
		t.Error("the session should be stopped after a full window of unbroken watching")
	}
}

func TestKeepWatchRestartsTheWindowAfterAGap(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name      string
		edit      func(m *Model)
		at        time.Time
		wantOK    bool
		wantFloor time.Time
	}{
		{"steady polling keeps the floor", func(*Model) {}, now, true, now.Add(-time.Hour)},
		{"a new cav starts its watch now", func(m *Model) { m.lastWatch, m.autoStopFloor = time.Time{}, time.Time{} }, now, true, now},
		{"an idle poll interval is not a gap", func(m *Model) { m.lastWatch = now.Add(-m.cfg.List.IdleRefresh - time.Second) }, now, true, now.Add(-time.Hour)},
		{"a suspended cav or a sleep is a gap", func(m *Model) { m.lastWatch = now.Add(-time.Hour) }, now, true, now},
		{"a stale refresh", func(*Model) {}, now.Add(-time.Minute), false, now},
		{"a slow poll floor is not staleness", func(m *Model) { m.cfg.List.MinRefresh = 6 * time.Second }, now.Add(-7 * time.Second), true, now.Add(-time.Hour)},
		{"a failed process scan", func(m *Model) { m.attached = nil }, now, false, now},
	} {
		m := autoStopModel(t, now)
		tc.edit(m)
		if ok := m.keepWatch(tc.at, now); ok != tc.wantOK {
			t.Errorf("%s: keepWatch = %v, want %v", tc.name, ok, tc.wantOK)
		}
		if !m.autoStopFloor.Equal(tc.wantFloor) {
			t.Errorf("%s: floor = %v, want %v", tc.name, m.autoStopFloor, tc.wantFloor)
		}
		if !m.lastWatch.Equal(tc.at) {
			t.Errorf("%s: lastWatch = %v, want the refresh time %v", tc.name, m.lastWatch, tc.at)
		}
	}
}

// A failed mark write must stop the round: a session stopped without its mark
// would jump to the stopped window.
func TestAutoStopIdleStopsNothingWhenTheMarkCannotBeSaved(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocker)
	m.unparked = unpark.Load()

	if cmd := m.autoStopIdle(now, now); cmd != nil {
		t.Error("no stop may be issued when the keep-here mark cannot be saved")
	}
	if m.err == nil {
		t.Error("the write failure should be reported")
	}
	if len(m.autoStopping) != 0 {
		t.Errorf("autoStopping = %v, want empty", m.autoStopping)
	}
}

func TestOpeningASessionCountsAsActivity(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)
	m.live["a-sid"] = true
	m.recompute()
	t.Setenv("HOME", t.TempDir()) // opening a session may write the trust flag
	t.Setenv("TMUX", "")
	if got := len(m.autoStopTargets(now)); got != 1 {
		t.Fatalf("setup: targets before opening = %d, want 1", got)
	}

	if cmd := m.openCurrent(); cmd == nil {
		t.Fatal("setup: the session should open")
	}
	// A refresh time has no monotonic reading, the open stamp has one.
	if got := len(m.autoStopTargets(time.Now().Round(0))); got != 0 {
		t.Errorf("targets right after opening = %d, want 0", got)
	}
}

// Every way out of a session (the attach ending, a tmux popup, pane or scratch
// closing) reports the job that was left, and that starts its window again.
func TestSteppingOutStartsAFreshWindow(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)
	if got := len(m.autoStopTargets(now)); got != 1 {
		t.Fatalf("setup: targets before stepping out = %d, want 1", got)
	}

	m.Update(actionMsg{note: "← back", leftJob: "aaaa0001"})
	if got := len(m.autoStopTargets(time.Now().Round(0))); got != 0 {
		t.Errorf("targets right after stepping out = %d, want 0", got)
	}
}

func TestTargetsJob(t *testing.T) {
	const jobID, sessionID, name = "72cdfc0f", "cc12801d-1111-2222-3333-444455556666", "Hermes-Agent"
	for target, want := range map[string]bool{
		"72cdfc0f":      true,  // the job id
		"72cd":          true,  // the start of it
		"cc12801d":      true,  // the start of the session id
		"hermes-agent":  true,  // the name, in lower case
		"mes-ag":        true,  // part of the name
		"12801d":        false, // the middle of an id is not a match
		"hermes-agents": false,
		"other":         false,
	} {
		if got := targetsJob(target, jobID, sessionID, name); got != want {
			t.Errorf("targetsJob(%q) = %v, want %v", target, got, want)
		}
	}
	if targetsJob("anything", "", "", "") {
		t.Error("a session with no ids and no name matches nothing")
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
		{SessionID: "nocount", Status: "idle"},
	}
	roster := claude.Roster{"idle": "j1", "woken": "j2", "noworker": "j3", "norecord": "j4", "nodate": "j5", "nocount": "j6"}
	recs := []claude.JobRecord{
		{JobID: "j1", UpdatedAt: updated, InFlight: 2, InFlightKnown: true},
		{JobID: "j2", UpdatedAt: updated, InFlightKnown: true},
		{JobID: "j3", UpdatedAt: updated, InFlightKnown: true},
		{JobID: "j5", InFlightKnown: true},
		{JobID: "j6", UpdatedAt: updated}, // no in-flight count in the record
	}
	got := workerActivities(sessions, roster, recs)

	if w := got["idle"]; !w.last.Equal(updated) || !w.updated.Equal(updated) || w.inFlight != 2 {
		t.Errorf("idle = %+v, want the record's update time and 2 in flight", w)
	}
	if w := got["woken"]; !w.last.Equal(woke) || !w.updated.Equal(updated) {
		t.Errorf("woken = %+v, want last = the later worker start %v and updated = the record's %v", w, woke, updated)
	}
	for _, sid := range []string{"noworker", "norecord", "nodate", "nocount"} {
		if _, ok := got[sid]; ok {
			t.Errorf("%s must have no activity entry, so it is never auto-stopped", sid)
		}
	}
}

// `claude attach` takes a job id or part of a session name, so both must mark
// the session as attached.
func TestAttachedJobs(t *testing.T) {
	sessions := []claude.Session{
		{SessionID: "s1", Name: "Hermes-Agent"},
		{SessionID: "s2", Name: "billing"},
		{SessionID: "s3", Name: ""},
		{SessionID: "s4", Name: "no-job"},
	}
	roster := claude.Roster{"s1": "aaaa0001", "s2": "bbbb0002", "s3": "cccc0003"}

	for _, tc := range []struct {
		targets []string
		want    []string
	}{
		{nil, nil},
		{[]string{"bbbb0002"}, []string{"bbbb0002"}},
		{[]string{"hermes"}, []string{"aaaa0001"}},
		{[]string{"hermes-agent", "cccc0003"}, []string{"aaaa0001", "cccc0003"}},
		{[]string{"aaaa", "s2"}, []string{"aaaa0001", "bbbb0002"}},
		{[]string{"no-job", "unknown-name", "dddd0004"}, nil},
	} {
		got := attachedJobs(tc.targets, sessions, roster)
		if got == nil {
			t.Fatalf("targets %v: the set must never be nil, nil means the scan failed", tc.targets)
		}
		if len(got) != len(tc.want) {
			t.Errorf("targets %v: got %v, want %v", tc.targets, got, tc.want)
		}
		for _, jid := range tc.want {
			if !got[jid] {
				t.Errorf("targets %v: job %s should be attached", tc.targets, jid)
			}
		}
	}
}

// agents --json also lists sessions that have no process. z and Z must not
// count them, whatever the live set says. z keeps its rule: every worker that
// is not busy.
func TestKillTargetsNeedARealWorker(t *testing.T) {
	m := openModel(t, "busy", "idle", "noprocess", "other")
	m.all[0].Status, m.all[1].Status, m.all[3].Status = "busy", "idle", "starting"
	m.live = map[string]bool{"a-sid": true, "b-sid": true, "c-sid": true, "d-sid": true}

	if got := len(m.killTargets("all")); got != 3 {
		t.Errorf("Z targets = %d, want the 3 sessions with a worker", got)
	}
	if got := len(m.killTargets("idle")); got != 2 {
		t.Errorf("z targets = %d, want the 2 workers that are not busy", got)
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

// writeJobState writes a job record under a temporary home.
func writeJobState(t *testing.T, home, jobID, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "jobs", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Each target is checked again right before its stop. Only a job that is still
// exactly as the refresh saw it (same record, same idle worker, no terminal
// attached) is stopped. A target left running hands back the mark it was given.
func TestAutoStopCmdChecksEachTargetAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seen := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const (
		at      = `"updatedAt":"2026-01-01T12:00:00Z"`
		quiet   = `"inFlight":{"tasks":0}`
		same    = `{"state":"working",` + at + `,` + quiet + `}`
		started = int64(1700000000000)
	)
	cases := []struct {
		job, record string
		status      string // what claude lists at stop time; "" = not listed
		startedAt   int64
		want        string // stop | fail | down | keep (left running, mark handed back)
	}{
		{"aaaa0001", same, "idle", started, "stop"},
		{"wait0000", `{"state":"blocked",` + at + `,` + quiet + `}`, "idle", started, "stop"},
		{"bad00000", same, "idle", started, "fail"},
		{"done0000", `{"state":"stopped",` + at + `,` + quiet + `}`, "", 0, "down"},
		{"act00000", `{"state":"working","updatedAt":"2026-01-01T12:00:09Z",` + quiet + `}`, "idle", started, "keep"},
		{"task0000", `{"state":"working",` + at + `,"inFlight":{"tasks":1}}`, "idle", started, "keep"},
		{"noin0000", `{"state":"working",` + at + `}`, "idle", started, "keep"},
		{"half0000", `{"state":"work`, "idle", started, "keep"},
		{"gone0000", "", "idle", started, "keep"},
		{"busy0000", same, "busy", started, "keep"},
		{"resp0000", same, "idle", started + 1, "keep"},
		{"unl00000", same, "", 0, "keep"},
		{"att00000", same, "idle", started, "keep"},
		{"name0000", same, "idle", started, "keep"},
	}
	var (
		targets  []stopTarget
		listed   []claude.Session
		wantKept []string
		wantLog  []string
	)
	for _, c := range cases {
		sid := c.job + "-sid"
		if c.record != "" {
			writeJobState(t, home, c.job, c.record)
		}
		if c.status != "" {
			listed = append(listed, claude.Session{SessionID: sid, Status: c.status, StartedAt: c.startedAt})
		}
		targets = append(targets, stopTarget{sessionID: sid, jobID: c.job, updated: seen, startedAt: started, marked: true})
		switch c.want {
		case "keep":
			wantKept = append(wantKept, sid)
		case "stop", "fail":
			wantLog = append(wantLog, "stop "+c.job)
		}
	}
	targets[len(targets)-1].name = "Hermes-Agent"
	stubLiveSessions(t, listed, nil)
	stubAttachTargets(t, []string{"att00000", "hermes"}, true)
	log := fakeClaude(t)

	msg := autoStopCmd(targets)().(autoStoppedMsg)

	if msg.stopped != 2 || msg.err == nil {
		t.Errorf("stopped = %d err = %v, want 2 stopped and the failed stop reported", msg.stopped, msg.err)
	}
	calls, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(calls)); got != strings.Join(wantLog, "\n") {
		t.Errorf("claude calls = %q, want %q", got, wantLog)
	}
	if !slices.Equal(msg.unmark, wantKept) {
		t.Errorf("unmark = %v, want every target left running: %v", msg.unmark, wantKept)
	}
}

// A mark that was there before the round is not handed back.
func TestAutoStopCmdKeepsAMarkItDidNotAdd(t *testing.T) {
	stubLiveSessions(t, nil, nil)
	stubAttachTargets(t, nil, true)
	t.Setenv("HOME", t.TempDir())

	msg := autoStopCmd([]stopTarget{{sessionID: "a-sid", jobID: "aaaa0001"}})().(autoStoppedMsg)
	if len(msg.unmark) != 0 {
		t.Errorf("unmark = %v, want none", msg.unmark)
	}
}

func TestAutoStopCmdStopsNothingWhenItCannotTell(t *testing.T) {
	seen := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	idle := []claude.Session{{SessionID: "a-sid", Status: "idle"}}
	for _, tc := range []struct {
		name    string
		listErr error
		scanOK  bool
	}{
		{"the process scan fails", nil, false},
		{"claude cannot list the sessions", errors.New("daemon down"), true},
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeJobState(t, home, "aaaa0001", `{"state":"working","updatedAt":"2026-01-01T12:00:00Z","inFlight":{"tasks":0}}`)
		stubLiveSessions(t, idle, tc.listErr)
		stubAttachTargets(t, nil, tc.scanOK)
		log := fakeClaude(t)

		target := stopTarget{sessionID: "a-sid", jobID: "aaaa0001", updated: seen}
		msg := autoStopCmd([]stopTarget{target})().(autoStoppedMsg)

		if _, err := os.Stat(log); err == nil || msg.stopped != 0 {
			t.Errorf("%s: msg = %+v, nothing may be stopped", tc.name, msg)
		}
	}
}

// A target found in use at stop time loses the mark the round gave it, so a
// running session is not left marked. A mark from before the round stays.
func TestAutoStopRollsBackOnlyTheMarkItAdded(t *testing.T) {
	for _, marked := range []bool{false, true} {
		now := time.Now()
		m := autoStopModel(t, now)
		home := t.TempDir()
		t.Setenv("HOME", home)
		writeJobState(t, home, "aaaa0001", `{"state":"working","inFlight":{"tasks":0}}`)
		stubLiveSessions(t, []claude.Session{{SessionID: "a-sid", Status: "busy"}}, nil) // a turn just started
		stubAttachTargets(t, nil, true)
		if marked {
			if err := m.unparked.Add("a-sid"); err != nil {
				t.Fatal(err)
			}
		}

		cmd := m.autoStopIdle(now, now)
		if cmd == nil || !m.unparked.Has("a-sid") {
			t.Fatalf("marked before = %v: setup: the round should mark and issue a stop", marked)
		}
		msg := cmd().(autoStoppedMsg)
		m.Update(msg)

		if msg.stopped != 0 || m.unparked.Has("a-sid") != marked {
			t.Errorf("marked before = %v: stopped = %d, mark after = %v", marked, msg.stopped, m.unparked.Has("a-sid"))
		}
	}
}

// doRefresh feeds auto-stop: the time must be wall clock only (a monotonic
// reading would hide a laptop sleep), and the attached set must be nil when
// the scan fails or is not wanted, never an empty set.
func TestDoRefreshStampsTheTimeAndTheAttachedSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeJobState(t, home, "aaaa0001", `{"sessionId":"a-sid","state":"working","updatedAt":"2026-01-01T12:00:00Z","inFlight":{"tasks":0}}`)
	stubLiveSessions(t, []claude.Session{{SessionID: "a-sid", Kind: "background", Name: "hermes", Status: "idle"}}, nil)

	for _, tc := range []struct {
		name     string
		watch    bool
		targets  []string
		scanOK   bool
		wantNil  bool
		attached bool
	}{
		{"attached by name", true, []string{"herm"}, true, false, true},
		{"nobody attached", true, nil, true, false, false},
		{"the scan fails", true, nil, false, true, false},
		{"auto-stop off", false, []string{"herm"}, true, true, false},
	} {
		stubAttachTargets(t, tc.targets, tc.scanOK)
		before := time.Now()
		rr := doRefresh("", tc.watch)

		if (rr.attached == nil) != tc.wantNil || rr.attached["aaaa0001"] != tc.attached {
			t.Errorf("%s: attached = %v", tc.name, rr.attached)
		}
		if wantErr := tc.watch && !tc.scanOK; (rr.attachErr != nil) != wantErr {
			t.Errorf("%s: attachErr = %v, want an error only when a wanted scan fails", tc.name, rr.attachErr)
		}
		if rr.at != rr.at.Round(0) {
			t.Errorf("%s: at carries a monotonic reading", tc.name)
		}
		if rr.at.Before(before.Add(-time.Second)) || rr.at.After(time.Now()) {
			t.Errorf("%s: at = %v, want the time of this refresh", tc.name, rr.at)
		}
		if w, ok := rr.workers["a-sid"]; !ok || w.updated.IsZero() {
			t.Errorf("%s: workers = %v, want the live session's activity", tc.name, rr.workers)
		}
	}
}

// While a native agents view is open cav cannot tell which session it shows.
// Nothing may be stopped, the header must say why, and every session gets a
// full window again once the view is closed.
func TestAnOpenAgentsViewPausesAutoStop(t *testing.T) {
	m := autoStopModel(t, time.Now())
	refresh := func(attached map[string]bool, err error) {
		m.Update(refreshResult{
			sessions:  m.all,
			roster:    m.roster,
			states:    map[string]string{},
			live:      map[string]bool{"a-sid": true},
			workers:   m.workers,
			attached:  attached,
			attachErr: err,
			at:        time.Now(),
		})
	}

	refresh(nil, claude.ErrAgentsView)
	if len(m.autoStopping) != 0 {
		t.Error("nothing may be stopped while an agents view is open")
	}
	if got := m.indicators(); !strings.Contains(got, "auto-stop paused: a claude agents view is open") {
		t.Errorf("header = %q, want the reason for the pause", got)
	}

	refresh(map[string]bool{}, nil) // the view was closed
	if len(m.autoStopping) != 0 {
		t.Error("a session must get a full window after the view is closed")
	}
	if got := m.indicators(); strings.Contains(got, "auto-stop paused") {
		t.Errorf("header = %q, want no pause note", got)
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
	rr := refreshResult{
		sessions: m.all,
		roster:   m.roster,
		states:   map[string]string{},
		live:     map[string]bool{"a-sid": true},
		workers:  m.workers,
		attached: map[string]bool{},
		at:       time.Now(),
	}
	m.Update(rr)
	if _, issued := m.autoStopping["aaaa0001"]; !issued {
		t.Error("the refresh should have issued a stop for the idle session")
	}

	m.Update(autoStoppedMsg{stopped: 1})
	if m.status != "auto-stopped 1 idle session(s)" {
		t.Errorf("footer note = %q", m.status)
	}
}

// The same refresh, delivered late because the UI was blocked, stops nothing.
func TestRefreshDeliveredLateStopsNothing(t *testing.T) {
	m := autoStopModel(t, time.Now())
	m.Update(refreshResult{
		sessions: m.all,
		roster:   m.roster,
		states:   map[string]string{},
		live:     map[string]bool{"a-sid": true},
		workers:  m.workers,
		attached: map[string]bool{},
		at:       time.Now().Add(-time.Minute),
	})
	if len(m.autoStopping) != 0 {
		t.Error("a late refresh must not issue a stop")
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

// x, z and auto-stop leave a keep-here mark. After the session is resumed the
// mark is still there, and d must stop the session, not only drop the mark.
func TestRemoveStopsAResumedSessionThatStillHasItsMark(t *testing.T) {
	m := autoStopModel(t, time.Now())
	if err := m.unparked.Add("a-sid"); err != nil {
		t.Fatal(err)
	}

	m.mode, m.pending = modeConfirm, &m.all[0]
	if prompt := m.footerBlock(); !strings.Contains(prompt, "Stop ") || strings.Contains(prompt, "back to the stopped window") {
		t.Errorf("the confirm should offer a stop for a live session:\n%s", prompt)
	}

	cmd := m.removeOne(m.all[0])
	if cmd == nil {
		t.Fatal("d on a live session must issue a stop")
	}
	if m.unparked.Has("a-sid") || !m.justStopped["a-sid"] {
		t.Errorf("mark kept = %v, hidden = %v; want the mark dropped and the session hidden", m.unparked.Has("a-sid"), m.justStopped["a-sid"])
	}
}

func TestRemoveMovesAStoppedSessionBackToTheStoppedWindow(t *testing.T) {
	m := autoStopModel(t, time.Now())
	m.all[0].Status = "" // stopped in place: no worker
	m.states["a-sid"] = "stopped"
	if err := m.unparked.Add("a-sid"); err != nil {
		t.Fatal(err)
	}

	m.mode, m.pending = modeConfirm, &m.all[0]
	if prompt := m.footerBlock(); !strings.Contains(prompt, "back to the stopped window") {
		t.Errorf("the confirm should offer the move back:\n%s", prompt)
	}
	if cmd := m.removeOne(m.all[0]); cmd != nil {
		t.Error("a session with no worker needs no stop")
	}
	if m.unparked.Has("a-sid") || m.dismissed.Has("a-sid") || !m.isStopped(m.all[0]) {
		t.Error("dropping the mark is enough: the session is stopped, so it is back in the stopped window")
	}
}

// A session that was stopped in place, resumed, and then lost its worker (it
// finished, or the laptop slept) still has the mark but is not stopped. One d
// must move it to the stopped window, not only drop the mark.
func TestRemoveMovesAResumedSessionThatLostItsWorker(t *testing.T) {
	m := autoStopModel(t, time.Now())
	m.all[0].Status = ""
	m.states["a-sid"] = "done"
	if err := m.unparked.Add("a-sid"); err != nil {
		t.Fatal(err)
	}

	if cmd := m.removeOne(m.all[0]); cmd != nil {
		t.Error("a session with no worker needs no stop")
	}
	if m.unparked.Has("a-sid") || !m.isStopped(m.all[0]) {
		t.Errorf("mark kept = %v, in the stopped window = %v; want one d to move it", m.unparked.Has("a-sid"), m.isStopped(m.all[0]))
	}
}
