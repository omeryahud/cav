package ui

import (
	"testing"

	"github.com/omeryahud/cav/internal/claude"
)

func TestApplyWorktreePaths(t *testing.T) {
	sessions := []claude.Session{
		{SessionID: "live-done", CWD: "/repo"}, // daemon reports the base once done
		{SessionID: "plain", CWD: "/plain"},    // no worktree
		{SessionID: "ondisk", CWD: "/repo"},    // on-disk base cwd
	}
	wtByS := map[string]string{
		"live-done": "/repo/.claude/worktrees/feat",
		"ondisk":    "/repo/.claude/worktrees/other",
	}
	applyWorktreePaths(sessions, wtByS)

	if sessions[0].CWD != "/repo/.claude/worktrees/feat" {
		t.Errorf("live worktree session CWD = %q, want the worktree path", sessions[0].CWD)
	}
	if sessions[1].CWD != "/plain" {
		t.Errorf("non-worktree session CWD = %q, want it unchanged", sessions[1].CWD)
	}
	if sessions[2].CWD != "/repo/.claude/worktrees/other" {
		t.Errorf("on-disk worktree session CWD = %q, want the worktree path", sessions[2].CWD)
	}
}
