package session

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Andste82/sessile/backend/internal/hostops"
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

	// sshForegroundTimeout bounds one SSH session's foreground lookup — a
	// remote `ps` plus reading back its own recorded PID (§4.10), unlike
	// local's plain syscalls. Generous relative to foregroundSampleInterval
	// so a slow but working link still gets an answer, but bounded so a
	// genuinely stuck connection can't leak a goroutine per sample forever
	// (sampleForeground waits for every session's sample before its next
	// tick can start new ones).
	sshForegroundTimeout = 3 * time.Second
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

// sampleForeground refreshes every session's foreground. Concurrently, one
// goroutine per session rather than a single serial loop: that was fine when
// every sample was a local syscall and two small reads, but an SSH session's
// sample (§4.10) is a remote round trip, and one slow or unresponsive link
// must not delay every other session's — local or SSH — refresh behind it.
// Waiting for the whole batch before returning (rather than firing and
// forgetting) bounds how many samples can ever be in flight at once to one
// per session, however far behind a stuck link falls: time.Ticker drops a
// tick it has no reader for rather than queuing it, so a slow batch just
// means the next one starts late, not that they pile up.
func (m *Manager) sampleForeground() {
	m.mu.RLock()
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.RUnlock()

	var wg sync.WaitGroup
	for _, s := range list {
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			if info, changed := m.sampleSession(s); changed {
				m.publishSession(info)
			}
		}(s)
	}
	wg.Wait()
}

// sampleSession updates one session and reports whether anything a client can
// see moved.
func (m *Manager) sampleSession(s *Session) (Info, bool) {
	backend := s.runningBackend()
	if backend == nil {
		return s.clearDerived()
	}

	// Outside the lock: this is the slow part, and broadcast must never wait on
	// a /proc read (or, for SSH, a remote round trip). Both fields it needs are
	// fixed for a session's lifetime — a restart builds a new Session rather
	// than re-pointing this one.
	var fg terminal.Foreground
	if s.TargetType == TargetSSH {
		fg = sshForeground(s.hostOps)
	} else {
		fg = backend.Foreground()
	}
	command := commandLabel(fg)
	cwd := relativeToRoot(m.root, fg.Cwd)

	s.mu.Lock()
	defer s.mu.Unlock()
	changed := command != s.fgCommand || cwd != s.fgCwd || s.titleDirty
	s.fgCommand, s.fgCwd, s.titleDirty = command, cwd, false
	return s.infoLocked(), changed
}

// sshForeground reads an SSH session's foreground process the way
// hostops.HostSession.Foreground finds it (§4.10) — /proc's tpgid on the
// session's own root process, the same kernel fact TIOCGPGRP gives a local
// pty, read a different way because there's no TIOCGPGRP over SSH. Cwd is
// left empty: unlike command, there's no single cheap remote call for it yet,
// and the dashboard already treats an empty cwd as "unknown" (§6, §7), same
// as it always has for an SSH session.
func sshForeground(hostOps *hostops.HostSession) terminal.Foreground {
	if hostOps == nil {
		return terminal.Foreground{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), sshForegroundTimeout)
	defer cancel()
	name, chain, ok := hostOps.Foreground(ctx)
	if !ok {
		return terminal.Foreground{}
	}
	return terminal.Foreground{Name: name, Chain: chain}
}

// runningBackend returns the session's Backend, or nil if it is no longer
// running.
func (s *Session) runningBackend() Backend {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != StatusRunning {
		return nil
	}
	return s.backend
}

// clearDerived drops the derived state of a session that has stopped, so a dead
// session cannot keep showing the program it was running when it died — nor the
// title that program left behind, which nothing will overwrite now that there
// is no shell to reach the next prompt.
func (s *Session) clearDerived() (Info, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fgCommand == "" && s.fgCwd == "" && s.title == "" {
		return s.infoLocked(), false
	}
	s.fgCommand, s.fgCwd, s.title, s.titleDirty = "", "", "", false
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
