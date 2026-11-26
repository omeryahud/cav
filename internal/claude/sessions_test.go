package claude

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
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
	verified.checks = map[int]processCheck{}
	return home
}

// deadPid returns the pid of a process that has ended.
func deadPid(t *testing.T) int {
	t.Helper()
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip("cannot run a helper process: " + err.Error())
	}
	return gone.Process.Pid
}

func stubProcStart(t *testing.T, start func(pid int) string) {
	t.Helper()
	orig := procStart
	procStart = start
	t.Cleanup(func() { procStart = orig })
}

const startedNow = "Thu Nov 26 10:00:00 2026"

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
	// The file name is the pid; these stand in for five processes.
	writeSessionFile(t, home, 1, rec("aaaa0001", "idle", ""))
	writeSessionFile(t, home, 2, rec("bbbb0001", "shell", ""))
	writeSessionFile(t, home, 3, rec("cccc0001", "busy", `,"spare":true`))
	writeSessionFile(t, home, 4, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"interactive","sessionId":"x","status":"idle"}`, me, started))
	writeSessionFile(t, home, 5, rec("dddd0001", "waiting", ""))

	got, ok := LiveSessions()
	if !ok || len(got) != 3 {
		t.Fatalf("LiveSessions() = %+v, %v; want the three background workers", got, ok)
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
	if byJob["dddd0001"].Status != "waiting" {
		t.Errorf("a worker waiting for input keeps its status, got %q", byJob["dddd0001"].Status)
	}
	if s, ok := LiveSession("bbbb0001"); !ok || s.SessionID != "bbbb0001-sid" {
		t.Errorf("LiveSession = %+v, %v", s, ok)
	}
	if _, ok := LiveSession("zzzz0001"); ok {
		t.Error("a job with no process has no live session")
	}
}

// A worker that starts cold writes its record with no status for about a
// second. claude lists it as busy then; asking the CLI would cost seconds.
func TestLiveSessionsListsAWorkerWithNoStatusYetAsBusy(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	stubProcStart(t, func(int) string { return startedNow })
	writeSessionFile(t, home, 1, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","jobId":"aaaa0001","sessionId":"s"}`, me, startedNow))

	got, ok := LiveSessions()
	if !ok || len(got) != 1 || got[0].Status != "busy" || got[0].StatusSince != 0 {
		t.Errorf("LiveSessions() = %+v, %v; want the worker as busy, with no idle time", got, ok)
	}
}

// When the records cannot be relied on, the caller must ask the CLI.
func TestLiveSessionsSaysWhenItCannotTell(t *testing.T) {
	me := os.Getpid()
	stubProcStart(t, func(int) string { return startedNow })
	for name, setup := range map[string]func(home string){
		"no sessions directory": func(home string) { os.RemoveAll(home + "/.claude") },
		"a record of a running process that does not parse": func(home string) {
			writeSessionFile(t, home, me, `{"pid":1,"kind":"bg","jobId":"aaaa`)
		},
		"a worker with no job id": func(home string) {
			writeSessionFile(t, home, 1, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","sessionId":"s","status":"idle"}`, me, startedNow))
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

// What a process that ended left behind must not turn the records off: cav
// would go back to one CLI call per refresh, with no way out.
func TestLiveSessionsSkipsWhatAnEndedProcessLeft(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	stubProcStart(t, func(int) string { return startedNow })
	writeSessionFile(t, home, me, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","jobId":"aaaa0001","sessionId":"s","status":"idle"}`, me, startedNow))
	writeSessionFile(t, home, deadPid(t), `{"pid":7,"kind":"bg","jobId":"cut`)
	if err := os.WriteFile(home+"/.claude/sessions/notes.json", []byte("not a record"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, ok := LiveSessions(); !ok || len(got) != 1 {
		t.Errorf("LiveSessions() = %+v, %v; want the one good worker", got, ok)
	}
}

// A record is rewritten in place: a read in the middle of a write is tried again.
func TestReadRecordTriesAgainWhenAWriteIsInProgress(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	path := writeSessionFile(t, home, me, `{"pid":`)
	go func() {
		time.Sleep(recordRetry / 2)
		os.WriteFile(path, []byte(fmt.Sprintf(`{"pid":%d,"kind":"bg","jobId":"aaaa0001"}`, me)), 0o600)
	}()

	if r, state := readRecord(path); state != recordRead || r.JobID != "aaaa0001" {
		t.Errorf("readRecord = %+v, state %d; want the finished record", r, state)
	}
}

// A record can outlive its process, and the pid can go to another process.
func TestLiveSessionsSkipsARecordWhoseProcessIsGone(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	const body = `{"pid":%d,"procStart":%q,"kind":"bg","jobId":%q,"sessionId":"s","status":"idle"}`
	writeSessionFile(t, home, 1, fmt.Sprintf(body, deadPid(t), "Mon Jan  1 00:00:00 2001", "dead0001"))
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
	procStart = func(int) string { return startedNow }
	b.Cleanup(func() { procStart = orig })
	for i := 0; i < 10; i++ {
		body := fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","jobId":"aaaa%04d","sessionId":"s%d","cwd":"/w","name":"n","startedAt":1,"status":"idle","statusUpdatedAt":2}`, me, startedNow, i, i)
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

// The start time is checked with ps once per process and per recheckProcess,
// not on every refresh. A pid that went to another process is found then.
func TestLiveSessionsChecksAProcessAgainAfterAWhile(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	calls, start := 0, startedNow
	stubProcStart(t, func(int) string { calls++; return start })
	writeSessionFile(t, home, 1, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","jobId":"aaaa0001","sessionId":"s","status":"idle"}`, me, startedNow))

	for i := 0; i < 3; i++ {
		if got, ok := LiveSessions(); !ok || len(got) != 1 {
			t.Fatalf("LiveSessions() = %+v, %v", got, ok)
		}
	}
	if calls != 1 {
		t.Fatalf("ps ran %d times for one process in three refreshes, want 1", calls)
	}

	start = "Fri Nov 27 09:00:00 2026" // the pid now belongs to a later process
	verified.checks[me] = processCheck{start: startedNow, at: time.Now().Add(-2 * recheckProcess)}
	if got, ok := LiveSessions(); !ok || len(got) != 0 || calls != 2 {
		t.Errorf("after recheckProcess: LiveSessions() = %+v, %v, ps runs %d; want no worker, one more check", got, ok, calls)
	}
}

// The checks of processes that have no record any more are dropped.
func TestLiveSessionsForgetsProcessesWithNoRecord(t *testing.T) {
	home := sessionHome(t)
	me := os.Getpid()
	stubProcStart(t, func(int) string { return startedNow })
	path := writeSessionFile(t, home, 1, fmt.Sprintf(`{"pid":%d,"procStart":%q,"kind":"bg","jobId":"aaaa0001","sessionId":"s","status":"idle"}`, me, startedNow))
	LiveSessions()
	if len(verified.checks) != 1 {
		t.Fatalf("checks = %v, want the one worker", verified.checks)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	LiveSessions()
	if len(verified.checks) != 0 {
		t.Errorf("checks = %v, want none: the record is gone", verified.checks)
	}
}
