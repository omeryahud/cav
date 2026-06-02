// Package names stores cav-local display-name overrides for sessions. The
// claude CLI has no rename verb and the daemon name isn't writable, so cav
// keeps its own sessionId -> name map (mirrors the order package). Renames
// show in cav and persist, but don't affect the native `claude agents` view.
package names

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Store maps sessionId -> custom display name.
type Store struct {
	path string
	m    map[string]string
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cav")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cav")
}

// Load reads names.json (missing file = empty store).
func Load() *Store {
	s := &Store{path: filepath.Join(configDir(), "names.json"), m: map[string]string{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

// Get returns the override for id, or "" if none.
func (s *Store) Get(id string) string { return s.m[id] }

// Set stores (or, with an empty name, clears) the override and persists it.
func (s *Store) Set(id, name string) error {
	if name == "" {
		delete(s.m, id)
	} else {
		s.m[id] = name
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.m, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}
