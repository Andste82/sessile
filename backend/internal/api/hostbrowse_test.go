package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/Andste82/sessile/backend/internal/auth"
	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// fakeSSHD is a real in-process SSH server with an SFTP subsystem served by
// pkg/sftp against the local filesystem. Unlike the single-shot servers in
// other packages it accepts any number of connections, and counts the ones
// currently open so tests can observe the cache closing them.
type fakeSSHD struct {
	addr        string
	fingerprint string
	root        string // the SFTP working directory, i.e. "home"

	hangSFTP atomic.Bool // accept the subsystem request, never answer it
	open     atomic.Int32
	total    atomic.Int32

	mu    sync.Mutex
	conns []*ssh.ServerConn
}

func newFakeSSHD(t *testing.T) *fakeSSHD {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSSHD{addr: ln.Addr().String(), fingerprint: ssh.FingerprintSHA256(signer.PublicKey()), root: root}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "testuser" && string(pw) == "testpass" {
				return nil, nil
			}
			return nil, errors.New("wrong credentials")
		},
	}
	cfg.AddHostKey(signer)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn, cfg)
		}
	}()
	t.Cleanup(f.killAll)
	return f
}

func (f *fakeSSHD) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	f.total.Add(1)
	f.open.Add(1)
	defer f.open.Add(-1)
	f.mu.Lock()
	f.conns = append(f.conns, sc)
	f.mu.Unlock()
	go ssh.DiscardRequests(reqs)
	go func() {
		for nc := range chans {
			ch, requests, err := nc.Accept()
			if err != nil {
				continue
			}
			go f.session(ch, requests)
		}
	}()
	_ = sc.Wait()
}

func (f *fakeSSHD) session(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		if req.Type == "subsystem" {
			req.Reply(true, nil)
			if f.hangSFTP.Load() {
				io.Copy(io.Discard, ch)
				return
			}
			srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(f.root))
			if err != nil {
				return
			}
			_ = srv.Serve()
			return
		}
		if req.WantReply {
			req.Reply(false, nil)
		}
	}
}

func (f *fakeSSHD) killAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close()
	}
	f.conns = nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

type browseHarness struct {
	t      *testing.T
	srv    *Server
	router *gin.Engine
	sshd   *fakeSSHD
	tokens map[string]string
}

func newBrowseHarness(t *testing.T) *browseHarness {
	t.Helper()
	sshd := newFakeSSHD(t)
	sessions := auth.NewSessionStore(time.Hour)
	srv := NewServer(&config.Config{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
		t.TempDir(), nil, nil, sessions, hosts.NewRegistry(t.TempDir()))
	t.Cleanup(srv.Close)
	dist := fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}
	return &browseHarness{
		t: t, srv: srv, router: srv.Router(dist), sshd: sshd,
		tokens: map[string]string{"alice": sessions.Create("alice"), "bob": sessions.Create("bob")},
	}
}

// addHost stores a host for user pointing at the fake server.
func (h *browseHarness) addHost(user, name string, mutate func(*hosts.Host)) hosts.Host {
	h.t.Helper()
	store, err := h.srv.hosts.For(user)
	if err != nil {
		h.t.Fatal(err)
	}
	host := hosts.Host{
		Name: name, Address: h.sshd.addr, Username: "testuser",
		AuthMethod: hosts.AuthPassword, Password: "testpass",
		TargetOS: hosts.OSLinux, TerminalType: "bash",
		TrustedHostKeyType: "ssh-ed25519", TrustedHostKeyFingerprint: h.sshd.fingerprint,
	}
	if mutate != nil {
		mutate(&host)
	}
	created, err := store.Create(host)
	if err != nil {
		h.t.Fatal(err)
	}
	return created
}

func (h *browseHarness) do(method, target, user, body string) *httptest.ResponseRecorder {
	return h.doCtx(context.Background(), method, target, user, body)
}

func (h *browseHarness) doCtx(ctx context.Context, method, target, user, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(ctx)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.tokens[user]})
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *browseHarness) list(user, hostID, path string) *httptest.ResponseRecorder {
	target := "/api/hosts/" + hostID + "/directories"
	if path != "\x00none" {
		target += "?path=" + url.QueryEscape(path)
	}
	return h.do(http.MethodGet, target, user, "")
}

type dirsJSON struct {
	Path        string   `json:"path"`
	Parent      *string  `json:"parent"`
	Directories []string `json:"directories"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

type errJSON struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestBrowseListsOnlyDirectoriesSorted(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	for _, d := range []string{"b", "A", ".hid", "c"} {
		if err := os.Mkdir(filepath.Join(h.sshd.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(h.sshd.root, "f.txt"), []byte("x"), 0o644)
	_ = os.Symlink(filepath.Join(h.sshd.root, "A"), filepath.Join(h.sshd.root, "dirlink"))
	_ = os.Symlink(filepath.Join(h.sshd.root, "f.txt"), filepath.Join(h.sshd.root, "filelink"))
	_ = os.Symlink(filepath.Join(h.sshd.root, "nope"), filepath.Join(h.sshd.root, "broken"))

	rec := h.list("alice", host.ID, h.sshd.root)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[dirsJSON](t, rec)
	want := []string{".hid", "A", "b", "c", "dirlink"}
	if strings.Join(got.Directories, ",") != strings.Join(want, ",") {
		t.Errorf("directories = %v, want %v", got.Directories, want)
	}
	if got.Path != h.sshd.root {
		t.Errorf("path = %q, want %q", got.Path, h.sshd.root)
	}
	if got.Parent == nil || *got.Parent != filepath.Dir(h.sshd.root) {
		t.Errorf("parent = %v, want %q", got.Parent, filepath.Dir(h.sshd.root))
	}
}

func TestBrowseEmptyPathMeansHomeAndRootHasNullParent(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)

	for _, p := range []string{"", "."} {
		rec := h.list("alice", host.ID, p)
		if rec.Code != 200 {
			t.Fatalf("path %q: status %d: %s", p, rec.Code, rec.Body)
		}
		if got := decode[dirsJSON](t, rec); got.Path != h.sshd.root {
			t.Errorf("path %q resolved to %q, want home %q", p, got.Path, h.sshd.root)
		}
	}
	// No query parameter at all behaves like an empty one.
	if rec := h.list("alice", host.ID, "\x00none"); rec.Code != 200 {
		t.Errorf("absent path: status %d", rec.Code)
	}

	rec := h.list("alice", host.ID, "/")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := decode[dirsJSON](t, rec); got.Path != "/" || got.Parent != nil {
		t.Errorf("root = %+v, want path / and null parent", got)
	}
	if !strings.Contains(rec.Body.String(), `"parent":null`) {
		t.Errorf("body %s should carry an explicit null parent", rec.Body)
	}
}

func TestBrowseRejectsBadRequests(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	win := h.addHost("alice", "win", func(x *hosts.Host) { x.TargetOS = hosts.OSWindows })
	ps := h.addHost("alice", "ps", func(x *hosts.Host) { x.TerminalType = "powershell" })
	file := filepath.Join(h.sshd.root, "plain.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)

	tests := []struct {
		name     string
		hostID   string
		path     string
		wantCode int
		wantMsg  string
	}{
		{"relative", host.ID, "some/dir", 400, "path must be absolute"},
		{"nul", host.ID, "/a\x00b", 400, "NUL"},
		{"too long", host.ID, "/" + strings.Repeat("a", 5000), 400, "too long"},
		{"missing", host.ID, filepath.Join(h.sshd.root, "nope"), 400, "not found on host"},
		{"file", host.ID, file, 400, "not found on host"},
		{"windows target", win.ID, "/", 400, "Windows"},
		{"powershell terminal", ps.ID, "/", 400, "Windows"},
		{"unknown host", "does-not-exist", "/", 404, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.list("alice", tt.hostID, tt.path)
			if rec.Code != tt.wantCode {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}
			e := decode[errJSON](t, rec)
			wantErrCode := CodeValidation
			if tt.wantCode == 404 {
				wantErrCode = CodeNotFound
			}
			if e.Error.Code != wantErrCode || !strings.Contains(e.Error.Message, tt.wantMsg) {
				t.Errorf("error = %+v, want code %s containing %q", e.Error, wantErrCode, tt.wantMsg)
			}
		})
	}
	// Only the missing/file cases need the host; they share one connection.
	// Input-validation and Windows rejections never dial.
	if h.sshd.total.Load() != 1 {
		t.Errorf("rejected requests dialed %d connections, want 1", h.sshd.total.Load())
	}
}

func TestBrowseMissingPathMessageNamesThePath(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	p := filepath.Join(h.sshd.root, "gone")
	e := decode[errJSON](t, h.list("alice", host.ID, p))
	if want := `directory "` + p + `" not found on host`; e.Error.Message != want {
		t.Errorf("message = %q, want %q", e.Error.Message, want)
	}
	// A "not found" is the caller's mistake, not a dead connection: the
	// cached connection must survive it.
	if h.srv.browse.len() != 1 {
		t.Errorf("cache len = %d after not-found, want connection kept", h.srv.browse.len())
	}
}

func TestBrowseRequiresAuthAndIsUserScoped(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)

	if rec := h.list("", host.ID, "/"); rec.Code != 401 {
		t.Errorf("unauthenticated GET = %d, want 401", rec.Code)
	}
	if rec := h.do(http.MethodDelete, "/api/hosts/"+host.ID+"/directories", "", ""); rec.Code != 401 {
		t.Errorf("unauthenticated DELETE = %d, want 401", rec.Code)
	}

	// Bob asking for Alice's host id is indistinguishable from an unknown id.
	other := h.list("bob", host.ID, "/")
	unknown := h.list("bob", "does-not-exist", "/")
	if other.Code != 404 || unknown.Code != 404 || other.Body.String() != unknown.Body.String() {
		t.Errorf("foreign host = %d %s, unknown = %d %s; want identical 404", other.Code, other.Body, unknown.Code, unknown.Body)
	}
	if rec := h.do(http.MethodDelete, "/api/hosts/"+host.ID+"/directories", "bob", ""); rec.Code != 404 {
		t.Errorf("foreign DELETE = %d, want 404", rec.Code)
	}
	if h.sshd.total.Load() != 0 {
		t.Errorf("foreign access dialed %d connections", h.sshd.total.Load())
	}

	// Alice's cached connection is keyed by (user, host): bob's DELETE must
	// not have touched it.
	if rec := h.list("alice", host.ID, "/"); rec.Code != 200 {
		t.Fatalf("alice GET = %d", rec.Code)
	}
	h.do(http.MethodDelete, "/api/hosts/"+host.ID+"/directories", "bob", "")
	if h.srv.browse.len() != 1 {
		t.Errorf("bob's DELETE dropped alice's connection")
	}
}

func TestBrowseHostKeyIsNeverAcceptedSilently(t *testing.T) {
	h := newBrowseHarness(t)
	unknown := h.addHost("alice", "new", func(x *hosts.Host) { x.TrustedHostKeyFingerprint, x.TrustedHostKeyType = "", "" })
	changed := h.addHost("alice", "changed", func(x *hosts.Host) { x.TrustedHostKeyFingerprint = "SHA256:old" })

	rec := h.list("alice", unknown.ID, "/")
	if rec.Code != 409 {
		t.Fatalf("unknown key: status %d: %s", rec.Code, rec.Body)
	}
	e := decode[struct {
		Error struct {
			Code        string `json:"code"`
			KeyType     string `json:"keyType"`
			Fingerprint string `json:"fingerprint"`
		} `json:"error"`
	}](t, rec)
	if e.Error.Code != "host_key_unverified" || e.Error.Fingerprint != h.sshd.fingerprint || e.Error.KeyType == "" {
		t.Errorf("unknown-key body = %+v", e.Error)
	}

	rec = h.list("alice", changed.ID, "/")
	if rec.Code != 409 {
		t.Fatalf("changed key: status %d: %s", rec.Code, rec.Body)
	}
	c := decode[struct {
		Error struct {
			Code                string `json:"code"`
			PreviousFingerprint string `json:"previousFingerprint"`
		} `json:"error"`
	}](t, rec)
	if c.Error.Code != "host_key_changed" || c.Error.PreviousFingerprint != "SHA256:old" {
		t.Errorf("changed-key body = %+v", c.Error)
	}

	// A failed dial creates no entry and leaves no connection behind.
	if h.srv.browse.len() != 0 {
		t.Errorf("cache len = %d after host-key failures, want 0", h.srv.browse.len())
	}
	if h.sshd.open.Load() != 0 {
		eventually(t, "no connection left open", func() bool { return h.sshd.open.Load() == 0 })
	}

	// Trusting the key (the frontend's HostKeyTrustDialog) unblocks it.
	trust := h.do(http.MethodPost, "/api/hosts/"+unknown.ID+"/host-key/trust", "alice",
		`{"fingerprint":"`+h.sshd.fingerprint+`","keyType":"ssh-ed25519"}`)
	if trust.Code != 200 {
		t.Fatalf("trust: %d %s", trust.Code, trust.Body)
	}
	if rec := h.list("alice", unknown.ID, "/"); rec.Code != 200 {
		t.Errorf("after trust: status %d: %s", rec.Code, rec.Body)
	}
}

func TestBrowseConnectionIsCachedAndDeleteClosesIt(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)

	for i := 0; i < 4; i++ {
		if rec := h.list("alice", host.ID, "/"); rec.Code != 200 {
			t.Fatalf("GET %d = %d", i, rec.Code)
		}
	}
	if got := h.sshd.total.Load(); got != 1 {
		t.Errorf("4 listings dialed %d times, want 1 (no dial per click)", got)
	}

	for i := 0; i < 2; i++ { // idempotent
		if rec := h.do(http.MethodDelete, "/api/hosts/"+host.ID+"/directories", "alice", ""); rec.Code != 204 {
			t.Fatalf("DELETE %d = %d, want 204", i, rec.Code)
		}
	}
	eventually(t, "server side connection closed", func() bool { return h.sshd.open.Load() == 0 })
	if h.srv.browse.len() != 0 {
		t.Errorf("cache len = %d after DELETE", h.srv.browse.len())
	}

	// The next listing simply dials again.
	if rec := h.list("alice", host.ID, "/"); rec.Code != 200 || h.sshd.total.Load() != 2 {
		t.Errorf("re-list = %d, dials = %d, want 200 and 2", rec.Code, h.sshd.total.Load())
	}
}

func TestBrowseIdleTimeoutClosesConnection(t *testing.T) {
	h := newBrowseHarness(t)
	h.srv.browse.idle = 150 * time.Millisecond
	host := h.addHost("alice", "box", nil)

	if rec := h.list("alice", host.ID, "/"); rec.Code != 200 {
		t.Fatalf("GET = %d", rec.Code)
	}
	// Using it again resets the clock: still alive after > idle in total.
	time.Sleep(100 * time.Millisecond)
	if rec := h.list("alice", host.ID, "/"); rec.Code != 200 {
		t.Fatalf("GET = %d", rec.Code)
	}
	time.Sleep(100 * time.Millisecond)
	if h.srv.browse.len() != 1 || h.sshd.total.Load() != 1 {
		t.Fatalf("connection expired %v early despite use (len %d, dials %d)", "", h.srv.browse.len(), h.sshd.total.Load())
	}

	eventually(t, "cache entry expired", func() bool { return h.srv.browse.len() == 0 })
	eventually(t, "server side connection closed", func() bool { return h.sshd.open.Load() == 0 })
}

func TestBrowseSingleFlightDial(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	var dials atomic.Int32
	h.srv.browse.dial = func(ctx context.Context, tg sshpty.Target) (*ssh.Client, error) {
		dials.Add(1)
		time.Sleep(150 * time.Millisecond)
		return sshpty.Dial(ctx, tg)
	}

	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = h.list("alice", host.ID, "/").Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 200 {
			t.Errorf("request %d = %d", i, c)
		}
	}
	if dials.Load() != 1 || h.sshd.total.Load() != 1 {
		t.Errorf("dials = %d, server saw %d; want 1 and 1", dials.Load(), h.sshd.total.Load())
	}
}

func TestBrowseAbortCancelsAnUnwantedDial(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	dialCtxDone := make(chan struct{})
	h.srv.browse.dial = func(ctx context.Context, _ sshpty.Target) (*ssh.Client, error) {
		<-ctx.Done()
		close(dialCtxDone)
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.doCtx(ctx, http.MethodGet, "/api/hosts/"+host.ID+"/directories?path=%2F", "alice", "")
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	select {
	case <-dialCtxDone:
	case <-time.After(2 * time.Second):
		t.Fatal("dial was not cancelled after its only waiter left")
	}
	eventually(t, "failed dial leaves no entry", func() bool { return h.srv.browse.len() == 0 })
}

func TestBrowseAbortOfOneWaiterKeepsTheDialForTheOther(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	release := make(chan struct{})
	h.srv.browse.dial = func(ctx context.Context, tg sshpty.Target) (*ssh.Client, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return sshpty.Dial(ctx, tg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	aborted := make(chan struct{})
	go func() {
		defer close(aborted)
		h.doCtx(ctx, http.MethodGet, "/api/hosts/"+host.ID+"/directories?path=%2F", "alice", "")
	}()
	other := make(chan int, 1)
	go func() { other <- h.list("alice", host.ID, "/").Code }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-aborted
	close(release)
	if code := <-other; code != 200 {
		t.Errorf("surviving waiter = %d, want 200", code)
	}
}

func TestBrowsePerUserCapEvictsLeastRecentlyUsed(t *testing.T) {
	h := newBrowseHarness(t)
	a := h.addHost("alice", "a", nil)
	b := h.addHost("alice", "b", nil)
	c := h.addHost("alice", "c", nil)
	bobHost := h.addHost("bob", "bob", nil)

	for _, id := range []string{a.ID, b.ID} {
		if rec := h.list("alice", id, "/"); rec.Code != 200 {
			t.Fatalf("GET = %d", rec.Code)
		}
	}
	if rec := h.list("bob", bobHost.ID, "/"); rec.Code != 200 {
		t.Fatal("bob GET failed")
	}
	// Touch a so that b is the oldest.
	h.list("alice", a.ID, "/")
	if rec := h.list("alice", c.ID, "/"); rec.Code != 200 {
		t.Fatal("GET c failed")
	}

	h.srv.browse.mu.Lock()
	_, hasA := h.srv.browse.entries[browseKey{"alice", a.ID}]
	_, hasB := h.srv.browse.entries[browseKey{"alice", b.ID}]
	_, hasC := h.srv.browse.entries[browseKey{"alice", c.ID}]
	_, hasBob := h.srv.browse.entries[browseKey{"bob", bobHost.ID}]
	h.srv.browse.mu.Unlock()
	if !hasA || hasB || !hasC {
		t.Errorf("alice's entries a=%v b=%v c=%v, want a and c (b evicted)", hasA, hasB, hasC)
	}
	if !hasBob {
		t.Error("alice's cap evicted bob's connection")
	}
	// 3 alive: a, c, bob.
	eventually(t, "evicted connection closed", func() bool { return h.sshd.open.Load() == 3 })
}

func TestBrowseDeadConnectionIsDroppedAndRedialed(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	if rec := h.list("alice", host.ID, "/"); rec.Code != 200 {
		t.Fatal("first GET failed")
	}
	h.sshd.killAll()
	eventually(t, "server dropped connection", func() bool { return h.sshd.open.Load() == 0 })

	// The call that finds the connection dead fails and discards it...
	first := h.list("alice", host.ID, "/")
	if first.Code == 200 {
		t.Fatalf("listing over a dead connection succeeded")
	}
	if h.srv.browse.len() != 0 {
		t.Errorf("dead entry still cached")
	}
	// ...and the retry dials fresh.
	if rec := h.list("alice", host.ID, "/"); rec.Code != 200 {
		t.Errorf("retry = %d: %s", rec.Code, rec.Body)
	}
}

func TestBrowseHostEditAndDeleteDropTheConnection(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)

	h.list("alice", host.ID, "/")
	if h.srv.browse.len() != 1 {
		t.Fatal("no cached entry")
	}
	put := `{"name":"renamed","address":"` + h.sshd.addr + `","username":"testuser","authMethod":"password","targetOS":"linux","terminalType":"bash"}`
	if rec := h.do(http.MethodPut, "/api/hosts/"+host.ID, "alice", put); rec.Code != 200 {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	if h.srv.browse.len() != 0 {
		t.Error("host update did not drop the browse connection")
	}

	h.list("alice", host.ID, "/")
	if h.srv.browse.len() != 1 {
		t.Fatal("no cached entry")
	}
	if rec := h.do(http.MethodDelete, "/api/hosts/"+host.ID, "alice", ""); rec.Code != 204 {
		t.Fatalf("DELETE host = %d", rec.Code)
	}
	if h.srv.browse.len() != 0 {
		t.Error("host delete did not drop the browse connection")
	}
	eventually(t, "connection closed", func() bool { return h.sshd.open.Load() == 0 })
}

func TestBrowseTrustDropsTheConnection(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	h.list("alice", host.ID, "/")
	if h.srv.browse.len() != 1 {
		t.Fatal("no cached entry")
	}
	rec := h.do(http.MethodPost, "/api/hosts/"+host.ID+"/host-key/trust", "alice",
		`{"fingerprint":"`+h.sshd.fingerprint+`","keyType":"ssh-ed25519"}`)
	if rec.Code != 200 {
		t.Fatalf("trust = %d %s", rec.Code, rec.Body)
	}
	if h.srv.browse.len() != 0 {
		t.Error("trusting a host key did not drop the browse connection")
	}
}

func TestBrowseServerCloseReleasesEverything(t *testing.T) {
	h := newBrowseHarness(t)
	a := h.addHost("alice", "a", nil)
	b := h.addHost("alice", "b", nil)
	h.list("alice", a.ID, "/")
	h.list("alice", b.ID, "/")
	if h.sshd.open.Load() != 2 {
		t.Fatalf("open = %d, want 2", h.sshd.open.Load())
	}

	h.srv.Close()
	eventually(t, "all connections closed", func() bool { return h.sshd.open.Load() == 0 })
	if h.srv.browse.len() != 0 {
		t.Error("entries remain after Close")
	}
	if rec := h.list("alice", a.ID, "/"); rec.Code == 200 {
		t.Error("a closed server still served a listing")
	}
	h.srv.Close() // idempotent
}

func TestBrowseWrongCredentialsUseSessionCreateMapping(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", func(x *hosts.Host) { x.Password = "wrong" })
	rec := h.list("alice", host.ID, "/")
	if rec.Code != 400 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if e := decode[errJSON](t, rec); e.Error.Code != CodeValidation || e.Error.Message == "" {
		t.Errorf("error = %+v", e.Error)
	}
	if h.srv.browse.len() != 0 {
		t.Error("failed dial left an entry")
	}
}

func TestBrowseHungSFTPIsCancelledAndClosesTheConnection(t *testing.T) {
	h := newBrowseHarness(t)
	h.sshd.hangSFTP.Store(true)
	host := h.addHost("alice", "box", nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.doCtx(ctx, http.MethodGet, "/api/hosts/"+host.ID+"/directories?path=%2F", "alice", "")
	}()
	eventually(t, "ssh connection established", func() bool { return h.sshd.open.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	eventually(t, "hung dial leaves no entry", func() bool { return h.srv.browse.len() == 0 })
	eventually(t, "ssh connection closed", func() bool { return h.sshd.open.Load() == 0 })
}

func TestBrowseIsDroppedReportsEviction(t *testing.T) {
	h := newBrowseHarness(t)
	host := h.addHost("alice", "box", nil)
	key := browseKey{"alice", host.ID}
	e, err := h.srv.browse.acquire(context.Background(), key, host.SSHTarget())
	if err != nil {
		t.Fatal(err)
	}
	defer h.srv.browse.release(e)
	if h.srv.browse.isDropped(e) {
		t.Fatal("fresh entry reported dropped")
	}
	h.srv.browse.drop("alice", host.ID)
	if !h.srv.browse.isDropped(e) {
		t.Fatal("evicted entry not reported dropped")
	}
}
