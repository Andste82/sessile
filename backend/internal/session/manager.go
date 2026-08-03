package session

import (
	"log/slog"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// activityThrottle bounds how often LastActivity is written to the store (§4.6).
const activityThrottle = 30 * time.Second

// killGrace is the grace period before escalating to SIGKILL when terminating
// a session's shell (§4.3).
const killGrace = 5 * time.Second

// Manager is the core component: it owns every live Session, the PTY read/
// broadcast goroutines, and (optionally) a metadata Store.
type Manager struct {
	root       string
	shells     []string // allowlist
	bufferSize int
	dataDir    string // scrollback + history state; "" disables both
	log        *slog.Logger
	store      Store            // may be nil (in-memory only)
	scrollback *ScrollbackStore // nil when dataDir is ""

	mu       sync.RWMutex
	sessions map[string]*Session

	stop     chan struct{} // closed by Shutdown to end the flush loop
	stopOnce sync.Once
}

// NewManager constructs a Manager. store may be nil, and so may dataDir — an
// empty dataDir means no scrollback snapshots and no per-session shell history,
// which is what the in-memory tests want.
func NewManager(root string, shells []string, bufferSize int, dataDir string, store Store, log *slog.Logger) *Manager {
	m := &Manager{
		root:       root,
		shells:     shells,
		bufferSize: bufferSize,
		dataDir:    dataDir,
		log:        log,
		store:      store,
		sessions:   make(map[string]*Session),
		stop:       make(chan struct{}),
	}
	if dataDir != "" {
		m.scrollback = NewScrollbackStore(dataDir)
		go m.flushLoop()
	}
	return m
}

// Create validates inputs, starts a PTY-backed shell, begins its read/broadcast
// goroutine, persists metadata and returns a snapshot.
func (m *Manager) Create(name, dir, shell string) (Info, error) {
	now := timeNow()
	s, err := m.spawn(uuid.NewString(), name, dir, shell, now)
	if err != nil {
		return Info{}, err
	}
	info := m.register(s)
	m.log.Info("session created", "id", s.ID, "name", name, "shell", shell, "pid", s.PID)
	return info, nil
}

// Restart gives a stopped session a new shell under the same id, in the same
// directory, with the same shell — and with its scrollback and command history
// carried over (§8). The processes that were running when it stopped are gone;
// only the session is restored.
//
// Reusing the id is the whole point: it is what makes the persisted scrollback
// file and the shell's HISTFILE line up again, and it keeps the row, the open
// browser tab and any bookmarked URL pointing at the same session.
func (m *Manager) Restart(id string) (Info, error) {
	prev, err := m.restartable(id)
	if err != nil {
		return Info{}, err
	}

	s, err := m.spawn(prev.ID, prev.Name, prev.Directory, prev.Shell, prev.Created)
	if err != nil {
		return Info{}, err
	}

	// Seed the fresh ring buffer with what the previous shell left behind, so the
	// first client to attach sees the old output above the new prompt. Nothing is
	// attached yet, so writing straight to the buffer needs no lock.
	if m.scrollback != nil {
		replay, err := m.scrollback.Load(id)
		if err != nil {
			m.log.Error("load scrollback failed", "id", id, "err", err)
		} else if len(replay) > 0 {
			_, _ = s.buffer.Write(replay)
			_, _ = s.buffer.Write(restoreSeparator(timeNow()))
		}
	}

	info := m.register(s)
	m.log.Info("session restarted", "id", s.ID, "name", s.Name, "shell", s.Shell, "pid", s.PID)
	return info, nil
}

// restartable returns the metadata to restart id with, or an error explaining
// why it cannot be restarted.
func (m *Manager) restartable(id string) (Info, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()

	if ok {
		info := s.Info()
		if info.Status == StatusRunning {
			return Info{}, ErrAlreadyRunning
		}
		return info, nil
	}
	if m.store == nil {
		return Info{}, ErrNotFound
	}
	info, found, err := m.store.Get(id)
	if err != nil {
		return Info{}, err
	}
	if !found {
		return Info{}, ErrNotFound
	}
	if info.Status == StatusRunning {
		// Only reachable if another process owns this row; the store reconciles
		// stale running rows to stopped on open (§8).
		return Info{}, ErrAlreadyRunning
	}
	return info, nil
}

// spawn validates the inputs, starts a PTY-backed shell and returns the
// resulting Session without registering it. Shared by Create and Restart so a
// restart runs through exactly the same allowlist and sandbox checks (§4.5) —
// a directory that has since been deleted, or a shell dropped from the
// allowlist, must fail here rather than at PTY start.
func (m *Manager) spawn(id, name, dir, shell string, created time.Time) (*Session, error) {
	if l := len(name); l < 1 || l > 64 {
		return nil, ErrInvalidName
	}
	shellPath, err := m.resolveShell(shell)
	if err != nil {
		return nil, err
	}
	resolvedDir, err := resolveDir(m.root, dir)
	if err != nil {
		return nil, err
	}

	var extraEnv []string
	if m.dataDir != "" {
		extraEnv, err = historyEnv(m.dataDir, shell, id)
		if err != nil {
			return nil, err
		}
	}

	pty, err := terminal.Start(shellPath, resolvedDir, defaultRows, defaultCols, extraEnv)
	if err != nil {
		return nil, err
	}

	now := timeNow()
	return &Session{
		ID:           id,
		Name:         name,
		Directory:    dir,
		Shell:        shell,
		Status:       StatusRunning,
		PID:          pty.Pid(),
		Created:      created,
		LastActivity: now,
		Rows:         defaultRows,
		Cols:         defaultCols,
		pty:          pty,
		buffer:       NewRingBuffer(m.bufferSize),
		clients:      make(map[Client]struct{}),
		lastPersist:  now,
		exited:       make(chan struct{}),
	}, nil
}

// register publishes a spawned session, persists it and starts its read loop.
func (m *Manager) register(s *Session) Info {
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	info := s.Info()
	if m.store != nil {
		if err := m.store.Insert(info); err != nil {
			m.log.Error("persist session failed", "id", s.ID, "err", err)
		}
	}
	go m.readLoop(s)
	return info
}

// resolveShell checks the allowlist then resolves the binary on PATH.
func (m *Manager) resolveShell(shell string) (string, error) {
	allowed := false
	for _, s := range m.shells {
		if s == shell {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", ErrInvalidShell
	}
	path, err := exec.LookPath(shell)
	if err != nil {
		return "", ErrInvalidShell
	}
	return path, nil
}

// readLoop reads PTY output, appends to the ring buffer and broadcasts it,
// until the shell exits (read error). One goroutine per session (§4.4).
func (m *Manager) readLoop(s *Session) {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.pty.File.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.broadcast(data)
			m.maybePersistActivity(s)
		}
		if err != nil {
			break
		}
	}
	if s.markStopped() {
		if m.store != nil {
			if err := m.store.SetStatus(s.ID, StatusStopped); err != nil {
				m.log.Error("persist stop failed", "id", s.ID, "err", err)
			}
		}
		// Snapshot the final output: this is what a later Restart replays.
		m.saveScrollback(s)
		m.log.Info("session stopped", "id", s.ID)
	}
	// Reap the shell process (single reaper), close the master, then signal
	// that termination is complete for any waiter in terminate().
	s.pty.Wait()
	s.pty.CloseFile()
	close(s.exited)
}

// saveScrollback writes a session's current ring buffer to disk. Failures are
// logged, never fatal: losing a snapshot costs history, not correctness.
func (m *Manager) saveScrollback(s *Session) {
	if m.scrollback == nil {
		return
	}
	if err := m.scrollback.Save(s.ID, s.buffer.Snapshot()); err != nil {
		m.log.Error("persist scrollback failed", "id", s.ID, "err", err)
	}
}

// flushLoop snapshots every live session's scrollback on a timer, so a backend
// that is SIGKILLed — or whose host loses power — still restores output up to
// the last flush. The clean exits are already covered by readLoop and Shutdown;
// this is only for the paths that run no code on the way out.
//
// It reuses activityThrottle: both trade the same freshness for the same write
// volume, and a second knob would only ever be tuned together with the first.
func (m *Manager) flushLoop() {
	ticker := time.NewTicker(activityThrottle)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.mu.RLock()
			live := make([]*Session, 0, len(m.sessions))
			for _, s := range m.sessions {
				live = append(live, s)
			}
			m.mu.RUnlock()

			for _, s := range live {
				if s.Info().Status == StatusRunning {
					m.saveScrollback(s)
				}
			}
		}
	}
}

// maybePersistActivity throttles LastActivity writes to the store.
func (m *Manager) maybePersistActivity(s *Session) {
	if m.store == nil {
		return
	}
	s.mu.Lock()
	due := timeNow().Sub(s.lastPersist) >= activityThrottle
	last := s.LastActivity
	if due {
		s.lastPersist = timeNow()
	}
	s.mu.Unlock()
	if due {
		if err := m.store.Touch(s.ID, last); err != nil {
			m.log.Error("persist activity failed", "id", s.ID, "err", err)
		}
	}
}

// Get returns a session snapshot from memory, falling back to the store for
// sessions that only survive as stopped rows after a restart.
func (m *Manager) Get(id string) (Info, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		return s.Info(), nil
	}
	if m.store != nil {
		info, found, err := m.store.Get(id)
		if err != nil {
			return Info{}, err
		}
		if found {
			return info, nil
		}
	}
	return Info{}, ErrNotFound
}

// List returns all sessions: live ones from memory merged with stopped rows
// from the store, newest first.
func (m *Manager) List() ([]Info, error) {
	m.mu.RLock()
	infos := make([]Info, 0, len(m.sessions))
	seen := make(map[string]struct{}, len(m.sessions))
	for _, s := range m.sessions {
		infos = append(infos, s.Info())
		seen[s.ID] = struct{}{}
	}
	m.mu.RUnlock()

	if m.store != nil {
		stopped, err := m.store.LoadStopped()
		if err != nil {
			return nil, err
		}
		for _, si := range stopped {
			if _, ok := seen[si.ID]; !ok {
				infos = append(infos, si)
			}
		}
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Created.After(infos[j].Created)
	})
	return infos, nil
}

// Delete kills the process group, disconnects clients and removes the session
// from memory and the store.
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	if !ok {
		// Might be a stopped session that only exists in the store.
		if m.store != nil {
			if _, found, err := m.store.Get(id); err == nil && found {
				if err := m.store.Delete(id); err != nil {
					return err
				}
				m.discardState(id)
				return nil
			}
		}
		return ErrNotFound
	}

	s.closeClients(closeSessionEnded, "session deleted")
	s.terminate(killGrace)
	if m.store != nil {
		if err := m.store.Delete(id); err != nil {
			return err
		}
	}
	m.discardState(id)
	m.log.Info("session deleted", "id", id)
	return nil
}

// discardState removes the scrollback snapshot and command history of a deleted
// session. Delete is documented as removing the session permanently (§6), which
// has to include the state a Restart would otherwise resurrect.
func (m *Manager) discardState(id string) {
	if m.dataDir == "" {
		return
	}
	if m.scrollback != nil {
		if err := m.scrollback.Delete(id); err != nil {
			m.log.Error("remove scrollback failed", "id", id, "err", err)
		}
	}
	if err := deleteHistory(m.dataDir, id); err != nil {
		m.log.Error("remove history failed", "id", id, "err", err)
	}
}

// Rename updates a session's name in memory and the store.
func (m *Manager) Rename(id, name string) (Info, error) {
	if l := len(name); l < 1 || l > 64 {
		return Info{}, ErrInvalidName
	}
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return Info{}, ErrNotFound
	}
	s.mu.Lock()
	s.Name = name
	info := s.infoLocked()
	s.mu.Unlock()
	if m.store != nil {
		if err := m.store.Insert(info); err != nil { // upsert
			return Info{}, err
		}
	}
	return info, nil
}

// Attach registers a client on a running session (sends attached + replay).
func (m *Manager) Attach(id string, c Client) (Info, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return Info{}, ErrNotFound
	}
	if s.Info().Status != StatusRunning {
		return Info{}, ErrStopped
	}
	s.attach(c)
	return s.Info(), nil
}

// Detach removes a client from a session.
func (m *Manager) Detach(id string, c Client) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		s.detach(c)
	}
}

// WriteInput forwards client keystrokes to the session's PTY.
func (m *Manager) WriteInput(id string, data []byte) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	return s.pty.Write(data)
}

// Resize applies a new terminal size (last resize wins, §5).
func (m *Manager) Resize(id string, rows, cols uint16) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	if err := s.pty.Resize(rows, cols); err != nil {
		return err
	}
	s.mu.Lock()
	s.Rows, s.Cols = rows, cols
	s.mu.Unlock()
	return nil
}

// Shutdown marks all running sessions stopped in the store, disconnects clients
// and terminates the shell process groups (graceful shutdown, §4.6).
func (m *Manager) Shutdown() {
	m.stopOnce.Do(func() { close(m.stop) })

	m.mu.Lock()
	live := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		live = append(live, s)
	}
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()

	for _, s := range live {
		s.closeClients(closeGoingAway, "server shutting down")
		if m.store != nil {
			if err := m.store.SetStatus(s.ID, StatusStopped); err != nil {
				m.log.Error("persist stop on shutdown failed", "id", s.ID, "err", err)
			}
		}
		// Snapshot before terminating: once the shell is gone its final output
		// is only in the ring buffer, and terminate blocks until it is reaped.
		m.saveScrollback(s)
		s.terminate(killGrace)
	}
}
