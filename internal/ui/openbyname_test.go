package ui

import (
	"strings"
	"testing"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/config"
	"github.com/omeryahud/cav/internal/dismiss"
	"github.com/omeryahud/cav/internal/entered"
	"github.com/omeryahud/cav/internal/forks"
	"github.com/omeryahud/cav/internal/unpark"
)

// openModel is resolveModel plus everything Update/recompute touch.
func openModel(t *testing.T, sessionNames ...string) *Model {
	t.Helper()
	m := resolveModel(t, sessionNames...)
	m.cfg = config.Defaults()
	m.dismissed = dismiss.Load()
	m.forks = forks.Load()
	m.unparked = unpark.Load()
	m.entered = entered.Load()
	m.states = map[string]string{}
	m.live = map[string]bool{}
	m.justStopped = map[string]bool{}
	m.pendingClone = map[string]string{}
	m.prevCache = map[string]string{}
	m.prevReq = map[string]bool{}
	return m
}

func (m *Model) feedRefresh() {
	sessions := m.all
	live := map[string]bool{}
	for _, s := range sessions {
		live[s.SessionID] = true
	}
	m.Update(refreshResult{sessions: sessions, roster: claude.Roster{}, states: map[string]string{}, live: live})
}

func TestOpenByNameNoMatchFallsBackToFilter(t *testing.T) {
	m := openModel(t, "aaa", "bbb")
	m.autoOpen = "zzz"
	m.feedRefresh()
	if m.autoOpen != "" {
		t.Error("autoOpen should be consumed one-shot")
	}
	if m.filter != "zzz" {
		t.Errorf("filter = %q, want %q", m.filter, "zzz")
	}
	if !strings.Contains(m.status, "no session named") {
		t.Errorf("status = %q, want a no-session note", m.status)
	}
	// The note must survive subsequent refreshes until statusTTL, not be
	// swept by the expiry bookkeeping.
	m.feedRefresh()
	m.feedRefresh()
	if !strings.Contains(m.status, "no session named") {
		t.Errorf("status cleared by later refreshes: %q", m.status)
	}
}

func TestOpenByNameAmbiguousReportsCount(t *testing.T) {
	m := openModel(t, "deploy-a", "deploy-b")
	m.autoOpen = "deploy"
	m.feedRefresh()
	if m.filter != "deploy" || !strings.Contains(m.status, "2 sessions match") {
		t.Errorf("filter=%q status=%q, want filter fallback with a pick-one note", m.filter, m.status)
	}
}
