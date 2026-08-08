package session

import (
	"time"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// Activity is a running session's derived state (PROJECT_PLAN.md §4.7). It says
// what the session is doing, not what is on its screen — nothing here reads
// output content, and adding support for a new program means adding no code.
type Activity string

const (
	// ActivityBusy: output is flowing, or a program is running that has simply
	// gone quiet. The default for "something is happening".
	ActivityBusy Activity = "busy"
	// ActivityWaiting: a program that is not the shell has stopped producing
	// output and is reading a line, or rang the bell. This is the one state
	// that asks the user for something.
	ActivityWaiting Activity = "waiting"
	// ActivityIdle: the shell is sitting at its prompt.
	ActivityIdle Activity = "idle"
)

// Thresholds, all measured against real programs rather than picked (§4.7).
const (
	// busyWindow is how recently bytes must have arrived to count as working.
	// htop repaints every 1.5 s, which is the slowest cadence a program can
	// plausibly call "live".
	busyWindow = 1500 * time.Millisecond
	// waitQuiet is how long a program must have been silent before its prompt
	// is reported. Without this dwell, anything that redraws more slowly than
	// busyWindow oscillates between busy and waiting.
	waitQuiet = 2500 * time.Millisecond
	// bellWindow is how long a bell keeps asking for attention.
	bellWindow = 60 * time.Second
	// activitySampleInterval is how often the foreground process is looked up.
	// Per session per second: two small /proc reads and an ioctl.
	activitySampleInterval = time.Second
)

// fgKind is what the terminal's foreground process group turned out to be.
type fgKind uint8

const (
	// fgUnknown: the lookup failed or the platform cannot answer (§4.7). It is
	// its own case rather than being folded into fgProgram because the two lead
	// to opposite answers, and guessing "a program" would make every shell
	// prompt on a non-Linux build report as waiting.
	fgUnknown fgKind = iota
	fgShell
	fgProgram
)

// activityInput is everything classify is allowed to look at.
type activityInput struct {
	lastOutput     time.Time
	bracketedPaste bool
	lastBell       time.Time
	fg             fgKind
}

// classify turns a snapshot of the three signals into a state. It is pure — no
// clock, no locks, no syscalls — so the rule table in §4.7 is a unit test.
//
// The order matters: each rule assumes the ones above it did not fire.
func classify(now time.Time, in activityInput) Activity {
	quiet := now.Sub(in.lastOutput)
	switch {
	// 1. Bytes are arriving. Whatever else is true, the session is working.
	case quiet < busyWindow:
		return ActivityBusy

	// 2. Something is reading a line and it is not a program — either the
	// shell's own prompt, or a foreground process we could not identify, where
	// a prompt is the likelier reading and the quieter one to be wrong about.
	case in.bracketedPaste && in.fg != fgProgram:
		return ActivityIdle

	// 3. A program is reading a line and has been silent long enough that this
	// is a question rather than a pause. The only rule that claims attention
	// from the output stream.
	case in.bracketedPaste && quiet >= waitQuiet:
		return ActivityWaiting

	// 4. The shell owns the terminal but never announced a line editor — dash
	// has no readline at all. At the prompt regardless.
	case in.fg == fgShell:
		return ActivityIdle

	// 5. A bell is a program asking to be looked at, whatever else it is doing.
	case !in.lastBell.IsZero() && now.Sub(in.lastBell) < bellWindow:
		return ActivityWaiting

	// 6. A program is running, silent, and not visibly prompting: a compile, a
	// download, a test suite. Working, not asking — this default is what keeps
	// a quiet `go build` from being reported as a question, and it is why
	// cursor visibility is not among the inputs. It is the terminal's resting
	// state, so it would send every one of these here to rule 3; Claude Code
	// inverts it anyway by hiding the cursor while it waits.
	default:
		return ActivityBusy
	}
}

// sampleActivity refreshes every session's derived state. One goroutine for the
// whole manager rather than one per session: the work is a syscall and two
// small reads each, and a single loop gives the event fan-out one place to
// publish from.
func (m *Manager) sampleActivity() {
	m.mu.RLock()
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.RUnlock()

	now := timeNow()
	for _, s := range list {
		if info, changed := m.sampleSession(s, now); changed {
			m.publishSession(info)
		}
	}
}

// sampleSession updates one session and reports whether anything a client can
// see moved.
func (m *Manager) sampleSession(s *Session, now time.Time) (Info, bool) {
	pty := s.runningPTY()
	if pty == nil {
		return s.clearActivity()
	}

	// Outside the lock: this is the slow part, and broadcast must never wait on
	// a /proc read. Both fields it needs are fixed for a session's lifetime —
	// a restart builds a new Session rather than re-pointing this one.
	fg := pty.Foreground()
	kind := fgUnknown
	switch {
	case fg.PID <= 0:
	case fg.PID == s.PID:
		kind = fgShell
	default:
		kind = fgProgram
	}
	cwd := relativeToRoot(m.root, fg.Cwd)

	s.mu.Lock()
	defer s.mu.Unlock()
	next := classify(now, activityInput{
		lastOutput:     s.LastActivity,
		bracketedPaste: s.vt.bracketedPaste,
		lastBell:       s.vt.lastBell,
		fg:             kind,
	})
	changed := next != s.activity || fg.Name != s.fgCommand || cwd != s.fgCwd
	if next != s.activity {
		s.activity = next
		// Only a real transition restarts the clock: the dashboard shows how
		// long this state has held, and re-stamping it every second would leave
		// it reading "0s" forever.
		s.activitySince = now
	}
	s.fgCommand, s.fgCwd = fg.Name, cwd
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

// clearActivity drops the derived state of a session that has stopped, so a
// dead session cannot keep showing the program it was running when it died.
func (s *Session) clearActivity() (Info, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activity == "" && s.fgCommand == "" && s.fgCwd == "" {
		return s.infoLocked(), false
	}
	s.activity, s.fgCommand, s.fgCwd = "", "", ""
	s.activitySince = timeNow()
	return s.infoLocked(), true
}

// activityLoop samples until the manager shuts down.
func (m *Manager) activityLoop() {
	t := time.NewTicker(activitySampleInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.sampleActivity()
		}
	}
}
