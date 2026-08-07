package session

import (
	"sync"
	"syscall"
	"time"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// Status is a session's lifecycle state.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
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
	// mu guards the mutable runtime fields below (status, geometry, activity,
	// clients, buffer coordination). Held during broadcast and attach so the
	// buffer-replay / live-stream ordering is race-free (§3, §4.4).
	mu sync.Mutex

	ID           string
	Name         string
	Directory    string // relative to root, as supplied by the user
	Shell        string
	Status       Status
	PID          int
	Created      time.Time
	LastActivity time.Time
	Rows, Cols   uint16

	// runtime-only
	pty         *terminal.PTY
	buffer      *RingBuffer
	vt          vtScanner // terminal modes seen in the output stream (§4.7)
	clients     map[Client]clientGeom
	lastPersist time.Time     // throttles LastActivity DB writes (§4.6)
	exited      chan struct{} // closed by the read loop once the shell is reaped
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
	ID           string
	Name         string
	Directory    string
	Shell        string
	Status       Status
	PID          int
	Created      time.Time
	LastActivity time.Time
	Rows, Cols   uint16
	ClientCount  int
}

// Info returns a copy of the session's public fields.
func (s *Session) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *Session) infoLocked() Info {
	return Info{
		ID:           s.ID,
		Name:         s.Name,
		Directory:    s.Directory,
		Shell:        s.Shell,
		Status:       s.Status,
		PID:          s.PID,
		Created:      s.Created,
		LastActivity: s.LastActivity,
		Rows:         s.Rows,
		Cols:         s.Cols,
		ClientCount:  len(s.clients),
	}
}

// attach registers c and primes it with the attached control frame followed by
// the current ring-buffer replay, atomically w.r.t. broadcast so live output
// can never interleave ahead of the replay (§5 attach sequence).
func (s *Session) attach(c Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	replay := s.buffer.Snapshot()
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
	if err := s.pty.Resize(rows, cols); err != nil {
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
	// The scanner runs here rather than on its own goroutine because this is
	// already the one place every output byte passes, under the lock that makes
	// the session's view of itself consistent. It is a byte loop over bytes the
	// ring buffer just copied anyway, so there is nothing to queue and nothing
	// that can be dropped — unlike the client fan-out below (§4.7).
	s.vt.Feed(data)
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
	if s.Status != StatusRunning {
		return nil, false
	}
	return s.buffer.Snapshot(), true
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
func (s *Session) terminate(grace time.Duration) {
	s.pty.Signal(syscall.SIGHUP)
	s.pty.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
	case <-time.After(grace):
		s.pty.Signal(syscall.SIGKILL)
		<-s.exited
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
	s.pty.Signal(syscall.SIGKILL)
	s.pty.Wait()
	s.pty.CloseFile()
}

// WebSocket close codes (application range).
const (
	closeSlowConsumer = 4001
	closeSessionEnded = 4000
	closeGoingAway    = 1001
)

// timeNow is a seam for tests; defaults to time.Now in UTC.
var timeNow = func() time.Time { return time.Now().UTC() }
