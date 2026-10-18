package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/auth"
	"github.com/Andste82/sessile/backend/internal/storage"
	"github.com/Andste82/sessile/backend/internal/upgrade"
)

// adoptDataDir builds the data directory of a 0.3 install that has since been
// upgraded and has an admin: the 0.3 sessions.db (three ownerless sessions),
// and a users.yml with alice in it.
func adoptDataDir(t *testing.T) (dir, aliceID string) {
	t.Helper()
	dir = t.TempDir()
	dump, err := os.ReadFile(filepath.Join("..", "..", "internal", "storage", "testdata", "v0.1-v0.3.sql"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	db.Close()

	users, err := auth.OpenUsers(filepath.Join(dir, "users.yml"))
	if err != nil {
		t.Fatal(err)
	}
	alice, err := users.CreateFirstAdmin("alice", "alice-password-123")
	if err != nil {
		t.Fatal(err)
	}
	return dir, alice.ID
}

func adopt(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runAdopt(args, &out, &errOut)
	return out.String(), err
}

func backups(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(upgrade.BackupsDir(dir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAdoptSessions(t *testing.T) {
	dir, aliceID := adoptDataDir(t)

	// Case-insensitive, like login.
	out, err := adopt(t, "--data-dir="+dir, "--user=ALICE")
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !strings.Contains(out, "Adopted 3 session(s) for alice") {
		t.Errorf("output = %q, want the count and the user", out)
	}
	// It migrated the 0.3 schema first (backup), then backed up again before
	// adopting.
	if b := backups(t, dir); len(b) != 2 || !strings.HasSuffix(b[0], "-adopt") && !strings.HasSuffix(b[1], "-adopt") {
		t.Errorf("backups = %v, want a schema-v0 and an adopt backup", b)
	}

	st, err := storage.Open(filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := st.LoadStopped()
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 3 {
		t.Fatalf("%d stopped sessions, want 3", len(stopped))
	}
	for _, s := range stopped {
		if s.UserID != aliceID {
			t.Errorf("session %s owner = %q, want alice (%s)", s.ID, s.UserID, aliceID)
		}
	}

	// A second run finds nothing, and changes nothing — not even a backup.
	before := backups(t, dir)
	out, err = adopt(t, "--data-dir="+dir, "--user=alice")
	if err != nil {
		t.Fatalf("second adopt: %v", err)
	}
	if !strings.Contains(out, "No sessions without an owner") {
		t.Errorf("second output = %q, want nothing to do", out)
	}
	if after := backups(t, dir); strings.Join(after, ",") != strings.Join(before, ",") {
		t.Errorf("second run changed backups: %v → %v", before, after)
	}
}

// The ways to get it wrong all stop before the directory is touched: no
// migration, no backup, no new files.
func TestAdoptSessionsRefuses(t *testing.T) {
	dir, _ := adoptDataDir(t)
	empty := t.TempDir()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no user", []string{"--data-dir=" + dir}, "--user is required"},
		{"unknown user", []string{"--data-dir=" + dir, "--user=bob"}, `no user "bob"`},
		{"wrong data dir", []string{"--data-dir=" + empty, "--user=alice"}, "no sessions.db"},
		{"stray argument", []string{"--data-dir=" + dir, "--user=alice", "extra"}, "unexpected argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adopt(t, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}

	if b := backups(t, dir); len(b) != 0 {
		t.Errorf("refused runs wrote backups %v", b)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 0 {
		t.Errorf("user_version = %d after refused runs, want 0 (not migrated)", v)
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("a run against the wrong directory created %v in it", entries)
	}
}

// Without any account there is nobody to adopt for; it says what to do.
func TestAdoptSessionsWithoutUsers(t *testing.T) {
	dir, _ := adoptDataDir(t)
	if err := os.Remove(filepath.Join(dir, "users.yml")); err != nil {
		t.Fatal(err)
	}
	_, err := adopt(t, "--data-dir="+dir, "--user=alice")
	if err == nil || !strings.Contains(err.Error(), "create the admin account") {
		t.Fatalf("error = %v, want a pointer at creating the admin first", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "users.yml")); !os.IsNotExist(err) {
		t.Error("adopt-sessions created users.yml")
	}
}
