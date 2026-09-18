package scripts

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Limits of a zip extension (§4.15.1).
const (
	MaxZip          = 5 << 20
	maxEntries      = 200
	maxUncompressed = 20 << 20
)

var (
	ErrNotFound = errors.New("script not found")
)

// ExistsError is returned by Install for a name that is already installed
// when the caller didn't ask to update it.
type ExistsError struct {
	Installed, Uploaded string // versions
}

func (e *ExistsError) Error() string {
	return fmt.Sprintf("a script with this name is installed (version %s; the upload is %s)", e.Installed, e.Uploaded)
}

// Store keeps every user's scripts and their settings. A user's scripts are
// only ever reached through their own id (§14.5).
type Store struct {
	dataDir string
	mu      sync.Mutex
}

// NewStore returns a Store rooted at <dataDir>/users.
func NewStore(dataDir string) *Store { return &Store{dataDir: dataDir} }

func (s *Store) scriptsDir(userID string) string {
	return filepath.Join(s.dataDir, "users", userID, "agent", "scripts")
}

func (s *Store) settingsPath(userID, name string) string {
	return filepath.Join(s.dataDir, "users", userID, "agent", "settings", name+".yml")
}

// Dir is an installed script's folder.
func (s *Store) Dir(userID, name string) string {
	return filepath.Join(s.scriptsDir(userID), name)
}

// Get returns an installed script's meta.
func (s *Store) Get(userID, name string) (Meta, error) {
	if !ValidName(name) {
		return Meta{}, ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(userID, name), "meta.json"))
	if os.IsNotExist(err) {
		return Meta{}, ErrNotFound
	}
	if err != nil {
		return Meta{}, err
	}
	m, err := ParseMeta(data)
	if err != nil {
		return Meta{}, err
	}
	if m.Name != name {
		return Meta{}, fmt.Errorf("meta.json names %q but the folder is %q", m.Name, name)
	}
	return m, nil
}

// List returns every installed script whose meta.json is valid, and the
// names of the ones that aren't (hand-edited into a broken state).
func (s *Store) List(userID string) ([]Meta, map[string]string) {
	entries, _ := os.ReadDir(s.scriptsDir(userID))
	var out []Meta
	broken := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		m, err := s.Get(userID, e.Name())
		if err != nil {
			broken[e.Name()] = err.Error()
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, broken
}

// Install validates a zip and installs it (§4.15.1). as, when set, installs
// it under another name. An existing script of the same name is replaced
// only with update, and keeps its settings. Nothing is written unless the
// whole zip passed validation, and the swap into place is a rename.
func (s *Store) Install(userID string, data []byte, as string, update bool) (Meta, error) {
	files, meta, err := readZip(data)
	if err != nil {
		return Meta{}, err
	}
	if as != "" {
		if !ValidName(as) {
			return Meta{}, fmt.Errorf("%q is not a script name", as)
		}
		meta.Name = as
		raw, err := rewriteName(files["meta.json"], as)
		if err != nil {
			return Meta{}, err
		}
		files["meta.json"] = raw
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	dest := s.Dir(userID, meta.Name)
	if existing, err := s.Get(userID, meta.Name); err == nil && !update {
		return Meta{}, &ExistsError{Installed: existing.Version, Uploaded: meta.Version}
	} else if _, statErr := os.Stat(dest); statErr == nil && !update && err != nil {
		return Meta{}, &ExistsError{Installed: "unknown", Uploaded: meta.Version}
	}

	root := s.scriptsDir(userID)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Meta{}, err
	}
	staging := filepath.Join(root, ".staging-"+randHex())
	if err := writeTree(staging, files); err != nil {
		os.RemoveAll(staging)
		return Meta{}, err
	}
	old := ""
	if _, err := os.Stat(dest); err == nil {
		old = filepath.Join(root, ".old-"+randHex())
		if err := os.Rename(dest, old); err != nil {
			os.RemoveAll(staging)
			return Meta{}, fmt.Errorf("move the old version aside: %w", err)
		}
	}
	if err := os.Rename(staging, dest); err != nil {
		if old != "" {
			_ = os.Rename(old, dest)
		}
		os.RemoveAll(staging)
		return Meta{}, fmt.Errorf("install: %w", err)
	}
	if old != "" {
		os.RemoveAll(old)
	}
	s.pruneSettings(userID, meta)
	return meta, nil
}

func randHex() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// readZip reads and validates a zip in memory. It returns the files by
// clean relative path (a single top-level folder stripped) and the meta.
func readZip(data []byte) (map[string][]byte, Meta, error) {
	if len(data) > MaxZip {
		return nil, Meta{}, fmt.Errorf("the zip is larger than %d MiB", MaxZip>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, Meta{}, fmt.Errorf("not a zip file: %w", err)
	}
	if len(zr.File) > maxEntries {
		return nil, Meta{}, fmt.Errorf("the zip has more than %d entries", maxEntries)
	}
	files := map[string][]byte{}
	var total int64
	for _, f := range zr.File {
		name := f.Name
		if strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
			return nil, Meta{}, fmt.Errorf("zip entry %q has an absolute or Windows-style path", name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return nil, Meta{}, fmt.Errorf("zip entry %q leaves the folder", name)
			}
		}
		mode := f.Mode()
		if mode&os.ModeSymlink != 0 {
			return nil, Meta{}, fmt.Errorf("zip entry %q is a symlink", name)
		}
		if mode.IsDir() || strings.HasSuffix(name, "/") {
			continue
		}
		if !mode.IsRegular() {
			return nil, Meta{}, fmt.Errorf("zip entry %q is not a regular file", name)
		}
		clean := path.Clean(name)
		if clean == "." || strings.HasPrefix(clean, "../") {
			return nil, Meta{}, fmt.Errorf("zip entry %q leaves the folder", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, Meta{}, fmt.Errorf("read %s: %w", name, err)
		}
		// The header's size can lie; count what actually comes out.
		b, err := io.ReadAll(io.LimitReader(rc, maxUncompressed-total+1))
		rc.Close()
		if err != nil {
			return nil, Meta{}, fmt.Errorf("read %s: %w", name, err)
		}
		total += int64(len(b))
		if total > maxUncompressed {
			return nil, Meta{}, fmt.Errorf("the zip unpacks to more than %d MiB", maxUncompressed>>20)
		}
		files[clean] = b
	}
	files = stripTopFolder(files)
	raw, ok := files["meta.json"]
	if !ok {
		return nil, Meta{}, errors.New("no meta.json at the top of the zip")
	}
	meta, err := ParseMeta(raw)
	if err != nil {
		return nil, Meta{}, err
	}
	if _, ok := files[meta.Entry]; !ok {
		return nil, Meta{}, fmt.Errorf("the zip has no %s (meta.json's entry)", meta.Entry)
	}
	return files, meta, nil
}

// stripTopFolder removes one leading folder every file shares, when
// meta.json isn't already at the top: zipping a folder usually nests it.
func stripTopFolder(files map[string][]byte) map[string][]byte {
	if _, ok := files["meta.json"]; ok {
		return files
	}
	prefix := ""
	for name := range files {
		first, _, found := strings.Cut(name, "/")
		if !found {
			return files
		}
		if prefix == "" {
			prefix = first
		} else if prefix != first {
			return files
		}
	}
	out := make(map[string][]byte, len(files))
	for name, b := range files {
		out[strings.TrimPrefix(name, prefix+"/")] = b
	}
	return out
}

func rewriteName(raw []byte, name string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["name"] = name
	return json.MarshalIndent(m, "", "  ")
}

func writeTree(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Remove deletes a script and its settings.
func (s *Store) Remove(userID, name string) error {
	if !ValidName(name) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.Dir(userID, name)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return ErrNotFound
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	_ = os.Remove(s.settingsPath(userID, name))
	return nil
}

// Export writes the installed script as a zip. The settings live elsewhere,
// so they can't be in it (§4.15.1).
func (s *Store) Export(userID, name string, w io.Writer) error {
	if _, err := s.Get(userID, name); err != nil {
		return err
	}
	root := s.Dir(userID, name)
	zw := zip.NewWriter(w)
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		f, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

// Settings are one user's values for one script (§4.15.3), in
// settings/<name>.yml. Git maps a secret setting to a Git account's token by
// git host, so one token is kept once.
type Settings struct {
	Values map[string]string `yaml:"values"`
	Git    map[string]string `yaml:"git,omitempty"`
}

// GetSettings reads a script's settings; a missing file is empty.
func (s *Store) GetSettings(userID, name string) (Settings, error) {
	st := Settings{Values: map[string]string{}, Git: map[string]string{}}
	data, err := os.ReadFile(s.settingsPath(userID, name))
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := yaml.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse settings: %w", err)
	}
	if st.Values == nil {
		st.Values = map[string]string{}
	}
	if st.Git == nil {
		st.Git = map[string]string{}
	}
	return st, nil
}

// PutSettings writes a script's settings, 0600, atomically.
func (s *Store) PutSettings(userID, name string, st Settings) error {
	data, err := yaml.Marshal(st)
	if err != nil {
		return err
	}
	p := s.settingsPath(userID, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".settings-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// pruneSettings drops values a new version no longer declares. Caller holds s.mu.
func (s *Store) pruneSettings(userID string, m Meta) {
	st, err := s.GetSettings(userID, m.Name)
	if err != nil {
		return
	}
	declared := map[string]bool{}
	for _, d := range m.Settings {
		declared[d.Name] = true
	}
	changed := false
	for k := range st.Values {
		if !declared[k] {
			delete(st.Values, k)
			changed = true
		}
	}
	for k := range st.Git {
		if !declared[k] {
			delete(st.Git, k)
			changed = true
		}
	}
	if changed {
		_ = s.PutSettings(userID, m.Name, st)
	}
}

// Missing lists the required settings that have no value.
func Missing(m Meta, st Settings) []string {
	var out []string
	for _, d := range m.Settings {
		if d.Required && st.Values[d.Name] == "" && st.Git[d.Name] == "" {
			out = append(out, d.Name)
		}
	}
	return out
}
