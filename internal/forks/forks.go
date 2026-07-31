// Package forks stores cav-local fork relationships. cav forks a session with F
// (`claude --bg --resume <id> --fork-session`), but the daemon records no parent
// link on disk, so cav keeps its own childJobId -> parentSessionId map (mirrors
// the names package) to nest children under their parent in the list. Persists.
package forks

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/omeryahud/cav/internal/config"
)

// Store maps a forked child's job id -> its parent session id.
type Store struct {
	path string
	m    map[string]string
}

// Load reads forks.json (missing file = empty store).
func Load() *Store {
	s := &Store{path: filepath.Join(config.Dir(), "forks.json"), m: map[string]string{}}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.m)
	}
	return s
}

// Parent returns the parent session id of the child with this job id, or "".
func (s *Store) Parent(childJobID string) string { return s.m[childJobID] }

// Set records that the child with childJobID was forked from parentSessionID.
func (s *Store) Set(childJobID, parentSessionID string) error {
	if childJobID == "" || parentSessionID == "" {
		return nil
	}
	s.m[childJobID] = parentSessionID
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.m, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}
