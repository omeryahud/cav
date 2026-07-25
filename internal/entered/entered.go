// Package entered records when the user last opened (attached to) each session
// from cav. Nothing on disk tracks this — the daemon knows when a session ran,
// not when you last stepped into it — so cav keeps its own sessionId -> time
// map to back the "recently entered" sort order (o cycles to it).
package entered

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Store maps sessionId -> last-entered time (unix milliseconds), backed by a
// JSON file.
type Store struct {
	path string
	m    map[string]int64
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cav")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cav")
}

// Load reads entered.json (missing file = empty store).
func Load() *Store {
	s := &Store{path: filepath.Join(configDir(), "entered.json"), m: map[string]int64{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

// At returns when id was last entered (unix ms), or 0 if never.
func (s *Store) At(id string) int64 { return s.m[id] }

// Mark records id as entered now and persists the map. Entering a session is a
// rare, deliberate action, so this writes immediately rather than batching.
func (s *Store) Mark(id string) error {
	if id == "" {
		return nil
	}
	s.m[id] = time.Now().UnixMilli()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.m, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}
