package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// EnsureTrustedWorkspace trusts cwd and, when cwd is inside a git repo, the
// repo's main checkout too. `claude --bg` checks trust on cwd (the worktree for
// a worktree session), but waking or respawning a background session checks the
// repo's main checkout, so both must be trusted for create and open to work.
func EnsureTrustedWorkspace(cwd string) {
	_ = EnsureTrusted(cwd, MainRoot(cwd))
}

// EnsureTrusted marks each directory as a trusted workspace in ~/.claude.json,
// the same state accepting the trust dialog records, so `claude` runs there
// without the interactive prompt (a fresh git worktree or repo is untrusted and
// would be refused). Empty and already-trusted directories are skipped, and it
// is a no-op when the global config is missing or unreadable. It edits only the
// touched project entries, in a single write.
func EnsureTrusted(dirs ...string) error {
	want := map[string]bool{}
	for _, d := range dirs {
		if d != "" {
			want[d] = true
		}
	}
	if len(want) == 0 {
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
	changed := false
	for dir := range want {
		entry := map[string]json.RawMessage{}
		if raw, ok := projects[dir]; ok {
			_ = json.Unmarshal(raw, &entry)
		}
		if string(entry["hasTrustDialogAccepted"]) == "true" {
			continue
		}
		entry["hasTrustDialogAccepted"] = json.RawMessage("true")
		projects[dir] = mustJSON(entry)
		changed = true
	}
	if !changed {
		return nil
	}
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
