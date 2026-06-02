// Package order persists a user-defined ordering of session IDs so the custom
// view can keep a stable, hand-arranged order across refreshes and restarts.
package order

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Store holds an ordered list of session IDs backed by a JSON file.
type Store struct {
	path string
	ids  []string
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cav")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cav")
}

// Load reads order.json. A missing file yields an empty (valid) store.
func Load() *Store {
	s := &Store{path: filepath.Join(configDir(), "order.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.ids)
	}
	return s
}

// IDs returns a copy of the saved order.
func (s *Store) IDs() []string { return append([]string(nil), s.ids...) }

// Set replaces the order and persists it to disk.
func (s *Store) Set(ids []string) error {
	s.ids = append([]string(nil), ids...)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.ids, "", "  ")
	return os.WriteFile(s.path, b, 0o644)
}
