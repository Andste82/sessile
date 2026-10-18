package storage

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/session"
)

// currentColumns is the sessions table every migrated database must end up
// with, whatever it started from.
var currentColumns = []string{
	"created", "directory", "group_name", "host_display_name", "host_id", "id",
	"last_activity", "name", "shell", "status", "target_type", "user_id",
}

// loadFixture builds a database file from one of the SQL dumps in testdata/.
func loadFixture(t *testing.T, name string) string {
	t.Helper()
	dump, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return path
}

// rawVersion reads user_version without going through Open.
func rawVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// columns lists the sessions table's columns, sorted.
func columns(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.db.Query(`SELECT name FROM pragma_table_info('sessions')`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// dumpRows renders every row as one string, so two opens can be compared for
// "changed nothing" without caring about the shape of Info.
func dumpRows(t *testing.T, st *Store) string {
	t.Helper()
	rows, err := st.db.Query(`SELECT ` + selectColumns + ` FROM sessions ORDER BY id`)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var cols [12]string
		ptrs := make([]any, len(cols))
		for i := range cols {
			ptrs[i] = &cols[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		b.WriteString(strings.Join(cols[:], "|"))
		b.WriteByte('\n')
	}
	return b.String()
}

// Every released schema migrates to the current one with its rows intact, and
// a second start changes nothing (§12e M31). The fixtures are dumps of real
// databases from those releases, so this is the upgrade an operator does.
func TestMigrateReleasedSchemas(t *testing.T) {
	type want struct {
		name, userID, group string
		target              session.TargetType
	}
	cases := []struct {
		fixture string
		rows    map[string]want
	}{
		{
			// Before auth: no owner, every session local.
			fixture: "v0.1-v0.3.sql",
			rows: map[string]want{
				"e7947b55-cd96-486a-bdec-f17e12331b15": {"build", "", "", session.TargetLocal},
				"431c2938-4113-4d5a-a275-2a90e670d35a": {"logs", "", "", session.TargetLocal},
				"crashed-0000-0000-0000-000000000000":  {"crashed", "", "", session.TargetLocal},
			},
		},
		{
			fixture: "v0.5.sql",
			rows: map[string]want{
				"49785ada-aa2a-4736-94b2-03a860f056fa": {"build", "d02a1eb2-1b44-4200-b40c-2451058e9f62", "", session.TargetLocal},
				"de2b5bc8-dab9-4f62-b9fd-8758466bb6a9": {"logs", "d02a1eb2-1b44-4200-b40c-2451058e9f62", "", session.TargetLocal},
			},
		},
		{
			fixture: "v0.6-v0.7.sql",
			rows: map[string]want{
				"c4b82559-943b-408d-87f6-d6d4cdebb6b1": {"build", "9eec50f7-0deb-4d9f-b223-9d3bdd43bb58", "infra", session.TargetLocal},
				"2917abae-81c0-4d14-bcd8-771af6b8f40f": {"logs", "9eec50f7-0deb-4d9f-b223-9d3bdd43bb58", "", session.TargetLocal},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			path := loadFixture(t, tc.fixture)
			if v := rawVersion(t, path); v != 0 {
				t.Fatalf("fixture user_version = %d, want 0 (pre-versioning)", v)
			}

			st, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if got := columns(t, st); strings.Join(got, ",") != strings.Join(currentColumns, ",") {
				t.Errorf("columns = %v, want %v", got, currentColumns)
			}

			stopped, err := st.LoadStopped()
			if err != nil {
				t.Fatalf("LoadStopped: %v", err)
			}
			// Every row is there and stopped — including one left running by
			// a crash, which Open reconciles.
			if len(stopped) != len(tc.rows) {
				t.Fatalf("LoadStopped returned %d rows, want %d", len(stopped), len(tc.rows))
			}
			for _, got := range stopped {
				w, ok := tc.rows[got.ID]
				if !ok {
					t.Errorf("unexpected row %s", got.ID)
					continue
				}
				if got.Name != w.name || got.UserID != w.userID || got.Group != w.group || got.TargetType != w.target {
					t.Errorf("row %s = {name %q user %q group %q target %q}, want %+v",
						got.ID, got.Name, got.UserID, got.Group, got.TargetType, w)
				}
			}
			ownerless := 0
			for _, w := range tc.rows {
				if w.userID == "" {
					ownerless++
				}
			}
			if n, err := st.CountOwnerless(); err != nil || n != ownerless {
				t.Errorf("CountOwnerless = (%d, %v), want %d", n, err, ownerless)
			}
			before := dumpRows(t, st)
			st.Close()

			if v := rawVersion(t, path); v != SchemaVersion() {
				t.Fatalf("user_version after Open = %d, want %d", v, SchemaVersion())
			}

			// A second start: nothing pending, nothing changed.
			st2, err := Open(path)
			if err != nil {
				t.Fatalf("second Open: %v", err)
			}
			after := dumpRows(t, st2)
			st2.Close()
			if after != before {
				t.Errorf("second Open changed rows:\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if v := rawVersion(t, path); v != SchemaVersion() {
				t.Errorf("user_version after second Open = %d, want %d", v, SchemaVersion())
			}
		})
	}
}

// A fresh database goes through the same steps and lands at the same shape.
func TestMigrateFreshDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := columns(t, st); strings.Join(got, ",") != strings.Join(currentColumns, ",") {
		t.Errorf("columns = %v, want %v", got, currentColumns)
	}
	st.Close()
	if v := rawVersion(t, path); v != SchemaVersion() {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion())
	}
}

// A database a newer release has migrated is refused before anything touches
// it: older code must never write rows into a schema it does not know.
func TestOpenRefusesNewerSchema(t *testing.T) {
	path := loadFixture(t, "v0.6-v0.7.sql")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	newer := SchemaVersion() + 1
	if _, err := db.Exec(`PRAGMA user_version = ` + strconv.Itoa(newer)); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	// A running row Open would otherwise reconcile: proof it wrote nothing.
	if _, err := db.Exec(`UPDATE sessions SET status='running'`); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	db.Close()

	_, err = Open(path)
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) {
		t.Fatalf("Open error = %v, want *SchemaTooNewError", err)
	}
	if tooNew.Found != newer || tooNew.Known != SchemaVersion() {
		t.Errorf("error versions = found %d known %d, want %d and %d",
			tooNew.Found, tooNew.Known, newer, SchemaVersion())
	}
	if msg := err.Error(); !strings.Contains(msg, strconv.Itoa(newer)) || !strings.Contains(msg, strconv.Itoa(SchemaVersion())) {
		t.Errorf("error %q does not name both versions", msg)
	}

	if v := rawVersion(t, path); v != newer {
		t.Errorf("user_version = %d after refusal, want it untouched at %d", v, newer)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen raw db: %v", err)
	}
	defer db.Close()
	var running int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE status='running'`).Scan(&running); err != nil {
		t.Fatalf("count running: %v", err)
	}
	if running != 2 {
		t.Errorf("%d rows still running, want 2: the refused Open reconciled rows", running)
	}
}

// A failing step leaves the database exactly at the version before it — the
// step's own changes rolled back with it — and the next start retries it.
func TestFailedMigrationRollsBack(t *testing.T) {
	path := loadFixture(t, "v0.1-v0.3.sql")

	saved := migrations
	t.Cleanup(func() { migrations = saved })
	boom := errors.New("boom")
	migrations = append(append([]func(*sql.Tx) error{}, saved...), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN half_done TEXT`); err != nil {
			return err
		}
		return boom
	})

	if _, err := Open(path); !errors.Is(err, boom) {
		t.Fatalf("Open error = %v, want the failing step's error", err)
	}
	if v := rawVersion(t, path); v != len(saved) {
		t.Fatalf("user_version = %d, want %d: the steps before the failed one stay committed",
			v, len(saved))
	}

	migrations = saved
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open after removing the failing step: %v", err)
	}
	defer st.Close()
	for _, c := range columns(t, st) {
		if c == "half_done" {
			t.Fatal("the failed step's column survived its rollback")
		}
	}
}
