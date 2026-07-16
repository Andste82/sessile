package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Andste82/sessile/backend/internal/session"
)

// Insert upserts a session's metadata (used on create and rename).
func (s *Store) Insert(i session.Info) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, name, directory, shell, status, created, last_activity)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name=excluded.name,
		   directory=excluded.directory,
		   shell=excluded.shell,
		   status=excluded.status,
		   last_activity=excluded.last_activity`,
		i.ID, i.Name, i.Directory, i.Shell, string(i.Status),
		i.Created.UTC().Format(time.RFC3339), i.LastActivity.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// SetStatus updates a session's lifecycle status.
func (s *Store) SetStatus(id string, status session.Status) error {
	_, err := s.db.Exec(`UPDATE sessions SET status=? WHERE id=?`, string(status), id)
	if err != nil {
		return fmt.Errorf("set status: %w", err)
	}
	return nil
}

// Touch updates a session's last-activity timestamp.
func (s *Store) Touch(id string, lastActivity time.Time) error {
	_, err := s.db.Exec(`UPDATE sessions SET last_activity=? WHERE id=?`,
		lastActivity.UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Delete removes a session row.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Get returns a single session's persisted metadata.
func (s *Store) Get(id string) (session.Info, bool, error) {
	row := s.db.QueryRow(
		`SELECT id, name, directory, shell, status, created, last_activity
		 FROM sessions WHERE id=?`, id)
	info, err := scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Info{}, false, nil
	}
	if err != nil {
		return session.Info{}, false, err
	}
	return info, true, nil
}

// LoadStopped returns all sessions persisted with stopped status.
func (s *Store) LoadStopped() ([]session.Info, error) {
	rows, err := s.db.Query(
		`SELECT id, name, directory, shell, status, created, last_activity
		 FROM sessions WHERE status='stopped'`)
	if err != nil {
		return nil, fmt.Errorf("query stopped: %w", err)
	}
	defer rows.Close()

	var out []session.Info
	for rows.Next() {
		info, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// scanner abstracts *sql.Row and *sql.Rows for scan.
type scanner interface {
	Scan(dest ...any) error
}

func scan(sc scanner) (session.Info, error) {
	var (
		info                     session.Info
		status, created, lastAct string
	)
	if err := sc.Scan(&info.ID, &info.Name, &info.Directory, &info.Shell,
		&status, &created, &lastAct); err != nil {
		return session.Info{}, err
	}
	info.Status = session.Status(status)
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		info.Created = t
	}
	if t, err := time.Parse(time.RFC3339, lastAct); err == nil {
		info.LastActivity = t
	}
	// PID/Rows/Cols are runtime-only; a persisted (stopped) session has none.
	return info, nil
}
