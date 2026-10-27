package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A hand-edited env.yml that fails validation is named as the cause on the
// defaults endpoints and on create, instead of surfacing as a confusing
// validation error about entries the caller never sent.
func TestInvalidEnvFileIsNamedAsCause(t *testing.T) {
	h := newEnvHarness(t)
	dir := filepath.Join(h.dataDir, "users", h.users["alice"].ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := "env:\n  - {name: A, value: '" + secretSentinel + "'}\n  - {name: A, value: '2'}\n"
	if err := os.WriteFile(filepath.Join(dir, "env.yml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	check := func(what string, code int, body string) {
		t.Helper()
		if code != http.StatusInternalServerError || !strings.Contains(body, "env.yml is invalid") {
			t.Errorf("%s = %d %s, want 500 naming env.yml", what, code, body)
		}
		assertNoSentinel(t, what, body)
	}
	rec := h.do(http.MethodGet, "/api/env-defaults", "alice", "")
	check("GET", rec.Code, rec.Body.String())
	rec = h.do(http.MethodPut, "/api/env-defaults", "alice", `{"env":[]}`)
	check("PUT", rec.Code, rec.Body.String())
	rec = h.createLocal("alice", `{"shell":"sh"}`)
	check("create", rec.Code, rec.Body.String())

	// Another user is unaffected.
	if rec := h.do(http.MethodGet, "/api/env-defaults", "bob", ""); rec.Code != http.StatusOK {
		t.Errorf("bob GET = %d", rec.Code)
	}
}

// Concurrent PUTs must not lose each other: a secret kept by "no value" is
// resolved against the state at write time.
func TestConcurrentPutEnvDefaultsKeepsSecret(t *testing.T) {
	h := newEnvHarness(t)
	rec := h.do(http.MethodPut, "/api/env-defaults", "alice",
		jsonBody(t, map[string]any{"env": []map[string]any{{"name": "TOKEN", "value": secretSentinel, "secret": true}}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed = %d %s", rec.Code, rec.Body)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := h.do(http.MethodPut, "/api/env-defaults", "alice",
				`{"env":[{"name":"TOKEN","secret":true}]}`)
			if r.Code != http.StatusOK {
				t.Errorf("PUT = %d %s", r.Code, r.Body)
			}
		}()
	}
	wg.Wait()
	store, err := h.reg.EnvFor(h.users["alice"].ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := store.Load()
	if len(got) != 1 || got[0].Value != secretSentinel {
		t.Errorf("secret lost: %+v", got)
	}
}
