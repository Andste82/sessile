package ws_test

import (
	"bytes"
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

// TestSessionLifecycleAndReplay exercises the M1 acceptance criteria end-to-end
// against a real PTY: create → attach → send input → observe output → detach →
// re-attach → confirm the ring-buffer replay contains the earlier output.
func TestSessionLifecycleAndReplay(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	root := t.TempDir()
	cfg := &config.Config{
		Root:       root,
		Shells:     []string{"sh"},
		BufferSize: 512 << 10,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(cfg.Root, cfg.Shells, cfg.BufferSize, nil, log)
	wsHandler := ws.NewHandler(mgr, cfg, log)
	srv := api.NewServer(cfg, mgr, wsHandler, log)

	ts := httptest.NewServer(srv.Router(nil))
	defer ts.Close()

	// Create a session in the root directory using the deterministic shell.
	id := createSession(t, ts.URL, `{"name":"test","directory":".","shell":"sh"}`)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/sessions/" + id

	// First client: attach, send a command, observe its output.
	c1 := dialWS(t, wsURL)
	assertAttached(t, c1, id)
	writeInput(t, c1, "echo hello-m1\n")
	if !readUntil(t, c1, "hello-m1", 5*time.Second) {
		t.Fatal("did not observe command output on first client")
	}
	_ = c1.Close()

	// Give the PTY a moment to flush into the ring buffer.
	time.Sleep(100 * time.Millisecond)

	// Second client: attach fresh, the replay must contain the earlier output.
	c2 := dialWS(t, wsURL)
	assertAttached(t, c2, id)
	if !readUntil(t, c2, "hello-m1", 5*time.Second) {
		t.Fatal("replay on second client did not contain earlier output")
	}
	_ = c2.Close()

	// Delete the session.
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/sessions/"+id, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestMultiClientMirroring verifies two clients attached to the same session
// both receive its output, and that clientCount reflects attachments (§12 M4).
func TestMultiClientMirroring(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	cfg := &config.Config{Root: root, Shells: []string{"sh"}, BufferSize: 512 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(cfg.Root, cfg.Shells, cfg.BufferSize, nil, log)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log)
	ts := httptest.NewServer(srv.Router(nil))
	defer ts.Close()

	id := createSession(t, ts.URL, `{"name":"t","directory":".","shell":"sh"}`)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/sessions/" + id

	c1 := dialWS(t, wsURL)
	defer c1.Close()
	assertAttached(t, c1, id)
	c2 := dialWS(t, wsURL)
	defer c2.Close()
	assertAttached(t, c2, id)

	// Both clients attached: clientCount should report 2.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sessionClientCount(t, ts.URL, id) == 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := sessionClientCount(t, ts.URL, id); n != 2 {
		t.Fatalf("clientCount = %d, want 2", n)
	}

	// Input on c1 is mirrored to both clients.
	writeInput(t, c1, "echo mirrored-out\n")
	if !readUntil(t, c1, "mirrored-out", 5*time.Second) {
		t.Fatal("c1 did not see output")
	}
	if !readUntil(t, c2, "mirrored-out", 5*time.Second) {
		t.Fatal("c2 did not mirror output")
	}
}

// TestAttachStoppedSessionRejected verifies that once a session's shell has
// exited, a new attach is closed with code 4404 (§5) — the signal the frontend
// uses to stop reconnecting after a backend restart.
func TestAttachStoppedSessionRejected(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	cfg := &config.Config{Root: root, Shells: []string{"sh"}, BufferSize: 64 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(cfg.Root, cfg.Shells, cfg.BufferSize, nil, log)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log)
	ts := httptest.NewServer(srv.Router(nil))
	defer ts.Close()

	id := createSession(t, ts.URL, `{"name":"t","directory":".","shell":"sh"}`)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/sessions/" + id

	// End the shell from the first client, then wait for status=stopped.
	c1 := dialWS(t, wsURL)
	assertAttached(t, c1, id)
	writeInput(t, c1, "exit\n")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := mgr.Get(id)
		if err == nil && info.Status == session.StatusStopped {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = c1.Close()
	if info, _ := mgr.Get(id); info.Status != session.StatusStopped {
		t.Fatalf("session did not stop; status=%s", info.Status)
	}

	// A fresh attach must be rejected with close code 4404.
	c2 := dialWS(t, wsURL)
	defer c2.Close()
	_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err := c2.ReadMessage()
	ce, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected CloseError, got %v", err)
	}
	if ce.Code != 4404 {
		t.Fatalf("close code = %d, want 4404", ce.Code)
	}
}

func sessionClientCount(t *testing.T, baseURL, id string) int {
	t.Helper()
	resp, err := http.Get(baseURL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ClientCount int `json:"clientCount"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.ClientCount
}

func createSession(t *testing.T, baseURL, body string) string {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status = %d, body=%s", resp.StatusCode, b)
	}
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if out.ID == "" || out.Status != "running" {
		t.Fatalf("unexpected create response: %+v", out)
	}
	return out.ID
}

func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	return c
}

func assertAttached(t *testing.T, c *websocket.Conn, id string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read attached: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("first frame type = %d, want text", mt)
	}
	var msg struct {
		Type        string `json:"type"`
		SessionID   string `json:"sessionId"`
		ReplayBytes int    `json:"replayBytes"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal attached: %v", err)
	}
	if msg.Type != "attached" || msg.SessionID != id {
		t.Fatalf("attached frame = %+v, want type=attached sessionId=%s", msg, id)
	}
}

func writeInput(t *testing.T, c *websocket.Conn, s string) {
	t.Helper()
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(s)); err != nil {
		t.Fatalf("write input: %v", err)
	}
}

// readUntil reads binary frames until substr appears or the deadline passes.
// A single read deadline is used because a gorilla read timeout is permanent.
func readUntil(t *testing.T, c *websocket.Conn, substr string, timeout time.Duration) bool {
	t.Helper()
	var acc bytes.Buffer
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return false
		}
		if mt == websocket.BinaryMessage {
			acc.Write(data)
			if strings.Contains(acc.String(), substr) {
				return true
			}
		}
	}
}
