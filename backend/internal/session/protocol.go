package session

// Client is the session's view of an attached WebSocket connection. The ws
// package implements it. Kept as an interface here to avoid an import cycle
// (ws imports session, not the reverse).
//
// Send / SendControl must be non-blocking: they enqueue onto the client's
// bounded write channel and report false if it is full, which the session
// treats as a slow consumer and drops (PROJECT_PLAN.md §4.4).
type Client interface {
	ID() string
	Send(data []byte) bool     // binary terminal bytes
	SendControl(v any) bool    // text JSON control message
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
