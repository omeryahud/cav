// Package search scans Claude transcripts on disk for content matches —
// the capability the built-in agents view lacks.
package search

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

// Match scans transcripts under ~/.claude/projects and returns the set of
// sessionIds whose transcript contains query (case-insensitive substring).
// The transcript filename (minus .jsonl) is the sessionId.
func Match(query string) (map[string]bool, error) {
	res := map[string]bool{}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return res, nil
	}
	qb := []byte(q)

	home, err := os.UserHomeDir()
	if err != nil {
		return res, err
	}
	root := filepath.Join(home, ".claude", "projects")

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil //nolint:nilerr // skip unreadable entries
		}
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if res[id] {
			return nil
		}
		if fileContains(path, qb) {
			res[id] = true
		}
		return nil
	})
	return res, err
}

func fileContains(path string, needle []byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // transcripts have long lines
	for sc.Scan() {
		if bytes.Contains(bytes.ToLower(sc.Bytes()), needle) {
			return true
		}
	}
	return false
}
