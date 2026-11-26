// Package unpark stores cav-local "brought back to the main pane" markers. A
// session removed with d, or genuinely stopped, lives in the stopped window
// (see isStopped); pressing b adds its id here to override that and show it in
// the main list anyway — still with its real status, and nested in the fork
// tree. d removes the marker, returning it to the stopped window. Persists
// across restarts (mirrors the dismiss package, plus Remove).
package unpark

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/omeryahud/cav/internal/config"
)

// Store is a persisted set of session IDs forced into the main pane.
type Store struct {
	path  string
	m     map[string]bool
	mtime time.Time // the file's modification time when it was last read or written
}

// Load reads unparked.json (missing file = empty set).
func Load() *Store {
	s := &Store{path: filepath.Join(config.Dir(), "unparked.json"), m: map[string]bool{}}
	s.read()
	return s
}

// Reload picks up marks another cav wrote since this store last read or wrote
// the file, so every open cav shows the same sessions in the main pane.
func (s *Store) Reload() {
	if fi, err := os.Stat(s.path); err == nil && !fi.ModTime().Equal(s.mtime) {
		s.read()
	}
}

// read replaces the set with the file's content. A missing or half-written
// file leaves the set as it is, to be read again later.
func (s *Store) read() {
	fi, err := os.Stat(s.path)
	if err != nil {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var ids []string
	if json.Unmarshal(b, &ids) != nil {
		return
	}
	s.m = make(map[string]bool, len(ids))
	for _, id := range ids {
		s.m[id] = true
	}
	s.mtime = fi.ModTime()
}

// Has reports whether id is marked (brought back to the main pane).
func (s *Store) Has(id string) bool { return s.m[id] }

// Add marks id and persists.
func (s *Store) Add(id string) error {
	return s.update(func() bool {
		had := s.m[id]
		s.m[id] = true
		return !had
	})
}

// Remove unmarks id (back to the stopped window) and persists.
func (s *Store) Remove(id string) error {
	return s.update(func() bool {
		had := s.m[id]
		delete(s.m, id)
		return had
	})
}

// update reads, changes and writes the file under a lock every cav takes. A
// failed write puts the set back: memory never holds a mark the file does not.
func (s *Store) update(change func() (changed bool)) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	defer lock(s.path + ".lock")()
	s.read()
	before := maps.Clone(s.m)
	if !change() {
		return nil
	}
	if err := s.save(); err != nil {
		s.m = before
		return err
	}
	return nil
}

// lock takes an exclusive lock on path and returns the function that frees it.
// When it cannot, the caller goes on unlocked: better than no marks at all.
func lock(path string) (unlock func()) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	return func() { f.Close() } // closing the file frees the lock
}

func (s *Store) save() error {
	ids := make([]string, 0, len(s.m))
	for id := range s.m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, _ := json.MarshalIndent(ids, "", "  ")
	mtime, err := writeAtomic(s.path, b)
	if err != nil {
		return err
	}
	s.mtime = mtime
	return nil
}

// writeAtomic replaces path in one step, so no cav reads a half-written file.
// It returns the file's modification time, taken before the rename.
func writeAtomic(path string, b []byte) (time.Time, error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return time.Time{}, err
	}
	defer os.Remove(tmp.Name()) // nothing to remove once the rename has worked
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	fi, statErr := tmp.Stat()
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = statErr
	}
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), os.Rename(tmp.Name(), path)
}
