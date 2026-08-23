package session

import "time"

// JSON is the wire shape of a session (PROJECT_PLAN.md §6) — the single source
// of truth, mirrored in frontend/src/api/types.ts.
//
// It lives here rather than in internal/api because two packages serialise it:
// the REST handlers and the event channel in internal/ws (§5.1). api imports
// ws, so a shape owned by api could not be reached from either of the places
// that push it — the same import-cycle reason the control messages below are
// defined in this package.
type JSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Directory    string `json:"directory"`
	Shell        string `json:"shell"`
	Status       string `json:"status"`
	PID          int    `json:"pid"`
	Created      string `json:"created"`
	LastActivity string `json:"lastActivity"`
	Rows         uint16 `json:"rows"`
	Cols         uint16 `json:"cols"`
	ClientCount  int    `json:"clientCount"`

	// Derived state (§4.7). Both are "" for a stopped session, and where they
	// could not be determined.
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

// ToJSON converts a session snapshot to its wire shape.
func ToJSON(i Info) JSON {
	return JSON{
		ID:           i.ID,
		Name:         i.Name,
		Directory:    i.Directory,
		Shell:        i.Shell,
		Status:       string(i.Status),
		PID:          i.PID,
		Created:      rfc3339(i.Created),
		LastActivity: rfc3339(i.LastActivity),
		Rows:         i.Rows,
		Cols:         i.Cols,
		ClientCount:  i.ClientCount,
		Command:      i.Command,
		Cwd:          i.Cwd,
	}
}

// rfc3339 formats a timestamp the way §6 requires, and renders the zero time as
// an empty string rather than year 1: "0001-01-01T00:00:00Z" would arrive in
// the browser as a date to render rather than as the absence of one.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Client is the session's view of an attached WebSocket connection. The ws
// package implements it. Kept as an interface here to avoid an import cycle
// (ws imports session, not the reverse).
//
// Send / SendControl must be non-blocking: they enqueue onto the client's
// bounded write channel and report false if it is full, which the session
// treats as a slow consumer and drops (PROJECT_PLAN.md §4.4).
type Client interface {
	ID() string
	Send(data []byte) bool  // binary terminal bytes
	SendControl(v any) bool // text JSON control message
	Close(code int, reason string)
}

// Server → client control messages (PROJECT_PLAN.md §5).

// AttachedMsg is sent immediately after upgrade, before the buffer replay.
type AttachedMsg struct {
	Type        string `json:"type"` // "attached"
	SessionID   string `json:"sessionId"`
	ReplayBytes int    `json:"replayBytes"`
}

// ExitMsg is sent when the shell process ends.
type ExitMsg struct {
	Type string `json:"type"` // "exit"
}

// ErrorMsg carries a human-readable error to the client.
type ErrorMsg struct {
	Type    string `json:"type"` // "error"
	Message string `json:"message"`
}

func newAttached(id string, replayBytes int) AttachedMsg {
	return AttachedMsg{Type: "attached", SessionID: id, ReplayBytes: replayBytes}
}

// Event-channel messages (PROJECT_PLAN.md §5.1). These travel on /ws/events,
// which carries session list state and never terminal bytes.

// SessionsMsg is the full snapshot sent once, immediately after subscribing.
type SessionsMsg struct {
	Type     string `json:"type"` // "sessions"
	Sessions []JSON `json:"sessions"`
}

// SessionMsg carries one session that was created or changed.
type SessionMsg struct {
	Type    string `json:"type"` // "session"
	Session JSON   `json:"session"`
}

// SessionGoneMsg carries the id of a session that was deleted.
type SessionGoneMsg struct {
	Type      string `json:"type"` // "sessionGone"
	SessionID string `json:"sessionId"`
}
