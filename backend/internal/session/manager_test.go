package session

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
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

	// Coming out of an ordinary shell, the separator must not carry the
	// alternate-screen reset: 1049l restores a cursor that was never saved, so
	// the terminal draws the banner over the top of the very history above it.
	if sepAt >= 0 {
		from := max(sepAt-32, 0) // the separator's escapes sit just before its text
		if bytes.Contains(replay[from:], []byte("\x1b[?1049l")) {
			t.Errorf("separator resets the alternate screen after a plain shell: %q", replay[from:])
		}
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
	mgr, store, dataDir := testManager(t)

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

	// The process comes back: a new Manager over the store and data directory the
	// old one left behind. Restarting on the shut-down Manager itself is not the
	// same scenario and no longer allowed — one that has torn down its sessions
	// can no longer terminate anything it starts.
	revived := NewManager(mgr.root, mgr.shells, mgr.bufferSize, dataDir, store,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(revived.Shutdown)

	restarted, err := revived.Restart(created.ID)
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

// TestConcurrentRestartStartsOneShell covers the window between deciding a
// session may restart and publishing its replacement — a fork+exec wide, and
// once entered by two callers at once it produced two shells under one id. The
// map kept the second; the first kept running with nothing pointing at it.
//
// Two tabs on the same session are enough to reach this, so "exactly one wins"
// has to hold under a real race, not just in sequence.
func TestConcurrentRestartStartsOneShell(t *testing.T) {
	mgr, _, _ := testManager(t)

	created, err := mgr.Create("contended", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	const callers = 4
	var wg sync.WaitGroup
	infos := make([]Info, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := range infos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			infos[i], errs[i] = mgr.Restart(created.ID)
		}(i)
	}
	close(start)
	wg.Wait()

	var winners []Info
	for i, err := range errs {
		switch {
		case err == nil:
			winners = append(winners, infos[i])
		case errors.Is(err, ErrAlreadyRunning):
		default:
			t.Errorf("Restart returned %v, want nil or %v", err, ErrAlreadyRunning)
		}
	}
	if len(winners) != 1 {
		t.Errorf("%d of %d concurrent restarts succeeded, want exactly 1", len(winners), callers)
	}

	live := liveSession(t, mgr, created.ID)
	for _, w := range winners {
		if w.PID == live.PID {
			continue
		}
		// Signal 0 only checks that the pid is still there — no shell of ours
		// should be, once the manager has stopped tracking it.
		if syscall.Kill(w.PID, 0) == nil {
			t.Errorf("orphaned shell: pid %d is still running but is not the session's (%d)",
				w.PID, live.PID)
		}
	}
}

// TestRegisterAfterShutdownDiscardsTheShell covers the last of the restart
// windows: a shell spawned before Shutdown drained the sessions map, arriving to
// be published after it. Publishing it would put a live shell somewhere nothing
// tracks — Shutdown has already been past that map, and the process is on its
// way out — and Setsid means the shell survives its parent.
//
// register is called directly with a hand-spawned session because the window it
// stands for is a fork+exec inside Restart, which cannot be paused from outside.
func TestRegisterAfterShutdownDiscardsTheShell(t *testing.T) {
	mgr, _, _ := testManager(t)

	s, err := mgr.spawn("11111111-2222-3333-4444-555555555555", "late", ".", "sh", timeNow())
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := s.PID

	mgr.Shutdown()

	if _, err := mgr.register(s); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("register during shutdown = %v, want %v", err, ErrShuttingDown)
	}
	if _, ok := mgr.sessions[s.ID]; ok {
		t.Error("the session was published into a manager that had already shut down")
	}
	// register reaps what it refuses, so the pid is gone for good rather than
	// left as a zombie for whoever exits last.
	if syscall.Kill(pid, 0) == nil {
		t.Errorf("shell %d outlived the manager that spawned it", pid)
	}
}

// Once Shutdown has run, nothing may start a shell — the cheap refusal in
// claimRestart and the authoritative one in register have to agree.
func TestStartAfterShutdownIsRefused(t *testing.T) {
	mgr, _, _ := testManager(t)

	created, err := mgr.Create("doomed", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mgr.Shutdown()

	if _, err := mgr.Restart(created.ID); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Restart after shutdown = %v, want %v", err, ErrShuttingDown)
	}
	if _, err := mgr.Create("too-late", ".", "sh"); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Create after shutdown = %v, want %v", err, ErrShuttingDown)
	}
	if n := len(mgr.sessions); n != 0 {
		t.Errorf("%d sessions live after shutdown, want 0", n)
	}
}

// TestDeleteDuringRestartIsRefused covers the other side of the reservation: a
// delete that lands while a restart is between spawning its shell and
// publishing it. Deleting now would drop the row and the state, and register
// would then put the session back under the id just erased — running, with
// nothing tracking it. The caller gets a conflict instead, and the delete works
// once the restart has landed.
//
// The reservation is taken directly because the window it stands for is a
// fork+exec inside Restart, which cannot be paused from the outside.
func TestDeleteDuringRestartIsRefused(t *testing.T) {
	mgr, store, _ := testManager(t)

	created, err := mgr.Create("contested", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	if err := mgr.claimRestart(created.ID); err != nil {
		t.Fatalf("claimRestart: %v", err)
	}
	if err := mgr.Delete(created.ID); !errors.Is(err, ErrRestarting) {
		t.Errorf("Delete during a restart = %v, want %v", err, ErrRestarting)
	}
	if _, found, _ := store.Get(created.ID); !found {
		t.Error("the refused delete removed the session's row anyway")
	}

	// Once the restart has let go, the delete goes through as usual.
	mgr.releaseRestart(created.ID)
	if err := mgr.Delete(created.ID); err != nil {
		t.Fatalf("Delete after the restart finished: %v", err)
	}
	if _, found, _ := store.Get(created.ID); found {
		t.Error("the session's row survived a successful delete")
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

// snapshotWatcher is a Client that records what the scrollback snapshot looked
// like at the instant the session announced its exit.
type snapshotWatcher struct {
	store *ScrollbackStore
	id    string

	mu       sync.Mutex
	sawExit  bool
	atExit   []byte
	loadErr  error
	exitSeen chan struct{}
}

func newSnapshotWatcher(store *ScrollbackStore, id string) *snapshotWatcher {
	return &snapshotWatcher{store: store, id: id, exitSeen: make(chan struct{})}
}

func (w *snapshotWatcher) ID() string            { return "watcher" }
func (w *snapshotWatcher) Send(_ []byte) bool    { return true }
func (w *snapshotWatcher) Close(_ int, _ string) {}

func (w *snapshotWatcher) SendControl(v any) bool {
	if _, ok := v.(ExitMsg); !ok {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sawExit {
		return true
	}
	w.sawExit = true
	w.atExit, w.loadErr = w.store.Load(w.id)
	close(w.exitSeen)
	return true
}

// The exit frame is the fastest signal a browser gets that a session stopped —
// it is what raises the "Restart session" banner — and Restart reads the
// scrollback snapshot. So the snapshot has to be on disk before that frame goes
// out, not after; otherwise a fast click restores nothing.
//
// This is checked from inside SendControl rather than by racing the loop from
// outside: the ordering is the guarantee, and a timing race reproduces it only
// on a loaded machine. (It reproduced on CI and not locally, which is how the
// original ordering shipped.)
func TestSnapshotIsWrittenBeforeClientsAreToldOfExit(t *testing.T) {
	mgr, _, dataDir := testManager(t)

	created, err := mgr.Create("racy", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	watcher := newSnapshotWatcher(NewScrollbackStore(dataDir), created.ID)
	if _, err := mgr.Attach(created.ID, watcher); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	const marker = "output-before-exit"
	if err := mgr.WriteInput(created.ID, []byte("echo "+marker+"\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	waitForOutput(t, liveSession(t, mgr, created.ID), marker)

	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}

	select {
	case <-watcher.exitSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("no exit control frame arrived")
	}

	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if watcher.loadErr != nil {
		t.Fatalf("Load at exit: %v", watcher.loadErr)
	}
	if !bytes.Contains(watcher.atExit, []byte(marker)) {
		t.Errorf("snapshot at the moment of the exit frame was %d bytes and lacks %q; "+
			"a client restarting on this signal would restore nothing",
			len(watcher.atExit), marker)
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

// TestLateFlushKeepsFinalSnapshot replays the interleaving the flush timer can
// lose: it read a running status, and by the time it gets to the buffer the
// session has stopped — final snapshot written, buffer released. Saving now
// would put an empty file where the session's whole output was, and a restart
// would restore nothing.
//
// Calling saveScrollback directly is the point: it stands in for a flusher that
// is already past its status check, which is the state that cannot be scheduled
// on demand.
func TestLateFlushKeepsFinalSnapshot(t *testing.T) {
	mgr, _, dataDir := testManager(t)

	created, err := mgr.Create("late-flush", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.WriteInput(created.ID, []byte("echo precious-output\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	s := liveSession(t, mgr, created.ID)
	waitForOutput(t, s, "precious-output")
	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	store := NewScrollbackStore(dataDir)
	before, err := store.Load(created.ID)
	if err != nil {
		t.Fatalf("Load snapshot: %v", err)
	}
	if !bytes.Contains(before, []byte("precious-output")) {
		t.Fatalf("the read loop's own snapshot already lacks the output: %q", before)
	}

	// The flusher finally gets to run, still holding the session it checked.
	mgr.saveScrollback(s)

	after, err := store.Load(created.ID)
	if err != nil {
		t.Fatalf("Load snapshot after the late flush: %v", err)
	}
	if !bytes.Contains(after, []byte("precious-output")) {
		t.Errorf("a flush that lost the race replaced the final snapshot with %d bytes: %q",
			len(after), after)
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

// recordingClient is a Client that keeps every control frame and every byte it
// was sent, for asserting what a browser would have seen.
type recordingClient struct {
	id string

	mu       sync.Mutex
	controls []any
	sent     [][]byte
	closed   bool
}

func (c *recordingClient) ID() string { return c.id }

func (c *recordingClient) Send(data []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, append([]byte(nil), data...))
	return true
}

func (c *recordingClient) SendControl(v any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.controls = append(c.controls, v)
	return true
}

func (c *recordingClient) Close(_ int, _ string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}

func (c *recordingClient) snapshot() ([]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.controls...), c.closed
}

// counts the control frames of type T the client received.
func countControls[T any](controls []any) int {
	n := 0
	for _, v := range controls {
		if _, ok := v.(T); ok {
			n++
		}
	}
	return n
}

// A stopped session's clients are not disconnected: the session can come back
// under the same id, and the connection is the only way to tell a browser that
// it did. Closing them left every client but the one that clicked "restart"
// holding a dead socket and a dialog offering to start a running session.
func TestStoppedSessionKeepsItsClients(t *testing.T) {
	mgr, _, _ := testManager(t)

	created, err := mgr.Create("watched", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	client := &recordingClient{id: "c1"}
	if _, err := mgr.Attach(created.ID, client); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	controls, closed := client.snapshot()
	if countControls[ExitMsg](controls) != 1 {
		t.Errorf("client got %d exit frames, want exactly 1: %+v", countControls[ExitMsg](controls), controls)
	}
	if closed {
		t.Error("stopped session closed its client; it has to stay attached for the restart")
	}
	if info, _ := mgr.Get(created.ID); info.ClientCount != 1 {
		t.Errorf("stopped session reports %d clients, want 1", info.ClientCount)
	}

	// Keystrokes at a dead terminal go nowhere, but must be distinguishable from
	// a broken connection: the ws read pump keeps the socket open on ErrStopped
	// and tears it down on anything else.
	if err := mgr.WriteInput(created.ID, []byte("ignored\n")); !errors.Is(err, ErrStopped) {
		t.Errorf("WriteInput on a stopped session = %v, want ErrStopped", err)
	}
}

// Issue #42: the restart one browser performs has to reach the others. They are
// still attached, so the replacement shell adopts them and re-primes each with
// the attach sequence — which is what takes their restart dialog down.
func TestRestartMovesEveryClientToTheNewShell(t *testing.T) {
	mgr, _, _ := testManager(t)

	created, err := mgr.Create("shared", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	clients := []*recordingClient{{id: "c1"}, {id: "c2"}}
	for _, c := range clients {
		if _, err := mgr.Attach(created.ID, c); err != nil {
			t.Fatalf("Attach %s: %v", c.ID(), err)
		}
	}

	if err := mgr.WriteInput(created.ID, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput exit: %v", err)
	}
	waitForStatus(t, mgr, created.ID, StatusStopped)

	info, err := mgr.Restart(created.ID)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if info.ClientCount != len(clients) {
		t.Errorf("restarted session reports %d clients, want %d", info.ClientCount, len(clients))
	}

	for _, c := range clients {
		controls, _ := c.snapshot()
		// One attach on the original Attach, a second one from the restart.
		if got := countControls[AttachedMsg](controls); got != 2 {
			t.Errorf("%s got %d attached frames, want 2 (the second is the restart): %+v",
				c.ID(), got, controls)
		}
	}

	// And the adopted clients are the live session's, so new output reaches them.
	if err := mgr.WriteInput(created.ID, []byte("echo after-restart\n")); err != nil {
		t.Fatalf("WriteInput after restart: %v", err)
	}
	waitForOutput(t, liveSession(t, mgr, created.ID), "after-restart")
}

// A restart after a backend restart has no previous session object at all — the
// session survives only as a stopped row — and must not trip over the migration.
func TestRestartFromStoreOnlyRowMigratesNothing(t *testing.T) {
	mgr, store, _ := testManager(t)

	created, err := mgr.Create("survivor", ".", "sh")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mgr.Shutdown()

	revived := NewManager(t.TempDir(), []string{"sh"}, 64<<10, t.TempDir(), store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(revived.Shutdown)

	info, err := revived.Restart(created.ID)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if info.ClientCount != 0 {
		t.Errorf("restarted session reports %d clients, want 0", info.ClientCount)
	}
	if info.Status != StatusRunning {
		t.Errorf("status = %s, want %s", info.Status, StatusRunning)
	}
}
