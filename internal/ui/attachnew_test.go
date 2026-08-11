package ui

import (
	"testing"

	"github.com/omeryahud/cav/internal/claude"
)

// `cav -n -a`: once the created session registers (selectJobID resolves), the
// refresh handler must attach to it — returning a non-nil exec command — and
// consume the flag so stepping out doesn't re-attach.
func TestAttachNewAttachesOnRegistration(t *testing.T) {
	m := openModel(t, "fresh")
	sid := m.all[0].SessionID
	m.selectJobID = "job1"
	m.attachNew = true

	live := map[string]bool{sid: true}
	_, cmd := m.Update(refreshResult{
		sessions: m.all,
		roster:   claude.Roster{sid: "job1"},
		states:   map[string]string{sid: "working"},
		live:     live,
	})

	if m.attachNew {
		t.Error("attachNew should be consumed once the session registers")
	}
	if m.selectJobID != "" {
		t.Error("selectJobID should be consumed")
	}
	if cmd == nil {
		t.Fatal("want an attach command batched into the refresh result")
	}
	if cur := m.current(); cur == nil || cur.SessionID != sid {
		t.Errorf("cursor should be on the new session, got %+v", cur)
	}
}

// Without -a the same registration only highlights.
func TestSelectWithoutAttachOnlyHighlights(t *testing.T) {
	m := openModel(t, "fresh")
	sid := m.all[0].SessionID
	m.selectJobID = "job1"

	m.Update(refreshResult{
		sessions: m.all,
		roster:   claude.Roster{sid: "job1"},
		states:   map[string]string{sid: "working"},
		live:     map[string]bool{sid: true},
	})

	if cur := m.current(); cur == nil || cur.SessionID != sid {
		t.Errorf("cursor should be on the new session, got %+v", cur)
	}
	if m.attachNew {
		t.Error("attachNew should still be false")
	}
}
