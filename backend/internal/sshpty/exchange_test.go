package sshpty

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// exchangeTestServer is a real SSH server (not a mock of this package) that
// accepts exactly the "exec" request ExchangeKeys sends, reads whatever is
// piped on stdin to completion, and records it — enough to prove the exact
// bytes ExchangeKeys sends are the marshaled public key, without needing a
// real remote shell.
type exchangeTestServer struct {
	addr        string
	fingerprint string

	received chan []byte
}

func newExchangeTestServer(t *testing.T, username, password string) *exchangeTestServer {
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

	es := &exchangeTestServer{
		addr:        ln.Addr().String(),
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		received:    make(chan []byte, 1),
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == username && string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("wrong credentials")
		},
	}
	cfg.AddHostKey(signer)

	go es.acceptOne(ln, cfg)
	return es
}

func (es *exchangeTestServer) acceptOne(ln net.Listener, cfg *ssh.ServerConfig) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
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
		go es.serveSession(ch, requests)
	}
}

func (es *exchangeTestServer) serveSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		if req.Type != "exec" {
			if req.WantReply {
				req.Reply(false, nil)
			}
			continue
		}
		req.Reply(true, nil)
		data, _ := io.ReadAll(ch)
		select {
		case es.received <- data:
		default:
		}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
		return
	}
}

func exchangeTestTarget(addr, fingerprint string) Target {
	return Target{Address: addr, TrustedHostKeyFingerprint: fingerprint}
}

func TestExchangeKeysAppendsThePublicKey(t *testing.T) {
	es := newExchangeTestServer(t, "deploy", "correct-password")

	privPEM, pubLine, keyType, fingerprint, comment, err := ExchangeKeys(
		exchangeTestTarget(es.addr, es.fingerprint), "deploy", "correct-password",
	)
	if err != nil {
		t.Fatalf("ExchangeKeys: %v", err)
	}
	if !strings.HasPrefix(privPEM, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Errorf("private key PEM does not look like OpenSSH PEM: %q", privPEM[:min(40, len(privPEM))])
	}
	if !strings.HasPrefix(pubLine, "ssh-ed25519 ") {
		t.Errorf("public key line = %q, want ssh-ed25519 prefix", pubLine)
	}
	if keyType != "ssh-ed25519" {
		t.Errorf("keyType = %q, want ssh-ed25519", keyType)
	}
	if !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q, want a SHA256: fingerprint", fingerprint)
	}
	if comment == "" {
		t.Error("comment is empty")
	}

	select {
	case got := <-es.received:
		if strings.TrimSpace(string(got)) != pubLine {
			t.Errorf("server received %q, want the returned public key line %q", got, pubLine)
		}
	default:
		t.Fatal("server never received the piped public key")
	}
}

func TestExchangeKeysRejectsUnknownHostKey(t *testing.T) {
	es := newExchangeTestServer(t, "deploy", "correct-password")

	_, _, _, _, _, err := ExchangeKeys(exchangeTestTarget(es.addr, ""), "deploy", "correct-password")
	var unknown *ErrHostKeyUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("ExchangeKeys with no pinned key: err = %v, want *ErrHostKeyUnknown", err)
	}
}

func TestExchangeKeysRejectsWrongPassword(t *testing.T) {
	es := newExchangeTestServer(t, "deploy", "correct-password")

	_, _, _, _, _, err := ExchangeKeys(exchangeTestTarget(es.addr, es.fingerprint), "deploy", "wrong")
	if err == nil {
		t.Fatal("ExchangeKeys with wrong password succeeded, want an error")
	}
	var unknown *ErrHostKeyUnknown
	if errors.As(err, &unknown) {
		t.Fatal("wrong password surfaced as a host-key error, want an auth error")
	}
}

func TestExchangeKeysNeverEchoesThePassword(t *testing.T) {
	es := newExchangeTestServer(t, "deploy", "super-secret-password")

	_, _, _, _, _, err := ExchangeKeys(exchangeTestTarget(es.addr, es.fingerprint), "deploy", "super-secret-password")
	if err != nil {
		t.Fatalf("ExchangeKeys: %v", err)
	}

	got := <-es.received
	if bytes.Contains(got, []byte("super-secret-password")) {
		t.Error("the password leaked into the data sent to the remote host")
	}
}

// remoteAppendKeyCommand only ever gets exercised for real against a live
// sshd in manual E2E testing — the fake exchangeTestServer above just
// records whatever bytes it received, it doesn't run the command. Running
// the exact command string through a local sh here catches the class of
// bug a fake server can't: an earlier version read stdin twice (once via
// $(cat) inside the grep check, again for the append), so the append
// always saw an already-drained stdin and authorized_keys was silently
// never written to.
func runRemoteAppendKeyCommand(t *testing.T, home, stdin string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", remoteAppendKeyCommand)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run remoteAppendKeyCommand: %v (%s)", err, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	return string(data)
}

func TestRemoteAppendKeyCommandWritesTheKey(t *testing.T) {
	home := t.TempDir()
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIQtestkeymaterialtesttest test-comment\n"

	got := runRemoteAppendKeyCommand(t, home, key)
	if got != key {
		t.Fatalf("authorized_keys = %q, want %q", got, key)
	}
}

func TestRemoteAppendKeyCommandIsIdempotent(t *testing.T) {
	home := t.TempDir()
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIQtestkeymaterialtesttest test-comment\n"

	runRemoteAppendKeyCommand(t, home, key)
	got := runRemoteAppendKeyCommand(t, home, key)
	if got != key {
		t.Fatalf("authorized_keys after a second identical run = %q, want unchanged %q", got, key)
	}
}

func TestRemoteAppendKeyCommandPreservesExistingKeys(t *testing.T) {
	home := t.TempDir()
	existing := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIQexistingkeyalreadythere existing\n"
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte(existing), 0o600); err != nil {
		t.Fatalf("seed authorized_keys: %v", err)
	}

	newKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIQbrandnewkeymaterialtest test-comment\n"
	got := runRemoteAppendKeyCommand(t, home, newKey)
	want := existing + newKey
	if got != want {
		t.Fatalf("authorized_keys = %q, want %q", got, want)
	}
}
