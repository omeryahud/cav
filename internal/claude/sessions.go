package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// sessionRecord is what a claude process keeps about itself in
// ~/.claude/sessions/<pid>.json.
type sessionRecord struct {
	PID             int    `json:"pid"`
	ProcStart       string `json:"procStart"`
	Kind            string `json:"kind"` // "bg" for a background worker
	Spare           bool   `json:"spare"`
	JobID           string `json:"jobId"`
	SessionID       string `json:"sessionId"`
	CWD             string `json:"cwd"`
	Name            string `json:"name"`
	StartedAt       int64  `json:"startedAt"`
	Status          string `json:"status"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	ParkedJobID     string `json:"parkedJobId"`
}

func sessionsDir() (string, bool) {
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".claude", "sessions"), err == nil
}

// readSessionRecords reads every process record. ok is false when the
// directory is missing or a record cannot be parsed.
func readSessionRecords() (recs []sessionRecord, ok bool) {
	dir, ok := sessionsDir()
	if !ok {
		return nil, false
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, false
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // the process just ended
		}
		var r sessionRecord
		if json.Unmarshal(b, &r) != nil {
			return nil, false
		}
		recs = append(recs, r)
	}
	return recs, true
}

// LiveSessions returns the background sessions that have a process, as
// `claude agents --json` lists them, read from the records the processes keep.
// ok is false when the records cannot be relied on: then ask the CLI.
func LiveSessions() (sessions []Session, ok bool) {
	recs, ok := readSessionRecords()
	if !ok {
		return nil, false
	}
	for _, r := range recs {
		if r.Kind != "bg" || r.Spare || !recordIsLive(r) {
			continue
		}
		if r.JobID == "" || r.SessionID == "" || r.Status == "" {
			return nil, false // a shape cav does not know
		}
		sessions = append(sessions, Session{
			JobID:       r.JobID,
			PID:         r.PID,
			CWD:         r.CWD,
			Kind:        "background",
			StartedAt:   r.StartedAt,
			SessionID:   r.SessionID,
			Name:        r.Name,
			Status:      listedStatus(r.Status),
			StatusSince: r.StatusUpdatedAt,
		})
	}
	return sessions, true
}

// LiveSession returns the live session of one job, read the same way.
func LiveSession(jobID string) (Session, bool) {
	sessions, _ := LiveSessions()
	for _, s := range sessions {
		if s.JobID == jobID {
			return s, true
		}
	}
	return Session{}, false
}

// listedStatus maps a record's status to what `claude agents --json` prints.
// A worker running a shell command is listed as busy.
func listedStatus(status string) string {
	if status == "shell" {
		return "busy"
	}
	return status
}

// verified remembers the records whose process start was checked with ps, so
// the check runs once per process and not on every refresh.
var verified = struct {
	sync.Mutex
	start map[int]string
}{start: map[int]string{}}

// recordIsLive reports whether r's process still runs. A record can outlive a
// crash, and its pid can go to another process.
func recordIsLive(r sessionRecord) bool {
	if r.PID <= 0 {
		return false
	}
	if err := syscall.Kill(r.PID, 0); err != nil && err != syscall.EPERM {
		forget(r.PID)
		return false
	}
	verified.Lock()
	seen, known := verified.start[r.PID]
	verified.Unlock()
	if known && seen == r.ProcStart {
		return true
	}
	if !sameProcess(r.PID, r.ProcStart) {
		return false
	}
	verified.Lock()
	verified.start[r.PID] = r.ProcStart
	verified.Unlock()
	return true
}

func forget(pid int) {
	verified.Lock()
	delete(verified.start, pid)
	verified.Unlock()
}
