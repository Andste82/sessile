package upgrade

import (
	"os"
	"path/filepath"
)

// stateFiles are the files a data directory has once any release has
// started on it. The directory itself proves nothing: config.Parse creates
// it, and <data-dir>/workspace inside it, before anything else runs.
var stateFiles = []string{"sessions.db", "users.yml", "config.yml", stateFile}

// LooksFresh reports whether dataDir holds none of the files any release
// leaves behind (§12e M33). On a first install that is expected. After an
// upgrade it almost always means the volume is not mounted where it used to
// be — the 0.3 → 0.5 compose change did exactly that — and the server would
// otherwise carry on as a brand-new install, asking for a first admin, while
// the real data sits somewhere else. It must be called before anything
// creates those files.
func LooksFresh(dataDir string) bool {
	for _, name := range stateFiles {
		if _, err := os.Lstat(filepath.Join(dataDir, name)); err == nil {
			return false
		}
	}
	return true
}
