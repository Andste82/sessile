package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Each shell needs a different lever to point at a per-session history, and
// getting the variable names wrong fails silently — the session just keeps using
// the operator's global history. Pin them.
func TestHistoryEnv(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"

	tests := []struct {
		shell string
		want  []string
	}{
		{
			shell: "bash",
			want: []string{
				"HISTFILE=%HIST%",
				"HISTSIZE=5000",
				"HISTFILESIZE=5000",
				// Without this bash only writes on exit, so a SIGKILLed shell
				// would lose everything typed since it started.
				"PROMPT_COMMAND=history -a",
			},
		},
		{
			shell: "zsh",
			want: []string{
				"HISTFILE=%HIST%",
				"HISTSIZE=5000",
				// zsh writes nothing at all unless SAVEHIST is above zero.
				"SAVEHIST=5000",
			},
		},
		{
			// fish has no variable for the history path — only a session name,
			// which is the narrower tool than relocating XDG_DATA_HOME.
			shell: "fish",
			want:  []string{"fish_history=" + id},
		},
		{
			shell: "unknown-shell",
			want:  nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.shell, func(t *testing.T) {
			dir := t.TempDir()
			got, err := historyEnv(dir, tc.shell, id)
			if err != nil {
				t.Fatalf("historyEnv: %v", err)
			}

			histPath := filepath.Join(dir, "history", id)
			want := make([]string, len(tc.want))
			for i, kv := range tc.want {
				if kv == "HISTFILE=%HIST%" {
					kv = "HISTFILE=" + histPath
				}
				want[i] = kv
			}
			if !slices.Equal(got, want) {
				t.Errorf("historyEnv(%s) = %v, want %v", tc.shell, got, want)
			}

			// bash and zsh will not create the parent directory themselves; a
			// missing one means the history is silently dropped.
			if tc.shell == "bash" || tc.shell == "zsh" {
				if fi, err := os.Stat(filepath.Dir(histPath)); err != nil || !fi.IsDir() {
					t.Errorf("history dir not created: %v", err)
				}
			}
		})
	}
}

// Restarting is only useful because the same id maps to the same history file.
func TestHistoryEnvIsStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	const id = "abc-123"
	first, err := historyEnv(dir, "bash", id)
	if err != nil {
		t.Fatalf("historyEnv: %v", err)
	}
	second, err := historyEnv(dir, "bash", id)
	if err != nil {
		t.Fatalf("historyEnv: %v", err)
	}
	if !slices.Equal(first, second) {
		t.Errorf("history env changed between calls: %v then %v", first, second)
	}
}

func TestHistoryEnvRejectsUnsafeIDs(t *testing.T) {
	for _, id := range []string{"../../etc/cron.d/x", "a/b", "", "."} {
		for _, shell := range []string{"bash", "zsh", "fish"} {
			if _, err := historyEnv(t.TempDir(), shell, id); err == nil {
				t.Errorf("historyEnv(%s, %q) succeeded, want error", shell, id)
			}
		}
	}
}

// Delete is documented as removing a session permanently, which has to include
// the history a later restart would otherwise resurrect.
func TestDeleteHistory(t *testing.T) {
	dir := t.TempDir()
	const id = "44444444-5555-6666-7777-888888888888"

	if _, err := historyEnv(dir, "bash", id); err != nil {
		t.Fatalf("historyEnv: %v", err)
	}
	path := filepath.Join(dir, "history", id)
	if err := os.WriteFile(path, []byte("echo hi\n"), 0o600); err != nil {
		t.Fatalf("write history: %v", err)
	}

	if err := deleteHistory(dir, id); err != nil {
		t.Fatalf("deleteHistory: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("history file still present after delete (err=%v)", err)
	}
	// Deleting twice is not an error.
	if err := deleteHistory(dir, id); err != nil {
		t.Errorf("second deleteHistory: %v", err)
	}
}

// fish stores history in its own data directory, so the cleanup path has to
// mirror fish's lookup rather than our data dir.
func TestFishHistoryFileFollowsXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg")
	if got, want := fishHistoryFile("abc-123"), "/xdg/fish/abc-123_history"; got != want {
		t.Errorf("fishHistoryFile = %q, want %q", got, want)
	}

	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/op")
	if got, want := fishHistoryFile("abc-123"), "/home/op/.local/share/fish/abc-123_history"; got != want {
		t.Errorf("fishHistoryFile = %q, want %q", got, want)
	}

	if got := fishHistoryFile("../escape"); got != "" {
		t.Errorf("fishHistoryFile(unsafe id) = %q, want \"\"", got)
	}
}
