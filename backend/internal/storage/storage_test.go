package storage

import (
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
