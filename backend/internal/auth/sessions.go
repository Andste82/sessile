package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// sweepInterval bounds how often expired tokens are purged from memory. It
// need not be frequent — an expired token is already rejected by Lookup —
// this only reclaims the map entry.
const sweepInterval = time.Minute

// DefaultSessionTTL is the sliding window a web session token stays valid
// for after its last use (§10, §11).
const DefaultSessionTTL = 30 * 24 * time.Hour

// SessionStore is an in-memory, sliding-TTL web session token store (§10,
// §11): not JWT, not persisted to disk or the database — a server restart
// logs everyone out, which is an accepted simplification for this project.
// Every successful Lookup renews the token's expiry, so an actively-used
// session never expires but an abandoned one lapses after ttl of inactivity.
type SessionStore struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]entry

	stop     chan struct{}
	stopOnce sync.Once
}

type entry struct {
	userID    string
	expiresAt time.Time
}

// NewSessionStore constructs a SessionStore and starts its background sweep
// goroutine, stopped by Stop.
func NewSessionStore(ttl time.Duration) *SessionStore {
	s := &SessionStore{
		ttl:     ttl,
		entries: make(map[string]entry),
		stop:    make(chan struct{}),
	}
	go s.sweepLoop()
	return s
}

// Create issues a new random token for userID, valid for ttl from now.
func (s *SessionStore) Create(userID string) string {
	token := randomToken()
	s.mu.Lock()
	s.entries[token] = entry{userID: userID, expiresAt: time.Now().Add(s.ttl)}
	s.mu.Unlock()
	return token
}

// Lookup returns the user id for token if it exists and hasn't expired, and
// — the sliding part — renews its expiry to ttl from now. A caller that
// finds ok false should treat the token exactly as if it were never issued.
func (s *SessionStore) Lookup(token string) (userID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, found := s.entries[token]
	if !found {
		return "", false
	}
	now := time.Now()
	if now.After(e.expiresAt) {
		delete(s.entries, token)
		return "", false
	}
	e.expiresAt = now.Add(s.ttl)
	s.entries[token] = e
	return e.userID, true
}

// Revoke invalidates one token (logout).
func (s *SessionStore) Revoke(token string) {
	s.mu.Lock()
	delete(s.entries, token)
	s.mu.Unlock()
}

// RevokeByUser invalidates every token issued to userID — used when an
// admin deletes an account (§12b M11), so a session that was already open
// doesn't keep working after the account is gone.
func (s *SessionStore) RevokeByUser(userID string) {
	s.mu.Lock()
	for token, e := range s.entries {
		if e.userID == userID {
			delete(s.entries, token)
		}
	}
	s.mu.Unlock()
}

// Stop ends the background sweep goroutine. Safe to call more than once.
func (s *SessionStore) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
}

// sweepLoop periodically reclaims expired entries. Lookup already rejects an
// expired token on its own, so this is only about not leaking memory for
// tokens nobody ever presents again (an abandoned browser tab, a revoked
// cookie the client dropped).
func (s *SessionStore) sweepLoop() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			for token, e := range s.entries {
				if now.After(e.expiresAt) {
					delete(s.entries, token)
				}
			}
			s.mu.Unlock()
		}
	}
}

// randomToken returns a 256-bit random token, URL/cookie-safe.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read only fails if the OS entropy source is broken,
		// which is not a condition any caller could recover from.
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
