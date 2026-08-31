package ws_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/Andste82/sessile/backend/internal/api"
	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/ws"
)

// eventEnvelope is enough of §5.1 to assert on without duplicating the shapes.
type eventEnvelope struct {
	Type      string         `json:"type"`
	SessionID string         `json:"sessionId"`
	Session   session.JSON   `json:"session"`
	Sessions  []session.JSON `json:"sessions"`
}

func eventsServer(t *testing.T) (*httptest.Server, string, *http.Cookie) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	cfg := &config.Config{Shells: []string{"sh"}, BufferSize: 64 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(root, cfg.Shells, cfg.BufferSize, t.TempDir(), nil, log)
	users, webSessions, cookie := newTestAuth(t)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log, root, nil, users, webSessions, nil)

	ts := httptest.NewServer(srv.Router(nil))
	t.Cleanup(func() {
		mgr.Shutdown()
		ts.Close()
	})
	return ts, "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/events", cookie
}

// readEvent reads one text frame and decodes it. The event channel carries no
// binary at all, so a binary frame is a protocol violation worth failing on.
func readEvent(t *testing.T, c *websocket.Conn, timeout time.Duration) eventEnvelope {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	mt, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("event channel sent message type %d, want text only", mt)
	}
	var ev eventEnvelope
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("decode event %q: %v", data, err)
	}
	return ev
}

// waitForEvent reads until one satisfies match, or the deadline passes.
func waitForEvent(t *testing.T, c *websocket.Conn, what string, match func(eventEnvelope) bool) eventEnvelope {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ev := readEvent(t, c, 10*time.Second)
		if match(ev) {
			return ev
		}
	}
	t.Fatalf("never received %s", what)
	return eventEnvelope{}
}

// The documented attach sequence: the snapshot arrives first, and it lists
// sessions that already existed.
func TestEventChannelOpensWithASnapshot(t *testing.T) {
	ts, wsURL, cookie := eventsServer(t)
	existing := createSession(t, ts.URL, `{"name":"before","directory":".","shell":"sh"}`, cookie)

	c := dialWS(t, wsURL, cookie)
	defer c.Close()

	ev := readEvent(t, c, 10*time.Second)
	if ev.Type != "sessions" {
		t.Fatalf("first event type %q, want %q", ev.Type, "sessions")
	}
	found := false
	for _, s := range ev.Sessions {
		if s.ID == existing {
			found = true
		}
	}
	if !found {
		t.Errorf("snapshot of %d sessions does not contain the one created before subscribing", len(ev.Sessions))
	}
}

// The reason the channel exists: a session created elsewhere shows up without
// anyone polling for it.
func TestEventChannelPushesCreateAndDelete(t *testing.T) {
	ts, wsURL, cookie := eventsServer(t)
	c := dialWS(t, wsURL, cookie)
	defer c.Close()

	if ev := readEvent(t, c, 10*time.Second); ev.Type != "sessions" {
		t.Fatalf("first event type %q, want the snapshot", ev.Type)
	}

	id := createSession(t, ts.URL, `{"name":"pushed","directory":".","shell":"sh"}`, cookie)
	ev := waitForEvent(t, c, "the create event", func(e eventEnvelope) bool {
		return e.Type == "session" && e.Session.ID == id
	})
	if ev.Session.Name != "pushed" {
		t.Errorf("pushed session name %q, want %q", ev.Session.Name, "pushed")
	}

	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
	if err != nil {
		t.Fatalf("build delete: %v", err)
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = resp.Body.Close()

	waitForEvent(t, c, "the delete event", func(e eventEnvelope) bool {
		return e.Type == "sessionGone" && e.SessionID == id
	})
}

// The foreground has to reach the channel, or the dashboard has nothing to
// render but a name. A fresh sh session reports the shell it was started with.
func TestEventChannelPushesTheForeground(t *testing.T) {
	ts, wsURL, cookie := eventsServer(t)
	c := dialWS(t, wsURL, cookie)
	defer c.Close()

	if ev := readEvent(t, c, 10*time.Second); ev.Type != "sessions" {
		t.Fatalf("first event type %q, want the snapshot", ev.Type)
	}
	id := createSession(t, ts.URL, `{"name":"active","directory":".","shell":"sh"}`, cookie)

	ev := waitForEvent(t, c, "a foreground update", func(e eventEnvelope) bool {
		return e.Type == "session" && e.Session.ID == id && e.Session.Command == "sh"
	})
	if ev.Session.Cwd != "." {
		t.Errorf("cwd %q, want %q — the session was created in the root", ev.Session.Cwd, ".")
	}
}

// §5.1 defines no client→server messages. Sending one must be discarded, not
// treated as an error that tears the connection down.
func TestEventChannelIgnoresClientMessages(t *testing.T) {
	ts, wsURL, cookie := eventsServer(t)
	c := dialWS(t, wsURL, cookie)
	defer c.Close()

	if ev := readEvent(t, c, 10*time.Second); ev.Type != "sessions" {
		t.Fatalf("first event type %q, want the snapshot", ev.Type)
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":80,"rows":24}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The connection must still be live and still delivering.
	id := createSession(t, ts.URL, `{"name":"after","directory":".","shell":"sh"}`, cookie)
	waitForEvent(t, c, "an event after an unsolicited client message", func(e eventEnvelope) bool {
		return e.Type == "session" && e.Session.ID == id
	})
}

// Two dashboards open at once both have to be served.
func TestEventChannelServesMultipleSubscribers(t *testing.T) {
	ts, wsURL, cookie := eventsServer(t)
	c1 := dialWS(t, wsURL, cookie)
	defer c1.Close()
	c2 := dialWS(t, wsURL, cookie)
	defer c2.Close()

	for _, c := range []*websocket.Conn{c1, c2} {
		if ev := readEvent(t, c, 10*time.Second); ev.Type != "sessions" {
			t.Fatalf("first event type %q, want the snapshot", ev.Type)
		}
	}

	id := createSession(t, ts.URL, `{"name":"shared","directory":".","shell":"sh"}`, cookie)
	for _, c := range []*websocket.Conn{c1, c2} {
		waitForEvent(t, c, "the create event on both subscribers", func(e eventEnvelope) bool {
			return e.Type == "session" && e.Session.ID == id
		})
	}
}
