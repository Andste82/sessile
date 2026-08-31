package hosts

import (
	"fmt"
	"path/filepath"
	"sync"
)

// Registry caches one open Store per user id, so a hosts.yml isn't
// re-parsed on every request. It is the only place a user id turns into a
// filesystem path — always the authenticated caller's own id (§4.5, §6),
// never one taken from a request body or query string.
type Registry struct {
	dataDir string

	mu     sync.Mutex
	stores map[string]*Store
}

// NewRegistry constructs a Registry rooted at <dataDir>/users.
func NewRegistry(dataDir string) *Registry {
	return &Registry{dataDir: dataDir, stores: make(map[string]*Store)}
}

// For returns the Store for userID, opening (and caching) it on first use.
func (r *Registry) For(userID string) (*Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if s, ok := r.stores[userID]; ok {
		return s, nil
	}
	path := filepath.Join(r.dataDir, "users", userID, "hosts.yml")
	s, err := Open(path)
	if err != nil {
		return nil, fmt.Errorf("open hosts.yml for user %s: %w", userID, err)
	}
	r.stores[userID] = s
	return s, nil
}
