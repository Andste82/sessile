// Package serverconfig holds the hand-editable server-wide settings that
// live in <data-dir>/config.yml: the display name shown on the login page,
// and the two admin-controlled toggles that gate registration and local-host
// sessions (PROJECT_PLAN.md §9). Unlike CLI flags, these are read at startup
// and can also be changed at runtime by an admin (§6's /api/admin/config),
// so Store keeps the current value in memory behind a mutex rather than
// re-reading the file on every Get.
package serverconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config is the full contents of config.yml, and also what GET/PUT
// /api/admin/config (§6) serves and accepts directly — the json tags matter
// as much as the yaml ones. Both bool fields default to false: an operator
// opts into wider access rather than discovering it was on.
type Config struct {
	DisplayName       string `yaml:"displayName" json:"displayName"`
	AllowRegistration bool   `yaml:"allowRegistration" json:"allowRegistration"`
	AllowLocalHost    bool   `yaml:"allowLocalHost" json:"allowLocalHost"`
}

// Store guards the current Config and persists changes to path.
type Store struct {
	path string

	mu  sync.RWMutex
	cfg Config
}

// Open loads config.yml at path, creating it with hand-editable defaults if
// it does not exist yet — so an operator who wants to change a setting
// before first start has a file to edit, rather than needing to guess its
// shape.
func Open(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		s := &Store{path: path}
		if err := s.Set(Config{}); err != nil {
			return nil, fmt.Errorf("write default config.yml: %w", err)
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config.yml: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config.yml: %w", err)
	}
	return &Store{path: path, cfg: cfg}, nil
}

// Get returns the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Set validates and persists c, atomically, then makes it current. The
// temp-file-then-rename write is the same pattern every other hand-editable
// store in this project uses (auth.UserStore, hosts.Store) — a crash or
// concurrent read never observes a half-written file.
func (s *Store) Set(c Config) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config.yml: %w", err)
	}

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace config.yml: %w", err)
	}

	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	return nil
}
