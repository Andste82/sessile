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
	log        *slog.Logger
	store      Store // may be nil (in-memory only)

	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewManager constructs a Manager. store may be nil.
func NewManager(root string, shells []string, bufferSize int, store Store, log *slog.Logger) *Manager {
	return &Manager{
		root:       root,
		shells:     shells,
		bufferSize: bufferSize,
		log:        log,
		store:      store,
		sessions:   make(map[string]*Session),
	}
}

// Create validates inputs, starts a PTY-backed shell, begins its read/broadcast
// goroutine, persists metadata and returns a snapshot.
func (m *Manager) Create(name, dir, shell string) (Info, error) {
	if l := len(name); l < 1 || l > 64 {
		return Info{}, ErrInvalidName
	}
	shellPath, err := m.resolveShell(shell)
	if err != nil {
		return Info{}, err
	}
	resolvedDir, err := resolveDir(m.root, dir)
	if err != nil {
		return Info{}, err
	}

	pty, err := terminal.Start(shellPath, resolvedDir, defaultRows, defaultCols)
	if err != nil {
		return Info{}, err
	}

	now := timeNow()
	s := &Session{
		ID:           uuid.NewString(),
		Name:         name,
		Directory:    dir,
		Shell:        shell,
		Status:       StatusRunning,
		PID:          pty.Pid(),
		Created:      now,
		LastActivity: now,
		Rows:         defaultRows,
		Cols:         defaultCols,
		pty:          pty,
		buffer:       NewRingBuffer(m.bufferSize),
		clients:      make(map[Client]struct{}),
		lastPersist:  now,
		exited:       make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	if m.store != nil {
		if err := m.store.Insert(s.Info()); err != nil {
			m.log.Error("persist session failed", "id", s.ID, "err", err)
		}
	}

	go m.readLoop(s)
	m.log.Info("session created", "id", s.ID, "name", name, "shell", shell, "pid", s.PID)
	return s.Info(), nil
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
		m.log.Info("session stopped", "id", s.ID)
	}
	// Reap the shell process (single reaper), close the master, then signal
	// that termination is complete for any waiter in terminate().
	s.pty.Wait()
	s.pty.CloseFile()
	close(s.exited)
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
				return m.store.Delete(id)
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
	m.log.Info("session deleted", "id", id)
	return nil
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
		s.terminate(killGrace)
	}
}
