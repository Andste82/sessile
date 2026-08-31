package session

import (
	"strings"
	"testing"
)

// The scanner is the one place a byte stream turns into a label a user reads,
// so the shapes it has to get right are pinned here: both spellings, both
// terminators, the sequences that look like a title and are not, and the bytes
// that must never reach the JSON.
func TestTitleScanner(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"OSC 0 terminated by BEL", "\x1b]0;build\x07", "build", true},
		{"OSC 2 terminated by ST", "\x1b]2;build\x1b\\", "build", true},
		{"a title among ordinary output", "done\r\n\x1b]0;~/project\x07$ ", "~/project", true},
		{"plain output", "make: nothing to be done\r\n", "", false},

		// OSC 1 is the icon name, not the title. OSC 52 is the clipboard, and
		// putting what someone copied into the session list would be a leak as
		// well as a lie.
		{"OSC 1 sets the icon name", "\x1b]1;icon\x07", "", false},
		{"OSC 52 is the clipboard", "\x1b]52;c;aGk=\x07", "", false},

		// A program clearing its title says so with an empty payload, and that
		// is a title change like any other.
		{"an empty title clears", "\x1b]0;\x07", "", true},
		{"no argument at all sets nothing", "\x1b]0\x07", "", false},

		// The last one wins: a burst of output that ends in a prompt carries
		// the title the shell wrote for the command and the one it wrote for
		// the prompt after it.
		{"several in one chunk", "\x1b]0;first\x07out\x1b]0;second\x07", "second", true},

		// A CSI cannot hide an ESC, so the scanner steps over one without a
		// state of its own — but it must not swallow what follows either.
		{"after a CSI sequence", "\x1b[?1049h\x1b[2J\x1b]0;vim\x07", "vim", true},

		// A string sequence that is not an OSC can carry any bytes at all,
		// including ones that spell a title.
		{"inside a DCS payload", "\x1bP]0;fake\x1b\\", "", false},

		// Half a title is not a title: the ESC abandons the string, and the
		// sequence that follows is the one the bytes belong to.
		{"unterminated, then something else", "\x1b]0;half\x1b[0m", "", false},

		// A pty carries bytes, not text. Neither of these is displayable, and
		// a title is one line in a list.
		{"control characters are dropped", "\x1b]0;two\r\nlines\x07", "twolines", true},
		{"surrounding whitespace is trimmed", "\x1b]0;   spaced   \x07", "spaced", true},
		{"invalid UTF-8 is dropped", "\x1b]0;caf\xc3\xa9\xff\x07", "café", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sc titleScanner
			got, ok := sc.scan([]byte(tt.in))
			if ok != tt.ok {
				t.Fatalf("scan(%q) ok = %v, want %v (title %q)", tt.in, ok, tt.ok, got)
			}
			if got != tt.want {
				t.Errorf("scan(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A PTY read ends wherever the kernel filled the buffer, which is regularly in
// the middle of a sequence. This is the case a per-chunk search gets wrong.
func TestTitleScannerAcrossChunks(t *testing.T) {
	var sc titleScanner
	chunks := []string{"\x1b", "]0", ";lo", "ng title", "\x07"}
	for i, c := range chunks[:len(chunks)-1] {
		if got, ok := sc.scan([]byte(c)); ok {
			t.Fatalf("chunk %d completed a title early: %q", i, got)
		}
	}
	got, ok := sc.scan([]byte(chunks[len(chunks)-1]))
	if !ok || got != "long title" {
		t.Errorf("scan = %q, %v, want %q, true", got, ok, "long title")
	}
}

// The cap is on the payload, not on the sequence: a program that writes a
// novel into its title still has a title, and the UI still has one line.
func TestTitleScannerCapsThePayload(t *testing.T) {
	var sc titleScanner
	got, ok := sc.scan([]byte("\x1b]0;" + strings.Repeat("x", 4096) + "\x07"))
	if !ok {
		t.Fatal("an over-long title was dropped entirely")
	}
	if len(got) > maxTitleBytes {
		t.Errorf("title is %d bytes, want at most %d", len(got), maxTitleBytes)
	}
	if !strings.HasPrefix(got, "xxx") {
		t.Errorf("title = %q, want the start of the payload", got)
	}
}

// Only a change is worth publishing: a shell that rewrites the same title at
// every prompt must not wake the event fan-out once per command.
func TestSetTitleMarksOnlyRealChanges(t *testing.T) {
	s := &Session{}
	s.setTitle("build")
	if !s.titleDirty {
		t.Fatal("a first title left nothing to publish")
	}
	s.titleDirty = false

	s.setTitle("build")
	if s.titleDirty {
		t.Error("the same title again was published as a change")
	}
	s.setTitle("test")
	if !s.titleDirty {
		t.Error("a new title left nothing to publish")
	}
}

// A dead session must not keep showing the title its last program left behind:
// there is no shell to reach another prompt and overwrite it.
func TestClearDerivedDropsTheTitle(t *testing.T) {
	s := &Session{fgCommand: "vim", title: "~/notes"}
	info, changed := s.clearDerived()
	if !changed {
		t.Fatal("clearing derived state reported no change")
	}
	if info.Title != "" || info.Command != "" {
		t.Errorf("stopped session reports command=%q title=%q", info.Command, info.Title)
	}
	if _, changed := s.clearDerived(); changed {
		t.Error("clearing an already-clear session reported a change")
	}
}

// End to end: a real shell writes the sequence, the read loop scans it out of
// the live stream, and the sampler carries it to the session list.
func TestSessionTitleFromARealShell(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.CreateLocal("test-user", "probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID, "test-user") })

	if err := mgr.WriteInput(info.ID, []byte(`printf '\033]0;from the shell\007'`+"\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	waitForForeground(t, mgr, info.ID, func(i Info) bool { return i.Title == "from the shell" })
}
