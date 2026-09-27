//go:build linux

package confine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The security property this whole design rests on (§4, E13): a confined
// agent can work in its own folder and cannot read sessile's data — which
// holds every user's host credentials and tokens.
//
// It runs in a child process, because confinement cannot be undone: applying
// it in the test process would confine the test runner itself.
func TestConfinedProcessCannotReachTheDataDir(t *testing.T) {
	if !Supported() {
		t.Skip("this kernel has no Landlock")
	}
	taskDir := t.TempDir()
	dataDir := t.TempDir()
	secret := filepath.Join(dataDir, "hosts.yml")
	if err := os.WriteFile(secret, []byte("password: hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := json.Marshal(Rules{
		ReadWrite: []string{taskDir, "/dev"},
		ReadOnly:  []string{"/usr", "/bin", "/lib", "/lib64", "/etc", "/proc"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestConfinedChild")
	cmd.Env = append(os.Environ(),
		"SESSILE_CONFINE_CHILD=1",
		"SESSILE_CONFINE_RULES="+string(rules),
		"SESSILE_CONFINE_TASKDIR="+taskDir,
		"SESSILE_CONFINE_SECRET="+secret,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	got := string(out)
	for _, want := range []string{
		"write inside its own folder: ok",
		"read its own folder: ok",
		"read the data dir: denied",
		"list the data dir: denied",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in the child's report:\n%s", want, got)
		}
	}
}

// TestConfinedChild is the child half of the test above. It is a no-op in a
// normal run.
func TestConfinedChild(t *testing.T) {
	if os.Getenv("SESSILE_CONFINE_CHILD") == "" {
		t.Skip("child half of TestConfinedProcessCannotReachTheDataDir")
	}
	runtime.LockOSThread()
	var rules Rules
	if err := json.Unmarshal([]byte(os.Getenv("SESSILE_CONFINE_RULES")), &rules); err != nil {
		t.Fatal(err)
	}
	if err := Apply(rules); err != nil {
		t.Fatal(err)
	}
	taskDir, secret := os.Getenv("SESSILE_CONFINE_TASKDIR"), os.Getenv("SESSILE_CONFINE_SECRET")

	mine := filepath.Join(taskDir, "notes.md")
	report(t, "write inside its own folder", os.WriteFile(mine, []byte("ok\n"), 0o600))
	_, err := os.ReadFile(mine)
	report(t, "read its own folder", err)
	_, err = os.ReadFile(secret)
	report(t, "read the data dir", err)
	_, err = os.ReadDir(filepath.Dir(secret))
	report(t, "list the data dir", err)
}

func report(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Logf("%s: ok", what)
		os.Stdout.WriteString(what + ": ok\n")
		return
	}
	os.Stdout.WriteString(what + ": denied\n")
}
