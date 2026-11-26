package claude

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/fsnotify/fsnotify"
)

// Watcher tells when claude changes the files cav reads its sessions from, so
// cav can refresh on a change and not on a timer.
type Watcher struct {
	fs      *fsnotify.Watcher
	changed chan struct{}
	root    string          // ~/.claude
	extra   map[string]bool // files outside root whose change matters too
	base    []string        // the directories watched from the start
	jobs    map[string]bool // the job directories watched
	// replaced holds the files written by a rename, with the inode each had
	// when its watch was set: a new inode means a watch to set again.
	replaced map[string]uint64
}

// NewWatcher watches the process records, the roster, the attach journal, the
// list of jobs and the extra files. It fails when the process records are not there.
func NewWatcher(extra ...string) (*Watcher, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, ".claude")
	sessions := filepath.Join(root, "sessions")
	if _, err := os.Stat(sessions); err != nil {
		return nil, err
	}
	fs, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{fs: fs, changed: make(chan struct{}, 1), root: root, extra: map[string]bool{},
		base: []string{sessions}, jobs: map[string]bool{}, replaced: map[string]uint64{}}
	for _, dir := range []string{"daemon", "daemon/attach-journal", "jobs"} {
		w.base = append(w.base, filepath.Join(root, dir))
	}
	for _, file := range extra {
		w.extra[file] = true
		w.base = append(w.base, filepath.Dir(file))
	}
	for _, file := range append(extra, filepath.Join(root, "daemon", "roster.json")) {
		w.replaced[file] = inode(file)
	}
	w.Repair()
	go w.run()
	return w, nil
}

// Changed gets one token when something changed since the last one was taken.
func (w *Watcher) Changed() <-chan struct{} { return w.changed }

// Close stops the watch.
func (w *Watcher) Close() { w.fs.Close() }

// TrackJobs also watches the records of these jobs. A job stays watched after
// its worker ends: fsnotify cannot give a directory back to its parent's watch.
func (w *Watcher) TrackJobs(ids []string) {
	for _, id := range ids {
		dir := filepath.Join(w.root, "jobs", id)
		if w.jobs[dir] {
			continue
		}
		state := filepath.Join(dir, "state.json")
		seen := inode(state)
		if w.fs.Add(dir) == nil {
			w.jobs[dir], w.replaced[state] = true, seen
		}
	}
}

// Repair sets up again what the watch lost, before the files are read. On
// kqueue, fsnotify can stop following a file that two renames replace in a row.
func (w *Watcher) Repair() {
	watched := map[string]bool{}
	for _, dir := range w.fs.WatchList() {
		watched[dir] = true
	}
	for _, dir := range w.base {
		if !watched[dir] {
			_ = w.fs.Add(dir) // not there on every claude version
		}
	}
	for dir := range w.jobs {
		if !watched[dir] && w.fs.Add(dir) != nil {
			delete(w.jobs, dir)
			delete(w.replaced, filepath.Join(dir, "state.json"))
		}
	}
	for file, seen := range w.replaced {
		if now := inode(file); now != seen {
			_ = w.fs.Remove(file)
			if now != 0 {
				_ = w.fs.Add(file)
			}
			w.replaced[file] = now
		}
	}
}

// inode identifies the file at path now, 0 when there is none.
func inode(path string) uint64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}

func (w *Watcher) run() {
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
		}
	}
}

func (w *Watcher) notify() {
	select {
	case w.changed <- struct{}{}:
	default: // a token already waits
	}
}

// matters reports whether an event is a change cav shows. A job's timeline
// grows on every tool call, and cav does not read it.
func (w *Watcher) matters(ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod {
		return false
	}
	if w.extra[ev.Name] {
		return true
	}
	name := filepath.Base(ev.Name)
	switch dir, _ := filepath.Rel(w.root, filepath.Dir(ev.Name)); dir {
	case "sessions":
		return strings.HasSuffix(name, ".json")
	case "daemon":
		return strings.HasPrefix(name, "roster.json")
	case "daemon/attach-journal", "jobs":
		return true
	default:
		// A job directory. The temp file of a replace counts too: it is seen
		// even when the watch on the record itself is lost.
		return strings.HasPrefix(name, "state.json")
	}
}
