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

// restoreSeparator is written between a restored snapshot and the output of the
// shell that replaces it.
//
// The leading escapes are not decoration. A snapshot is raw PTY bytes, and a
// session that was in htop when it stopped ends mid alternate-screen: replaying
// it verbatim would leave the new shell drawing into a screen the user cannot
// scroll, with a hidden cursor and possibly inverse attributes. Undoing those
// modes is what makes the restored buffer safe to prepend to a live session.
func restoreSeparator(at time.Time) []byte {
	const (
		leaveAltScreen = "\x1b[?1049l"
		resetAttrs     = "\x1b[0m"
		showCursor     = "\x1b[?25h"
		enableWrap     = "\x1b[?7h"
		dim            = "\x1b[2m"
	)
	return []byte(leaveAltScreen + resetAttrs + showCursor + enableWrap +
		"\r\n" + dim + "── restored " + at.UTC().Format(time.RFC3339) +
		" ── output above is from the previous run ──" + resetAttrs + "\r\n")
}

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
