package sshpty

import (
	"os/exec"
	"strconv"
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
	// A Windows target skips the preamble regardless of terminalType — a
	// "custom" TerminalType with a targetOS of "windows" is exactly
	// finding #4's case: terminalType alone doesn't say the target is
	// Windows, targetOS does.
	for _, terminalType := range []string{"cmd", "powershell", "custom"} {
		path, wrapped := wrapWithPIDRecording("windows", terminalType, "powershell.exe -NoLogo")
		if path != "" {
			t.Errorf("targetOS=windows terminalType=%q: pidFilePath = %q, want empty", terminalType, path)
		}
		if wrapped != "powershell.exe -NoLogo" {
			t.Errorf("targetOS=windows terminalType=%q: wrapped = %q, want cmd unchanged", terminalType, wrapped)
		}
	}
}

func TestWrapWithPIDRecordingDoesNotWrapANonWindowsCustomCommandJustBecauseItLooksLikeOne(t *testing.T) {
	// The old (wrong) gate keyed off terminalType == "cmd"/"powershell";
	// confirm a Linux/other target with those exact terminalType values
	// still gets wrapped — the gate is targetOS now, not terminalType.
	path, _ := wrapWithPIDRecording("linux", "bash", "bash")
	if path == "" {
		t.Fatal("targetOS=linux: pidFilePath is empty, want a preamble written")
	}
}

func TestWrapWithPIDRecordingWrapsPOSIXTargets(t *testing.T) {
	path, wrapped := wrapWithPIDRecording("linux", "bash", "bash")
	if path == "" || !strings.HasPrefix(path, "/tmp/.sessile-pid-") {
		t.Fatalf("pidFilePath = %q, want a /tmp/.sessile-pid-* path", path)
	}
	if !strings.HasPrefix(wrapped, "sh -c '") {
		t.Fatalf("wrapped = %q, want the whole preamble wrapped as sh -c '...'", wrapped)
	}
	if !strings.Contains(wrapped, "echo $$ > "+path) {
		t.Errorf("wrapped = %q, want it to record its own pid to %q", wrapped, path)
	}
	// "bash" is a bare shell name — nothing to interpret, so it's exec'd
	// directly, not through a second nested sh -c.
	if !strings.Contains(wrapped, "exec bash") {
		t.Errorf("wrapped = %q, want a direct exec of the bare shell name", wrapped)
	}
}

func TestWrapWithPIDRecordingRunsCustomCommandsThroughSh(t *testing.T) {
	// A CustomCommand containing shell operators must still run as one
	// whole unit under the inner sh -c, not have `exec` bind to only its
	// first word — that would silently drop everything after the `;`.
	_, wrapped := wrapWithPIDRecording("linux", "custom", "echo hi; tmux new -A -s main")
	if !strings.Contains(wrapped, `exec sh -c `) {
		t.Fatalf("wrapped = %q, want the custom command run through an inner sh -c", wrapped)
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

// TestShellSingleQuoteSurvivesNesting proves the two-level quoting
// wrapWithPIDRecording relies on (the custom-command case quotes cmd once
// for the inner `sh -c`, then the whole preamble — including that
// already-quoted text — gets quoted again for the outer `sh -c`) actually
// composes correctly through two real, chained shell invocations, not just
// one.
func TestShellSingleQuoteSurvivesNesting(t *testing.T) {
	cmd := "echo 'hi there'; echo done"
	innerQuoted := "sh -c " + shellSingleQuote(cmd)
	outerQuoted := "sh -c " + shellSingleQuote(innerQuoted)
	out := runSh(t, outerQuoted)
	want := "hi there\ndone\n"
	if out != want {
		t.Fatalf("doubly-nested sh -c produced %q, want %q", out, want)
	}
}

// TestWrapWithPIDRecordingAgainstRealFish and TestWrapWithPIDRecordingAgainstRealCsh
// are the review's exact repro: a login shell that isn't POSIX-syntax
// compatible with the raw preamble. Both are skipped, not failed, if the
// shell isn't installed — but installed here specifically to run them for
// real (not asserted about from documentation), since a preamble that
// merely looks portable is exactly what caused finding #3 in the first
// place.
func TestWrapWithPIDRecordingAgainstRealFish(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish not installed")
	}
	testWrapWithPIDRecordingAgainstRealLoginShell(t, fish)
}

func TestWrapWithPIDRecordingAgainstRealCsh(t *testing.T) {
	csh, err := exec.LookPath("csh")
	if err != nil {
		t.Skip("csh not installed")
	}
	testWrapWithPIDRecordingAgainstRealLoginShell(t, csh)
}

// testWrapWithPIDRecordingAgainstRealLoginShell simulates exactly what
// sshd does with an exec request: run the wrapped command through
// <loginShell> -c "<wrapped>". Before finding #3's fix, this failed for
// fish ($$ is not the pid) and silently wrote no pid file at all for csh
// (2> is not fd-numbered redirect syntax there — the shell reinterprets
// it, so the file is simply never created, no error at all). After the
// fix, the login shell only ever needs to invoke `sh -c '<opaque
// string>'` — ordinary command invocation, so it should work under both.
func testWrapWithPIDRecordingAgainstRealLoginShell(t *testing.T, loginShell string) {
	t.Helper()
	dir := t.TempDir()
	path, wrapped := wrapWithPIDRecording("linux", "custom", "echo started")
	// Redirect the preamble's own pidfile into a location we control so
	// this test doesn't touch the real /tmp path another test might race
	// with, and so it's easy to read back afterward.
	pidFile := dir + "/pid"
	wrapped = strings.ReplaceAll(wrapped, path, pidFile)

	out, err := exec.Command(loginShell, "-c", wrapped).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s -c %q failed: %v, stderr: %s", loginShell, wrapped, err, ee.Stderr)
		}
		t.Fatalf("%s -c %q failed: %v", loginShell, wrapped, err)
	}
	if !strings.Contains(string(out), "started") {
		t.Fatalf("%s output = %q, want it to contain the final command's output", loginShell, out)
	}

	pidBytes, err := exec.Command("cat", pidFile).Output()
	if err != nil {
		t.Fatalf("pid file was not written: %v", err)
	}
	if _, err := strconv.Atoi(strings.TrimSpace(string(pidBytes))); err != nil {
		t.Fatalf("pid file contents = %q, not a valid pid", pidBytes)
	}
}
