package tasks

import (
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// FS is where a task folder is written: the target over the session's own
// SFTP channel, or the server's disk for a local-host task. Paths are the
// target's own, absolute.
type FS interface {
	// Home is the SSH login directory (unused for local tasks).
	Home() (string, error)
	Join(elem ...string) string
	MkdirAll(p string) error
	// Chmod sets p's mode: a task folder holds the agent's instructions and,
	// briefly, its credentials, so it is private to the login user.
	Chmod(p string, perm os.FileMode) error
	WriteFile(p string, data []byte, perm os.FileMode) error
	Close() error
}

// sftpFS writes over an SFTP channel on the session's own client — no second
// dial, no second host-key decision (§4.12.2).
type sftpFS struct{ c *sftp.Client }

func newSFTPFS(client *ssh.Client) (*sftpFS, error) {
	c, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("open sftp: %w", err)
	}
	return &sftpFS{c: c}, nil
}

func (f *sftpFS) Home() (string, error) { return f.c.Getwd() }

// Join is POSIX: SFTP paths use forward slashes on every target, Windows
// included (/C:/Users/…).
func (f *sftpFS) Join(elem ...string) string { return path.Join(elem...) }

func (f *sftpFS) MkdirAll(p string) error {
	if err := f.c.MkdirAll(p); err != nil {
		return fmt.Errorf("create %s: %w", p, err)
	}
	return nil
}

func (f *sftpFS) Chmod(p string, perm os.FileMode) error {
	if err := f.c.Chmod(p, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", p, err)
	}
	return nil
}

func (f *sftpFS) WriteFile(p string, data []byte, perm os.FileMode) error {
	w, err := f.c.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fmt.Errorf("open %s: %w", p, err)
	}
	// Mode first, while the file is still empty: .env must never be readable
	// by anyone else, not even for the moment between write and chmod.
	if err := w.Chmod(perm); err != nil {
		w.Close()
		return fmt.Errorf("chmod %s: %w", p, err)
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return fmt.Errorf("write %s: %w", p, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close %s: %w", p, err)
	}
	return nil
}

func (f *sftpFS) Close() error { return f.c.Close() }

// localFS writes to the server's disk, under a folder the session manager
// has already validated against the workspace root (§4.5).
type localFS struct{}

func (localFS) Home() (string, error)      { return os.UserHomeDir() }
func (localFS) Join(elem ...string) string { return filepath.Join(elem...) }
func (localFS) MkdirAll(p string) error    { return os.MkdirAll(p, 0o700) }
func (localFS) Close() error               { return nil }

func (localFS) Chmod(p string, perm os.FileMode) error { return os.Chmod(p, perm) }

func (localFS) WriteFile(p string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open %s: %w", p, err)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return fmt.Errorf("chmod %s: %w", p, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", p, err)
	}
	return f.Close()
}
