// Package preview extracts human-readable conversation snippets from a
// session's transcript: a recent tail for the (non-live) preview pane.
package preview

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Snippet is one user/assistant message.
type Snippet struct {
	Role string // "user" | "assistant"
	Text string
}

type entry struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Recent returns up to n recent user/assistant snippets, oldest-first. It
// tail-reads a window of the transcript so it is fast and robust to very long
// lines (huge tool outputs/pastes) that would stall a whole-file scanner.
func Recent(sessionID string, n int) []Snippet {
	path := findTranscript(sessionID)
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	const window = 512 * 1024
	if fi.Size() > window {
		_, _ = f.Seek(fi.Size()-window, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	var out []Snippet
	for _, ln := range strings.Split(string(data), "\n") {
		if sn, ok := parseLine([]byte(ln)); ok { // partial first line fails JSON parse → skipped
			out = append(out, sn)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

func parseLine(b []byte) (Snippet, bool) {
	var e entry
	if json.Unmarshal(b, &e) != nil {
		return Snippet{}, false
	}
	if e.Type != "user" && e.Type != "assistant" {
		return Snippet{}, false
	}
	txt := strings.TrimSpace(extractText(e.Message.Content))
	if txt == "" ||
		strings.HasPrefix(txt, "<system-reminder>") ||
		strings.HasPrefix(txt, "<command-") ||
		strings.HasPrefix(txt, "<local-command") {
		return Snippet{}, false
	}
	return Snippet{Role: e.Type, Text: txt}, true // raw (newlines kept) so the preview can render markdown
}

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

func findTranscript(sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}
