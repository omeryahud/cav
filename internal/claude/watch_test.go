package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// watchedHome returns a home with the directories claude keeps, and a watcher on it.
func watchedHome(t *testing.T) (root string, w *Watcher) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root = filepath.Join(home, ".claude")
	for _, dir := range []string{"sessions", "daemon/attach-journal", "jobs/aaaa0001", "jobs/bbbb0001"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	w, err := NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return root, w
}

func changed(w *Watcher) bool {
	select {
	case <-w.Changed():
		return true
	case <-time.After(700 * time.Millisecond):
		return false
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherTellsWhenASessionChanges(t *testing.T) {
	root, w := watchedHome(t)
	w.TrackJobs([]string{"aaaa0001"})

	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"a process record appears", "sessions/123.json", true},
		{"a key file next to it", "sessions/123.abc.key", false},
		{"the roster", "daemon/roster.json", true},
		{"another daemon file", "daemon/control.key", false},
		{"an attach starts", "daemon/attach-journal/gesture.json", true},
		{"a watched job's record", "jobs/aaaa0001/state.json", true},
		{"a watched job's timeline", "jobs/aaaa0001/timeline.jsonl", false},
		{"a job that is not watched", "jobs/bbbb0001/state.json", false},
	} {
		write(t, filepath.Join(root, tc.path), "{}")
		if got := changed(w); got != tc.want {
			t.Errorf("%s: changed = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A record replaced by a rename, as an atomic write does it.
	tmp := filepath.Join(root, "sessions", "123.json.tmp")
	write(t, tmp, `{"status":"idle"}`)
	changed(w) // the temp file itself does not matter either way
	if err := os.Rename(tmp, filepath.Join(root, "sessions", "123.json")); err != nil {
		t.Fatal(err)
	}
	if !changed(w) {
		t.Error("a record replaced by a rename must count as a change")
	}
	if err := os.Remove(filepath.Join(root, "sessions", "123.json")); err != nil {
		t.Fatal(err)
	}
	if !changed(w) {
		t.Error("a record that goes away (the process ended) must count as a change")
	}
}

func TestWatcherFollowsTheTrackedJobs(t *testing.T) {
	root, w := watchedHome(t)
	w.TrackJobs([]string{"aaaa0001"})
	w.TrackJobs([]string{"bbbb0001", "gone0000"}) // gone0000 has no directory

	write(t, filepath.Join(root, "jobs/aaaa0001/state.json"), "{}")
	if changed(w) {
		t.Error("a job that is no longer tracked must not wake cav")
	}
	write(t, filepath.Join(root, "jobs/bbbb0001/state.json"), "{}")
	if !changed(w) {
		t.Error("a newly tracked job must wake cav")
	}
}

// Many changes at once leave one token: cav refreshes once for a burst.
func TestWatcherMergesABurst(t *testing.T) {
	root, w := watchedHome(t)
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(root, "daemon/roster.json"), "{}")
	}
	if !changed(w) {
		t.Fatal("a burst must count as a change")
	}
	time.Sleep(200 * time.Millisecond)
	select {
	case <-w.Changed():
	default:
	}
	if changed(w) {
		t.Error("nothing changed after the burst was taken")
	}
}

func TestNewWatcherFailsWithoutTheProcessRecords(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if w, err := NewWatcher(); err == nil {
		w.Close()
		t.Error("with no sessions directory there is nothing to watch: want an error")
	}
}
