// Package sshpty implements session.Backend over an SSH connection
// (PROJECT_PLAN.md §4.5.1, §12b M17), so Manager's existing read-loop,
// ring-buffer, broadcast and terminate/restart machinery drives a remote
// shell exactly as it drives a local one — nothing above the Backend
// interface knows or cares which this is.
package sshpty

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Andste82/sessile/backend/internal/envfile"
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

	// pidFilePath is where the started command's PID was asked to record
	// itself (§4.10) — "" for a target Start didn't wrap (see
	// wrapWithPIDRecording). internal/hostops reads it lazily, only when a
	// caller actually asks to scope a process tree to this session; Start
	// itself never reads it back.
	pidFilePath string
}

// Start dials target, requests a PTY, and starts its configured command.
// It is StartWithEnv without an environment.
func Start(t Target, dir string, rows, cols uint16) (*PTY, error) {
	p, _, err := StartWithEnv(t, StartOptions{Dir: dir, Rows: rows, Cols: cols})
	return p, err
}

// StartWithEnv dials target, requests a PTY, and starts its configured
// command. A non-empty o.Dir is the absolute start directory (POSIX targets
// only): it is verified over SFTP before the shell exists and entered by the
// exec preamble (wrapWithPIDRecording). Rows/cols size the initial window,
// exactly like terminal.Start's local equivalent.
//
// o.Env and the optional remote .env (o.LoadDotenv) are exported by the same
// preamble (issue #123). The returned warnings describe a .env that could
// not be used; they never contain a value and are not stored anywhere.
func StartWithEnv(t Target, o StartOptions) (*PTY, []string, error) {
	var warnings []string
	p, err := startPTY(t, o, &warnings)
	if err != nil {
		return nil, nil, err
	}
	return p, warnings, nil
}

func startPTY(t Target, o StartOptions, warnings *[]string) (*PTY, error) {
	dir, rows, cols := o.Dir, o.Rows, o.Cols
	supportsEnv := SupportsEnv(t.TargetOS, t.TerminalType)
	if dir != "" && !supportsEnv {
		return nil, &ErrDirUnsupported{}
	}
	// Refused before dialing: nothing would apply the variables, and a .env
	// must not even be read for such a target.
	if (len(o.Env) > 0 || o.LoadDotenv) && !supportsEnv {
		return nil, ErrEnvUnsupported
	}

	client, err := Dial(context.Background(), t)
	if err != nil {
		return nil, err
	}

	// Checked before NewSession so a missing directory fails visibly
	// instead of silently landing in the home directory. One SFTP client
	// serves both this check and the .env read.
	var dotenv []envfile.Entry
	if dir != "" || o.LoadDotenv {
		sc, serr := openSFTP(client)
		switch {
		case serr != nil && dir != "":
			_ = client.Close()
			return nil, &ErrSFTPUnavailable{Err: serr}
		case serr != nil:
			// Only the .env wanted SFTP: the session still starts. openSFTP
			// may have closed the client on timeout; NewSession then fails
			// visibly, which is the right outcome.
			*warnings = append(*warnings, warnDotenvUnreadable)
		default:
			if dir != "" {
				if err := checkDirSFTP(sc, dir); err != nil {
					_ = sc.Close()
					_ = client.Close()
					return nil, err
				}
			}
			if o.LoadDotenv {
				var w []string
				dotenv, w = readRemoteDotenv(sc, dir)
				*warnings = append(*warnings, w...)
			}
			_ = sc.Close()
		}
	}
	final := envfile.Merge(o.Env, dotenv)

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

	pidFilePath, startCmd := wrapWithPIDRecording(t.TargetOS, t.TerminalType, dir, cmd, final)

	if len(startCmd) > envfile.MaxExportBytes {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = session.Close()
		_ = client.Close()
		return nil, ErrEnvTooLarge
	}

	if err := session.Start(startCmd); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = session.Close()
		_ = client.Close()
		// cmd, never startCmd: the wrapped line carries the exported values,
		// and x/crypto's own error ("ssh: command <line> failed") quotes it,
		// so the line is scrubbed out of the message (and not %w-wrapped).
		return nil, fmt.Errorf("start %q: %s", cmd, strings.ReplaceAll(err.Error(), startCmd, "<command>"))
	}

	p := &PTY{
		client: client, session: session,
		stdinR: stdinR, stdinW: stdinW,
		stdoutR: stdoutR, stdoutW: stdoutW,
		done:        make(chan struct{}),
		pidFilePath: pidFilePath,
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

// wrapWithPIDRecording rewrites cmd so that whatever process ends up
// running it also, silently, records its own PID first — needed because
// there is no SSH-protocol-level way to ask "which remote process is my
// shell", and the alternative internal/hostops otherwise falls back to
// (matching this connection's socket against `ss` on the far end) usually
// can't resolve on a stock OpenSSH target at all (§4.10's design note).
//
// The trick is `exec`: "echo $$ > path; exec <target>" records the
// wrapper's own PID (echo is a shell builtin — no fork), then exec
// *replaces* that same process with <target>, keeping the PID identical.
// So the recorded PID is exactly the PID of the process running cmd — not
// a guess, not a different process, and nothing is written to the PTY the
// user sees (the write goes to a file, never to stdout/stderr).
//
// The whole preamble — not just <target> — is itself wrapped as one
// single-quoted argument to an explicitly-named `sh -c`, rather than sent
// as-is to be interpreted by whatever sshd's `<login-shell> -c "…"` turns
// out to be. `$$` and `2>` are not portable to every shell an operator's
// account might have: fish rejects `$$` outright ("$$ is not the pid...
// please use $fish_pid"), and csh/tcsh's `2>` isn't fd-numbered redirect
// syntax at all — `echo $$ > path 2>/dev/null` silently redirects `path`'s
// own stdout instead of writing the pid to it, so the file is never
// created — a real, measured difference between "loud parse error" and
// "quiet wrong behavior" depending on the specific shell, verified against
// real `fish` and real (Debian's bsd-csh) `csh` binaries, not assumed.
// Wrapping the whole preamble through an explicit `sh -c '<opaque string>'`
// means the login shell only ever needs to parse "invoke sh with two
// quoted arguments" — ordinary external-command invocation syntax, as
// close to universal across interactive shells as anything gets — and
// never has to understand $$/2>/exec itself.
//
// <target> is cmd unquoted when it's one of the fixed shell names
// (bash/zsh/fish) — a bare word needs no interpretation by anything, so
// there is nothing here for any shell to get wrong — and `sh -c
// '<quoted cmd>'` only for a "custom" TerminalType, whose CustomCommand
// may contain real shell syntax. That is a real, documented behavior
// change for a CustomCommand written assuming a non-POSIX login shell's
// own builtins (bash's `source`, `[[`, arrays): before this preamble
// existed, CustomCommand was interpreted by sshd's own `<login-shell> -c
// "…"`, whatever that happened to be; sessile has never actually known
// what that is (TerminalType is sessile's own choice of what to run, not
// a read of the account's /etc/passwd shell), so "preserve the exact
// previous interpreter" was never a guarantee this could keep making — a
// CustomCommand needing another shell's syntax should say so itself now
// ("bash -c '…'"), the same way a portable script would.
//
// Skipped entirely for a Windows target — this is POSIX shell syntax, and
// Windows's OpenSSH doesn't run exec requests through a POSIX shell in the
// first place. Those targets get no PID recording; internal/hostops's
// ss-based fallback is what little there is for them today.
//
// Both targetOS and terminalType gate this, because either one alone
// misses a real configuration. targetOS is needed because a host can be
// Windows and still use TerminalType "custom" (a CustomCommand like
// "powershell.exe -NoLogo"), which terminalType doesn't reveal.
// terminalType is needed because targetOS is not a reliable signal on its
// own: hosts.TargetOS is documented as informational, nothing validates
// it, and the host dialog defaults it to "" ("Unspecified") — so a
// Windows host running powershell, and every hosts.yml written before
// the TargetOS field existed, arrives here with targetOS == "". Gating on
// targetOS alone sent those the POSIX preamble: Win32-OpenSSH runs it
// through cmd.exe, "echo $$ > /tmp/…" writes a nonsense file or fails
// outright, and "exec" is not a cmd.exe builtin — the session fails to
// start. Either signal saying "Windows" is therefore enough to skip.
//
// A non-empty dir (POSIX only) prefixes the preamble with `cd -- <dir> &&`.
// This is a safety net behind Start's SFTP check: a failed cd exits the
// wrapper non-zero and never reaches exec, so the user never silently
// lands in the wrong directory. Windows targets get no wrapper and no cd.
//
// env (already merged and sorted, see envfile.Merge) is exported after the
// cd and before the PID echo: `export K='v'; export K2='v2'; …`. Names are
// regex-validated upstream so they go in bare; values go through
// shellSingleQuote, and the whole preamble is quoted once more as the
// `sh -c` argument. A Windows-flagged target never gets env (StartWithEnv
// refuses before reaching here).
func wrapWithPIDRecording(targetOS, terminalType, dir, cmd string, env []envfile.Entry) (pidFilePath, wrapped string) {
	if !SupportsEnv(targetOS, terminalType) {
		return "", cmd
	}
	var path string
	token := make([]byte, 8)
	if _, err := rand.Read(token); err == nil {
		path = "/tmp/.sessile-pid-" + hex.EncodeToString(token)
	} else if dir == "" && len(env) == 0 {
		return "", cmd // can't generate a safe path — run cmd exactly as before
	}

	target := cmd // a bare shell name (bash/zsh/fish) — nothing to interpret, exec it directly
	if terminalType == "custom" {
		target = "sh -c " + shellSingleQuote(cmd)
	}
	var b strings.Builder
	for _, e := range env {
		fmt.Fprintf(&b, "export %s=%s; ", e.Name, shellSingleQuote(e.Value))
	}
	if path != "" {
		fmt.Fprintf(&b, "echo $$ > %s 2>/dev/null; ", path)
	}
	fmt.Fprintf(&b, "exec %s", target)
	preamble := b.String()
	if dir != "" {
		preamble = fmt.Sprintf("cd -- %s && { %s; }", shellSingleQuote(dir), preamble)
	}
	return path, "sh -c " + shellSingleQuote(preamble)
}

// shellSingleQuote wraps s for safe use as one single-quoted POSIX shell
// word: close the quote, escape a literal quote, reopen it.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PIDFilePath returns where wrapWithPIDRecording asked the started
// command to record its own PID, or "" if Start skipped that (a Windows
// target). internal/hostops reads it lazily and on demand (§4.10) — never
// read here, so a session that never uses the process-tree feature pays
// nothing beyond the one-line shell preamble already in its exec request.
func (p *PTY) PIDFilePath() string { return p.pidFilePath }

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
