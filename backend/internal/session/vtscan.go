package session

import "time"

// vtScanner extracts the handful of terminal mode changes that §4.7 needs from
// the raw output stream. It is deliberately not a terminal emulator: it keeps
// a handful of booleans, holds no screen, no cursor and no cell attributes, and
// never looks at printable text. It can answer "is something reading a line
// right now" and "has something taken this terminal over", and never "what is
// on the screen" — the line §14.2 draws.
//
// It is a state machine rather than a per-chunk scan because a PTY read ends
// wherever the kernel filled the buffer, which is regularly in the middle of an
// escape sequence: 32 KiB into a redraw, `ESC [ ? 2` arrives in one chunk and
// `0 0 4 h` in the next. Anything that rescans each chunk from scratch misses
// exactly the sequences a busy program emits most.
//
// Not safe for concurrent use: a session's scanner is written by broadcast and
// read by the activity sampler, both under Session.mu.
type vtScanner struct {
	st      vtParse
	params  []byte
	tooLong bool // parameter run exceeded maxParams; ignore this sequence

	altScreen      bool
	bracketedPaste bool
	// mouseTracking and focusReporting are the other two ways a program takes
	// the terminal over. With altScreen they answer "is something drawing its
	// own interface here", which is what tells a program's question from a
	// shell's prompt when the process lookup cannot see either (§4.7).
	mouseTracking  bool
	focusReporting bool
	lastBell       time.Time

	// Semantic prompt marks (OSC 133, §4.7). promptSeen stays false for a shell
	// with no integration, and that is what makes the marks additive: where they
	// are absent, every other rule decides exactly as it did before.
	promptSeen   bool
	promptActive bool
	// lastMark is when the most recent one arrived, which is how a mark that
	// belongs to a program that has just started is told from one left behind
	// by the program it replaced.
	lastMark time.Time

	// osc tracks whether the string sequence currently being skipped is an OSC,
	// and oscBuf holds as much of its payload as a mark can need.
	osc    bool
	oscLen int
	oscBuf [oscPrefixMax]byte
}

type vtParse uint8

const (
	vtGround    vtParse = iota
	vtEsc               // saw ESC
	vtCSI               // inside ESC [ …, collecting parameter/intermediate bytes
	vtString            // inside a string sequence (OSC/DCS/APC/PM/SOS) payload
	vtStringEsc         // inside a string sequence, saw ESC — expecting \ (ST)
)

// maxParams bounds the parameter run. Real mode sequences are a handful of
// bytes; a longer one is malformed or not a mode set, and either way must not
// let a stream grow this buffer without limit.
const maxParams = 64

// altScreenModes are the DEC private modes that switch to the alternate screen
// buffer: the original 47, 1047 (clears on exit) and 1049 (saves and restores
// the cursor as well). Programs use all three, sometimes in the same sequence.
var altScreenModes = map[string]bool{"47": true, "1047": true, "1049": true}

// bracketedPasteMode is DEC private mode 2004. It is the narrow signal for "a
// line editor is reading right now": bash's readline, zsh's ZLE, fish's reader
// and Ink-based apps such as Claude Code all set it before reading a line and
// clear it before running what they read (§4.7).
const bracketedPasteMode = "2004"

// mouseTrackingModes are the DEC private modes that ask for mouse reports: 1000
// (button press and release), 1002 (and drags), 1003 (and every move). The
// encodings a program picks alongside them — 1005, 1006, 1015, 1016 — say how
// a report is spelled, not that one was asked for, and are deliberately not
// here: a program can select an encoding and never enable tracking.
var mouseTrackingModes = map[string]bool{"1000": true, "1001": true, "1002": true, "1003": true}

// focusReportingMode is DEC private mode 1004, where the terminal reports the
// window gaining and losing focus. Only a program that redraws on focus asks
// for it.
const focusReportingMode = "1004"

// promptMarkPrefix introduces the semantic prompt marks: OSC 133 ; A (a prompt
// begins), B (the prompt ends and input is being read), C (the command's output
// begins), D (the command finished).
//
// These are the one signal that survives a change of terminal. The foreground
// lookup stops at our own pty, so a shell inside tmux, a container or an ssh
// session is invisible to it — but the marks are bytes in the stream, and both
// tmux and `docker -t` forward them unchanged (measured, see §4.7). Where the
// inner shell emits them we can tell a prompt from a running command across
// that boundary; where it does not, nothing changes.
const promptMarkPrefix = "133;"

// oscPrefixMax bounds the OSC payload kept for inspection. A mark is decided by
// its first five bytes ("133;D"); a window title or a clipboard write can be
// arbitrarily long and is dropped by the prefix check either way.
const oscPrefixMax = 16

// Feed advances the scanner over one chunk of PTY output.
func (v *vtScanner) Feed(p []byte) {
	for _, b := range p {
		switch v.st {
		case vtGround:
			switch b {
			case 0x1b:
				v.st = vtEsc
			case 0x07:
				// A bell in the ground state is a real bell. The one that ends
				// an OSC string is consumed by vtString and never reaches here,
				// which is the whole reason string sequences are tracked at all:
				// bash and fish set the window title on every prompt, so
				// counting every 0x07 would report a bell several times a
				// minute in an idle session.
				v.lastBell = timeNow()
			}
			// Every other byte is text. UTF-8 cannot hide an ESC or a BEL in a
			// multi-byte sequence — lead bytes are 0xc2-0xf4 and continuation
			// bytes 0x80-0xbf — so scanning byte by byte here is safe, and no
			// decoding is needed.

		case vtEsc:
			v.afterEsc(b)

		case vtCSI:
			switch {
			case b >= 0x40 && b <= 0x7e: // final byte
				if !v.tooLong {
					v.csiFinal(b)
				}
				v.st = vtGround
			case b >= 0x20 && b <= 0x3f: // parameter or intermediate byte
				if len(v.params) < maxParams {
					v.params = append(v.params, b)
				} else {
					v.tooLong = true
				}
			default:
				// A control byte aborts the sequence; the terminal acts on it
				// and returns to ground.
				v.st = vtGround
			}

		case vtString:
			switch b {
			case 0x07:
				v.endString() // BEL terminates — xterm accepts it for all of them
			case 0x1b:
				v.st = vtStringEsc
			default:
				// Only an OSC payload is kept, and only its prefix. Everything
				// else in here — a Sixel image, a tmux passthrough — is skipped
				// as before.
				if v.osc && v.oscLen < len(v.oscBuf) {
					v.oscBuf[v.oscLen] = b
					v.oscLen++
				}
			}

		case vtStringEsc:
			// ESC \ is ST and ends the string. Anything else means the string
			// was aborted by a fresh escape sequence, and this byte introduces
			// it — the same dispatch as from vtEsc, which is why they share it.
			if b == '\\' {
				v.endString()
			} else {
				v.afterEsc(b)
			}
		}
	}
}

// afterEsc dispatches the byte following an ESC.
//
// It is also how a string sequence is abandoned, so it clears the OSC payload
// first: reaching here from vtStringEsc means the string never terminated, and
// half a mark must not be applied by the sequence that interrupted it.
func (v *vtScanner) afterEsc(b byte) {
	v.osc = false
	switch b {
	case '[':
		v.st, v.params, v.tooLong = vtCSI, v.params[:0], false
	case ']':
		// OSC. Its payload is scanned for a prompt mark; anything else in it is
		// dropped by the prefix check in promptMark.
		v.st, v.osc, v.oscLen = vtString, true, 0
	case 'P', 'X', '^', '_':
		// DCS, SOS, PM, APC. Their payloads are arbitrary bytes — a Sixel image
		// or a tmux passthrough can carry anything, including 0x07 — so they
		// are skipped wholesale rather than parsed.
		v.st = vtString
	case 0x1b:
		v.st = vtEsc // ESC ESC: the second one introduces the sequence
	default:
		v.st = vtGround
	}
}

// endString closes a properly terminated string sequence.
func (v *vtScanner) endString() {
	if v.osc {
		v.promptMark()
	}
	v.st, v.osc = vtGround, false
}

// promptMark applies a completed OSC payload. Only OSC 133 is of interest: a
// window title (OSC 0/2, sent by bash and fish on every prompt) or a clipboard
// write (OSC 52) fails the prefix check and changes nothing.
func (v *vtScanner) promptMark() {
	p := v.oscBuf[:v.oscLen]
	if len(p) <= len(promptMarkPrefix) || string(p[:len(promptMarkPrefix)]) != promptMarkPrefix {
		return
	}
	mark := p[len(promptMarkPrefix)]
	if mark == 'A' || mark == 'B' || mark == 'C' || mark == 'D' {
		v.lastMark = timeNow()
	}
	switch mark {
	// A prompt is being drawn (A), or it is drawn and the line editor has it
	// (B). D is here too: the command finished, so whatever we were watching is
	// over and the shell has the terminal back. Reading D as "at the prompt"
	// rather than waiting for the next A is the quiet direction to be wrong in —
	// a shell that emits D and then takes its time never nags in the meantime.
	case 'A', 'B', 'D':
		v.promptSeen, v.promptActive = true, true
	// The command's output begins: from here until D, something other than the
	// shell owns the terminal.
	case 'C':
		v.promptSeen, v.promptActive = true, false
	}
}

// csiFinal applies a completed CSI sequence. Only DEC private mode set/reset
// is of interest; every other sequence is a drawing command and is ignored.
func (v *vtScanner) csiFinal(final byte) {
	if final != 'h' && final != 'l' {
		return
	}
	if len(v.params) == 0 || v.params[0] != '?' {
		return // not a private mode: `ESC [ 4 h` is insert mode, not ours
	}
	set := final == 'h'
	// Parameters are separated by ';' and several modes travel together — vim
	// sends `ESC [ ? 1049 ; 1004 ; 2004 h` as one sequence.
	for _, p := range splitParams(v.params[1:]) {
		switch {
		case altScreenModes[p]:
			v.altScreen = set
		case p == bracketedPasteMode:
			v.bracketedPaste = set
		case mouseTrackingModes[p]:
			v.mouseTracking = set
		case p == focusReportingMode:
			v.focusReporting = set
		}
	}
}

// screenOwned reports whether something has taken the terminal over: it is
// drawing on the alternate screen, or it has asked for mouse or focus reports.
//
// This is the line between a program's question and a shell's prompt, and it is
// the only one that survives a pty boundary. A shell needs none of these — it
// wants a line of text, on the screen it shares with everything before it.
// Anything that draws an interface asks for at least one, and asks for it in
// bytes, which a byte proxy forwards: measured through `docker run -it`, all
// three arrive unchanged (§4.7).
func (v *vtScanner) screenOwned() bool {
	return v.altScreen || v.mouseTracking || v.focusReporting
}

// forgetPromptMarks drops what the marks said, back to "this stream carries
// none". They describe whichever program was in the foreground when they
// arrived; once that program is gone they are a claim about something that is
// no longer there, and a stale claim is worse than no claim — it cannot be
// contradicted by a shell that never emits marks of its own.
func (v *vtScanner) forgetPromptMarks() {
	v.promptSeen, v.promptActive, v.lastMark = false, false, time.Time{}
}

// splitParams splits a parameter run on ';' without allocating a slice of
// strings per byte scanned. Called only for completed mode sequences.
func splitParams(b []byte) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(b); i++ {
		if i == len(b) || b[i] == ';' {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	return out
}

// endsInAltScreen reports whether data leaves the terminal in the alternate
// screen buffer, by replaying the mode switches it contains and keeping the
// last one to win.
//
// A snapshot is the tail of a ring buffer, so it can begin mid-sequence and the
// switch that entered the alternate screen may have been cut off the front. In
// that case this reports false and the separator simply does not reset a mode
// the terminal was in — the same outcome as before this check existed, and
// still no worse than overwriting the history in every ordinary session.
func endsInAltScreen(data []byte) bool {
	var v vtScanner
	v.Feed(data)
	return v.altScreen
}
