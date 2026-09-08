package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/claude"
)

// dirModel builds a model whose sessions live in the given directories
// (one session per entry, named after its index).
func dirModel(t *testing.T, dirs ...string) *Model {
	t.Helper()
	m := openModel(t)
	m.input = textinput.New()
	for i, d := range dirs {
		m.all = append(m.all, claude.Session{
			SessionID: string(rune('a'+i)) + "-sid",
			Name:      string(rune('a' + i)),
			Kind:      "background",
			CWD:       d,
		})
	}
	m.recompute()
	return m
}

func TestDirEntriesAllFirstThenAlphabeticalWithCounts(t *testing.T) {
	m := dirModel(t, "/w/zeta", "/w/alpha", "/w/alpha", "/w/mid")
	got := m.dirEntries()
	want := []dirEntry{
		{"", "all", 4},
		{"/w/alpha", "alpha", 2},
		{"/w/mid", "mid", 1},
		{"/w/zeta", "zeta", 1},
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDirEntriesDisambiguateSameLeaf(t *testing.T) {
	m := dirModel(t, "/one/proj", "/two/proj")
	got := m.dirEntries()
	if got[1].label != "one/proj" || got[2].label != "two/proj" {
		t.Errorf("colliding leaves should show parent/leaf, got %q and %q", got[1].label, got[2].label)
	}
}

func TestTabCyclesDirectoryAndScopesTheList(t *testing.T) {
	m := dirModel(t, "/w/alpha", "/w/beta", "/w/beta")
	if len(m.view) != 3 || m.dirSel != "" {
		t.Fatalf("start: view=%d dirSel=%q, want all", len(m.view), m.dirSel)
	}
	m.handleListKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.dirSel != "/w/alpha" || len(m.view) != 1 {
		t.Errorf("tab: dirSel=%q view=%d, want alpha scoped to 1", m.dirSel, len(m.view))
	}
	m.handleListKey(tea.KeyMsg{Type: tea.KeyTab})
	if m.dirSel != "/w/beta" || len(m.view) != 2 {
		t.Errorf("tab: dirSel=%q view=%d, want beta scoped to 2", m.dirSel, len(m.view))
	}
	m.handleListKey(tea.KeyMsg{Type: tea.KeyTab}) // wraps back to all
	if m.dirSel != "" || len(m.view) != 3 {
		t.Errorf("wrap: dirSel=%q view=%d, want all", m.dirSel, len(m.view))
	}
	m.handleListKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.dirSel != "/w/beta" {
		t.Errorf("shift+tab from all should land on the last dir, got %q", m.dirSel)
	}
}

func TestScopedRowsDropTheDirPrefix(t *testing.T) {
	m := dirModel(t, "/w/alpha")
	if got := m.rowName(m.all[0]); got != "alpha/a" {
		t.Errorf("unscoped rowName = %q, want alpha/a", got)
	}
	m.dirSel = "/w/alpha"
	if got := m.rowName(m.all[0]); got != "a" {
		t.Errorf("scoped rowName = %q, want a", got)
	}
}

func TestSelectedDirFallsBackToAllWhenEmpty(t *testing.T) {
	m := dirModel(t, "/w/alpha", "/w/beta")
	m.dirSel = "/w/beta"
	m.all = m.all[:1] // beta's session is gone
	m.recompute()
	if m.dirSel != "" || len(m.view) != 1 {
		t.Errorf("dirSel=%q view=%d, want fallback to all", m.dirSel, len(m.view))
	}
}

func TestDotKeyTargetsSelectedDirThenLaunchDir(t *testing.T) {
	dot := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(".")}

	m := dirModel(t, "/w/alpha", "/w/beta")
	m.launchDir = "/launch/here"
	m.dirSel = "/w/beta"
	m.handleListKey(dot)
	if m.mode != modeNewName || m.newKind != kindDir || m.newCWD != "/w/beta" {
		t.Errorf("selected dir: mode=%v kind=%v cwd=%q", m.mode, m.newKind, m.newCWD)
	}

	m = dirModel(t, "/w/alpha")
	m.launchDir = "/launch/here"
	m.handleListKey(dot)
	if m.newCWD != "/launch/here" {
		t.Errorf("on all, . should fall back to the launch dir, got %q", m.newCWD)
	}

	m = dirModel(t, "/w/alpha")
	m.launchDir = ""
	m.handleListKey(dot)
	if m.mode != modeList {
		t.Errorf("with no selection and no launch dir, . should do nothing, mode=%v", m.mode)
	}
}

func TestFilterNarrowsDirectoryPane(t *testing.T) {
	m := dirModel(t, "/w/alpha", "/w/beta", "/w/beta")
	m.filter = "bet"
	m.recompute()
	got := m.dirEntries()
	if len(got) != 2 || got[0].count != 2 || got[1].path != "/w/beta" || got[1].count != 2 {
		t.Errorf("filtered entries = %+v, want all(2) and beta(2) only", got)
	}
	m.dirSel = "/w/alpha" // no session in alpha matches the filter
	m.recompute()
	if m.dirSel != "" {
		t.Errorf("a selected dir with no matching sessions should fall back to all, got %q", m.dirSel)
	}
}

func TestLaunchDirFocusedOnFirstRefresh(t *testing.T) {
	for _, tc := range []struct {
		name    string
		launch  string
		wantSel string
	}{
		{"launch dir has sessions", "/w/beta", "/w/beta"},
		{"launch dir has none", "/elsewhere", ""},
	} {
		m := dirModel(t, "/w/alpha", "/w/beta")
		m.launchDir, m.focusLaunchDir = tc.launch, true
		m.feedRefresh()
		if m.dirSel != tc.wantSel {
			t.Errorf("%s: dirSel = %q, want %q", tc.name, m.dirSel, tc.wantSel)
		}
		if m.focusLaunchDir {
			t.Errorf("%s: focus should be one-shot", tc.name)
		}
	}
}

func TestRemoveDirectoryMovesAllToStopped(t *testing.T) {
	m := dirModel(t, "/w/alpha", "/w/alpha", "/w/beta")
	m.all[0].Status = "idle" // live -> stop
	m.all[1].Status = ""     // dead -> dismiss
	m.roster = claude.Roster{m.all[0].SessionID: "job0"}
	m.recompute()

	m.dirSel = "/w/alpha"
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if m.mode != modeConfirm || m.pendingDir != "/w/alpha" {
		t.Fatalf("D should arm the confirm, mode=%v pendingDir=%q", m.mode, m.pendingDir)
	}
	m.handleConfirmKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.mode != modeList || m.pendingDir != "" {
		t.Errorf("confirm should clear pendingDir, mode=%v pendingDir=%q", m.mode, m.pendingDir)
	}
	if !m.justStopped[m.all[0].SessionID] {
		t.Error("the live alpha session should be optimistically stopped")
	}
	if !m.dismissed.Has(m.all[1].SessionID) {
		t.Error("the dead alpha session should be dismissed")
	}
	if m.dismissed.Has(m.all[2].SessionID) || m.justStopped[m.all[2].SessionID] {
		t.Error("the beta session must be untouched")
	}
	if m.dirSel != "" {
		t.Errorf("emptied selection should fall back to all, got %q", m.dirSel)
	}
}

func TestRemoveDirectoryCancelDoesNothing(t *testing.T) {
	m := dirModel(t, "/w/alpha")
	m.all[0].Status = ""
	m.dirSel = "/w/alpha"
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	m.handleConfirmKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.mode != modeList || m.pendingDir != "" {
		t.Errorf("n should cancel: mode=%v pendingDir=%q", m.mode, m.pendingDir)
	}
	if m.dismissed.Has(m.all[0].SessionID) {
		t.Error("cancel must not remove anything")
	}
}

func TestRemoveDirectoryOnAllTargetsHighlightedDir(t *testing.T) {
	m := dirModel(t, "/w/alpha", "/w/beta")
	m.all[0].Status, m.all[1].Status = "", ""
	m.recompute()
	m.cursor = 0
	target := m.view[0].CWD
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	if m.pendingDir != target {
		t.Errorf("on all, D targets the highlighted session's dir %q, got %q", target, m.pendingDir)
	}
}

func TestDirPaneWidth(t *testing.T) {
	m := dirModel(t, "/w/alpha")
	m.width = 160
	if got := m.dirPaneWidth(); got != 40 {
		t.Errorf("25%% of 160 = %d, want 40", got)
	}
	m.cfg.DirPane.WidthPercent = 0
	if got := m.dirPaneWidth(); got != 0 {
		t.Errorf("widthPercent 0 should hide the pane, got %d", got)
	}
	m.cfg.DirPane.WidthPercent = 25
	m.width = 50
	if got := m.dirPaneWidth(); got != 0 {
		t.Errorf("narrow terminal should hide the pane, got %d", got)
	}
}
