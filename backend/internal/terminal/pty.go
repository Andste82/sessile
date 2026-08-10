// Package terminal wraps PTY creation, resize and teardown for shell processes.
package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// defaultLocale is forced on shells that inherit no locale of their own.
// C.UTF-8 exists on both glibc and musl and needs no locale packages.
const defaultLocale = "LANG=C.UTF-8"

// localeVars are the locale settings that decide character handling, in the
// order the C library resolves them.
var localeVars = []string{"LC_ALL", "LC_CTYPE", "LANG"}

// PTY couples a running shell process with its controlling pseudo-terminal.
type PTY struct {
	Cmd  *exec.Cmd
	File *os.File

	writeMu sync.Mutex // serializes input writes from multiple clients (§4.4)
}

// Foreground describes the program currently owning the terminal — the process
// group the kernel will deliver a Ctrl+C to. When it is the session's own
// shell, the shell is sitting at its prompt; otherwise it is the program the
// user started (PROJECT_PLAN.md §4.7).
//
// A zero value means "not known", which is what every field reduces to off
// Linux. Callers must treat empty strings as absent rather than as an answer.
type Foreground struct {
	PID  int
	Name string // the process group leader, e.g. "claude", "htop", "bash"
	Cwd  string // absolute working directory, still to be sandbox-checked
	// Chain is the group leader followed by the processes running under it that
	// stayed in its group — ["bash", "ping"] for a script that runs ping. Its
	// first element is always Name, and its last is the program actually
	// running. Empty when nothing could be determined.
	Chain []string
}

// Leaf is the program actually running: the innermost member of the chain, or
// the group leader when there is nothing under it.
func (f Foreground) Leaf() string {
	if len(f.Chain) == 0 {
		return f.Name
	}
	return f.Chain[len(f.Chain)-1]
}

// Write sends input bytes to the PTY, serialized across concurrent callers.
func (p *PTY) Write(data []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if _, err := p.File.Write(data); err != nil {
		return fmt.Errorf("write pty: %w", err)
	}
	return nil
}

// Start launches shellPath in dir with a PTY of the given size. The shell is
// placed in its own session/process group (Setsid) so the whole tree can be
// signalled on teardown (PROJECT_PLAN.md §4.6). shellPath must already be an
// absolute, allowlisted path (resolved by the caller via exec.LookPath).
//
// extraEnv is appended last and so overrides anything of the same name inherited
// from the server; it carries the per-session history settings (§8).
func Start(shellPath, dir string, rows, cols uint16, extraEnv []string) (*PTY, error) {
	cmd := exec.Command(shellPath)
	cmd.Dir = dir
	cmd.Env = shellEnv(os.Environ(), extraEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}
	return &PTY{Cmd: cmd, File: f}, nil
}

// shellEnv builds the shell's environment from the server's own: TERM, because
// the shell must know what it is drawing on, a UTF-8 locale when the parent has
// none, and finally the caller's extra assignments.
//
// Order matters: exec resolves duplicate names to the last occurrence, so extra
// wins over both the parent and the defaults set here.
//
// The locale is not cosmetic. The wire protocol is UTF-8 by definition — the
// browser encodes keystrokes with TextEncoder and xterm.js decodes PTY bytes as
// UTF-8 — but a server started in a bare container inherits no locale, and
// glibc's default C locale is strictly ASCII. bash then rejects a typed umlaut
// with a BEL and zsh renders it as <ffffffff>; musl is more forgiving, which is
// why this only bites on the glibc images. An explicit locale always wins:
// callers who really want C keep it.
func shellEnv(parent, extra []string) []string {
	env := append(append([]string{}, parent...), "TERM=xterm-256color")
	if !hasLocale(parent) {
		env = append(env, defaultLocale)
	}
	return append(env, extra...)
}

// hasLocale reports whether env configures character handling. An empty
// assignment (LANG=) is not a setting — the C library ignores it too.
func hasLocale(env []string) bool {
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if ok && value != "" {
			for _, want := range localeVars {
				if name == want {
					return true
				}
			}
		}
	}
	return false
}

// Resize applies a new terminal size to the PTY.
func (p *PTY) Resize(rows, cols uint16) error {
	if err := pty.Setsize(p.File, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return fmt.Errorf("resize pty: %w", err)
	}
	return nil
}

// Pid returns the shell process id (0 if not started).
func (p *PTY) Pid() int {
	if p.Cmd == nil || p.Cmd.Process == nil {
		return 0
	}
	return p.Cmd.Process.Pid
}

// Signal sends sig to the shell's whole process group (the negative pid targets
// the group created by Setsid). Interactive shells ignore SIGTERM but honor
// SIGHUP (terminal hangup), which is why teardown leads with SIGHUP.
func (p *PTY) Signal(sig syscall.Signal) {
	if p.Cmd == nil || p.Cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-p.Cmd.Process.Pid, sig)
}

// Wait reaps the shell process. It must be called exactly once, by the read
// loop after it observes EOF, so exited children never become zombies.
func (p *PTY) Wait() {
	if p.Cmd != nil {
		_, _ = p.Cmd.Process.Wait()
	}
}

// CloseFile closes the PTY master file descriptor.
func (p *PTY) CloseFile() {
	if p.File != nil {
		_ = p.File.Close()
	}
}
