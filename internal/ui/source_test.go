package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/config"
	"github.com/omeryahud/cav/internal/unpark"
)

func jobsOf(sessions []claude.Session) []string {
	out := make([]string, len(sessions))
	for i, s := range sessions {
		out[i] = s.JobID + ":" + s.Status
	}
	return out
}

func worker(job, status string) claude.Session {
	return claude.Session{JobID: job, SessionID: job + "-sid", Kind: "background", Status: status, StatusSince: 5}
}

// The records are the truth for sessions with a process. The listing still
// gives the ones the daemon lists with no process, and its order.
func TestMergeListing(t *testing.T) {
	listing := []claude.Session{
		{JobID: "nopr0001"},                   // listed, no process
		{JobID: "aaaa0001", Status: "idle"},   // stale: the record says busy now
		{JobID: "gone0001", Status: "busy"},   // its process ended since the listing
		{Kind: "interactive", SessionID: "i"}, // filtered later by doRefresh
	}
	workers := []claude.Session{worker("bbbb0001", "idle"), worker("aaaa0001", "busy")}

	got := jobsOf(mergeListing(workers, listing))
	want := []string{"nopr0001:", "aaaa0001:busy", ":", "bbbb0001:idle"}
	if len(got) != len(want) {
		t.Fatalf("merged = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("merged[%d] = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
	if s := mergeListing(workers, listing)[1]; s.StatusSince != 5 {
		t.Errorf("a worker must come from its record, with its status time: %+v", s)
	}
}

// Starting claude is the costly part of a refresh. With the records it must
// run only when a worker starts or ends, on r, and once per safety interval.
func TestSessionSourceStartsClaudeOnlyWhenNeeded(t *testing.T) {
	workers := []claude.Session{worker("aaaa0001", "idle")}
	stubLiveRecords(t, workers, true)
	calls := stubLiveSessions(t, []claude.Session{{JobID: "nopr0001"}}, nil)
	src := &sessionSource{safetyPoll: time.Hour}
	ctx := context.Background()

	list := func(step string, wantCalls int) []claude.Session {
		t.Helper()
		got, fromRecords, err := src.list(ctx)
		if err != nil || !fromRecords || *calls != wantCalls {
			t.Fatalf("%s: err = %v, fromRecords = %v, claude runs = %d; want nil, true, %d", step, err, fromRecords, *calls, wantCalls)
		}
		return got
	}

	if got := jobsOf(list("first refresh", 1)); len(got) != 2 || got[0] != "nopr0001:" || got[1] != "aaaa0001:idle" {
		t.Errorf("first refresh = %q", got)
	}
	workers[0].Status = "busy" // a turn started: the record changed, the worker set did not
	if got := jobsOf(list("a status change", 1)); got[1] != "aaaa0001:busy" {
		t.Errorf("a status change must show without a listing: %q", got)
	}

	stubLiveRecords(t, append(workers, worker("bbbb0001", "idle")), true)
	list("a worker started", 2)
	list("nothing changed", 2)

	src.force = true
	list("the user pressed r", 3)
	if src.force {
		t.Error("the forced listing must be used up")
	}

	src.listedAt = time.Now().Add(-2 * time.Hour)
	list("the safety interval passed", 4)
}

// A listing that fails keeps the last one, and is not tried on every refresh:
// under load each try costs seconds.
func TestSessionSourceKeepsTheLastListingWhenClaudeFails(t *testing.T) {
	workers := []claude.Session{worker("aaaa0001", "idle")}
	stubLiveRecords(t, workers, true)
	stubLiveSessions(t, []claude.Session{{JobID: "nopr0001"}}, nil)
	src := &sessionSource{safetyPoll: time.Hour}
	if _, _, err := src.list(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := stubLiveSessions(t, nil, errors.New("timed out"))
	stubLiveRecords(t, append(workers, worker("bbbb0001", "idle")), true)
	for i := 0; i < 3; i++ {
		got, fromRecords, err := src.list(context.Background())
		if err != nil || !fromRecords || len(got) != 3 {
			t.Fatalf("try %d: got %q, fromRecords %v, err %v; want the two workers and the kept entry", i, jobsOf(got), fromRecords, err)
		}
	}
	if *calls != 1 {
		t.Errorf("claude ran %d times across three refreshes, want 1", *calls)
	}
}

// Without the records cav asks claude on every refresh, as it always did.
func TestSessionSourceFallsBackToClaude(t *testing.T) {
	stubLiveRecords(t, nil, false)
	calls := stubLiveSessions(t, []claude.Session{{JobID: "aaaa0001", Status: "idle"}}, nil)
	src := &sessionSource{safetyPoll: time.Hour}

	for i := 1; i <= 2; i++ {
		got, fromRecords, err := src.list(context.Background())
		if err != nil || fromRecords || len(got) != 1 || *calls != i {
			t.Fatalf("refresh %d: got %q, fromRecords %v, err %v, runs %d", i, jobsOf(got), fromRecords, err, *calls)
		}
	}

	stubLiveSessions(t, nil, errors.New("daemon down"))
	if _, _, err := src.list(context.Background()); err == nil {
		t.Error("with no records a failed listing must be reported")
	}
}

// With no file events there is nothing to pace a read of the records, so cav
// asks the CLI on every refresh, as it did before it read them.
func TestSessionSourcePollsTheCLIWithoutAWatcher(t *testing.T) {
	stubLiveRecords(t, []claude.Session{worker("aaaa0001", "idle")}, true)
	calls := stubLiveSessions(t, []claude.Session{{JobID: "aaaa0001", Status: "idle"}}, nil)
	src := &sessionSource{safetyPoll: time.Hour, polling: true}

	for i := 1; i <= 2; i++ {
		if _, fromRecords, err := src.list(context.Background()); err != nil || fromRecords || *calls != i {
			t.Fatalf("refresh %d: fromRecords %v, err %v, claude runs %d; want a CLI listing each time", i, fromRecords, err, *calls)
		}
	}
}

// The watch on a worker's job must be set before the listing and the job
// files are read, or a change in between is never seen.
func TestSessionSourceTracksTheWorkersBeforeItLists(t *testing.T) {
	stubLiveRecords(t, []claude.Session{worker("aaaa0001", "idle"), worker("bbbb0001", "busy")}, true)
	calls := stubLiveSessions(t, nil, nil)
	var tracked []string
	listedBefore := -1
	src := &sessionSource{safetyPoll: time.Hour, track: func(jobs []string) { tracked, listedBefore = jobs, *calls }}

	if _, _, err := src.list(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tracked) != 2 || tracked[0] != "aaaa0001" || tracked[1] != "bbbb0001" || listedBefore != 0 {
		t.Errorf("tracked %q after %d listings; want both workers, before the first listing", tracked, listedBefore)
	}
}

func TestEventInterval(t *testing.T) {
	l := config.Defaults().List
	if got := eventInterval(l, false); got != time.Minute {
		t.Errorf("auto-stop off: %v, want the safety interval", got)
	}
	if got := eventInterval(l, true); got != 10*time.Second {
		t.Errorf("auto-stop on: %v, want a steady look at who is attached", got)
	}
	l.IdleRefresh = 5 * time.Minute
	if got := eventInterval(l, true); got != time.Minute {
		t.Errorf("a long idle interval: %v, want the safety interval", got)
	}
}

// loopHome is an empty home with claude's process records directory.
func loopHome(t *testing.T) (sessions string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	sessions = filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	return sessions
}

// slowRefresh is how long a test waits for a refresh that must come. On a
// loaded machine one refresh can take seconds.
const slowRefresh = 15 * time.Second

// countListings replaces the `claude agents` call and counts its runs. The
// count is safe to read while the loop runs.
func countListings(t *testing.T) *atomic.Int32 {
	t.Helper()
	var runs atomic.Int32
	orig := liveSessions
	liveSessions = func(context.Context) ([]claude.Session, error) { runs.Add(1); return nil, nil }
	t.Cleanup(func() { liveSessions = orig })
	return &runs
}

// startLoop runs the refresh loop until the test ends. Call it after the
// stubs: the loop must stop before they are put back.
func startLoop(t *testing.T, l config.List) (next func(wait time.Duration) (refreshResult, bool), act *activity) {
	t.Helper()
	ch := make(chan refreshResult)
	act = newActivity()
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		refreshLoop(stop, ch, l, act, "", false)
		close(stopped)
	}()
	t.Cleanup(func() { close(stop); <-stopped })
	return func(wait time.Duration) (refreshResult, bool) {
		select {
		case rr := <-ch:
			return rr, true
		case <-time.After(wait):
			return refreshResult{}, false
		}
	}, act
}

// The loop itself: with the records it refreshes when a file changes and not
// in between, and r asks for a refresh at once.
func TestRefreshLoopWaitsForAChange(t *testing.T) {
	sessions := loopHome(t)
	stubLiveRecords(t, nil, true)
	listings := countListings(t)
	stubAttachTargets(t, nil, true)
	l := config.Defaults().List
	l.MinRefresh, l.SafetyPoll = time.Millisecond, time.Hour
	next, act := startLoop(t, l)

	if rr, ok := next(slowRefresh); !ok || !rr.events {
		t.Fatalf("first refresh: got one = %v, events = %v; want an event-driven refresh", ok, rr.events)
	}
	if _, ok := next(700 * time.Millisecond); ok {
		t.Error("nothing changed: there must be no refresh")
	}
	act.touch() // a keypress alone is not a reason to refresh
	if _, ok := next(300 * time.Millisecond); ok {
		t.Error("a keypress must not start a refresh")
	}

	if err := os.WriteFile(filepath.Join(sessions, "123.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := next(slowRefresh); !ok {
		t.Error("a changed record must start a refresh")
	}

	// A late event of that write may still bring a refresh of its own first.
	before := listings.Load()
	act.refreshNow()
	for listings.Load() == before {
		if _, ok := next(slowRefresh); !ok {
			t.Fatal("r must start a refresh with a full listing")
		}
	}
}

// One write can give several events. They must start one refresh, after the
// write is done, not one each.
func TestWaitMergesTheEventsOfOneWrite(t *testing.T) {
	changed := make(chan struct{}, 1)
	l := config.Defaults().List
	l.MinRefresh = 500 * time.Millisecond
	r := &refresher{l: l, act: newActivity(), src: &sessionSource{}, changed: changed}

	changed <- struct{}{} // the first event of the write
	start, due := time.Now(), make(chan bool)
	go func() { due <- r.wait(true) }()
	changed <- struct{}{} // the second: it gets in once wait took the first
	if !<-due || time.Since(start) < l.MinRefresh {
		t.Errorf("wait returned after %v; want it to wait %v for the rest of the write", time.Since(start), l.MinRefresh)
	}
	select {
	case <-changed:
		t.Error("the second event was left: it would start a second refresh")
	default:
	}
}

// What ends a wait in event mode: r (with a full listing), the interval, a stop.
func TestWaitEndsOnRTheIntervalAndAStop(t *testing.T) {
	l := config.Defaults().List
	l.SafetyPoll = time.Hour
	stop := make(chan struct{})
	r := &refresher{l: l, act: newActivity(), src: &sessionSource{}, stop: stop}

	r.act.refreshNow()
	if !r.wait(true) || !r.src.force {
		t.Error("r must end the wait and ask for a full listing")
	}

	r.l.SafetyPoll = 10 * time.Millisecond
	if !r.wait(true) {
		t.Error("the interval must end the wait")
	}

	r.l.SafetyPoll = time.Hour
	close(stop)
	if r.wait(true) {
		t.Error("a stop must end the loop")
	}
}

// Another cav saves its marks in unparked.json. That is no file of claude's,
// so the loop must name it to the watcher, or the marks show a minute late.
func TestRefreshLoopRefreshesWhenAnotherCavSavesItsMarks(t *testing.T) {
	loopHome(t)
	marks := unpark.Path()
	if err := os.MkdirAll(filepath.Dir(marks), 0o755); err != nil {
		t.Fatal(err)
	}
	stubLiveRecords(t, nil, true)
	stubLiveSessions(t, nil, nil)
	stubAttachTargets(t, nil, true)
	l := config.Defaults().List
	l.MinRefresh, l.SafetyPoll = time.Millisecond, time.Hour
	next, _ := startLoop(t, l)
	if _, ok := next(slowRefresh); !ok {
		t.Fatal("no first refresh")
	}

	if err := os.WriteFile(marks, []byte(`["a-sid"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := next(slowRefresh); !ok {
		t.Error("another cav's marks must start a refresh")
	}
}

// When the records cannot be read the loop polls the CLI. Its nap must still
// end on r and on a file event, and the events must take over again after.
func TestRefreshLoopPollsWhenTheRecordsFailAndComesBack(t *testing.T) {
	sessions := loopHome(t)
	var recordsOK atomic.Bool
	orig := liveRecords
	liveRecords = func() ([]claude.Session, bool) { return nil, recordsOK.Load() }
	t.Cleanup(func() { liveRecords = orig })
	polls := countListings(t)
	stubAttachTargets(t, nil, true)
	l := config.Defaults().List
	l.MinRefresh, l.SafetyPoll = time.Millisecond, time.Hour
	l.IdleAfter, l.IdleRefresh = time.Millisecond, time.Hour // the user is away: the poll naps
	next, act := startLoop(t, l)

	if rr, ok := next(slowRefresh); !ok || rr.events || polls.Load() != 1 {
		t.Fatalf("first refresh: got one = %v, events = %v, claude runs %d; want a poll", ok, rr.events, polls.Load())
	}
	if _, ok := next(700 * time.Millisecond); ok {
		t.Fatal("the user is away: the poll must nap")
	}

	act.refreshNow()
	if rr, ok := next(slowRefresh); !ok || rr.events || polls.Load() != 2 {
		t.Errorf("r during the nap: got a refresh = %v, events = %v, claude runs %d; want one more poll", ok, rr.events, polls.Load())
	}

	recordsOK.Store(true)
	if err := os.WriteFile(filepath.Join(sessions, "123.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rr, ok := next(slowRefresh); !ok || !rr.events {
		t.Errorf("a file event during the nap: got a refresh = %v, events = %v; want an event-driven one", ok, rr.events)
	}
}

// With nothing to watch (an older claude) the loop polls, and it starts to
// watch as soon as the process records appear.
func TestRefreshLoopStartsToWatchWhenTheRecordsAppear(t *testing.T) {
	sessions := loopHome(t)
	if err := os.Remove(sessions); err != nil {
		t.Fatal(err)
	}
	stubLiveRecords(t, nil, true)
	polls := countListings(t)
	stubAttachTargets(t, nil, true)
	l := config.Defaults().List
	l.MinRefresh, l.SafetyPoll = time.Millisecond, time.Hour
	next, _ := startLoop(t, l)

	for i := int32(1); i <= 2; i++ {
		if rr, ok := next(slowRefresh); !ok || rr.events || polls.Load() < i {
			t.Fatalf("poll %d: got one = %v, events = %v, claude runs %d; want a CLI poll", i, ok, rr.events, polls.Load())
		}
	}
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // one poll may have started before the directory was there
		if rr, ok := next(slowRefresh); ok && rr.events {
			return
		}
	}
	t.Error("the process records appeared: the loop must turn to file events")
}
