package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/hosts"
)

// A remote .env is read over the target host's connection, so the host lookup
// must stay scoped to the authenticated user: bob naming alice's host id (with
// or without Load .env) gets the same 404 as an unknown id and creates nothing.
func TestCreateSSHEnvScopedToAuthenticatedUsersHost(t *testing.T) {
	h := newEnvHarness(t)
	alice := h.users["alice"].ID
	hostStore, _ := h.reg.For(alice)
	host, err := hostStore.Create(hosts.Host{
		Name: "alices", Address: "127.0.0.1:1", Username: "u", AuthMethod: hosts.AuthPassword,
		Password: "p", TargetOS: hosts.OSLinux, TerminalType: "bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string]string{
		"dotenv": `,"loadDotenv":true`,
		"env":    `,"env":[{"name":"X","value":"1"}]`,
	} {
		foreign := h.createLocal("bob", `{"name":"x","target":"ssh","hostId":"`+host.ID+`"`+extra+`}`)
		unknown := h.createLocal("bob", `{"name":"x","target":"ssh","hostId":"does-not-exist"`+extra+`}`)
		if foreign.Code != http.StatusNotFound || foreign.Code != unknown.Code || foreign.Body.String() != unknown.Body.String() {
			t.Errorf("%s: foreign = %d %s, unknown = %d %s; want identical 404",
				name, foreign.Code, foreign.Body, unknown.Code, unknown.Body)
		}
	}
	if list := h.do(http.MethodGet, "/api/sessions", "bob", ""); strings.Contains(list.Body.String(), `"name":"x"`) {
		t.Errorf("a session was created for bob: %s", list.Body)
	}
}
