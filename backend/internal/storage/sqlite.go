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

// schema is applied on Open (idempotent). Metadata only — the live PTY, ring
// buffer and geometry are runtime state and are never persisted (§8).
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

// migrationColumns adds columns introduced after the original schema, each
// guarded by a column-existence check so running it is safe on every
// startup regardless of which schema version the database was created
// under — the first such migration this project has needed (§12b M17):
// sessions gained ownership and an SSH target once auth and hosts arrived.
// Rows written before this migration get an empty user_id and target_type=local
// — the former makes them invisible under strict per-owner scoping (§10,
// §4.3), an expected, one-time consequence of adding auth to a previously
// single-tenant table; the latter is simply correct, since every session
// before this milestone was a local one.
var migrationColumns = []struct{ name, ddl string }{
	{"user_id", `ALTER TABLE sessions ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`},
	{"target_type", `ALTER TABLE sessions ADD COLUMN target_type TEXT NOT NULL DEFAULT 'local'`},
	{"host_id", `ALTER TABLE sessions ADD COLUMN host_id TEXT NOT NULL DEFAULT ''`},
	{"host_display_name", `ALTER TABLE sessions ADD COLUMN host_display_name TEXT NOT NULL DEFAULT ''`},
}

// migrate applies migrationColumns, skipping any column that already exists.
func migrate(db *sql.DB) error {
	for _, m := range migrationColumns {
		exists, err := columnExists(db, m.name)
		if err != nil {
			return fmt.Errorf("check column %s: %w", m.name, err)
		}
		if exists {
			continue
		}
		if _, err := db.Exec(m.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", m.name, err)
		}
	}
	return nil
}

// columnExists reports whether the sessions table already has column —
// always that one hardcoded table, so this never interpolates anything
// caller-supplied into the PRAGMA statement.
func columnExists(db *sql.DB, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(sessions)`)
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

// Store is a SQLite-backed session metadata store.
type Store struct {
	db *sql.DB
}

// Open opens (creating parent dirs and the file as needed) the database at
// path, applies the schema, and reconciles orphaned sessions: any row still
// marked running belongs to a shell that died with the previous process, so it
// is transitioned to stopped (§3, §8).
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

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
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
