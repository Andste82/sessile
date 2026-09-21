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

// tasksMigrationColumns are added to an existing tasks table the way §8's
// session columns are: guarded by a PRAGMA check, with nothing to backfill.
var tasksMigrationColumns = []struct{ name, ddl string }{
	{"state", `ALTER TABLE tasks ADD COLUMN state TEXT NOT NULL DEFAULT ''`},
	{"question", `ALTER TABLE tasks ADD COLUMN question TEXT NOT NULL DEFAULT ''`},
	{"kind", `ALTER TABLE tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'task'`},
	// v0.9: a task runs two sessions — its agent on the server (session_id)
	// and the user's shell on its host (shell_session_id), which is "" for a
	// task that has no host.
	{"shell_session_id", `ALTER TABLE tasks ADD COLUMN shell_session_id TEXT NOT NULL DEFAULT ''`},
}

// TaskRow is one task as stored.
type TaskRow struct {
	ID string
	// SessionID is the agent session; ShellSessionID the user's shell on the
	// host, "" for a task without one.
	SessionID      string
	ShellSessionID string
	UserID         string
	HostID         string // "" for a local-host task
	Dir            string // absolute task folder on the target, known once it is created
	SpecJSON       string
	Summary        string
	// State, Question and Kind are §4.18's: what the task's agent says it is
	// doing, what it is waiting for, and whether this is a task or the
	// user's orchestrator.
	State    string
	Question string
	Kind     string
	Created  time.Time
}

// taskColumns is every column the task queries read, in scan order.
const taskColumns = `id, session_id, shell_session_id, user_id, host_id, dir, spec_json, summary, state, question, kind, created`

func scanTask(sc interface{ Scan(...any) error }) (TaskRow, error) {
	var t TaskRow
	var created string
	if err := sc.Scan(&t.ID, &t.SessionID, &t.ShellSessionID, &t.UserID, &t.HostID, &t.Dir, &t.SpecJSON,
		&t.Summary, &t.State, &t.Question, &t.Kind, &created); err != nil {
		return TaskRow{}, err
	}
	if ct, err := time.Parse(time.RFC3339, created); err == nil {
		t.Created = ct
	}
	return t, nil
}

// InsertTask stores a new task.
func (s *Store) InsertTask(t TaskRow) error {
	if t.Kind == "" {
		t.Kind = "task"
	}
	_, err := s.db.Exec(
		`INSERT INTO tasks (id, session_id, shell_session_id, user_id, host_id, dir, spec_json, summary, state, question, kind, created)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.SessionID, t.ShellSessionID, t.UserID, t.HostID, t.Dir, t.SpecJSON, t.Summary, t.State, t.Question, t.Kind,
		t.Created.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// GetTask returns the task id, scoped to userID: another user's task is
// reported exactly like a missing one (§14.5).
func (s *Store) GetTask(id, userID string) (TaskRow, bool, error) {
	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id=? AND user_id=?`, id, userID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRow{}, false, nil
	}
	if err != nil {
		return TaskRow{}, false, fmt.Errorf("get task: %w", err)
	}
	return t, true, nil
}

// SetTaskShellSession records the task's shell pane, which is created after
// the task row and can be replaced if the user closes and reopens it.
func (s *Store) SetTaskShellSession(id, sessionID string) error {
	if _, err := s.db.Exec(`UPDATE tasks SET shell_session_id=? WHERE id=?`, sessionID, id); err != nil {
		return fmt.Errorf("set task shell session: %w", err)
	}
	return nil
}

// TaskBySession finds the task a session belongs to, whether it is the
// agent's session or the shell's.
func (s *Store) TaskBySession(sessionID, userID string) (TaskRow, bool, error) {
	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE (session_id=? OR shell_session_id=?) AND user_id=?`,
		sessionID, sessionID, userID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRow{}, false, nil
	}
	if err != nil {
		return TaskRow{}, false, fmt.Errorf("task by session: %w", err)
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

// SetTaskState records what a task's agent says it is doing (§4.18.2).
func (s *Store) SetTaskState(id, state, summary, question string) error {
	if _, err := s.db.Exec(`UPDATE tasks SET state=?, summary=?, question=? WHERE id=?`,
		state, summary, question, id); err != nil {
		return fmt.Errorf("set task state: %w", err)
	}
	return nil
}

// OrchestratorTask returns the user's orchestrator task (§4.18), if any.
func (s *Store) OrchestratorTask(userID string) (TaskRow, bool, error) {
	row := s.db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE user_id=? AND kind='orchestrator'
	                      ORDER BY created DESC LIMIT 1`, userID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRow{}, false, nil
	}
	if err != nil {
		return TaskRow{}, false, fmt.Errorf("get orchestrator: %w", err)
	}
	return t, true, nil
}

// DeleteTask removes a task row by id — for a task whose session never
// came into existence.
func (s *Store) DeleteTask(id string) error {
	if _, err := s.db.Exec(`DELETE FROM tasks WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}

// ListTasks returns userID's tasks.
func (s *Store) ListTasks(userID string) ([]TaskRow, error) {
	rows, err := s.db.Query(`SELECT `+taskColumns+` FROM tasks WHERE user_id=? ORDER BY created`, userID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var out []TaskRow
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
