package session

import (
	"log/slog"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Andste82/sessile/backend/internal/sshpty"
	"github.com/Andste82/sessile/backend/internal/terminal"
)

// HostResolver resolves a user's host id to the SSH target to connect with,
// for Restart (§12b M17) — a fresh Create already has the target handed to
// it by the caller, but a restart has only the session's stored user/host
// ids, and SSH credentials are never persisted to sqlite. Implemented in
// internal/api over the hosts.Registry (§14: scoped to userID, never a
// client-supplied id — same discipline as everywhere else this interface's
// implementation touches a filesystem path).
type HostResolver interface {
	Resolve(userID, hostID string) (target sshpty.Target, displayName string, err error)
}

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
	// hostResolver is nil until SetHostResolver is called (main.go, after
	// construction — a setter rather than a NewManager parameter so the many
	// tests that never create an SSH session don't all need to pass one).
	hostResolver HostResolver

	mu       sync.RWMutex
	sessions map[string]*Session
	// ids with a Restart in flight. A restart spawns a shell before it can
	// publish the session, and that gap is not covered by sessions alone.
	restarting map[string]struct{}
	// set by Shutdown, in the same critical section that drains sessions.
	shuttingDown bool

	// subMu guards subs and is never held while taking another lock, so
	// publish is safe to call from anywhere — see Subscribe. Maps each
	// subscriber to the user id it was subscribed for, so publish can scope
	// delivery to that user's own sessions (§10).
	subMu sync.Mutex
	subs  map[Subscriber]string

	stop     chan struct{} // closed by Shutdown to end the background loops
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
		restarting: make(map[string]struct{}),
		subs:       make(map[Subscriber]string),
		stop:       make(chan struct{}),
	}
	if dataDir != "" {
		m.scrollback = NewScrollbackStore(dataDir)
		go m.flushLoop()
	}
	// Unconditional, unlike the flush loop: the foreground is read from the PTY
	// and needs no data directory (§4.7).
	go m.foregroundLoop()
	return m
}

// SetHostResolver wires the resolver Restart uses for an SSH session. Called
// once at startup, before the server accepts requests — unsynchronized reads
// of m.hostResolver elsewhere are safe under that happens-before.
func (m *Manager) SetHostResolver(r HostResolver) {
	m.hostResolver = r
}

// CreateLocal validates inputs, starts a local PTY-backed shell, begins its
// read/broadcast goroutine, persists metadata and returns a snapshot.
func (m *Manager) CreateLocal(userID, name, dir, shell string) (Info, error) {
	now := timeNow()
	s, err := m.spawnLocal(uuid.NewString(), userID, name, dir, shell, now)
	if err != nil {
		return Info{}, err
	}
	info, err := m.register(s)
	if err != nil {
		return Info{}, err
	}
	m.log.Info("session created", "id", s.ID, "name", name, "shell", shell, "pid", s.PID)
	m.publishSession(info)
	return info, nil
}

// CreateSSH connects to target and starts a remote shell/command under it.
// hostDisplayName is snapshotted onto the session so it keeps showing a
// sensible name even if the host is later renamed or deleted (§4.2).
//
// On a host-key rejection (*sshpty.ErrHostKeyUnknown / *ErrHostKeyChanged,
// §4.5.1) this returns that error unwrapped and creates no session and no
// store row — a clean no-op the caller retries after the user trusts the
// key (§6).
func (m *Manager) CreateSSH(userID, name, hostID, hostDisplayName string, target sshpty.Target) (Info, error) {
	now := timeNow()
	s, err := m.spawnSSH(uuid.NewString(), userID, name, hostID, hostDisplayName, target, now)
	if err != nil {
		return Info{}, err
	}
	info, err := m.register(s)
	if err != nil {
		return Info{}, err
	}
	m.log.Info("session created", "id", s.ID, "name", name, "target", "ssh", "hostId", hostID)
	m.publishSession(info)
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
func (m *Manager) Restart(id, userID string) (Info, error) {
	if err := m.claimRestart(id); err != nil {
		return Info{}, err
	}
	defer m.releaseRestart(id)

	meta, err := m.restartable(id, userID)
	if err != nil {
		return Info{}, err
	}

	// The stopped session object, captured before register replaces the map
	// entry. It is nil for a session that only survives as a stopped row — the
	// state after a backend restart — and it holds the clients to migrate below.
	// The restart reservation is what makes it safe to hold across the spawn: no
	// second restart can replace it, and Delete refuses while it is held.
	prev := m.live(id)

	var s *Session
	switch meta.TargetType {
	case TargetSSH:
		// SSH credentials are never persisted to sqlite (§8), so a restart
		// re-resolves the *current* host config — including its current
		// pinned host-key fingerprint — rather than reusing anything saved
		// at create time. An edited host takes effect on the next restart
		// (documented, intentional); a deleted one fails via ErrHostNotFound
		// from the resolver; a changed remote host key surfaces the same
		// *sshpty.ErrHostKeyChanged a fresh connect would.
		if m.hostResolver == nil {
			return Info{}, ErrHostNotFound
		}
		target, displayName, rerr := m.hostResolver.Resolve(meta.UserID, meta.HostID)
		if rerr != nil {
			return Info{}, rerr
		}
		s, err = m.spawnSSH(meta.ID, meta.UserID, meta.Name, meta.HostID, displayName, target, meta.Created)
	default: // TargetLocal, and pre-M17 rows the migration defaulted to it
		s, err = m.spawnLocal(meta.ID, meta.UserID, meta.Name, meta.Directory, meta.Shell, meta.Created)
	}
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
			_, _ = s.buffer.Write(restoreSeparator(timeNow(), endsInAltScreen(replay)))
		}
	}

	if _, err := m.register(s); err != nil {
		return Info{}, err
	}

	// Hand the stopped shell's viewers to the replacement. They are the clients
	// that watched it stop — every browser that had this session open, not only
	// the one that asked for the restart — and attach re-primes each of them with
	// the attached frame and the restored scrollback. That frame is what tells
	// them the session is live again; without it they sat on a dead socket
	// offering to start a session someone else had already started.
	//
	// After register, so they are handed to the published session, whose read
	// loop broadcasts under the same lock attach takes: no output can slip in
	// ahead of a client's replay.
	if prev != nil {
		clients := prev.takeClients()
		for _, c := range clients {
			s.attach(c)
		}
		if len(clients) > 0 {
			m.log.Info("clients moved to the restarted session", "id", s.ID, "clients", len(clients))
		}
	}

	m.log.Info("session restarted", "id", s.ID, "name", s.Name, "shell", s.Shell, "pid", s.PID)
	// Snapshotted after the migration so the reply carries the real client count.
	info := s.Info()
	m.publishSession(info)
	return info, nil
}

// claimRestart reserves id for one restart, refusing if the session is already
// running or another restart is mid-flight.
//
// Checking that a session is stopped and publishing its replacement were the two
// ends of a window with a fork+exec in the middle, and nothing held the id
// across it: two restarts of the same session — two browser tabs, a double
// click, any second client — both passed the check, both started a shell, and
// register kept the last one. The other shell went on running, reachable through
// no API, terminated by no shutdown, and still writing its scrollback over the
// live session's snapshot under the id they share.
//
// The reservation covers the gap. It cannot be the sessions map itself: the
// replacement does not exist yet, and the spawn that creates it must not run
// under the manager lock, which every keystroke needs.
func (m *Manager) claimRestart(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		// register would refuse anyway; refusing here spares the fork.
		return ErrShuttingDown
	}
	if _, ok := m.restarting[id]; ok {
		return ErrAlreadyRunning
	}
	if s, ok := m.sessions[id]; ok && s.Info().Status == StatusRunning {
		return ErrAlreadyRunning
	}
	m.restarting[id] = struct{}{}
	return nil
}

// releaseRestart ends the reservation, whether the restart succeeded or not.
func (m *Manager) releaseRestart(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.restarting, id)
}

// live returns the session object currently published under id, or nil.
func (m *Manager) live(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// restartable returns the metadata to restart id with, or an error explaining
// why it cannot be restarted. A session owned by someone else is reported
// exactly like one that doesn't exist (§4.5, §10) — deliberately, so probing
// an id you don't own learns nothing.
func (m *Manager) restartable(id, userID string) (Info, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()

	if ok {
		info := s.Info()
		if info.UserID != userID {
			return Info{}, ErrNotFound
		}
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
	if !found || info.UserID != userID {
		return Info{}, ErrNotFound
	}
	if info.Status == StatusRunning {
		// Only reachable if another process owns this row; the store reconciles
		// stale running rows to stopped on open (§8).
		return Info{}, ErrAlreadyRunning
	}
	return info, nil
}

// spawnLocal validates the inputs, starts a local PTY-backed shell and
// returns the resulting Session without registering it. Shared by
// CreateLocal and Restart so a restart runs through exactly the same
// allowlist and sandbox checks (§4.5) — a directory that has since been
// deleted, or a shell dropped from the allowlist, must fail here rather
// than at PTY start.
func (m *Manager) spawnLocal(id, userID, name, dir, shell string, created time.Time) (*Session, error) {
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
		UserID:       userID,
		TargetType:   TargetLocal,
		Directory:    dir,
		Shell:        shell,
		Status:       StatusRunning,
		PID:          pty.Pid(),
		Created:      created,
		LastActivity: now,
		Rows:         defaultRows,
		Cols:         defaultCols,
		backend:      pty,
		buffer:       NewRingBuffer(m.bufferSize),
		clients:      make(map[Client]clientGeom),
		lastPersist:  now,
		exited:       make(chan struct{}),
	}, nil
}

// spawnSSH validates the inputs, connects to target and returns the
// resulting Session without registering it. Shared by CreateSSH and Restart,
// same reason as spawnLocal — a name that fails validation, or a host key
// that no longer matches, must fail here rather than partway through.
//
// hostKeyErr (an *sshpty.ErrHostKeyUnknown or *ErrHostKeyChanged) is
// returned unwrapped so the caller can map it to the API's distinct
// host-key responses (§4.5.1, §6) instead of a generic connection failure.
func (m *Manager) spawnSSH(id, userID, name, hostID, hostDisplayName string, target sshpty.Target, created time.Time) (*Session, error) {
	if l := len(name); l < 1 || l > 64 {
		return nil, ErrInvalidName
	}

	pty, err := sshpty.Start(target, defaultRows, defaultCols)
	if err != nil {
		return nil, err
	}

	now := timeNow()
	return &Session{
		ID:              id,
		Name:            name,
		UserID:          userID,
		TargetType:      TargetSSH,
		HostID:          hostID,
		HostDisplayName: hostDisplayName,
		Status:          StatusRunning,
		PID:             pty.Pid(),
		Created:         created,
		LastActivity:    now,
		Rows:            defaultRows,
		Cols:            defaultCols,
		backend:         pty,
		buffer:          NewRingBuffer(m.bufferSize),
		clients:         make(map[Client]clientGeom),
		lastPersist:     now,
		exited:          make(chan struct{}),
	}, nil
}

// register publishes a spawned session, persists it and starts its read loop.
//
// A Manager that has begun shutting down publishes nothing. Its map has already
// been drained and every shell in it terminated, so a session landing here now
// would be tracked by nothing and terminated by nothing: the process exits and
// the shell, which Setsid gave a session of its own, outlives it. The shell is
// discarded here rather than handed back, because a caller holding an
// unpublished session has no other way to reach it.
func (m *Manager) register(s *Session) (Info, error) {
	m.mu.Lock()
	if m.shuttingDown {
		m.mu.Unlock()
		s.discard()
		return Info{}, ErrShuttingDown
	}
	m.sessions[s.ID] = s
	m.mu.Unlock()

	info := s.Info()
	if m.store != nil {
		if err := m.store.Insert(info); err != nil {
			m.log.Error("persist session failed", "id", s.ID, "err", err)
		}
	}
	go m.readLoop(s)
	return info, nil
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
		n, err := s.backend.Read(buf)
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
	// Snapshot before anything is told the session stopped. The buffer is final
	// the moment this loop breaks — broadcast is the only writer and it runs
	// here — and markStopped both flips the status and sends the exit frame. A
	// client that acts on either can call Restart immediately, and Restart reads
	// this file: writing it afterwards would hand that restart a missing or
	// stale scrollback.
	m.saveScrollback(s)

	if s.markStopped() {
		if m.store != nil {
			if err := m.store.SetStatus(s.ID, StatusStopped); err != nil {
				m.log.Error("persist stop failed", "id", s.ID, "err", err)
			}
		}
		// The snapshot is on disk and nothing can read the buffer again.
		s.releaseBuffer()
		m.log.Info("session stopped", "id", s.ID)
		// Clear the derived state before announcing it, or the dashboard keeps
		// showing the program the session was running when its shell died.
		info, _ := s.clearForeground()
		// Unless the session has already been deleted. This loop can outlive a
		// delete by as long as some process outside the shell's group holds the
		// terminal open, and by then every subscriber has been told the session
		// is gone. Announcing it again puts it straight back on their dashboards
		// as a stopped session that nothing can remove.
		if !s.isDiscarded() {
			m.publishSession(info)
		}
	}
	// Reap the shell process (single reaper), close the master, then signal
	// that termination is complete for any waiter in terminate().
	s.backend.Wait()
	s.backend.CloseFile()
	close(s.exited)
}

// saveScrollback writes a running session's ring buffer to disk, and is a no-op
// for one that has stopped: its final snapshot was written by the read loop and
// its buffer released, so saving again could only replace good output with an
// empty file. Deciding that here rather than at each call site is what keeps the
// decision atomic — see Session.snapshotRunning.
//
// Failures are logged, never fatal: losing a snapshot costs history, not
// correctness.
func (m *Manager) saveScrollback(s *Session) {
	if m.scrollback == nil {
		return
	}
	data, ok := s.snapshotRunning()
	if !ok {
		return
	}
	if err := m.scrollback.Save(s.ID, data); err != nil {
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
				m.saveScrollback(s)
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
// sessions that only survive as stopped rows after a restart. A session
// owned by someone else is reported exactly like one that doesn't exist
// (§4.5, §10).
func (m *Manager) Get(id, userID string) (Info, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		info := s.Info()
		if info.UserID != userID {
			return Info{}, ErrNotFound
		}
		return info, nil
	}
	if m.store != nil {
		info, found, err := m.store.Get(id)
		if err != nil {
			return Info{}, err
		}
		if found && info.UserID == userID {
			return info, nil
		}
	}
	return Info{}, ErrNotFound
}

// List returns userID's sessions: live ones from memory merged with stopped
// rows from the store, newest first. Strictly owner-scoped — an admin's
// list is exactly as scoped as anyone else's (§10, decision recorded in
// PROJECT_PLAN.md §12b M17).
func (m *Manager) List(userID string) ([]Info, error) {
	m.mu.RLock()
	infos := make([]Info, 0, len(m.sessions))
	seen := make(map[string]struct{}, len(m.sessions))
	for _, s := range m.sessions {
		info := s.Info()
		seen[info.ID] = struct{}{}
		if info.UserID == userID {
			infos = append(infos, info)
		}
	}
	m.mu.RUnlock()

	if m.store != nil {
		stopped, err := m.store.LoadStopped()
		if err != nil {
			return nil, err
		}
		for _, si := range stopped {
			if _, ok := seen[si.ID]; !ok && si.UserID == userID {
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
//
// A session with a restart in flight is refused with ErrRestarting rather than
// deleted. Its replacement shell is already starting but not yet published, so a
// delete that ran now would remove the row and the state and then watch register
// put a session back under the id it just erased — one that no shutdown would
// ever terminate. Refusing costs the caller a retry once the restart lands, and
// the delete then does the whole job.
//
// The check shares the critical section that publishes and unpublishes
// sessions, so a delete either sees the reservation or runs wholly outside it.
func (m *Manager) Delete(id, userID string) error {
	m.mu.Lock()
	if _, restarting := m.restarting[id]; restarting {
		m.mu.Unlock()
		return ErrRestarting
	}
	s, ok := m.sessions[id]
	if ok {
		if s.Info().UserID != userID {
			m.mu.Unlock()
			return ErrNotFound
		}
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	if !ok {
		// Might be a stopped session that only exists in the store.
		if m.store != nil {
			if info, found, err := m.store.Get(id); err == nil && found {
				if info.UserID != userID {
					return ErrNotFound
				}
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
	// Before terminating, so there is no window in which the read loop could
	// still take a snapshot of a session that is on its way out.
	s.markDiscarded()
	if !s.terminate(killGrace) {
		m.log.Warn("session shell outlived its delete: a process outside its "+
			"group is holding the terminal open; the read loop will finish when "+
			"that process exits", "id", id, "pid", s.PID)
	}
	if m.store != nil {
		if err := m.store.Delete(id); err != nil {
			return err
		}
	}
	m.discardState(id)
	m.log.Info("session deleted", "id", id)
	m.publishGone(id, userID)
	return nil
}

// PruneStopped discards stopped sessions that have been idle longer than
// retention, along with their scrollback and history, and reports how many went.
// A retention of zero disables it.
//
// Without this the sessions table only ever grows: every session ever created
// leaves a row, and after this change a scrollback snapshot and a history file
// too. It is off by default and meant to be called at startup, before any
// session is live — a stopped session is no longer a dead end now that it can be
// restarted with its output and command history, so discarding one on a timer is
// an operator's decision, not a default.
func (m *Manager) PruneStopped(retention time.Duration) (int, error) {
	if retention <= 0 || m.store == nil {
		return 0, nil
	}
	ids, err := m.store.DeleteStoppedBefore(timeNow().Add(-retention))
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		m.discardState(id)
	}
	if len(ids) > 0 {
		// .String() because slog renders a Duration as raw nanoseconds, which is
		// unreadable in the one line an operator sees this feature act.
		m.log.Info("pruned stopped sessions", "count", len(ids), "retention", retention.String())
	}
	return len(ids), nil
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
func (m *Manager) Rename(id, userID, name string) (Info, error) {
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
	if s.UserID != userID {
		s.mu.Unlock()
		return Info{}, ErrNotFound
	}
	s.Name = name
	info := s.infoLocked()
	s.mu.Unlock()
	if m.store != nil {
		if err := m.store.Insert(info); err != nil { // upsert
			return Info{}, err
		}
	}
	m.publishSession(info)
	return info, nil
}

// Attach registers a client on a running session (sends attached + replay).
// A session owned by someone else closes exactly like one that doesn't
// exist (ws.Handler's existing closeSessionUnavailable path, §5) —
// deliberately indistinguishable, same as Get/Restart.
func (m *Manager) Attach(id, userID string, c Client) (Info, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return Info{}, ErrNotFound
	}
	info := s.Info()
	if info.UserID != userID {
		return Info{}, ErrNotFound
	}
	if info.Status != StatusRunning {
		return Info{}, ErrStopped
	}
	s.attach(c)
	return s.Info(), nil
}

// Detach removes a client from a session. The session is resized afterwards:
// the client that leaves may have been the one holding the size down, and the
// windows that remain should get their space back.
func (m *Manager) Detach(id string, c Client) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		s.detach(c)
	}
}

// WriteInput forwards client keystrokes to the session's PTY.
//
// A stopped session reports ErrStopped rather than the write error from a
// closed PTY. Clients now stay attached across a stop, waiting to be told the
// session came back, so typing into the dead terminal has to be a no-op the
// caller can recognise instead of a failure that tears the connection down.
func (m *Manager) WriteInput(id string, data []byte) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	if s.Info().Status != StatusRunning {
		return ErrStopped
	}
	return s.backend.Write(data)
}

// Resize records what one client can display and sizes the PTY to the smallest
// of them (§5).
//
// One PTY serves every attached client, and each renders it in a window of its
// own — a phone beside a desktop is the ordinary case here, not a corner one.
// Sizing to whoever spoke last leaves the others rendering a width the program
// is not writing for: lines wrap where they should not, and a full-screen
// program cleaning up after itself moves the cursor over rows that are not the
// ones it drew, which is how the leftovers appear. The smallest size fits in
// every window, which is the same answer tmux reaches for the same reason.
//
// A client whose window is larger than the session simply has unused space,
// exactly as a tmux client does.
func (m *Manager) Resize(id string, c Client, rows, cols uint16) error {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	return s.resize(c, rows, cols)
}

// Shutdown marks all running sessions stopped in the store, disconnects clients
// and terminates the shell process groups (graceful shutdown, §4.6).
func (m *Manager) Shutdown() {
	m.stopOnce.Do(func() { close(m.stop) })

	m.mu.Lock()
	// Set with the drain, not before or after it: from here on register refuses,
	// so a session spawned by a call already in flight cannot slip into the map
	// behind this loop and outlive the process.
	m.shuttingDown = true
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
		// A session that already stopped is skipped inside saveScrollback.
		m.saveScrollback(s)
		if !s.terminate(killGrace) {
			// Not fatal, and no longer able to stall the shutdown: the snapshot
			// above is already on disk, so the session restarts intact next time.
			m.log.Warn("shell outlived shutdown: a process outside its group is "+
				"holding the terminal open", "id", s.ID, "pid", s.PID)
		}
	}
	m.closeSubscribers()
}

// closeSubscribers disconnects the event channel on shutdown, with the same
// close code a terminal client gets (§5). Last, so a dashboard is told about
// the sessions going down before its own socket does.
func (m *Manager) closeSubscribers() {
	m.subMu.Lock()
	subs := make([]Subscriber, 0, len(m.subs))
	for sub := range m.subs {
		subs = append(subs, sub)
	}
	m.subs = make(map[Subscriber]string)
	m.subMu.Unlock()

	for _, sub := range subs {
		sub.Close(closeGoingAway, "server shutting down")
	}
}
