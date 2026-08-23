package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ScrollbackStore persists per-session ring-buffer snapshots so terminal output
// survives a backend restart (PROJECT_PLAN.md §8).
//
// The plan's "metadata only" rule covers SQLite; scrollback is deliberately kept
// out of the database. It is opaque, up to --buffer-size per session and
// rewritten on a timer — exactly the write pattern that would make a
// single-connection SQLite the bottleneck for the PTY read loop. One file per
// session, replaced atomically, costs nothing on the hot path.
//
// Files live in the data directory beside the database, never under --root: a
// shell able to read or rewrite its own scrollback would make the restored
// history worthless.
type ScrollbackStore struct {
	dir string
}

// NewScrollbackStore returns a store writing into dir/scrollback. The directory
// is created lazily on the first Save so a read-only deployment that never
// starts a session stays clean.
func NewScrollbackStore(dir string) *ScrollbackStore {
	return &ScrollbackStore{dir: filepath.Join(dir, "scrollback")}
}

// path returns the snapshot path for id, rejecting ids that are not plain file
// names. Session ids are UUIDs, but they arrive from the request URL, so the
// check is not theoretical (§4.5: every user-supplied path is validated).
func (s *ScrollbackStore) path(id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid session id")
	}
	return filepath.Join(s.dir, id+".bin"), nil
}

// Save writes data as id's snapshot, replacing any previous one atomically so a
// crash mid-write can never leave a truncated file behind.
func (s *ScrollbackStore) Save(id string, data []byte) error {
	final, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("create scrollback dir: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, id+".*.tmp")
	if err != nil {
		return fmt.Errorf("create scrollback temp: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write scrollback: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close scrollback: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("chmod scrollback: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return fmt.Errorf("replace scrollback: %w", err)
	}
	return nil
}

// Load returns id's snapshot. A session that has none — never saved, or the
// file was removed — yields (nil, nil): an absent scrollback is the normal case
// for a fresh session, not an error.
func (s *ScrollbackStore) Load(id string) ([]byte, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read scrollback: %w", err)
	}
	return data, nil
}

// Delete removes id's snapshot. A missing file is not an error.
func (s *ScrollbackStore) Delete(id string) error {
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove scrollback: %w", err)
	}
	return nil
}

// altScreenModes are the DEC private modes that switch to the alternate screen
// buffer: the original 47, 1047 (clears on exit) and 1049 (saves and restores
// the cursor as well). Programs use all three, sometimes in the same sequence.
var altScreenModes = map[string]bool{"47": true, "1047": true, "1049": true}

// endsInAltScreen reports whether data leaves the terminal in the alternate
// screen buffer, by replaying the mode switches it contains and keeping the
// last one to win.
//
// It walks the stream the way sanitizeReplay does and for the same reason: an
// escape sequence is the only thing here that is not text, and everything
// between an introducer and its final byte has to be stepped over rather than
// searched for. String sequences (OSC, DCS) are skipped wholesale — a Sixel
// image or a tmux passthrough can carry any bytes at all, including ones that
// spell a mode switch.
//
// A snapshot is the tail of a ring buffer, so it can begin mid-sequence and the
// switch that entered the alternate screen may have been cut off the front. In
// that case this reports false and the separator simply does not reset a mode
// the terminal was in — the same outcome as before this check existed, and
// still no worse than overwriting the history in every ordinary session.
func endsInAltScreen(data []byte) bool {
	var (
		alt     bool
		st      vtParse
		params  []byte
		tooLong bool
	)
	for _, b := range data {
		switch st {
		case vtGround:
			if b == 0x1b {
				st = vtEsc
			}
			// Every other byte is text. UTF-8 cannot hide an ESC in a
			// multi-byte sequence — lead bytes are 0xc2-0xf4 and continuation
			// bytes 0x80-0xbf — so scanning byte by byte here is safe.

		case vtEsc:
			switch b {
			case '[':
				st, params, tooLong = vtCSI, params[:0], false
			case ']', 'P', 'X', '^', '_':
				st = vtString
			case 0x1b:
				st = vtEsc // ESC ESC: the second one introduces the sequence
			default:
				st = vtGround
			}

		case vtCSI:
			switch {
			case b >= 0x40 && b <= 0x7e: // final byte
				// DEC private mode set (h) and reset (l), which is the only
				// sequence this cares about: ESC [ ? 1049 h.
				if !tooLong && (b == 'h' || b == 'l') && len(params) > 0 && params[0] == '?' {
					for _, p := range splitParams(params[1:]) {
						if altScreenModes[p] {
							alt = b == 'h'
						}
					}
				}
				st = vtGround
			case b >= 0x20 && b <= 0x3f: // parameter or intermediate byte
				if len(params) < maxParams {
					params = append(params, b)
				} else {
					tooLong = true
				}
			default:
				// A control byte aborts the sequence; the terminal acts on it
				// and returns to ground.
				st = vtGround
			}

		case vtString:
			switch b {
			case 0x07:
				st = vtGround // BEL terminates — xterm accepts it for all of them
			case 0x1b:
				st = vtStringEsc
			}

		case vtStringEsc:
			// ESC \ is ST and ends the string. Anything else means the string
			// was aborted by a fresh escape sequence, and this byte introduces
			// it.
			if b == '\\' {
				st = vtGround
			} else {
				// The byte after that ESC has already arrived: dispatch it here
				// rather than going back through vtEsc for it.
				switch b {
				case '[':
					st, params, tooLong = vtCSI, params[:0], false
				case ']', 'P', 'X', '^', '_':
					st = vtString
				default:
					st = vtGround
				}
			}
		}
	}
	return alt
}

// restoreSeparator is written between a restored snapshot and the output of the
// shell that replaces it. inAltScreen says whether the snapshot leaves the
// terminal in the alternate screen buffer — endsInAltScreen answers that.
//
// The leading escapes are not decoration. A snapshot is raw PTY bytes, and a
// session that was in htop when it stopped ends mid alternate-screen: replaying
// it verbatim would leave the new shell drawing into a screen the user cannot
// scroll, with a hidden cursor and possibly inverse attributes. Undoing those
// modes is what makes the restored buffer safe to prepend to a live session.
//
// The alternate-screen reset is the one that has to be conditional. DECRST 1049
// does not only switch buffers, it also restores the cursor saved when the
// alternate screen was entered — and a terminal that never entered it has no
// saved cursor, so the restore lands at the top-left of the screen. That is the
// whole of the "separator overwrites the restored history" bug: the separator
// was drawn over the first lines of the very output it was supposed to follow.
// Emitting 1049l only when the snapshot really ends in the alternate screen
// leaves the cursor where the previous shell left it — the end of the output —
// in every other case.
//
// The input modes are the other half, and the one that makes a session
// unusable rather than merely ugly. A program that exits cleanly turns them off
// itself; one that is killed — a `docker stop`, a SIGKILLed backend, a machine
// that went down — does not, and the snapshot then ends with them on. Mouse
// tracking is the worst of them: the replacement shell inherits `?1003h` and
// every mouse *move* over the window sends it a report, which it echoes as
// `35;42;7M` and a bell, several per second, with no way to type past it. That
// is not hypothetical — it is what a snapshot from a killed Claude Code looks
// like, mouse reports and bells to the last byte.
//
// Which modes: the ones a program turns on for itself and a shell cannot use.
// Mouse tracking (1000/1002/1003 and the encodings 1005/1006/1015/1016), focus
// reporting (1004), bracketed paste (2004, re-armed by the new shell's own line
// editor a moment later), application cursor keys (DECCKM) and the application
// keypad. Not a full reset: RIS would clear the screen, and clearing the screen
// is deleting the very history this separator introduces.
//
// The scroll region needs the same care as 1049, for the same reason. DECSTBM
// homes the cursor as a side effect, so resetting the margins straight would
// draw the separator over the top of the restored output. Wrapping it in
// DECSC/DECRC puts the cursor back where the previous shell left it — the idiom
// the programs that set margins use themselves.
func restoreSeparator(at time.Time, inAltScreen bool) []byte {
	const (
		leaveAltScreen = "\x1b[?1049l"
		mouseOff       = "\x1b[?1000l\x1b[?1002l\x1b[?1003l" + // tracking
			"\x1b[?1005l\x1b[?1006l\x1b[?1015l\x1b[?1016l" // report encodings
		focusOff         = "\x1b[?1004l"
		pasteOff         = "\x1b[?2004l"
		cursorKeysOff    = "\x1b[?1l" // DECCKM: arrows send CSI again, not SS3
		keypadNumeric    = "\x1b>"    // DECKPNM
		fullScrollRegion = "\x1b7" +  // DECSC, so DECSTBM cannot home the cursor
			"\x1b[r" + "\x1b8" // DECSTBM with no parameters, DECRC
		resetAttrs = "\x1b[0m"
		showCursor = "\x1b[?25h"
		enableWrap = "\x1b[?7h"
		dim        = "\x1b[2m"
	)
	var b strings.Builder
	if inAltScreen {
		b.WriteString(leaveAltScreen)
	}
	b.WriteString(mouseOff + focusOff + pasteOff + cursorKeysOff + keypadNumeric)
	b.WriteString(fullScrollRegion)
	b.WriteString(resetAttrs + showCursor + enableWrap)
	b.WriteString("\r\n" + dim + "── restored " + at.UTC().Format(time.RFC3339) +
		" ── output above is from the previous run ──" + resetAttrs + "\r\n")
	return []byte(b.String())
}

// endsInAltScreen lives in vtscan.go: the mode parsing it needs is the same
// parsing the activity scanner does, and one stream format deserves one parser.

// validID reports whether id is safe to use as a path element: non-empty, no
// separators, no "." or ".." and no characters outside the UUID alphabet. It
// guards every filesystem path derived from a session id — scrollback snapshots
// and shell history files alike.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-':
		default:
			return false
		}
	}
	// A name of only dashes is not a traversal risk, but it is not a session id
	// either; require at least one alphanumeric character.
	return strings.ContainsFunc(id, func(r rune) bool {
		return r != '-'
	})
}
