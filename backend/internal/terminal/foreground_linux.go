//go:build linux

package terminal

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// Foreground asks the kernel which process group owns the terminal, then reads
// its name and working directory out of /proc.
//
// This is how tmux resolves #{pane_current_command} and #{pane_current_path}
// (osdep-linux.c): tcgetpgrp on the pty master, then /proc/<pgid>/. It is a
// measurement, not a heuristic — there is no guessing from output, and it costs
// one ioctl and two small reads, called once a second per session rather than
// per byte.
//
// The ioctl runs on the master fd, which works because the kernel's tiocgpgrp
// compares the tty it was called on against the tty's link and only refuses
// when they are the same — a master is never its own slave.
//
// Errors are not reported: every one of them means "cannot tell right now" —
// the shell exited between the read loop noticing and this call, the process
// left no /proc entry, the fd is closed. The caller's answer is the zero value
// either way, and an activity indicator is not worth an error path that has to
// be logged once a second.
func (p *PTY) Foreground() Foreground {
	if p == nil || p.File == nil {
		return Foreground{}
	}

	// SyscallConn, never File.Fd(): Fd detaches the file from the runtime
	// poller and puts it into blocking mode, which would strand the read loop
	// in a Read that Close can no longer interrupt. Control hands out the
	// descriptor for the duration of the callback and keeps it from being
	// closed underneath, without touching its mode.
	rc, err := p.File.SyscallConn()
	if err != nil {
		return Foreground{}
	}
	var (
		pgid     int
		ioctlErr error
	)
	if err := rc.Control(func(fd uintptr) {
		pgid, ioctlErr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
	}); err != nil || ioctlErr != nil || pgid <= 0 {
		return Foreground{}
	}

	fg := Foreground{PID: pgid}
	proc := "/proc/" + strconv.Itoa(pgid)
	// comm rather than cmdline: it is a single short line needing no NUL
	// splitting, and the dashboard wants "claude", not the full argv.
	if b, err := os.ReadFile(proc + "/comm"); err == nil {
		fg.Name = strings.TrimSpace(string(b))
	}
	if dir, err := os.Readlink(proc + "/cwd"); err == nil {
		fg.Cwd = dir
	}
	return fg
}
