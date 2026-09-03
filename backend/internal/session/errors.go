package session

import (
	"errors"
	"time"
)

var (
	// ErrNotFound is returned when no session exists for an id.
	ErrNotFound = errors.New("session not found")
	// ErrStopped is returned when attaching to a session whose shell has ended.
	ErrStopped = errors.New("session is stopped")
	// ErrAlreadyRunning is returned when restarting a session that still has a
	// live shell.
	ErrAlreadyRunning = errors.New("session is already running")
	// ErrRestarting is returned when deleting a session that is mid-restart: its
	// shell is already starting, so honouring the delete would leave the process
	// behind with nothing tracking it.
	ErrRestarting = errors.New("session is restarting")
	// ErrShuttingDown is returned when a session would be started on a Manager
	// that has begun tearing down: nothing would ever terminate it.
	ErrShuttingDown = errors.New("server is shutting down")
	// ErrInvalidName is returned for names outside the 1–64 char range.
	ErrInvalidName = errors.New("name must be 1-64 characters")
	// ErrInvalidShell is returned when a shell is not in the allowlist or is
	// not installed on PATH.
	ErrInvalidShell = errors.New("shell not allowed or not installed")
	// ErrHostNotFound is returned by Restart for an SSH session whose host
	// has since been deleted (§12b M17) — HostResolver.Resolve returns this,
	// or Restart does directly if no resolver has been wired at all.
	ErrHostNotFound = errors.New("host not found")
)

// Store persists session metadata (implemented by internal/storage).
// A nil Store means in-memory only — the Manager tolerates it.
type Store interface {
	Insert(Info) error
	SetStatus(id string, status Status) error
	Touch(id string, lastActivity time.Time) error
	Delete(id string) error
	Get(id string) (Info, bool, error)
	// LoadStopped returns all sessions persisted with stopped status.
	LoadStopped() ([]Info, error)
	// DeleteStoppedBefore removes stopped sessions last active before cutoff,
	// returning the ids removed.
	DeleteStoppedBefore(cutoff time.Time) ([]string, error)
}
