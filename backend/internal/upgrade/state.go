package upgrade

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// LayoutVersion is the version of the data directory's file layout that this
// build reads and writes: which files exist under --data-dir, where, and in
// what format. It is 1 for the layout §8 describes. A change that moves or
// reshapes a file — not one that only adds a field a YAML reader ignores —
// bumps it, together with the code that brings an older directory forward.
const LayoutVersion = 1

// stateFile sits beside config.yml and users.yml. The leading dot keeps it
// out of the way of someone editing those by hand; it is not secret.
const stateFile = ".sessile-state.yml"

// State is the contents of <data-dir>/.sessile-state.yml.
type State struct {
	// LastVersion is the app version (config.Version) that last started on
	// this directory. Informational: release, dev and git-describe versions
	// do not order reliably, so nothing is refused on it.
	LastVersion string `yaml:"lastVersion"`
	// LayoutVersion is the LayoutVersion of that build. This one is a guard.
	LayoutVersion int `yaml:"layoutVersion"`
}

// LayoutTooNewError means a newer release has already changed the data
// directory's layout. It is the downgrade guard for everything that is not
// sessions.db — and the only one for a directory that has no database yet.
type LayoutTooNewError struct {
	Path  string
	Found int
	Known int
	// By is the LastVersion that wrote it, for the message.
	By string
}

func (e *LayoutTooNewError) Error() string {
	return fmt.Sprintf("%s has layout version %d (written by sessile %s), but this release only knows up to %d",
		e.Path, e.Found, e.By, e.Known)
}

// StatePath is the state file's path under dataDir.
func StatePath(dataDir string) string {
	return filepath.Join(dataDir, stateFile)
}

// ReadState reads the state file. found is false, with a zero State and no
// error, when there is none — any directory from before §12e M32, or a new
// one.
func ReadState(dataDir string) (s State, found bool, err error) {
	data, err := os.ReadFile(StatePath(dataDir))
	if os.IsNotExist(err) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, fmt.Errorf("read %s: %w", stateFile, err)
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return State{}, false, fmt.Errorf("parse %s: %w", stateFile, err)
	}
	return s, true, nil
}

// CheckLayout refuses a state written by a build with a newer layout.
func CheckLayout(dataDir string, s State) error {
	if s.LayoutVersion > LayoutVersion {
		return &LayoutTooNewError{Path: StatePath(dataDir), Found: s.LayoutVersion, Known: LayoutVersion, By: s.LastVersion}
	}
	return nil
}

// WriteState atomically replaces the state file: a temp file in the same
// directory, then a rename, like every other file in the data directory.
func WriteState(dataDir string, s State) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", stateFile, err)
	}
	data = append([]byte("# Written by sessile on every start. Do not edit: it is how a release\n"+
		"# recognises a data directory that a newer one has already upgraded.\n"), data...)

	tmp, err := os.CreateTemp(dataDir, ".sessile-state-*.yml.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmpName, StatePath(dataDir)); err != nil {
		return fmt.Errorf("replace %s: %w", stateFile, err)
	}
	return nil
}
