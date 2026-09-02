package sshpty

import (
	"os/exec"
	"strings"
	"testing"
)

// runSh runs line via a real local /bin/sh and returns its stdout — used to
// verify shellSingleQuote's escaping against an actual shell's parser, not
// just by inspecting the escaped string's shape.
func runSh(t *testing.T, line string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", line).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", line, err)
	}
	return string(out)
}

func TestWrapWithPIDRecordingSkipsWindowsTargets(t *testing.T) {
	for _, tt := range []string{"cmd", "powershell"} {
		path, wrapped := wrapWithPIDRecording(tt, tt)
		if path != "" {
			t.Errorf("terminalType=%q: pidFilePath = %q, want empty", tt, path)
		}
		if wrapped != tt {
			t.Errorf("terminalType=%q: wrapped = %q, want unchanged %q", tt, wrapped, tt)
		}
	}
}

func TestWrapWithPIDRecordingWrapsPOSIXTargets(t *testing.T) {
	path, wrapped := wrapWithPIDRecording("bash", "bash")
	if path == "" || !strings.HasPrefix(path, "/tmp/.sessile-pid-") {
		t.Fatalf("pidFilePath = %q, want a /tmp/.sessile-pid-* path", path)
	}
	if !strings.Contains(wrapped, "echo $$ > "+path) {
		t.Errorf("wrapped = %q, want it to record its own pid to %q", wrapped, path)
	}
	if !strings.Contains(wrapped, "exec sh -c 'bash'") {
		t.Errorf("wrapped = %q, want an exec into the original command", wrapped)
	}
}

func TestWrapWithPIDRecordingPreservesCustomCommandWithShellOperators(t *testing.T) {
	// A CustomCommand containing shell operators must still run as one
	// whole unit under the inner sh -c, not have `exec` bind to only its
	// first word — that would silently drop everything after the `;`.
	_, wrapped := wrapWithPIDRecording("custom", "echo hi; tmux new -A -s main")
	if !strings.Contains(wrapped, `exec sh -c 'echo hi; tmux new -A -s main'`) {
		t.Fatalf("wrapped = %q, want the whole custom command quoted as one sh -c argument", wrapped)
	}
}

func TestWrapWithPIDRecordingEscapesEmbeddedSingleQuotes(t *testing.T) {
	_, wrapped := wrapWithPIDRecording("custom", "echo 'hi there'")
	want := `exec sh -c 'echo '\''hi there'\'''`
	if !strings.Contains(wrapped, want) {
		t.Fatalf("wrapped = %q, want it to contain %q", wrapped, want)
	}
}

func TestShellSingleQuoteRoundTripsThroughARealShell(t *testing.T) {
	// The escaping trick only matters if a real POSIX shell actually
	// parses it back to the original string — verified against /bin/sh,
	// not just asserted about its shape.
	cases := []string{
		"plain",
		"has space",
		"has'quote",
		"''already''quoted''",
		"a; b && c | d",
	}
	for _, s := range cases {
		quoted := shellSingleQuote(s)
		out := runSh(t, "printf '%s' "+quoted)
		if out != s {
			t.Errorf("shellSingleQuote(%q) round-tripped through sh as %q", s, out)
		}
	}
}
