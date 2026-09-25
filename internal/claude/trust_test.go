package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readClaudeJSON(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result is not valid json: %v", err)
	}
	return m
}

func TestEnsureTrustedAddsFlagAndPreservesRest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orig := `{"firstStartTime":"x","projects":{"/other":{"hasTrustDialogAccepted":true,"keep":42}}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureTrusted("/new/worktree"); err != nil {
		t.Fatal(err)
	}
	m := readClaudeJSON(t, home)
	if m["firstStartTime"] != "x" {
		t.Error("unrelated top-level key was lost")
	}
	projects := m["projects"].(map[string]any)
	other := projects["/other"].(map[string]any)
	if other["hasTrustDialogAccepted"] != true || other["keep"].(float64) != 42 {
		t.Errorf("existing project entry not preserved: %v", other)
	}
	newEntry := projects["/new/worktree"].(map[string]any)
	if newEntry["hasTrustDialogAccepted"] != true {
		t.Errorf("new dir not trusted: %v", newEntry)
	}
}

func TestEnsureTrustedNoOpWhenAlreadyTrusted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude.json")
	orig := `{"projects":{"/d":{"hasTrustDialogAccepted":true}}}`
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	fi0, _ := os.Stat(path)
	if err := EnsureTrusted("/d"); err != nil {
		t.Fatal(err)
	}
	fi1, _ := os.Stat(path)
	if !fi0.ModTime().Equal(fi1.ModTime()) {
		t.Error("file was rewritten even though the dir was already trusted")
	}
}

func TestEnsureTrustedToleratesMissingAndGarbage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := EnsureTrusted("/d"); err != nil {
		t.Errorf("missing file should be a no-op, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Error("missing file should not be created")
	}
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureTrusted("/d"); err != nil {
		t.Errorf("garbage file should be a no-op, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Error("garbage file must be left untouched")
	}
}

func TestEnsureTrustedEmptyDir(t *testing.T) {
	if err := EnsureTrusted(""); err != nil {
		t.Errorf("empty dir should be a no-op, got %v", err)
	}
}
