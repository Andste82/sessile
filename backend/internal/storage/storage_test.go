package storage

import (
	"path/filepath"
	"reflect"
	"strings"
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

// Env and DotEnv survive Insert/Get/LoadStopped, secrets and non-secrets land
// in their own columns, Default flags are kept, and a later Insert (the
// upsert Update uses) rewrites them.
func TestStoreEnvRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	in := newInfo("e", session.StatusStopped)
	in.DotEnv = true
	in.Env = []session.EnvVar{
		{Name: "A_PLAIN", Value: "1", Default: true},
		{Name: "B_TOKEN", Value: "s3cret-sentinel", Secret: true},
		{Name: "C_MULTI", Value: "line1\nline2 'q' \"d\""},
		{Name: "D_TOKEN", Value: "other-secret", Secret: true, Default: true},
	}
	if err := st.Insert(in); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var vars, secrets string
	var dotenv int
	if err := st.db.QueryRow(`SELECT env_vars, env_secrets, dotenv FROM sessions WHERE id='e'`).
		Scan(&vars, &secrets, &dotenv); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(vars, "s3cret-sentinel") || strings.Contains(vars, "other-secret") {
		t.Errorf("secret value in env_vars: %s", vars)
	}
	if !strings.Contains(secrets, "s3cret-sentinel") || strings.Contains(secrets, "A_PLAIN") {
		t.Errorf("env_secrets = %s", secrets)
	}
	if dotenv != 1 {
		t.Errorf("dotenv = %d, want 1", dotenv)
	}

	check := func(label string, got session.Info) {
		t.Helper()
		if !got.DotEnv || !reflect.DeepEqual(got.Env, in.Env) {
			t.Errorf("%s: Env=%+v DotEnv=%v, want %+v true", label, got.Env, got.DotEnv, in.Env)
		}
	}
	got, ok, err := st.Get("e")
	if err != nil || !ok {
		t.Fatalf("get: %v %v", ok, err)
	}
	check("Get", got)
	stopped, err := st.LoadStopped()
	if err != nil || len(stopped) != 1 {
		t.Fatalf("LoadStopped: %v %v", stopped, err)
	}
	check("LoadStopped", stopped[0])

	// Upsert replaces the environment, including clearing it.
	in.Env, in.DotEnv = nil, false
	if err := st.Insert(in); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.Get("e")
	if len(got.Env) != 0 || got.DotEnv {
		t.Errorf("after clearing: Env=%+v DotEnv=%v", got.Env, got.DotEnv)
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

// A group survives the round trip, and survives being cleared again — the
// upsert has to write "" over an existing value, not skip it (§4.11).
func TestGroupRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	in := newInfo("grouped", session.StatusStopped)
	in.Group = "Production"
	if err := st.Insert(in); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _, err := st.Get("grouped")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Group != "Production" {
		t.Fatalf("Group = %q, want %q", got.Group, "Production")
	}

	in.Group = ""
	if err := st.Insert(in); err != nil {
		t.Fatalf("re-insert: %v", err)
	}
	got, _, err = st.Get("grouped")
	if err != nil {
		t.Fatalf("get after clear: %v", err)
	}
	if got.Group != "" {
		t.Fatalf("Group = %q after clearing, want empty", got.Group)
	}
}
