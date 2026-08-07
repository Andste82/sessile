package session

import (
	"strings"
	"testing"
	"time"
)

// fixedClock pins timeNow for the duration of a test so bell timestamps are
// comparable.
func fixedClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := timeNow
	timeNow = func() time.Time { return at }
	t.Cleanup(func() { timeNow = prev })
}

func TestVTScannerBracketedPaste(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"never set", "$ ls\r\nfile\r\n", false},
		// The shell's prompt cycle, as captured from bash: on while reading the
		// line, off while the command runs.
		{"at the prompt", "\x1b[?2004h$ ", true},
		{"command running", "\x1b[?2004h$ sleep 3\r\n\x1b[?2004l", false},
		{"back at the prompt", "\x1b[?2004h$ ls\x1b[?2004l\r\nfile\r\n\x1b[?2004h$ ", true},
		// vim and Claude Code send it alongside other private modes.
		{"combined set", "\x1b[?1049;1004;2004h", true},
		{"combined reset", "\x1b[?2004h\x1b[?1049;2004l", false},
		// Claude Code's opening sequence: hides the cursor, enables bracketed
		// paste, stays on the normal screen.
		{"claude code idle", "\x1b[?25l\x1b[?2004h", true},
		{"not a substring match", "\x1b[?20040h", false},
		{"not a private mode", "\x1b[2004h", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v vtScanner
			v.Feed([]byte(tt.data))
			if v.bracketedPaste != tt.want {
				t.Errorf("bracketedPaste after %q = %v, want %v", tt.data, v.bracketedPaste, tt.want)
			}
		})
	}
}

// The bell is what rule 5 of §4.7 hangs on, and it is also the byte that ends
// an OSC string. bash and fish set the window title on every prompt, so a
// scanner that counts every 0x07 reports a bell several times a minute in a
// session where nothing is happening.
func TestVTScannerBellIgnoresStringTerminators(t *testing.T) {
	at := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		data string
		want bool // a real bell was seen
	}{
		{"no bell", "plain output\r\n", false},
		{"bare bell", "\x07", true},
		{"bell after text", "error\x07\r\n", true},
		// OSC 0 (window title) terminated by BEL — bash and fish, every prompt.
		{"osc title ended by bel", "\x1b]0;user@host: ~\x07$ ", false},
		{"osc title ended by st", "\x1b]0;user@host: ~\x1b\\$ ", false},
		{"two titles then a real bell", "\x1b]0;a\x07\x1b]0;b\x07\x07", true},
		// A DCS payload (Sixel, tmux passthrough) can carry any byte at all.
		{"dcs payload containing bel", "\x1bPq#0;2;0;0;0\x07", false},
		{"osc aborted by a new sequence", "\x1b]0;titl\x1b[?2004h\x07", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixedClock(t, at)
			var v vtScanner
			v.Feed([]byte(tt.data))
			if got := !v.lastBell.IsZero(); got != tt.want {
				t.Errorf("bell after %q = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}

// A PTY read ends wherever the kernel filled the buffer, so every sequence the
// scanner cares about will eventually arrive split in two. The result must not
// depend on where the split falls — this is the property a per-chunk scan
// cannot hold, and the reason the scanner carries state.
func TestVTScannerIsIndependentOfChunkBoundaries(t *testing.T) {
	inputs := []string{
		"\x1b[?2004h$ ls\x1b[?2004l\r\nfile\r\n\x1b[?2004h$ ",
		"$ htop\r\n\x1b[?1049h\x1b[H\x1b[?25lcpu 12%",
		"\x1b]0;user@host: ~\x07\x1b[?2004h$ \x07",
		"\x1b[?1049;1004;2004h drawing \x1b[?1049;2004l",
		"\x1bPq#0;2;0;0;0\x07\x1b[?2004h",
	}

	for _, in := range inputs {
		t.Run(strings.ReplaceAll(in[:min(12, len(in))], "\x1b", "ESC"), func(t *testing.T) {
			fixedClock(t, time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC))

			var whole vtScanner
			whole.Feed([]byte(in))

			// Every two-way split.
			for i := 0; i <= len(in); i++ {
				var split vtScanner
				split.Feed([]byte(in[:i]))
				split.Feed([]byte(in[i:]))
				if split.altScreen != whole.altScreen ||
					split.bracketedPaste != whole.bracketedPaste ||
					split.lastBell != whole.lastBell {
					t.Fatalf("split at %d: alt=%v/%v bracketed=%v/%v bell=%v/%v",
						i, split.altScreen, whole.altScreen,
						split.bracketedPaste, whole.bracketedPaste,
						!split.lastBell.IsZero(), !whole.lastBell.IsZero())
				}
			}

			// And one byte at a time, the worst case a slow program produces.
			var single vtScanner
			for i := range len(in) {
				single.Feed([]byte(in[i : i+1]))
			}
			if single.altScreen != whole.altScreen ||
				single.bracketedPaste != whole.bracketedPaste ||
				single.lastBell != whole.lastBell {
				t.Errorf("byte-at-a-time: alt=%v/%v bracketed=%v/%v bell=%v/%v",
					single.altScreen, whole.altScreen,
					single.bracketedPaste, whole.bracketedPaste,
					!single.lastBell.IsZero(), !whole.lastBell.IsZero())
			}
		})
	}
}

// A malformed or hostile stream must not be able to grow the parameter buffer,
// and an over-long run must not be acted on.
func TestVTScannerBoundsParameters(t *testing.T) {
	var v vtScanner
	v.Feed([]byte("\x1b[?" + strings.Repeat("9", 4096) + ";2004h"))

	if len(v.params) > maxParams {
		t.Errorf("parameter buffer grew to %d, want at most %d", len(v.params), maxParams)
	}
	if v.bracketedPaste {
		t.Error("acted on a parameter run past the cap; an over-long sequence is malformed, not a mode set")
	}
	if cap(v.params) > 4*maxParams {
		t.Errorf("parameter buffer capacity %d is not bounded", cap(v.params))
	}
}

// The scanner is reused for the lifetime of a session, so a completed sequence
// must not leave parameters behind for the next one to inherit.
func TestVTScannerDoesNotLeakParametersBetweenSequences(t *testing.T) {
	var v vtScanner
	v.Feed([]byte("\x1b[?1049h")) // alt screen on
	v.Feed([]byte("\x1b[H"))      // cursor home — no parameters at all
	v.Feed([]byte("\x1b[?2004h")) // bracketed paste on

	if !v.altScreen || !v.bracketedPaste {
		t.Errorf("alt=%v bracketed=%v, want both true", v.altScreen, v.bracketedPaste)
	}

	// `ESC [ 4 h` is insert mode, not a private mode, and must change nothing.
	before := v.altScreen
	v.Feed([]byte("\x1b[4h"))
	if v.altScreen != before {
		t.Error("a non-private mode changed the alternate screen state")
	}
}
