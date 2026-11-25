package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestScanJobsCountsInFlightWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeJob(t, home, "aaaa1111", `{"sessionId":"busy-bg","cwd":"/a","inFlight":{"tasks":2,"queued":1,"kinds":["monitor"]}}`)
	writeJob(t, home, "bbbb2222", `{"sessionId":"quiet","cwd":"/b","inFlight":{"tasks":0,"queued":0}}`)
	writeJob(t, home, "cccc3333", `{"sessionId":"old-record","cwd":"/c"}`)

	got := map[string]int{}
	for _, j := range ScanJobs() {
		got[j.SessionID] = j.InFlight
	}
	if got["busy-bg"] != 2 || got["quiet"] != 0 || got["old-record"] != 0 {
		t.Errorf("InFlight = %v, want busy-bg 2 (tasks only), quiet 0, old-record 0", got)
	}
}

// A message queue that rests above zero is not background work: counting it
// would keep an idle session alive forever.
func TestScanJobsIgnoresTheMessageQueue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeJob(t, home, "aaaa1111", `{"sessionId":"s","inFlight":{"tasks":0,"queued":1}}`)

	if recs := ScanJobs(); len(recs) != 1 || recs[0].InFlight != 0 {
		t.Errorf("records = %+v, want one with InFlight 0", recs)
	}
}

func TestReadJob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeJob(t, home, "aaaa1111", `{"sessionId":"s","state":"working","updatedAt":"2026-01-01T00:00:00Z","inFlight":{"tasks":1}}`)
	writeJob(t, home, "bbbb2222", `{"sessionId":"s`)

	rec, ok := ReadJob("aaaa1111")
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !ok || rec.JobID != "aaaa1111" || rec.State != "working" || rec.InFlight != 1 || !rec.UpdatedAt.Equal(want) {
		t.Errorf("ReadJob = %+v, %v", rec, ok)
	}
	for _, id := range []string{"bbbb2222", "missing0", ""} {
		if _, ok := ReadJob(id); ok {
			t.Errorf("ReadJob(%q) ok = true, want false for a half-written, missing or unnamed job", id)
		}
	}
}
