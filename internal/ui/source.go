package ui

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/omeryahud/cav/internal/claude"
	"github.com/omeryahud/cav/internal/config"
)

// Tests stub the readers of claude's process records.
var (
	liveRecords = claude.LiveSessions
	liveRecord  = claude.LiveSession
)

// sessionSource lists the sessions for a refresh. Starting `claude agents`
// costs about 160 MB and a third of a CPU second, so it runs only when needed.
type sessionSource struct {
	safetyPoll time.Duration
	polling    bool                // no file events: ask the CLI on every refresh
	track      func(jobs []string) // told the live workers before their job files are read
	listing    []claude.Session    // the last full listing
	listedAt   time.Time
	workers    string // the worker set at the last full listing
	force      bool   // the user asked for a full listing
}

// list returns the sessions as `claude agents --json` lists them. fromRecords
// tells that the live ones were read from the process records, with no CLI call.
func (src *sessionSource) list(ctx context.Context) (sessions []claude.Session, fromRecords bool, err error) {
	workers, ok := liveRecords()
	if !ok || src.polling {
		sessions, err = liveSessions(ctx)
		return sessions, false, err
	}
	if src.track != nil {
		src.track(workerJobs(workers))
	}
	// The CLI still gives the sessions the daemon lists with no process.
	if key := workerKey(workers); src.force || key != src.workers || time.Since(src.listedAt) >= src.safetyPoll {
		if full, err := liveSessions(ctx); err == nil {
			src.listing = full
		}
		src.listedAt, src.workers, src.force = time.Now(), key, false
	}
	return mergeListing(workers, src.listing), true, nil
}

// workerKey names a worker set, to tell when a worker started or ended.
func workerKey(workers []claude.Session) string {
	ids := workerJobs(workers)
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// mergeListing keeps the listing's order and its entries with no process, and
// takes every entry with a process from the records, which are the fresher truth.
func mergeListing(workers, listing []claude.Session) []claude.Session {
	byJob := make(map[string]claude.Session, len(workers))
	for _, w := range workers {
		byJob[w.JobID] = w
	}
	out := make([]claude.Session, 0, len(listing)+len(workers))
	used := map[string]bool{}
	for _, s := range listing {
		if w, ok := byJob[s.JobID]; ok {
			out = append(out, w)
			used[s.JobID] = true
		} else if s.Status == "" {
			out = append(out, s)
		}
	}
	for _, w := range workers {
		if !used[w.JobID] {
			out = append(out, w)
		}
	}
	return out
}

// workerJobs returns the job ids of the sessions that have a process.
func workerJobs(sessions []claude.Session) []string {
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		if hasLiveWorker(s) && s.JobID != "" {
			ids = append(ids, s.JobID)
		}
	}
	return ids
}

// eventInterval is the longest wait between refreshes when file events drive
// them. Auto-stop needs a steady look at who is attached; that leaves no file.
func eventInterval(l config.List, watchAttached bool) time.Duration {
	if watchAttached && l.IdleRefresh < l.SafetyPoll {
		return l.IdleRefresh
	}
	return l.SafetyPoll
}
