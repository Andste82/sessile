package session

import "bytes"

// sanitizeReplay removes the terminal *queries* from a ring-buffer snapshot.
//
// A snapshot is raw PTY output, and PTY output contains questions as well as
// drawing: `ESC [ c` asks the terminal what it is, `ESC [ 6 n` where the cursor
// is, `ESC ] 11 ; ?` what the background colour is. Live, that is a
// conversation — the program asks, the emulator answers on the input side, the
// program reads its answer. Replayed, only half of it is left: the program that
// asked is long gone, and the answer the emulator dutifully sends lands in
// whatever is reading the PTY now, which is a fresh shell sitting at a prompt.
//
// That is the whole of the "session comes back with control characters typed
// into it" bug. Claude Code emits `ESC [ > 0 q ESC [ c` when it starts; the
// bytes sit in the ring buffer; every attach replays them; xterm.js answers
// each replay with `ESC [ ? 1 ; 2 c`; the shell's line editor swallows the
// `ESC [ ?` it cannot bind and leaves `1;2c` on the command line. One per
// attach, and attaches are cheap — a reload, a second tab, every reconnect —
// so the prompt collects a row of them. Restart re-seeds the new buffer from
// the same snapshot, which is how one query outlives any number of restarts.
//
// Dropping the queries costs the replay nothing: a query draws no cell. Only
// the replay is filtered. Live output must keep them, or a running program
// would wait forever for an answer to a question we deleted.
//
// Not handled, deliberately: the 8-bit C1 introducers (0x9b for CSI, 0x9d for
// OSC). In a UTF-8 stream those bytes are continuation bytes of ordinary text,
// and no program that has to survive a UTF-8 locale emits them — the same call
// vtScanner makes.
func sanitizeReplay(data []byte) []byte {
	var (
		out     []byte // built lazily: an unfiltered replay is returned as it came
		dropped bool
		kept    int // data[:kept] is already accounted for in out

		st        vtParse
		seqStart  int    // index of the ESC that introduced the current sequence
		params    []byte // CSI parameter/intermediate run
		tooLong   bool
		intro     byte   // string-sequence introducer: ']' for OSC, 'P' for DCS
		payload   []byte // the first maxParams payload bytes
		truncated bool   // the payload was longer than that
	)

	drop := func(end int) {
		out = append(out, data[kept:seqStart]...)
		kept = end + 1
		dropped = true
	}

	for i := 0; i < len(data); i++ {
		b := data[i]
		switch st {
		case vtGround:
			if b == 0x1b {
				st, seqStart = vtEsc, i
			}
			// Everything else is text or a C0 control, and neither can hide an
			// escape: UTF-8 lead bytes are 0xc2-0xf4 and continuations
			// 0x80-0xbf, so a byte-wise scan needs no decoding.

		case vtEsc:
			switch b {
			case '[':
				st, params, tooLong = vtCSI, params[:0], false
			case ']', 'P':
				// OSC and DCS are the two string sequences that carry queries.
				st, intro, payload, truncated = vtString, b, payload[:0], false
			case 'X', '^', '_':
				// SOS, PM, APC. Nothing answers these, and stringIsQuery says so
				// by their introducer alone.
				st, intro = vtString, b
			case 0x1b:
				st, seqStart = vtEsc, i // ESC ESC: the second one introduces
			case 'Z':
				// DECID, the obsolete spelling of `ESC [ c`. Still answered.
				drop(i)
				st = vtGround
			default:
				st = vtGround
			}

		case vtCSI:
			switch {
			case b >= 0x40 && b <= 0x7e: // final byte
				if !tooLong && csiIsQuery(params, b) {
					drop(i)
				}
				st = vtGround
			case b >= 0x20 && b <= 0x3f: // parameter or intermediate byte
				if len(params) < maxParams {
					params = append(params, b)
				} else {
					// Longer than any query: kept, and kept unexamined.
					tooLong = true
				}
			default:
				// A control byte aborts the sequence; the terminal acts on it
				// and returns to ground.
				st = vtGround
			}

		case vtString:
			switch b {
			case 0x07: // BEL terminates — xterm accepts it for all of them
				if stringIsQuery(intro, payload, truncated) {
					drop(i)
				}
				st = vtGround
			case 0x1b:
				st = vtStringEsc
			default:
				if len(payload) < maxParams {
					payload = append(payload, b)
				} else {
					truncated = true
				}
			}

		case vtStringEsc:
			if b == '\\' { // ST
				if stringIsQuery(intro, payload, truncated) {
					drop(i)
				}
				st = vtGround
			} else {
				// The string never terminated: this ESC — the one at i-1 —
				// abandons it and introduces a sequence of its own, which is
				// the sequence the following bytes belong to. The unterminated
				// string is left alone; half a query is not a query, and we
				// cannot tell where the caller meant it to end.
				seqStart = i - 1
				st = vtEsc
				i-- // re-dispatch b from vtEsc
			}
		}
	}

	// A sequence still open at the end is emitted untouched. The snapshot ends
	// where the ring buffer's write cursor is, so the live bytes that follow it
	// on the wire continue it exactly — cutting it here would corrupt a
	// sequence that is merely in flight, and a query completed live is one the
	// program asking is still there to read.
	if !dropped {
		return data
	}
	return append(out, data[kept:]...)
}

// csiIsQuery reports whether a completed CSI sequence asks the terminal for an
// answer. params is the parameter and intermediate run between `ESC [` and the
// final byte.
//
// The list is the set xterm.js replies to, plus the ones other emulators reply
// to — the replay has to be inert in any client, not only in ours. Each case is
// narrowed to the query spelling, because most of these final bytes are shared
// with a command that draws or sets and must survive the filter.
func csiIsQuery(params []byte, final byte) bool {
	switch final {
	case 'c':
		// Device attributes: primary (`ESC [ c`), secondary (`ESC [ > c`) and
		// tertiary (`ESC [ = c`). Every CSI ending in 'c' asks the terminal to
		// identify itself; none of them draws.
		return true
	case 'n':
		// DSR — device status (`5 n`) and cursor position (`6 n`, `? 6 n`).
		// `ESC [ > n` is XTMODKEYS, which resets a key-encoding mode and
		// answers nothing.
		return len(params) == 0 || params[0] != '>'
	case 'p':
		// DECRQM, `ESC [ ? 2004 $ p`. The '$' intermediate is what tells it
		// from DECSTR (`! p`) and DECSCL (`" p`), which have no reply.
		return len(params) > 0 && params[len(params)-1] == '$'
	case 'q':
		// XTVERSION, `ESC [ > q`. `ESC [ SP q` is DECSCUSR — the cursor shape,
		// which a program legitimately leaves behind.
		return len(params) > 0 && params[0] == '>'
	case 'u':
		// The kitty keyboard protocol query, `ESC [ ? u`. Its push, pop and set
		// forms all carry parameters and answer nothing.
		return len(params) == 1 && params[0] == '?'
	case 't':
		// XTWINOPS. The window-manipulation ops are ignored by browser
		// terminals, but the report ops answer with a size, a state or — 20 and
		// 21 — the window title.
		if len(params) == 0 || params[0] == '>' || params[0] == '?' {
			return false
		}
		return winopReports[splitParams(params)[0]]
	}
	return false
}

// winopReports are the XTWINOPS operations that answer rather than act.
var winopReports = map[string]bool{
	"11": true, // window state
	"13": true, // window position
	"14": true, // text area size in pixels
	"15": true, // screen size in pixels
	"16": true, // cell size in pixels
	"18": true, // text area size in characters
	"19": true, // screen size in characters
	"20": true, // icon label
	"21": true, // window title
}

// stringIsQuery reports whether a completed string sequence asks for an answer.
// intro is the introducer byte and payload the first maxParams payload bytes,
// with truncated saying that there were more.
func stringIsQuery(intro byte, payload []byte, truncated bool) bool {
	switch intro {
	case ']':
		// The OSC queries are spelled by replacing the value with a '?', and
		// the terminal answers with the real one. Both halves have to be
		// checked, though: the final '?' alone would take a window title with a
		// question in it — `ESC ] 0 ; run tests? BEL`, which every shell that
		// puts the command line in the title can produce — for a query and cut
		// it out of the replay.
		//
		// Truncated payloads are kept for the same reason they are in
		// csiIsQuery: no query is this long, and one cut off at maxParams is
		// something else that happens to end in a '?'.
		if truncated {
			return false
		}
		ps, args, ok := bytes.Cut(payload, []byte(";"))
		if !ok || !oscQueryPs[string(ps)] {
			return false
		}
		return string(args) == "?" || bytes.HasSuffix(args, []byte(";?"))
	case 'P':
		// DCS. `+ q` is XTGETTCAP (a terminfo capability), `$ q` is DECRQSS (a
		// setting). Sixel data and tmux passthrough share the introducer and
		// answer nothing. Two bytes decide it, so a truncated payload is as
		// good as a whole one here.
		return len(payload) >= 2 && payload[1] == 'q' &&
			(payload[0] == '+' || payload[0] == '$')
	}
	return false
}

// oscQueryPs are the OSC operations that answer when their argument is '?'.
// The colour ones (4 palette, 5 special colours, 10-19 the dynamic foreground,
// background, cursor and selection colours) reply with an rgb: string; 52
// replies with the clipboard, which is the query that would paste whatever the
// user last copied into a shell prompt. The title operations (0, 1, 2) are
// deliberately absent: they never answer, whatever they end in.
var oscQueryPs = map[string]bool{
	"4": true, "5": true,
	"10": true, "11": true, "12": true, "13": true, "14": true,
	"15": true, "16": true, "17": true, "18": true, "19": true,
	"52": true,
}
