package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/omeryahud/cav/internal/claude"
)

// benchSessions builds n sessions, a quarter of them with an idle worker, plus
// the roster and job records that go with them.
func benchSessions(n int, now time.Time) ([]claude.Session, claude.Roster, []claude.JobRecord) {
	sessions := make([]claude.Session, n)
	roster := claude.Roster{}
	recs := make([]claude.JobRecord, n)
	for i := range sessions {
		sid, jid := fmt.Sprintf("session-%04d", i), fmt.Sprintf("%08x", i)
		sessions[i] = claude.Session{SessionID: sid, Kind: "background", StartedAt: now.Add(-time.Hour).UnixMilli()}
		if i%4 == 0 {
			sessions[i].Status = "idle"
		}
		roster[sid] = jid
		recs[i] = claude.JobRecord{JobID: jid, SessionID: sid, UpdatedAt: now.Add(-10 * time.Minute), InFlightKnown: true}
	}
	return sessions, roster, recs
}

// The two functions auto-stop adds to every refresh, for 80 sessions (about
// what a busy machine holds), 20 of them with a live worker.
func BenchmarkWorkerActivities(b *testing.B) {
	sessions, roster, recs := benchSessions(80, time.Now())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		workerActivities(sessions, roster, recs)
	}
}

func BenchmarkAutoStopTargets(b *testing.B) {
	now := time.Now()
	sessions, roster, recs := benchSessions(80, now)
	m := &Model{all: sessions, roster: roster, workers: workerActivities(sessions, roster, recs),
		attached: map[string]bool{}, lastAttached: map[string]time.Time{}, autoStopping: map[string]time.Time{}}
	m.cfg.AutoStop.After = 5 * time.Minute
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.autoStopTargets(now)
	}
}
