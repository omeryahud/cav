package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchedHome returns a home with the directories claude keeps, and a watcher on it.
func watchedHome(t *testing.T, extra ...string) (root string, w *Watcher) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root = filepath.Join(home, ".claude")
	for _, dir := range []string{"sessions", "daemon/attach-journal", "jobs/aaaa0001", "jobs/bbbb0001"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w, err := NewWatcher(extra...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return root, w
}

// changed waits for a token. The wait is long: on a loaded machine the first
// event after a new file can take over a second.
func changed(w *Watcher) bool {
	select {
	case <-w.Changed():
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

// settle takes the tokens of the events still on their way.
func settle(w *Watcher) {
	for {
		select {
		case <-w.Changed():
		case <-time.After(time.Second):
			return
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// replace writes path the way claude writes a job record: a temp file, then a rename.
func replace(t *testing.T, path string, n int) {
	t.Helper()
	tmp := fmt.Sprintf("%s.tmp.%04x", path, n)
	write(t, tmp, fmt.Sprintf(`{"n":%d}`, n))
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func isWatched(w *Watcher, dir string) bool {
	for _, p := range w.fs.WatchList() {
		if p == dir {
			return true
		}
	}
	return false
}

// Which events are a change cav shows. No clock in this test.
func TestWatcherMatters(t *testing.T) {
	const marks = "/cfg/cav/unparked.json"
	w := &Watcher{root: "/h/.claude", extra: map[string]bool{marks: true}}
	for _, tc := range []struct {
		name string
		path string
		op   fsnotify.Op
		want bool
	}{
		{"a process record", "/h/.claude/sessions/123.json", fsnotify.Write, true},
		{"a process record goes away", "/h/.claude/sessions/123.json", fsnotify.Remove, true},
		{"a key file next to it", "/h/.claude/sessions/123.abc.key", fsnotify.Create, false},
		{"the roster", "/h/.claude/daemon/roster.json", fsnotify.Create, true},
		{"the roster's temp file", "/h/.claude/daemon/roster.json.tmp.1", fsnotify.Create, true},
		{"another daemon file", "/h/.claude/daemon/control.key", fsnotify.Write, false},
		{"an attach starts", "/h/.claude/daemon/attach-journal/gesture.json", fsnotify.Create, true},
		{"a job appears", "/h/.claude/jobs/cccc0001", fsnotify.Create, true},
		{"a job's record", "/h/.claude/jobs/aaaa0001/state.json", fsnotify.Create, true},
		{"the temp file of a job's record", "/h/.claude/jobs/aaaa0001/state.json.tmp.9f", fsnotify.Create, true},
		{"a job's timeline", "/h/.claude/jobs/aaaa0001/timeline.jsonl", fsnotify.Write, false},
		{"a mode change", "/h/.claude/sessions/123.json", fsnotify.Chmod, false},
		{"another cav's marks", marks, fsnotify.Create, true},
		{"another file of cav's own", "/cfg/cav/seen.json", fsnotify.Write, false},
	} {
		if got := w.matters(fsnotify.Event{Name: tc.path, Op: tc.op}); got != tc.want {
			t.Errorf("%s: matters = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWatcherTellsWhenASessionChanges(t *testing.T) {
	root, w := watchedHome(t)
	w.TrackJobs([]string{"aaaa0001"})

	record := filepath.Join(root, "sessions", "123.json")
	for _, path := range []string{
		record,
		filepath.Join(root, "daemon", "roster.json"),
		filepath.Join(root, "daemon", "attach-journal", "gesture.json"),
		filepath.Join(root, "jobs", "aaaa0001", "state.json"),
	} {
		write(t, path, "{}")
		if !changed(w) {
			t.Errorf("%s: a new file must count as a change", path)
		}
		settle(w)
	}

	write(t, record, `{"status":"idle"}`) // claude rewrites a record in place
	if !changed(w) {
		t.Error("a record written in place must count as a change")
	}
	settle(w)
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if !changed(w) {
		t.Error("a record that goes away (the process ended) must count as a change")
	}
}

// Only the jobs that had a worker are watched, and such a job stays watched.
func TestWatcherTracksTheJobsWithAWorker(t *testing.T) {
	root, w := watchedHome(t)
	a, b := filepath.Join(root, "jobs", "aaaa0001"), filepath.Join(root, "jobs", "bbbb0001")
	w.TrackJobs([]string{"aaaa0001", "gone0000"}) // gone0000 has no directory
	if !isWatched(w, a) || isWatched(w, b) || len(w.jobs) != 1 {
		t.Fatalf("watched: a = %v, b = %v, jobs = %v; want only a", isWatched(w, a), isWatched(w, b), w.jobs)
	}

	w.TrackJobs([]string{"bbbb0001"}) // a's worker ended
	if !isWatched(w, a) || !isWatched(w, b) {
		t.Error("a job whose worker ended must stay watched: claude still writes its last state")
	}
	write(t, filepath.Join(a, "state.json"), "{}")
	if !changed(w) {
		t.Error("the last state of an ended job must wake cav")
	}

	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	if !changed(w) {
		t.Error("a job that is deleted must wake cav")
	}
	settle(w)
	w.Repair()
	if w.jobs[a] || len(w.replaced) != 2 {
		t.Errorf("a deleted job must be dropped: jobs = %v, replaced = %v", w.jobs, w.replaced)
	}
}

// fsnotify on kqueue can stop following a file that is replaced by a rename
// twice in a row. Repair, which every refresh runs, must bring the watch back.
func TestWatcherRepairsALostRecordWatch(t *testing.T) {
	root, w := watchedHome(t)
	state := filepath.Join(root, "jobs", "aaaa0001", "state.json")
	write(t, state, "{}")
	w.TrackJobs([]string{"aaaa0001"})

	for i := 0; i < 40; i++ {
		replace(t, state, i)
		time.Sleep(20 * time.Millisecond)
	}
	if !changed(w) {
		t.Fatal("a burst of replaces must count as a change")
	}
	for i := 100; i < 104; i++ {
		settle(w)
		w.Repair()
		replace(t, state, i)
		if !changed(w) {
			t.Fatalf("replace %d after the burst gave no token: the watch is lost", i)
		}
	}
}

// A directory made after cav started is watched from the next refresh.
func TestWatcherRepairsAMissingDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".claude")
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)

	journal := filepath.Join(root, "daemon", "attach-journal")
	if err := os.MkdirAll(journal, 0o755); err != nil {
		t.Fatal(err)
	}
	w.Repair()
	write(t, filepath.Join(journal, "gesture.json"), "{}")
	if !changed(w) {
		t.Error("an attach journal made after the start must be watched after a repair")
	}

	if err := os.RemoveAll(sessions); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	settle(w)
	w.Repair()
	write(t, filepath.Join(sessions, "9.json"), "{}")
	if !changed(w) {
		t.Error("process records made again must be watched after a repair")
	}
}

// Another cav saves its marks by a rename: that must wake this cav too.
func TestWatcherTellsWhenAnExtraFileChanges(t *testing.T) {
	marks := filepath.Join(t.TempDir(), "unparked.json")
	write(t, marks, "[]")
	_, w := watchedHome(t, marks)

	write(t, filepath.Join(filepath.Dir(marks), "seen.json"), "{}")
	replace(t, marks, 1)
	if !changed(w) {
		t.Error("a replaced extra file must count as a change")
	}
}

// Many changes at once leave one token: cav refreshes once for a burst.
func TestWatcherMergesABurst(t *testing.T) {
	w := &Watcher{changed: make(chan struct{}, 1)}
	for i := 0; i < 20; i++ {
		w.notify()
	}
	<-w.Changed()
	select {
	case <-w.Changed():
		t.Error("a burst must leave one token, not two")
	default:
	}
}

func TestNewWatcherFailsWithoutTheProcessRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if w, err := NewWatcher(); err == nil {
		w.Close()
		t.Error("with no sessions directory there is nothing to watch: want an error")
	}
}
