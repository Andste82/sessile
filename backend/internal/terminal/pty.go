// Package terminal wraps PTY creation, resize and teardown for shell processes.
package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// PTY couples a running shell process with its controlling pseudo-terminal.
type PTY struct {
	Cmd  *exec.Cmd
	File *os.File

	writeMu sync.Mutex // serializes input writes from multiple clients (§4.4)
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
func Start(shellPath, dir string, rows, cols uint16) (*PTY, error) {
	cmd := exec.Command(shellPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}
	return &PTY{Cmd: cmd, File: f}, nil
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
