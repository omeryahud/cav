package unpark

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two stores on the same file stand for two open cavs.
func TestReloadPicksUpAnotherStoresMarks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	a, b := Load(), Load()

	if err := a.Add("s1"); err != nil {
		t.Fatal(err)
	}
	if b.Has("s1") {
		t.Fatal("setup: b should not see the mark before it reloads")
	}
	b.Reload()
	if !b.Has("s1") {
		t.Error("after Reload, b should see the mark a wrote")
	}

	if err := b.Add("s2"); err != nil {
		t.Fatal(err)
	}
	a.Reload()
	if !a.Has("s1") || !a.Has("s2") {
		t.Error("b's write must keep a's mark, and a should see b's after Reload")
	}

	if err := a.Remove("s1"); err != nil {
		t.Fatal(err)
	}
	b.Reload()
	if b.Has("s1") || !b.Has("s2") {
		t.Error("a removal by a should reach b after Reload")
	}
}

// A cav that writes before its next refresh has not reloaded. Its write must
// still keep what the other cav wrote in between.
func TestWritesKeepAnotherStoresMarksWithoutAReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	a, b := Load(), Load()

	if err := a.Add("s1"); err != nil {
		t.Fatal(err)
	}
	if err := b.Add("s2"); err != nil {
		t.Fatal(err)
	}
	if err := a.Remove("s1"); err != nil {
		t.Fatal(err)
	}

	onDisk := Load()
	if onDisk.Has("s1") || !onDisk.Has("s2") {
		t.Errorf("file should hold only s2 (b's mark kept, a's removed); s1=%v s2=%v", onDisk.Has("s1"), onDisk.Has("s2"))
	}
	left, _ := filepath.Glob(filepath.Join(dir, "cav", "unparked.json.*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestAddReportsAFailedWrite(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocker)
	if err := Load().Add("s1"); err == nil {
		t.Error("Add should fail when the config directory cannot be created")
	}
}

func TestReloadKeepsTheSetWhenTheFileIsHalfWritten(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	s := Load()
	if err := s.Add("s1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cav", "unparked.json")
	if err := os.WriteFile(path, []byte(`["s1", "s`), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if !s.Has("s1") {
		t.Error("an unreadable file must not wipe the marks")
	}
}

func TestReloadWithoutAFileIsANoOp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := Load()
	s.Reload()
	if s.Has("anything") {
		t.Error("an empty store should stay empty")
	}
}
