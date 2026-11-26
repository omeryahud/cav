package claude

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// sessionHome returns an empty home with a sessions directory, and forgets the
// processes verified so far.
func sessionHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(home+"/.claude/sessions", 0o755); err != nil {
		t.Fatal(err)
	}
	verified.start = map[int]string{}
	return home
}

func TestLiveSessionsReadsTheWorkersAsTheListingShowsThem(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	started := procStart(me)
	if started == "" {
		t.Skip("cannot read a process start time here")
	}
	rec := func(job, status, extra string) string {
		return fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","jobId":%q,"sessionId":"%s-sid","cwd":"/w","name":"n-%s","startedAt":111,"status":%q,"statusUpdatedAt":222%s}`,
			me, started, job, job, job, status, extra)
	}
	// The file name is the pid; these stand in for four processes.
	writeSessionFile(t, home, 1, rec("aaaa0001", "idle", ""))
	writeSessionFile(t, home, 2, rec("bbbb0001", "shell", ""))
	writeSessionFile(t, home, 3, rec("cccc0001", "busy", `,"spare":true`))
	writeSessionFile(t, home, 4, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"interactive","sessionId":"x","status":"idle"}`, me, started))

	got, ok := LiveSessions()
	if !ok || len(got) != 2 {
		t.Fatalf("LiveSessions() = %+v, %v; want the two background workers", got, ok)
	}
	byJob := map[string]Session{}
	for _, s := range got {
		byJob[s.JobID] = s
	}
	a := byJob["aaaa0001"]
	want := Session{JobID: "aaaa0001", PID: me, CWD: "/w", Kind: "background", StartedAt: 111,
		SessionID: "aaaa0001-sid", Name: "n-aaaa0001", Status: "idle", StatusSince: 222}
	if a != want {
		t.Errorf("idle worker = %+v, want %+v", a, want)
	}
	if byJob["bbbb0001"].Status != "busy" {
		t.Errorf("a worker running a shell is listed as busy, got %q", byJob["bbbb0001"].Status)
	}
	if s, ok := LiveSession("bbbb0001"); !ok || s.SessionID != "bbbb0001-sid" {
		t.Errorf("LiveSession = %+v, %v", s, ok)
	}
	if _, ok := LiveSession("zzzz0001"); ok {
		t.Error("a job with no process has no live session")
	}
}

// When the records cannot be relied on, the caller must ask the CLI.
func TestLiveSessionsSaysWhenItCannotTell(t *testing.T) {
	me := os.Getpid()
	started := procStart(me)
	for name, setup := range map[string]func(home string){
		"no sessions directory": func(home string) { os.RemoveAll(home + "/.claude") },
		"a half-written record": func(home string) { writeSessionFile(t, home, 1, `{"pid":1,"kind":"bg","jobId":"aaaa`) },
		"a worker with no status": func(home string) {
			writeSessionFile(t, home, 1, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","jobId":"aaaa0001","sessionId":"s"}`, me, started))
		},
	} {
		home := sessionHome(t)
		setup(home)
		if got, ok := LiveSessions(); ok {
			t.Errorf("%s: LiveSessions() = %+v, true; want false", name, got)
		}
	}

	sessionHome(t) // an empty directory is a clear answer: no workers
	if got, ok := LiveSessions(); !ok || len(got) != 0 {
		t.Errorf("no records: LiveSessions() = %+v, %v; want none, true", got, ok)
	}
}

// A record can outlive its process, and the pid can go to another process.
func TestLiveSessionsSkipsARecordWhoseProcessIsGone(t *testing.T) {
	home := sessionHome(t)
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip("cannot run a helper process: " + err.Error())
	}
	me := os.Getpid()
	const body = `{"pid":%d,"procStart":%q,"kind":"bg","jobId":%q,"sessionId":"s","status":"idle"}`
	writeSessionFile(t, home, 1, fmt.Sprintf(body, gone.Process.Pid, "Mon Jan  1 00:00:00 2001", "dead0001"))
	writeSessionFile(t, home, 2, fmt.Sprintf(body, me, "Mon Jan  1 00:00:00 2001", "reused01"))

	if got, ok := LiveSessions(); !ok || len(got) != 0 {
		t.Errorf("LiveSessions() = %+v, %v; want none: one process is gone, one pid was reused", got, ok)
	}
}

// What a refresh costs in place of one `claude agents --json`: reading the
// records of 10 workers.
func BenchmarkLiveSessions(b *testing.B) {
	home := b.TempDir()
	b.Setenv("HOME", home)
	dir := home + "/.claude/sessions"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	me := os.Getpid()
	orig := procStart
	procStart = func(int) string { return "Thu Nov 26 10:00:00 2026" }
	b.Cleanup(func() { procStart = orig })
	for i := 0; i < 10; i++ {
		body := fmt.Sprintf(`{"pid":%d,"procStart":"Thu Nov 26 10:00:00 2026","kind":"bg","jobId":"aaaa%04d","sessionId":"s%d","cwd":"/w","name":"n","startedAt":1,"status":"idle","statusUpdatedAt":2}`, me, i, i)
		if err := os.WriteFile(fmt.Sprintf("%s/%d.json", dir, i), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got, ok := LiveSessions(); !ok || len(got) != 10 {
			b.Fatalf("LiveSessions() = %d, %v", len(got), ok)
		}
	}
}

// The start time is checked with ps once per process, not on every refresh.
func TestLiveSessionsChecksAProcessOnce(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	calls := 0
	orig := procStart
	procStart = func(int) string { calls++; return "Thu Nov 26 10:00:00 2026" }
	t.Cleanup(func() { procStart = orig })
	writeSessionFile(t, home, 1, fmt.Sprintf(`{"pid":%d,"procStart":"Thu Nov 26 10:00:00 2026","kind":"bg","jobId":"aaaa0001","sessionId":"s","status":"idle"}`, me))

	for i := 0; i < 3; i++ {
		if got, ok := LiveSessions(); !ok || len(got) != 1 {
			t.Fatalf("LiveSessions() = %+v, %v", got, ok)
		}
	}
	if calls != 1 {
		t.Errorf("ps ran %d times for one process, want 1", calls)
	}
}
