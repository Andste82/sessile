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
	clients     map[Client]struct{}
	lastPersist time.Time     // throttles LastActivity DB writes (§4.6)
	exited      chan struct{} // closed by the read loop once the shell is reaped
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
	s.clients[c] = struct{}{}
}

// detach removes c from the client set.
func (s *Session) detach(c Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, c)
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

// markStopped transitions the session to stopped, notifies every client with an
// exit control frame and closes them. Returns true if it changed state.
func (s *Session) markStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Status == StatusStopped {
		return false
	}
	s.Status = StatusStopped
	for c := range s.clients {
		c.SendControl(ExitMsg{Type: "exit"})
		go c.Close(closeSessionEnded, "session ended")
		delete(s.clients, c)
	}
	return true
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

// WebSocket close codes (application range).
const (
	closeSlowConsumer = 4001
	closeSessionEnded = 4000
	closeGoingAway    = 1001
)

// timeNow is a seam for tests; defaults to time.Now in UTC.
var timeNow = func() time.Time { return time.Now().UTC() }
