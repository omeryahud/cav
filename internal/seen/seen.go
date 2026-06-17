// Package seen persists the last name cav has observed for each session, so a
// name survives a restart (and a transient drop from `agents --json` / state.json,
// where the daemon sometimes omits the name and a job's state.json often has
// none). displayName falls back to it. It's an auto-populated cache, distinct
// from the user's rename overrides (internal/names).
package seen

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Store maps sessionId -> last non-empty name seen, backed by a JSON file.
type Store struct {
	path  string
	m     map[string]string
	dirty bool
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cav")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cav")
}

// Load reads seen.json (missing file = empty store).
func Load() *Store {
	s := &Store{path: filepath.Join(configDir(), "seen.json"), m: map[string]string{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

// Get returns the last name seen for id, or "".
func (s *Store) Get(id string) string { return s.m[id] }

// Set records a (non-empty) name in memory, flagging a pending write. A no-op if
// unchanged, so the continuous refresh doesn't churn the disk.
func (s *Store) Set(id, name string) {
	if id == "" || name == "" || s.m[id] == name {
		return
	}
	s.m[id] = name
	s.dirty = true
}

// Save writes the cache to disk if anything changed since the last Save.
func (s *Store) Save() error {
	if !s.dirty {
		return nil
	}
	s.dirty = false
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.m, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}
