package session

import (
	"path/filepath"
	"strings"
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
	// fgNestedShell: a shell other than the session's own — `bash` typed at the
	// prompt, a login shell, anything the operator allowed as a shell. It is at
	// its prompt under exactly the same evidence the session's own shell is, so
	// it takes the same branch; the pid check cannot see it because the pid is
	// only ever the shell we started ourselves.
	fgNestedShell
	fgProgram
)

// knownShells are the foreground names that make a process a shell rather than
// a program.
//
// A name table is the thing §4.7 avoids, so this one is bounded by what the
// section is actually about: shell-versus-program is the axis every rule turns
// on, and the session's own shell is recognised by pid only because we happened
// to start it. This adds no knowledge of any *program* — a new TUI still needs
// no code. Shells the operator configured are matched too, so `--shells nu`
// works without touching this.
var knownShells = map[string]bool{
	"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true,
	"fish": true, "ksh": true, "mksh": true, "csh": true, "tcsh": true,
}

// chainSeparator joins the foreground chain for display, and maxChainShown is
// how many of its links are worth showing: the label is a glance, not a process
// tree, and the two ends are what carry the meaning.
const (
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

// isShellName reports whether a foreground process name is a shell.
func (m *Manager) isShellName(name string) bool {
	if name == "" {
		return false
	}
	if knownShells[name] {
		return true
	}
	for _, sh := range m.shells {
		if filepath.Base(sh) == name {
			return true
		}
	}
	return false
}

// activityInput is everything classify is allowed to look at.
type activityInput struct {
	previous   Activity
	lastOutput time.Time
	// sustainedOutput is true when output arrived in this sample interval and
	// in the one before it — roughly two seconds of continuous writing, which
	// a spinner produces and an occasional repaint does not.
	sustainedOutput bool
	bracketedPaste  bool
	lastBell        time.Time
	fg              fgKind
	// promptMarks is true once the stream has carried an OSC 133 mark, and
	// atPrompt is what the most recent one said. The pair is deliberately two
	// fields: "no marks at all" is a different answer from "not at the prompt",
	// and only the first may fall back to the foreground lookup.
	promptMarks bool
	atPrompt    bool
}

// classify turns a snapshot of the three signals into a state. It is pure — no
// clock, no locks, no syscalls — so the rule table in §4.7 is a unit test.
//
// The order matters: each rule assumes the ones above it did not fire.
func classify(now time.Time, in activityInput) Activity {
	quiet := now.Sub(in.lastOutput)

	// 0. A prompt mark answers the question the foreground lookup exists to
	// answer — is a shell at its prompt — and answers it through a wrapper with
	// a pty of its own, which the lookup cannot see past. That is the whole
	// gain: a shell in a container says so itself.
	//
	// It outranks the lookup in one direction only. "At a prompt" wins, because
	// nothing else can know it. "A command is running" does not: it is true of a
	// nested shell too — an interactive `bash` *is* a command its parent ran —
	// and there the kernel seeing a shell in the foreground is the better
	// evidence. So that half only fills in a foreground we could not identify,
	// and then leaves rules 1-6 to decide. A mark never says what a program
	// wants, which is what keeps Claude Code in a container reporting waiting.
	if in.promptMarks {
		if in.atPrompt {
			if quiet < busyWindow {
				return ActivityBusy // rule 1 still comes first
			}
			return ActivityIdle
		}
		if in.fg == fgUnknown {
			in.fg = fgProgram
		}
	}

	// Leaving "waiting" takes more than a byte. A program sitting at its prompt
	// still repaints — a spinner, a hint line, a cursor — and treating any
	// output as work starting made the indicator drop out of waiting for four
	// seconds at a time, several times a minute, against a real Claude Code
	// session. So while the conditions that produced the state still hold — a
	// program, still reading a line — only sustained output moves it.
	//
	// Deliberately asymmetric: entering waiting needs positive evidence and a
	// dwell, leaving it needs positive evidence too. A state that flickers is
	// worse than one that is a second late.
	if in.previous == ActivityWaiting && in.fg == fgProgram &&
		in.bracketedPaste && !in.sustainedOutput {
		return ActivityWaiting
	}

	switch {
	// 1. Bytes are arriving. Whatever else is true, the session is working.
	case quiet < busyWindow:
		return ActivityBusy

	// 2. Something is reading a line and it is not a program — the session's own
	// shell, a shell started inside it, or a foreground process we could not
	// identify, where a prompt is the likelier reading and the quieter one to be
	// wrong about. A nested shell belongs here rather than under rule 4: with a
	// line editor open it is at a prompt, and without one it is running a script
	// (`bash deploy.sh` keeps the name and loses the mode), which is work.
	case in.bracketedPaste && in.fg != fgProgram:
		return ActivityIdle

	// 3. A program is reading a line and has been silent long enough that this
	// is a question rather than a pause. The only rule that claims attention
	// from the output stream.
	case in.bracketedPaste && quiet >= waitQuiet:
		return ActivityWaiting

	// 4. The shell owns the terminal but never announced a line editor — dash
	// has no readline at all. At the prompt regardless.
	//
	// The session's own shell only, not a nested one: this rule is safe here
	// because a command the session's shell runs gets its own process group and
	// so is never mistaken for it, and that is exactly what does not hold one
	// level down.
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
	// The leader is our own shell and nothing is running under it. The second
	// half matters: an interactive shell gives every job a group of its own, so
	// a child still inside the group means job control is off and that child,
	// not the shell, has the terminal.
	case fg.PID == s.PID && len(fg.Chain) <= 1:
		kind = fgShell
	// Everything else is decided by the innermost process, which is the one
	// actually running — `bash deploy.sh` is a program at work, not a shell at
	// a prompt, and only the leaf says so.
	case m.isShellName(fg.Leaf()):
		kind = fgNestedShell
	default:
		kind = fgProgram
	}
	command := commandLabel(fg)
	cwd := relativeToRoot(m.root, fg.Cwd)

	s.mu.Lock()
	defer s.mu.Unlock()
	// A different program is in the foreground than the one the marks were about,
	// so what they said concerns something that has exited. The case this exists
	// for: a container shell with integration draws a prompt, says so, and is
	// then left behind — the session's own shell emits no marks of its own, so
	// nothing would ever contradict it, and a program started later would be
	// reported as sitting at a prompt that is not there.
	//
	// Two guards. An empty name is a lookup that failed, not a change, and must
	// not discard a live wrapper's state on a hiccup. And a mark newer than one
	// sampling window may well have come from the program that just took over —
	// a container's first prompt can easily land before the sampler notices the
	// container — so only marks older than the change are treated as its debris.
	if command != "" && command != s.fgCommand &&
		now.Sub(s.vt.lastMark) > activitySampleInterval {
		s.vt.forgetPromptMarks()
	}
	next := classify(now, activityInput{
		previous:        s.activity,
		lastOutput:      s.LastActivity,
		sustainedOutput: s.sampleOutputRunLocked(),
		bracketedPaste:  s.vt.bracketedPaste,
		lastBell:        s.vt.lastBell,
		fg:              kind,
		promptMarks:     s.vt.promptSeen,
		atPrompt:        s.vt.promptActive,
	})
	changed := next != s.activity || command != s.fgCommand || cwd != s.fgCwd
	if next != s.activity {
		s.activity = next
		// Only a real transition restarts the clock: the dashboard shows how
		// long this state has held, and re-stamping it every second would leave
		// it reading "0s" forever.
		s.activitySince = now
	}
	s.fgCommand, s.fgCwd = command, cwd
	return s.infoLocked(), changed
}

// sampleOutputRunLocked advances the consecutive-output counter and reports
// whether output has now arrived in two samples running.
//
// It counts sample intervals rather than bytes on purpose. "Did output keep
// coming" is a property of any program; "did more than N bytes arrive" is a
// threshold that would have to be tuned per program, which is exactly what
// §4.7 exists to avoid.
func (s *Session) sampleOutputRunLocked() bool {
	if s.outputBytes != s.sampledBytes {
		s.sampledBytes = s.outputBytes
		s.outputRun++
	} else {
		s.outputRun = 0
	}
	return s.outputRun >= 2
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
