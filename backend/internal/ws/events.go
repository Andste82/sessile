package ws

import (
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Andste82/sessile/backend/internal/session"
)

// HandleEvents serves the session event channel (PROJECT_PLAN.md §5.1): text
// frames carrying session list state, never terminal bytes.
//
// It exists because the per-session socket is open only for a session whose
// terminal is on screen, and the dashboard is by definition the page that
// mounts no terminal — so the one connection that could report on a session is
// the one whose screen the user is already looking at.
//
// Everything hard about the connection is inherited rather than rewritten:
// Client is used unchanged, so this gets the single write-pump goroutine, the
// bounded queue, the ping/pong keep-alive and the slow-consumer policy that a
// terminal client has.
func (h *Handler) HandleEvents(w http.ResponseWriter, r *http.Request, userID string) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", "err", err)
		return
	}

	client := newClient(conn)
	go client.writePump()

	// Subscribe first, snapshot second (§5.1). A change landing between the two
	// is enqueued ahead of the snapshot, and the snapshot is taken afterwards
	// and so already contains it: the client sees one redundant update rather
	// than silently missing one. The other order loses it.
	//
	// Scoped to userID throughout: the snapshot below lists only this user's
	// sessions, and Subscribe records the same id so every later publish is
	// filtered the same way (§10) — no subscriber ever sees another user's
	// session, admins included.
	unsubscribe := h.mgr.Subscribe(client, userID)
	defer unsubscribe()

	h.sendSnapshot(client, userID)

	h.drainPump(client)
	client.Close(websocket.CloseNormalClosure, "")
}

// sendSnapshot primes a freshly subscribed client with userID's session list.
func (h *Handler) sendSnapshot(client *Client, userID string) {
	infos, err := h.mgr.List(userID)
	if err != nil {
		h.log.Warn("event snapshot failed", "err", err)
		// The subscription stands: updates still arrive, and the client falls
		// back to polling for the list it could not be given here.
		client.SendControl(session.ErrorMsg{Type: "error", Message: "cannot list sessions"})
		return
	}
	out := make([]session.JSON, 0, len(infos))
	for _, i := range infos {
		out = append(out, session.ToJSON(i))
	}
	client.SendControl(session.SessionsMsg{Type: "sessions", Sessions: out})
}

// drainPump reads until the connection errors. The event channel defines no
// client→server messages (§5.1), so frames are discarded rather than parsed —
// but something must call ReadMessage, or the pong handler never runs and the
// keep-alive cannot detect a browser that went away.
func (h *Handler) drainPump(client *Client) {
	conn := client.conn
	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}
