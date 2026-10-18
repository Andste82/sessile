package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLooksFresh(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		dirs  []string
		fresh bool
	}{
		{"empty", nil, nil, true},
		// config.Parse creates <data-dir>/workspace before anything else
		// runs, and an empty mount may carry lost+found: neither is state.
		{"only directories", nil, []string{"workspace", "lost+found"}, true},
		{"v0.3: only the database", []string{"sessions.db"}, nil, false},
		{"users.yml", []string{"users.yml"}, nil, false},
		{"config.yml", []string{"config.yml"}, nil, false},
		{"state file", []string{stateFile}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, d := range tc.dirs {
				if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := LooksFresh(dir); got != tc.fresh {
				t.Errorf("LooksFresh = %v, want %v", got, tc.fresh)
			}
		})
	}
}
