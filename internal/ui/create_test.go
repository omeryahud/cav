package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/omeryahud/cav/internal/claude"
)

func enterKey() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

func wizardModel(t *testing.T) *Model {
	m := openModel(t, "parent")
	m.all[0].CWD = "/work/proj"
	m.view = m.all
	m.input = textinput.New()
	m.roster = claude.Roster{m.all[0].SessionID: "job9"}
	return m
}

func TestAKeyStartsWizardInHighlightedDir(t *testing.T) {
	m := wizardModel(t)
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if m.mode != modeNewName || m.newKind != kindDir || m.newCWD != "/work/proj" {
		t.Errorf("mode=%v kind=%v cwd=%q", m.mode, m.newKind, m.newCWD)
	}
}

func TestNameStepRefusesEmpty(t *testing.T) {
	m := wizardModel(t)
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	m.input.SetValue("   ")
	m.handleNewNameKey(enterKey())
	if m.mode != modeNewName {
		t.Errorf("empty name must be refused, mode=%v", m.mode)
	}
	if m.status == "" {
		t.Error("refusal should explain itself in the footer")
	}
	m.input.SetValue("real-name")
	m.handleNewNameKey(enterKey())
	if m.mode != modeNew || m.newName != "real-name" {
		t.Errorf("named enter should advance to the prompt step, mode=%v name=%q", m.mode, m.newName)
	}
}

func TestForkAndClonePromptForNameFirst(t *testing.T) {
	for _, tc := range []struct {
		key  string
		kind createKind
	}{
		{"F", kindFork},
		{"C", kindClone},
	} {
		m := wizardModel(t)
		m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
		if m.mode != modeNewName || m.newKind != tc.kind || m.newParent == nil {
			t.Fatalf("%s: mode=%v kind=%v parent=%v", tc.key, m.mode, m.newKind, m.newParent)
		}
		if tc.kind == kindClone && m.input.Value() != "copy-parent" {
			t.Errorf("clone prefill = %q, want copy-parent", m.input.Value())
		}
		if tc.kind == kindFork && m.input.Value() != "parent" {
			t.Errorf("fork prefill = %q, want parent", m.input.Value())
		}
		m.input.SetValue("child-name")
		_, cmd := m.handleNewNameKey(enterKey())
		if cmd == nil {
			t.Fatalf("%s: named enter should fire the fork/clone command", tc.key)
		}
		if m.mode != modeList || m.newParent != nil {
			t.Errorf("%s: wizard should close and drop the snapshot", tc.key)
		}
	}
}

func TestForkNameStepEscCreatesNothing(t *testing.T) {
	m := wizardModel(t)
	m.handleListKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("F")})
	_, cmd := m.handleNewNameKey(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil || m.mode != modeList || m.newParent != nil {
		t.Errorf("esc must cancel without creating: cmd=%v mode=%v parent=%v", cmd, m.mode, m.newParent)
	}
}

func TestForkedMsgHidesBothKindsUntilNamed(t *testing.T) {
	for _, record := range []bool{true, false} {
		m := wizardModel(t)
		m.Update(forkedMsg{childJobID: "j2", parentSID: m.all[0].SessionID, cloneName: "child-name", record: record})
		if m.pendingClone["j2"] != "child-name" {
			t.Errorf("record=%v: child should stay hidden until it appears as child-name", record)
		}
		if m.selectJobID != "j2" {
			t.Errorf("record=%v: child should be highlighted once it appears", record)
		}
	}
}
