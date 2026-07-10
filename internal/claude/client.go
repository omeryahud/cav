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

// attachWatch is a sh prelude defining cav_attach, which runs `claude attach`
// under a watchdog so that detaching with ← returns to cav. Since claude
// ~2.1.2xx, an ←-detach doesn't exit the attach process: it relaunches the
// native agent view (`claude agents`, CLAUDE_AGENTS_SELECT preselected) on the
// same terminal — observed as the attach process exec-replacing itself (same
// pid, new args), with a spawned blocking child as the code's other path —
// which would sit on top of a suspended cav until quit by hand. There is no
// CLI flag/env to disable just that (the disableAgentView setting kills the
// whole background subsystem), so cav polls the attach pid (and its children)
// and kills it the moment its args become `… agents` — then cav resumes.
// Killing the view/client never harms the session (it keeps running either
// way; the ~10Hz poll makes the view at most a brief flash). A kill is
// reported as exit 0 (a detach is a normal way out); anything else keeps the
// real status.
//
// Two sh subtleties, both load-bearing: the attach must run backgrounded (so
// the shell can poll), and a background command in a non-interactive shell
// gets stdin from /dev/null unless explicitly redirected — `<&0` keeps the
// real terminal fd (re-opening /dev/tty instead breaks input: macOS can't
// kqueue-poll /dev/tty, so node never sees keystrokes). The binary and job id
// are passed via env (CAV_CLAUDE / CAV_JOB) to avoid quoting.
const attachWatch = `cav_attach() {
	"$CAV_CLAUDE" attach "$1" <&0 & A=$!
	K=0
	while kill -0 "$A" 2>/dev/null; do
		case "$(ps -ww -o args= -p "$A" 2>/dev/null)" in
		*\ agents) kill "$A" 2>/dev/null && K=1 ;;
		esac
		for c in $(pgrep -P "$A" 2>/dev/null); do
			case "$(ps -ww -o args= -p "$c" 2>/dev/null)" in
			*\ agents) kill "$c" 2>/dev/null && K=1 ;;
			esac
		done
		sleep 0.1
	done
	wait "$A"; S=$?
	[ "$K" = 1 ] && return 0
	return "$S"
}
`

// attachEnv is the environment for the attach watchdog shell.
func attachEnv(jobID string) []string {
	return append(os.Environ(), "CAV_CLAUDE="+Bin(), "CAV_JOB="+jobID)
}

// AttachCmd builds the command to attach to a session (full terminal handoff),
// watchdogged so an ←-detach returns to cav instead of the native agent view.
func AttachCmd(id string) *exec.Cmd {
	cmd := exec.Command("sh", "-c", attachWatch+`cav_attach "$CAV_JOB"`)
	cmd.Env = attachEnv(id)
	return cmd
}

// LogsShellCmd builds a shell command that pages a session's logs with less.
func LogsShellCmd(id string) *exec.Cmd {
	return exec.Command("sh", "-c", fmt.Sprintf("%s logs %s | less -R +G", Bin(), id))
}

// ResumeAttachCmd respawns a stopped/dropped background session, then attaches —
// the path the native agents view uses. `claude attach` alone fails once the
// daemon has released the worker ("job not found"); `claude respawn` restarts it
// (same job id) from the stored respawnFlags/resumeSessionId, after which attach
// succeeds. The attach runs under the same ←-detach watchdog as AttachCmd.
func ResumeAttachCmd(jobID string) *exec.Cmd {
	cmd := exec.Command("sh", "-c", attachWatch+`"$CAV_CLAUDE" respawn "$CAV_JOB" && cav_attach "$CAV_JOB"`)
	cmd.Env = attachEnv(jobID)
	return cmd
}

// Fork starts a new background session that continues parentSessionID's
// conversation under a fresh session id (`--bg --resume <id> --fork-session`),
// reusing the parent job's cwd and respawn flags (read from its state.json, with
// the parent's --name dropped so the fork isn't named identically). Returns the
// new child's job id. The daemon records no parent link, so the caller tracks it.
func Fork(ctx context.Context, parentSessionID, parentJobID, cwd string) (string, error) {
	if parentSessionID == "" {
		return "", fmt.Errorf("fork: no parent session id")
	}
	var flags []string // reuse the parent's respawn flags (model etc.), minus --name
	if parentJobID != "" {
		if h, err := os.UserHomeDir(); err == nil {
			if b, err := os.ReadFile(filepath.Join(h, ".claude", "jobs", parentJobID, "state.json")); err == nil {
				var st struct {
					RespawnFlags []string `json:"respawnFlags"`
				}
				if json.Unmarshal(b, &st) == nil {
					for i := 0; i < len(st.RespawnFlags); i++ {
						if st.RespawnFlags[i] == "--name" {
							i++ // skip its value; the fork shouldn't inherit the parent's name
							continue
						}
						flags = append(flags, st.RespawnFlags[i])
					}
				}
			}
		}
	}
	args := append([]string{"--bg", "--resume", parentSessionID, "--fork-session"}, flags...)
	cmd := exec.CommandContext(ctx, Bin(), args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("claude --bg --resume --fork-session: %w: %s", err, out)
	}
	return parseJobID(string(out)), nil
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
