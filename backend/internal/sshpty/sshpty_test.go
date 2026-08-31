package sshpty

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// testServer is a minimal, real SSH server (not a mock of this package) used
// to exercise Start/Read/Write/Resize/Signal/Wait/CloseFile against the
// actual wire protocol. It accepts one connection, one "session" channel,
// and understands just enough of pty-req/exec/window-change/signal to prove
// the client side wires up correctly.
type testServer struct {
	addr        string
	hostKey     ssh.Signer
	fingerprint string

	signals chan ssh.Signal
	resizes chan [2]int // rows, cols
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("wrap host key: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	ts := &testServer{
		addr:        ln.Addr().String(),
		hostKey:     signer,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		signals:     make(chan ssh.Signal, 4),
		resizes:     make(chan [2]int, 4),
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if c.User() == "testuser" && string(password) == "testpass" {
				return nil, nil
			}
			return nil, errors.New("wrong credentials")
		},
	}
	cfg.AddHostKey(signer)

	go ts.acceptOne(t, ln, cfg)
	return ts
}

func (ts *testServer) acceptOne(t *testing.T, ln net.Listener, cfg *ssh.ServerConfig) {
	conn, err := ln.Accept()
	if err != nil {
		return // listener closed on test cleanup
	}
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return // auth failure test cases close before a session is used
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session supported")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			return
		}
		go ts.serveSession(ch, requests)
	}
}

// serveSession handles exactly one session channel: accepts pty-req and
// window-change unconditionally, echoes whatever is written to it back
// verbatim (so a test can assert round-tripped bytes) until it reads
// "QUIT\n", then sends a clean exit status and closes.
func (ts *testServer) serveSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	started := make(chan struct{})
	var startOnce bool

	for req := range requests {
		switch req.Type {
		case "pty-req":
			req.Reply(true, nil)
		case "window-change":
			// No reply wanted per protocol; just record it for assertions.
			var payload struct{ Cols, Rows, Width, Height uint32 }
			_ = ssh.Unmarshal(req.Payload, &payload)
			select {
			case ts.resizes <- [2]int{int(payload.Rows), int(payload.Cols)}:
			default:
			}
		case "signal":
			var payload struct{ Signal string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			select {
			case ts.signals <- ssh.Signal(payload.Signal):
			default:
			}
		case "exec", "shell":
			req.Reply(true, nil)
			if !startOnce {
				startOnce = true
				close(started)
				go ts.echoUntilQuit(ch)
			}
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func (ts *testServer) echoUntilQuit(ch ssh.Channel) {
	buf := make([]byte, 4096)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			data := buf[:n]
			if string(data) == "QUIT\n" {
				_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
				// ssh.Session.Wait() blocks until the channel's request
				// stream closes, not merely on exit-status arriving — so
				// the server must close its end for the client to unblock.
				_ = ch.Close()
				return
			}
			_, _ = ch.Write(data)
		}
		if err != nil {
			return
		}
	}
}

func testTarget(addr string, fingerprint string) Target {
	return Target{
		Address:                   addr,
		Username:                  "testuser",
		AuthMethod:                "password",
		Password:                  "testpass",
		TerminalType:              "custom",
		CustomCommand:             "irrelevant-to-the-fake-server",
		TrustedHostKeyFingerprint: fingerprint,
	}
}

func TestStartRoundTripsData(t *testing.T) {
	ts := newTestServer(t)

	pty, err := Start(testTarget(ts.addr, ts.fingerprint), 24, 80)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pty.CloseFile()

	if err := pty.Write([]byte("hello ssh\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	buf := make([]byte, 64)
	deadline := time.Now().Add(3 * time.Second)
	var got []byte
	for time.Now().Before(deadline) && len(got) < len("hello ssh\n") {
		_ = pty.stdoutR.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, err := pty.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil && n == 0 {
			continue
		}
	}
	if string(got) != "hello ssh\n" {
		t.Fatalf("round-tripped data = %q, want %q", got, "hello ssh\n")
	}
}

func TestStartUnknownHostKey(t *testing.T) {
	ts := newTestServer(t)

	_, err := Start(testTarget(ts.addr, ""), 24, 80)
	var unknown *ErrHostKeyUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("Start with no pinned key: err = %v, want *ErrHostKeyUnknown", err)
	}
	if unknown.Fingerprint != ts.fingerprint {
		t.Errorf("ErrHostKeyUnknown.Fingerprint = %q, want %q", unknown.Fingerprint, ts.fingerprint)
	}
}

func TestStartChangedHostKey(t *testing.T) {
	ts := newTestServer(t)

	_, err := Start(testTarget(ts.addr, "SHA256:not-the-real-fingerprint"), 24, 80)
	var changed *ErrHostKeyChanged
	if !errors.As(err, &changed) {
		t.Fatalf("Start with wrong pinned key: err = %v, want *ErrHostKeyChanged", err)
	}
	if changed.Fingerprint != ts.fingerprint {
		t.Errorf("ErrHostKeyChanged.Fingerprint = %q, want %q", changed.Fingerprint, ts.fingerprint)
	}
	if changed.Previous != "SHA256:not-the-real-fingerprint" {
		t.Errorf("ErrHostKeyChanged.Previous = %q, want the pinned value", changed.Previous)
	}
}

func TestStartWrongCredentials(t *testing.T) {
	ts := newTestServer(t)

	target := testTarget(ts.addr, ts.fingerprint)
	target.Password = "wrong"
	if _, err := Start(target, 24, 80); err == nil {
		t.Fatal("Start with wrong password succeeded, want an error")
	}
}

func TestProbeHostKey(t *testing.T) {
	ts := newTestServer(t)

	keyType, fingerprint, err := ProbeHostKey(ts.addr)
	if err != nil {
		t.Fatalf("ProbeHostKey: %v", err)
	}
	if fingerprint != ts.fingerprint {
		t.Errorf("fingerprint = %q, want %q", fingerprint, ts.fingerprint)
	}
	if keyType == "" {
		t.Error("keyType is empty")
	}
}

func TestResizeSendsWindowChange(t *testing.T) {
	ts := newTestServer(t)

	pty, err := Start(testTarget(ts.addr, ts.fingerprint), 24, 80)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pty.CloseFile()

	if err := pty.Resize(30, 100); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	select {
	case rc := <-ts.resizes:
		if rc[0] != 30 || rc[1] != 100 {
			t.Errorf("server observed resize %v, want [30 100]", rc)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never observed the window-change request")
	}
}

func TestSignalForwardsToServer(t *testing.T) {
	ts := newTestServer(t)

	pty, err := Start(testTarget(ts.addr, ts.fingerprint), 24, 80)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pty.CloseFile()

	pty.Signal(syscall.SIGTERM)

	select {
	case sig := <-ts.signals:
		if sig != ssh.SIGTERM {
			t.Errorf("server observed signal %q, want TERM", sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never observed the signal request")
	}
}

func TestWaitUnblocksOnRemoteExit(t *testing.T) {
	ts := newTestServer(t)

	pty, err := Start(testTarget(ts.addr, ts.fingerprint), 24, 80)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer pty.CloseFile()

	done := make(chan struct{})
	go func() {
		pty.Wait()
		close(done)
	}()

	if err := pty.Write([]byte("QUIT\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not unblock after the remote command exited")
	}

	// Read must now observe EOF (stdoutW closed by the cleanup goroutine).
	buf := make([]byte, 16)
	_ = pty.stdoutR.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = pty.Read(buf)
	if err == nil {
		t.Error("Read after remote exit succeeded, want EOF")
	}
}
