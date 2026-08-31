// Package auth holds the multi-user account model: a hand-editable
// users.yml store (PROJECT_PLAN.md §8) with bcrypt-hashed passwords, and an
// in-memory, sliding-TTL web session token store (sessions.go). Neither
// depends on Gin — the HTTP wiring (cookies, middleware) lives in
// internal/api, matching how internal/session stays free of internal/ws.
package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// User is one account in users.yml. PasswordHash is a bcrypt hash — the
// plaintext password is never stored or logged.
type User struct {
	ID           string `yaml:"id"`
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"passwordHash"`
	IsAdmin      bool   `yaml:"isAdmin"`
}

// UserStore guards users.yml: an in-memory cache kept in sync with an
// atomically-rewritten file on every mutation, so a concurrent read never
// observes a half-written file and a hand-edit between restarts is picked up
// on the next Open.
type UserStore struct {
	path string

	mu    sync.Mutex
	users []User
}

// OpenUsers loads users.yml at path, creating an empty 0600 file if it
// doesn't exist yet. An empty store is the server's "unlocked" first-run
// state (§10, §11): Count() == 0 until the first bootstrap.
func OpenUsers(path string) (*UserStore, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s := &UserStore{path: path}
		if err := s.persist(nil); err != nil {
			return nil, fmt.Errorf("create users.yml: %w", err)
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read users.yml: %w", err)
	}

	var users []User
	if err := yaml.Unmarshal(data, &users); err != nil {
		return nil, fmt.Errorf("parse users.yml: %w", err)
	}
	return &UserStore{path: path, users: users}, nil
}

// Count returns the number of accounts.
func (s *UserStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.users)
}

// FindByUsername looks up a user case-insensitively, matching Create's
// uniqueness check.
func (s *UserStore) FindByUsername(username string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if strings.EqualFold(u.Username, username) {
			return u, true
		}
	}
	return User{}, false
}

// FindByID looks up a user by id.
func (s *UserStore) FindByID(id string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.ID == id {
			return u, true
		}
	}
	return User{}, false
}

// List returns every account, in no particular order.
func (s *UserStore) List() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, len(s.users))
	copy(out, s.users)
	return out
}

// Create adds a new account with a bcrypt hash of password. Username
// uniqueness is case-insensitive: "Admin" and "admin" would otherwise be two
// accounts that look identical everywhere they're displayed.
func (s *UserStore) Create(username, password string, isAdmin bool) (User, error) {
	username = strings.TrimSpace(username)
	if l := len(username); l < 1 || l > 64 {
		return User{}, fmt.Errorf("username must be 1-64 characters")
	}
	if len(password) < 8 {
		return User{}, fmt.Errorf("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range s.users {
		if strings.EqualFold(u.Username, username) {
			return User{}, ErrUserExists
		}
	}

	user := User{ID: uuid.NewString(), Username: username, PasswordHash: string(hash), IsAdmin: isAdmin}
	next := append(append([]User{}, s.users...), user)
	if err := s.persist(next); err != nil {
		return User{}, err
	}
	s.users = next
	return user, nil
}

// Verify checks username/password against the stored hash. Both a missing
// username and a wrong password return the same ErrInvalidCredentials, so a
// caller can't use the error to enumerate valid usernames.
func (s *UserStore) Verify(username, password string) (User, error) {
	user, ok := s.FindByUsername(username)
	if !ok {
		// Still run bcrypt against a fixed hash even when the username is
		// unknown, so the response time doesn't itself reveal that.
		_ = bcrypt.CompareHashAndPassword([]byte(unknownUserHash), []byte(password))
		return User{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return User{}, ErrInvalidCredentials
	}
	return user, nil
}

// unknownUserHash is a real bcrypt hash of a password nobody will type —
// computed once at startup so Verify has a valid, constant-cost comparison
// to run when the username itself doesn't exist.
var unknownUserHash = func() string {
	h, err := bcrypt.GenerateFromPassword([]byte("sessile-unknown-user-placeholder"), bcrypt.DefaultCost)
	if err != nil {
		// bcrypt.GenerateFromPassword only fails on a cost out of range,
		// which DefaultCost never is.
		panic(fmt.Errorf("compute placeholder hash: %w", err))
	}
	return string(h)
}()

// Delete removes an account. Refuses to remove the last admin (§12b M11) —
// the server would then have no way to reach its own admin panel.
func (s *UserStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, u := range s.users {
		if u.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrUserNotFound
	}
	if s.users[idx].IsAdmin && s.countAdminsLocked() <= 1 {
		return ErrLastAdmin
	}

	next := append(append([]User{}, s.users[:idx]...), s.users[idx+1:]...)
	if err := s.persist(next); err != nil {
		return err
	}
	s.users = next
	return nil
}

// SetAdmin promotes or demotes an account. Refuses to demote the last admin,
// same guard as Delete.
func (s *UserStore) SetAdmin(id string, isAdmin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, u := range s.users {
		if u.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return ErrUserNotFound
	}
	if s.users[idx].IsAdmin && !isAdmin && s.countAdminsLocked() <= 1 {
		return ErrLastAdmin
	}

	next := append([]User{}, s.users...)
	next[idx].IsAdmin = isAdmin
	if err := s.persist(next); err != nil {
		return err
	}
	s.users = next
	return nil
}

// countAdminsLocked counts admin accounts in the current in-memory list.
// Callers must hold s.mu.
func (s *UserStore) countAdminsLocked() int {
	n := 0
	for _, u := range s.users {
		if u.IsAdmin {
			n++
		}
	}
	return n
}

// persist atomically rewrites users.yml to hold users: a temp file in the
// same directory, then a rename, so a crash or a concurrent read never
// observes a half-written file. Callers must hold s.mu.
func (s *UserStore) persist(users []User) error {
	data, err := yaml.Marshal(users)
	if err != nil {
		return fmt.Errorf("marshal users.yml: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".users-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create temp users file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp users file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp users file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp users file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace users.yml: %w", err)
	}
	return nil
}
