package session

import (
	"sync"
	"syscall"
	"time"

	"github.com/Andste82/sessile/backend/internal/hostops"
)

// Status is a session's lifecycle state.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
)

// TargetType selects what a Session's Backend actually is.
type TargetType string

const (
	TargetLocal TargetType = "local"
	TargetSSH   TargetType = "ssh"
)

// Default terminal geometry until the first client resize arrives.
const (
	defaultRows uint16 = 24
	defaultCols uint16 = 80
)

// Session is a persistent PTY-backed shell plus its attached clients and
// scrollback ring buffer. Metadata fields are persisted (§8); runtime fields
// are never persisted.
type Session struct {
	// mu guards the mutable runtime fields below (status, geometry, foreground,
	// clients, buffer coordination). Held during broadcast and attach so the
	// buffer-replay / live-stream ordering is race-free (§3, §4.4).
	mu sync.Mutex

	ID     string
	Name   string
	UserID string // owner — every lookup on Manager is scoped to this (§4.5, §10)

	TargetType TargetType // "local" | "ssh"
	Directory  string     // relative to root, as supplied by the user (local only)
	Shell      string     // local only
	HostID     string     // the owning user's host id (ssh only)
	// HostDisplayName is the host's name, snapshotted at creation time (ssh
	// only) — survives the host being renamed or deleted after the fact.
	HostDisplayName string

	Status       Status
	PID          int
	Created      time.Time
	LastActivity time.Time
	Rows, Cols   uint16

	// derived foreground, refreshed by the manager's sampler (§4.7). Both are
	// empty for a session that is not running, and always empty for an SSH
	// session — there is no way to introspect a remote process's /proc.
	fgCommand string // foreground program name
	fgCwd     string // its working directory, relative to root

	// title is the window title the program in the session last set (§4.8).
	// Unlike the two above it is scanned out of the output stream by the read
	// loop rather than sampled, so titleDirty carries the news to the sampler,
	// which is what publishes it.
	title      string
	titleDirty bool

	// modes is the terminal state the program in the session has switched on
	// for itself — alternate screen, mouse tracking, bracketed paste (§4.9).
	// Scanned out of the output stream by the read loop like the title above,
	// and read by attach, which writes it ahead of the replay.
	modes termModes

	// runtime-only
	backend     Backend
	hostOps     *hostops.HostSession // process tree + file browser/transfer (§4.10)
	buffer      *RingBuffer
	clients     map[Client]clientGeom
	lastPersist time.Time     // throttles LastActivity DB writes (§4.6)
	exited      chan struct{} // closed by the read loop once the shell is reaped
	discarded   bool          // Delete has removed this session and its files
}

// clientGeom is the terminal size one attached client reports. A client that
// has not sent a resize frame yet has none, and takes no part in the size the
// session settles on.
type clientGeom struct {
	rows, cols uint16
	known      bool
}

// Info is a snapshot of a session's public fields, safe to hand to the API and
// storage layers without exposing internal pointers.
type Info struct {
	ID              string
	Name            string
	UserID          string
	TargetType      TargetType
	Directory       string
	Shell           string
	HostID          string
	HostDisplayName string
	Status          Status
	PID             int
	Created         time.Time
	LastActivity    time.Time
	Rows, Cols      uint16
	ClientCount     int

	// Derived, never persisted (§4.7, §4.8). Empty for a stopped session, and
	// where they cannot be determined.
	Command string
	Cwd     string
	Title   string
}

// Info returns a copy of the session's public fields.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

// HostOps returns the session's process-tree/file-browser handle (§4.10).
// Set once at spawn and never reassigned, so this needs no lock.
func (s *Session) HostOps() *hostops.HostSession {
	return s.hostOps
}

func (s *Session) infoLocked() Info {
	return Info{
		ID:              s.ID,
		Name:            s.Name,
		UserID:          s.UserID,
		TargetType:      s.TargetType,
		Directory:       s.Directory,
		Shell:           s.Shell,
		HostID:          s.HostID,
		HostDisplayName: s.HostDisplayName,
		Status:          s.Status,
		PID:             s.PID,
		Created:         s.Created,
		LastActivity:    s.LastActivity,
		Rows:            s.Rows,
		Cols:            s.Cols,
		ClientCount:     len(s.clients),
		Command:         s.fgCommand,
		Cwd:             s.fgCwd,
		Title:           s.title,
	}
}

// attach registers c and primes it with the attached control frame followed by
// the current ring-buffer replay, atomically w.r.t. broadcast so live output
// can never interleave ahead of the replay (§5 attach sequence).
//
// The replay is filtered here rather than on the way into the ring buffer:
// sanitizeReplay is what keeps a replayed question from being answered into a
// live shell (§8), and this is the one place where the whole snapshot is in
// hand at once — on the write side a query can be split across two PTY reads.
// It is a byte loop over a buffer Snapshot has just copied anyway, and it runs
// once per attach, not per chunk of output.
//
// The mode preamble goes in front of it (§4.9). The ring buffer is bounded, so
// a program that repaints eventually pushes its own `ESC [ ? 1049 h` off the
// front and the replay alone would leave a fresh terminal on the normal screen
// with mouse reporting off. Whatever the replay still carries is applied after
// the preamble and wins, so a snapshot that survived intact is unaffected.
// `replayBytes` counts both, which is what §5 asks of it: what is actually sent.
func (s *Session) attach(c Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replay := sanitizeReplay(s.buffer.Snapshot())
	if pre := s.modes.preamble(); len(pre) > 0 {
		// Prepended rather than sent as a frame of its own: §5 describes the
		// replay as one binary send, and the guard keeps a session with nothing
		// to restore on the copy-free path sanitizeReplay already gives it.
		replay = append(pre, replay...)
	}
	c.SendControl(newAttached(s.ID, len(replay)))
	if len(replay) > 0 {
		c.Send(replay)
	}
	// No geometry yet: the client sends its own the moment its socket opens,
	// and until then it must not drag the session down to a size nobody has.
	s.clients[c] = clientGeom{}
}

// detach removes c from the client set and gives the size it was holding down
// back to the clients that remain.
func (s *Session) detach(c Client) {
	s.mu.Lock()
	delete(s.clients, c)
	rows, cols, ok := s.smallestLocked()
	s.mu.Unlock()
	if ok {
		s.applySize(rows, cols)
	}
}

// resize records what c can display and sizes the session to the smallest
// window attached to it — see Manager.Resize for why the smallest one wins.
//
// A resize from a client that is not attached is ignored rather than applied:
// it has no window to fit, and honouring it would let a closed connection set a
// size nobody can see.
func (s *Session) resize(c Client, rows, cols uint16) error {
	s.mu.Lock()
	if _, attached := s.clients[c]; !attached {
		s.mu.Unlock()
		return nil
	}
	s.clients[c] = clientGeom{rows: rows, cols: cols, known: true}
	rows, cols, ok := s.smallestLocked()
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.applySize(rows, cols)
}

// smallestLocked returns the largest size every attached client can display —
// the smallest reported rows and the smallest reported cols, taken
// independently, since a phone in portrait and a wide desktop window each
// constrain a different axis. ok is false while nobody has reported a size,
// where the session keeps whatever it has rather than falling back to a default
// that no window asked for.
//
// s.mu must be held.
func (s *Session) smallestLocked() (rows, cols uint16, ok bool) {
	for _, g := range s.clients {
		if !g.known {
			continue
		}
		if !ok || g.rows < rows {
			rows = g.rows
		}
		if !ok || g.cols < cols {
			cols = g.cols
		}
		ok = true
	}
	return rows, cols, ok
}

// applySize resizes the PTY, and records the size on the session, if it is not
// the size already in force. Resizing raises SIGWINCH, so repeating one costs
// every full-screen program a redraw it does not need.
func (s *Session) applySize(rows, cols uint16) error {
	s.mu.Lock()
	unchanged := s.Rows == rows && s.Cols == cols
	s.mu.Unlock()
	if unchanged {
		return nil
	}
	if err := s.backend.Resize(rows, cols); err != nil {
		return err
	}
	s.mu.Lock()
	s.Rows, s.Cols = rows, cols
	s.mu.Unlock()
	return nil
}

// broadcast appends data to the ring buffer and fans it out to every attached
// client. A client whose write channel is full is a slow consumer: it is
// dropped and closed without blocking the others (§4.4).
func (s *Session) broadcast(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.buffer.Write(data)
	s.LastActivity = timeNow()
	for c := range s.clients {
		if !c.Send(data) {
			delete(s.clients, c)
			go c.Close(closeSlowConsumer, "slow consumer")
		}
	}
}

// markStopped transitions the session to stopped and notifies every client with
// an exit control frame. Returns true if it changed state.
//
// The clients stay attached. A stopped session can be restarted under the same
// id, and the connection that is showing "session ended" is precisely the one
// that has to be told when it comes back — whoever restarted it. Closing them
// here left every other client with a dead socket and a restart dialog nothing
// would ever take down, so it kept offering to start a session that was already
// running. Restart hands them to the replacement (see Manager.Restart); Delete
// and Shutdown still close them, and the keep-alive still reaps a client whose
// browser went away.
func (s *Session) markStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == StatusStopped {
		return false
	}
	s.Status = StatusStopped
	for c := range s.clients {
		c.SendControl(ExitMsg{Type: "exit"})
	}
	return true
}

// takeClients removes and returns every attached client, for handing them to
// the session that replaces this one. They are removed under the same lock that
// returns them so a concurrent second caller cannot hand the same connection to
// two sessions.
func (s *Session) takeClients() []Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Client, 0, len(s.clients))
	for c := range s.clients {
		out = append(out, c)
		delete(s.clients, c)
	}
	return out
}

// snapshotRunning returns a copy of the scrollback, or false once the session
// has stopped.
//
// The status check and the read of the buffer happen under the one lock that
// stops the session, and that is the whole point. Both were separate steps
// before, so a snapshot could straddle markStopped: the caller saw a running
// session, the read loop then wrote the final snapshot and released the buffer,
// and the caller went on to save the empty buffer it had just been handed —
// over the good snapshot, leaving a restart with nothing to restore.
func (s *Session) snapshotRunning() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status != StatusRunning || s.discarded {
		return nil, false
	}
	return s.buffer.Snapshot(), true
}

// markDiscarded records that Delete has removed this session, so the read loop
// does not write a scrollback file for it.
//
// It matters because the read loop can outlive the delete. terminate gives up
// on a shell whose PTY is held open from outside its process group, and the read
// loop then finishes minutes later and reaches its snapshot — after discardState
// has already removed the session's files, leaving an orphan on disk under an id
// nothing refers to any more.
//
// In the ordinary case this only saves a write: the file would be deleted
// moments later anyway.
func (s *Session) markDiscarded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discarded = true
}

// isDiscarded reports whether Delete has already removed this session.
func (s *Session) isDiscarded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.discarded
}

// releaseBuffer drops the scrollback of a stopped session.
//
// A stopped session stays in the Manager's map — it is still listed, still
// restartable — but its ring buffer is unreachable from that moment on, because
// Attach rejects anything that is not running. Holding up to --buffer-size per
// dead session for the lifetime of the process buys nothing; the contents have
// already been snapshotted to disk, which is where Restart reads them from.
//
// The buffer is replaced rather than nilled: attach and broadcast dereference it
// without checking, and a stopped session is not worth a nil guard on the two
// hottest paths in the package.
func (s *Session) releaseBuffer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffer = NewRingBuffer(1)
}

// closeClients disconnects all attached clients without changing status (used
// on graceful shutdown).
func (s *Session) closeClients(code int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		go c.Close(code, reason)
		delete(s.clients, c)
	}
}

// terminate stops the shell: SIGHUP+SIGTERM the process group, wait up to grace
// for the read loop to reap it, then SIGKILL as a last resort (§4.3). It relies
// on the read loop closing s.exited after Wait, so there is a single reaper.
//
// Reports whether the shell was reaped. False means the read loop is still
// blocked on the PTY, because a process outside the shell's process group is
// holding the slave open: anything that calls setsid() survives a signal aimed
// at that group — a daemon, tmux, screen — and while it lives, the master never
// reaches EOF.
//
// The second wait is bounded for that case. It used to be unbounded, which made
// this a permanent hang rather than a slow path: the HTTP request that asked for
// the delete never returned, and a graceful shutdown never finished, because
// both wait here.
//
// Giving up is the only option available. Closing the master does not help —
// creack/pty hands back a blocking file, so a Read already in the kernel cannot
// be interrupted by a Close, which returns nil while the read stays put. The
// read loop is not leaked, only late: it finishes on its own the moment the last
// holder of the slave lets go, and runs its usual cleanup then.
func (s *Session) terminate(grace time.Duration) bool {
	s.backend.Signal(syscall.SIGHUP)
	s.backend.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
		return true
	case <-time.After(grace):
	}

	s.backend.Signal(syscall.SIGKILL)
	select {
	case <-s.exited:
		return true
	case <-time.After(grace):
		return false
	}
}

// discard kills and reaps a shell that was spawned but never published.
//
// terminate cannot do this job: it waits for the read loop to close s.exited,
// and a session that was never registered never got one. Nothing has ever seen
// this shell — it has been alive for the length of a failed publish and holds no
// user state — so it goes straight to SIGKILL, and this goroutine is the only
// reaper it will ever have.
func (s *Session) discard() {
	s.backend.Signal(syscall.SIGKILL)
	s.backend.Wait()
	s.backend.CloseFile()
}

// WebSocket close codes (application range).
const (
	closeSlowConsumer = 4001
	closeSessionEnded = 4000
	closeGoingAway    = 1001
)

// timeNow is a seam for tests; defaults to time.Now in UTC.
var timeNow = func() time.Time { return time.Now().UTC() }
