package auth

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *UserStore {
	t.Helper()
	s, err := OpenUsers(filepath.Join(t.TempDir(), "users.yml"))
	if err != nil {
		t.Fatalf("OpenUsers: %v", err)
	}
	return s
}

// A fresh store is the server's "unlocked" first-run state (§10, §11).
func TestOpenUsersStartsEmpty(t *testing.T) {
	s := newTestStore(t)
	if n := s.Count(); n != 0 {
		t.Errorf("Count() = %d, want 0", n)
	}
}

func TestCreateAndVerify(t *testing.T) {
	s := newTestStore(t)

	user, err := s.Create("admin", "correct horse battery", true)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.ID == "" {
		t.Error("Create returned a user with no id")
	}
	if user.PasswordHash == "correct horse battery" {
		t.Error("PasswordHash stored the plaintext password")
	}

	got, err := s.Verify("admin", "correct horse battery")
	if err != nil {
		t.Fatalf("Verify(correct): %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("Verify returned id %q, want %q", got.ID, user.ID)
	}

	if _, err := s.Verify("admin", "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Verify(wrong password) error = %v, want ErrInvalidCredentials", err)
	}
	if _, err := s.Verify("nobody", "correct horse battery"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("Verify(unknown user) error = %v, want ErrInvalidCredentials", err)
	}
}

// Usernames are unique case-insensitively, so "Admin" and "admin" can't end
// up as two accounts that look identical everywhere they're displayed.
func TestCreateRejectsDuplicateUsernameCaseInsensitive(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("Admin", "correct horse battery", true); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := s.Create("admin", "another password", false); !errors.Is(err, ErrUserExists) {
		t.Errorf("second Create error = %v, want ErrUserExists", err)
	}
}

func TestCreateValidatesInput(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("", "correct horse battery", false); err == nil {
		t.Error("Create with empty username succeeded, want an error")
	}
	if _, err := s.Create("short-pw", "short", false); err == nil {
		t.Error("Create with a short password succeeded, want an error")
	}
}

// Accounts survive a restart: a second OpenUsers against the same file sees
// what the first one wrote.
func TestUsersPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yml")

	s1, err := OpenUsers(path)
	if err != nil {
		t.Fatalf("OpenUsers: %v", err)
	}
	if _, err := s1.Create("admin", "correct horse battery", true); err != nil {
		t.Fatalf("Create: %v", err)
	}

	s2, err := OpenUsers(path)
	if err != nil {
		t.Fatalf("re-OpenUsers: %v", err)
	}
	if n := s2.Count(); n != 1 {
		t.Fatalf("reopened Count() = %d, want 1", n)
	}
	if _, err := s2.Verify("admin", "correct horse battery"); err != nil {
		t.Errorf("Verify after reopen: %v", err)
	}
}

// The admin panel must never be able to lock itself out.
func TestDeleteAndSetAdminGuardTheLastAdmin(t *testing.T) {
	s := newTestStore(t)
	admin, err := s.Create("admin", "correct horse battery", true)
	if err != nil {
		t.Fatalf("Create admin: %v", err)
	}

	if err := s.SetAdmin(admin.ID, false); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("SetAdmin(demote sole admin) error = %v, want ErrLastAdmin", err)
	}
	if err := s.Delete(admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("Delete(sole admin) error = %v, want ErrLastAdmin", err)
	}

	other, err := s.Create("other", "correct horse battery", true)
	if err != nil {
		t.Fatalf("Create second admin: %v", err)
	}

	// With two admins present, demoting one is fine — it leaves one behind.
	if err := s.SetAdmin(admin.ID, false); err != nil {
		t.Errorf("SetAdmin(demote) with a second admin present: %v", err)
	}
	// Now only "other" is an admin; deleting it hits the guard again.
	if err := s.Delete(other.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("Delete(now-sole admin) error = %v, want ErrLastAdmin", err)
	}
	// Re-promote admin so there are two again, and deleting one now succeeds.
	if err := s.SetAdmin(admin.ID, true); err != nil {
		t.Fatalf("SetAdmin(re-promote): %v", err)
	}
	if err := s.Delete(other.ID); err != nil {
		t.Errorf("Delete second admin with two present: %v", err)
	}
}

func TestDeleteUnknownUser(t *testing.T) {
	s := newTestStore(t)
	if err := s.Delete("no-such-id"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("Delete(unknown) error = %v, want ErrUserNotFound", err)
	}
}

func TestListReturnsEveryUser(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("a", "correct horse battery", true); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if _, err := s.Create("b", "correct horse battery", false); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if n := len(s.List()); n != 2 {
		t.Errorf("List() length = %d, want 2", n)
	}
}
