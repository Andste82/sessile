package sshpty

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWrapWithPIDRecordingEmptyDirIsUnchanged pins that dir == "" produces
// exactly the pre-start-directory preamble.
func TestWrapWithPIDRecordingEmptyDirIsUnchanged(t *testing.T) {
	path, wrapped := wrapWithPIDRecording("linux", "bash", "", "bash")
	want := "sh -c " + shellSingleQuote("echo $$ > "+path+" 2>/dev/null; exec bash")
	if wrapped != want {
		t.Fatalf("wrapped = %q, want %q", wrapped, want)
	}
}

// TestWrapWithPIDRecordingChangesDirectory runs the wrapped command through
// a real sh for directory names that stress the quoting.
func TestWrapWithPIDRecordingChangesDirectory(t *testing.T) {
	names := []string{
		"plain",
		"with space",
		"it's",
		"$(touch pwned)",
		"`touch pwned`",
		"new\nline",
		"-dash",
		"semi;colon && x",
	}
	pidBase := t.TempDir()
	for i, name := range names {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, name)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Skipf("filesystem refuses this name: %v", err)
			}
			path, wrapped := wrapWithPIDRecording("linux", "custom", dir, "pwd -P")
			pidFile := filepath.Join(pidBase, "pid-"+strconv.Itoa(i))
			wrapped = strings.ReplaceAll(wrapped, path, pidFile)

			out, err := exec.Command("sh", "-c", wrapped).Output()
			if err != nil {
				t.Fatalf("sh -c %q: %v", wrapped, err)
			}
			want, _ := filepath.EvalSymlinks(dir)
			if got := strings.TrimSuffix(string(out), "\n"); got != want {
				t.Errorf("cwd = %q, want %q", got, want)
			}
			if _, err := os.Stat(pidFile); err != nil {
				t.Errorf("pid file not written: %v", err)
			}
			// Nothing injected through the directory name may have run.
			for _, d := range []string{base, dir, "."} {
				if _, err := os.Stat(filepath.Join(d, "pwned")); err == nil {
					t.Errorf("injection created %s/pwned", d)
				}
			}
		})
	}
}

// TestWrapWithPIDRecordingMissingDirDoesNotExec is the safety net: a failed
// cd must exit non-zero and never reach exec.
func TestWrapWithPIDRecordingMissingDirDoesNotExec(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	path, wrapped := wrapWithPIDRecording("linux", "custom", missing, "echo SHOULD-NOT-RUN")
	pidFile := filepath.Join(t.TempDir(), "pid")
	wrapped = strings.ReplaceAll(wrapped, path, pidFile)

	out, err := exec.Command("sh", "-c", wrapped).CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() == 0 {
		t.Fatalf("err = %v, want a non-zero exit (output %q)", err, out)
	}
	if strings.Contains(string(out), "SHOULD-NOT-RUN") {
		t.Errorf("command ran despite failed cd: %q", out)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("pid file written despite failed cd")
	}
}

// TestWrapWithPIDRecordingNoDirOnWindows: Windows-flagged targets get no
// wrapper and no cd, whatever dir is.
func TestWrapWithPIDRecordingNoDirOnWindows(t *testing.T) {
	cases := []struct{ targetOS, terminalType string }{
		{"windows", "custom"},
		{"", "cmd"},
		{"", "powershell"},
	}
	for _, c := range cases {
		path, wrapped := wrapWithPIDRecording(c.targetOS, c.terminalType, "/srv/x", "powershell.exe")
		if path != "" || wrapped != "powershell.exe" {
			t.Errorf("%+v: got (%q, %q), want unwrapped", c, path, wrapped)
		}
	}
}

func TestStartWithDirectory(t *testing.T) {
	existing := t.TempDir()
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(existing, "missing")

	t.Run("happy path", func(t *testing.T) {
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		p, err := Start(testTarget(ts.addr, ts.fingerprint), existing, 24, 80)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		p.CloseFile()
	})

	t.Run("missing directory", func(t *testing.T) {
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		_, err := Start(testTarget(ts.addr, ts.fingerprint), missing, 24, 80)
		var nf *ErrDirNotFound
		if !errors.As(err, &nf) {
			t.Fatalf("err = %v, want *ErrDirNotFound", err)
		}
		if nf.Path != missing || !strings.Contains(err.Error(), missing) {
			t.Errorf("error %q does not name %q", err, missing)
		}
	})

	t.Run("a file is not a directory", func(t *testing.T) {
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		_, err := Start(testTarget(ts.addr, ts.fingerprint), file, 24, 80)
		var nf *ErrDirNotFound
		if !errors.As(err, &nf) {
			t.Fatalf("err = %v, want *ErrDirNotFound", err)
		}
	})

	t.Run("no sftp subsystem", func(t *testing.T) {
		ts := newTestServer(t)
		_, err := Start(testTarget(ts.addr, ts.fingerprint), existing, 24, 80)
		var un *ErrSFTPUnavailable
		if !errors.As(err, &un) {
			t.Fatalf("err = %v, want *ErrSFTPUnavailable", err)
		}
		want := "cannot verify start directory: the host's SFTP subsystem is unavailable"
		if err.Error() != want {
			t.Errorf("message = %q, want %q", err.Error(), want)
		}
	})

	t.Run("windows target", func(t *testing.T) {
		ts := newTestServer(t)
		for _, mod := range []func(*Target){
			func(tg *Target) { tg.TargetOS = "windows" },
			func(tg *Target) { tg.TerminalType = "powershell" },
			func(tg *Target) { tg.TerminalType = "cmd" },
		} {
			tg := testTarget(ts.addr, ts.fingerprint)
			mod(&tg)
			_, err := Start(tg, existing, 24, 80)
			var un *ErrDirUnsupported
			if !errors.As(err, &un) {
				t.Errorf("err = %v, want *ErrDirUnsupported", err)
			}
		}
	})

	t.Run("host key errors take precedence", func(t *testing.T) {
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		_, err := Start(testTarget(ts.addr, ""), existing, 24, 80)
		var unknown *ErrHostKeyUnknown
		if !errors.As(err, &unknown) {
			t.Fatalf("err = %v, want *ErrHostKeyUnknown", err)
		}
		ts2 := newTestServer(t)
		_, err = Start(testTarget(ts2.addr, "SHA256:other"), existing, 24, 80)
		var changed *ErrHostKeyChanged
		if !errors.As(err, &changed) {
			t.Fatalf("err = %v, want *ErrHostKeyChanged", err)
		}
	})
}

func TestDialContextCancelAbortsHandshake(t *testing.T) {
	// A server that accepts TCP but never speaks SSH.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = Dial(ctx, testTarget(ln.Addr().String(), "SHA256:x"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("Dial took %v to notice the cancel", time.Since(start))
	}
}

func TestDialReturnsHostKeyErrors(t *testing.T) {
	ts := newTestServer(t)
	_, err := Dial(context.Background(), testTarget(ts.addr, ""))
	var unknown *ErrHostKeyUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want *ErrHostKeyUnknown", err)
	}
}
