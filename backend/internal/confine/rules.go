package confine

import (
	"os"
	"os/exec"
	"path/filepath"
)

// AgentRules are what a task's agent may touch on the sessile server
// (§4.12.9, E13): its own folder, a temp dir inside it, the terminal, and
// the programs it needs to run — and nothing else. Not --data-dir, not
// another task's folder, not the operator's home.
//
// binary is the agent CLI as resolved on this machine; self is the sessile
// binary, which the agent execs for the MCP bridge.
func AgentRules(taskDir, binary, self string) Rules {
	r := Rules{
		ReadWrite: []string{taskDir, "/dev"},
		ReadOnly: []string{
			// The system: libraries, certificates, time zones. Read-only, so
			// nothing here can be changed.
			"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt", "/proc", "/sys/devices",
			// Where /etc/resolv.conf points on a systemd-resolved or
			// resolvconf machine. Landlock follows the link to the real file,
			// so without these the agent has no DNS — and an agent that
			// cannot resolve its vendor's API hangs rather than failing.
			"/run/systemd/resolve", "/run/resolvconf", "/run/NetworkManager",
		},
	}
	// The agent CLI and the sessile binary, by the directory each lives in —
	// enough to run them, not enough to see what else is in a home directory.
	for _, p := range []string{binary, self} {
		if p == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		r.ReadOnly = append(r.ReadOnly, filepath.Dir(p))
	}
	// A native CLI keeps its versions beside its launcher; a node one keeps
	// them under the same share directory. Both are program files.
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		r.ReadOnly = append(r.ReadOnly,
			filepath.Join(home, ".local", "share", "claude"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".nvm"),
		)
	}
	return r
}

// ScriptRules are what one script run may touch: its own folder and venv,
// and a temp dir. Scripts have always run on the server as sessile's user
// (§11); this is what stops one from reading another user's settings.
func ScriptRules(scriptDir, venvDir, tempDir string) Rules {
	rw := []string{scriptDir, "/dev"}
	for _, p := range []string{venvDir, tempDir} {
		if p != "" {
			rw = append(rw, p)
		}
	}
	return Rules{
		ReadWrite: rw,
		ReadOnly:  []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt", "/proc"},
	}
}

// LookPath is exec.LookPath, exposed so callers can resolve a binary the
// same way the rules do.
func LookPath(name string) (string, error) { return exec.LookPath(name) }
