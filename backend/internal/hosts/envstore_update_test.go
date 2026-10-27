package hosts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Andste82/sessile/backend/internal/envfile"
)

func TestEnvStoreUpdateIsAtomic(t *testing.T) {
	s, err := NewRegistry(t.TempDir()).EnvFor("u1")
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Update(func(cur []envfile.Entry) ([]envfile.Entry, error) {
				return append(cur, envfile.Entry{Name: fmt.Sprintf("V%d", i), Value: "x"}), nil
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	got, _ := s.Load()
	if len(got) != n {
		t.Fatalf("lost updates: %d entries, want %d", len(got), n)
	}
}

func TestEnvStoreUpdateErrorWritesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := NewRegistry(dir).EnvFor("u1")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if _, err := s.Update(func([]envfile.Entry) ([]envfile.Entry, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// An invalid result is refused too.
	_, err = s.Update(func([]envfile.Entry) ([]envfile.Entry, error) {
		return []envfile.Entry{{Name: "TERM", Value: "x"}}, nil
	})
	if !errors.Is(err, envfile.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "users", "u1", "env.yml")); !os.IsNotExist(err) {
		t.Errorf("env.yml was written (stat err = %v)", err)
	}
}

func TestEnvStoreRejectsInvalidFileOnOpen(t *testing.T) {
	for name, content := range map[string]string{
		"duplicate": "env:\n  - {name: A, value: '1'}\n  - {name: A, value: '2'}\n",
		"reserved":  "env:\n  - {name: TERM, value: dumb}\n",
		"bad name":  "env:\n  - {name: '1X', value: v}\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "users", "u1"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "users", "u1", "env.yml"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewRegistry(dir).EnvFor("u1")
			if !errors.Is(err, envfile.ErrInvalid) || !strings.Contains(err.Error(), "env.yml is invalid") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
