package claude

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

// Watcher tells when claude changes the files cav reads its sessions from, so
// cav can refresh on a change and not on a timer.
type Watcher struct {
	fs      *fsnotify.Watcher
	changed chan struct{}
	root    string          // ~/.claude
	jobs    map[string]bool // the job directories watched now
}

// NewWatcher watches the process records, the roster, the attach journal and
// the list of jobs. It fails when the process records are not there.
func NewWatcher() (*Watcher, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{fs: fs, changed: make(chan struct{}, 1), root: filepath.Join(home, ".claude"), jobs: map[string]bool{}}
	if err := fs.Add(filepath.Join(w.root, "sessions")); err != nil {
		fs.Close()
		return nil, err
	}
	for _, dir := range []string{"daemon", "daemon/attach-journal", "jobs"} {
		_ = fs.Add(filepath.Join(w.root, dir)) // not there on every claude version
	}
	go w.run()
	return w, nil
}

// Changed gets one token when something changed since the last one was taken.
func (w *Watcher) Changed() <-chan struct{} { return w.changed }

// Close stops the watch.
func (w *Watcher) Close() { w.fs.Close() }

// TrackJobs watches the records of these jobs and drops the others. Watching
// only the jobs with a process keeps the open files few.
func (w *Watcher) TrackJobs(ids []string) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
		if !w.jobs[id] && w.fs.Add(filepath.Join(w.root, "jobs", id)) == nil {
			w.jobs[id] = true
		}
	}
	for id := range w.jobs {
		if !want[id] {
			_ = w.fs.Remove(filepath.Join(w.root, "jobs", id))
			delete(w.jobs, id)
		}
	}
}

func (w *Watcher) run() {
	for {
		select {
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			if w.matters(ev) {
				select {
				case w.changed <- struct{}{}:
				default: // a token already waits
				}
			}
		case _, ok := <-w.fs.Errors:
			if !ok {
				return
			}
		}
	}
}

// matters reports whether an event is a change cav shows. A job's timeline
// grows on every tool call, and cav does not read it.
func (w *Watcher) matters(ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod {
		return false
	}
	name := filepath.Base(ev.Name)
	switch dir, _ := filepath.Rel(w.root, filepath.Dir(ev.Name)); dir {
	case "sessions":
		return strings.HasSuffix(name, ".json")
	case "daemon":
		return name == "roster.json"
	case "daemon/attach-journal", "jobs":
		return true
	default:
		return name == "state.json"
	}
}
