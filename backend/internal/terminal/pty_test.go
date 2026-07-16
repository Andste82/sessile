package terminal

import (
	"slices"
	"testing"
)

// A shell with no locale gets a UTF-8 one, because the wire protocol is UTF-8
// and glibc's C locale is ASCII-only. An explicit locale is never overridden.
func TestShellEnvLocale(t *testing.T) {
	tests := []struct {
		name        string
		parent      []string
		wantDefault bool
	}{
		{
			name:        "bare container has no locale",
			parent:      []string{"PATH=/usr/bin"},
			wantDefault: true,
		},
		{
			name:        "empty LANG is not a setting",
			parent:      []string{"LANG="},
			wantDefault: true,
		},
		{
			name:        "explicit LANG wins",
			parent:      []string{"LANG=de_DE.UTF-8"},
			wantDefault: false,
		},
		{
			name:        "explicit C locale is respected, not corrected",
			parent:      []string{"LANG=C"},
			wantDefault: false,
		},
		{
			name:        "LC_ALL alone counts",
			parent:      []string{"LC_ALL=en_US.UTF-8"},
			wantDefault: false,
		},
		{
			name:        "LC_CTYPE alone counts",
			parent:      []string{"LC_CTYPE=en_US.UTF-8"},
			wantDefault: false,
		},
		{
			name:        "a variable merely containing LANG does not count",
			parent:      []string{"SLANG=1", "LANGUAGE=de"},
			wantDefault: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shellEnv(tc.parent)
			if gotDefault := slices.Contains(got, defaultLocale); gotDefault != tc.wantDefault {
				t.Errorf("shellEnv(%v) default locale = %v, want %v", tc.parent, gotDefault, tc.wantDefault)
			}
			if !slices.Contains(got, "TERM=xterm-256color") {
				t.Errorf("shellEnv(%v) did not set TERM", tc.parent)
			}
			for _, kv := range tc.parent {
				if !slices.Contains(got, kv) {
					t.Errorf("shellEnv(%v) dropped %q from the parent env", tc.parent, kv)
				}
			}
		})
	}
}

// shellEnv must not write through to the caller's slice: os.Environ()'s result
// is shared, and appending to it in place could corrupt a concurrent Start.
func TestShellEnvDoesNotMutateParent(t *testing.T) {
	parent := []string{"PATH=/usr/bin"}
	before := slices.Clone(parent)
	shellEnv(parent)
	if !slices.Equal(parent, before) {
		t.Errorf("shellEnv mutated its argument: %v, want %v", parent, before)
	}
}
