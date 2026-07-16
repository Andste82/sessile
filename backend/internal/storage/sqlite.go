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
