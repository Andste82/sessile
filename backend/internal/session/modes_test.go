package session

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestModeScanner(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want termModes
	}{
		{
			name: "plain output changes nothing",
			in:   "total 48\r\ndrwxr-xr-x 3 root root 4096 Aug 31 11:20 .\r\n",
		},
		{
			name: "a TUI starting",
			in:   "\x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[?25l",
			want: termModes{alt: "1049", mouse: "1002", mouseEnc: "1006", cursorHidden: true},
		},
		{
			name: "several modes in one sequence",
			in:   "\x1b[?1000;1006;2004h",
			want: termModes{mouse: "1000", mouseEnc: "1006", bracketedPaste: true},
		},
		{
			name: "the same TUI exiting cleanly leaves nothing behind",
			in:   "\x1b[?1049h\x1b[?1002h\x1b[?25l" + "\x1b[?25h\x1b[?1002l\x1b[?1049l",
		},
		{
			name: "the last tracking mode wins",
			in:   "\x1b[?1000h\x1b[?1003h",
			want: termModes{mouse: "1003"},
		},
		{
			// xterm.js keeps one active protocol, so a reset of any of the
			// three turns tracking off whichever one was on.
			name: "resetting one tracking mode clears tracking",
			in:   "\x1b[?1003h\x1b[?1000l",
		},
		{
			name: "the alternate screen keeps the spelling the program used",
			in:   "\x1b[?47h",
			want: termModes{alt: "47"},
		},
		{
			name: "bracketed paste, as a shell's line editor arms it",
			in:   "\x1b[?2004h",
			want: termModes{bracketedPaste: true},
		},
		{
			name: "application cursor keys and keypad",
			in:   "\x1b[?1h\x1b=",
			want: termModes{appCursor: true, appKeypad: true},
		},
		{
			name: "the numeric keypad turns the application one off",
			in:   "\x1b=\x1b>",
		},
		{
			name: "focus reporting",
			in:   "\x1b[?1004h",
			want: termModes{focusReport: true},
		},
		{
			name: "autowrap off",
			in:   "\x1b[?7l",
			want: termModes{wrapOff: true},
		},
		{
			// Without the '?' this is the ANSI mode set, a different namespace
			// entirely: `ESC [ 4 h` is insert mode, not the alternate screen.
			name: "an ANSI mode set is not a DEC private one",
			in:   "\x1b[4h\x1b[25h",
		},
		{
			name: "modes that are none of our business are ignored",
			in:   "\x1b[?12h\x1b[?2026h\x1b[?1049h",
			want: termModes{alt: "1049"},
		},
		{
			// A Sixel image or a tmux passthrough can carry any bytes at all,
			// and none of them may be read as a mode set. An ESC is the one
			// byte that cannot appear in a well-formed payload — Sixel data is
			// printable, and tmux passthrough doubles the ESCs it wraps.
			name: "a string payload is stepped over, not scanned",
			in:   "\x1bPq[?1049h\x1b\\",
		},
		{
			name: "and the same inside an OSC",
			in:   "\x1b]0;[?1002h\x07",
		},
		{
			// The other half of that rule, and what a real terminal does: an
			// ESC that is not ST abandons the string and introduces a sequence
			// of its own, which is then executed. titleScanner reads it the
			// same way.
			name: "an escape inside a string abandons it and the rest is real",
			in:   "\x1b]0;\x1b[?1049h",
			want: termModes{alt: "1049"},
		},
		{
			name: "a title containing an escape does not swallow what follows",
			in:   "\x1b]0;build\x07\x1b[?1049h",
			want: termModes{alt: "1049"},
		},
		{
			name: "a control byte aborts a CSI",
			in:   "\x1b[?1049\r\nh",
		},
		{
			name: "an overlong parameter run is kept unexamined",
			in:   "\x1b[?" + strings.Repeat("1;", maxParams) + "1049h",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var m modeScanner
			got, _ := m.scan([]byte(tc.in))
			if got != tc.want {
				t.Errorf("scan(%q)\n got %+v\nwant %+v", tc.in, got, tc.want)
			}
		})
	}
}

// A PTY read ends wherever the kernel filled the buffer, so an escape sequence
// arriving in two pieces is routine rather than exotic.
func TestModeScannerAcrossChunks(t *testing.T) {
	chunks := []string{"\x1b[?10", "49h\x1b", "[?100", "2h"}
	var m modeScanner
	var got termModes
	for _, c := range chunks {
		got, _ = m.scan([]byte(c))
	}
	want := termModes{alt: "1049", mouse: "1002"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// The read loop takes the session lock only when something actually changed,
// so "changed" has to be false for the chunks that are merely output — which
// is nearly all of them.
func TestModeScannerReportsChange(t *testing.T) {
	var m modeScanner

	if _, changed := m.scan([]byte("hello\r\n")); changed {
		t.Error("plain output reported a change")
	}
	if _, changed := m.scan([]byte("\x1b[?1049h")); !changed {
		t.Error("entering the alternate screen reported no change")
	}
	if _, changed := m.scan([]byte("\x1b[?1049h")); changed {
		t.Error("re-setting a mode already set reported a change")
	}
	if _, changed := m.scan([]byte("\x1b[H\x1b[2Jredrawing")); changed {
		t.Error("a repaint reported a change")
	}
	if _, changed := m.scan([]byte("\x1b[?1049l")); !changed {
		t.Error("leaving the alternate screen reported no change")
	}
}

func TestPreamble(t *testing.T) {
	tests := []struct {
		name  string
		modes termModes
		want  string
	}{
		{
			name: "a reset terminal needs no preamble",
		},
		{
			name:  "a TUI with the mouse",
			modes: termModes{alt: "1049", mouse: "1002", mouseEnc: "1006", cursorHidden: true},
			want:  "\x1b[?1049h\x1b[?1002h\x1b[?1006h\x1b[?25l",
		},
		{
			name:  "a shell at a prompt",
			modes: termModes{bracketedPaste: true},
			want:  "\x1b[?2004h",
		},
		{
			name:  "keypad and cursor keys",
			modes: termModes{appCursor: true, appKeypad: true},
			want:  "\x1b[?1h\x1b=",
		},
		{
			name:  "autowrap off",
			modes: termModes{wrapOff: true},
			want:  "\x1b[?7l",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(tc.modes.preamble())
			if got != tc.want {
				t.Errorf("preamble() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Whatever the scanner saw, feeding its own preamble back through a fresh
// scanner has to reproduce it — otherwise the client is primed into a state the
// session is not in.
func TestPreambleRoundTrips(t *testing.T) {
	states := []termModes{
		{alt: "1049", mouse: "1003", mouseEnc: "1006", bracketedPaste: true},
		{alt: "47", cursorHidden: true, wrapOff: true},
		{mouse: "1000", mouseEnc: "1015", focusReport: true, appCursor: true, appKeypad: true},
		{bracketedPaste: true},
		{},
	}
	for _, want := range states {
		var m modeScanner
		got, _ := m.scan(want.preamble())
		if got != want {
			t.Errorf("round trip of %+v gave %+v", want, got)
		}
	}
}

// Issue #92, and the reason this file exists. A program that repaints pushes
// its own opening sequence off the front of the ring buffer, so the replay a
// later attach gets switches nothing on. Before the preamble, a client that
// attached here sat on the normal screen with mouse reporting off while htop
// believed both were on — scrolling did nothing, reopening the tab replayed the
// same truncated snapshot, and only a window resize brought it back.
func TestAttachRestoresModesEvictedFromTheBuffer(t *testing.T) {
	const bufSize = 4096
	s := &Session{
		ID:      "11111111-2222-3333-4444-555555555555",
		Status:  StatusRunning,
		buffer:  NewRingBuffer(bufSize),
		clients: make(map[Client]clientGeom),
	}

	// The read loop, in miniature: scan each chunk, record what changed, and
	// append it to the buffer.
	var scanner modeScanner
	feed := func(chunk string) {
		if state, changed := scanner.scan([]byte(chunk)); changed {
			s.setModes(state)
		}
		if _, err := s.buffer.Write([]byte(chunk)); err != nil {
			t.Fatalf("buffer write: %v", err)
		}
	}

	// htop starts, then repaints until its opening sequence is long gone.
	feed("\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1006h\x1b[?25l")
	repaint := "\x1b[H\x1b[2J" + strings.Repeat("cpu 12%  mem 3.4G\r\n", 16)
	for written := 0; written < bufSize*3; written += len(repaint) {
		feed(repaint)
	}

	// The premise: the snapshot really has lost them.
	snapshot := s.buffer.Snapshot()
	for _, mode := range []string{"?1049h", "?1002h", "?1006h", "?25l"} {
		if bytes.Contains(snapshot, []byte(mode)) {
			t.Fatalf("test is not exercising eviction: snapshot still has %s", mode)
		}
	}

	c := &recordingClient{id: "c1"}
	s.attach(c)

	if len(c.sent) != 1 {
		t.Fatalf("got %d binary sends, want 1", len(c.sent))
	}
	sent := c.sent[0]

	// A fresh terminal fed these bytes ends up where the session is.
	var replayed modeScanner
	got, _ := replayed.scan(sent)
	want := termModes{alt: "1049", mouse: "1002", mouseEnc: "1006", cursorHidden: true}
	if got != want {
		t.Errorf("a client attaching lands in %+v, want %+v", got, want)
	}

	// The preamble leads, or the replay would draw into the wrong screen.
	if !bytes.HasPrefix(sent, want.preamble()) {
		t.Error("the replay does not start with the preamble")
	}

	// And replayBytes still counts what was actually sent (§5).
	if len(c.controls) != 1 {
		t.Fatalf("got %d control frames, want 1", len(c.controls))
	}
	att, ok := c.controls[0].(AttachedMsg)
	if !ok {
		t.Fatalf("first control frame is %T, want AttachedMsg", c.controls[0])
	}
	if att.ReplayBytes != len(sent) {
		t.Errorf("replayBytes = %d, want %d", att.ReplayBytes, len(sent))
	}
}

// The other half: a session whose snapshot still carries its own mode switches
// must not be given a second, older opinion in front of them. The preamble is
// written first precisely so the replay overrides it.
func TestAttachPreambleDoesNotOverrideAnIntactReplay(t *testing.T) {
	s := &Session{
		ID:      "22222222-3333-4444-5555-666666666666",
		Status:  StatusRunning,
		buffer:  NewRingBuffer(1 << 16),
		clients: make(map[Client]clientGeom),
	}

	var scanner modeScanner
	feed := func(chunk string) {
		if state, changed := scanner.scan([]byte(chunk)); changed {
			s.setModes(state)
		}
		if _, err := s.buffer.Write([]byte(chunk)); err != nil {
			t.Fatalf("buffer write: %v", err)
		}
	}

	feed("\x1b[?1049h\x1b[?1002h") // vim opens
	feed("some editing\r\n")
	feed("\x1b[?1002l\x1b[?1049l") // and closes again
	feed("$ ")

	c := &recordingClient{id: "c1"}
	s.attach(c)

	var replayed modeScanner
	got, _ := replayed.scan(c.sent[0])
	if got != (termModes{}) {
		t.Errorf("a client attaching after the program exited lands in %+v, want a reset terminal", got)
	}
}

// An ordinary shell session that never switched a mode pays nothing: no
// preamble, and the replay stays the slice sanitizeReplay handed back.
func TestAttachWithoutModesSendsNoPreamble(t *testing.T) {
	s := &Session{
		ID:      "33333333-4444-5555-6666-777777777777",
		Status:  StatusRunning,
		buffer:  NewRingBuffer(1 << 16),
		clients: make(map[Client]clientGeom),
	}
	const out = "$ ls\r\nREADME.md\r\n$ "
	if _, err := s.buffer.Write([]byte(out)); err != nil {
		t.Fatalf("buffer write: %v", err)
	}

	c := &recordingClient{id: "c1"}
	s.attach(c)

	if len(c.sent) != 1 || string(c.sent[0]) != out {
		t.Errorf("sent %q, want %q", c.sent, out)
	}
}

// End to end, through a real shell and a real pty: a program switches the modes
// on, and a client attaching afterwards is primed with them.
func TestSessionModesFromARealShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	mgr, _, _ := testManager(t)
	info, err := mgr.CreateLocal("test-user", "probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID, "test-user") })

	// What a TUI writes when it takes the screen.
	const setup = `printf '\033[?1049h\033[?1002h\033[?1006h\033[?25l'` + "\n"
	if err := mgr.WriteInput(info.ID, []byte(setup)); err != nil {
		t.Fatalf("write: %v", err)
	}

	want := termModes{alt: "1049", mouse: "1002", mouseEnc: "1006", cursorHidden: true}
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := &recordingClient{id: "probe"}
		if _, err := mgr.Attach(info.ID, "test-user", c); err != nil {
			t.Fatalf("attach: %v", err)
		}
		mgr.Detach(info.ID, c)

		var seen modeScanner
		if len(c.sent) == 1 {
			if got, _ := seen.scan(c.sent[0]); got == want {
				if !bytes.HasPrefix(c.sent[0], want.preamble()) {
					t.Error("the replay does not start with the preamble")
				}
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the session never reported %+v", want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
