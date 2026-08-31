package config

import (
	"bytes"
	"errors"
	"flag"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// capture redirects the flag set's usage/error output into a buffer, so tests
// can assert on it and a test run stays readable.
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := usageOut
	usageOut = &buf
	t.Cleanup(func() { usageOut = old })
	return &buf
}

// DataDir ends up in a shell's environment as HISTFILE, and that shell runs in
// the session's directory rather than the server's. A relative --data-dir that
// stayed relative would send every session's history somewhere unwritable.
func TestDataDirIsAbsolute(t *testing.T) {
	base := t.TempDir()

	for _, args := range [][]string{
		{"--data-dir", filepath.Join(base, "state")},
		{"--data-dir", filepath.Join(base, "x", "..", "state2")}, // traversal
	} {
		cfg, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%v): %v", args, err)
		}
		if !filepath.IsAbs(cfg.DataDir) {
			t.Errorf("Parse(%v).DataDir = %q, want an absolute path", args, cfg.DataDir)
		}
		if !filepath.IsAbs(cfg.DB) {
			t.Errorf("Parse(%v).DB = %q, want an absolute path", args, cfg.DB)
		}
	}
}

// Without --data-dir, the default is the relative "./data" — resolved against
// the server's working directory, and created if it doesn't exist yet, so a
// zero-flag start (Docker's default CMD, a fresh `go run`) always has
// somewhere to put config.yml, users.yml and everything else in §8.
func TestDataDirDefaultsToDataUnderCwd(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	if base := filepath.Base(cfg.DataDir); base != "data" {
		t.Errorf("DataDir = %q, want it to end in \"data\"", cfg.DataDir)
	}
	if !filepath.IsAbs(cfg.DataDir) {
		t.Errorf("DataDir = %q, want an absolute path", cfg.DataDir)
	}
}

// The database is not separately addressable any more: it is one of the things
// inside the state directory, and every other one is found relative to that
// same directory.
func TestDatabaseLivesInTheDataDir(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Parse([]string{"--data-dir", dir})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.DataDir != dir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, dir)
	}
	if want := filepath.Join(dir, "sessions.db"); cfg.DB != want {
		t.Errorf("DB = %q, want %q", cfg.DB, want)
	}
}

// --db named a file and then claimed the directory around it. Answering it with
// the flag package's "flag provided but not defined" would say it is gone
// without saying what replaced it, and the replacement takes a different kind
// of value — a directory, not a file — so a silent rename would be wrong too.
func TestRemovedDBFlagExplainsItself(t *testing.T) {
	for _, args := range [][]string{
		{"--db", "/tmp/x.db"},
		{"--db=/tmp/x.db"},
		{"-db", "/tmp/x.db"},
	} {
		_, err := Parse(args)
		if err == nil {
			t.Fatalf("Parse(%v) succeeded, want an error", args)
		}
		if !strings.Contains(err.Error(), "--data-dir") {
			t.Errorf("Parse(%v) error = %q, want it to name --data-dir", args, err)
		}
	}

	// A session directory that happens to be called "db" is not the flag.
	if _, err := Parse([]string{"--data-dir", t.TempDir(), "--shells", "bash", "--", "db"}); err != nil {
		t.Errorf("Parse with a positional \"db\": %v", err)
	}
}

// --root named the local-host sandbox directory, which is now a fixed
// <data-dir>/workspace gated by config.yml's allowLocalHost, not an operator
// flag. The error must say so rather than just "flag provided but not
// defined".
func TestRemovedRootFlagExplainsItself(t *testing.T) {
	for _, args := range [][]string{
		{"--root", "/tmp/x"},
		{"--root=/tmp/x"},
		{"-root", "/tmp/x"},
	} {
		_, err := Parse(args)
		if err == nil {
			t.Fatalf("Parse(%v) succeeded, want an error", args)
		}
		if !strings.Contains(err.Error(), "workspace") {
			t.Errorf("Parse(%v) error = %q, want it to explain the workspace replacement", args, err)
		}
	}
}

// Retention deletes sessions that can now be restarted with their scrollback
// and history, so the off switch has to be the default and a typo has to be an
// error rather than a silently different window.
func TestSessionRetention(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    time.Duration
		wantErr bool
	}{
		{name: "default is off", args: nil, want: 0},
		{name: "explicit zero", args: []string{"--session-retention", "0"}, want: 0},
		{name: "empty is off", args: []string{"--session-retention", ""}, want: 0},
		{name: "hours", args: []string{"--session-retention", "720h"}, want: 720 * time.Hour},
		{name: "minutes", args: []string{"--session-retention", "90m"}, want: 90 * time.Minute},
		// Go durations have no day unit; "30d" is a plausible typo that must not
		// be read as something else.
		{name: "days are not a unit", args: []string{"--session-retention", "30d"}, wantErr: true},
		{name: "negative", args: []string{"--session-retention", "-1h"}, wantErr: true},
		{name: "garbage", args: []string{"--session-retention", "soon"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture(t)
			cfg, err := Parse(append([]string{"--data-dir", t.TempDir()}, tc.args...))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%v) succeeded, want an error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%v): %v", tc.args, err)
			}
			if cfg.SessionRetention != tc.want {
				t.Errorf("SessionRetention = %v, want %v", cfg.SessionRetention, tc.want)
			}
		})
	}
}

// --version and --help are intent, not failure. Both must be distinguishable
// from a real error so main can exit 0 instead of printing "fatal:".
func TestParseIntentFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want error
	}{
		{name: "version long", args: []string{"--version"}, want: ErrVersionRequested},
		{name: "version short", args: []string{"-version"}, want: ErrVersionRequested},
		{name: "help long", args: []string{"--help"}, want: flag.ErrHelp},
		{name: "help short", args: []string{"-h"}, want: flag.ErrHelp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture(t)
			cfg, err := Parse(tc.args)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Parse(%v) error = %v, want %v", tc.args, err, tc.want)
			}
			if cfg != nil {
				t.Errorf("Parse(%v) returned a config; want nil", tc.args)
			}
		})
	}
}

// --version must work with no other flags at all: asking a binary its version
// should never depend on any other configuration being present.
func TestVersionNeedsNoOtherFlags(t *testing.T) {
	capture(t)
	if _, err := Parse([]string{"--version"}); !errors.Is(err, ErrVersionRequested) {
		t.Fatalf("Parse(--version) error = %v, want ErrVersionRequested", err)
	}
}

// --help must actually document the flags, not just exit quietly.
func TestHelpListsFlags(t *testing.T) {
	buf := capture(t)
	if _, err := Parse([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(--help) error = %v, want flag.ErrHelp", err)
	}
	usage := buf.String()
	for _, want := range []string{"-addr", "-data-dir", "-shells", "-version", "sessile"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage text missing %q; got:\n%s", want, usage)
		}
	}
}
