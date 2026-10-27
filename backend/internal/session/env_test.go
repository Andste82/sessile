package session

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/envfile"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

const envSentinel = "S3NTINEL-VALUE"

// stopLocal ends a local sh session and waits for it to be stopped.
func stopLocal(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	if err := mgr.WriteInput(id, []byte("exit\n")); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	waitForStatus(t, mgr, id, StatusStopped)
}

// shellSees types a command into a live session and waits for the marker the
// command prints around the variable's value.
func shellSees(t *testing.T, mgr *Manager, id, name, want string) {
	t.Helper()
	cmd := "printf '<<" + name + "=%s>>' \"$" + name + "\"\n"
	if err := mgr.WriteInput(id, []byte(cmd)); err != nil {
		t.Fatalf("WriteInput: %v", err)
	}
	waitForOutput(t, liveSession(t, mgr, id), "<<"+name+"="+want+">>")
}

func TestCreateLocalAppliesEnv(t *testing.T) {
	mgr, store, _ := testManager(t)
	env := []EnvVar{
		{Name: "ZED", Value: "it's a \"test\" $HOME"},
		{Name: "ALPHA", Value: "1", Default: true},
		{Name: "TOKEN", Value: envSentinel, Secret: true},
	}
	info, warns, err := mgr.CreateLocal("test-user", "e", "", ".", "sh", CreateOptions{Env: env})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %q", warns)
	}
	var got []string
	for _, e := range info.Env {
		got = append(got, e.Name)
	}
	if !reflect.DeepEqual(got, []string{"ALPHA", "TOKEN", "ZED"}) {
		t.Errorf("Info.Env not sorted by name: %v", got)
	}
	row, _, _ := store.Get(info.ID)
	if !reflect.DeepEqual(row.Env, info.Env) {
		t.Errorf("stored env = %+v, want %+v", row.Env, info.Env)
	}
	shellSees(t, mgr, info.ID, "ZED", `it's a "test" $HOME`)
	shellSees(t, mgr, info.ID, "ALPHA", "1")
	shellSees(t, mgr, info.ID, "TOKEN", envSentinel)
	shellSees(t, mgr, info.ID, "TERM", "xterm-256color")

	// The caller's slice and the returned Info do not alias the live session.
	env[0].Value = "mutated"
	info.Env[0].Value = "mutated"
	if got := liveSession(t, mgr, info.ID).Info().Env; got[0].Value != "1" {
		t.Errorf("live env aliased: %+v", got)
	}
}

func TestCreateLocalHistoryVariablesWinOverUserValues(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dataDir := t.TempDir()
	mgr := NewManager(t.TempDir(), []string{"bash"}, 64<<10, dataDir, newMemStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(mgr.Shutdown)

	// Reserved names cannot arrive through Create; call the spawn directly to
	// prove the ordering as the second line of defence.
	s, _, err := mgr.spawnLocal("11111111-2222-3333-4444-555555555555", "test-user", "h", ".", "bash", timeNow(),
		[]EnvVar{{Name: "HISTSIZE", Value: "7"}, {Name: "HISTFILE", Value: "/dev/null"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.register(s); err != nil {
		t.Fatal(err)
	}
	if err := mgr.WriteInput(s.ID, []byte("printf '<<%s>>' \"${HISTFILE##*/}\"\n")); err != nil {
		t.Fatal(err)
	}
	want := "<<" + s.ID + ">>" // HISTSIZE is not asserted: a distro bashrc resets it
	waitForOutput(t, s, want)
}

func TestCreateValidatesEnv(t *testing.T) {
	mgr, store, _ := testManager(t)
	cases := map[string][]EnvVar{
		"reserved TERM":   {{Name: "TERM", Value: envSentinel}},
		"reserved LANG":   {{Name: "LANG", Value: envSentinel}},
		"bad name":        {{Name: "1BAD", Value: envSentinel}},
		"duplicate":       {{Name: "A", Value: envSentinel}, {Name: "A", Value: envSentinel}},
		"NUL":             {{Name: "A", Value: envSentinel + "\x00"}},
		"value too large": {{Name: "A", Value: strings.Repeat("x", envfile.MaxValueLen+1)}},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := mgr.CreateLocal("test-user", "e", "", ".", "sh", CreateOptions{Env: env})
			if !errors.Is(err, ErrInvalidEnv) || !errors.Is(err, envfile.ErrInvalid) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), envSentinel) {
				t.Errorf("error leaks a value: %v", err)
			}
			srv := newStartDirServer(t)
			_, _, err = mgr.CreateSSH("test-user", "e", "", "h", "host", srv.target(), "", CreateOptions{Env: env})
			if !errors.Is(err, ErrInvalidEnv) {
				t.Fatalf("ssh err = %v", err)
			}
		})
	}
	if len(store.rows) != 0 {
		t.Errorf("%d rows stored after rejected creates", len(store.rows))
	}
}

func TestLocalDotenv(t *testing.T) {
	mgr, _, _ := testManager(t)
	root := mgr.root
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.env"), []byte("LEAKED="+envSentinel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("missing", func(t *testing.T) {
		info, warns, err := mgr.CreateLocal("test-user", "d", "", ".", "sh", CreateOptions{DotEnv: true})
		if err != nil {
			t.Fatal(err)
		}
		if !info.DotEnv || !reflect.DeepEqual(warns, []string{"no .env file found"}) {
			t.Errorf("DotEnv=%v warnings=%q", info.DotEnv, warns)
		}
	})
	t.Run("read from a subdirectory of the root", func(t *testing.T) {
		write("sub/.env", "FROM_SUB=yes\nTERM=bad\n")
		info, warns, err := mgr.CreateLocal("test-user", "d", "", "sub", "sh", CreateOptions{DotEnv: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(warns) != 1 || !strings.HasPrefix(warns[0], ".env line 2:") {
			t.Errorf("warnings = %q", warns)
		}
		shellSees(t, mgr, info.ID, "FROM_SUB", "yes")
		shellSees(t, mgr, info.ID, "TERM", "xterm-256color")
		// .env-derived values are not part of the stored session.
		if len(info.Env) != 0 {
			t.Errorf("dotenv values leaked into Info.Env: %+v", info.Env)
		}
	})
	t.Run("symlink inside the root is followed", func(t *testing.T) {
		write("real/target.env", "VIA_LINK=ok\n")
		if err := os.MkdirAll(filepath.Join(root, "linkdir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "real", "target.env"), filepath.Join(root, "linkdir", ".env")); err != nil {
			t.Fatal(err)
		}
		info, warns, err := mgr.CreateLocal("test-user", "d", "", "linkdir", "sh", CreateOptions{DotEnv: true})
		if err != nil || len(warns) != 0 {
			t.Fatalf("err=%v warnings=%q", err, warns)
		}
		shellSees(t, mgr, info.ID, "VIA_LINK", "ok")
	})
	t.Run("symlink escaping the root is skipped", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(root, "esc"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "secret.env"), filepath.Join(root, "esc", ".env")); err != nil {
			t.Fatal(err)
		}
		info, warns, err := mgr.CreateLocal("test-user", "d", "", "esc", "sh", CreateOptions{DotEnv: true})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(warns, []string{"cannot read .env"}) {
			t.Errorf("warnings = %q", warns)
		}
		shellSees(t, mgr, info.ID, "LEAKED", "") // never set
		for _, w := range warns {
			if strings.Contains(w, envSentinel) {
				t.Errorf("warning leaks content: %q", w)
			}
		}
	})
	t.Run("directory instead of file", func(t *testing.T) {
		if err := os.MkdirAll(filepath.Join(root, "dirdir", ".env"), 0o755); err != nil {
			t.Fatal(err)
		}
		_, warns, err := mgr.CreateLocal("test-user", "d", "", "dirdir", "sh", CreateOptions{DotEnv: true})
		if err != nil || !reflect.DeepEqual(warns, []string{"cannot read .env"}) {
			t.Errorf("err=%v warnings=%q", err, warns)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		write("big/.env", "BIG_VAR=1\n"+strings.Repeat("# padding\n", envfile.MaxFileBytes/10))
		info, warns, err := mgr.CreateLocal("test-user", "d", "", "big", "sh", CreateOptions{DotEnv: true})
		if err != nil || !reflect.DeepEqual(warns, []string{".env too large, skipped"}) {
			t.Fatalf("err=%v warnings=%q", err, warns)
		}
		shellSees(t, mgr, info.ID, "BIG_VAR", "")
	})
	t.Run("at the size cap is read", func(t *testing.T) {
		line := "AT_CAP=1\n"
		write("cap/.env", line+strings.Repeat("#", envfile.MaxFileBytes-len(line)))
		info, warns, err := mgr.CreateLocal("test-user", "d", "", "cap", "sh", CreateOptions{DotEnv: true})
		if err != nil || len(warns) != 0 {
			t.Fatalf("err=%v warnings=%q", err, warns)
		}
		shellSees(t, mgr, info.ID, "AT_CAP", "1")
	})
	t.Run("dotenv off ignores the file", func(t *testing.T) {
		write("off/.env", "IGNORED=1\n")
		info, warns, err := mgr.CreateLocal("test-user", "d", "", "off", "sh", CreateOptions{})
		if err != nil || len(warns) != 0 {
			t.Fatalf("err=%v warnings=%q", err, warns)
		}
		shellSees(t, mgr, info.ID, "IGNORED", "")
	})
}

func TestLocalPrecedenceDefaultsDotenvDialog(t *testing.T) {
	mgr, _, _ := testManager(t)
	if err := os.WriteFile(filepath.Join(mgr.root, ".env"), []byte("DEF_OVERRIDDEN=env\nDLG_OVERRIDDEN=env\nENV_ONLY=env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _, err := mgr.CreateLocal("test-user", "p", "", ".", "sh", CreateOptions{DotEnv: true, Env: []EnvVar{
		{Name: "DEF_OVERRIDDEN", Value: "default", Default: true},
		{Name: "DEF_ONLY", Value: "default", Default: true},
		{Name: "DLG_OVERRIDDEN", Value: "dialog"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	shellSees(t, mgr, info.ID, "DEF_OVERRIDDEN", "env")
	shellSees(t, mgr, info.ID, "DEF_ONLY", "default")
	shellSees(t, mgr, info.ID, "DLG_OVERRIDDEN", "dialog")
	shellSees(t, mgr, info.ID, "ENV_ONLY", "env")
}

func TestRestartRereadsDotenv(t *testing.T) {
	mgr, _, _ := testManager(t)
	path := filepath.Join(mgr.root, ".env")
	if err := os.WriteFile(path, []byte("GEN=one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _, err := mgr.CreateLocal("test-user", "r", "", ".", "sh", CreateOptions{DotEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	shellSees(t, mgr, info.ID, "GEN", "one")
	stopLocal(t, mgr, info.ID)

	if err := os.WriteFile(path, []byte("GEN=two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, warns, err := mgr.Restart(info.ID, "test-user")
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warnings=%q", err, warns)
	}
	shellSees(t, mgr, restarted.ID, "GEN", "two")

	// File gone: still starts, warns.
	stopLocal(t, mgr, info.ID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, warns, err = mgr.Restart(info.ID, "test-user")
	if err != nil || !reflect.DeepEqual(warns, []string{"no .env file found"}) {
		t.Fatalf("err=%v warnings=%q", err, warns)
	}
}

func TestEnvSurvivesManagerRestart(t *testing.T) {
	mgr, store, dataDir := testManager(t)
	env := []EnvVar{
		{Name: "KEEP", Value: "plain"},
		{Name: "INHERITED", Value: "d", Default: true},
		{Name: "SEC", Value: envSentinel, Secret: true, Default: true},
	}
	created, _, err := mgr.CreateLocal("test-user", "s", "", ".", "sh", CreateOptions{Env: env, DotEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	stopLocal(t, mgr, created.ID)
	mgr.Shutdown()

	revived := NewManager(mgr.root, mgr.shells, mgr.bufferSize, dataDir, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(revived.Shutdown)
	row, _, _ := store.Get(created.ID)
	if !row.DotEnv || !reflect.DeepEqual(row.Env, created.Env) {
		t.Fatalf("stored row = %+v", row)
	}
	restarted, _, err := revived.Restart(created.ID, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.DotEnv || !reflect.DeepEqual(restarted.Env, created.Env) {
		t.Errorf("restarted env = %+v dotenv=%v", restarted.Env, restarted.DotEnv)
	}
	shellSees(t, revived, restarted.ID, "SEC", envSentinel)
	shellSees(t, revived, restarted.ID, "KEEP", "plain")
}

func ep(name string, value *string, secret bool) EnvPatchEntry {
	return EnvPatchEntry{Name: name, Value: value, Secret: secret}
}

func entries(es ...EnvPatchEntry) *EnvUpdate { return &EnvUpdate{Entries: &es} }

func TestUpdateEnvOnStoppedLiveSession(t *testing.T) {
	mgr, store, _ := testManager(t)
	info, _, err := mgr.CreateLocal("test-user", "u", "", ".", "sh", CreateOptions{Env: []EnvVar{
		{Name: "A", Value: "1"},
		{Name: "SEC", Value: envSentinel, Secret: true},
		{Name: "DEF", Value: "d", Default: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Running: refused, and nothing else of the same call is applied.
	if _, err := mgr.Update(info.ID, "test-user", ptr("renamed"), nil, entries()); !errors.Is(err, ErrEnvRunning) {
		t.Fatalf("running: err = %v, want ErrEnvRunning", err)
	}
	if got, _ := mgr.Get(info.ID, "test-user"); got.Name != "u" || len(got.Env) != 3 {
		t.Errorf("rejected update changed the session: %+v", got)
	}
	// Someone else's running session is simply not found.
	if _, err := mgr.Update(info.ID, "other-user", nil, nil, entries()); !errors.Is(err, ErrNotFound) {
		t.Errorf("other user: err = %v, want ErrNotFound", err)
	}

	stopLocal(t, mgr, info.ID)

	updated, err := mgr.Update(info.ID, "test-user", nil, nil, &EnvUpdate{
		Entries: &[]EnvPatchEntry{
			ep("SEC", nil, true), // keep the stored secret
			ep("DEF", ptr("d"), false),
			ep("NEW", ptr("n"), false),
		},
		DotEnv: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvVar{
		{Name: "DEF", Value: "d", Default: true},
		{Name: "NEW", Value: "n"},
		{Name: "SEC", Value: envSentinel, Secret: true},
	}
	if !reflect.DeepEqual(updated.Env, want) || !updated.DotEnv {
		t.Errorf("updated = %+v dotenv=%v", updated.Env, updated.DotEnv)
	}
	if row, _, _ := store.Get(info.ID); !reflect.DeepEqual(row.Env, want) || !row.DotEnv {
		t.Errorf("stored = %+v", row)
	}

	// Entries nil, DotEnv only: the list is kept.
	updated, err = mgr.Update(info.ID, "test-user", nil, nil, &EnvUpdate{DotEnv: boolPtr(false)})
	if err != nil || !reflect.DeepEqual(updated.Env, want) || updated.DotEnv {
		t.Errorf("dotenv-only update: %+v err=%v", updated, err)
	}

	// Invalid input changes nothing and names no value.
	_, err = mgr.Update(info.ID, "test-user", ptr("x"), nil, entries(ep("TERM", ptr(envSentinel), false)))
	if !errors.Is(err, ErrInvalidEnv) || strings.Contains(err.Error(), envSentinel) {
		t.Fatalf("err = %v", err)
	}
	_, err = mgr.Update(info.ID, "test-user", nil, nil, entries(ep("NEW2", nil, true)))
	if !errors.Is(err, ErrInvalidEnv) {
		t.Fatalf("secret without existing entry: err = %v", err)
	}
	if got, _ := mgr.Get(info.ID, "test-user"); got.Name != "u" || !reflect.DeepEqual(got.Env, want) {
		t.Errorf("rejected updates changed the session: %+v", got)
	}

	// The updated list is what the next restart uses.
	restarted, _, err := mgr.Restart(info.ID, "test-user")
	if err != nil {
		t.Fatal(err)
	}
	shellSees(t, mgr, restarted.ID, "NEW", "n")
	shellSees(t, mgr, restarted.ID, "SEC", envSentinel)
	shellSees(t, mgr, restarted.ID, "A", "")
}

func boolPtr(b bool) *bool { return &b }

func TestUpdateEnvOnStoredOnlyRow(t *testing.T) {
	mgr, store, _ := testManager(t)
	_ = store.Insert(Info{
		ID: "orphan", Name: "o", UserID: "test-user", TargetType: TargetLocal, Directory: ".", Shell: "sh",
		Status: StatusStopped, Env: []EnvVar{{Name: "SEC", Value: envSentinel, Secret: true}},
	})
	_ = store.Insert(Info{ID: "stale", Name: "s", UserID: "test-user", Status: StatusRunning})

	info, err := mgr.Update("orphan", "test-user", nil, nil, entries(ep("SEC", nil, true), ep("B", ptr("2"), false)))
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvVar{{Name: "B", Value: "2"}, {Name: "SEC", Value: envSentinel, Secret: true}}
	if !reflect.DeepEqual(info.Env, want) {
		t.Errorf("env = %+v", info.Env)
	}
	if row, _, _ := store.Get("orphan"); !reflect.DeepEqual(row.Env, want) {
		t.Errorf("stored = %+v", row.Env)
	}
	if _, err := mgr.Update("orphan", "other-user", nil, nil, entries()); !errors.Is(err, ErrNotFound) {
		t.Errorf("other user: %v", err)
	}
	if _, err := mgr.Update("stale", "test-user", nil, nil, entries()); !errors.Is(err, ErrEnvRunning) {
		t.Errorf("non-stopped row: %v", err)
	}
	// Name/group-only updates keep working on any row, as before.
	if _, err := mgr.Update("stale", "test-user", ptr("n"), nil, nil); err != nil {
		t.Errorf("name-only update: %v", err)
	}
}

func TestUpdateNameGroupOnlyIgnoresStatusAndEnv(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, _, err := mgr.CreateLocal("test-user", "u", "", ".", "sh", CreateOptions{Env: []EnvVar{{Name: "A", Value: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Update(info.ID, "test-user", ptr("n"), ptr("g"), nil)
	if err != nil || got.Name != "n" || got.Group != "g" || len(got.Env) != 1 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestUpdateEnvRefusedWhileRestartReserved(t *testing.T) {
	mgr, store, _ := testManager(t)
	_ = store.Insert(Info{ID: "x", Name: "o", UserID: "test-user", Status: StatusStopped})
	if err := mgr.claimRestart("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Update("x", "test-user", nil, nil, entries()); !errors.Is(err, ErrEnvRunning) {
		t.Errorf("owner: err = %v", err)
	}
	if _, err := mgr.Update("x", "other-user", nil, nil, entries()); !errors.Is(err, ErrNotFound) {
		t.Errorf("other user: err = %v", err)
	}
	mgr.releaseRestart("x")
	if _, err := mgr.Update("x", "test-user", nil, nil, entries()); err != nil {
		t.Errorf("after release: %v", err)
	}
	// The edit's own reservation is gone again.
	if err := mgr.claimRestart("x"); err != nil {
		t.Errorf("reservation leaked: %v", err)
	}
}

func TestReservedNamesCoverHistoryEnv(t *testing.T) {
	dataDir := t.TempDir()
	names := map[string]bool{"TERM": true, "LANG": true}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		vars, err := historyEnv(dataDir, shell, "11111111-2222-3333-4444-555555555555")
		if err != nil || len(vars) == 0 {
			t.Fatalf("historyEnv(%s) = %v, %v", shell, vars, err)
		}
		for _, kv := range vars {
			names[strings.SplitN(kv, "=", 2)[0]] = true
		}
	}
	for n := range names {
		if !envfile.IsReserved(n) {
			t.Errorf("%s is set by the application but not in envfile.ReservedNames", n)
		}
	}
}

// ---- SSH ----

func unquoteExec(line string) string {
	line = strings.TrimPrefix(line, "sh -c ")
	line = strings.TrimSuffix(strings.TrimPrefix(line, "'"), "'")
	return strings.ReplaceAll(line, `'\''`, "'")
}

type funcResolver func() sshpty.Target

func (f funcResolver) Resolve(userID, hostID string) (sshpty.Target, string, error) {
	return f(), "host", nil
}

func TestCreateSSHExportsAndDotenv(t *testing.T) {
	mgr, store, _ := testManager(t)
	srv := newStartDirServer(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FROM_FILE=f\nOVR=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, warns, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), dir, CreateOptions{
		DotEnv: true,
		Env: []EnvVar{
			{Name: "OVR", Value: "dialog"},
			{Name: "TOK", Value: envSentinel, Secret: true},
		},
	})
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warnings=%q", err, warns)
	}
	line := unquoteExec(srv.lastExec())
	for _, want := range []string{"export FROM_FILE='f'", "export OVR='dialog'", "export TOK='" + envSentinel + "'"} {
		if !strings.Contains(line, want) {
			t.Errorf("exec line lacks %q: %s", want, line)
		}
	}
	if !strings.HasPrefix(line, "cd -- ") || strings.Index(line, "cd --") > strings.Index(line, "export") {
		t.Errorf("cd must come first: %s", line)
	}
	// .env values are never persisted; the session's own entries are.
	row, _, _ := store.Get(info.ID)
	if !row.DotEnv || len(row.Env) != 2 {
		t.Errorf("stored = %+v", row)
	}

	// Restart re-reads the file.
	if err := mgr.WriteInput(info.ID, []byte("QUIT\n")); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, mgr, info.ID, StatusStopped)
	mgr.SetHostResolver(fixedResolver{srv.target()})
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FROM_FILE=changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mgr.Restart(info.ID, "test-user"); err != nil {
		t.Fatal(err)
	}
	if line := unquoteExec(srv.lastExec()); !strings.Contains(line, "export FROM_FILE='changed'") {
		t.Errorf("restart did not re-read .env: %s", line)
	}
}

func TestSSHDotenvWarningsAreReturned(t *testing.T) {
	mgr, _, _ := testManager(t)
	srv := newStartDirServer(t)
	_, warns, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), t.TempDir(), CreateOptions{DotEnv: true})
	if err != nil || !reflect.DeepEqual(warns, []string{"no .env file found"}) {
		t.Fatalf("err=%v warnings=%q", err, warns)
	}
}

func TestCreateSSHUnsupportedTarget(t *testing.T) {
	mgr, store, _ := testManager(t)
	srv := newStartDirServer(t)
	for _, mod := range []func(*sshpty.Target){
		func(tg *sshpty.Target) { tg.TargetOS = "windows" },
		func(tg *sshpty.Target) { tg.TerminalType = "powershell" },
		func(tg *sshpty.Target) { tg.TerminalType = "cmd" },
	} {
		for _, opts := range []CreateOptions{
			{Env: []EnvVar{{Name: "A", Value: envSentinel, Secret: true}}},
			{DotEnv: true},
		} {
			tg := srv.target()
			mod(&tg)
			_, _, err := mgr.CreateSSH("test-user", "s", "", "h", "host", tg, "", opts)
			if !errors.Is(err, ErrEnvUnsupported) {
				t.Errorf("err = %v, want ErrEnvUnsupported", err)
			}
		}
	}
	if len(store.rows) != 0 || srv.lastExec() != "" {
		t.Errorf("a session was created: rows=%d exec=%q", len(store.rows), srv.lastExec())
	}
	// Without env a Windows-flagged target is unaffected.
	tg := srv.target()
	tg.TargetOS = "windows"
	if _, _, err := mgr.CreateSSH("test-user", "s", "", "h", "host", tg, "", CreateOptions{}); err != nil {
		t.Errorf("plain create on windows target: %v", err)
	}
}

func TestRestartSSHRechecksEnvSupport(t *testing.T) {
	mgr, store, _ := testManager(t)
	srv := newStartDirServer(t)
	info, _, err := mgr.CreateSSH("test-user", "s", "", "h1", "host", srv.target(), "", CreateOptions{
		Env: []EnvVar{{Name: "A", Value: envSentinel, Secret: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.WriteInput(info.ID, []byte("QUIT\n")); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, mgr, info.ID, StatusStopped)

	// The host was edited to Windows in the meantime.
	win := srv.target()
	win.TargetOS = "windows"
	mgr.SetHostResolver(fixedResolver{win})
	before := srv.lastExec()
	_, _, err = mgr.Restart(info.ID, "test-user")
	if !errors.Is(err, ErrEnvUnsupported) {
		t.Fatalf("err = %v, want ErrEnvUnsupported", err)
	}
	if strings.Contains(err.Error(), envSentinel) {
		t.Errorf("error leaks a value: %v", err)
	}
	if srv.lastExec() != before {
		t.Error("a shell was started despite the failed re-check")
	}
	got, err := mgr.Get(info.ID, "test-user")
	if err != nil || got.Status != StatusStopped {
		t.Errorf("after refused restart: %+v err=%v", got, err)
	}
	if row, _, _ := store.Get(info.ID); row.Status != StatusStopped || len(row.Env) != 1 {
		t.Errorf("stored row changed: %+v", row)
	}
	// And the claim is released: restarting on a supporting host works.
	mgr.SetHostResolver(funcResolver(srv.target))
	if _, _, err := mgr.Restart(info.ID, "test-user"); err != nil {
		t.Errorf("restart after the host recovered: %v", err)
	}
}
