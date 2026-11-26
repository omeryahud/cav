package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/unpark"
)

const testAfter = 5 * time.Minute

// autoStopModel returns a model with auto-stop on, watched by this cav for an
// hour, and one idle session per name that qualifies for a stop at `now`. The
// first is sid "a-sid", job "aaaa0001"; the second "b-sid", "bbbb0001".
func autoStopModel(t *testing.T, now time.Time, names ...string) *Model {
	t.Helper()
	if len(names) == 0 {
		names = []string{"a"}
	}
	m := openModel(t, names...)
	m.cfg.AutoStop.After = testAfter
	m.roster = claude.Roster{}
	m.workers = map[string]workerActivity{}
	m.hadWorker = map[string]bool{}
	for i := range m.all {
		s := &m.all[i]
		s.Status = "idle"
		s.JobID = strings.Repeat(s.SessionID[:1], 4) + "0001"
		m.roster[s.SessionID] = s.JobID
		m.workers[s.SessionID] = workerActivity{last: now.Add(-10 * time.Minute)}
		m.hadWorker[s.JobID] = true
	}
	m.attached = map[string]bool{}
	m.lastWatch = now.Add(-time.Second)
	m.autoStopFloor = now.Add(-time.Hour)
	return m
}

// refreshOf is a fresh refresh result that shows the model's sessions as they are.
func refreshOf(m *Model) refreshResult {
	live := map[string]bool{}
	for _, s := range m.all {
		live[s.SessionID] = true
	}
	return refreshResult{sessions: m.all, roster: m.roster, states: m.states, live: live,
		workers: m.workers, attached: map[string]bool{}, at: time.Now()}
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
		{"in use a minute ago", func(m *Model) {
			m.lastInUse["aaaa0001"] = now.Add(-time.Minute)
		}, 0},
		{"in use long ago", func(m *Model) {
			m.lastInUse["aaaa0001"] = now.Add(-testAfter - time.Second)
		}, 1},
		{"cav started watching a minute ago", func(m *Model) {
			m.autoStopFloor = now.Add(-time.Minute)
		}, 0},
		{"removed with d a moment ago", func(m *Model) { m.justStopped["a-sid"] = true }, 0},
		{"claude gave no job id for the worker", func(m *Model) { m.all[0].JobID = "" }, 0},
		{"the job id is only a guess from the records", func(m *Model) { m.all[0].JobID = "ffff0009" }, 0},
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
	m.Update(autoStoppedMsg{target: stopTarget{sessionID: "a-sid", jobID: "aaaa0001", name: "a"}, outcome: stillIdle})
	if m.status != "auto-stopped a" {
		t.Errorf("footer note = %q", m.status)
	}

	later := now.Add(time.Second)
	if cmd := m.autoStopIdle(later, later); cmd != nil {
		t.Error("the same session must not be stopped again right away")
	}
	later = now.Add(autoStopRetry + time.Second)
	m.lastWatch = later.Add(-time.Second)
	if cmd := m.autoStopIdle(later, later); cmd == nil {
		t.Error("a session still live after the retry window should be stopped again")
	}
}

// One stop per refresh, and none while a stop is running: every stop is then
// picked from fresh data. A session the user opens and leaves while another
// one is being stopped must not be stopped from the old pick.
func TestAutoStopIdleStopsOneSessionAtATime(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now, "a", "b")

	if cmd := m.autoStopIdle(now, now); cmd == nil || len(m.autoStopping) != 1 {
		t.Fatalf("the first refresh should stop exactly one session, issued = %v", m.autoStopping)
	}
	if _, first := m.autoStopping["aaaa0001"]; !first || m.unparked.Has("b-sid") {
		t.Error("only the first session may be marked and stopped")
	}
	later := now.Add(time.Second)
	if cmd := m.autoStopIdle(later, later); cmd != nil {
		t.Error("no second stop while the first is still running")
	}

	m.Update(actionMsg{leftJob: "bbbb0001"}) // the user opened b and stepped out
	m.Update(autoStoppedMsg{target: stopTarget{sessionID: "a-sid", jobID: "aaaa0001"}, outcome: stillIdle})
	later = time.Now().Add(time.Second)
	m.lastWatch = later.Add(-time.Second)
	if cmd := m.autoStopIdle(later.Round(0), later); cmd != nil {
		t.Error("b was just left: the next round must see that and leave it alone")
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

// What cav sees only by polling (an attach from another terminal, a busy
// worker, background work) ends somewhere between two polls. The idle window
// must run from the first poll that sees the session free, not from the last
// one that saw it in use, or the window would be short by up to one poll. It
// must also not rest on the job record alone: a record can hold still through
// a long turn.
func TestAutoStopIdleDatesTheEndOfUseAtThePollThatSeesItFree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inUse  func(m *Model)
		isFree func(m *Model)
	}{
		{"an attach from another terminal",
			func(m *Model) { m.attached["aaaa0001"] = true },
			func(m *Model) { m.attached = map[string]bool{} }},
		{"a busy worker with a record that held still",
			func(m *Model) { m.all[0].Status = "busy" },
			func(m *Model) { m.all[0].Status = "idle" }},
		{"background work",
			func(m *Model) { m.workers["a-sid"] = workerActivity{last: m.workers["a-sid"].last, inFlight: 1} },
			func(m *Model) { m.workers["a-sid"] = workerActivity{last: m.workers["a-sid"].last} }},
	} {
		seen := time.Now()
		m := autoStopModel(t, seen)
		tc.inUse(m)
		m.autoStopIdle(seen, seen)

		free := seen.Add(10 * time.Second) // the next idle poll
		tc.isFree(m)
		m.autoStopIdle(free, free)

		early := seen.Add(testAfter + time.Second) // a full window after the last sighting
		m.lastWatch = early.Add(-time.Second)
		if cmd := m.autoStopIdle(early, early); cmd != nil {
			t.Errorf("%s: the window must run from the poll that saw the session free", tc.name)
		}
		due := free.Add(testAfter)
		m.lastWatch = due.Add(-time.Second)
		if cmd := m.autoStopIdle(due, due); cmd == nil {
			t.Errorf("%s: the session should be stopped a full window after that poll", tc.name)
		}
	}
}

// A stamp only moves forward. The refresh that follows a step-out was taken
// before it, and must not date the step-out earlier than it happened.
func TestAStepOutStampIsNeverMovedBack(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)
	m.attached["aaaa0001"] = true
	m.autoStopIdle(now, now)

	leftAt := time.Now()
	m.Update(actionMsg{leftJob: "aaaa0001"})
	m.attached = map[string]bool{}
	taken := leftAt.Add(-3 * time.Second).Round(0) // read before the step-out, handled after it
	m.autoStopIdle(taken, time.Now())

	if got := m.lastInUse["aaaa0001"]; got.Before(leftAt) {
		t.Errorf("stamp = %v, moved back before the step-out at %v", got, leftAt)
	}
	m.markInUse("aaaa0001", leftAt.Add(-time.Hour))
	if got := m.lastInUse["aaaa0001"]; got.Before(leftAt) {
		t.Errorf("stamp = %v, an older sighting moved it back", got)
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

// A session stopped without its mark would jump to the stopped window. So no
// stop may be issued while the mark cannot be saved: not on the first refresh
// and not on the ones after it.
func TestAutoStopIdleStopsNothingWhileTheMarkCannotBeSaved(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block func(t *testing.T) (configHome string)
	}{
		{"the config directory cannot be made", func(t *testing.T) string {
			blocker := filepath.Join(t.TempDir(), "not-a-dir")
			if err := os.WriteFile(blocker, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			return blocker
		}},
		{"the config directory is read-only", func(t *testing.T) string {
			home := t.TempDir()
			dir := filepath.Join(home, "cav")
			if err := os.Mkdir(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			return home
		}},
	} {
		now := time.Now()
		m := autoStopModel(t, now)
		t.Setenv("XDG_CONFIG_HOME", tc.block(t))
		m.unparked = unpark.Load()

		for round := 1; round <= 2; round++ {
			at := now.Add(time.Duration(round) * time.Second)
			if cmd := m.autoStopIdle(at, at); cmd != nil {
				t.Errorf("%s, refresh %d: no stop may be issued", tc.name, round)
			}
		}
		if m.err == nil {
			t.Errorf("%s: the write failure should be reported", tc.name)
		}
		if m.unparked.Has("a-sid") || len(m.autoStopping) != 0 || m.autoStopBusy {
			t.Errorf("%s: mark = %v, issued = %v, busy = %v; want nothing left behind",
				tc.name, m.unparked.Has("a-sid"), m.autoStopping, m.autoStopBusy)
		}
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

// d hides a live session and stops it. A refresh taken before that stop landed
// still shows it idle: auto-stop must not pick it and mark it to stay.
func TestAutoStopLeavesASessionTheUserJustRemoved(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)

	if cmd := m.removeOne(m.all[0]); cmd == nil {
		t.Fatal("setup: d on a live session issues a stop")
	}
	if cmd := m.autoStopIdle(now, now); cmd != nil || m.unparked.Has("a-sid") {
		t.Error("a session removed with d must not be picked or marked")
	}
	if !m.isStopped(m.all[0]) {
		t.Error("the session should stay in the stopped window")
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

// The target is checked again right before its stop. Only a job that is still
// exactly as the refresh saw it (same record, the same idle worker under the
// same job id, no terminal attached) is stopped.
func TestAutoStopCmdChecksTheTargetAgain(t *testing.T) {
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
		listedJob   string // the job id claude lists for the session
		startedAt   int64
		want        int
		stopCalled  bool
	}{
		{"aaaa0001", same, "idle", "aaaa0001", started, stillIdle, true},
		{"wait0000", `{"state":"blocked",` + at + `,` + quiet + `}`, "idle", "wait0000", started, stillIdle, true},
		{"bad00000", same, "idle", "bad00000", started, stillIdle, true},
		{"done0000", `{"state":"stopped",` + at + `,` + quiet + `}`, "", "", 0, alreadyDown, false},
		{"act00000", `{"state":"working","updatedAt":"2026-01-01T12:00:09Z",` + quiet + `}`, "idle", "act00000", started, inUse, false},
		{"task0000", `{"state":"working",` + at + `,"inFlight":{"tasks":1}}`, "idle", "task0000", started, inUse, false},
		{"noin0000", `{"state":"working",` + at + `}`, "idle", "noin0000", started, inUse, false},
		{"half0000", `{"state":"work`, "idle", "half0000", started, inUse, false},
		{"gone0000", "", "idle", "gone0000", started, inUse, false},
		{"busy0000", same, "busy", "busy0000", started, inUse, false},
		{"resp0000", same, "idle", "resp0000", started + 1, inUse, false},
		{"unl00000", same, "", "", 0, inUse, false},
		{"othr0000", same, "idle", "ffff0009", started, inUse, false}, // the session runs under another job
		{"att00000", same, "idle", "att00000", started, inUse, false},
		{"name0000", same, "idle", "name0000", started, inUse, false},
	}
	var listed []claude.Session
	for _, c := range cases {
		if c.record != "" {
			writeJobState(t, home, c.job, c.record)
		}
		if c.status != "" {
			listed = append(listed, claude.Session{SessionID: c.job + "-sid", JobID: c.listedJob, Status: c.status, StartedAt: c.startedAt})
		}
	}
	stubLiveSessions(t, listed, nil)
	stubAttachTargets(t, []string{"att00000", "hermes"}, true)
	log := fakeClaude(t)

	var wantLog []string
	for _, c := range cases {
		target := stopTarget{sessionID: c.job + "-sid", jobID: c.job, updated: seen, startedAt: started}
		if c.job == "name0000" {
			target.name = "Hermes-Agent"
		}
		msg := autoStopCmd(target)().(autoStoppedMsg)

		if msg.outcome != c.want || msg.target.jobID != c.job {
			t.Errorf("%s: outcome = %d for %s, want %d", c.job, msg.outcome, msg.target.jobID, c.want)
		}
		if (msg.err != nil) != (c.job == "bad00000") {
			t.Errorf("%s: err = %v", c.job, msg.err)
		}
		if c.stopCalled {
			wantLog = append(wantLog, "stop "+c.job)
		}
	}
	calls, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(calls)); got != strings.Join(wantLog, "\n") {
		t.Errorf("claude calls = %q, want %q", got, wantLog)
	}
}

func TestAutoStopCmdStopsNothingWhenItCannotTell(t *testing.T) {
	seen := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	idle := []claude.Session{{SessionID: "a-sid", JobID: "aaaa0001", Status: "idle"}}
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
		msg := autoStopCmd(target)().(autoStoppedMsg)

		if _, err := os.Stat(log); err == nil || msg.outcome != inUse {
			t.Errorf("%s: msg = %+v, nothing may be stopped", tc.name, msg)
		}
	}
}

// A target found in use at stop time keeps running, so it must lose the mark
// the round gave it. The mark goes only when a refresh taken after the round
// still shows the worker: a session that x, z or another cav stopped meanwhile
// needs the mark to stay in the main pane. A refresh that shows neither a
// worker nor a stop settles nothing: the list can miss a worker for a poll.
func TestARoundThatLeavesItsTargetRunningSettlesTheMarkLater(t *testing.T) {
	for _, tc := range []struct {
		name        string
		markedByIt  bool
		then        func(m *Model) // what the refresh after the round shows
		wantMark    bool
		wantSettled bool
	}{
		{"the round's own mark, worker still there", true, func(*Model) {}, false, true},
		{"the round's own mark, stopped meanwhile", true, func(m *Model) {
			m.all[0].Status, m.states["a-sid"] = "", "stopped"
		}, true, true},
		{"the round's own mark, worker missing from one listing", true, func(m *Model) { m.all[0].Status = "" }, true, false},
		{"a mark from before the round", false, func(*Model) {}, true, true},
	} {
		m := autoStopModel(t, time.Now())
		if err := m.unparked.Add("a-sid"); err != nil {
			t.Fatal(err)
		}
		m.autoStopBusy = true
		m.autoStopping["aaaa0001"] = time.Now()

		m.Update(autoStoppedMsg{target: stopTarget{sessionID: "a-sid", jobID: "aaaa0001", marked: tc.markedByIt}, outcome: inUse})
		if m.autoStopBusy || !m.unparked.Has("a-sid") {
			t.Fatalf("%s: busy = %v, mark = %v; the round is over and the mark waits for a refresh", tc.name, m.autoStopBusy, m.unparked.Has("a-sid"))
		}

		before := refreshOf(m)
		before.at = time.Now().Add(-time.Second) // read before the round ended
		m.Update(before)
		if !m.unparked.Has("a-sid") {
			t.Errorf("%s: a refresh from before the end of the round must not settle the mark", tc.name)
		}

		tc.then(m)
		time.Sleep(5 * time.Millisecond)
		m.Update(refreshOf(m))
		if got := m.unparked.Has("a-sid"); got != tc.wantMark {
			t.Errorf("%s: mark after the next refresh = %v, want %v", tc.name, got, tc.wantMark)
		}
		if settled := len(m.unmarkPending) == 0; settled != tc.wantSettled {
			t.Errorf("%s: settled = %v, want %v", tc.name, settled, tc.wantSettled)
		}
	}
}

// The listing missed the worker for one poll and shows it again: the mark the
// round gave must still go.
func TestAMarkWaitsForAListingThatMissedTheWorker(t *testing.T) {
	m := autoStopModel(t, time.Now())
	if err := m.unparked.Add("a-sid"); err != nil {
		t.Fatal(err)
	}
	m.autoStopping["aaaa0001"] = time.Now()
	m.Update(autoStoppedMsg{target: stopTarget{sessionID: "a-sid", jobID: "aaaa0001", marked: true}, outcome: inUse})

	time.Sleep(5 * time.Millisecond)
	m.all[0].Status = "" // one listing without the worker
	m.Update(refreshOf(m))
	m.all[0].Status = "busy" // it is back
	m.Update(refreshOf(m))

	if m.unparked.Has("a-sid") || len(m.unmarkPending) != 0 {
		t.Errorf("mark = %v, pending = %v; want the mark gone once the worker shows again", m.unparked.Has("a-sid"), m.unmarkPending)
	}
}

// When `claude agents` fails, cav has no statuses: a session can be busy with
// a record that holds still. Such a refresh is one cav did not watch. Nothing
// is stopped from it, the header says why, and the idle window starts again
// after it.
func TestAFailedListingIsNotWatchedTime(t *testing.T) {
	start := time.Now()
	m := autoStopModel(t, start)
	m.all[0].Status = "busy"
	m.autoStopIdle(start, start)

	// The listing fails for longer than the idle window. The session comes
	// from its on-disk record, with no status.
	m.all[0].Status = ""
	m.attached, m.watchErr = nil, errListFailed
	var last time.Time
	for i := 1; i <= 31; i++ {
		last = start.Add(time.Duration(i) * 10 * time.Second)
		if cmd := m.autoStopIdle(last, last); cmd != nil {
			t.Fatal("nothing may be stopped while the listing fails")
		}
	}
	if got := m.indicators(); !strings.Contains(got, "auto-stop paused: claude cannot list the sessions") {
		t.Errorf("header = %q, want the reason for the pause", got)
	}

	// The first good listing shows the session idle.
	m.all[0].Status = "idle"
	m.attached, m.watchErr = map[string]bool{}, nil
	back := last.Add(10 * time.Second)
	if cmd := m.autoStopIdle(back, back); cmd != nil {
		t.Error("the session was not watched during the outage: it needs a full window first")
	}
	due := back.Add(testAfter + 10*time.Second)
	m.lastWatch = due.Add(-time.Second)
	if cmd := m.autoStopIdle(due, due); cmd == nil {
		t.Error("the session should be stopped a full window after the listing came back")
	}
}

// A listing can also succeed and still miss a worker for a poll (the daemon is
// restarting). A worker that shows again was not watched meanwhile.
func TestAWorkerThatShowsAgainGetsAFreshWindow(t *testing.T) {
	now := time.Now()
	m := autoStopModel(t, now)
	m.all[0].Status = "" // this poll does not show the worker
	if cmd := m.autoStopIdle(now, now); cmd != nil {
		t.Fatal("setup: no worker, no stop")
	}

	m.all[0].Status = "idle"
	back := now.Add(time.Second)
	if cmd := m.autoStopIdle(back, back); cmd != nil {
		t.Error("a worker that just showed again must not be stopped at once")
	}
	watched := back.Add(10 * time.Second) // the first poll that watched it all the way
	if cmd := m.autoStopIdle(watched, watched); cmd != nil {
		t.Error("ten seconds are not a full window")
	}
	due := watched.Add(testAfter)
	m.lastWatch = due.Add(-time.Second)
	if cmd := m.autoStopIdle(due, due); cmd == nil {
		t.Error("it should be stopped after a full window")
	}
}

// The checks before a stop run from cheap to costly, and the process scan runs
// last so it is the freshest. A record that already rules the stop out must
// not cost a `claude agents` call.
func TestRecheckOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seen := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	writeJobState(t, home, "aaaa0001", `{"state":"working","updatedAt":"2026-01-01T12:00:00Z","inFlight":{"tasks":0}}`)
	writeJobState(t, home, "busy0000", `{"state":"working","updatedAt":"2026-01-01T12:00:00Z","inFlight":{"tasks":2}}`)

	var calls []string
	origList, origScan := liveSessions, attachTargets
	t.Cleanup(func() { liveSessions, attachTargets = origList, origScan })
	liveSessions = func(context.Context) ([]claude.Session, error) {
		calls = append(calls, "list")
		return []claude.Session{{SessionID: "a-sid", JobID: "aaaa0001", Status: "idle"}}, nil
	}
	attachTargets = func() ([]string, error) {
		calls = append(calls, "scan")
		return nil, nil
	}

	if got := recheck(stopTarget{sessionID: "b-sid", jobID: "busy0000", updated: seen}); got != inUse || len(calls) != 0 {
		t.Errorf("a record with work in flight: outcome %d after calls %v, want in use with no calls", got, calls)
	}
	if got := recheck(stopTarget{sessionID: "a-sid", jobID: "aaaa0001", updated: seen}); got != stillIdle || strings.Join(calls, ",") != "list,scan" {
		t.Errorf("an idle target: outcome %d after calls %v, want still idle after list then scan", got, calls)
	}
}

func TestAutoStopDoneNotes(t *testing.T) {
	target := stopTarget{sessionID: "a-sid", jobID: "aaaa0001", name: "a"}
	for _, tc := range []struct {
		msg  autoStoppedMsg
		want string
	}{
		{autoStoppedMsg{target: target, outcome: stillIdle}, "auto-stopped a"},
		{autoStoppedMsg{target: target, outcome: stillIdle, err: os.ErrNotExist}, "auto-stop failed: " + os.ErrNotExist.Error()},
		{autoStoppedMsg{target: target, outcome: inUse}, ""},
		{autoStoppedMsg{target: target, outcome: alreadyDown}, ""},
	} {
		m := autoStopModel(t, time.Now())
		m.autoStopBusy = true
		m.Update(tc.msg)
		if m.status != tc.want || m.autoStopBusy {
			t.Errorf("outcome %d err %v: note = %q, want %q; busy = %v", tc.msg.outcome, tc.msg.err, m.status, tc.want, m.autoStopBusy)
		}
	}
}

// doRefresh feeds auto-stop. The time must be wall clock only (a monotonic
// reading would hide a laptop sleep). The attached set must be nil when the
// scan fails or is not wanted, never an empty set. And a live session's job is
// the one claude lists for it, not the first on-disk record with its session id.
func TestDoRefreshFeedsAutoStop(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const rec = `{"sessionId":"a-sid","state":"working","updatedAt":"2026-01-01T12:00:00Z","inFlight":{"tasks":0}}`
	writeJobState(t, home, "0000aaaa", rec) // an older job whose record still names this session
	writeJobState(t, home, "aaaa0001", rec)
	listed := []claude.Session{{SessionID: "a-sid", JobID: "aaaa0001", Kind: "background", Name: "hermes", Status: "idle"}}

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
		stubLiveSessions(t, listed, nil)
		stubAttachTargets(t, tc.targets, tc.scanOK)
		before := time.Now()
		rr := doRefresh("", tc.watch)

		if got := rr.roster["a-sid"]; got != "aaaa0001" {
			t.Errorf("%s: job = %q, want the one claude lists", tc.name, got)
		}
		if (rr.attached == nil) != tc.wantNil || rr.attached["aaaa0001"] != tc.attached {
			t.Errorf("%s: attached = %v", tc.name, rr.attached)
		}
		if wantErr := tc.watch && !tc.scanOK; (rr.watchErr != nil) != wantErr {
			t.Errorf("%s: watchErr = %v, want an error only when a wanted scan fails", tc.name, rr.watchErr)
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

	// A failed listing: the sessions still come from the on-disk records, but
	// the refresh must say it cannot tell, even though the process scan works.
	stubLiveSessions(t, nil, errors.New("daemon down"))
	stubAttachTargets(t, nil, true)
	rr := doRefresh("", true)
	if rr.attached != nil || !errors.Is(rr.watchErr, errListFailed) || len(rr.sessions) == 0 {
		t.Errorf("a failed listing: attached = %v, watchErr = %v, sessions = %d; want nil, the list error, and the on-disk sessions",
			rr.attached, rr.watchErr, len(rr.sessions))
	}
}

// While a native agents view is open cav cannot tell which session it shows.
// Nothing may be stopped, the header must say why, and every session gets a
// full window again once the view is closed.
func TestAnOpenAgentsViewPausesAutoStop(t *testing.T) {
	m := autoStopModel(t, time.Now())
	refresh := func(attached map[string]bool, err error) {
		rr := refreshOf(m)
		rr.attached, rr.watchErr = attached, err
		m.Update(rr)
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

// The refresh handler is where auto-stop runs: a refresh that carries an idle
// session past the threshold must issue its stop.
func TestRefreshRunsAutoStop(t *testing.T) {
	m := autoStopModel(t, time.Now())
	m.Update(refreshOf(m))
	if _, issued := m.autoStopping["aaaa0001"]; !issued || !m.autoStopBusy {
		t.Error("the refresh should have issued a stop for the idle session")
	}
}

// The same refresh, delivered late because the UI was blocked, stops nothing.
func TestRefreshDeliveredLateStopsNothing(t *testing.T) {
	m := autoStopModel(t, time.Now())
	late := refreshOf(m)
	late.at = time.Now().Add(-time.Minute)
	m.Update(late)
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

// An idle session whose job record counts background tasks is never stopped,
// and the count can be left over from work that has ended. The row says so,
// or it would look as if auto-stop did not work.
func TestTheRowSaysWhyAnIdleSessionIsKept(t *testing.T) {
	m := autoStopModel(t, time.Now())
	row := func() string { return m.rowLine(m.all[0], false, true, 120) }

	if strings.Contains(row(), "tasks") {
		t.Errorf("a session with no background work needs no note: %q", row())
	}
	m.workers["a-sid"] = workerActivity{inFlight: 3}
	if !strings.Contains(row(), "3 tasks") {
		t.Errorf("row = %q, want the task count", row())
	}
	m.all[0].Status = "busy"
	if strings.Contains(row(), "tasks") {
		t.Errorf("a busy session is not a candidate, so no note: %q", row())
	}
	m.all[0].Status = "idle"
	m.cfg.AutoStop.After = 0
	if strings.Contains(row(), "tasks") {
		t.Errorf("with auto-stop off the row must not change: %q", row())
	}
}

// The hint needs room after the age. Rows give up name width for it, and a
// row too narrow for the whole hint leaves it out: it must never be cut short
// or cut into the age.
func TestTheRowHintNeverCutsTheAge(t *testing.T) {
	for _, preview := range []bool{false, true} {
		m := autoStopModel(t, time.Now())
		m.all[0].Name = strings.Repeat("n", 70)
		m.all[0].StartedAt = time.Now().Add(-3 * time.Hour).UnixMilli()
		m.workers["a-sid"] = workerActivity{inFlight: 3}
		m.live["a-sid"] = true
		m.previewOn, m.width = preview, 300
		m.recompute()

		for width := 36; width <= 130; width++ { // 36 is the narrowest row that holds a name and the age
			row := m.rowLine(m.view[0], false, true, width)
			if !strings.Contains(row, " 3h") {
				t.Fatalf("preview %v, width %d: the age is cut: %q", preview, width, row)
			}
			whole := strings.Contains(row, "  3 tasks")
			if strings.Contains(row, "task") != whole {
				t.Fatalf("preview %v, width %d: the hint is cut short: %q", preview, width, row)
			}
			if width >= 60 && !whole {
				t.Fatalf("preview %v, width %d: there is room for the hint: %q", preview, width, row)
			}
		}
	}
}

func TestSteppedOutNamesTheJobThatWasLeft(t *testing.T) {
	if msg := steppedOut("← back", "aaaa0001"); msg.note != "← back" || msg.leftJob != "aaaa0001" || msg.selectJob != "" {
		t.Errorf("steppedOut = %+v", msg)
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
