package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJob(t *testing.T, home, jobID, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "jobs", jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanJobsPrefersWorktreePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeJob(t, home, "aaaa1111", `{"sessionId":"sid-wt","cwd":"/repo","worktreePath":"/repo/.claude/worktrees/feat","name":"wt","state":"working","updatedAt":"2026-01-01T00:00:00Z"}`)
	writeJob(t, home, "bbbb2222", `{"sessionId":"sid-plain","cwd":"/plain","name":"plain","state":"working","updatedAt":"2026-01-01T00:00:00Z"}`)

	got := map[string]JobRecord{}
	for _, j := range ScanJobs() {
		got[j.SessionID] = j
	}
	if got["sid-wt"].CWD != "/repo" || got["sid-wt"].WorktreePath != "/repo/.claude/worktrees/feat" {
		t.Errorf("worktree record = cwd %q wt %q, want /repo and the worktreePath", got["sid-wt"].CWD, got["sid-wt"].WorktreePath)
	}
	if got["sid-plain"].CWD != "/plain" || got["sid-plain"].WorktreePath != "" {
		t.Errorf("plain record = cwd %q wt %q, want /plain and empty", got["sid-plain"].CWD, got["sid-plain"].WorktreePath)
	}
}
