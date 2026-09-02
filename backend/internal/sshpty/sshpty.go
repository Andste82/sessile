// Package sshpty implements session.Backend over an SSH connection
// (PROJECT_PLAN.md §4.5.1, §12b M17), so Manager's existing read-loop,
// ring-buffer, broadcast and terminate/restart machinery drives a remote
// shell exactly as it drives a local one — nothing above the Backend
// interface knows or cares which this is.
package sshpty

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// Target names an SSH host and how to connect to it. Credentials are
// plaintext in memory only for the lifetime of one Start call — the
// long-term storage (hosts.Host) is the only place they persist, and that
// is deliberately plaintext too (CLAUDE.md's security posture).
type Target struct {
	Address  string // host, or host:port (default port 22)
	Username string

	AuthMethod           string // "password" | "privateKey"
	Password             string
	PrivateKeyPEM        string
	PrivateKeyPassphrase string

	// TargetOS is the host's declared OS ("linux"|"darwin"|"windows"|"other",
	// hosts.TargetOS) — not used to connect (SSH doesn't care), carried
	// through so internal/hostops (§4.10) can pick a Platform once the
	// session exists. Never interpreted here.
	TargetOS string

	// TerminalType is the remote command to run: bash|zsh|fish|cmd|powershell
	// run directly, or CustomCommand when it's "custom". There is no
	// allowlist here (unlike the local shell one) — this is the user's own
	// host, entirely their own choice of command.
	TerminalType  string
	CustomCommand string

	// TrustedHostKeyFingerprint is the pinned fingerprint for this host, or
	// "" if not yet trusted (§4.5.1). Start returns *ErrHostKeyUnknown /
	// *ErrHostKeyChanged instead of connecting when this doesn't match what
	// the server presents.
	TrustedHostKeyFingerprint string
}

// dialTimeout bounds the SSH handshake, separately from any timeout on the
// TCP dial itself (ssh.ClientConfig.Timeout covers both phases).
const dialTimeout = 10 * time.Second

// PTY is Target, connected — a session.Backend backed by one SSH session
// instead of a local pty(7). Wired via os.Pipe() pairs rather than
// ssh.Session's StdinPipe/StdoutPipe helpers, which require draining before
// Wait() — a poor fit for the continuous streaming this needs.
type PTY struct {
	client  *ssh.Client
	session *ssh.Session

	stdinR, stdinW   *os.File
	stdoutR, stdoutW *os.File
	writeMu          sync.Mutex

	done chan struct{} // closed once the background Wait()/cleanup goroutine finishes
}

// Start dials target, requests a PTY, and starts its configured command.
// Rows/cols size the initial window, exactly like terminal.Start's local
// equivalent.
func Start(t Target, rows, cols uint16) (*PTY, error) {
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

	client, err := ssh.Dial("tcp", address, cfg)
	if err != nil {
		// Host-key rejections come back wrapped by x/crypto/ssh's handshake
		// error; unwrap them so callers can type-switch on the originals
		// (session.CreateSSH maps them to distinct API responses, §6).
		var unknown *ErrHostKeyUnknown
		if errors.As(err, &unknown) {
			return nil, unknown
		}
		var changed *ErrHostKeyChanged
		if errors.As(err, &changed) {
			return nil, changed
		}
		return nil, fmt.Errorf("ssh dial %s: %w", address, err)
	}

	session, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ssh new session: %w", err)
	}

	// Matches terminal.Start's local TERM value, so a program in the remote
	// shell sees the same terminal identity either way.
	if err := session.RequestPty("xterm-256color", int(rows), int(cols), ssh.TerminalModes{}); err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}
	session.Stdin = stdinR
	session.Stdout = stdoutW
	// A real remote PTY already merges stdout+stderr into one stream for
	// most servers, but some still send stderr as separate SSH extended
	// data even so — pointing both at the same pipe keeps that case correct
	// too, and matches local PTY semantics either way.
	session.Stderr = stdoutW

	cmd := t.TerminalType
	if cmd == "custom" {
		cmd = t.CustomCommand
	}
	if err := session.Start(cmd); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = session.Close()
		_ = client.Close()
		return nil, fmt.Errorf("start %q: %w", cmd, err)
	}

	p := &PTY{
		client: client, session: session,
		stdinR: stdinR, stdinW: stdinW,
		stdoutR: stdoutR, stdoutW: stdoutW,
		done: make(chan struct{}),
	}
	// session.Wait() already blocks until the stdin-copying goroutine ssh
	// starts internally has finished (it reaps every copyFunc before
	// returning), so no separate synchronization is needed for stdinR.
	// Closing stdoutW is what readLoop is waiting on: it turns the blocked
	// Read() on stdoutR into an EOF the moment the remote command exits,
	// exactly like a local PTY's master closing.
	go func() {
		_ = session.Wait()
		_ = stdoutW.Close()
		close(p.done)
	}()
	return p, nil
}

// authMethods builds the ssh.AuthMethod list for t's configured credential.
func authMethods(t Target) ([]ssh.AuthMethod, error) {
	switch t.AuthMethod {
	case "privateKey":
		var signer ssh.Signer
		var err error
		if t.PrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(t.PrivateKeyPEM), []byte(t.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(t.PrivateKeyPEM))
		}
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default: // "password"
		return []ssh.AuthMethod{ssh.Password(t.Password)}, nil
	}
}

// Read reads combined remote stdout/stderr.
func (p *PTY) Read(b []byte) (int, error) {
	return p.stdoutR.Read(b)
}

// Write sends input bytes to the remote shell, serialized across concurrent
// callers — same guarantee as terminal.PTY.Write.
func (p *PTY) Write(data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.stdinW.Write(data); err != nil {
		return fmt.Errorf("write ssh stdin: %w", err)
	}
	return nil
}

// Resize informs the remote host of a window size change.
func (p *PTY) Resize(rows, cols uint16) error {
	if err := p.session.WindowChange(int(rows), int(cols)); err != nil {
		return fmt.Errorf("ssh window-change: %w", err)
	}
	return nil
}

// Pid has no meaning for a remote shell.
func (p *PTY) Pid() int { return 0 }

// Client returns the underlying *ssh.Client so internal/hostops (§4.10) can
// open its own exec/sftp channels on the same already-dialed, TOFU-verified
// connection — no second dial, no second trust decision.
func (p *PTY) Client() *ssh.Client { return p.client }

// signalNames maps the POSIX signals terminate() sends to the RFC 4254
// names the SSH "signal" request expects.
var signalNames = map[syscall.Signal]ssh.Signal{
	syscall.SIGABRT: ssh.SIGABRT,
	syscall.SIGALRM: ssh.SIGALRM,
	syscall.SIGFPE:  ssh.SIGFPE,
	syscall.SIGHUP:  ssh.SIGHUP,
	syscall.SIGILL:  ssh.SIGILL,
	syscall.SIGINT:  ssh.SIGINT,
	syscall.SIGKILL: ssh.SIGKILL,
	syscall.SIGPIPE: ssh.SIGPIPE,
	syscall.SIGQUIT: ssh.SIGQUIT,
	syscall.SIGSEGV: ssh.SIGSEGV,
	syscall.SIGTERM: ssh.SIGTERM,
	syscall.SIGUSR1: ssh.SIGUSR1,
	syscall.SIGUSR2: ssh.SIGUSR2,
}

// Signal best-effort forwards sig as an SSH "signal" channel request — many
// OpenSSH servers silently reject it, same philosophy as local's "errors are
// not fatal" (terminate() ignores this return already). SIGKILL additionally
// force-closes the session and client immediately: the one action fully
// within our control, mirroring terminate()'s documented "giving up is the
// only option" for a shell held open outside its process group. Closing our
// end always succeeds within terminate()'s grace period; the remote process
// may keep running detached — the same caveat class as a local shell held
// open by something outside its process group.
func (p *PTY) Signal(sig syscall.Signal) {
	if name, ok := signalNames[sig]; ok {
		_ = p.session.Signal(name)
	}
	if sig == syscall.SIGKILL {
		_ = p.session.Close()
		_ = p.client.Close()
	}
}

// Wait blocks until the background cleanup goroutine started in Start has
// finished — the remote command has exited (or the connection was forced
// down by Signal(SIGKILL)) and stdoutW is closed, which is what unblocks a
// concurrent Read().
func (p *PTY) Wait() {
	<-p.done
}

// CloseFile closes the SSH session and client. Safe to call after Signal's
// SIGKILL path already closed them — a second Close is a no-op error we
// ignore, same as the first.
func (p *PTY) CloseFile() {
	_ = p.stdoutR.Close()
	_ = p.stdinW.Close()
	_ = p.session.Close()
	_ = p.client.Close()
}

// Foreground is always the zero value: there is no way to introspect a
// remote process's /proc from here. sampleSession's diff logic already
// treats the zero value as "nothing to report" (PROJECT_PLAN.md §4.7).
func (p *PTY) Foreground() terminal.Foreground {
	return terminal.Foreground{}
}
