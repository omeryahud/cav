package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
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

const (
	// A record is rewritten in place, so a read can land in the middle of a write.
	recordRetry = 50 * time.Millisecond
	// A pid can go to another process, so its start time is checked again this often.
	recheckProcess = time.Minute
)

func sessionsDir() (string, bool) {
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".claude", "sessions"), err == nil
}

// readSessionRecords reads every process record it can. ok is false when the
// directory is missing or the record of a running process cannot be parsed.
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
		r, state := readRecord(f)
		switch state {
		case recordRead:
			recs = append(recs, r)
		case recordUnknown:
			ok = false
		}
	}
	return recs, ok
}

const (
	recordRead    = iota
	recordGone    // no file, or what a process that ended left behind
	recordUnknown // a running process wrote something cav cannot parse
)

func readRecord(path string) (sessionRecord, int) {
	for try := 0; ; try++ {
		b, err := os.ReadFile(path)
		if err != nil {
			return sessionRecord{}, recordGone
		}
		var r sessionRecord
		if json.Unmarshal(b, &r) == nil {
			return r, recordRead
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(path), ".json"))
		if err != nil || !running(pid) {
			return sessionRecord{}, recordGone
		}
		if try > 0 {
			return sessionRecord{}, recordUnknown
		}
		time.Sleep(recordRetry)
	}
}

// LiveSessions returns the background sessions that have a process, as
// `claude agents --json` lists them. ok is false when cav must ask the CLI.
func LiveSessions() (sessions []Session, ok bool) {
	recs, ok := readSessionRecords()
	if !ok {
		return nil, false
	}
	pids := make(map[int]bool, len(recs))
	for _, r := range recs {
		pids[r.PID] = true
		if r.Kind != "bg" || r.Spare || !recordIsLive(r) {
			continue
		}
		if r.JobID == "" || r.SessionID == "" {
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
	forgetAllBut(pids)
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

// listedStatus maps a record's status to what `claude agents --json` prints:
// anything but idle and waiting is busy. A worker that just started has none yet.
func listedStatus(status string) string {
	if status == "idle" || status == "waiting" {
		return status
	}
	return "busy"
}

type processCheck struct {
	start string
	at    time.Time
}

// verified remembers the records whose process start was checked with ps, so
// the check does not run on every refresh.
var verified = struct {
	sync.Mutex
	checks map[int]processCheck
}{checks: map[int]processCheck{}}

func running(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// recordIsLive reports whether r's process still runs. A record can outlive a
// crash, and its pid can go to another process.
func recordIsLive(r sessionRecord) bool {
	if !running(r.PID) {
		verified.Lock()
		delete(verified.checks, r.PID)
		verified.Unlock()
		return false
	}
	verified.Lock()
	c, known := verified.checks[r.PID]
	verified.Unlock()
	if known && c.start == r.ProcStart && time.Since(c.at) < recheckProcess {
		return true
	}
	if !sameProcess(r.PID, r.ProcStart) {
		return false
	}
	verified.Lock()
	verified.checks[r.PID] = processCheck{start: r.ProcStart, at: time.Now()}
	verified.Unlock()
	return true
}

// forgetAllBut drops the checks of the processes that have no record any more.
func forgetAllBut(pids map[int]bool) {
	verified.Lock()
	defer verified.Unlock()
	for pid := range verified.checks {
		if !pids[pid] {
			delete(verified.checks, pid)
		}
	}
}
