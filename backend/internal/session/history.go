package session

import (
	"fmt"
	"os"
	"path/filepath"
)

// historyLines is how many commands each session's shell keeps. It matches the
// xterm.js scrollback setting (§7) closely enough that the two feel like one
// history to the user.
const historyLines = "5000"

// historyEnv returns the environment assignments that give the session its own
// command history, so arrow-up in a restarted session replays the commands typed
// in that session rather than the operator's global shell history (§8).
//
// It must be called with the same id across restarts — that is what makes the
// history survive. The returned slice is empty for an unrecognised shell, which
// simply means that shell keeps its default history behaviour.
//
// The shells were verified to flush on SIGHUP, which is what the kernel delivers
// when the backend dies and the PTY master closes:
//
//   - bash and zsh write their history from the exit path SIGHUP runs. bash also
//     gets PROMPT_COMMAND=history -a so a shell that is SIGKILLed outright still
//     has everything up to the last prompt on disk.
//   - fish appends after every command on its own; nothing extra is needed.
//
// Caveat, documented in the README: a user rc file that assigns HISTFILE itself
// (oh-my-zsh does) overrides this, and that session falls back to the shared
// history file.
func historyEnv(dataDir, shell, id string) ([]string, error) {
	switch shell {
	case "bash", "zsh":
		path, err := historyFile(dataDir, id)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create history dir: %w", err)
		}
		if shell == "bash" {
			return []string{
				"HISTFILE=" + path,
				"HISTSIZE=" + historyLines,
				"HISTFILESIZE=" + historyLines,
				"PROMPT_COMMAND=history -a",
			}, nil
		}
		return []string{
			"HISTFILE=" + path,
			"HISTSIZE=" + historyLines,
			"SAVEHIST=" + historyLines,
		}, nil

	case "fish":
		// fish has no variable for the history *path* — only fish_history, which
		// names a session whose file lands in fish's own data directory. Pointing
		// XDG_DATA_HOME at dataDir would relocate the file but also move every
		// other XDG-aware program's data along with it, so the session name is
		// the narrower tool and the one used here.
		if !validID(id) {
			return nil, fmt.Errorf("invalid session id")
		}
		return []string{"fish_history=" + id}, nil
	}
	return nil, nil
}

// historyFile is the bash/zsh HISTFILE path for a session.
func historyFile(dataDir, id string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid session id")
	}
	return filepath.Join(dataDir, "history", id), nil
}

// deleteHistory removes a session's history so a deleted session leaves nothing
// behind. Both shell families are cleaned regardless of which one the session
// used: shells are cheap to check and a session's recorded shell is not proof of
// what it ran after an operator edited the row.
func deleteHistory(dataDir, id string) error {
	path, err := historyFile(dataDir, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove history: %w", err)
	}
	if fishPath := fishHistoryFile(id); fishPath != "" {
		if err := os.Remove(fishPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove fish history: %w", err)
		}
	}
	return nil
}

// fishHistoryFile resolves where fish stores the history for session id, or ""
// if the location cannot be determined. It mirrors fish's own lookup:
// $XDG_DATA_HOME/fish, falling back to ~/.local/share/fish.
func fishHistoryFile(id string) string {
	if !validID(id) {
		return ""
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "fish", id+"_history")
}
