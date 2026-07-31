// Package labels stores cav-local, user-assigned labels for sessions —
// free-form space-separated tags kept per session id (mirrors the names
// package). Labels show on the row (#tag) and are matched by the / filter.
package labels

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/omeryahud/cav/internal/config"
)

// Store maps sessionId -> label text (space-separated tags).
type Store struct {
	path string
	m    map[string]string
}

// Load reads labels.json (missing file = empty store).
func Load() *Store {
	s := &Store{path: filepath.Join(config.Dir(), "labels.json"), m: map[string]string{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

// Get returns the label text for id, or "" if none.
func (s *Store) Get(id string) string { return s.m[id] }

// Set stores (or, with empty text, clears) the label and persists it.
func (s *Store) Set(id, text string) error {
	if text == "" {
		delete(s.m, id)
	} else {
		s.m[id] = text
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.m, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}
