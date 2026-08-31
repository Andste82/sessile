// Package hosts holds each user's SSH host definitions: a hand-editable
// hosts.yml store per user (PROJECT_PLAN.md §8, §9), one file at
// <data-dir>/users/<user-id>/hosts.yml. Credentials are stored inline,
// plaintext, by design — see CLAUDE.md's security posture section and
// PROJECT_PLAN.md §11 — not a bug to "fix" without discussion.
package hosts

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// AuthMethod selects which of Host's credential fields is used to connect.
type AuthMethod string

const (
	AuthPassword   AuthMethod = "password"
	AuthPrivateKey AuthMethod = "privateKey"
)

// TargetOS is informational — it steers UI defaults (terminal type choices)
// but is not otherwise interpreted by the SSH connection itself.
type TargetOS string

const (
	OSLinux   TargetOS = "linux"
	OSDarwin  TargetOS = "darwin"
	OSWindows TargetOS = "windows"
	OSOther   TargetOS = "other"
)

// Host is one SSH target, scoped to the user whose hosts.yml it lives in —
// never addressable across users (§4.5, §6).
type Host struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	Group    string `yaml:"group"`
	Address  string `yaml:"address"` // host[:port]; port defaults to 22
	Username string `yaml:"username"`

	AuthMethod AuthMethod `yaml:"authMethod"`
	// Password and PrivateKey are plaintext, inline, by design — see the
	// package doc comment. TODO(security): optional encryption-at-rest is
	// tracked but not built.
	Password             string `yaml:"password,omitempty"`
	PrivateKey           string `yaml:"privateKey,omitempty"`
	PrivateKeyPassphrase string `yaml:"privateKeyPassphrase,omitempty"`

	TargetOS      TargetOS `yaml:"targetOS"`
	TerminalType  string   `yaml:"terminalType"` // bash|zsh|fish|cmd|powershell|custom
	CustomCommand string   `yaml:"customCommand,omitempty"`

	// Host-key pin (§4.5.1). Empty TrustedHostKeyFingerprint means "not yet
	// trusted" — the first SSH attempt against this host must prompt, never
	// connect silently. Not a secret; safe to echo back to API clients.
	TrustedHostKeyType        string `yaml:"trustedHostKeyType,omitempty"`
	TrustedHostKeyFingerprint string `yaml:"trustedHostKeyFingerprint,omitempty"`

	Created time.Time `yaml:"created"`
}

// Store guards one user's hosts.yml: an in-memory cache kept in sync with an
// atomically-rewritten file on every mutation, mirroring auth.UserStore and
// serverconfig.Store.
type Store struct {
	path string

	mu    sync.Mutex
	hosts []Host
}

// Open loads hosts.yml at path, creating an empty 0600 file (and its parent
// directory — <data-dir>/users/<user-id>/) if it doesn't exist yet.
func Open(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s := &Store{path: path}
		if err := s.persist(nil); err != nil {
			return nil, fmt.Errorf("create hosts.yml: %w", err)
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read hosts.yml: %w", err)
	}

	var hostList []Host
	if err := yaml.Unmarshal(data, &hostList); err != nil {
		return nil, fmt.Errorf("parse hosts.yml: %w", err)
	}
	return &Store{path: path, hosts: hostList}, nil
}

// List returns every host, in no particular order.
func (s *Store) List() []Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Host, len(s.hosts))
	copy(out, s.hosts)
	return out
}

// Get looks up a host by id.
func (s *Store) Get(id string) (Host, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hosts {
		if h.ID == id {
			return h, true
		}
	}
	return Host{}, false
}

// Create adds a new host. The caller-supplied ID and Created fields, if any,
// are ignored — Create always stamps its own.
func (s *Store) Create(h Host) (Host, error) {
	h.ID = uuid.NewString()
	h.Created = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	next := append(append([]Host{}, s.hosts...), h)
	if err := s.persist(next); err != nil {
		return Host{}, err
	}
	s.hosts = next
	return h, nil
}

// Update replaces the host at id with h, preserving its original ID and
// Created timestamp regardless of what h carries.
func (s *Store) Update(id string, h Host) (Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, existing := range s.hosts {
		if existing.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return Host{}, ErrNotFound
	}

	h.ID = id
	h.Created = s.hosts[idx].Created
	next := append([]Host{}, s.hosts...)
	next[idx] = h
	if err := s.persist(next); err != nil {
		return Host{}, err
	}
	s.hosts = next
	return h, nil
}

// Delete removes a host. Does not touch any session that already
// snapshotted its display name (session.Info.HostDisplayName, §12b M17) —
// a later restart of such a session fails cleanly with "host not found"
// instead of cascading.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, h := range s.hosts {
		if h.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrNotFound
	}

	next := append(append([]Host{}, s.hosts[:idx]...), s.hosts[idx+1:]...)
	if err := s.persist(next); err != nil {
		return err
	}
	s.hosts = next
	return nil
}

// persist atomically rewrites hosts.yml to hold hostList: a temp file in the
// same directory, then a rename. Callers must hold s.mu.
func (s *Store) persist(hostList []Host) error {
	data, err := yaml.Marshal(hostList)
	if err != nil {
		return fmt.Errorf("marshal hosts.yml: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".hosts-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create temp hosts file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp hosts file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp hosts file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp hosts file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace hosts.yml: %w", err)
	}
	return nil
}
