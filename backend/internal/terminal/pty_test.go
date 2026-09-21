package terminal

import (
	"slices"
	"strings"
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
			got := shellEnv(tc.parent, nil)
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
	shellEnv(parent, []string{"HISTFILE=/tmp/h"})
	if !slices.Equal(parent, before) {
		t.Errorf("shellEnv mutated its argument: %v, want %v", parent, before)
	}
}

// Extra assignments must come last: exec resolves duplicate names to the final
// occurrence, which is what lets a session's HISTFILE override the server's.
func TestShellEnvExtraOverridesParent(t *testing.T) {
	got := shellEnv([]string{"HISTFILE=/home/op/.bash_history"}, []string{"HISTFILE=/data/history/abc"})

	var last string
	for _, kv := range got {
		if strings.HasPrefix(kv, "HISTFILE=") {
			last = kv
		}
	}
	if want := "HISTFILE=/data/history/abc"; last != want {
		t.Errorf("last HISTFILE = %q, want %q", last, want)
	}
}

// TestStartArgsDropsBlockedEnvironment: a task's agent must not inherit the
// server's own agent environment (§4.12.9), while everything else — PATH, a
// proxy, the locale — still reaches it, and the task's own values still win.
func TestStartArgsDropsBlockedEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "the-operator's-session")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ANTHROPIC_API_KEY", "sk-operator")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")

	dir := t.TempDir()
	p, err := StartArgs("/bin/sh", []string{"-c", "env; exit 0"}, dir, 24, 80,
		[]string{"CLAUDE_CODE_OAUTH_TOKEN=oat-from-the-task"},
		"CLAUDE", "ANTHROPIC")
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := p.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	p.Wait()
	p.CloseFile()
	got := string(out)

	for _, unwanted := range []string{"CLAUDE_CODE_SESSION_ID=", "CLAUDECODE=", "ANTHROPIC_API_KEY="} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%s reached the task's environment:\n%s", unwanted, got)
		}
	}
	for _, want := range []string{"CLAUDE_CODE_OAUTH_TOKEN=oat-from-the-task", "HTTPS_PROXY=http://proxy.example:3128", "PATH="} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is missing from the task's environment:\n%s", want, got)
		}
	}
}
