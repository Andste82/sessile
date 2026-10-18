// Package storage persists session metadata in SQLite via the pure-Go
// modernc.org/sqlite driver (no CGO). It implements session.Store.
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// schema is the original v0.1 table. Metadata only — the live PTY, ring
// buffer and geometry are runtime state and are never persisted (§8). It is
// not the current shape: migrations below take it there, and a fresh database
// goes through exactly the same steps as an old one.
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  directory     TEXT NOT NULL,
  shell         TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'running',
  created       TEXT NOT NULL,
  last_activity TEXT NOT NULL
);`

// migrations is the schema's history, in order (§8, §12e M31). Step i takes
// the database from user_version i to i+1, so the current version is
// len(migrations). Steps are only ever appended: a released step is never
// edited or reordered, since some database out there is already past it.
//
// Step 1 is the one exception to "a step may assume the exact schema before
// it". user_version 0 is every database written before versioning existed,
// whichever release wrote it — v0.1–v0.3 have the bare table, v0.5 has four
// more columns, v0.6+ has group_name too — so step 1 checks before it adds,
// and brings all of them to the same version-1 shape. Every later step can
// rely on that shape and may rewrite data, not only add columns.
var migrations = []func(*sql.Tx) error{
	migrateLegacyColumns,
}

// SchemaVersion is the user_version this build migrates a database to.
func SchemaVersion() int { return len(migrations) }

// SchemaTooNewError is returned by Open for a database a newer release has
// already migrated past what this one knows. Running on it anyway would mean
// writing rows without columns the newer schema relies on, so Open refuses.
type SchemaTooNewError struct {
	Path  string
	Found int // the database's user_version
	Known int // SchemaVersion() of this build
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("%s has schema version %d, but this release only knows up to %d: "+
		"it was written by a newer sessile", e.Path, e.Found, e.Known)
}

// legacyColumns are the columns added after the original schema, before
// versioning existed — each one used to be checked for and added on every
// start. §12b M17 added the first four when sessions gained ownership and an
// SSH target; §12d M27 added group_name.
//
// Rows written before M17 get an empty user_id and target_type=local. The
// latter is simply correct, since every session before that milestone was a
// local one. The former makes them invisible under strict per-owner scoping
// (§10, §4.3) until an operator adopts them (§12e M34).
var legacyColumns = []struct{ name, ddl string }{
	{"user_id", `ALTER TABLE sessions ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`},
	{"target_type", `ALTER TABLE sessions ADD COLUMN target_type TEXT NOT NULL DEFAULT 'local'`},
	{"host_id", `ALTER TABLE sessions ADD COLUMN host_id TEXT NOT NULL DEFAULT ''`},
	{"host_display_name", `ALTER TABLE sessions ADD COLUMN host_display_name TEXT NOT NULL DEFAULT ''`},
	// group_name, not group: GROUP is a SQL keyword, and a column that has to
	// be quoted at every use is a trap for the next query written by hand.
	// Rows written before this column get '', which is exactly "no group"
	// (§4.11) — nothing to backfill.
	{"group_name", `ALTER TABLE sessions ADD COLUMN group_name TEXT NOT NULL DEFAULT ''`},
}

// migrateLegacyColumns is step 1: create the table if this is a new database,
// then add whichever legacy column is missing.
func migrateLegacyColumns(tx *sql.Tx) error {
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	for _, c := range legacyColumns {
		exists, err := columnExists(tx, c.name)
		if err != nil {
			return fmt.Errorf("check column %s: %w", c.name, err)
		}
		if exists {
			continue
		}
		if _, err := tx.Exec(c.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", c.name, err)
		}
	}
	return nil
}

// columnExists reports whether the sessions table already has column —
// always that one hardcoded table, so this never interpolates anything
// caller-supplied into the PRAGMA statement.
func columnExists(tx *sql.Tx, column string) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// userVersion reads the schema version from the database header.
func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

// migrate runs every step after the database's current version. Each step
// shares one transaction with its user_version bump: SQLite DDL is
// transactional and user_version lives in the database header, so a step
// that fails leaves both the schema and the version where they were — never a
// half-applied step that the next start would try to apply again.
func migrate(db *sql.DB, from int) error {
	for v := from; v < len(migrations); v++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", v+1, err)
		}
		if err := migrations[v](tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		// PRAGMA takes no bound parameters. v+1 is an int from this loop,
		// never anything caller-supplied.
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: set user_version: %w", v+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", v+1, err)
		}
	}
	return nil
}

// Store is a SQLite-backed session metadata store.
type Store struct {
	db *sql.DB
}

// Open opens (creating parent dirs and the file as needed) the database at
// path, migrates it to SchemaVersion, and reconciles orphaned sessions: any
// row still marked running belongs to a shell that died with the previous
// process, so it is transitioned to stopped (§3, §8).
//
// A database already past SchemaVersion is refused with *SchemaTooNewError
// before anything is written to it.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc.org/sqlite is a single connection engine under concurrency; a
	// single open connection keeps writes serialized and avoids lock churn.
	db.SetMaxOpenConns(1)

	version, err := userVersion(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	if version > len(migrations) {
		db.Close()
		return nil, &SchemaTooNewError{Path: path, Found: version, Known: len(migrations)}
	}
	if err := migrate(db, version); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if _, err := db.Exec(`UPDATE sessions SET status='stopped' WHERE status='running'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("reconcile running sessions: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	return s.db.Close()
}
