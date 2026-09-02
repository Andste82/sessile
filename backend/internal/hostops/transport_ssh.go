package hostops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

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

	// pidFilePath is where sshpty.Start's exec preamble recorded the
	// session's own PID (§4.10) — "" for a target that didn't get one (a
	// Windows target; wrapWithPIDRecording's job, not this package's).
	pidFilePath string

	sftpMu   sync.Mutex
	sftpErr  error
	sftpOnce *sftp.Client
}

// NewSSH returns a Transport backed by client. The caller owns client's
// lifecycle — hostops never closes it. pidFilePath is sshpty.PTY's
// PIDFilePath() — "" is fine, it just means sessionRootPID has one fewer
// way to resolve.
func NewSSH(client *ssh.Client, pidFilePath string) Transport {
	return &sshTransport{client: client, pidFilePath: pidFilePath}
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

func (t *sshFileTransport) Stat(_ context.Context, p string) (DirEntry, error) {
	c, err := t.transport.sftpClient()
	if err != nil {
		return DirEntry{}, err
	}
	info, err := c.Stat(p)
	if err != nil {
		return DirEntry{}, fmt.Errorf("stat %s: %w", p, err)
	}
	return DirEntry{Name: info.Name(), IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()}, nil
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

// pidFileRetries/pidFileRetryDelay bound how long sessionRootPID waits for
// sshpty.Start's exec preamble to have actually run "echo $$ > path" by
// the time something asks — Start returning only means the exec request
// was accepted, not that its first shell statement has executed yet. This
// is normally near-instant; the budget here is generous, not expected.
const (
	pidFileRetries    = 10
	pidFileRetryDelay = 100 * time.Millisecond
)

// sessionRootPID finds this SSH session's own PID on the target, trying
// two independent mechanisms in order:
//
//  1. Reading back sshpty.Start's exec preamble (§4.10's "wrap with PID
//     recording") — the process running the session's command wrote its
//     own PID to t.pidFilePath before exec-ing into that command, so this
//     is exact whenever a preamble was written (every POSIX SSH target;
//     see wrapWithPIDRecording). Retried briefly for the startup race
//     noted above.
//  2. Falling back to matching this connection's own local TCP address
//     against `ss`'s socket table on the far end (below) — kept as a
//     second attempt for a target with no pidFilePath (a Windows target,
//     where the preamble doesn't apply) or where reading it somehow
//     failed; on a stock OpenSSH target this mechanism alone usually
//     can't resolve anything (see its own doc comment), which is exactly
//     why (1) exists.
func (t *sshTransport) sessionRootPID(ctx context.Context) (int, bool) {
	if t.pidFilePath != "" {
		if pid, ok := t.readPIDFile(ctx); ok {
			return pid, true
		}
	}
	return t.sessionRootPIDViaSocket(ctx)
}

func (t *sshTransport) readPIDFile(ctx context.Context) (int, bool) {
	line := fmt.Sprintf("cat %s 2>/dev/null", t.pidFilePath)
	for attempt := 0; attempt < pidFileRetries; attempt++ {
		res, err := t.Exec(ctx, line)
		if err == nil && res.ExitCode == 0 {
			if pid, atoiErr := strconv.Atoi(strings.TrimSpace(string(res.Stdout))); atoiErr == nil && pid > 0 {
				return pid, true
			}
		}
		select {
		case <-ctx.Done():
			return 0, false
		case <-time.After(pidFileRetryDelay):
		}
	}
	return 0, false
}

// sessionRootPIDViaSocket is the ss-based fallback — reached only when
// there's no pidFilePath to read (a Windows target) or reading it failed
// (see sessionRootPID's doc comment). It matches this *ssh.Client's own
// local TCP address/port — which, from the target's point of view, is the
// peer of the established connection it's holding — against `ss`'s socket
// table on the far end. That process is the one sshd forked for this
// login, so its descendant subtree (walked by the existing
// buildProcessTree, using this PID as root, exactly like any other) is
// this session's own processes and nothing else's.
//
// This is deterministic, not a guess: either the matching socket is found
// with a pid attached, or it isn't — never a plausible-looking wrong
// answer, unlike inferring from process names or timing (the class of
// technique §4.7's removed activity classifier already showed doesn't
// hold up). It needs `ss` (iproute2) on the target, *and* it needs the pid
// behind that socket to be visible to the login user running it — which,
// tested against a real sshd, it usually is not: OpenSSH's per-connection
// process is commonly non-dumpable (PR_SET_DUMPABLE cleared, standard
// hardening for a process that started privileged), which blocks
// /proc/<pid>/fd — and so `ss -p` — for everyone but root, even the
// connection's own login user who technically owns that process. So on a
// stock OpenSSH target this resolves to "not found" far more often than
// "found" — which is exactly why sessionRootPID tries the pidFilePath
// first: that mechanism doesn't depend on any of this. This one stays as
// the fallback for a Windows target and for defense in depth. ok is false
// for anything else too (ss missing, a non-Linux target, no line matching
// this port at all).
func (t *sshTransport) sessionRootPIDViaSocket(ctx context.Context) (int, bool) {
	local := t.client.LocalAddr()
	if local == nil {
		return 0, false
	}
	_, myPort, err := net.SplitHostPort(local.String())
	if err != nil {
		return 0, false
	}

	res, err := t.Exec(ctx, "ss -Htnp state established")
	if err != nil || res.ExitCode != 0 {
		return 0, false
	}
	return parseSSPeerPID(string(res.Stdout), myPort)
}

var ssPIDPattern = regexp.MustCompile(`pid=(\d+)`)

// parseSSPeerPID scans `ss -Htnp state established` output (Recv-Q, Send-Q,
// Local Address:Port, Peer Address:Port, Process — one line per
// connection, no header since -H) for the one line whose peer port is
// myPort, and returns the pid from its Process column. myPort alone is
// enough to match: the kernel will not hand the same local ephemeral port
// to a second outbound connection while this one is still open, so it is
// unique system-wide for the connection's whole lifetime — no need to
// compare the peer IP too.
func parseSSPeerPID(output, myPort string) (int, bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		peer := fields[3]
		i := strings.LastIndex(peer, ":")
		if i < 0 || peer[i+1:] != myPort {
			continue
		}
		process := strings.Join(fields[4:], " ")
		m := ssPIDPattern.FindStringSubmatch(process)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		return pid, true
	}
	return 0, false
}
