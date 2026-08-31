package session

import (
	"bytes"
	"strings"
)

// A terminal holds state that is not on its screen: whether the alternate
// screen is up, whether a program asked for mouse reports and in which
// encoding, whether the line editor is given bracketed paste, what the arrow
// keys send. A program switches these on for itself when it starts and off
// again when it exits, with a DEC private mode set or reset.
//
// None of it survives in the ring buffer, and that is the bug this file exists
// for. The buffer is content, and it is bounded: a program that repaints —
// htop, vim, a build with a progress bar — pushes its own opening
// `ESC [ ? 1049 h ESC [ ? 1002 h` off the front long before anyone switches
// sessions. What a later attach replays is then a stream of repaints that
// switches nothing on. The frontend builds a fresh xterm for every session
// switch (TerminalPage keys TerminalView on the session id), so that terminal
// sits on the normal screen with mouse reporting off while the program on the
// other end believes both are on: scrolling a TUI stops working, reopening the
// tab replays the same truncated snapshot and does not help, and only a window
// resize brings it back — SIGWINCH makes the program redraw and re-issue its
// setup.
//
// tmux does not have this problem, because its state does not live in the
// stream: it parses output into a screen model with a mode bitset beside it,
// and writes those modes out to whichever client attaches. This is that half of
// tmux's design and only that half. The other half — the character grid — stays
// out, per §14.2: nothing here holds a cell, a cursor position or an attribute,
// and the browser remains the only terminal emulator in the system.
//
// The zero value is a terminal in its reset state, which is what a fresh xterm
// is. Only what differs from it is written on attach, so an ordinary shell
// session — the common case — carries a preamble of nothing.
type termModes struct {
	// alt is the DEC private mode that put the terminal on the alternate
	// screen: "47", "1047" or "1049", empty for the normal screen. The
	// spelling is kept rather than normalised because the three differ in what
	// else they do — 1049 saves and restores the cursor, 1047 clears on exit —
	// and the right one to re-issue is the one the program chose.
	alt string

	// mouse is the tracking mode a program asked for ("1000", "1002", "1003")
	// and mouseEnc the encoding it asked reports to arrive in ("1005", "1006",
	// "1015", "1016"). Both are single values with the last set winning, which
	// is how xterm.js models them: it keeps one active protocol and one active
	// encoding, and a reset of any of them clears the lot.
	mouse    string
	mouseEnc string

	bracketedPaste bool // 2004 — the shell's line editor asks for this
	focusReport    bool // 1004
	appCursor      bool // DECCKM, private mode 1: arrows send SS3 instead of CSI
	appKeypad      bool // DECKPAM (`ESC =`) / DECKPNM (`ESC >`)

	// The two that are on in a reset terminal, so these record having been
	// switched off. A TUI hides the cursor for as long as it draws its own.
	cursorHidden bool // 25
	wrapOff      bool // 7 (DECAWM)
}

// modeScanner tracks those modes across a session's live output. One per read
// loop and touched from nowhere else, exactly like titleScanner (§4.8) and for
// the same reason: the state it carries is escape-sequence state, and a PTY
// read ends wherever the kernel filled the buffer, regularly in the middle of a
// sequence.
type modeScanner struct {
	st      vtParse
	params  []byte // CSI parameter/intermediate run
	tooLong bool
	modes   termModes
}

// scan feeds one chunk of PTY output and reports the mode state after it,
// together with whether the chunk changed anything. Nearly every chunk changes
// nothing, and that is the case that has to stay cheap.
func (m *modeScanner) scan(data []byte) (termModes, bool) {
	// The fast path, and the same one titleScanner takes: ordinary output — a
	// build log, a `cat` — contains no escape at all, and every byte of every
	// session passes through here.
	if m.st == vtGround && bytes.IndexByte(data, 0x1b) < 0 {
		return m.modes, false
	}

	before := m.modes
	for _, b := range data {
		switch m.st {
		case vtGround:
			if b == 0x1b {
				m.st = vtEsc
			}
			// Everything else is text or a C0 control. UTF-8 cannot hide an
			// ESC — lead bytes are 0xc2-0xf4, continuations 0x80-0xbf — so a
			// byte-wise walk needs no decoding.

		case vtEsc:
			switch b {
			case '[':
				m.st, m.params, m.tooLong = vtCSI, m.params[:0], false
			case ']', 'P', 'X', '^', '_':
				// OSC, DCS, SOS, PM, APC. Their payloads are stepped over
				// rather than scanned: a Sixel image or a tmux passthrough can
				// carry any bytes at all, including ones that spell a mode set.
				// An ESC is the exception, and vtStringEsc below handles it the
				// way a terminal does — Sixel data is printable and tmux
				// passthrough doubles the ESCs it wraps, so an ESC arriving
				// here is a string nobody terminated rather than payload.
				m.st = vtString
			case '=': // DECKPAM — application keypad
				m.modes.appKeypad = true
				m.st = vtGround
			case '>': // DECKPNM — numeric keypad
				m.modes.appKeypad = false
				m.st = vtGround
			case 0x1b:
				m.st = vtEsc // ESC ESC: the second one introduces
			default:
				m.st = vtGround
			}

		case vtCSI:
			switch {
			case b >= 0x40 && b <= 0x7e: // final byte
				// DECSET (h) and DECRST (l) with a '?' prefix is the whole of
				// what this reads. Every other CSI draws, moves or sets a
				// colour, and none of that is state a client has to be primed
				// with — the replay redraws it.
				if !m.tooLong && (b == 'h' || b == 'l') &&
					len(m.params) > 0 && m.params[0] == '?' {
					for _, ps := range splitParams(m.params[1:]) {
						m.apply(ps, b == 'h')
					}
				}
				m.st = vtGround
			case b >= 0x20 && b <= 0x3f: // parameter or intermediate byte
				if len(m.params) < maxParams {
					m.params = append(m.params, b)
				} else {
					// Longer than any real mode set; kept unexamined.
					m.tooLong = true
				}
			default:
				// A control byte aborts the sequence; the terminal acts on it
				// and returns to ground.
				m.st = vtGround
			}

		case vtString:
			switch b {
			case 0x07: // BEL terminates — xterm accepts it for all of them
				m.st = vtGround
			case 0x1b:
				m.st = vtStringEsc
			}

		case vtStringEsc:
			if b == '\\' { // ST
				m.st = vtGround
			} else {
				// The string never terminated: that ESC abandons it and
				// introduces a sequence of its own, which this byte belongs to.
				switch b {
				case '[':
					m.st, m.params, m.tooLong = vtCSI, m.params[:0], false
				case ']', 'P', 'X', '^', '_':
					m.st = vtString
				case '=':
					m.modes.appKeypad = true
					m.st = vtGround
				case '>':
					m.modes.appKeypad = false
					m.st = vtGround
				default:
					m.st = vtGround
				}
			}
		}
	}
	return m.modes, m.modes != before
}

// apply records one DEC private mode set or reset.
//
// The alternate-screen and mouse modes are single-valued rather than a bit
// each, because that is how the terminal on the other end models them: xterm.js
// keeps one active mouse protocol and one active encoding, so a reset of any of
// the three tracking modes turns tracking off whichever one was on. Re-issuing
// them as independent bits could hand a client two tracking modes it would then
// resolve by their arrival order rather than by the one the program meant.
func (m *modeScanner) apply(ps string, set bool) {
	switch ps {
	case "47", "1047", "1049":
		if set {
			m.modes.alt = ps
		} else {
			m.modes.alt = ""
		}
	case "1000", "1002", "1003":
		if set {
			m.modes.mouse = ps
		} else {
			m.modes.mouse = ""
		}
	case "1005", "1006", "1015", "1016":
		if set {
			m.modes.mouseEnc = ps
		} else {
			m.modes.mouseEnc = ""
		}
	case "2004":
		m.modes.bracketedPaste = set
	case "1004":
		m.modes.focusReport = set
	case "1":
		m.modes.appCursor = set
	case "25":
		m.modes.cursorHidden = !set
	case "7":
		m.modes.wrapOff = !set
	}
	// Everything else is a mode the browser terminal either owns itself or does
	// not implement, and re-issuing it would be guessing at a default.
}

// preamble renders the modes as the sequences that put a freshly built terminal
// into this state. It is written ahead of the replay on attach; anything the
// replay still contains is applied after it and wins, so a snapshot that did
// survive intact is unaffected by this.
//
// Order is not arbitrary. The alternate screen comes first because 1049 saves
// the cursor as it switches, and the tracking mode comes before its encoding
// because a program that sets both sets them that way round.
//
// Returns nil when the terminal is in its reset state, which is what an
// ordinary shell session is: no allocation and no bytes on the wire.
func (t termModes) preamble() []byte {
	var b strings.Builder
	set := func(ps string) { b.WriteString("\x1b[?" + ps + "h") }
	reset := func(ps string) { b.WriteString("\x1b[?" + ps + "l") }

	if t.alt != "" {
		set(t.alt)
	}
	if t.mouse != "" {
		set(t.mouse)
	}
	if t.mouseEnc != "" {
		set(t.mouseEnc)
	}
	if t.bracketedPaste {
		set("2004")
	}
	if t.focusReport {
		set("1004")
	}
	if t.appCursor {
		set("1")
	}
	if t.appKeypad {
		b.WriteString("\x1b=") // DECKPAM, which has no private-mode spelling
	}
	if t.cursorHidden {
		reset("25")
	}
	if t.wrapOff {
		reset("7")
	}
	if b.Len() == 0 {
		return nil
	}
	return []byte(b.String())
}

// setModes records the mode state the read loop scanned out of a session's
// output, so attach can prime a new client with it.
//
// Called only when a chunk actually changed something, which is a handful of
// times in a session's life — a program starting and a program exiting. Taking
// the session lock per chunk would put it on the PTY hot path for nothing.
func (s *Session) setModes(m termModes) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modes = m
}
