package hosts

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/Andste82/sessile/backend/internal/envfile"
)

// envFile is the on-disk shape of users/<id>/env.yml.
type envFile struct {
	Env []envFileEntry `yaml:"env"`
}

// envFileEntry is one default variable. Value is plaintext, inline, by design
// — the same posture as the credentials in hosts.yml. TODO(security):
// optional encryption-at-rest is tracked but not built; a secret entry is
// only hidden from the API, not from the file.
type envFileEntry struct {
	Name   string `yaml:"name"`
	Value  string `yaml:"value"`
	Secret bool   `yaml:"secret"`
}

// EnvStore guards one user's env.yml: the environment variables every new
// session of that user starts with (issue #123). Like Store it keeps an
// in-memory cache in sync with an atomically rewritten file. Defaults are
// merged into a session at create time only; changing them later never
// touches an existing session.
type EnvStore struct {
	path string

	mu  sync.Mutex
	env []envfile.Entry
}

// openEnvStore loads env.yml at path. A missing file is an empty list and is
// not created until the first Save, so a user who never sets a default gets no
// extra file.
func openEnvStore(path string) (*EnvStore, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &EnvStore{path: path}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read env.yml: %w", err)
	}
	var f envFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		// yaml errors can quote the offending line, i.e. a value.
		return nil, fmt.Errorf("parse env.yml: invalid YAML")
	}
	entries := make([]envfile.Entry, 0, len(f.Env))
	for _, e := range f.Env {
		entries = append(entries, envfile.Entry{Name: e.Name, Value: e.Value, Secret: e.Secret})
	}
	// The file may be edited by hand. A bad list must fail here, by name and
	// with the file named as the cause, rather than surface later as a
	// confusing validation error on every session create.
	if err := envfile.Validate(entries); err != nil {
		return nil, fmt.Errorf("env.yml is invalid, fix or remove the file: %w", err)
	}
	return &EnvStore{path: path, env: entries}, nil
}

// Load returns a copy of the stored defaults, in file order.
func (s *EnvStore) Load() ([]envfile.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]envfile.Entry, len(s.env))
	copy(out, s.env)
	return out, nil
}

// Save validates entries and atomically replaces env.yml with them. Nothing
// is written for an invalid list. The Default flag has no meaning here and is
// not stored.
func (s *EnvStore) Save(entries []envfile.Entry) error {
	if err := envfile.Validate(entries); err != nil {
		return err
	}
	clean := make([]envfile.Entry, len(entries))
	f := envFile{Env: make([]envFileEntry, 0, len(entries))}
	for i, e := range entries {
		clean[i] = envfile.Entry{Name: e.Name, Value: e.Value, Secret: e.Secret}
		f.Env = append(f.Env, envFileEntry{Name: e.Name, Value: e.Value, Secret: e.Secret})
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal env.yml: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(data, clean)
}

func (s *EnvStore) writeLocked(data []byte, clean []envfile.Entry) error {
	if err := writeAtomic0600(s.path, data); err != nil {
		return err
	}
	s.env = clean
	return nil
}

// Update runs fn on a copy of the stored list and saves its result, holding
// the store lock across the whole read-modify-write so concurrent updates
// cannot lose each other's changes. fn's error aborts without writing. It
// returns the saved list.
func (s *EnvStore) Update(fn func(current []envfile.Entry) ([]envfile.Entry, error)) ([]envfile.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := make([]envfile.Entry, len(s.env))
	copy(cur, s.env)
	next, err := fn(cur)
	if err != nil {
		return nil, err
	}
	if err := envfile.Validate(next); err != nil {
		return nil, err
	}
	clean := make([]envfile.Entry, len(next))
	f := envFile{Env: make([]envFileEntry, 0, len(next))}
	for i, e := range next {
		clean[i] = envfile.Entry{Name: e.Name, Value: e.Value, Secret: e.Secret}
		f.Env = append(f.Env, envFileEntry{Name: e.Name, Value: e.Value, Secret: e.Secret})
	}
	data, err := yaml.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("marshal env.yml: %w", err)
	}
	if err := s.writeLocked(data, clean); err != nil {
		return nil, err
	}
	out := make([]envfile.Entry, len(clean))
	copy(out, clean)
	return out, nil
}

// writeAtomic0600 replaces path with data via a 0600 temp file in the same
// directory and a rename, the way hosts.yml is written.
func writeAtomic0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".env-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create temp env file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp env file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp env file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp env file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace env.yml: %w", err)
	}
	return nil
}
