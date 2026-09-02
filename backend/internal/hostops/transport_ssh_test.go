package hostops

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// hostopsTestServer is a real SSH server (not a mock of this package),
// modeled on internal/sshpty's own test servers — it accepts one
// connection and understands exactly the two request types sshTransport
// sends: "exec" (run a real shell command and pipe stdout/stderr/exit-status
// back, exercising the actual wire protocol) and the "sftp" subsystem
// (served by github.com/pkg/sftp's own server half against a temp
// directory, so FileTransport's SSH implementation is tested against a
// real SFTP server, not a fake of one).
type hostopsTestServer struct {
	addr        string
	fingerprint string
	root        string
}

func newHostopsTestServer(t *testing.T) *hostopsTestServer {
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

	hs := &hostopsTestServer{
		addr:        ln.Addr().String(),
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
		root:        t.TempDir(),
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

	go hs.acceptOne(ln, cfg)
	return hs
}

func (hs *hostopsTestServer) acceptOne(ln net.Listener, cfg *ssh.ServerConfig) {
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
		go hs.serveSession(ch, requests)
	}
}

func (hs *hostopsTestServer) serveSession(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			req.Reply(true, nil)
			hs.runExec(ch, payload.Command)
			return
		case "subsystem":
			var payload struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			if payload.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			hs.runSFTP(ch)
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// runExec runs command for real (via sh -c) so the client's Exec is tested
// against a genuine process, not a scripted response.
func (hs *hostopsTestServer) runExec(ch ssh.Channel, command string) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	err := cmd.Run()

	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{uint32(exitCode)}))
}

func (hs *hostopsTestServer) runSFTP(ch ssh.Channel) {
	server, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(hs.root))
	if err != nil {
		return
	}
	_ = server.Serve()
	_ = server.Close()
}

// newTestSSHClient dials hs directly. Host-key trust-on-first-use (§4.5.1)
// is internal/sshpty's job, already exercised by its own tests — by the
// time a *ssh.Client reaches hostops (via Manager.CreateSSH / PTY.Client),
// that verification has already happened, so this package's tests only
// need *some* connected client to a server they control, not a second
// implementation of TOFU.
func newTestSSHClient(t *testing.T, hs *hostopsTestServer) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", hs.addr, &ssh.ClientConfig{
		User:            "testuser",
		Auth:            []ssh.AuthMethod{ssh.Password("testpass")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // test-only dial to our own in-process fake server; see comment above
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSSHExecCapturesStdoutStderrAndExitCode(t *testing.T) {
	hs := newHostopsTestServer(t)
	tr := NewSSH(newTestSSHClient(t, hs))

	res, err := tr.Exec(context.Background(), "echo out; echo err >&2")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if string(res.Stdout) != "out\n" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "out\n")
	}
	if string(res.Stderr) != "err\n" {
		t.Errorf("stderr = %q, want %q", res.Stderr, "err\n")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}

	res, err = tr.Exec(context.Background(), "exit 7")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}
}

func TestSSHFilesRoundTrip(t *testing.T) {
	hs := newHostopsTestServer(t)
	ctx := context.Background()
	files := NewSSH(newTestSSHClient(t, hs)).Files()

	if err := files.Write(ctx, "a.txt", []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := files.List(ctx, ".")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.txt" || entries[0].IsDir || entries[0].Size != 5 {
		t.Fatalf("List = %+v, want one file a.txt size 5", entries)
	}

	data, err := files.Read(ctx, "a.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("Read = %q, want %q", data, "hello")
	}

	stat, err := files.Stat(ctx, "a.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Name != "a.txt" || stat.IsDir || stat.Size != 5 {
		t.Errorf("Stat = %+v, want name=a.txt isDir=false size=5", stat)
	}

	if err := files.Copy(ctx, "a.txt", "b.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if data, err := files.Read(ctx, "b.txt"); err != nil || string(data) != "hello" {
		t.Fatalf("Read copy = %q, %v", data, err)
	}

	if err := files.Rename(ctx, "b.txt", "c.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hs.root, "b.txt")); !os.IsNotExist(err) {
		t.Errorf("b.txt still exists on disk after rename")
	}
	if _, err := os.Stat(filepath.Join(hs.root, "c.txt")); err != nil {
		t.Errorf("c.txt missing on disk after rename: %v", err)
	}
}

func TestSSHRemoveDeletesNestedDirectory(t *testing.T) {
	hs := newHostopsTestServer(t)
	ctx := context.Background()
	files := NewSSH(newTestSSHClient(t, hs)).Files()

	if err := os.MkdirAll(filepath.Join(hs.root, "sub", "deeper"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := files.Write(ctx, "sub/deeper/leaf.txt", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := files.Remove(ctx, "sub"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hs.root, "sub")); !os.IsNotExist(err) {
		t.Errorf("sub still exists on disk after Remove")
	}
}
