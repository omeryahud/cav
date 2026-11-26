package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/config"
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

// The loop itself: with the records it refreshes when a file changes and not
// in between, and r asks for a refresh at once.
func TestRefreshLoopWaitsForAChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessions := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	stubLiveRecords(t, nil, true)
	calls := stubLiveSessions(t, nil, nil)
	stubAttachTargets(t, nil, true)

	l := config.Defaults().List
	l.MinRefresh, l.SafetyPoll = time.Millisecond, time.Hour
	ch, act := make(chan refreshResult), newActivity()
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		refreshLoop(stop, ch, l, act, "", false)
		close(stopped)
	}()
	t.Cleanup(func() { close(stop); <-stopped }) // before the stubs are put back

	next := func(wait time.Duration) (refreshResult, bool) {
		select {
		case rr := <-ch:
			return rr, true
		case <-time.After(wait):
			return refreshResult{}, false
		}
	}
	if rr, ok := next(5 * time.Second); !ok || !rr.events {
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
	if _, ok := next(5 * time.Second); !ok {
		t.Error("a changed record must start a refresh")
	}

	before := *calls
	act.refreshNow()
	if _, ok := next(5 * time.Second); !ok || *calls != before+1 {
		t.Errorf("r: got a refresh = %v, claude runs %d to %d; want a refresh with a full listing", ok, before, *calls)
	}
}
