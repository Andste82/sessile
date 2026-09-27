package agents

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// Connection is one credential for one agent: a kind (§4.13) and its field
// values. Secret fields are stored alongside the others, plaintext — the API
// layer is what masks them.
type Connection struct {
	ID      string            `yaml:"id"`
	Name    string            `yaml:"name"`
	Kind    string            `yaml:"kind"`
	Fields  map[string]string `yaml:"fields"`
	Expires *time.Time        `yaml:"expires,omitempty"`
}

// Profile is what a task picks: an agent, the connection it authenticates
// with, and optionally a model (§4.12.7). Agent is stored for a readable
// hand-edited file, and always equals the connection kind's agent.
type Profile struct {
	ID           string `yaml:"id"`
	Name         string `yaml:"name"`
	Agent        Agent  `yaml:"agent"`
	ConnectionID string `yaml:"connectionId"`
	Model        string `yaml:"model,omitempty"`
}

// TaskDefaults pre-select the task form's host and profile.
type TaskDefaults struct {
	HostID    string `yaml:"hostId,omitempty"`
	ProfileID string `yaml:"profileId,omitempty"`
}

// GitAccount is one git identity for one git host (§4.16).
type GitAccount struct {
	ID       string `yaml:"id"`
	Host     string `yaml:"host"`
	Name     string `yaml:"name"`
	Email    string `yaml:"email"`
	Username string `yaml:"username"`
	Token    string `yaml:"token,omitempty"`
}

// Settings is the whole of agent.yml.
type Settings struct {
	Connections  []Connection `yaml:"connections"`
	Profiles     []Profile    `yaml:"profiles"`
	TaskDefaults TaskDefaults `yaml:"taskDefaults"`
	Git          []GitAccount `yaml:"git"`
}

// ErrNotFound is returned for an id that isn't in this user's settings —
// which, since a Store is always opened for one user, also covers another
// user's id (§14.5).
var ErrNotFound = errors.New("not found")

// ValidationError is a user-facing validation failure.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// modelRe is the model-id rule of §4.12.7: a model id may reach an argv on
// the codex path, so it is held to a strict character set.
var modelRe = regexp.MustCompile(`^[A-Za-z0-9._:/@\[\]-]{1,128}$`)

// ValidModel reports whether m is empty (the CLI's default) or a valid id.
func ValidModel(m string) bool { return m == "" || modelRe.MatchString(m) }

// gitHostRe accepts a bare host name with an optional port.
var gitHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252})(:[0-9]{1,5})?$`)

// Validate checks the whole document, including references between its
// parts. hostExists resolves TaskDefaults.HostID against the user's hosts.
func (s Settings) Validate(hostExists func(string) bool) error {
	conns := map[string]Connection{}
	for _, c := range s.Connections {
		if c.ID == "" {
			return invalid("connection without an id")
		}
		if _, dup := conns[c.ID]; dup {
			return invalid("duplicate connection id %q", c.ID)
		}
		conns[c.ID] = c
		if l := len(strings.TrimSpace(c.Name)); l < 1 || l > 64 {
			return invalid("connection name must be 1-64 characters")
		}
		k, ok := KindByID(c.Kind)
		if !ok {
			return invalid("connection %q: unknown kind %q", c.Name, c.Kind)
		}
		for name := range c.Fields {
			if _, ok := k.field(name); !ok {
				return invalid("connection %q: unknown field %q for %s", c.Name, name, k.ID)
			}
		}
		for _, f := range k.Fields {
			v := c.Fields[f.Name]
			if f.Required && strings.TrimSpace(v) == "" {
				return invalid("connection %q: %s is required", c.Name, f.Label)
			}
			if strings.ContainsAny(v, "\r\n\x00") {
				return invalid("connection %q: %s must be a single line", c.Name, f.Label)
			}
			if f.Name == k.ModelField && !ValidModel(v) {
				return invalid("connection %q: invalid model id", c.Name)
			}
		}
	}

	profiles := map[string]bool{}
	for _, p := range s.Profiles {
		if p.ID == "" {
			return invalid("profile without an id")
		}
		if profiles[p.ID] {
			return invalid("duplicate profile id %q", p.ID)
		}
		profiles[p.ID] = true
		if l := len(strings.TrimSpace(p.Name)); l < 1 || l > 64 {
			return invalid("profile name must be 1-64 characters")
		}
		c, ok := conns[p.ConnectionID]
		if !ok {
			return invalid("profile %q: connection not found", p.Name)
		}
		k, _ := KindByID(c.Kind)
		if p.Agent != k.Agent {
			return invalid("profile %q: agent %q doesn't match its connection (%s)", p.Name, p.Agent, k.Agent)
		}
		if !ValidModel(p.Model) {
			return invalid("profile %q: invalid model id", p.Name)
		}
	}

	if id := s.TaskDefaults.ProfileID; id != "" && !profiles[id] {
		return invalid("default profile not found")
	}
	if id := s.TaskDefaults.HostID; id != "" && id != "local" && hostExists != nil && !hostExists(id) {
		return invalid("default host not found")
	}

	gitIDs := map[string]bool{}
	gitHosts := map[string]bool{}
	for _, g := range s.Git {
		if g.ID == "" {
			return invalid("git account without an id")
		}
		if gitIDs[g.ID] {
			return invalid("duplicate git account id %q", g.ID)
		}
		gitIDs[g.ID] = true
		host := strings.ToLower(g.Host)
		if !gitHostRe.MatchString(host) {
			return invalid("git account: %q is not a host name (e.g. github.com)", g.Host)
		}
		if gitHosts[host] {
			return invalid("git account: only one account per host (%s)", host)
		}
		gitHosts[host] = true
		for label, v := range map[string]string{"name": g.Name, "email": g.Email, "username": g.Username, "token": g.Token} {
			if strings.ContainsAny(v, "\r\n\x00") {
				return invalid("git account %s: %s must be a single line", host, label)
			}
		}
		if strings.TrimSpace(g.Username) == "" || strings.TrimSpace(g.Token) == "" {
			return invalid("git account %s: username and token are required", host)
		}
	}
	return nil
}

// Connection looks a connection up by id.
func (s Settings) Connection(id string) (Connection, bool) {
	for _, c := range s.Connections {
		if c.ID == id {
			return c, true
		}
	}
	return Connection{}, false
}

// Profile looks a profile up by id.
func (s Settings) Profile(id string) (Profile, bool) {
	for _, p := range s.Profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// GitFor returns the Git account for a repo URL's host, if the user has one.
// ssh:// and scp-style (git@host:path) URLs return none: those keep using the
// target's SSH keys (§4.16).
func (s Settings) GitFor(repoURL string) (GitAccount, bool) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" {
		return GitAccount{}, false
	}
	host := strings.ToLower(u.Host)
	for _, g := range s.Git {
		if strings.ToLower(g.Host) == host {
			return g, true
		}
	}
	return GitAccount{}, false
}

// Expired reports whether c has an expiry that has passed.
func (c Connection) Expired(now time.Time) bool {
	return c.Expires != nil && !now.Before(*c.Expires)
}

// ExpiresSoon reports whether c expires within 14 days (§4.13).
func (c Connection) ExpiresSoon(now time.Time) bool {
	return c.Expires != nil && !c.Expired(now) && c.Expires.Sub(now) < 14*24*time.Hour
}

// Env is the environment a task session gets for c: the kind's fixed vars
// plus every non-empty field that maps to one. Sorted by name, so a
// rendered .env is deterministic.
func (c Connection) Env() [][2]string {
	k, ok := KindByID(c.Kind)
	if !ok {
		return nil
	}
	m := map[string]string{}
	for name, v := range k.FixedEnv {
		m[name] = v
	}
	for _, f := range k.Fields {
		if f.Env != "" && c.Fields[f.Name] != "" {
			m[f.Env] = c.Fields[f.Name]
		}
	}
	out := make([][2]string, 0, len(m))
	for name, v := range m {
		out = append(out, [2]string{name, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

// NewID returns a fresh id for a connection, profile or git account.
func NewID() string { return uuid.NewString() }

// Store guards one user's agent.yml, mirroring hosts.Store: an in-memory
// copy kept in sync with an atomically rewritten 0600 file.
type Store struct {
	path string

	mu       sync.Mutex
	settings Settings
}

// Open loads agent.yml at path. A missing file is an empty document; it is
// only written on the first change, so opening settings never creates
// clutter in a user's directory.
func Open(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Store{path: path}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent.yml: %w", err)
	}
	var s Settings
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse agent.yml: %w", err)
	}
	return &Store{path: path, settings: s}, nil
}

// Get returns a deep copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.settings)
}

// Update applies fn to a copy of the settings, validates the result and
// persists it. fn's error, or a validation error, leaves everything as it was.
func (s *Store) Update(fn func(*Settings) error, hostExists func(string) bool) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.settings)
	if err := fn(&next); err != nil {
		return Settings{}, err
	}
	if err := next.Validate(hostExists); err != nil {
		return Settings{}, err
	}
	if err := s.persist(next); err != nil {
		return Settings{}, err
	}
	s.settings = next
	return clone(next), nil
}

func clone(s Settings) Settings {
	out := Settings{TaskDefaults: s.TaskDefaults}
	for _, c := range s.Connections {
		cc := c
		cc.Fields = make(map[string]string, len(c.Fields))
		for k, v := range c.Fields {
			cc.Fields[k] = v
		}
		if c.Expires != nil {
			t := *c.Expires
			cc.Expires = &t
		}
		out.Connections = append(out.Connections, cc)
	}
	out.Profiles = append(out.Profiles, s.Profiles...)
	out.Git = append(out.Git, s.Git...)
	return out
}

func (s *Store) persist(settings Settings) error {
	data, err := yaml.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal agent.yml: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".agent-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create temp agent file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp agent file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp agent file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp agent file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace agent.yml: %w", err)
	}
	return nil
}

// Registry caches one Store per user id, like hosts.Registry. It is the only
// place a user id becomes a path, and that id is always the authenticated
// caller's own.
type Registry struct {
	dataDir string

	mu     sync.Mutex
	stores map[string]*Store
}

// NewRegistry constructs a Registry rooted at <dataDir>/users.
func NewRegistry(dataDir string) *Registry {
	return &Registry{dataDir: dataDir, stores: make(map[string]*Store)}
}

// Dir is a user's agent directory, <data-dir>/users/<id>/agent.
func (r *Registry) Dir(userID string) string {
	return filepath.Join(r.dataDir, "users", userID, "agent")
}

// For returns userID's Store, opening and caching it on first use.
func (r *Registry) For(userID string) (*Store, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.stores[userID]; ok {
		return s, nil
	}
	s, err := Open(filepath.Join(r.Dir(userID), "agent.yml"))
	if err != nil {
		return nil, fmt.Errorf("open agent.yml for user %s: %w", userID, err)
	}
	r.stores[userID] = s
	return s, nil
}

// Evict drops a cached Store. hosts.Registry.Remove deletes the whole user
// directory when an account is deleted; this makes sure no stale copy of
// that user's settings outlives it in memory.
func (r *Registry) Evict(userID string) {
	r.mu.Lock()
	delete(r.stores, userID)
	r.mu.Unlock()
}
