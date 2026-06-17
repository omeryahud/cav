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
)

// Store is a persisted set of session IDs forced into the main pane.
type Store struct {
	path string
	m    map[string]bool
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cav")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cav")
}

// Load reads unparked.json (missing file = empty set).
func Load() *Store {
	s := &Store{path: filepath.Join(configDir(), "unparked.json"), m: map[string]bool{}}
	if b, err := os.ReadFile(s.path); err == nil {
		var ids []string
		if json.Unmarshal(b, &ids) == nil {
			for _, id := range ids {
				s.m[id] = true
			}
		}
	}
	return s
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
	return os.WriteFile(s.path, b, 0o644)
}
