//go:build linux

package terminal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// startShell brings up a real PTY running sh and tears it down with the test.
// sh rather than bash, matching the plan's choice of a deterministic shell for
// integration tests (§13).
func startShell(t *testing.T, dir string) *PTY {
	t.Helper()
	p, err := Start("/bin/sh", dir, 24, 80, nil)
	if err != nil {
		t.Fatalf("start pty: %v", err)
	}
	t.Cleanup(func() {
		p.Signal(9)
		p.Wait()
		p.CloseFile()
	})
	// Drain, or the shell blocks once the pty buffer fills.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := p.File.Read(buf); err != nil {
				return
			}
		}
	}()
	return p
}

// waitForName polls until the foreground program has the wanted name. The
// shell needs a moment to fork, and the point of the test is the transition,
// not its latency.
func waitForName(t *testing.T, p *PTY, want string) Foreground {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last Foreground
	for time.Now().Before(deadline) {
		last = p.Foreground()
		if last.Name == want {
			return last
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("foreground never became %q; last was %+v", want, last)
	return last
}

// The whole point of asking the kernel instead of reading output: the answer
// changes the moment the user starts a program and changes back when it exits,
// with no parsing and nothing to get wrong.
func TestForegroundFollowsTheRunningProgram(t *testing.T) {
	p := startShell(t, t.TempDir())

	shell := waitForName(t, p, "sh")
	if shell.PID <= 0 {
		t.Errorf("foreground pid %d, want a real process group", shell.PID)
	}

	if err := p.Write([]byte("sleep 5\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sleeper := waitForName(t, p, "sleep")
	if sleeper.PID == shell.PID {
		t.Errorf("sleep reported the shell's own pid %d; the foreground group did not change", sleeper.PID)
	}

	// Ctrl+C returns the terminal to the shell.
	if err := p.Write([]byte{0x03}); err != nil {
		t.Fatalf("write interrupt: %v", err)
	}
	waitForName(t, p, "sh")
}

// cwd is what lets the dashboard show where a session actually is rather than
// where it was started, so it has to follow cd.
func TestForegroundReportsTheWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "nested")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The shell resolves its cwd through any symlinks (/tmp is one on macOS and
	// in some containers), and so does /proc, so compare resolved paths.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval root: %v", err)
	}

	p := startShell(t, root)
	waitForName(t, p, "sh")

	if got := p.Foreground().Cwd; got != resolvedRoot {
		t.Errorf("initial cwd = %q, want %q", got, resolvedRoot)
	}

	if err := p.Write([]byte("cd nested\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(resolvedRoot, "nested")
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := p.Foreground().Cwd
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cwd after cd = %q, want %q", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A closed pty must answer "not known" rather than panic or block: the sampler
// runs on its own clock and will call this on a session whose shell exited a
// moment ago.
func TestForegroundOnClosedPTYReportsNothing(t *testing.T) {
	p := startShell(t, t.TempDir())
	waitForName(t, p, "sh")

	p.Signal(9)
	p.Wait()
	p.CloseFile()

	if got := p.Foreground(); got.PID != 0 || got.Name != "" || got.Cwd != "" {
		t.Errorf("foreground on a closed pty = %+v, want the zero value", got)
	}
}

func TestForegroundOnZeroValuePTYReportsNothing(t *testing.T) {
	var p PTY
	if got := p.Foreground(); got != (Foreground{}) {
		t.Errorf("foreground on an unstarted pty = %+v, want the zero value", got)
	}
}
