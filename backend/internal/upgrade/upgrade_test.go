package upgrade

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/storage"
)

// oldDataDir builds a data directory the way a v0.5 release leaves it: a
// pre-versioning sessions.db, config.yml, users.yml and one user's hosts.yml.
func oldDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dump, err := os.ReadFile(filepath.Join("..", "storage", "testdata", "v0.5.sql"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "sessions.db"))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	db.Close()

	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.yml", "allowLocalHost: true\n")
	write("users.yml", "- id: u1\n  username: admin\n")
	write("users/u1/hosts.yml", "- id: h1\n  password: secret\n")
	write("users/u1/env.yml", "env:\n  - name: TOKEN\n    value: env-secret\n    secret: true\n")
	return dir
}

// openWithBackup opens dir's database the way cmd/server does.
func openWithBackup(dir string, now time.Time) (*storage.Store, string, error) {
	var backup string
	st, err := storage.OpenWith(filepath.Join(dir, "sessions.db"), storage.Options{
		BeforeMigrate: func(p storage.Pending) error {
			var err error
			backup, err = Backup(dir, "schema-v0", p.Snapshot, now)
			return err
		},
	})
	return st, backup, err
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A migration is preceded by a complete, private backup of the state as it
// was before the migration.
func TestBackupBeforeMigration(t *testing.T) {
	dir := oldDataDir(t)
	now := time.Date(2026, 10, 18, 19, 0, 2, 0, time.UTC)

	st, backup, err := openWithBackup(dir, now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()

	want := filepath.Join(dir, "backups", "20261018T190002Z-schema-v0")
	if backup != want {
		t.Fatalf("backup dir = %q, want %q", backup, want)
	}
	if v := userVersion(t, filepath.Join(dir, "sessions.db")); v != storage.SchemaVersion() {
		t.Errorf("live db user_version = %d, want %d: the migration did not run", v, storage.SchemaVersion())
	}
	// The snapshot is from before the migration.
	if v := userVersion(t, filepath.Join(backup, "sessions.db")); v != 0 {
		t.Errorf("backup user_version = %d, want 0", v)
	}
	db, err := sql.Open("sqlite", filepath.Join(backup, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&rows); err != nil {
		t.Fatalf("query backup: %v", err)
	}
	db.Close()
	if rows != 2 {
		t.Errorf("backup has %d sessions, want 2", rows)
	}

	for _, rel := range []string{"sessions.db", "config.yml", "users.yml", "users/u1/hosts.yml", "users/u1/env.yml"} {
		fi, err := os.Stat(filepath.Join(backup, rel))
		if err != nil {
			t.Errorf("backup is missing %s: %v", rel, err)
			continue
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", rel, fi.Mode().Perm())
		}
	}
	if env, _ := os.ReadFile(filepath.Join(backup, "users/u1/env.yml")); !strings.Contains(string(env), "env-secret") {
		t.Errorf("env.yml copy = %q, want the original content", env)
	}
	got, _ := os.ReadFile(filepath.Join(backup, "users/u1/hosts.yml"))
	if !strings.Contains(string(got), "secret") {
		t.Errorf("hosts.yml copy = %q, want the original content", got)
	}
	for _, d := range []string{filepath.Join(dir, "backups"), backup, filepath.Join(backup, "users", "u1")} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %v, want 0700", d, fi.Mode().Perm())
		}
	}

	// Nothing pending on the next start: no second backup.
	st, backup, err = openWithBackup(dir, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	st.Close()
	if backup != "" {
		t.Errorf("second open wrote backup %s with no migration pending", backup)
	}
}

// A new database has nothing to back up, so the hook is not called.
func TestNoBackupForNewDatabase(t *testing.T) {
	dir := t.TempDir()
	st, backup, err := openWithBackup(dir, time.Now())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	st.Close()
	if backup != "" {
		t.Errorf("backup %s written for a new database", backup)
	}
	if _, err := os.Stat(BackupsDir(dir)); !os.IsNotExist(err) {
		t.Errorf("backups/ exists for a new database (stat err %v)", err)
	}
}

// A v0.3 data directory has only sessions.db: missing YAML is not an error.
func TestBackupWithoutYAML(t *testing.T) {
	dir := t.TempDir()
	backup, err := Backup(dir, "schema-v0", nil, time.Now())
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	entries, err := os.ReadDir(backup)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("backup of an empty dir has %d entries, want 0", len(entries))
	}
}

// If the backup cannot be written, the migration does not run: the database
// stays exactly where it was, and no half-written backup is left behind.
func TestFailedBackupStopsMigration(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := oldDataDir(t)
	backups := BackupsDir(dir)
	if err := os.Mkdir(backups, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(backups, 0o700) })

	_, _, err := openWithBackup(dir, time.Now())
	if err == nil {
		t.Fatal("open succeeded with a read-only backups/ dir")
	}
	if v := userVersion(t, filepath.Join(dir, "sessions.db")); v != 0 {
		t.Errorf("user_version = %d after a failed backup, want 0 (not migrated)", v)
	}
	entries, _ := os.ReadDir(backups)
	if len(entries) != 0 {
		t.Errorf("backups/ has %d entries after a failed backup, want 0", len(entries))
	}
}

// A snapshot that fails part-way leaves no backup directory, partial or not.
func TestFailedSnapshotLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("boom")
	_, err := Backup(dir, "schema-v0", func(dest string) error {
		os.WriteFile(dest, []byte("half"), 0o600)
		return boom
	}, time.Now())
	if !errors.Is(err, boom) {
		t.Fatalf("Backup error = %v, want boom", err)
	}
	entries, _ := os.ReadDir(BackupsDir(dir))
	if len(entries) != 0 {
		t.Errorf("backups/ has %v after a failed snapshot, want nothing", entries)
	}
}

// Only the newest KeepBackups survive; anything not named like a backup is
// not ours to delete.
func TestBackupPrunesOldest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(BackupsDir(dir), "keep-me"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var made []string
	for i := 0; i < KeepBackups+2; i++ {
		b, err := Backup(dir, "schema-v0", nil, start.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
		made = append(made, filepath.Base(b))
	}

	entries, err := os.ReadDir(BackupsDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := append(append([]string{}, made[len(made)-KeepBackups:]...), "keep-me")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("backups/ = %v, want %v", got, want)
	}
}

// A ".partial" left by a backup that was killed mid-way is not a backup: it
// must not count towards KeepBackups, so it cannot push a real backup out a
// generation early, and pruning must not touch it either.
func TestBackupPruneIgnoresStalePartial(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(BackupsDir(dir), "20250101T000000Z-schema-v0.partial")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var made []string
	for i := 0; i < KeepBackups; i++ {
		b, err := Backup(dir, "schema-v0", nil, start.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatalf("Backup %d: %v", i, err)
		}
		made = append(made, b)
	}
	for _, b := range made {
		if _, err := os.Stat(b); err != nil {
			t.Errorf("real backup %s was pruned with only %d real backups present: %v",
				filepath.Base(b), KeepBackups, err)
		}
	}
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("stale .partial was removed by pruning: %v", err)
	}
}

// Two backups in the same second get distinct names.
func TestBackupSameSecond(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a, err := Backup(dir, "adopt", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Backup(dir, "adopt", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both backups named %s", a)
	}
}

func TestStateRoundTripAndLayoutGuard(t *testing.T) {
	dir := t.TempDir()

	s, found, err := ReadState(dir)
	if err != nil || found || s != (State{}) {
		t.Fatalf("ReadState on a new dir = (%+v, %v, %v), want zero, false, nil", s, found, err)
	}
	if err := CheckLayout(dir, s); err != nil {
		t.Errorf("CheckLayout on no state: %v", err)
	}

	want := State{LastVersion: "0.8.0", LayoutVersion: LayoutVersion}
	if err := WriteState(dir, want); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	s, found, err = ReadState(dir)
	if err != nil || !found || s != want {
		t.Fatalf("ReadState = (%+v, %v, %v), want %+v", s, found, err, want)
	}

	newer := State{LastVersion: "9.0.0", LayoutVersion: LayoutVersion + 1}
	err = CheckLayout(dir, newer)
	var tooNew *LayoutTooNewError
	if !errors.As(err, &tooNew) {
		t.Fatalf("CheckLayout(newer) = %v, want *LayoutTooNewError", err)
	}
	if !strings.Contains(err.Error(), "9.0.0") {
		t.Errorf("error %q does not name the release that wrote it", err)
	}
}
