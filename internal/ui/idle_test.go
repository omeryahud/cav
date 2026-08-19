package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

// The tmux attach flavors must not yank a cursor the user moved: the
// re-highlight applies only when no cav keypress landed after the attach began.
func TestSelectUnlessTouched(t *testing.T) {
	a := newActivity()
	start := time.Now().Add(10 * time.Millisecond) // strictly after the opening keypress
	if got := selectUnlessTouched(a, start, "job1"); got != "job1" {
		t.Errorf("untouched: got %q, want job1", got)
	}
	time.Sleep(20 * time.Millisecond)
	a.touch() // the user moved the cursor while the session was open
	if got := selectUnlessTouched(a, start, "job1"); got != "" {
		t.Errorf("touched: got %q, want empty", got)
	}
	if got := selectUnlessTouched(nil, start, "job1"); got != "job1" {
		t.Errorf("nil activity should fall back to restoring, got %q", got)
	}
}

// pgup/pgdn jump to the first/last session; alt+up/down move five.
func TestListNavKeys(t *testing.T) {
	m := openModel(t, "a", "b", "c", "d", "e", "f", "g", "h")
	m.view = m.all
	press := func(k tea.KeyMsg) { m.handleListKey(k) }

	press(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.cursor != 7 {
		t.Errorf("pgdown: cursor = %d, want 7", m.cursor)
	}
	press(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.cursor != 0 {
		t.Errorf("pgup: cursor = %d, want 0", m.cursor)
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlDown})
	if m.cursor != 5 {
		t.Errorf("ctrl+down: cursor = %d, want 5", m.cursor)
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlDown}) // clamps at the end
	if m.cursor != 7 {
		t.Errorf("ctrl+down clamp: cursor = %d, want 7", m.cursor)
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlUp})
	if m.cursor != 2 {
		t.Errorf("ctrl+up: cursor = %d, want 2", m.cursor)
	}
	press(tea.KeyMsg{Type: tea.KeyCtrlUp}) // clamps at the top
	if m.cursor != 0 {
		t.Errorf("ctrl+up clamp: cursor = %d, want 0", m.cursor)
	}
}
