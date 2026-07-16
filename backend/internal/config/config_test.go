package config

import (
	"bytes"
	"errors"
	"flag"
	"strings"
	"testing"
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
