package claude

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// statEvery is how often the files replaced by a rename are checked. Tests shorten it.
var statEvery = time.Second

// Watcher tells when claude changes the files cav reads its sessions from, so
// cav can refresh on a change and not on a timer.
type Watcher struct {
	fs       *fsnotify.Watcher
	changed  chan struct{}
	sessions string   // the process records: written in place, so fsnotify follows them
	dirs     []string // the directories fsnotify watches

	// stamps holds the files that are replaced by a rename. On kqueue fsnotify
	// can stop following such a file, so these are checked with stat.
	mu     sync.Mutex
	stamps map[string]fileStamp
	keep   map[string]bool // checked even while they do not exist
}

// fileStamp tells one version of a file from the next. It is zero for no file.
type fileStamp struct {
	inode   uint64
	size    int64
	modTime int64
}

// NewWatcher watches the process records, the attach journal, the list of
// jobs, the roster and the extra files. It fails when it cannot watch the records.
func NewWatcher(extra ...string) (*Watcher, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".claude")
	w := &Watcher{fs: fs, changed: make(chan struct{}, 1), sessions: filepath.Join(root, "sessions"),
		stamps: map[string]fileStamp{}, keep: map[string]bool{}}
	if err := fs.Add(w.sessions); err != nil {
		fs.Close()
		return nil, err
	}
	w.dirs = []string{w.sessions, filepath.Join(root, "daemon", "attach-journal"), filepath.Join(root, "jobs")}
	for _, file := range append([]string{filepath.Join(root, "daemon", "roster.json")}, extra...) {
		w.stamps[file], w.keep[file] = stampOf(file), true
	}
	w.Repair()
	go w.run()
	return w, nil
}

// Changed gets one token when something changed since the last one was taken.
func (w *Watcher) Changed() <-chan struct{} { return w.changed }

// Close stops the watch.
func (w *Watcher) Close() { w.fs.Close() }

// TrackJobs also checks the records of these jobs. A job is checked until its
// directory is gone: claude still writes the last state after the worker ends.
func (w *Watcher) TrackJobs(ids []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range ids {
		state := filepath.Join(filepath.Dir(w.sessions), "jobs", id, "state.json")
		if _, tracked := w.stamps[state]; !tracked {
			w.stamps[state] = stampOf(state)
		}
	}
}

// Repair watches a directory that was not there before: one made after cav
// started, or removed and made again.
func (w *Watcher) Repair() {
	watched := w.fs.WatchList()
	for _, dir := range w.dirs {
		if !slices.Contains(watched, dir) {
			_ = w.fs.Add(dir) // not there on every claude version
		}
	}
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	s := fileStamp{size: fi.Size(), modTime: fi.ModTime().UnixNano()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.inode = uint64(st.Ino)
	}
	return s
}

// replaced reports whether a checked file changed since the last look, and
// stops checking the record of a job that is gone.
func (w *Watcher) replaced() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	changed := false
	for file, seen := range w.stamps {
		now := stampOf(file)
		if now != seen {
			changed = true
			w.stamps[file] = now
		}
		if now == (fileStamp{}) && !w.keep[file] && !exists(filepath.Dir(file)) {
			delete(w.stamps, file)
		}
	}
	return changed
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (w *Watcher) run() {
	tick := time.NewTicker(statEvery)
	defer tick.Stop()
	for {
		select {
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			if w.matters(ev) {
				w.notify()
			}
		case _, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			w.notify() // an event may be lost: look again
		case <-tick.C:
			if w.replaced() {
				w.notify()
			}
		}
	}
}

func (w *Watcher) notify() {
	select {
	case w.changed <- struct{}{}:
	default: // a token already waits
	}
}

// matters reports whether an event is a change cav shows. Next to each
// process record lies a key file that cav does not read.
func (w *Watcher) matters(ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod {
		return false
	}
	if filepath.Dir(ev.Name) == w.sessions {
		return strings.HasSuffix(ev.Name, ".json")
	}
	return true // the attach journal, and a job that appears or goes
}
