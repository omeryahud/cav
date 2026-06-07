// Package dirs enumerates candidate directories for starting a new session.
// Pure Go (no `fd`): roots come from cav's own ~/.config/cav/roots.txt when
// present, else common dev directories are auto-detected; the user's `cdf` roots
// (~/.config/cdfpaths.txt) are added too, so the picker offers the same dirs as
// `cdf` — minus $HOME, which is too broad to walk eagerly. Each root is walked
// with a depth cap and the usual noise directories pruned.
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

// roots returns the directories to scan: cav's configured roots (or, if none,
// the usual dev locations) plus the user's cdf roots (see cdfRoots), with $HOME
// as a last resort.
func roots() []string {
	home, _ := os.UserHomeDir()
	rs := readList(configRootsFile())
	if len(rs) == 0 {
		for _, c := range []string{"go/src", "src", "dev", "projects", "code", "work", "repos", "Documents"} {
			if p := filepath.Join(home, c); isDir(p) {
				rs = append(rs, p)
			}
		}
	}
	rs = dedupClean(append(rs, cdfRoots(home)...))
	if len(rs) == 0 && home != "" {
		rs = []string{home}
	}
	return rs
}

// cdfRoots returns the directories from the user's cdf path file
// (~/.config/cdfpaths.txt, or $CDFPATHS_FILE) so the picker offers the same
// directories as the `cdf` shell command. $HOME itself is skipped: cdf walks it
// lazily with fd, but cav's eager walk of all of $HOME is far too slow/large.
func cdfRoots(home string) []string {
	var out []string
	for _, p := range readList(cdfPathsFile()) {
		if home != "" && filepath.Clean(p) == filepath.Clean(home) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func cdfPathsFile() string {
	if f := os.Getenv("CDFPATHS_FILE"); f != "" {
		return f
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cdfpaths.txt")
}

// dedupClean drops duplicate paths (compared after Clean), preserving order, so
// a root listed in several sources is walked only once.
func dedupClean(paths []string) []string {
	seen := map[string]bool{}
	out := paths[:0]
	for _, p := range paths {
		if c := filepath.Clean(p); !seen[c] {
			seen[c] = true
			out = append(out, p)
		}
	}
	return out
}

// readList parses a roots file: one path per line, '#' comments, ${VAR}/$VAR
// and leading ~ expanded, only existing directories kept. Matches what cdf
// accepts in its path file.
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
		line = os.ExpandEnv(line)
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
