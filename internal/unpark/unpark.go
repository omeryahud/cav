// Package unpark stores cav-local "brought back to the main pane" markers. A
// session removed with d, or genuinely stopped, lives in the stopped window
// (see isStopped); pressing b adds its id here to override that and show it in
// the main list anyway — still with its real status, and nested in the fork
// tree. d removes the marker, returning it to the stopped window. Persists
// across restarts (mirrors the dismiss package, plus Remove).
package unpark

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
func (s *Store) Add(id string) error { s.m[id] = true; return s.save() }

// Remove unmarks id (back to the stopped window) and persists.
func (s *Store) Remove(id string) error { delete(s.m, id); return s.save() }

func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	ids := make([]string, 0, len(s.m))
	for id := range s.m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, _ := json.MarshalIndent(ids, "", "  ")
	if err := os.WriteFile(s.path, b, 0o644); err != nil {
		return err
	}
	if fi, err := os.Stat(s.path); err == nil {
		s.mtime = fi.ModTime()
	}
	return nil
}
