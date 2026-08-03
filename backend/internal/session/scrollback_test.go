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
	got := string(restoreSeparator(at))

	for _, want := range []string{
		"\x1b[?1049l", // leave alternate screen
		"\x1b[0m",     // reset attributes
		"\x1b[?25h",   // show cursor
		"\x1b[?7h",    // re-enable autowrap
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
