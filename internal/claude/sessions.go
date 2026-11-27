package claude

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// recordState tells what a read of one process record gave.
type recordState int

const (
	recordRead    recordState = iota
	recordGone                // no file, or what a process that ended left behind
	recordUnknown             // cav cannot tell: the read failed, or a running process wrote what cav cannot parse
)

// A pid can go to another process, so its start time is checked again this often.
const recheckProcess = time.Minute

// retryRecord waits before a record is read again: records are rewritten in
// place, so a read can land in the middle of a write. Tests replace it.
var retryRecord = func() { time.Sleep(50 * time.Millisecond) }

// workerJobID is the shape of a job id. The id is used as a directory name.
var workerJobID = regexp.MustCompile(`^[0-9a-f]{8}$`)

func sessionsDir() (string, bool) {
	h, err := os.UserHomeDir()
	return filepath.Join(h, ".claude", "sessions"), err == nil
}

// readSessionRecords reads every process record it can. ok is false when the
// directory or a record cannot be read, so the caller cannot rely on the rest.
func readSessionRecords() (recs []sessionRecord, ok bool) {
	dir, ok := sessionsDir()
	if !ok {
		return nil, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		switch r, state := readRecord(filepath.Join(dir, e.Name())); state {
		case recordRead:
			recs = append(recs, r)
		case recordUnknown:
			ok = false
		}
	}
	return recs, ok
}

func readRecord(path string) (sessionRecord, recordState) {
	for try := 0; ; try++ {
		b, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return sessionRecord{}, recordGone
		}
		if err != nil {
			return sessionRecord{}, recordUnknown
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
		retryRecord()
	}
}

// LiveSessions returns the background sessions that have a process, as
// `claude agents --json` lists them. ok is false when cav must ask the CLI.
func LiveSessions() (sessions []Session, ok bool) {
	recs, ok := readSessionRecords()
	if !ok {
		return nil, false
	}
	var workers []sessionRecord
	pids := make(map[int]bool, len(recs))
	for _, r := range recs {
		pids[r.PID] = true
		if r.Kind == "bg" && !r.Spare {
			workers = append(workers, r)
		}
	}
	forgetAllBut(pids)
	live := liveProcesses(workers)
	for _, r := range workers {
		if !live[r.PID] {
			continue
		}
		if !workerJobID.MatchString(r.JobID) || r.SessionID == "" {
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

// listedStatus maps a record's status to what `claude agents --json` prints:
// anything but idle and waiting is busy. A worker that just started has none yet.
func listedStatus(status string) string {
	if status == "idle" || status == "waiting" {
		return status
	}
	return "busy"
}

// processCheck is the answer of one ps check: whether the process with a
// record's pid still is the one that started at the record's start time.
type processCheck struct {
	start string
	same  bool
	at    time.Time
}

// verified remembers the ps checks, so ps does not run on every refresh.
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

// liveProcesses reports which records still belong to the process that wrote
// them. A record can outlive a crash, and its pid can go to another process.
func liveProcesses(recs []sessionRecord) map[int]bool {
	live, ask := checkedLately(recs)
	if len(ask) > 0 {
		pids := make([]int, len(ask))
		for i, r := range ask {
			pids[i] = r.PID
		}
		checkNow(ask, procStarts(pids), live)
	}
	return live
}

// checkedLately answers from the remembered checks, and returns the records
// of running processes that need a new one.
func checkedLately(recs []sessionRecord) (live map[int]bool, ask []sessionRecord) {
	live = make(map[int]bool, len(recs))
	verified.Lock()
	defer verified.Unlock()
	for _, r := range recs {
		c, known := verified.checks[r.PID]
		switch {
		case !running(r.PID):
			delete(verified.checks, r.PID)
		case known && c.start == r.ProcStart && time.Since(c.at) < recheckProcess:
			live[r.PID] = c.same
		default:
			ask = append(ask, r)
		}
	}
	return live, ask
}

// checkNow answers from what ps printed, and remembers it. ps lists every pid
// that runs, so a missing one means ps failed: the last answer stands then.
func checkNow(ask []sessionRecord, starts map[int]string, live map[int]bool) {
	verified.Lock()
	defer verified.Unlock()
	for _, r := range ask {
		c, known := verified.checks[r.PID]
		now, listed := starts[r.PID]
		if !listed {
			live[r.PID] = !known || c.start != r.ProcStart || c.same
			continue
		}
		same := r.ProcStart == "" || now == strings.Join(strings.Fields(r.ProcStart), " ")
		live[r.PID] = same
		verified.checks[r.PID] = processCheck{start: r.ProcStart, same: same, at: time.Now()}
	}
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
