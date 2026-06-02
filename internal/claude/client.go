// Package claude wraps the `claude` CLI verbs cav relies on.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Roster maps a full sessionId to its job id — the identifier that
// `claude attach|stop|logs` expect. The job id usually equals the session's
// 8-char prefix, but NOT always (e.g. respawned or busy sessions: 72cdfc0f
// lives under job cc12801d), so we read the authoritative daemon roster
// instead of guessing from the session id.
type Roster map[string]string

type rosterFile struct {
	Workers map[string]struct {
		SessionID string `json:"sessionId"`
	} `json:"workers"`
}

// LoadRoster reads ~/.claude/daemon/roster.json. Sessions absent from the
// roster (interactive sessions, anything the daemon isn't supervising) are
// not attachable and won't appear in the returned map.
func LoadRoster() Roster {
	r := Roster{}
	h, err := os.UserHomeDir()
	if err != nil {
		return r
	}
	b, err := os.ReadFile(filepath.Join(h, ".claude", "daemon", "roster.json"))
	if err != nil {
		return r
	}
	var rf rosterFile
	if err := json.Unmarshal(b, &rf); err != nil {
		return r
	}
	for jobID, w := range rf.Workers {
		if w.SessionID != "" {
			r[w.SessionID] = jobID
		}
	}
	return r
}

// JobID returns the attachable job id for s, or "" if s is not attachable.
func (r Roster) JobID(s Session) string { return r[s.SessionID] }

// JobState returns a job's lifecycle state from ~/.claude/jobs/<jobID>/state.json
// — "working", "done", "blocked", etc. — or "" if unavailable. This is richer
// than the coarse idle/busy in `agents --json`.
func JobState(jobID string) string {
	if jobID == "" {
		return ""
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(h, ".claude", "jobs", jobID, "state.json"))
	if err != nil {
		return ""
	}
	var st struct {
		State string `json:"state"`
	}
	if json.Unmarshal(b, &st) != nil {
		return ""
	}
	return st.State
}

// Bin is the claude executable name; override with $CLAUDE_BIN.
func Bin() string {
	if b := os.Getenv("CLAUDE_BIN"); b != "" {
		return b
	}
	return "claude"
}

// List returns current sessions from `claude agents --json`.
func List(ctx context.Context) ([]Session, error) {
	out, err := exec.CommandContext(ctx, Bin(), "agents", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("claude agents --json: %w", err)
	}
	var ss []Session
	if err := json.Unmarshal(out, &ss); err != nil {
		return nil, fmt.Errorf("parse agents json: %w", err)
	}
	return ss, nil
}

// Stop terminates a session (process dies; history/transcript is retained).
func Stop(ctx context.Context, id string) error {
	if out, err := exec.CommandContext(ctx, Bin(), "stop", id).CombinedOutput(); err != nil {
		return fmt.Errorf("claude stop: %w: %s", err, out)
	}
	return nil
}

// Create starts a new background session in cwd with an optional name and prompt.
func Create(ctx context.Context, cwd, name, prompt string) error {
	args := []string{"--bg"}
	if name != "" {
		args = append(args, "--name", name)
	}
	if prompt != "" {
		args = append(args, prompt)
	}
	cmd := exec.CommandContext(ctx, Bin(), args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("claude --bg: %w: %s", err, out)
	}
	return nil
}

// AttachCmd builds the command to attach to a session (full terminal handoff).
func AttachCmd(id string) *exec.Cmd {
	return exec.Command(Bin(), "attach", id)
}

// LogsShellCmd builds a shell command that pages a session's logs with less.
func LogsShellCmd(id string) *exec.Cmd {
	return exec.Command("sh", "-c", fmt.Sprintf("%s logs %s | less -R +G", Bin(), id))
}
