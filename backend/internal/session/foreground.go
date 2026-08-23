package session

import (
	"strings"
	"time"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// The foreground of a running session: which program holds its terminal, and
// where that program is (PROJECT_PLAN.md §4.7).
//
// Both are facts read from the kernel and reported unchanged. This file
// deliberately draws no conclusion from them — whether a session wants
// something from you is a question the terminal cannot answer, and a wrong
// answer to it is worse than none.
const (
	// foregroundSampleInterval is how often the foreground process is looked
	// up. Per session per second: two small /proc reads and an ioctl.
	foregroundSampleInterval = time.Second

	// chainSeparator joins the foreground chain for display, and maxChainShown
	// is how many of its links are worth showing: the label is a glance, not a
	// process tree, and the two ends are what carry the meaning.
	chainSeparator = " › "
	maxChainShown  = 3
)

// commandLabel renders the foreground chain: "bash › ping" for a script that
// runs ping, and just "claude" for a program started directly.
//
// A chain deeper than the cap keeps its ends — the outermost says what was
// started, the innermost what is running, and everything between them is
// scaffolding a build system put there.
func commandLabel(fg terminal.Foreground) string {
	switch {
	case len(fg.Chain) == 0:
		return fg.Name
	case len(fg.Chain) <= maxChainShown:
		return strings.Join(fg.Chain, chainSeparator)
	default:
		return fg.Chain[0] + chainSeparator + "…" + chainSeparator + fg.Leaf()
	}
}

// sampleForeground refreshes every session's foreground. One goroutine for the
// whole manager rather than one per session: the work is a syscall and two
// small reads each, and a single loop gives the event fan-out one place to
// publish from.
func (m *Manager) sampleForeground() {
	m.mu.RLock()
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.RUnlock()

	for _, s := range list {
		if info, changed := m.sampleSession(s); changed {
			m.publishSession(info)
		}
	}
}

// sampleSession updates one session and reports whether anything a client can
// see moved.
func (m *Manager) sampleSession(s *Session) (Info, bool) {
	pty := s.runningPTY()
	if pty == nil {
		return s.clearForeground()
	}

	// Outside the lock: this is the slow part, and broadcast must never wait on
	// a /proc read. Both fields it needs are fixed for a session's lifetime —
	// a restart builds a new Session rather than re-pointing this one.
	fg := pty.Foreground()
	command := commandLabel(fg)
	cwd := relativeToRoot(m.root, fg.Cwd)

	s.mu.Lock()
	defer s.mu.Unlock()
	changed := command != s.fgCommand || cwd != s.fgCwd
	s.fgCommand, s.fgCwd = command, cwd
	return s.infoLocked(), changed
}

// runningPTY returns the session's PTY, or nil if it is no longer running.
func (s *Session) runningPTY() *terminal.PTY {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != StatusRunning {
		return nil
	}
	return s.pty
}

// clearForeground drops the derived state of a session that has stopped, so a
// dead session cannot keep showing the program it was running when it died.
func (s *Session) clearForeground() (Info, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fgCommand == "" && s.fgCwd == "" {
		return s.infoLocked(), false
	}
	s.fgCommand, s.fgCwd = "", ""
	return s.infoLocked(), true
}

// foregroundLoop samples until the manager shuts down.
func (m *Manager) foregroundLoop() {
	t := time.NewTicker(foregroundSampleInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.sampleForeground()
		}
	}
}
