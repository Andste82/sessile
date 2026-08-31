package session

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// A session's window title is what the program running in it calls itself:
// `ESC ] 0 ; text BEL` sets the icon name and the title, `ESC ] 2 ; text ST`
// the title alone. It is the sequence xterm defined and every terminal since
// has implemented, the browser one this project ships included — shells emit it
// from PROMPT_COMMAND or precmd, vim from 'titlestring', and long-running tools
// use it to say what they are working on.
//
// It is the companion of the foreground in §4.7, not a replacement for it.
// `command` is read from the kernel and cannot be wrong about which program
// holds the terminal; the title is that program's own account of itself —
// richer, and a claim rather than a fact. It can also go stale, because a
// program that exits without restoring the title leaves its last one standing
// until the shell writes the next prompt. So both are reported and §4.7 stays
// the headline (§4.8).
const (
	// maxTitleBytes bounds one payload, its `0;` prefix included. Real titles
	// are a path or a short sentence; anything longer is either a mistake or
	// not a title at all, and either way the UI has one line for it.
	maxTitleBytes = 256

	oscIconAndTitle = "0"
	oscTitleOnly    = "2"
)

// titleScanner walks a session's live output for those sequences. One per read
// loop and touched from nowhere else, which is what makes it safe without a
// lock of its own.
//
// It is a state machine rather than a per-chunk search for the same reason
// vtParse is: a PTY read ends wherever the kernel filled the buffer, so a title
// arrives split across two chunks often enough to matter — `ESC ] 0 ;` in one
// and the text in the next.
//
// Only string sequences need a state here. Everything else is text this ignores
// and cannot hide an ESC in: CSI parameter and final bytes are all 0x20-0x7e,
// UTF-8 lead bytes are 0xc2-0xf4 and continuations 0x80-0xbf.
type titleScanner struct {
	st      vtParse
	intro   byte   // string-sequence introducer: ']' for OSC
	payload []byte // the first maxTitleBytes of the payload
}

// scan feeds one chunk of PTY output and reports the last title completed in
// it. ok is false when the chunk completed none, which is the ordinary case and
// the one that has to stay cheap.
//
// The last one wins because a chunk regularly carries several: a shell writes
// the title from its prompt hook, so a burst of output that ends in a prompt
// carries at least two, and only the one the terminal is left showing is worth
// reporting.
func (t *titleScanner) scan(data []byte) (title string, ok bool) {
	// The fast path, and the reason this is affordable at all: ordinary output
	// — a build log, a `cat` — contains no escape at all, and every byte of
	// every session passes through here.
	if t.st == vtGround && bytes.IndexByte(data, 0x1b) < 0 {
		return "", false
	}

	for i := 0; i < len(data); i++ {
		b := data[i]
		switch t.st {
		case vtGround:
			if b == 0x1b {
				t.st = vtEsc
			}

		case vtEsc:
			switch b {
			case ']':
				t.st, t.intro, t.payload = vtString, b, t.payload[:0]
			case 'P', 'X', '^', '_':
				// DCS, SOS, PM, APC. None of them carries a title, but their
				// payloads still have to be stepped over rather than scanned:
				// a Sixel image or a tmux passthrough can contain any bytes,
				// including ones that spell a title sequence.
				t.st, t.intro = vtString, b
			case 0x1b:
				t.st = vtEsc // ESC ESC: the second one introduces
			default:
				// Including '[': a CSI sequence needs no state, since nothing
				// between its introducer and its final byte can be an ESC.
				t.st = vtGround
			}

		case vtString:
			switch b {
			case 0x07: // BEL terminates — xterm accepts it for all of them
				if s, isTitle := t.finish(); isTitle {
					title, ok = s, true
				}
				t.st = vtGround
			case 0x1b:
				t.st = vtStringEsc
			default:
				if len(t.payload) < maxTitleBytes {
					t.payload = append(t.payload, b)
				}
				// Past the cap the rest of the payload is dropped rather than
				// the sequence: what a program put in the first 256 bytes is
				// still what it is calling itself.
			}

		case vtStringEsc:
			if b == '\\' { // ST
				if s, isTitle := t.finish(); isTitle {
					title, ok = s, true
				}
				t.st = vtGround
			} else {
				// The string never terminated: the ESC at i-1 abandons it and
				// introduces a sequence of its own, and this byte belongs to
				// that one. An unterminated title sets nothing — half a title
				// is not a title, and there is no telling where it was meant
				// to end.
				t.st = vtEsc
				i-- // re-dispatch b from vtEsc
			}
		}
	}
	return title, ok
}

// finish renders the payload of a completed string sequence, and reports false
// for every sequence that is not setting a title.
//
// OSC 1 is deliberately absent: it sets the icon name, which is the label a
// window manager shows when the window is minimised and not what anyone means
// by the title. xterm.js draws the same line — its onTitleChange fires for 0
// and 2 — and a session should read the same in this list as it would in a
// terminal beside it.
func (t *titleScanner) finish() (string, bool) {
	if t.intro != ']' {
		return "", false
	}
	ps, text, found := bytes.Cut(t.payload, []byte(";"))
	if !found {
		return "", false
	}
	switch string(ps) {
	case oscIconAndTitle, oscTitleOnly:
		// An empty payload is a program clearing its title, and is passed on as
		// the empty string it is.
		return sanitizeTitle(text), true
	}
	return "", false
}

// sanitizeTitle makes a payload safe to put in a JSON field and in one line of
// a list.
//
// Control characters are dropped rather than rendered: a program that put a tab
// or a newline in a title meant a space at most, and a C0 byte reaching the
// dashboard is a byte that arrived from a pty and was never displayed as text.
// Invalid UTF-8 goes the same way — the payload cap can fall in the middle of a
// multi-byte rune, and half of one is not text any more.
func sanitizeTitle(b []byte) string {
	var out strings.Builder
	out.Grow(len(b))
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		switch {
		case r == utf8.RuneError && size == 1: // an invalid or truncated byte
		case r < 0x20 || r == 0x7f: // C0 controls
		default:
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}

// setTitle records a title the read loop scanned out of a session's output.
//
// It publishes nothing. The foreground sampler carries the change out with the
// rest of the derived state one tick later (§4.7), which costs a second of
// latency on a dashboard label and bounds a program that repaints its title in
// a progress loop to one event per session per second. Doing it here would put
// the event fan-out on the PTY hot path at whatever rate that program writes.
func (s *Session) setTitle(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t != s.title {
		s.title, s.titleDirty = t, true
	}
}
