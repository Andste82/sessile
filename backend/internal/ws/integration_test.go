package ws_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
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

// TestSessionLifecycleAndReplay exercises the core flow end-to-end against a
// real PTY: create → attach → send input → observe output → detach → re-attach
// → confirm the ring-buffer replay contains the earlier output.
func TestSessionLifecycleAndReplay(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	root := t.TempDir()
	cfg := &config.Config{
		Shells:     []string{"sh"},
		BufferSize: 512 << 10,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(root, cfg.Shells, cfg.BufferSize, t.TempDir(), nil, log)
	wsHandler := ws.NewHandler(mgr, cfg, log)
	srv := api.NewServer(cfg, mgr, wsHandler, log, root, nil)

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
// both receive its output, and that clientCount reflects attachments.
func TestMultiClientMirroring(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	cfg := &config.Config{Shells: []string{"sh"}, BufferSize: 512 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(root, cfg.Shells, cfg.BufferSize, t.TempDir(), nil, log)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log, root, nil)
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
	cfg := &config.Config{Shells: []string{"sh"}, BufferSize: 64 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(root, cfg.Shells, cfg.BufferSize, t.TempDir(), nil, log)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log, root, nil)
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

// TestExitFrameDeliveredAndConnectionKept is the end-to-end shape of a user
// typing `exit`: the attached client gets the {"type":"exit"} control frame and
// keeps its connection (§5). The connection is what a restart — from this
// browser or any other one on the session — sends the next `attached` frame
// down; closing it here is what used to leave every other client stuck on a
// restart dialog for a session that was already running again.
func TestExitFrameDeliveredAndConnectionKept(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	cfg := &config.Config{Shells: []string{"sh"}, BufferSize: 64 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(root, cfg.Shells, cfg.BufferSize, t.TempDir(), nil, log)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log, root, nil)
	ts := httptest.NewServer(srv.Router(nil))
	defer ts.Close()

	id := createSession(t, ts.URL, `{"name":"t","directory":".","shell":"sh"}`)
	c := dialWS(t, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws/sessions/"+id)
	defer c.Close()
	assertAttached(t, c, id)
	writeInput(t, c, "exit\n")

	readControl(t, c, "exit", 5*time.Second)

	// Typing at the dead terminal goes nowhere, but it must not be what ends
	// the connection: the readPump has to survive an input write that fails
	// because the shell is gone.
	writeInput(t, c, "ignored\n")

	// Nothing more is expected on the socket, but it has to still be open: a
	// close would arrive here as a CloseError rather than a read timeout.
	_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if _, _, err := c.ReadMessage(); err != nil {
		var ce *websocket.CloseError
		if errors.As(err, &ce) {
			t.Fatalf("server closed the connection on exit with code %d", ce.Code)
		}
		if !isTimeout(err) {
			t.Fatalf("read: %v", err)
		}
	}
}

// TestRestartReattachesEveryClient is issue #42: two browsers on one session,
// one of them restarts it, and both are told — over the connections they were
// already holding — that it is live again.
func TestRestartReattachesEveryClient(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	cfg := &config.Config{Shells: []string{"sh"}, BufferSize: 64 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(root, cfg.Shells, cfg.BufferSize, t.TempDir(), nil, log)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log, root, nil)
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

	writeInput(t, c1, "exit\n")
	readControl(t, c1, "exit", 5*time.Second)
	readControl(t, c2, "exit", 5*time.Second)

	// The restart one browser performs, over REST, while both still hold their
	// sockets. Only c1 knows it happened; c2 has to be told.
	info, err := mgr.Restart(id)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if info.ClientCount != 2 {
		t.Errorf("restarted session has %d clients, want the 2 that were attached", info.ClientCount)
	}

	readControl(t, c1, "attached", 5*time.Second)
	readControl(t, c2, "attached", 5*time.Second)

	// And the connection that was never re-dialled still drives the new shell.
	writeInput(t, c2, "echo after-restart\n")
	if !readUntil(t, c1, "after-restart", 5*time.Second) {
		t.Error("c1 does not see output from the restarted shell")
	}
}

// readControl drains the connection until a control frame of the given type
// arrives, failing the test on close or timeout.
func readControl(t *testing.T, c *websocket.Conn, want string, timeout time.Duration) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %q control frame: %v", want, err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var msg struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &msg); err == nil && msg.Type == want {
			return
		}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// TestRestartStoppedSessionReplaysScrollback is the end-to-end proof of the
// restore path: a session that has stopped comes back under the same id, the
// client that reattaches sees the output from before the stop, and the new shell
// works. This is what a user experiences after a backend restart.
func TestRestartStoppedSessionReplaysScrollback(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	root := t.TempDir()
	cfg := &config.Config{Shells: []string{"sh"}, BufferSize: 64 << 10}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := session.NewManager(root, cfg.Shells, cfg.BufferSize, t.TempDir(), nil, log)
	srv := api.NewServer(cfg, mgr, ws.NewHandler(mgr, cfg, log), log, root, nil)
	ts := httptest.NewServer(srv.Router(nil))
	defer ts.Close()

	id := createSession(t, ts.URL, `{"name":"restore","directory":".","shell":"sh"}`)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/sessions/" + id

	c1 := dialWS(t, wsURL)
	assertAttached(t, c1, id)
	writeInput(t, c1, "echo before-the-stop\n")
	if !readUntil(t, c1, "before-the-stop", 5*time.Second) {
		t.Fatal("did not observe output before stopping")
	}
	writeInput(t, c1, "exit\n")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := mgr.Get(id); err == nil && info.Status == session.StatusStopped {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = c1.Close()
	if info, _ := mgr.Get(id); info.Status != session.StatusStopped {
		t.Fatalf("session did not stop; status=%s", info.Status)
	}

	// Restarting an unknown session is a 404, a running one a 409.
	if code := restartStatus(t, ts.URL, "11111111-2222-3333-4444-555555555555"); code != http.StatusNotFound {
		t.Errorf("restart of unknown session = %d, want 404", code)
	}
	if code := restartStatus(t, ts.URL, id); code != http.StatusOK {
		t.Fatalf("restart = %d, want 200", code)
	}
	if code := restartStatus(t, ts.URL, id); code != http.StatusConflict {
		t.Errorf("restart of a running session = %d, want 409", code)
	}

	// Reattaching now replays the old output, then the separator, and the new
	// shell takes commands.
	c2 := dialWS(t, wsURL)
	defer c2.Close()
	assertAttached(t, c2, id)
	// One read: the replay arrives as a single frame, so the separator and the
	// restored output have to be asserted against the same accumulated bytes.
	replay := readCollect(t, c2, "── restored ", 5*time.Second)
	if !strings.Contains(replay, "before-the-stop") {
		t.Fatalf("replay after restart lost the output from before the stop: %q", replay)
	}
	if !strings.Contains(replay, "── restored ") {
		t.Fatalf("replay after restart has no restore separator: %q", replay)
	}
	if strings.Index(replay, "── restored ") < strings.Index(replay, "before-the-stop") {
		t.Error("separator precedes the restored output; want old output first")
	}
	writeInput(t, c2, "echo after-the-restart\n")
	if !readUntil(t, c2, "after-the-restart", 5*time.Second) {
		t.Fatal("restarted shell does not accept input")
	}
}

func restartStatus(t *testing.T, baseURL, id string) int {
	t.Helper()
	resp, err := http.Post(baseURL+"/api/sessions/"+id+"/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("restart session: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
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
	return strings.Contains(readCollect(t, c, substr, timeout), substr)
}

// readCollect is readUntil that hands back everything it read. Callers asserting
// on more than one substring need this: the bytes a plain readUntil consumes
// past its match are gone, and an attach replay arrives as a single frame.
func readCollect(t *testing.T, c *websocket.Conn, substr string, timeout time.Duration) string {
	t.Helper()
	var acc bytes.Buffer
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			return acc.String()
		}
		if mt == websocket.BinaryMessage {
			acc.Write(data)
			if strings.Contains(acc.String(), substr) {
				return acc.String()
			}
		}
	}
}
