package unpark

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
	entries, _ := os.ReadDir(filepath.Join(dir, "cav"))
	for _, e := range entries {
		if n := e.Name(); n != "unparked.json" && n != "unparked.json.lock" {
			t.Errorf("temp file left behind: %s", n)
		}
	}
}

// Two cavs that write at the same moment must both keep their marks.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const perStore = 100
	var wg sync.WaitGroup
	for _, prefix := range []string{"a", "b"} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			for i := 0; i < perStore; i++ {
				if err := store.Add(fmt.Sprintf("%s%d", prefix, i)); err != nil {
					t.Error(err)
					return
				}
			}
		}(Load())
	}
	wg.Wait()

	onDisk := Load()
	for _, prefix := range []string{"a", "b"} {
		for i := 0; i < perStore; i++ {
			if id := fmt.Sprintf("%s%d", prefix, i); !onDisk.Has(id) {
				t.Fatalf("mark %s was lost", id)
			}
		}
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
