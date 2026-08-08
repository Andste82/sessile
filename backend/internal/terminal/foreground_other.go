//go:build !linux

package terminal

// Foreground reports nothing off Linux.
//
// TIOCGPGRP itself is portable — macOS and the BSDs answer it on a pty master
// too — but turning a process group id into a name and a working directory
// needs /proc, and everywhere else needs a different mechanism per platform
// (sysctl KERN_PROC on the BSDs, proc_pidinfo on macOS). tmux carries one
// osdep file per system for exactly this.
//
// Linux is what sessile ships on. A macOS development build keeps the activity
// classification that runs on output cadence and terminal modes; what it loses
// is the ability to tell a shell prompt from a program's prompt, since both
// look like "something is reading a line" once the foreground process is
// unknown. classify resolves that ambiguity towards idle, so such a build never
// claims a session wants attention — see the fgUnknown case in activity.go.
func (p *PTY) Foreground() Foreground { return Foreground{} }
