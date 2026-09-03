package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/session"
)

func newInfo(id string, status session.Status) session.Info {
	now := time.Now().UTC().Truncate(time.Second)
	return session.Info{
		ID:           id,
		Name:         "s-" + id,
		Directory:    "project-a",
		Shell:        "bash",
		Status:       status,
		Created:      now,
		LastActivity: now,
	}
}

func TestStoreCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	in := newInfo("a", session.StatusRunning)
	if err := st.Insert(in); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, found, err := st.Get("a")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Name != in.Name || got.Directory != in.Directory || got.Shell != in.Shell {
		t.Fatalf("get mismatch: %+v", got)
	}
	if !got.Created.Equal(in.Created) {
		t.Fatalf("created roundtrip: got %v want %v", got.Created, in.Created)
	}

	// Upsert (rename) via Insert.
	in.Name = "renamed"
	if err := st.Insert(in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _, _ = st.Get("a")
	if got.Name != "renamed" {
		t.Fatalf("upsert name = %q, want renamed", got.Name)
	}

	// SetStatus + LoadStopped.
	if err := st.SetStatus("a", session.StatusStopped); err != nil {
		t.Fatalf("set status: %v", err)
	}
	stopped, err := st.LoadStopped()
	if err != nil {
		t.Fatalf("load stopped: %v", err)
	}
	if len(stopped) != 1 || stopped[0].ID != "a" {
		t.Fatalf("load stopped = %+v", stopped)
	}

	// Delete.
	if err := st.Delete("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, _ := st.Get("a"); found {
		t.Fatalf("session still present after delete")
	}
}

func TestReconcileOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Insert(newInfo("live", session.StatusRunning)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	st.Close()

	// Reopen: the previously-running session must be reconciled to stopped.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()

	got, found, err := st2.Get("live")
	if err != nil || !found {
		t.Fatalf("get after reopen: found=%v err=%v", found, err)
	}
	if got.Status != session.StatusStopped {
		t.Fatalf("status after reopen = %q, want stopped", got.Status)
	}
}

// Pruning must be selective in two directions: only stopped sessions, and only
// those idle past the cutoff. A running session is never a candidate no matter
// how stale its last_activity is — the throttle in §4.6 lets that lag 30 s.
func TestDeleteStoppedBefore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC().Truncate(time.Second)
	rows := []struct {
		id       string
		status   session.Status
		lastSeen time.Time
	}{
		{"stale-stopped", session.StatusStopped, now.Add(-48 * time.Hour)},
		{"fresh-stopped", session.StatusStopped, now.Add(-1 * time.Hour)},
		{"stale-running", session.StatusRunning, now.Add(-48 * time.Hour)},
	}
	for _, r := range rows {
		in := newInfo(r.id, r.status)
		in.LastActivity = r.lastSeen
		if err := st.Insert(in); err != nil {
			t.Fatalf("insert %s: %v", r.id, err)
		}
	}

	ids, err := st.DeleteStoppedBefore(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("DeleteStoppedBefore: %v", err)
	}
	if len(ids) != 1 || ids[0] != "stale-stopped" {
		t.Fatalf("pruned %v, want [stale-stopped]", ids)
	}

	for _, id := range []string{"fresh-stopped", "stale-running"} {
		if _, found, _ := st.Get(id); !found {
			t.Errorf("%s was pruned but should have survived", id)
		}
	}
	if _, found, _ := st.Get("stale-stopped"); found {
		t.Error("stale-stopped survived pruning")
	}

	// Nothing left to prune reports no ids and no error.
	ids, err = st.DeleteStoppedBefore(now.Add(-24 * time.Hour))
	if err != nil || len(ids) != 0 {
		t.Errorf("second prune = (%v, %v), want (nil, nil)", ids, err)
	}
}

// User/target fields round-trip through Insert/Get/LoadStopped — the actual
// point of the §12b M17 migration, not just that Open doesn't error.
func TestUserAndTargetFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	in := newInfo("ssh-1", session.StatusStopped)
	in.UserID = "user-42"
	in.TargetType = session.TargetSSH
	in.HostID = "host-7"
	in.HostDisplayName = "prod-db"
	if err := st.Insert(in); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, found, err := st.Get("ssh-1")
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.UserID != "user-42" || got.TargetType != session.TargetSSH ||
		got.HostID != "host-7" || got.HostDisplayName != "prod-db" {
		t.Fatalf("round-tripped info = %+v, want the SSH fields preserved", got)
	}

	stopped, err := st.LoadStopped()
	if err != nil {
		t.Fatalf("load stopped: %v", err)
	}
	if len(stopped) != 1 || stopped[0].UserID != "user-42" || stopped[0].TargetType != session.TargetSSH {
		t.Fatalf("LoadStopped = %+v, want the same SSH fields", stopped)
	}
}

// TestMigrationUpgradesPreM17Database is the actual upgrade path: a database
// created under the original schema (no user_id/target_type/host_id/
// host_display_name columns, PROJECT_PLAN.md §8 pre-M17) must open cleanly
// under the current one, and rows written before the migration must default
// to an empty user_id (invisible under strict per-owner scoping — expected) and
// target_type='local' (correct: every pre-M17 session was local).
func TestMigrationUpgradesPreM17Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")

	// Hand-build the pre-M17 schema directly — Open would already apply the
	// current one, which is exactly what this test must not start from.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	const oldSchema = `
	CREATE TABLE sessions (
	  id            TEXT PRIMARY KEY,
	  name          TEXT NOT NULL,
	  directory     TEXT NOT NULL,
	  shell         TEXT NOT NULL,
	  status        TEXT NOT NULL DEFAULT 'running',
	  created       TEXT NOT NULL,
	  last_activity TEXT NOT NULL
	);`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO sessions (id, name, directory, shell, status, created, last_activity)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"pre-migration", "old-session", "project-a", "bash", string(session.StatusStopped), now, now,
	); err != nil {
		t.Fatalf("insert under old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	// Now open it the real way — this is what must apply the migration.
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a pre-M17 database: %v", err)
	}
	defer st.Close()

	got, found, err := st.Get("pre-migration")
	if err != nil || !found {
		t.Fatalf("get pre-migration row: found=%v err=%v", found, err)
	}
	if got.UserID != "" {
		t.Errorf("UserID = %q, want empty for a row written before the migration", got.UserID)
	}
	if got.TargetType != session.TargetLocal {
		t.Errorf("TargetType = %q, want %q for a row written before the migration", got.TargetType, session.TargetLocal)
	}

	// The migration must also be idempotent: a second Open (simulating a
	// second server start) must not error on columns that already exist.
	st.Close()
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open on an already-migrated database: %v", err)
	}
	st2.Close()
}
