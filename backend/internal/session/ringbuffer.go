package session

import "sync"

// RingBuffer is a fixed-capacity byte buffer that retains the most recent
// bytes written to it, dropping the oldest on overflow. It backs per-session
// scrollback replay (PROJECT_PLAN.md §3): raw PTY output is appended here and
// replayed verbatim to newly-attached clients — no terminal emulation.
//
// It is safe for concurrent use.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	max  int
}

// NewRingBuffer returns a buffer retaining at most max bytes. max must be > 0.
func NewRingBuffer(max int) *RingBuffer {
	if max <= 0 {
		max = 1
	}
	return &RingBuffer{max: max, buf: make([]byte, 0, min(max, 64<<10))}
}

// Write appends p, dropping the oldest bytes if the total would exceed max.
// It never returns an error and always reports len(p) written, matching
// io.Writer semantics closely enough for a fan-out sink.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If the incoming chunk alone exceeds capacity, keep only its tail.
	if len(p) >= r.max {
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return len(p), nil
	}

	// Drop just enough of the oldest bytes to make room for p.
	if len(r.buf)+len(p) > r.max {
		drop := len(r.buf) + len(p) - r.max
		r.buf = append(r.buf[:0], r.buf[drop:]...)
	}
	r.buf = append(r.buf, p...)
	return len(p), nil
}

// Snapshot returns a copy of the current contents (oldest byte first).
func (r *RingBuffer) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// Len returns the number of bytes currently retained.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buf)
}
