package ui

import (
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/config"
)

func TestIdleDelay(t *testing.T) {
	l := config.List{IdleAfter: 60 * time.Second, IdleRefresh: 10 * time.Second}
	for _, tc := range []struct {
		name  string
		since time.Duration
		want  time.Duration
	}{
		{"just active", 0, 0},
		{"under threshold", 59 * time.Second, 0},
		{"over threshold", 61 * time.Second, 10 * time.Second},
		{"long idle", time.Hour, 10 * time.Second},
	} {
		if got := idleDelay(l, tc.since); got != tc.want {
			t.Errorf("%s: idleDelay = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIdleDelayDisabled(t *testing.T) {
	l := config.List{IdleAfter: 0, IdleRefresh: 10 * time.Second}
	if got := idleDelay(l, time.Hour); got != 0 {
		t.Errorf("IdleAfter 0 must disable the backoff, got %v", got)
	}
}

// touch must stamp recency and leave a wake token; sinceInput reflects it.
func TestActivityTouchAndWake(t *testing.T) {
	a := newActivity()
	if a.sinceInput() > time.Second {
		t.Error("fresh activity should read as just-active")
	}
	// Drain the token deposited by newActivity's initial touch.
	select {
	case <-a.wake:
	default:
		t.Error("touch should have left a wake token")
	}
	a.touch()
	select {
	case <-a.wake:
	default:
		t.Error("second touch should deposit a new token")
	}
	// A second token never accumulates (buffered 1, non-blocking send).
	a.touch()
	a.touch()
	<-a.wake
	select {
	case <-a.wake:
		t.Error("wake channel should hold at most one token")
	default:
	}
}

func TestGroupingFromConfig(t *testing.T) {
	for v, want := range map[string]grouping{
		"dir-status":   groupDirStatus,
		"status-dir":   groupStatusDir,
		"recent":       groupRecent,
		"alphabetical": groupNone,
		"":             groupStatusDir, // config default wins for anything else
	} {
		if got := groupingFromConfig(v); got != want {
			t.Errorf("groupingFromConfig(%q) = %v, want %v", v, got, want)
		}
	}
}
