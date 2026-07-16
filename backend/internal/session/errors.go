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
	// ErrInvalidName is returned for names outside the 1–64 char range.
	ErrInvalidName = errors.New("name must be 1-64 characters")
	// ErrInvalidShell is returned when a shell is not in the allowlist or is
	// not installed on PATH.
	ErrInvalidShell = errors.New("shell not allowed or not installed")
)

// Store persists session metadata (implemented by internal/storage in M2).
// A nil Store means in-memory only — the Manager tolerates it.
type Store interface {
	Insert(Info) error
	SetStatus(id string, status Status) error
	Touch(id string, lastActivity time.Time) error
	Delete(id string) error
	Get(id string) (Info, bool, error)
	// LoadStopped returns all sessions persisted with stopped status.
	LoadStopped() ([]Info, error)
}
