package hostops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sync"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sshTransport runs commands and touches files over a session's own
// already-dialed, TOFU-verified *ssh.Client (§4.5.1) — no second dial, no
// second trust decision. Safe for concurrent use: Exec opens a fresh
// ssh.Session per call (the protocol requires it), and the sftp.Client is
// opened once, lazily, and shared.
type sshTransport struct {
	client *ssh.Client

	sftpMu   sync.Mutex
	sftpErr  error
	sftpOnce *sftp.Client
}

// NewSSH returns a Transport backed by client. The caller owns client's
// lifecycle — hostops never closes it.
func NewSSH(client *ssh.Client) Transport {
	return &sshTransport{client: client}
}

func (t *sshTransport) Exec(ctx context.Context, line string) (Result, error) {
	session, err := t.client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("open ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(line) }()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return Result{}, ctx.Err()
	case runErr := <-done:
		exitCode := 0
		if runErr != nil {
			var exitErr *ssh.ExitError
			if errors.As(runErr, &exitErr) {
				exitCode = exitErr.ExitStatus()
				runErr = nil
			}
		}
		if runErr != nil {
			return Result{}, fmt.Errorf("run %q: %w", line, runErr)
		}
		return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode}, nil
	}
}

func (t *sshTransport) Files() FileTransport { return &sshFileTransport{transport: t} }

// sftpClient opens the sftp-server subsystem on first use and reuses it —
// most sessions never touch the file browser, so paying for the subsystem
// request only when something does keeps ProcessTree/Exec-only sessions
// (and targets with no sftp-server at all) unaffected.
func (t *sshTransport) sftpClient() (*sftp.Client, error) {
	t.sftpMu.Lock()
	defer t.sftpMu.Unlock()
	if t.sftpOnce != nil || t.sftpErr != nil {
		return t.sftpOnce, t.sftpErr
	}
	c, err := sftp.NewClient(t.client)
	if err != nil {
		t.sftpErr = fmt.Errorf("open sftp session: %w", err)
		return nil, t.sftpErr
	}
	t.sftpOnce = c
	return c, nil
}

// sshFileTransport is FileTransport over SFTP — the wire protocol is the
// same regardless of the remote OS, so this one implementation covers every
// SSH target (§4.10). Remote paths are always "/"-separated per the SFTP
// spec, so this uses "path", never "path/filepath" (OS-dependent on the
// machine running sessile, not on the remote).
type sshFileTransport struct {
	transport *sshTransport
}

func (t *sshFileTransport) List(_ context.Context, p string) ([]DirEntry, error) {
	c, err := t.transport.sftpClient()
	if err != nil {
		return nil, err
	}
	entries, err := c.ReadDir(p)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", p, err)
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, DirEntry{Name: e.Name(), IsDir: e.IsDir(), Size: e.Size(), ModTime: e.ModTime()})
	}
	return out, nil
}

func (t *sshFileTransport) Read(_ context.Context, p string) ([]byte, error) {
	c, err := t.transport.sftpClient()
	if err != nil {
		return nil, err
	}
	f, err := c.Open(p)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", p, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return data, nil
}

func (t *sshFileTransport) Write(_ context.Context, p string, data []byte) error {
	c, err := t.transport.sftpClient()
	if err != nil {
		return err
	}
	f, err := c.Create(p)
	if err != nil {
		return fmt.Errorf("create %s: %w", p, err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

func (t *sshFileTransport) Rename(_ context.Context, oldpath, newpath string) error {
	c, err := t.transport.sftpClient()
	if err != nil {
		return err
	}
	if err := c.Rename(oldpath, newpath); err != nil {
		return fmt.Errorf("rename %s to %s: %w", oldpath, newpath, err)
	}
	return nil
}

func (t *sshFileTransport) Remove(ctx context.Context, p string) error {
	c, err := t.transport.sftpClient()
	if err != nil {
		return err
	}
	info, err := c.Stat(p)
	if err != nil {
		return fmt.Errorf("stat %s: %w", p, err)
	}
	if !info.IsDir() {
		if err := c.Remove(p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		return nil
	}
	entries, err := c.ReadDir(p)
	if err != nil {
		return fmt.Errorf("list %s: %w", p, err)
	}
	for _, e := range entries {
		if err := t.Remove(ctx, path.Join(p, e.Name())); err != nil {
			return err
		}
	}
	if err := c.RemoveDirectory(p); err != nil {
		return fmt.Errorf("remove directory %s: %w", p, err)
	}
	return nil
}

func (t *sshFileTransport) Copy(ctx context.Context, src, dst string) error {
	data, err := t.Read(ctx, src)
	if err != nil {
		return err
	}
	return t.Write(ctx, dst, data)
}
