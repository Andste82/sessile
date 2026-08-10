package session

import (
	"bytes"
	"strings"
	"testing"
)

// The queries a replay must not carry, and the commands that share their final
// byte and must survive it. Everything outside a sequence is plain text and is
// there to catch a filter that eats more than the sequence it dropped.
func TestSanitizeReplayDropsQueries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Device attributes: the query behind the reported bug.
		{"primary DA", "a\x1b[cb", "ab"},
		{"primary DA with parameter", "a\x1b[0cb", "ab"},
		{"secondary DA", "a\x1b[>cb", "ab"},
		{"secondary DA with parameter", "a\x1b[>0cb", "ab"},
		{"tertiary DA", "a\x1b[=cb", "ab"},
		{"DECID", "a\x1bZb", "ab"},

		// Status reports.
		{"device status", "a\x1b[5nb", "ab"},
		{"cursor position", "a\x1b[6nb", "ab"},
		{"extended cursor position", "a\x1b[?6nb", "ab"},
		{"XTMODKEYS is not a query", "a\x1b[>4nb", "a\x1b[>4nb"},

		// Mode and setting requests.
		{"DECRQM", "a\x1b[?2004$pb", "ab"},
		{"DECRQM ANSI mode", "a\x1b[4$pb", "ab"},
		{"DECSTR is not a query", "a\x1b[!pb", "a\x1b[!pb"},
		{"DECSCL is not a query", "a\x1b[61\"pb", "a\x1b[61\"pb"},
		{"XTVERSION", "a\x1b[>0qb", "ab"},
		{"DECSCUSR is not a query", "a\x1b[5 qb", "a\x1b[5 qb"},
		{"kitty keyboard query", "a\x1b[?ub", "ab"},
		{"kitty keyboard push is not a query", "a\x1b[>1ub", "a\x1b[>1ub"},

		// Window operations: the report forms answer, the rest act.
		{"text area size in characters", "a\x1b[18tb", "ab"},
		{"window title report", "a\x1b[21tb", "ab"},
		{"title stack push is not a query", "a\x1b[22;0tb", "a\x1b[22;0tb"},
		{"deiconify is not a query", "a\x1b[1tb", "a\x1b[1tb"},

		// OSC: the queries are the ones whose value is a '?'.
		{"background colour query, BEL", "a\x1b]11;?\x07b", "ab"},
		{"foreground colour query, ST", "a\x1b]10;?\x1b\\b", "ab"},
		{"palette query", "a\x1b]4;1;?\x07b", "ab"},
		{"clipboard read", "a\x1b]52;c;?\x07b", "ab"},
		{"colour set is not a query", "a\x1b]11;rgb:00/00/00\x07b", "a\x1b]11;rgb:00/00/00\x07b"},
		{"clipboard write is not a query", "a\x1b]52;c;bG9uZw==\x07b", "a\x1b]52;c;bG9uZw==\x07b"},
		// A shell that puts the command line in the window title writes these.
		{"window title is not a query", "a\x1b]0;why?\x07b", "a\x1b]0;why?\x07b"},
		{"icon title is not a query", "a\x1b]1;make test?\x07b", "a\x1b]1;make test?\x07b"},

		// DCS.
		{"XTGETTCAP", "a\x1bP+q544e\x1b\\b", "ab"},
		{"DECRQSS", "a\x1bP$qm\x1b\\b", "ab"},
		{"sixel data is not a query", "a\x1bPq#0;2;0;0;0\x1b\\b", "a\x1bPq#0;2;0;0;0\x1b\\b"},

		// Drawing must pass through untouched, escapes and all.
		{"cursor movement", "\x1b[2J\x1b[H\x1b[31mx\x1b[0m", "\x1b[2J\x1b[H\x1b[31mx\x1b[0m"},
		{"mode set", "\x1b[?1049h\x1b[?2004h", "\x1b[?1049h\x1b[?2004h"},

		{"several queries in one run", "\x1b[c\x1b[6n\x1b[>0q", ""},
		{
			"what Claude Code actually emits",
			"\x1b[?2031h\x1b[>0q\x1b[c\x1b[?1049h\x1b[2J",
			"\x1b[?2031h\x1b[?1049h\x1b[2J",
		},

		// Edges of the buffer. A snapshot is the tail of a ring, so it starts
		// wherever the ring wrapped and ends at the write cursor.
		{"query truncated at the front", "[c\x1b[cx", "[cx"},
		{"incomplete sequence at the end is kept", "x\x1b[", "x\x1b["},
		{"incomplete query at the end is kept", "x\x1b[?200", "x\x1b[?200"},
		{"unterminated OSC is kept", "x\x1b]0;title", "x\x1b]0;title"},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeReplay([]byte(tc.in))
			if string(got) != tc.want {
				t.Errorf("sanitizeReplay(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A run longer than any real query is left alone rather than being judged on
// the prefix that fits: whatever it is, it is not `ESC [ c`.
func TestSanitizeReplayKeepsOverlongSequences(t *testing.T) {
	for name, in := range map[string][]byte{
		"CSI": []byte("\x1b[" + strings.Repeat("1;", maxParams) + "c"),
		"OSC": []byte("\x1b]11;" + strings.Repeat("x", maxParams) + "?\x07"),
	} {
		if got := sanitizeReplay(in); !bytes.Equal(got, in) {
			t.Errorf("%s: sanitizeReplay dropped an overlong sequence: %q", name, got)
		}
	}
}

// The overwhelmingly common replay contains no query at all, and pays nothing
// for the filter: it comes back as the same backing array.
func TestSanitizeReplayReturnsInputWhenNothingToDrop(t *testing.T) {
	in := []byte("plain output\r\n\x1b[32mgreen\x1b[0m\r\n")
	got := sanitizeReplay(in)
	if len(got) != len(in) || &got[0] != &in[0] {
		t.Errorf("sanitizeReplay copied a replay it did not change")
	}
}

// An OSC abandoned by a fresh escape leaves the string state in a real
// terminal, and the sequence that abandoned it is parsed — and answered — as
// usual. The filter has to reach it there too.
func TestSanitizeReplayFindsQueryAfterAbandonedString(t *testing.T) {
	got := sanitizeReplay([]byte("x\x1b]0;title\x1b[cy"))
	if want := "x\x1b]0;titley"; string(got) != want {
		t.Errorf("sanitizeReplay = %q, want %q", got, want)
	}
}

// The end of the round trip: a client attaching to a session whose buffer holds
// a query is primed with the drawing and not with the question, and the
// attached frame's byte count describes what was actually sent.
func TestAttachReplayCarriesNoQuery(t *testing.T) {
	s := &Session{
		ID:      "attach-replay",
		buffer:  NewRingBuffer(4096),
		clients: make(map[Client]clientGeom),
	}
	// What a Claude Code start leaves behind, between two lines of output.
	s.buffer.Write([]byte("before\r\n\x1b[>0q\x1b[c\x1b[?1049hafter"))

	c := &recordingClient{id: "c1"}
	s.attach(c)

	c.mu.Lock()
	sent := bytes.Join(c.sent, nil)
	controls, _ := c.controls, 0
	c.mu.Unlock()

	if bytes.Contains(sent, []byte("\x1b[c")) || bytes.Contains(sent, []byte("\x1b[>0q")) {
		t.Errorf("attach replayed a terminal query: %q", sent)
	}
	if !bytes.Contains(sent, []byte("before")) || !bytes.Contains(sent, []byte("after")) ||
		!bytes.Contains(sent, []byte("\x1b[?1049h")) {
		t.Errorf("attach lost replay content: %q", sent)
	}
	if len(controls) != 1 {
		t.Fatalf("attach sent %d control frames, want 1", len(controls))
	}
	att, ok := controls[0].(AttachedMsg)
	if !ok {
		t.Fatalf("attach control frame is %T, want AttachedMsg", controls[0])
	}
	if att.ReplayBytes != len(sent) {
		t.Errorf("attached replayBytes = %d, want %d (the bytes actually sent)",
			att.ReplayBytes, len(sent))
	}
}
