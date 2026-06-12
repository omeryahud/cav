// Package claude wraps the `claude` CLI verbs cav relies on.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"
)

// Roster maps a full sessionId to its job id — the identifier that
// `claude attach|stop|logs` expect. The job id usually equals the session's
// 8-char prefix, but NOT always (e.g. respawned or busy sessions: 72cdfc0f
// lives under job cc12801d), so we read the authoritative daemon roster
// instead of guessing from the session id.
// Roster maps a full sessionId to its job id (the id attach/stop/logs expect).
type Roster map[string]string

// JobID returns the attachable job id for s, or "" if s is not attachable.
func (r Roster) JobID(s Session) string { return r[s.SessionID] }

type rosterFile struct {
	Workers map[string]struct {
		SessionID string `json:"sessionId"`
	} `json:"workers"`
}

// LoadRoster maps each live daemon worker's CURRENT sessionId to its job id
// (~/.claude/daemon/roster.json). Authoritative for live sessions — notably
// after a /branch or fork, where a job's state.json may still carry the
// original sessionId while the roster tracks the new one.
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
	if json.Unmarshal(b, &rf) != nil {
		return r
	}
	for jobID, w := range rf.Workers {
		if w.SessionID != "" {
			r[w.SessionID] = jobID
		}
	}
	return r
}

// JobState returns a job's lifecycle state from ~/.claude/jobs/<jobID>/state.json.
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

// JobRecord is a session's durable on-disk job record (~/.claude/jobs/<jobId>/state.json).
type JobRecord struct {
	JobID     string
	SessionID string
	CWD       string
	Name      string
	State     string // working | done | blocked | stopped | ...
	UpdatedAt time.Time
}

// ScanJobs reads all on-disk job records. Unlike `agents --json` (which lists
// only live daemon workers) this survives the daemon dropping workers after a
// laptop sleep, so cav keeps showing sessions that are still resumable. The job
// directory name is the job id that attach/stop/logs key on.
func ScanJobs() []JobRecord {
	h, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, _ := filepath.Glob(filepath.Join(h, ".claude", "jobs", "*", "state.json"))
	out := make([]JobRecord, 0, len(matches))
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var st struct {
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
			Name      string `json:"name"`
			State     string `json:"state"`
			UpdatedAt string `json:"updatedAt"`
		}
		if json.Unmarshal(b, &st) != nil || st.SessionID == "" {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, st.UpdatedAt) // zero on failure → treated as recent
		out = append(out, JobRecord{
			JobID:     filepath.Base(filepath.Dir(p)),
			SessionID: st.SessionID,
			CWD:       st.CWD,
			Name:      st.Name,
			State:     st.State,
			UpdatedAt: ts,
		})
	}
	return out
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

// Create starts a new background session in cwd with an optional name and prompt
// (an empty prompt yields an idle session). It returns the new session's short
// job id, parsed from `claude --bg`'s output, for attaching to it; the id is ""
// if it couldn't be parsed.
func Create(ctx context.Context, cwd, name, prompt string) (string, error) {
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("claude --bg: %w: %s", err, out)
	}
	return parseJobID(string(out)), nil
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// parseJobID pulls the 8-char job id out of `claude --bg`'s output, e.g.
// "backgrounded · a7dc5ce7 · name (idle…)" and "claude attach a7dc5ce7".
func parseJobID(out string) string {
	out = ansiRE.ReplaceAllString(out, "")
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`backgrounded[^\n]*?([0-9a-f]{8})`),
		regexp.MustCompile(`attach\s+([0-9a-f]{8})`),
	} {
		if m := re.FindStringSubmatch(out); m != nil {
			return m[1]
		}
	}
	return ""
}

// AttachCmd builds the command to attach to a session (full terminal handoff).
func AttachCmd(id string) *exec.Cmd {
	return exec.Command(Bin(), "attach", id)
}

// LogsShellCmd builds a shell command that pages a session's logs with less.
func LogsShellCmd(id string) *exec.Cmd {
	return exec.Command("sh", "-c", fmt.Sprintf("%s logs %s | less -R +G", Bin(), id))
}

// ResumeAttachCmd respawns a stopped/dropped background session, then attaches —
// the path the native agents view uses. `claude attach` alone fails once the
// daemon has released the worker ("job not found"); `claude respawn` restarts it
// (same job id) from the stored respawnFlags/resumeSessionId, after which attach
// succeeds.
func ResumeAttachCmd(jobID string) *exec.Cmd {
	return exec.Command("sh", "-c", fmt.Sprintf("%s respawn %s && %s attach %s", Bin(), jobID, Bin(), jobID))
}

// Logs returns a live session's recent terminal output as raw bytes (with the
// ANSI/cursor-control sequences intact, for a terminal emulator to reconstruct
// the screen). Only works for a session with a live daemon worker; a
// stopped/exited session yields an error ("job not found").
func Logs(ctx context.Context, id string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, Bin(), "logs", id).Output()
	if err != nil {
		return nil, fmt.Errorf("claude logs: %w", err)
	}
	return out, nil
}
