package hosts

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/envfile"
)

func TestEnvStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	s, err := r.EnvFor("u1")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(); len(got) != 0 {
		t.Fatalf("fresh store = %+v, want empty", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "u1")); !os.IsNotExist(err) {
		t.Errorf("EnvFor created the user dir (stat err = %v)", err)
	}

	in := []envfile.Entry{
		{Name: "A", Value: "1"},
		{Name: "TOKEN", Value: "multi\nline 'x'", Secret: true},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, "users", "u1", "env.yml")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("env.yml mode = %v, want 0600", st.Mode().Perm())
	}

	// A fresh registry reads it back from disk.
	s2, err := NewRegistry(dir).EnvFor("u1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s2.Load()
	if !reflect.DeepEqual(got, in) {
		t.Errorf("reloaded = %+v, want %+v", got, in)
	}
}

func TestEnvStoreSaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewRegistry(dir).EnvFor("u1")
	for name, in := range map[string][]envfile.Entry{
		"reserved":  {{Name: "TERM", Value: "x"}},
		"duplicate": {{Name: "A", Value: "1"}, {Name: "A", Value: "2"}},
		"bad name":  {{Name: "1A", Value: "1"}},
		"NUL":       {{Name: "A", Value: "a\x00b"}},
	} {
		err := s.Save(in)
		if !errors.Is(err, envfile.ErrInvalid) {
			t.Errorf("%s: Save error = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "u1", "env.yml")); !os.IsNotExist(err) {
		t.Error("an invalid Save wrote a file")
	}
}

func TestEnvStoreIsolatedPerUser(t *testing.T) {
	r := NewRegistry(t.TempDir())
	a, _ := r.EnvFor("a")
	b, _ := r.EnvFor("b")
	if err := a.Save([]envfile.Entry{{Name: "ONLY_A", Value: "x", Secret: true}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Load(); len(got) != 0 {
		t.Errorf("user b sees %+v", got)
	}
}

func TestEnvStoreCorruptFileErrorHasNoContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "users", "u1")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "env.yml"), []byte("env: [{name: A, value: sentinel-secret\n  : :"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewRegistry(dir).EnvFor("u1")
	if err == nil {
		t.Fatal("want error for a corrupt file")
	}
	if strings.Contains(err.Error(), "sentinel-secret") {
		t.Errorf("error leaks file content: %v", err)
	}
}

func TestRegistryRemoveEvictsEnvStore(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)
	s, _ := r.EnvFor("u1")
	if err := s.Save([]envfile.Entry{{Name: "A", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.For("u1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "u1")); !os.IsNotExist(err) {
		t.Fatalf("user dir still there: %v", err)
	}
	// The cache entry is gone: EnvFor opens a fresh, empty store and does not
	// resurrect the deleted file or recreate the directory.
	s2, err := r.EnvFor("u1")
	if err != nil {
		t.Fatal(err)
	}
	if s2 == s {
		t.Error("env store still cached after Remove")
	}
	if got, _ := s2.Load(); len(got) != 0 {
		t.Errorf("deleted user's defaults came back: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "u1")); !os.IsNotExist(err) {
		t.Error("EnvFor recreated the removed directory")
	}
}

func TestHostSupportsEnv(t *testing.T) {
	cases := []struct {
		name string
		h    Host
		want bool
	}{
		{"linux bash", Host{TargetOS: OSLinux, TerminalType: "bash"}, true},
		{"empty os", Host{TerminalType: "bash"}, true},
		{"empty everything", Host{}, true},
		{"darwin zsh", Host{TargetOS: OSDarwin, TerminalType: "zsh"}, true},
		{"windows os", Host{TargetOS: OSWindows, TerminalType: "custom"}, false},
		{"cmd", Host{TargetOS: OSLinux, TerminalType: "cmd"}, false},
		{"powershell", Host{TerminalType: "powershell"}, false},
	}
	for _, c := range cases {
		if got := c.h.SupportsEnv(); got != c.want {
			t.Errorf("%s: SupportsEnv = %v, want %v", c.name, got, c.want)
		}
	}
}
