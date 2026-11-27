package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchedHome returns a home with the directories claude keeps, and a watcher
// on it that checks the replaced files often.
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
	orig := statEvery
	statEvery = 20 * time.Millisecond
	t.Cleanup(func() { statEvery = orig })
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

// openFiles counts this process's open file descriptors.
func openFiles(t *testing.T) int {
	t.Helper()
	fds, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip("cannot count open files here: " + err.Error())
	}
	return len(fds)
}

// Which fsnotify events are a change cav shows. No clock in this test.
func TestWatcherMatters(t *testing.T) {
	w := &Watcher{sessions: "/h/.claude/sessions"}
	for _, tc := range []struct {
		name string
		path string
		op   fsnotify.Op
		want bool
	}{
		{"a process record", "/h/.claude/sessions/123.json", fsnotify.Write, true},
		{"a process record goes away", "/h/.claude/sessions/123.json", fsnotify.Remove, true},
		{"a key file next to it", "/h/.claude/sessions/123.abc.key", fsnotify.Create, false},
		{"an attach starts", "/h/.claude/daemon/attach-journal/gesture.json", fsnotify.Create, true},
		{"a job appears", "/h/.claude/jobs/cccc0001", fsnotify.Create, true},
		{"a job goes", "/h/.claude/jobs/cccc0001", fsnotify.Remove, true},
		{"a mode change", "/h/.claude/sessions/123.json", fsnotify.Chmod, false},
	} {
		if got := w.matters(fsnotify.Event{Name: tc.path, Op: tc.op}); got != tc.want {
			t.Errorf("%s: matters = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The stat check, with no clock and no goroutine: what counts as a change of
// a file that is replaced by a rename.
func TestWatcherChecksTheReplacedFiles(t *testing.T) {
	root := t.TempDir()
	job := filepath.Join(root, "jobs", "aaaa0001")
	other := filepath.Join(root, "jobs", "bbbb0001")
	for _, dir := range []string{job, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state, roster := filepath.Join(job, "state.json"), filepath.Join(root, "daemon", "roster.json")
	write(t, state, "{}")
	w := &Watcher{sessions: filepath.Join(root, "sessions"), stamps: map[string]fileStamp{}, keep: map[string]bool{roster: true}}
	w.stamps[roster] = stampOf(roster) // not there yet
	w.TrackJobs([]string{"aaaa0001"})

	if w.replaced() {
		t.Error("nothing changed since the job was tracked")
	}
	replace(t, state, 1)
	if !w.replaced() || w.replaced() {
		t.Error("a replaced record must count as a change, once")
	}
	write(t, state, `{"written":"in place"}`)
	if !w.replaced() {
		t.Error("a record written in place must count too")
	}
	write(t, filepath.Join(other, "state.json"), "{}")
	if w.replaced() {
		t.Error("the record of a job with no worker is not checked")
	}

	if err := os.MkdirAll(filepath.Dir(roster), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, roster, "{}")
	if !w.replaced() {
		t.Error("a roster that appears must count as a change")
	}

	w.TrackJobs([]string{"aaaa0001", "bbbb0001"}) // tracked twice: the stamp must stay
	if err := os.RemoveAll(job); err != nil {
		t.Fatal(err)
	}
	if !w.replaced() || len(w.stamps) != 2 {
		t.Errorf("a deleted job must count as a change and leave the check: %d files checked, want the roster and one job", len(w.stamps))
	}
	if err := os.Remove(roster); err != nil {
		t.Fatal(err)
	}
	if !w.replaced() || len(w.stamps) != 2 {
		t.Error("the roster is checked also while it does not exist")
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
	settle(w)
	if err := os.RemoveAll(filepath.Join(root, "jobs", "bbbb0001")); err != nil {
		t.Fatal(err)
	}
	if !changed(w) {
		t.Error("a job that is deleted must count as a change, also one with no worker")
	}
}

// fsnotify on kqueue can stop following a file that is replaced by a rename,
// and a watch set again by hand leaked open files. The stat check has neither.
func TestWatcherFollowsARecordThroughManyReplaces(t *testing.T) {
	root, w := watchedHome(t)
	state := filepath.Join(root, "jobs", "aaaa0001", "state.json")
	write(t, state, "{}")
	w.TrackJobs([]string{"aaaa0001"})
	settle(w)
	before := openFiles(t)

	for i := 0; i < 40; i++ {
		replace(t, state, i)
		time.Sleep(20 * time.Millisecond)
		w.Repair() // every refresh runs it
	}
	if !changed(w) {
		t.Fatal("a burst of replaces must count as a change")
	}
	for i := 100; i < 104; i++ {
		settle(w)
		replace(t, state, i)
		if !changed(w) {
			t.Fatalf("replace %d after the burst gave no token: the record is not followed", i)
		}
	}
	settle(w)
	if after := openFiles(t); after != before {
		t.Errorf("open files went from %d to %d over 44 replaces, want no change", before, after)
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
	if !slices.Contains(w.fs.WatchList(), sessions) {
		t.Fatal("process records made again must be watched after a repair")
	}
	write(t, filepath.Join(sessions, "9.json"), "{}")
	if !changed(w) {
		t.Error("a record in the new directory must count as a change")
	}
}

// Another cav saves its marks by a rename: that must wake this cav too.
func TestWatcherTellsWhenAnExtraFileChanges(t *testing.T) {
	marks := filepath.Join(t.TempDir(), "unparked.json")
	write(t, marks, "[]")
	_, w := watchedHome(t, marks)

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

// With no watch on the process records cav must poll: a watcher that watches
// nothing would leave it with no refresh at all.
func TestNewWatcherFailsWithoutTheProcessRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if w, err := NewWatcher(); err == nil {
		w.Close()
		t.Error("with no sessions directory there is nothing to watch: want an error")
	}

	if os.Geteuid() == 0 {
		return // root opens a directory of any mode
	}
	sessions := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(sessions, 0)
	t.Cleanup(func() { os.Chmod(sessions, 0o755) })
	if w, err := NewWatcher(); err == nil {
		w.Close()
		t.Error("a sessions directory that cannot be opened: want an error")
	}
}
