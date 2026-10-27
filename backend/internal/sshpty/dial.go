package sshpty

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Dial connects to t through the pinned host-key callback (§4.5.1) and
// returns the authenticated client. It is the single dial path shared by
// Start and the session-less directory browser: an unknown or changed host
// key comes back as *ErrHostKeyUnknown / *ErrHostKeyChanged, unwrapped, and
// is never accepted silently.
//
// The TCP dial and the SSH handshake both honour ctx, so cancelling it
// aborts a connection attempt that is still waiting on a slow host.
func Dial(ctx context.Context, t Target) (*ssh.Client, error) {
	methods, err := authMethods(t)
	if err != nil {
		return nil, err
	}

	address := ensurePort(t.Address)
	cfg := &ssh.ClientConfig{
		User:            t.Username,
		Auth:            methods,
		HostKeyCallback: pinnedHostKeyCallback(t.TrustedHostKeyFingerprint),
		Timeout:         dialTimeout,
	}

	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", address, err)
	}

	// ssh.NewClientConn has no context parameter; closing the connection is
	// what aborts a handshake in flight.
	type result struct {
		c     ssh.Conn
		chans <-chan ssh.NewChannel
		reqs  <-chan *ssh.Request
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		c, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
		ch <- result{c, chans, reqs, err}
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		if r := <-ch; r.err == nil {
			_ = r.c.Close()
		}
		return nil, fmt.Errorf("ssh dial %s: %w", address, ctx.Err())
	case r := <-ch:
		if r.err != nil {
			_ = conn.Close()
			// Host-key rejections come back wrapped by x/crypto/ssh's
			// handshake error; unwrap them so callers can type-switch on
			// the originals (session.CreateSSH maps them to distinct API
			// responses, §6).
			var unknown *ErrHostKeyUnknown
			if errors.As(r.err, &unknown) {
				return nil, unknown
			}
			var changed *ErrHostKeyChanged
			if errors.As(r.err, &changed) {
				return nil, changed
			}
			return nil, fmt.Errorf("ssh dial %s: %w", address, r.err)
		}
		return ssh.NewClient(r.c, r.chans, r.reqs), nil
	}
}

// ErrDirNotFound is returned when the requested start directory does not
// exist on the host, is not a directory, or cannot be stat'ed (Cause holds
// the underlying reason for the latter, e.g. permission denied).
type ErrDirNotFound struct {
	Path  string
	Cause error
}

func (e *ErrDirNotFound) Error() string {
	msg := fmt.Sprintf("start directory %q does not exist or is not a directory on the host", e.Path)
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *ErrDirNotFound) Unwrap() error { return e.Cause }

// ErrSFTPUnavailable is returned when a start directory was requested but
// the host's SFTP subsystem cannot be opened to verify it. There is no
// silent proceed: a start directory that cannot be checked is refused.
type ErrSFTPUnavailable struct{ Err error }

func (e *ErrSFTPUnavailable) Error() string {
	return "cannot verify start directory: the host's SFTP subsystem is unavailable"
}

func (e *ErrSFTPUnavailable) Unwrap() error { return e.Err }

// ErrDirUnsupported is returned when a start directory is requested for a
// Windows target (the start-directory mechanism is POSIX shell syntax).
type ErrDirUnsupported struct{}

func (e *ErrDirUnsupported) Error() string {
	return "start directory is not supported for Windows targets"
}

// sftpOpenTimeout bounds the SFTP subsystem handshake, which sftp.NewClient
// itself cannot (no context support).
const sftpOpenTimeout = 15 * time.Second

// openSFTP opens SFTP on client; on timeout it closes client, which unblocks
// the handshake. The caller treats the client as unusable after an error.
func openSFTP(client *ssh.Client) (*sftp.Client, error) {
	type result struct {
		sc  *sftp.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc, err := sftp.NewClient(client)
		ch <- result{sc, err}
	}()
	t := time.NewTimer(sftpOpenTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.sc, r.err
	case <-t.C:
		_ = client.Close()
		return nil, errors.New("sftp subsystem did not answer in time")
	}
}

// checkDirSFTP verifies dir is an existing directory on the host, over an
// SFTP client opened on the already-dialed (and already TOFU-verified)
// connection.
func checkDirSFTP(sc *sftp.Client, dir string) error {
	fi, err := sc.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &ErrDirNotFound{Path: dir}
		}
		return &ErrDirNotFound{Path: dir, Cause: err}
	}
	if !fi.IsDir() {
		return &ErrDirNotFound{Path: dir}
	}
	return nil
}
