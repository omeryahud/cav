package claude

import "time"

// Session mirrors one element of `claude agents --json`.
// Name and Status are only present for background sessions.
type Session struct {
	PID       int    `json:"pid"`
	CWD       string `json:"cwd"`
	Kind      string `json:"kind"`      // "background" | "interactive"
	StartedAt int64  `json:"startedAt"` // epoch milliseconds
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
}

// Short returns the 8-char id prefix (matches ~/.claude/jobs/<short>).
func (s Session) Short() string {
	if len(s.SessionID) >= 8 {
		return s.SessionID[:8]
	}
	return s.SessionID
}

// Display returns the best human label for the session.
func (s Session) Display() string {
	if s.Name != "" {
		return s.Name
	}
	return s.Short()
}

// Started converts the epoch-ms start time to a time.Time.
func (s Session) Started() time.Time {
	return time.UnixMilli(s.StartedAt)
}
