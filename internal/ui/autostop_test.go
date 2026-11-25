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

// stubAttachTargets replaces the process scan the stop-time check runs.
func stubAttachTargets(t *testing.T, targets []string, ok bool) {
	t.Helper()
	orig := attachTargets
	attachTargets = func() ([]string, bool) { return targets, ok }
	t.Cleanup(func() { attachTargets = orig })
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
	if got := len(m.autoStopTargets(time.Now())); got != 0 {
		t.Errorf("targets right after opening = %d, want 0", got)
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

	if w := got["idle"]; !w.last.Equal(updated) || !w.updated.Equal(updated) || w.inFlight != 2 {
		t.Errorf("idle = %+v, want the record's update time and 2 in flight", w)
	}
	if w := got["woken"]; !w.last.Equal(woke) || !w.updated.Equal(updated) {
		t.Errorf("woken = %+v, want last = the later worker start %v and updated = the record's %v", w, woke, updated)
	}
	for _, sid := range []string{"noworker", "norecord", "nodate"} {
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
// exactly as the refresh saw it, with no terminal attached, is stopped.
func TestAutoStopCmdChecksEachTargetAgain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	seen := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const same, moved = `"updatedAt":"2026-01-01T12:00:00Z"`, `"updatedAt":"2026-01-01T12:00:09Z"`
	for jobID, body := range map[string]string{
		"aaaa0001": `{"state":"working",` + same + `}`,
		"bbbb0001": `{"state":"blocked",` + same + `}`,
		"bad00000": `{"state":"working",` + same + `}`,
		"done0000": `{"state":"stopped",` + same + `}`,
		"act00000": `{"state":"working",` + moved + `}`,
		"task0000": `{"state":"working",` + same + `,"inFlight":{"tasks":1}}`,
		"att00000": `{"state":"working",` + same + `}`,
		"name0000": `{"state":"working",` + same + `}`,
		"half0000": `{"state":"work`,
	} {
		writeJobState(t, home, jobID, body)
	}
	stubAttachTargets(t, []string{"att00000", "hermes"}, true)
	log := fakeClaude(t)

	var targets []stopTarget
	for _, id := range []string{"aaaa0001", "done0000", "act00000", "task0000", "att00000", "half0000", "gone0000", "bad00000", "bbbb0001"} {
		targets = append(targets, stopTarget{jobID: id, updated: seen})
	}
	targets = append(targets, stopTarget{jobID: "name0000", name: "Hermes-Agent", updated: seen})
	msg := autoStopCmd(targets)().(autoStoppedMsg)

	if msg.stopped != 2 || msg.err == nil {
		t.Errorf("msg = %+v, want 2 stopped and the failure reported", msg)
	}
	calls, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(calls)); got != "stop aaaa0001\nstop bad00000\nstop bbbb0001" {
		t.Errorf("claude calls = %q, want only the three unchanged, unattached jobs", got)
	}
}

func TestAutoStopCmdStopsNothingWhenTheProcessScanFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeJobState(t, home, "aaaa0001", `{"state":"working","updatedAt":"2026-01-01T12:00:00Z"}`)
	stubAttachTargets(t, nil, false)
	log := fakeClaude(t)

	target := stopTarget{jobID: "aaaa0001", updated: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	msg := autoStopCmd([]stopTarget{target})().(autoStoppedMsg)

	if _, err := os.Stat(log); err == nil || msg.stopped != 0 {
		t.Errorf("msg = %+v: nothing may be stopped when cav cannot tell who is attached", msg)
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
	if m.unparked.Has("a-sid") {
		t.Error("the mark should be dropped")
	}
}
