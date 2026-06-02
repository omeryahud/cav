// Package dismiss stores cav-local "dismissed" session IDs — sessions the user
// has removed from cav's view with `d`. The claude CLI has no per-session delete
// verb, and `claude stop` is a no-op on a finished session, so rather than
// mutating ~/.claude state cav keeps its own set of hidden sessionIds (mirrors
// the order/names packages). Dismissed sessions stay on disk and remain
// resumable via the claude CLI; cav just stops listing them. The hide persists
// across restarts.
package dismiss

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

// Store holds the set of dismissed session IDs backed by a JSON file.
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

// Load reads dismissed.json (missing file = empty store).
func Load() *Store {
	s := &Store{path: filepath.Join(configDir(), "dismissed.json"), m: map[string]bool{}}
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

// Has reports whether id has been dismissed.
func (s *Store) Has(id string) bool { return s.m[id] }

// Len returns the number of dismissed sessions.
func (s *Store) Len() int { return len(s.m) }

// Add marks id dismissed and persists the set (sorted, for stable files).
func (s *Store) Add(id string) error {
	if id == "" || s.m[id] {
		return nil
	}
	s.m[id] = true
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	ids := make([]string, 0, len(s.m))
	for k := range s.m {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	b, _ := json.MarshalIndent(ids, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}
