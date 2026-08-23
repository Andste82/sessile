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
// either way, and a foreground label is not worth an error path that has to be
// logged once a second.
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
	fg.Chain = chainFrom(pgid, fg.Name)
	return fg
}

// maxChainDepth bounds the descent. Real chains are one or two deep; anything
// past this is a build system, and the label has long stopped being readable.
const maxChainDepth = 8

// chainFrom walks from the process group leader down to the process that is
// actually running, and returns the names along the way.
//
// The descent exists because a shell running a script does not do job control:
// `bash deploy.sh` leaves the `ping` it starts in its own process group, so the
// group leader — all TIOCGPGRP can report — is the script, and the program the
// user is waiting on is invisible one level down.
//
// Only children that stayed in the group are followed, and that condition is
// the whole safety of it: an interactive shell puts every job in a group of its
// own, so a backgrounded `sleep 300 &` has a different group and is skipped.
// Without that check a shell sitting at its prompt with anything in the
// background would be reported as running it.
func chainFrom(pgid int, name string) []string {
	if name == "" {
		return nil
	}
	chain := make([]string, 1, 2)
	chain[0] = name
	for pid := pgid; len(chain) < maxChainDepth; {
		next, nextName := groupChild(pid, pgid)
		if next == 0 {
			break
		}
		chain = append(chain, nextName)
		pid = next
	}
	return chain
}

// groupChild returns the child of pid that belongs to process group pgid, and
// its name.
//
// Where a pipeline inside a script puts several there at once — `ping | grep`
// makes both children of the script — the one that started first wins. It is
// the command the line is about; the rest are filters hanging off it, and it is
// also the answer tmux gives for the same pipeline typed at a prompt.
//
// Only the leader's own thread is asked for children. A shell is single
// threaded, and walking every thread would cost a directory scan per sample to
// cover a case that does not arise here.
func groupChild(pid, pgid int) (int, string) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/task/" + strconv.Itoa(pid) + "/children")
	if err != nil {
		return 0, "" // no CONFIG_PROC_CHILDREN, or the process just exited
	}

	var (
		bestPID   int
		bestName  string
		bestStart uint64
	)
	for _, f := range strings.Fields(string(b)) {
		child, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		group, start, name, ok := procStat(child)
		if !ok || group != pgid {
			continue
		}
		if bestPID == 0 || start < bestStart {
			bestPID, bestName, bestStart = child, name, start
		}
	}
	return bestPID, bestName
}

// procStat reads the process group, start time and name of a pid out of one
// /proc/<pid>/stat read.
//
// The name is parsed out of the parenthesised field rather than read from
// /proc/<pid>/comm so that a child costs one open instead of two, and it is the
// same string either way.
func procStat(pid int) (group int, start uint64, name string, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, "", false
	}
	s := string(b)

	// Field 2 is the executable name in parentheses and may itself contain
	// spaces or parentheses, so the fields after it are found from the *last*
	// ')' rather than by splitting the whole line.
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return 0, 0, "", false
	}
	name = s[open+1 : close]

	// After the name come state (3), ppid (4), pgrp (5) … starttime (22).
	fields := strings.Fields(s[close+1:])
	const pgrpField, startField = 2, 19 // zero-based, counting from state
	if len(fields) <= startField {
		return 0, 0, "", false
	}
	group, err = strconv.Atoi(fields[pgrpField])
	if err != nil {
		return 0, 0, "", false
	}
	start, err = strconv.ParseUint(fields[startField], 10, 64)
	if err != nil {
		return 0, 0, "", false
	}
	return group, start, name, true
}
