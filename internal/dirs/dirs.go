// Package dirs enumerates candidate directories for starting a new session.
// It is self-contained (pure Go, no `fd` and no dependency on the cdf config):
// roots come from cav's own ~/.config/cav/roots.txt when present, else common
// dev directories are auto-detected. Each root is walked with a depth cap and
// the usual noise directories pruned.
package dirs

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxDepth bounds how deep below a root we descend (keeps the walk fast and
// the candidate list manageable even under large trees).
const maxDepth = 8

var excluded = map[string]bool{
	".git": true, "node_modules": true, ".cache": true, "Library": true,
	".Trash": true, ".npm": true, ".yarn": true, ".pnpm": true, ".rustup": true,
	".cargo": true, ".terraform": true, ".venv": true, "__pycache__": true,
	"target": true, "build": true, "dist": true, "out": true, "pkg": true,
	"vendor": true, "Pictures": true, ".kube": true, ".oh-my-zsh": true,
	".DocumentRevisions-V100": true, ".Spotlight-V100": true, ".fseventsd": true,
}

// Candidates returns the roots plus their descendant directories, sorted and
// de-duplicated.
func Candidates() []string {
	set := map[string]struct{}{}
	for _, r := range roots() {
		set[r] = struct{}{}
		walk(r, set)
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func configRootsFile() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cav", "roots.txt")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cav", "roots.txt")
}

// roots returns the directories to scan: cav's configured roots if present,
// else any of the usual dev locations that exist, else $HOME.
func roots() []string {
	if rs := readList(configRootsFile()); len(rs) > 0 {
		return rs
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var rs []string
	for _, c := range []string{"go/src", "src", "dev", "projects", "code", "work", "repos", "Documents"} {
		if p := filepath.Join(home, c); isDir(p) {
			rs = append(rs, p)
		}
	}
	if len(rs) == 0 {
		rs = []string{home}
	}
	return rs
}

// readList parses a roots file: one path per line, '#' comments, leading ~
// expanded, only existing directories kept.
func readList(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "~") {
			line = home + line[1:]
		}
		if isDir(line) {
			out = append(out, line)
		}
	}
	return out
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func walk(root string, set map[string]struct{}) {
	base := strings.Count(filepath.Clean(root), string(os.PathSeparator))
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir // unreadable dir — skip its subtree
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && excluded[d.Name()] {
			return fs.SkipDir
		}
		if strings.Count(filepath.Clean(path), string(os.PathSeparator))-base >= maxDepth {
			return fs.SkipDir
		}
		set[path] = struct{}{}
		return nil
	})
}
