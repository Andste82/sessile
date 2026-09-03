package auth

import (
	"testing"
	"time"
)

func TestSessionStoreCreateAndLookup(t *testing.T) {
	s := NewSessionStore(time.Hour)
	t.Cleanup(s.Stop)

	token := s.Create("user-1")
	if token == "" {
		t.Fatal("Create returned an empty token")
	}
	userID, ok := s.Lookup(token)
	if !ok {
		t.Fatal("Lookup(fresh token) ok = false")
	}
	if userID != "user-1" {
		t.Errorf("Lookup returned %q, want %q", userID, "user-1")
	}
}

func TestSessionStoreLookupUnknownToken(t *testing.T) {
	s := NewSessionStore(time.Hour)
	t.Cleanup(s.Stop)

	if _, ok := s.Lookup("no-such-token"); ok {
		t.Error("Lookup(unknown token) ok = true, want false")
	}
}

// The defining behavior: every successful Lookup renews the expiry, so an
// actively-used session never lapses.
func TestSessionStoreLookupSlidesExpiry(t *testing.T) {
	s := NewSessionStore(50 * time.Millisecond)
	t.Cleanup(s.Stop)

	token := s.Create("user-1")

	// Keep "using" the token faster than it would expire on its own.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := s.Lookup(token); !ok {
			t.Fatal("token expired despite being looked up inside its TTL")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The inverse: an abandoned token does lapse once nobody presents it inside
// its TTL.
func TestSessionStoreTokenExpiresWhenUnused(t *testing.T) {
	s := NewSessionStore(30 * time.Millisecond)
	t.Cleanup(s.Stop)

	token := s.Create("user-1")
	time.Sleep(100 * time.Millisecond)

	if _, ok := s.Lookup(token); ok {
		t.Error("Lookup(expired token) ok = true, want false")
	}
}

func TestSessionStoreRevoke(t *testing.T) {
	s := NewSessionStore(time.Hour)
	t.Cleanup(s.Stop)

	token := s.Create("user-1")
	s.Revoke(token)

	if _, ok := s.Lookup(token); ok {
		t.Error("Lookup(revoked token) ok = true, want false")
	}
}

// Used when an admin deletes an account (§12b M11): every session that
// account had open stops working immediately, not just future logins.
func TestSessionStoreRevokeByUser(t *testing.T) {
	s := NewSessionStore(time.Hour)
	t.Cleanup(s.Stop)

	tokenA1 := s.Create("user-a")
	tokenA2 := s.Create("user-a")
	tokenB := s.Create("user-b")

	s.RevokeByUser("user-a")

	if _, ok := s.Lookup(tokenA1); ok {
		t.Error("user-a's first token survived RevokeByUser")
	}
	if _, ok := s.Lookup(tokenA2); ok {
		t.Error("user-a's second token survived RevokeByUser")
	}
	if _, ok := s.Lookup(tokenB); !ok {
		t.Error("user-b's token was revoked by user-a's RevokeByUser")
	}
}

// Two tokens for the same user must not collide.
func TestSessionStoreTokensAreUnique(t *testing.T) {
	s := NewSessionStore(time.Hour)
	t.Cleanup(s.Stop)

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		token := s.Create("user-1")
		if seen[token] {
			t.Fatalf("duplicate token generated: %q", token)
		}
		seen[token] = true
	}
}
