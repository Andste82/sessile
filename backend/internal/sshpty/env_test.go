package sshpty

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/envfile"
)

const envSentinel = "S3NTINEL-VALUE"

func TestSupportsEnv(t *testing.T) {
	cases := []struct {
		os, term string
		want     bool
	}{
		{"linux", "bash", true},
		{"darwin", "zsh", true},
		{"other", "fish", true},
		{"", "bash", true}, // unspecified OS + a POSIX shell
		{"", "custom", true},
		{"linux", "custom", true},
		{"windows", "custom", false},
		{"windows", "bash", false},
		{"", "cmd", false},
		{"", "powershell", false},
		{"linux", "powershell", false},
	}
	for _, c := range cases {
		if got := SupportsEnv(c.os, c.term); got != c.want {
			t.Errorf("SupportsEnv(%q,%q) = %v, want %v", c.os, c.term, got, c.want)
		}
	}
}

// runWrapped executes the wrapped command through a real sh, with the pid
// file redirected into a temp dir, and returns stdout.
func runWrapped(t *testing.T, dir, cmd string, env []envfile.Entry) string {
	t.Helper()
	path, wrapped := wrapWithPIDRecording("linux", "custom", dir, cmd, env)
	pidFile := filepath.Join(t.TempDir(), "pid")
	wrapped = strings.ReplaceAll(wrapped, path, pidFile)
	out, err := exec.Command("sh", "-c", wrapped).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", wrapped, err)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Errorf("pid file not written: %v", err)
	}
	return string(out)
}

func TestWrapWithPIDRecordingExportsRoundTrip(t *testing.T) {
	values := map[string]string{
		"spaces":      "a b  c",
		"squote":      "it's",
		"dquote":      `say "hi"`,
		"newline":     "l1\nl2\n",
		"dollar":      "$HOME ${X} $(touch pwned)",
		"backtick":    "`touch pwned`",
		"backslash":   `a\b\\c\n`,
		"empty":       "",
		"unicode":     "grüße ☃ 日本",
		"manyq":       strings.Repeat("'", 50) + "x" + strings.Repeat("'", 7),
		"semicolon":   "a; touch pwned && b | c > d",
		"leadingdash": "-n -e",
	}
	for name, val := range values {
		t.Run(name, func(t *testing.T) {
			cwd := t.TempDir()
			out := runWrapped(t, cwd, `printf %s "$SESSILE_T"`, []envfile.Entry{{Name: "SESSILE_T", Value: val}})
			if out != val {
				t.Errorf("round trip = %q, want %q", out, val)
			}
			if _, err := os.Stat(filepath.Join(cwd, "pwned")); err == nil {
				t.Error("injection ran")
			}
		})
	}
}

func TestWrapWithPIDRecordingMultipleExportsAndCustomInheritance(t *testing.T) {
	env := []envfile.Entry{{Name: "A", Value: "1 1"}, {Name: "B", Value: "it's"}, {Name: "_C9", Value: ""}}
	// The custom command is itself run through a nested `sh -c`: the
	// variables must be inherited across that exec.
	out := runWrapped(t, "", `printf '%s|%s|%s' "$A" "$B" "${_C9-unset}"`, env)
	if out != "1 1|it's|" {
		t.Errorf("out = %q", out)
	}
}

func TestWrapWithPIDRecordingOrder(t *testing.T) {
	env := []envfile.Entry{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	path, wrapped := wrapWithPIDRecording("linux", "bash", "/srv/x", "bash", env)
	want := "sh -c " + shellSingleQuote("cd -- '/srv/x' && { export A='1'; export B='2'; echo $$ > "+path+" 2>/dev/null; exec bash; }")
	if wrapped != want {
		t.Errorf("wrapped = %q\nwant      %q", wrapped, want)
	}
	path, wrapped = wrapWithPIDRecording("linux", "bash", "", "bash", env)
	want = "sh -c " + shellSingleQuote("export A='1'; export B='2'; echo $$ > "+path+" 2>/dev/null; exec bash")
	if wrapped != want {
		t.Errorf("no dir: wrapped = %q\nwant      %q", wrapped, want)
	}
}

func TestWrapWithPIDRecordingCdRunsBeforeExports(t *testing.T) {
	cwd := t.TempDir()
	out := runWrapped(t, cwd, `pwd -P; printf %s "$A"`, []envfile.Entry{{Name: "A", Value: "x"}})
	want, _ := filepath.EvalSymlinks(cwd)
	if out != want+"\nx" {
		t.Errorf("out = %q", out)
	}
	// A failed cd must not export or exec.
	path, wrapped := wrapWithPIDRecording("linux", "custom", filepath.Join(cwd, "gone"), "echo RAN", []envfile.Entry{{Name: "A", Value: "x"}})
	wrapped = strings.ReplaceAll(wrapped, path, filepath.Join(cwd, "pid"))
	if out, err := exec.Command("sh", "-c", wrapped).CombinedOutput(); err == nil || strings.Contains(string(out), "RAN") {
		t.Errorf("err=%v out=%q", err, out)
	}
}

func TestWrapWithPIDRecordingWindowsIgnoresEnv(t *testing.T) {
	path, wrapped := wrapWithPIDRecording("windows", "custom", "", "powershell.exe", []envfile.Entry{{Name: "A", Value: "1"}})
	if path != "" || wrapped != "powershell.exe" {
		t.Errorf("got (%q, %q)", path, wrapped)
	}
}

func startEnv(t *testing.T, ts *testServer, mod func(*Target), o StartOptions) (*PTY, []string, error) {
	t.Helper()
	tg := testTarget(ts.addr, ts.fingerprint)
	if mod != nil {
		mod(&tg)
	}
	if o.Rows == 0 {
		o.Rows, o.Cols = 24, 80
	}
	return StartWithEnv(tg, o)
}

// lastExec returns the preamble the server received: the argument of the
// outer `sh -c '<preamble>'`, unquoted.
func lastExec(ts *testServer) string {
	s, _ := ts.lastExec.Load().(string)
	s = strings.TrimPrefix(s, "sh -c ")
	s = strings.TrimSuffix(strings.TrimPrefix(s, "'"), "'")
	return strings.ReplaceAll(s, `'\''`, "'")
}

func TestStartWithEnvExports(t *testing.T) {
	ts := newTestServer(t)
	p, warns, err := startEnv(t, ts, nil, StartOptions{Env: []envfile.Entry{
		{Name: "B", Value: "it's", Secret: true}, {Name: "A", Value: "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseFile()
	if len(warns) != 0 {
		t.Errorf("warnings = %q", warns)
	}
	// Merge sorts: A before B, whatever the input order.
	got := lastExec(ts)
	ia, ib := strings.Index(got, "export A="), strings.Index(got, "export B=")
	if ia < 0 || ib < ia {
		t.Errorf("exec line lacks sorted exports: %q", got)
	}
}

func TestStartWithEnvDotenvFromDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FROM_FILE=file\nOVERRIDDEN=file\nDEFAULTED=file\nTERM=bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t)
	ts.sftpEnabled.Store(true)
	p, warns, err := startEnv(t, ts, nil, StartOptions{Dir: dir, LoadDotenv: true, Env: []envfile.Entry{
		{Name: "OVERRIDDEN", Value: "dialog"},
		{Name: "DEFAULTED", Value: "default", Default: true},
		{Name: "ONLY_DEFAULT", Value: "d", Default: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseFile()
	if len(warns) != 1 || !strings.Contains(warns[0], "TERM is reserved") {
		t.Errorf("warnings = %q", warns)
	}
	line := lastExec(ts)
	for _, want := range []string{"export FROM_FILE='file'", "export OVERRIDDEN='dialog'", "export DEFAULTED='file'", "export ONLY_DEFAULT='d'"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	if strings.Contains(line, "bad") {
		t.Errorf("reserved name from .env was applied: %q", line)
	}
}

func TestStartWithEnvDotenvFromSFTPHome(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("HOME_VAR=h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home) // the in-process sftp server's start directory
	ts := newTestServer(t)
	ts.sftpEnabled.Store(true)
	p, warns, err := startEnv(t, ts, nil, StartOptions{LoadDotenv: true})
	if err != nil {
		t.Fatal(err)
	}
	defer p.CloseFile()
	if len(warns) != 0 || !strings.Contains(lastExec(ts), "export HOME_VAR='h'") {
		t.Errorf("warnings=%q exec=%q", warns, lastExec(ts))
	}
}

func TestStartWithEnvDotenvProblems(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		p, warns, err := startEnv(t, ts, nil, StartOptions{Dir: t.TempDir(), LoadDotenv: true})
		if err != nil {
			t.Fatal(err)
		}
		defer p.CloseFile()
		if len(warns) != 1 || warns[0] != "no .env file found" {
			t.Errorf("warnings = %q", warns)
		}
	})
	t.Run("directory instead of file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".env"), 0o755); err != nil {
			t.Fatal(err)
		}
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		p, warns, err := startEnv(t, ts, nil, StartOptions{Dir: dir, LoadDotenv: true})
		if err != nil {
			t.Fatal(err)
		}
		defer p.CloseFile()
		if len(warns) != 1 || warns[0] != "cannot read .env" {
			t.Errorf("warnings = %q", warns)
		}
	})
	t.Run("too large", func(t *testing.T) {
		dir := t.TempDir()
		big := "A=" + strings.Repeat("x", envfile.MaxFileBytes) + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(big), 0o600); err != nil {
			t.Fatal(err)
		}
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		p, warns, err := startEnv(t, ts, nil, StartOptions{Dir: dir, LoadDotenv: true})
		if err != nil {
			t.Fatal(err)
		}
		defer p.CloseFile()
		if len(warns) != 1 || warns[0] != ".env too large, skipped" {
			t.Errorf("warnings = %q", warns)
		}
		if strings.Contains(lastExec(ts), "export A=") {
			t.Error("oversized .env was applied")
		}
	})
	t.Run("sftp unavailable without dir still starts", func(t *testing.T) {
		ts := newTestServer(t) // sftp refused
		p, warns, err := startEnv(t, ts, nil, StartOptions{LoadDotenv: true, Env: []envfile.Entry{{Name: "A", Value: "1"}}})
		if err != nil {
			t.Fatal(err)
		}
		defer p.CloseFile()
		if len(warns) != 1 || warns[0] != "cannot read .env" {
			t.Errorf("warnings = %q", warns)
		}
		if !strings.Contains(lastExec(ts), "export A='1'") {
			t.Errorf("exec = %q", lastExec(ts))
		}
	})
	t.Run("sftp unavailable with dir is a hard error", func(t *testing.T) {
		ts := newTestServer(t)
		_, _, err := startEnv(t, ts, nil, StartOptions{Dir: t.TempDir(), LoadDotenv: true})
		var un *ErrSFTPUnavailable
		if !errors.As(err, &un) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing dir stays a hard error", func(t *testing.T) {
		ts := newTestServer(t)
		ts.sftpEnabled.Store(true)
		_, _, err := startEnv(t, ts, nil, StartOptions{Dir: filepath.Join(t.TempDir(), "gone"), LoadDotenv: true})
		var nf *ErrDirNotFound
		if !errors.As(err, &nf) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestStartWithEnvUnsupportedTarget(t *testing.T) {
	ts := newTestServer(t)
	for _, mod := range []func(*Target){
		func(tg *Target) { tg.TargetOS = "windows" },
		func(tg *Target) { tg.TerminalType = "powershell" },
		func(tg *Target) { tg.TerminalType = "cmd" },
	} {
		for _, o := range []StartOptions{
			{Env: []envfile.Entry{{Name: "A", Value: envSentinel, Secret: true}}},
			{LoadDotenv: true},
		} {
			_, _, err := startEnv(t, ts, mod, o)
			if !errors.Is(err, ErrEnvUnsupported) {
				t.Errorf("err = %v, want ErrEnvUnsupported", err)
			} else if strings.Contains(err.Error(), envSentinel) {
				t.Errorf("error leaks a value: %v", err)
			}
		}
	}
	// No env: a Windows target still starts as before.
	p, _, err := startEnv(t, ts, func(tg *Target) { tg.TargetOS = "windows" }, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p.CloseFile()
}

func TestStartWithEnvTooLarge(t *testing.T) {
	ts := newTestServer(t)
	var env []envfile.Entry
	for i := 0; i < 40; i++ {
		env = append(env, envfile.Entry{Name: "V" + strings.Repeat("a", i), Value: strings.Repeat("'", envfile.MaxValueLen), Secret: true})
	}
	_, _, err := startEnv(t, ts, nil, StartOptions{Env: env})
	if !errors.Is(err, ErrEnvTooLarge) {
		t.Fatalf("err = %v, want ErrEnvTooLarge", err)
	}
	if strings.Contains(err.Error(), "'") {
		t.Errorf("error leaks content: %v", err)
	}
}

func TestStartWithEnvErrorsNeverLeakValues(t *testing.T) {
	ts := newTestServer(t)
	env := []envfile.Entry{{Name: "A", Value: envSentinel, Secret: true}}
	// Unknown host key: the dial fails before anything is built.
	tg := testTarget(ts.addr, "")
	_, _, err := StartWithEnv(tg, StartOptions{Env: env, Rows: 24, Cols: 80})
	if err == nil || strings.Contains(err.Error(), envSentinel) {
		t.Errorf("err = %v", err)
	}
	// Failing start: server refuses the exec request. The error names the
	// bare command, never the wrapped line.
	ts2 := newTestServer(t)
	ts2.rejectExec.Store(true)
	_, _, err = startEnv(t, ts2, nil, StartOptions{Env: env})
	if err == nil {
		t.Fatal("expected a start error")
	}
	if strings.Contains(err.Error(), envSentinel) || strings.Contains(err.Error(), "export") {
		t.Errorf("start error leaks the wrapped command: %v", err)
	}
	if !strings.Contains(err.Error(), "irrelevant-to-the-fake-server") {
		t.Errorf("start error should name the bare command: %v", err)
	}
}
