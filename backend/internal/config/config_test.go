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
// the session's directory rather than the server's. A relative --db that stayed
// relative would send every session's history somewhere unwritable.
func TestDataDirIsAbsolute(t *testing.T) {
	root := t.TempDir()

	for _, args := range [][]string{
		{"--root", root}, // default db under root
		{"--root", root, "--db", "data/sessions.db"},     // relative --db
		{"--root", root, "--db", "./x/../data/state.db"}, // relative with traversal
	} {
		cfg, err := Parse(args)
		if err != nil {
			t.Fatalf("Parse(%v): %v", args, err)
		}
		if !filepath.IsAbs(cfg.DB) {
			t.Errorf("Parse(%v).DB = %q, want an absolute path", args, cfg.DB)
		}
		if !filepath.IsAbs(cfg.DataDir) {
			t.Errorf("Parse(%v).DataDir = %q, want an absolute path", args, cfg.DataDir)
		}
		if want := filepath.Dir(cfg.DB); cfg.DataDir != want {
			t.Errorf("Parse(%v).DataDir = %q, want %q", args, cfg.DataDir, want)
		}
	}
}

// Retention deletes sessions that can now be restarted with their scrollback
// and history, so the off switch has to be the default and a typo has to be an
// error rather than a silently different window.
func TestSessionRetention(t *testing.T) {
	root := t.TempDir()

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
			cfg, err := Parse(append([]string{"--root", root}, tc.args...))
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

// --version must not require --root: asking a binary its version should never
// depend on a valid sandbox being present.
func TestVersionIgnoresMissingRoot(t *testing.T) {
	capture(t)
	if _, err := Parse([]string{"--version"}); !errors.Is(err, ErrVersionRequested) {
		t.Fatalf("Parse(--version) error = %v, want ErrVersionRequested", err)
	}
	// Sanity: without --version the same empty args do fail on root, proving the
	// test above passed for the right reason.
	if _, err := Parse(nil); err == nil {
		t.Fatal("Parse(nil) succeeded; want a --root error")
	}
}

// --help must actually document the flags, not just exit quietly.
func TestHelpListsFlags(t *testing.T) {
	buf := capture(t)
	if _, err := Parse([]string{"--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("Parse(--help) error = %v, want flag.ErrHelp", err)
	}
	usage := buf.String()
	for _, want := range []string{"-root", "-addr", "-db", "-shells", "-version", "sessile"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage text missing %q; got:\n%s", want, usage)
		}
	}
}
