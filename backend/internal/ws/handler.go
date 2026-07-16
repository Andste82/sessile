package ws

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/session"
)

// closeSessionUnavailable is sent when attaching to a missing or stopped
// session (§5: WS close code 4404).
const closeSessionUnavailable = 4404

// Handler upgrades WebSocket connections and bridges them to sessions.
type Handler struct {
	mgr      *session.Manager
	cfg      *config.Config
	log      *slog.Logger
	upgrader websocket.Upgrader
}

// NewHandler builds a WS Handler with an origin-checking upgrader.
func NewHandler(mgr *session.Manager, cfg *config.Config, log *slog.Logger) *Handler {
	h := &Handler{mgr: mgr, cfg: cfg, log: log}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

// checkOrigin enforces same-origin by default, allowing an extra configured
// origin (e.g. the Vite dev server) — PROJECT_PLAN.md §11.
func (h *Handler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (curl, test harness)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if h.cfg.AllowOrigin != "" {
		if a, err := url.Parse(h.cfg.AllowOrigin); err == nil && strings.EqualFold(a.Host, u.Host) {
			return true
		}
	}
	return false
}

// Handle upgrades the request and serves the WebSocket for session id. It is
// registered by the router which supplies the path parameter.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, id string) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Warn("ws upgrade failed", "err", err)
		return
	}

	client := newClient(conn)
	go client.writePump()

	if _, err := h.mgr.Attach(id, client); err != nil {
		reason := "session unavailable"
		if errors.Is(err, session.ErrNotFound) {
			reason = "session not found"
		} else if errors.Is(err, session.ErrStopped) {
			reason = "session stopped"
		}
		client.Close(closeSessionUnavailable, reason)
		return
	}

	h.readPump(client, id)
	h.mgr.Detach(id, client)
	client.Close(websocket.CloseNormalClosure, "")
}

// readPump reads frames until the connection errors: binary → PTY input,
// text → JSON control (resize). It also drives ping/pong keep-alive.
func (h *Handler) readPump(client *Client, id string) {
	conn := client.conn
	conn.SetReadLimit(maxMessageSize)
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			if err := h.mgr.WriteInput(id, data); err != nil {
				h.log.Warn("write input failed", "id", id, "err", err)
				return
			}
		case websocket.TextMessage:
			h.handleControl(client, id, data)
		}
	}
}

// handleControl parses and applies a client control message.
func (h *Handler) handleControl(client *Client, id string, data []byte) {
	var msg inboundControl
	if err := json.Unmarshal(data, &msg); err != nil {
		client.SendControl(session.ErrorMsg{Type: "error", Message: "invalid control message"})
		return
	}
	switch msg.Type {
	case "resize":
		if msg.Rows == 0 || msg.Cols == 0 {
			return
		}
		if err := h.mgr.Resize(id, msg.Rows, msg.Cols); err != nil {
			h.log.Warn("resize failed", "id", id, "err", err)
		}
	default:
		client.SendControl(session.ErrorMsg{Type: "error", Message: "unknown control type"})
	}
}
