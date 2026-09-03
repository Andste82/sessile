package hosts

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hosts.yml"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpenStartsEmpty(t *testing.T) {
	s := newTestStore(t)
	if n := len(s.List()); n != 0 {
		t.Errorf("List() length = %d, want 0", n)
	}
}

func TestCreateAssignsIDAndCreated(t *testing.T) {
	s := newTestStore(t)
	h, err := s.Create(Host{Name: "prod-db", Address: "db.example.com:22", Username: "deploy", AuthMethod: AuthPassword, Password: "hunter2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.ID == "" {
		t.Error("Create returned a host with no id")
	}
	if h.Created.IsZero() {
		t.Error("Create returned a host with no Created timestamp")
	}

	got, ok := s.Get(h.ID)
	if !ok {
		t.Fatal("Get after Create: not found")
	}
	if got.Name != "prod-db" || got.Password != "hunter2" {
		t.Errorf("Get returned %+v, want the created host back", got)
	}
}

func TestCreateIgnoresCallerSuppliedIDAndCreated(t *testing.T) {
	s := newTestStore(t)
	h, err := s.Create(Host{ID: "attacker-chosen-id", Name: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.ID == "attacker-chosen-id" {
		t.Error("Create honored a caller-supplied id")
	}
}

func TestUpdatePreservesIDAndCreated(t *testing.T) {
	s := newTestStore(t)
	h, err := s.Create(Host{Name: "original"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := s.Update(h.ID, Host{Name: "renamed", ID: "ignored", Password: "new-pw"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ID != h.ID {
		t.Errorf("Update changed the id: %q -> %q", h.ID, updated.ID)
	}
	if !updated.Created.Equal(h.Created) {
		t.Errorf("Update changed Created: %v -> %v", h.Created, updated.Created)
	}
	if updated.Name != "renamed" || updated.Password != "new-pw" {
		t.Errorf("Update did not apply the new fields: %+v", updated)
	}
}

func TestUpdateUnknownID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Update("no-such-id", Host{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestDeleteRemovesHost(t *testing.T) {
	s := newTestStore(t)
	h, err := s.Create(Host{Name: "to-delete"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Delete(h.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := s.Get(h.ID); ok {
		t.Error("host still present after Delete")
	}
}

func TestDeleteUnknownID(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("no-such-id"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(unknown) error = %v, want ErrNotFound", err)
	}
}

// Hosts survive a restart: a second Open against the same file sees what
// the first one wrote, credentials included (plaintext, by design — §11).
func TestHostsPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.yml")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h, err := s1.Create(Host{
		Name: "prod-db", Address: "db.example.com:22", Username: "deploy",
		AuthMethod: AuthPrivateKey, PrivateKey: "-----BEGIN...-----",
		TrustedHostKeyType: "ssh-ed25519", TrustedHostKeyFingerprint: "SHA256:abc",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	got, ok := s2.Get(h.ID)
	if !ok {
		t.Fatal("host missing after reopen")
	}
	if got.PrivateKey != h.PrivateKey || got.TrustedHostKeyFingerprint != h.TrustedHostKeyFingerprint {
		t.Errorf("reopened host = %+v, want it to match what was created", got)
	}
}

func TestListReturnsEveryHost(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(Host{Name: "a"}); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if _, err := s.Create(Host{Name: "b"}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if n := len(s.List()); n != 2 {
		t.Errorf("List() length = %d, want 2", n)
	}
}
