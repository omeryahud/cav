package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// EnsureTrusted marks dir as a trusted workspace in ~/.claude.json, the same
// state accepting the trust dialog records, so `claude --bg` runs there without
// the interactive prompt (a fresh git worktree or repo is untrusted and would
// be refused). It is a no-op when dir is empty, already trusted, or the global
// config is missing or unreadable, and it edits only the one project entry.
func EnsureTrusted(dir string) error {
	if dir == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	path := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // no global config yet: let claude create and handle it
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil {
		return nil
	}
	projects := map[string]json.RawMessage{}
	if raw, ok := top["projects"]; ok {
		_ = json.Unmarshal(raw, &projects)
	}
	entry := map[string]json.RawMessage{}
	if raw, ok := projects[dir]; ok {
		_ = json.Unmarshal(raw, &entry)
	}
	if string(entry["hasTrustDialogAccepted"]) == "true" {
		return nil
	}
	entry["hasTrustDialogAccepted"] = json.RawMessage("true")
	projects[dir] = mustJSON(entry)
	top["projects"] = mustJSON(projects)
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode()
	}
	tmp := path + ".cav.tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
