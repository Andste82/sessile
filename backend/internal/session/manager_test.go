package session

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store, standing in for SQLite.
type memStore struct {
	mu   sync.Mutex
	rows map[string]Info
}

func newMemStore() *memStore { return &memStore{rows: map[string]Info{}} }

func (s *memStore) Insert(i Info) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[i.ID] = i
	return nil
}

func (s *memStore) SetStatus(id string, status Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok {
		row.Status = status
		s.rows[id] = row
	}
	return nil
}

func (s *memStore) Touch(id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok {
		row.LastActivity = at
		s.rows[id] = row
	}
	return nil
}

func (s *memStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, id)
	return nil
}

func (s *memStore) Get(id string) (Info, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[id]
	return row, ok, nil
}

func (s *memStore) LoadStopped() ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Info
	for _, row := range s.rows {
		if row.Status == StatusStopped {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *memStore) DeleteStoppedBefore(cutoff time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, row := range s.rows {
		if row.Status == StatusStopped && row.LastActivity.Before(cutoff) {
			ids = append(ids, id)
			delete(s.rows, id)
		}
	}
	return ids, nil
}

// testManager builds a Manager backed by a temp sandbox and data directory.
func testManager(t *testing.T) (*Manager, *memStore, string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dataDir := t.TempDir()
	store := newMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(t.TempDir(), []string{"sh"}, 64<<10, dataDir, store, log)
	t.Cleanup(mgr.Shutdown)
	return mgr, store, dataDir
}

// waitForStatus polls until a session reaches want, or fails the test.
func waitForStatus(t *testing.T, m *Manager, id string, want Status) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := m.Get(id); err == nil && info.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, _ := m.Get(id)
	t.Fatalf("session %s status = %s, want %s", id, info.Status, want)
}

// waitForOutput polls a live session's ring buffer for want.
func waitForOutput(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(s.buffer.Snapshot(), []byte(want)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("output %q never appeared in the ring buffer", want)
}

func liveSession(t *testing.T, m *Manager, id string) *Session {
	t.Helper()
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		t.Fatalf("session %s not live", id)
	}
	return s
}

// The heart of the feature: a stopped session comes back under the same id with
// its previous output still on screen.
func TestRestartRestoresIdentityAndScrollback(t *testing.T) {
	mgr, _, dataDir := testManager(t)

	created, err := mgr.Create("restore-me", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := mgr.WriteInput(created.ID, []byte("echo restart-marker\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	waitForOutput(t, liveSession(t, mgr, created.ID), "restart-marker")

	// End the shell the way a user would; readLoop snapshots on the way out.
	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	if _, err := os.Stat(filepath.Join(dataDir, "scrollback", created.ID+".bin")); err != nil {
		t.Fatalf("no scrollback snapshot written: %v", err)
	}

	restarted, err := mgr.Restart(created.ID)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if restarted.ID != created.ID {
		t.Errorf("Restart changed the id: %s, want %s", restarted.ID, created.ID)
	}
	if !restarted.Created.Equal(created.Created) {
		t.Errorf("Restart changed Created: %v, want %v", restarted.Created, created.Created)
	}
	if restarted.Name != created.Name || restarted.Directory != created.Directory ||
		restarted.Shell != created.Shell {
		t.Errorf("Restart changed metadata: %+v, want name/dir/shell of %+v", restarted, created)
	}
	if restarted.Status != StatusRunning {
		t.Errorf("Restart status = %s, want %s", restarted.Status, StatusRunning)
	}
	if restarted.PID == created.PID || restarted.PID == 0 {
		t.Errorf("Restart PID = %d, want a new non-zero pid (was %d)", restarted.PID, created.PID)
	}

	// The replay the next client will receive: old output, then the separator.
	replay := liveSession(t, mgr, created.ID).buffer.Snapshot()
	markerAt := bytes.Index(replay, []byte("restart-marker"))
	sepAt := bytes.Index(replay, []byte("── restored "))
	switch {
	case markerAt < 0:
		t.Errorf("restored buffer lost the previous output: %q", replay)
	case sepAt < 0:
		t.Errorf("restored buffer has no separator: %q", replay)
	case sepAt < markerAt:
		t.Errorf("separator precedes the restored output; want old output first")
	}
}

// The command history is the other half of the restore, and it hinges entirely
// on the restarted shell being pointed at the same HISTFILE.
func TestRestartReusesTheSameHistoryFile(t *testing.T) {
	mgr, _, dataDir := testManager(t)

	created, err := mgr.Create("hist", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The session itself runs sh, which has no per-session history; what is under
	// test is that the id — and therefore the path — is stable across a restart.
	want := filepath.Join(dataDir, "history", created.ID)
	for _, shell := range []string{"bash", "zsh"} {
		env, err := historyEnv(dataDir, shell, created.ID)
		if err != nil {
			t.Fatalf("historyEnv(%s): %v", shell, err)
		}
		if env[0] != "HISTFILE="+want {
			t.Errorf("%s HISTFILE = %q, want %q", shell, env[0], "HISTFILE="+want)
		}
	}

	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	restarted, err := mgr.Restart(created.ID)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	// Same id in, same history path out — that is what carries arrow-up across.
	env, err := historyEnv(dataDir, "bash", restarted.ID)
	if err != nil {
		t.Fatalf("historyEnv after restart: %v", err)
	}
	if env[0] != "HISTFILE="+want {
		t.Errorf("history path changed across restart: %q, want %q", env[0], "HISTFILE="+want)
	}
}

// A session whose row survives but whose Manager does not — the state after a
// backend restart — must still be restartable.
func TestRestartFromStoreOnlyRow(t *testing.T) {
	mgr, store, _ := testManager(t)

	created, err := mgr.Create("survivor", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mgr.Shutdown() // drops every live session, as a process exit would

	if _, ok := mgr.sessions[created.ID]; ok {
		t.Fatal("Shutdown left the session in memory")
	}
	if err := store.SetStatus(created.ID, StatusStopped); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	restarted, err := mgr.Restart(created.ID)
	if err != nil {
		t.Fatalf("Restart from store-only row: %v", err)
	}
	if restarted.ID != created.ID || restarted.Status != StatusRunning {
		t.Errorf("Restart = %+v, want id %s running", restarted, created.ID)
	}
}

func TestRestartErrors(t *testing.T) {
	mgr, _, _ := testManager(t)

	if _, err := mgr.Restart("11111111-2222-3333-4444-555555555555"); err != ErrNotFound {
		t.Errorf("Restart of unknown id = %v, want %v", err, ErrNotFound)
	}

	created, err := mgr.Create("live", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := mgr.Restart(created.ID); err != ErrAlreadyRunning {
		t.Errorf("Restart of running session = %v, want %v", err, ErrAlreadyRunning)
	}
}

// A restart re-runs the sandbox check, so a directory that has since been
// removed must fail cleanly rather than at PTY start.
func TestRestartRejectsVanishedDirectory(t *testing.T) {
	mgr, _, _ := testManager(t)

	sub := filepath.Join(mgr.root, "work")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	created, err := mgr.Create("gone", "work", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	if err := os.RemoveAll(sub); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if _, err := mgr.Restart(created.ID); err == nil {
		t.Error("Restart into a deleted directory succeeded, want an error")
	}
}

// A stopped session's ring buffer is unreachable — Attach rejects anything not
// running — so holding up to --buffer-size for it until the process exits is
// pure waste. The snapshot on disk is what a restart reads.
func TestStoppedSessionReleasesItsBuffer(t *testing.T) {
	mgr, _, dataDir := testManager(t)

	created, err := mgr.Create("leaky", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("echo filling-the-buffer\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	s := liveSession(t, mgr, created.ID)
	waitForOutput(t, s, "filling-the-buffer")

	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	// The session is still listed and restartable, but its buffer is gone.
	if n := s.buffer.Len(); n != 0 {
		t.Errorf("stopped session still holds %d scrollback bytes, want 0", n)
	}
	if _, err := mgr.Get(created.ID); err != nil {
		t.Errorf("stopped session no longer resolvable: %v", err)
	}

	// Releasing must happen after the snapshot, not instead of it.
	snap, err := NewScrollbackStore(dataDir).Load(created.ID)
	if err != nil {
		t.Fatalf("Load snapshot: %v", err)
	}
	if !bytes.Contains(snap, []byte("filling-the-buffer")) {
		t.Errorf("snapshot lost the output the buffer held: %q", snap)
	}
}

// Shutdown snapshots live sessions, but must leave an already-stopped one alone:
// its buffer has been released, so saving again would overwrite a good snapshot
// with an empty one.
func TestShutdownDoesNotOverwriteAStoppedSnapshot(t *testing.T) {
	mgr, _, dataDir := testManager(t)

	created, err := mgr.Create("stopped-early", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("echo precious-output\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	waitForOutput(t, liveSession(t, mgr, created.ID), "precious-output")
	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	mgr.Shutdown()

	snap, err := NewScrollbackStore(dataDir).Load(created.ID)
	if err != nil {
		t.Fatalf("Load snapshot: %v", err)
	}
	if !bytes.Contains(snap, []byte("precious-output")) {
		t.Errorf("Shutdown clobbered the snapshot: %q", snap)
	}
}

// Stopped rows accumulate forever otherwise — one per session ever created,
// each with a scrollback snapshot and a history file behind it.
func TestPruneStopped(t *testing.T) {
	mgr, store, dataDir := testManager(t)

	old, err := mgr.Create("ancient", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	recent, err := mgr.Create("recent", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, id := range []string{old.ID, recent.ID} {
		if err := mgr.WriteInput(id, []byte("exit\n")); err != nil {
			t.Fatalf("WriteInput exit: %v", err)
		}
		waitForStatus(t, mgr, id, StatusStopped)
	}

	// Age one of them past the retention window.
	if err := store.Touch(old.ID, time.Now().UTC().Add(-48*time.Hour)); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	histPath := filepath.Join(dataDir, "history", old.ID)
	if err := os.MkdirAll(filepath.Dir(histPath), 0o750); err != nil {
		t.Fatalf("mkdir history: %v", err)
	}
	if err := os.WriteFile(histPath, []byte("echo old\n"), 0o600); err != nil {
		t.Fatalf("write history: %v", err)
	}

	// Zero retention is the off switch and must touch nothing.
	if n, err := mgr.PruneStopped(0); err != nil || n != 0 {
		t.Fatalf("PruneStopped(0) = (%d, %v), want (0, nil)", n, err)
	}
	if _, found, _ := store.Get(old.ID); !found {
		t.Fatal("PruneStopped(0) removed a session; zero must disable pruning")
	}

	n, err := mgr.PruneStopped(24 * time.Hour)
	if err != nil {
		t.Fatalf("PruneStopped: %v", err)
	}
	if n != 1 {
		t.Errorf("PruneStopped pruned %d sessions, want 1", n)
	}

	if _, found, _ := store.Get(old.ID); found {
		t.Error("the idle session survived pruning")
	}
	if _, found, _ := store.Get(recent.ID); !found {
		t.Error("pruning took a session inside the retention window")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "scrollback", old.ID+".bin")); !os.IsNotExist(err) {
		t.Errorf("pruning left the scrollback behind (err=%v)", err)
	}
	if _, err := os.Stat(histPath); !os.IsNotExist(err) {
		t.Errorf("pruning left the history behind (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "scrollback", recent.ID+".bin")); err != nil {
		t.Errorf("pruning removed a retained session's scrollback: %v", err)
	}
}

// Delete removes a session permanently (§6) — including the state a later
// restart would otherwise bring back.
func TestDeleteDiscardsScrollbackAndHistory(t *testing.T) {
	mgr, _, dataDir := testManager(t)

	created, err := mgr.Create("temporary", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("echo doomed\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	waitForOutput(t, liveSession(t, mgr, created.ID), "doomed")

	// Stop it so a snapshot exists, otherwise the assertion below is vacuous.
	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	snapPath := filepath.Join(dataDir, "scrollback", created.ID+".bin")
	if _, err := os.Stat(snapPath); err != nil {
		t.Fatalf("no scrollback snapshot to delete: %v", err)
	}

	// sh has no per-session history of its own; stand in for a bash session.
	histPath := filepath.Join(dataDir, "history", created.ID)
	if err := os.MkdirAll(filepath.Dir(histPath), 0o750); err != nil {
		t.Fatalf("mkdir history: %v", err)
	}
	if err := os.WriteFile(histPath, []byte("echo doomed\n"), 0o600); err != nil {
		t.Fatalf("write history: %v", err)
	}

	if err := mgr.Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Errorf("scrollback survived Delete (err=%v)", err)
	}
	if _, err := os.Stat(histPath); !os.IsNotExist(err) {
		t.Errorf("history survived Delete (err=%v)", err)
	}
}
