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

	got := map[string]string{}
	for _, j := range ScanJobs() {
		got[j.SessionID] = j.CWD
	}
	if got["sid-wt"] != "/repo/.claude/worktrees/feat" {
		t.Errorf("worktree session CWD = %q, want the worktreePath", got["sid-wt"])
	}
	if got["sid-plain"] != "/plain" {
		t.Errorf("plain session CWD = %q, want the cwd", got["sid-plain"])
	}
}
