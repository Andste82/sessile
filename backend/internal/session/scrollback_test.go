package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScrollbackStoreRoundTrip(t *testing.T) {
	st := NewScrollbackStore(t.TempDir())
	const id = "11111111-2222-3333-4444-555555555555"
	want := []byte("\x1b[32mhello\x1b[0m\r\n")

	if err := st.Save(id, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := st.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Load = %q, want %q", got, want)
	}

	// Saving again must replace, not append.
	second := []byte("second")
	if err := st.Save(id, second); err != nil {
		t.Fatalf("Save (replace): %v", err)
	}
	if got, _ := st.Load(id); !bytes.Equal(got, second) {
		t.Errorf("after replace Load = %q, want %q", got, second)
	}

	if err := st.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := st.Load(id); err != nil || got != nil {
		t.Errorf("Load after Delete = (%q, %v), want (nil, nil)", got, err)
	}
	// Deleting what is already gone is not an error.
	if err := st.Delete(id); err != nil {
		t.Errorf("Delete of missing snapshot: %v", err)
	}
}

// A session that has never been saved is the normal case for a fresh session,
// so it must not read as a failure.
func TestScrollbackStoreLoadMissing(t *testing.T) {
	st := NewScrollbackStore(t.TempDir())
	got, err := st.Load("no-such-session-id")
	if err != nil {
		t.Fatalf("Load of missing snapshot: %v", err)
	}
	if got != nil {
		t.Errorf("Load = %q, want nil", got)
	}
}

// Save must leave no temp files behind, so a crashed backend cannot accumulate
// half-written snapshots in the data directory.
func TestScrollbackStoreLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	st := NewScrollbackStore(dir)
	const id = "abc123"
	if err := st.Save(id, []byte("x")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "scrollback"))
	if err != nil {
		t.Fatalf("read scrollback dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != id+".bin" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("scrollback dir = %v, want exactly [%s.bin]", names, id)
	}
}

// The id reaches the store straight from the request URL, so it is a path
// injection vector and must be validated (§4.5).
func TestScrollbackStoreRejectsUnsafeIDs(t *testing.T) {
	st := NewScrollbackStore(t.TempDir())
	for _, id := range []string{
		"", "..", "../etc/passwd", "a/b", `a\b`, ".", "with space",
		"tilde~", "null\x00byte", "---", strings.Repeat("a", 65),
	} {
		t.Run(id, func(t *testing.T) {
			if err := st.Save(id, []byte("x")); err == nil {
				t.Errorf("Save(%q) succeeded, want error", id)
			}
			if _, err := st.Load(id); err == nil {
				t.Errorf("Load(%q) succeeded, want error", id)
			}
			if err := st.Delete(id); err == nil {
				t.Errorf("Delete(%q) succeeded, want error", id)
			}
		})
	}
}

// The separator has to undo the terminal modes a snapshot may end in, or a
// session that stopped inside a full-screen program leaves the restarted shell
// drawing into the alternate screen.
func TestRestoreSeparatorResetsTerminalModes(t *testing.T) {
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	got := string(restoreSeparator(at, true))

	for _, want := range []string{
		"\x1b[?1049l", // leave alternate screen
		"\x1b[0m",     // reset attributes
		"\x1b[?25h",   // show cursor
		"\x1b[?7h",    // re-enable autowrap
		"\x1b[?1000l", // mouse tracking, all three levels
		"\x1b[?1002l",
		"\x1b[?1003l",
		"\x1b[?1006l", // and the report encodings
		"\x1b[?1004l", // focus reporting
		"\x1b[?2004l", // bracketed paste
		"\x1b[?1l",    // application cursor keys
		"\x1b>",       // application keypad
		"\x1b[r",      // scroll margins
	} {
		if !strings.Contains(got, want) {
			t.Errorf("separator %q missing escape %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "\x1b[?1049l") {
		t.Errorf("separator must leave the alternate screen first, got %q", got)
	}
	if !strings.Contains(got, "2026-08-03T12:00:00Z") {
		t.Errorf("separator %q does not carry the restore time", got)
	}
}

// DECRST 1049 also restores the cursor saved when the alternate screen was
// entered, and a terminal that never entered it restores to the top-left. Sent
// after an ordinary snapshot, it moved the cursor back to the top of the screen
// and the separator was drawn over the first lines of the restored history —
// the output it exists to sit below.
func TestRestoreSeparatorKeepsTheCursorOutsideTheAlternateScreen(t *testing.T) {
	at := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	got := string(restoreSeparator(at, false))

	if strings.Contains(got, "\x1b[?1049l") {
		t.Errorf("separator moves the cursor with 1049l outside the alternate screen: %q", got)
	}
	// The resets that do not move the cursor stay unconditional.
	for _, want := range []string{"\x1b[0m", "\x1b[?25h", "\x1b[?7h", "\x1b[?1003l"} {
		if !strings.Contains(got, want) {
			t.Errorf("separator %q missing escape %q", got, want)
		}
	}
	// DECSTBM homes the cursor, which is the same trap 1049l was. It may only
	// appear between a save and a restore of the cursor.
	save, region, restore := strings.Index(got, "\x1b7"), strings.Index(got, "\x1b[r"), strings.Index(got, "\x1b8")
	if save < 0 || restore < 0 || !(save < region && region < restore) {
		t.Errorf("separator resets the scroll region unguarded by DECSC/DECRC: %q", got)
	}
	if !strings.Contains(got, "── restored 2026-08-03T12:00:00Z") {
		t.Errorf("separator %q does not carry the restore banner", got)
	}
}

// The case this exists for: a program that was killed rather than closed. It
// never turned its input modes off, so the snapshot ends with them on, and
// without the separator undoing them the restarted shell inherits them — with
// mouse tracking that means a report and a bell for every mouse move across the
// window, which is a session nobody can type in.
func TestRestoreSeparatorEndsWithInputModesOff(t *testing.T) {
	// How a real snapshot ends when Claude Code is killed: several modes in one
	// sequence, no reset after it.
	snapshot := []byte("$ claude\r\n\x1b[?1004h\x1b[?1000;1002;1003;1006h\x1b[?2004h" +
		"drawing…\x1b[?1049h\x1b[H\x1b[?25l")
	replay := append(snapshot, restoreSeparator(
		time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC), endsInAltScreen(snapshot))...)

	for _, ps := range []string{"1000", "1002", "1003", "1006", "1004", "2004", "1049", "1"} {
		if finalModeState(replay, ps) {
			t.Errorf("mode ?%s is still set after the separator", ps)
		}
	}
	// And the modes a usable terminal needs are on.
	for _, ps := range []string{"25", "7"} {
		if !finalModeState(replay, ps) {
			t.Errorf("mode ?%s is not set after the separator", ps)
		}
	}
}

// finalModeState replays data and reports whether DEC private mode ps is set at
// the end of it, parsing sequences the way the scanner does — several modes can
// travel in one `ESC [ ? 1000 ; 1002 h`.
func finalModeState(data []byte, ps string) bool {
	set := false
	for i := 0; i+2 < len(data); i++ {
		if data[i] != 0x1b || data[i+1] != '[' || data[i+2] != '?' {
			continue
		}
		j := i + 3
		for j < len(data) && (data[j] == ';' || (data[j] >= '0' && data[j] <= '9')) {
			j++
		}
		if j >= len(data) || (data[j] != 'h' && data[j] != 'l') {
			continue
		}
		for _, p := range splitParams(data[i+3 : j]) {
			if p == ps {
				set = data[j] == 'h'
			}
		}
		i = j
	}
	return set
}

func TestEndsInAltScreen(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"empty", "", false},
		{"plain output", "$ ls\r\nfile\r\n$ ", false},
		{"entered and never left", "$ htop\r\n\x1b[?1049h\x1b[Hcpu", true},
		{"entered and left", "\x1b[?1049h\x1b[Hcpu\x1b[?1049l$ ", false},
		{"left then re-entered", "\x1b[?1049h\x1b[?1049l\x1b[?1049h", true},
		{"legacy 47", "\x1b[?47h drawing", true},
		{"legacy 1047 left", "\x1b[?1047h\x1b[?1047l", false},
		// vim sends the alternate-screen switch alongside other private modes.
		{"combined params", "\x1b[?1049;1004;2004h", true},
		{"combined reset", "\x1b[?1049h\x1b[?1049;2004l", false},
		// Unrelated private modes must not be mistaken for the alternate screen.
		{"bracketed paste only", "\x1b[?2004h\x1b[?25l", false},
		{"mode number is not a substring match", "\x1b[?10490h", false},
		// A ring buffer hands back a tail, so a snapshot can start mid-sequence.
		{"truncated head", "049h\x1b[Hcpu", false},
		{"truncated tail", "output\x1b[?1049", false},
		{"escape at the very end", "output\x1b[?", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := endsInAltScreen([]byte(tt.data)); got != tt.want {
				t.Errorf("endsInAltScreen(%q) = %v, want %v", tt.data, got, tt.want)
			}
		})
	}
}
