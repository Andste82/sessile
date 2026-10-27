package session

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// startDirServer is a minimal in-process SSH server: password auth, an sftp
// subsystem on the local filesystem, and an exec handler that records the
// command and then idles until it reads "QUIT\n".
type startDirServer struct {
	addr, fingerprint string

	mu    sync.Mutex
	execs []string
}

func newStartDirServer(t *testing.T) *startDirServer {
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

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "u" && string(pw) == "p" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)

	s := &startDirServer{addr: ln.Addr().String(), fingerprint: ssh.FingerprintSHA256(signer.PublicKey())}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, cfg)
		}
	}()
	return s
}

func (s *startDirServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			return
		}
		go s.session(ch, requests)
	}
}

func (s *startDirServer) session(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(true, nil)
		case "subsystem":
			_ = req.Reply(true, nil)
			if srv, err := sftp.NewServer(ch); err == nil {
				_ = srv.Serve()
				_ = srv.Close()
			}
			return
		case "exec":
			var p struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &p)
			s.mu.Lock()
			s.execs = append(s.execs, p.Command)
			s.mu.Unlock()
			_ = req.Reply(true, nil)
			go func() {
				buf := make([]byte, 64)
				for {
					n, err := ch.Read(buf)
					if strings.Contains(string(buf[:n]), "QUIT") {
						_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
						_ = ch.Close()
						return
					}
					if err != nil {
						return
					}
				}
			}()
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func (s *startDirServer) lastExec() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.execs) == 0 {
		return ""
	}
	return s.execs[len(s.execs)-1]
}

func (s *startDirServer) target() sshpty.Target {
	return sshpty.Target{
		Address: s.addr, Username: "u", AuthMethod: "password", Password: "p",
		TerminalType: "bash", TrustedHostKeyFingerprint: s.fingerprint,
	}
}

type fixedResolver struct{ target sshpty.Target }

func (r fixedResolver) Resolve(userID, hostID string) (sshpty.Target, string, error) {
	return r.target, "host", nil
}

func TestCreateSSHPersistsStartDirectory(t *testing.T) {
	mgr, store, _ := testManager(t)
	srv := newStartDirServer(t)
	dir := filepath.Join(t.TempDir(), "it's here")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	info, _, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), dir, CreateOptions{})
	if err != nil {
		t.Fatalf("CreateSSH: %v", err)
	}
	if info.Directory != dir {
		t.Errorf("Info.Directory = %q, want %q", info.Directory, dir)
	}
	row, ok, _ := store.Get(info.ID)
	if !ok || row.Directory != dir {
		t.Errorf("stored row = %+v (found %v), want Directory %q", row, ok, dir)
	}
	if got := srv.lastExec(); !strings.Contains(got, "cd --") {
		t.Errorf("exec command %q does not enter the directory", got)
	}
}

func TestCreateSSHWithoutDirectoryIsUnchanged(t *testing.T) {
	mgr, store, _ := testManager(t)
	srv := newStartDirServer(t)

	info, _, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), "", CreateOptions{})
	if err != nil {
		t.Fatalf("CreateSSH: %v", err)
	}
	if row, _, _ := store.Get(info.ID); row.Directory != "" {
		t.Errorf("stored Directory = %q, want empty", row.Directory)
	}
	if got := srv.lastExec(); strings.Contains(got, "cd --") {
		t.Errorf("exec command %q contains a cd with no directory set", got)
	}
}

func TestCreateSSHMissingDirectoryCreatesNothing(t *testing.T) {
	mgr, store, _ := testManager(t)
	srv := newStartDirServer(t)
	missing := filepath.Join(t.TempDir(), "gone")

	_, _, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), missing, CreateOptions{})
	var nf *sshpty.ErrDirNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want *sshpty.ErrDirNotFound", err)
	}
	if len(store.rows) != 0 {
		t.Errorf("store has %d rows after a failed create, want 0", len(store.rows))
	}
	if srv.lastExec() != "" {
		t.Errorf("a shell was started despite the missing directory: %q", srv.lastExec())
	}
}

func TestRestartSSHReusesStoredDirectory(t *testing.T) {
	mgr, _, _ := testManager(t)
	srv := newStartDirServer(t)
	mgr.SetHostResolver(fixedResolver{srv.target()})
	dir := t.TempDir()

	created, _, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), dir, CreateOptions{})
	if err != nil {
		t.Fatalf("CreateSSH: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("QUIT\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	restarted, _, err := mgr.Restart(created.ID, "test-user")
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if restarted.Directory != dir {
		t.Errorf("restarted Directory = %q, want %q", restarted.Directory, dir)
	}
	if got := srv.lastExec(); !strings.Contains(got, "cd --") {
		t.Errorf("restart exec %q does not enter the stored directory", got)
	}
}

func TestRestartSSHMissingDirectoryStaysStopped(t *testing.T) {
	mgr, store, _ := testManager(t)
	srv := newStartDirServer(t)
	mgr.SetHostResolver(fixedResolver{srv.target()})
	dir := filepath.Join(t.TempDir(), "work")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	created, _, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), dir, CreateOptions{})
	if err != nil {
		t.Fatalf("CreateSSH: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("QUIT\n")); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	_, _, err = mgr.Restart(created.ID, "test-user")
	var nf *sshpty.ErrDirNotFound
	if !errors.As(err, &nf) || nf.Path != dir {
		t.Fatalf("Restart err = %v, want *ErrDirNotFound for %q", err, dir)
	}
	// No fallback: the session is still stopped, row intact.
	info, err := mgr.Get(created.ID, "test-user")
	if err != nil || info.Status != StatusStopped {
		t.Errorf("after failed restart: info = %+v, err = %v, want stopped", info, err)
	}
	if row, ok, _ := store.Get(created.ID); !ok || row.Status != StatusStopped || row.Directory != dir {
		t.Errorf("stored row = %+v (found %v), want stopped with Directory %q", row, ok, dir)
	}
}
