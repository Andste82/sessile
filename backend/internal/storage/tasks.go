package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// tasksSchema is the tasks table of PROJECT_PLAN.md §8: one row per task,
// 1:1 with the session that runs it, owner-scoped like sessions.
const tasksSchema = `
CREATE TABLE IF NOT EXISTS tasks (
  id          TEXT PRIMARY KEY,
  session_id  TEXT NOT NULL UNIQUE,
  user_id     TEXT NOT NULL,
  host_id     TEXT NOT NULL DEFAULT '',
  dir         TEXT NOT NULL DEFAULT '',
  spec_json   TEXT NOT NULL,
  summary     TEXT NOT NULL DEFAULT '',
  created     TEXT NOT NULL
);`

// TaskRow is one task as stored.
type TaskRow struct {
	ID        string
	SessionID string
	UserID    string
	HostID    string // "" for a local-host task
	Dir       string // absolute task folder on the target, known once it is created
	SpecJSON  string
	Summary   string
	Created   time.Time
}

// InsertTask stores a new task.
func (s *Store) InsertTask(t TaskRow) error {
	_, err := s.db.Exec(
		`INSERT INTO tasks (id, session_id, user_id, host_id, dir, spec_json, summary, created)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, t.UserID, t.HostID, t.Dir, t.SpecJSON, t.Summary,
		t.Created.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// GetTask returns the task id, scoped to userID: another user's task is
// reported exactly like a missing one (§14.5).
func (s *Store) GetTask(id, userID string) (TaskRow, bool, error) {
	row := s.db.QueryRow(`SELECT id, session_id, user_id, host_id, dir, spec_json, summary, created
	                      FROM tasks WHERE id=? AND user_id=?`, id, userID)
	var t TaskRow
	var created string
	err := row.Scan(&t.ID, &t.SessionID, &t.UserID, &t.HostID, &t.Dir, &t.SpecJSON, &t.Summary, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRow{}, false, nil
	}
	if err != nil {
		return TaskRow{}, false, fmt.Errorf("get task: %w", err)
	}
	if ct, err := time.Parse(time.RFC3339, created); err == nil {
		t.Created = ct
	}
	return t, true, nil
}

// SetTaskDir records where the task's folder is on its target.
func (s *Store) SetTaskDir(id, dir string) error {
	if _, err := s.db.Exec(`UPDATE tasks SET dir=? WHERE id=?`, dir, id); err != nil {
		return fmt.Errorf("set task dir: %w", err)
	}
	return nil
}

// SetTaskSummary records the agent's latest status line (§4.17.3).
func (s *Store) SetTaskSummary(id, summary string) error {
	if _, err := s.db.Exec(`UPDATE tasks SET summary=? WHERE id=?`, summary, id); err != nil {
		return fmt.Errorf("set task summary: %w", err)
	}
	return nil
}

// DeleteTask removes a task row by id — for a task whose session never
// came into existence.
func (s *Store) DeleteTask(id string) error {
	if _, err := s.db.Exec(`DELETE FROM tasks WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}
