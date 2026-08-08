package session

import "time"

// vtScanner extracts the handful of terminal mode changes that §4.7 needs from
// the raw output stream. It is deliberately not a terminal emulator: it keeps
// three fields, holds no screen, no cursor and no cell attributes, and never
// looks at printable text. It can answer "is something reading a line right
// now" and never "what is on the screen" — the line §14.2 draws.
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
	lastBell       time.Time
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
				v.st = vtGround // BEL terminates — xterm accepts it for all of them
			case 0x1b:
				v.st = vtStringEsc
			}

		case vtStringEsc:
			// ESC \ is ST and ends the string. Anything else means the string
			// was aborted by a fresh escape sequence, and this byte introduces
			// it — the same dispatch as from vtEsc, which is why they share it.
			if b == '\\' {
				v.st = vtGround
			} else {
				v.afterEsc(b)
			}
		}
	}
}

// afterEsc dispatches the byte following an ESC.
func (v *vtScanner) afterEsc(b byte) {
	switch b {
	case '[':
		v.st, v.params, v.tooLong = vtCSI, v.params[:0], false
	case ']', 'P', 'X', '^', '_':
		// OSC, DCS, SOS, PM, APC. Their payloads are arbitrary bytes — a Sixel
		// image or a tmux passthrough can carry anything, including 0x07 — so
		// they are skipped wholesale rather than parsed.
		v.st = vtString
	case 0x1b:
		v.st = vtEsc // ESC ESC: the second one introduces the sequence
	default:
		v.st = vtGround
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
		}
	}
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
