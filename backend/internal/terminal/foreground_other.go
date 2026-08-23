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
// Linux is what sessile ships on. On a macOS development build a session's
// `command` and `cwd` are simply empty, and the dashboard card shows the name
// and the shell alone — the same as a session whose foreground could not be
// read on Linux either (§4.7).
func (p *PTY) Foreground() Foreground { return Foreground{} }
