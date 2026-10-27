package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/auth"
	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/envfile"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/serverconfig"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/sshpty"
	"github.com/Andste82/sessile/backend/internal/storage"
)

// secretSentinel is the value every test below treats as a secret: if it ever
// shows up in a response, an event, a log line or an error, a leak is found.
const secretSentinel = "SENTINEL-s3cr3t-value-9f2c"

// syncBuf is a log sink safe for the concurrent writers slog has.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type envHarness struct {
	t       *testing.T
	srv     *Server
	router  *gin.Engine
	mgr     *session.Manager
	reg     *hosts.Registry
	logs    *syncBuf
	dbPath  string
	dataDir string
	users   map[string]auth.User
	tokens  map[string]string
}

// newEnvHarness builds a server over a real Manager and a real SQLite store,
// with users "alice" (admin) and "bob".
func newEnvHarness(t *testing.T) *envHarness {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	dataDir := t.TempDir()
	root := t.TempDir()
	logs := &syncBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	dbPath := filepath.Join(dataDir, "sessions.db")
	st, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	userStore, err := auth.OpenUsers(filepath.Join(dataDir, "users.yml"))
	if err != nil {
		t.Fatal(err)
	}
	webSessions := auth.NewSessionStore(time.Hour)
	t.Cleanup(webSessions.Stop)
	h := &envHarness{
		t: t, logs: logs, dbPath: dbPath, dataDir: dataDir,
		users: map[string]auth.User{}, tokens: map[string]string{},
	}
	for name, admin := range map[string]bool{"alice": true, "bob": false} {
		u, err := userStore.Create(name, "password-"+name, admin)
		if err != nil {
			t.Fatal(err)
		}
		h.users[name] = u
		h.tokens[name] = webSessions.Create(u.ID)
	}

	serverCfg, err := serverconfig.Open(filepath.Join(dataDir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := serverCfg.Set(serverconfig.Config{AllowLocalHost: true}); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Shells: []string{"sh"}, BufferSize: 64 << 10}
	h.mgr = session.NewManager(root, cfg.Shells, cfg.BufferSize, dataDir, st, log)
	t.Cleanup(h.mgr.Shutdown)
	h.reg = hosts.NewRegistry(dataDir)
	h.srv = NewServer(cfg, h.mgr, nil, log, root, serverCfg, userStore, webSessions, h.reg)
	t.Cleanup(h.srv.Close)
	h.router = h.srv.Router(fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}})
	return h
}

func (h *envHarness) do(method, target, user, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: h.tokens[user]})
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *envHarness) createLocal(user, body string) *httptest.ResponseRecorder {
	return h.do(http.MethodPost, "/api/sessions", user, body)
}

// stop makes the session's shell exit and waits for it to be stopped.
func (h *envHarness) stop(user, id string) {
	h.t.Helper()
	if err := h.mgr.WriteInput(id, []byte("exit\n")); err != nil {
		h.t.Fatal(err)
	}
	uid := h.users[user].ID
	eventually(h.t, "session stopped", func() bool {
		info, err := h.mgr.Get(id, uid)
		return err == nil && info.Status == session.StatusStopped
	})
}

type envSessionResp struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	LoadDotenv bool   `json:"loadDotenv"`
	Env        []struct {
		Name      string  `json:"name"`
		Value     *string `json:"value"`
		Secret    bool    `json:"secret"`
		IsSet     bool    `json:"isSet"`
		Inherited bool    `json:"inherited"`
	} `json:"env"`
	Warnings *[]string `json:"warnings"`
}

func assertNoSentinel(t *testing.T, what, s string) {
	t.Helper()
	if strings.Contains(s, secretSentinel) {
		t.Errorf("%s leaks the secret value: %s", what, s)
	}
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A secret value never leaves through create, list, get, update, restart, the
// live events, or a /ws/events snapshot; a non-secret one does.
func TestEnvSecretNeverInResponsesOrEvents(t *testing.T) {
	h := newEnvHarness(t)

	sub := &captureSub{}
	unsub := h.mgr.Subscribe(sub, h.users["alice"].ID)
	defer unsub()

	rec := h.createLocal("alice", jsonBody(t, map[string]any{
		"name": "s1", "directory": ".", "shell": "sh", "loadDotenv": true,
		"env": []map[string]any{
			{"name": "PLAIN_VAR", "value": "visible", "secret": false},
			{"name": "API_TOKEN", "value": secretSentinel, "secret": true},
		},
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	assertNoSentinel(t, "create response", rec.Body.String())
	created := decode[envSessionResp](t, rec)
	if created.Warnings == nil {
		t.Error("create response has no warnings array")
	}
	if !created.LoadDotenv || len(created.Env) != 2 {
		t.Fatalf("created = %+v", created)
	}
	for _, e := range created.Env {
		switch e.Name {
		case "PLAIN_VAR":
			if e.Value == nil || *e.Value != "visible" || e.Secret || !e.IsSet || e.Inherited {
				t.Errorf("PLAIN_VAR = %+v", e)
			}
		case "API_TOKEN":
			if e.Value != nil || !e.Secret || !e.IsSet {
				t.Errorf("API_TOKEN = %+v", e)
			}
		}
	}
	if !strings.Contains(rec.Body.String(), `"env":[`) {
		t.Errorf("env is not an array in %s", rec.Body)
	}
	id := created.ID

	for _, target := range []string{"/api/sessions", "/api/sessions/" + id} {
		r := h.do(http.MethodGet, target, "alice", "")
		assertNoSentinel(t, "GET "+target, r.Body.String())
		if strings.Contains(r.Body.String(), `"warnings"`) {
			t.Errorf("GET %s carries warnings", target)
		}
	}

	// Rename while running: env is not touched, still no secret in the reply.
	r := h.do(http.MethodPatch, "/api/sessions/"+id, "alice", `{"name":"renamed"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", r.Code, r.Body)
	}
	assertNoSentinel(t, "rename response", r.Body.String())

	h.stop("alice", id)

	// Edit the environment while stopped; the secret keeps its value though
	// the request does not carry it.
	r = h.do(http.MethodPatch, "/api/sessions/"+id, "alice", jsonBody(t, map[string]any{
		"env": []map[string]any{
			{"name": "PLAIN_VAR", "value": "changed", "secret": false},
			{"name": "API_TOKEN", "secret": true},
		},
	}))
	if r.Code != http.StatusOK {
		t.Fatalf("update env = %d %s", r.Code, r.Body)
	}
	assertNoSentinel(t, "update response", r.Body.String())
	if strings.Contains(r.Body.String(), `"warnings"`) {
		t.Error("update response carries warnings")
	}
	info, err := h.mgr.Get(id, h.users["alice"].ID)
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, e := range info.Env {
		if e.Name == "API_TOKEN" && e.Secret && e.Value == secretSentinel {
			kept = true
		}
	}
	if !kept {
		t.Errorf("secret value was not kept by a nil-value update: %+v", info.Env)
	}

	r = h.do(http.MethodPost, "/api/sessions/"+id+"/restart", "alice", "")
	if r.Code != http.StatusOK {
		t.Fatalf("restart = %d %s", r.Code, r.Body)
	}
	assertNoSentinel(t, "restart response", r.Body.String())
	restarted := decode[envSessionResp](t, r)
	if restarted.Warnings == nil {
		t.Error("restart response has no warnings array")
	} else if len(*restarted.Warnings) != 1 || !strings.Contains((*restarted.Warnings)[0], "no .env file found") {
		t.Errorf("restart warnings = %v, want the missing-.env warning", *restarted.Warnings)
	}

	// Every event the subscriber got, plus a snapshot built the way
	// /ws/events builds it.
	msgs := sub.all()
	if len(msgs) == 0 {
		t.Fatal("subscriber received no events")
	}
	for _, m := range msgs {
		assertNoSentinel(t, "event", jsonBody(t, m))
	}
	infos, err := h.mgr.List(h.users["alice"].ID)
	if err != nil {
		t.Fatal(err)
	}
	snap := session.SessionsMsg{Type: "sessions"}
	for _, i := range infos {
		snap.Sessions = append(snap.Sessions, session.ToJSON(i))
	}
	snapJSON := jsonBody(t, snap)
	assertNoSentinel(t, "snapshot", snapJSON)
	if !strings.Contains(snapJSON, "changed") {
		t.Error("snapshot lacks the non-secret value")
	}
}

type captureSub struct {
	mu   sync.Mutex
	msgs []any
}

func (c *captureSub) SendControl(v any) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, v)
	return true
}
func (c *captureSub) Close(int, string) {}
func (c *captureSub) all() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]any(nil), c.msgs...)
}

// The request logger records method, path, status and duration - never a
// body - even for a request that fails validation.
func TestRequestLogDoesNotCaptureBodies(t *testing.T) {
	h := newEnvHarness(t)

	rec := h.createLocal("alice", jsonBody(t, map[string]any{
		"name": "ok", "directory": ".", "shell": "sh",
		"env": []map[string]any{{"name": "TOKEN", "value": secretSentinel, "secret": true}},
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	// Invalid: reserved name carrying the sentinel -> 400.
	rec = h.createLocal("alice", jsonBody(t, map[string]any{
		"name": "bad", "directory": ".", "shell": "sh",
		"env": []map[string]any{{"name": "TERM", "value": secretSentinel, "secret": true}},
	}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved name = %d %s, want 400", rec.Code, rec.Body)
	}
	assertNoSentinel(t, "error response", rec.Body.String())
	// Defaults endpoint, valid and invalid.
	h.do(http.MethodPut, "/api/env-defaults", "alice",
		jsonBody(t, map[string]any{"env": []map[string]any{{"name": "D", "value": secretSentinel, "secret": true}}}))
	h.do(http.MethodPut, "/api/env-defaults", "alice",
		jsonBody(t, map[string]any{"env": []map[string]any{{"name": "1bad", "value": secretSentinel, "secret": true}}}))

	if !strings.Contains(h.logs.String(), "POST") {
		t.Fatalf("no request was logged at all: %q", h.logs.String())
	}
	assertNoSentinel(t, "log output", h.logs.String())
}

func TestEnvDefaultsEndpoints(t *testing.T) {
	h := newEnvHarness(t)

	type resp struct {
		Env []struct {
			Name      string  `json:"name"`
			Value     *string `json:"value"`
			Secret    bool    `json:"secret"`
			IsSet     bool    `json:"isSet"`
			Inherited *bool   `json:"inherited"`
		} `json:"env"`
	}

	rec := h.do(http.MethodGet, "/api/env-defaults", "alice", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"env":[]}` {
		t.Fatalf("empty GET = %d %s", rec.Code, rec.Body)
	}
	if h.do(http.MethodGet, "/api/env-defaults", "", "").Code != http.StatusUnauthorized {
		t.Error("unauthenticated GET was not refused")
	}

	rec = h.do(http.MethodPut, "/api/env-defaults", "alice", jsonBody(t, map[string]any{
		"env": []map[string]any{
			{"name": "EDITOR", "value": "vim"},
			{"name": "TOKEN", "value": secretSentinel, "secret": true},
		},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	assertNoSentinel(t, "PUT response", rec.Body.String())
	got := decode[resp](t, rec)
	if len(got.Env) != 2 || got.Env[0].Value == nil || *got.Env[0].Value != "vim" ||
		got.Env[1].Value != nil || !got.Env[1].Secret || !got.Env[1].IsSet {
		t.Errorf("PUT body = %+v", got)
	}
	for _, e := range got.Env {
		if e.Inherited != nil {
			t.Errorf("defaults entry %s carries inherited", e.Name)
		}
	}
	rec = h.do(http.MethodGet, "/api/env-defaults", "alice", "")
	assertNoSentinel(t, "GET response", rec.Body.String())

	// nil value on a secret keeps it; names missing from the list are deleted.
	rec = h.do(http.MethodPut, "/api/env-defaults", "alice", jsonBody(t, map[string]any{
		"env": []map[string]any{{"name": "TOKEN", "secret": true}},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("keep PUT = %d %s", rec.Code, rec.Body)
	}
	store, _ := h.reg.EnvFor(h.users["alice"].ID)
	stored, _ := store.Load()
	if len(stored) != 1 || stored[0].Name != "TOKEN" || stored[0].Value != secretSentinel || !stored[0].Secret {
		t.Errorf("stored after keep = %+v", stored)
	}

	// Isolation: bob sees nothing of alice's file.
	rec = h.do(http.MethodGet, "/api/env-defaults", "bob", "")
	if strings.TrimSpace(rec.Body.String()) != `{"env":[]}` {
		t.Errorf("bob's defaults = %s", rec.Body)
	}

	bad := map[string]string{
		"reserved":         `{"env":[{"name":"TERM","value":"x","secret":false}]}`,
		"duplicate":        `{"env":[{"name":"A","value":"1"},{"name":"A","value":"2"}]}`,
		"bad name":         `{"env":[{"name":"1A","value":"1"}]}`,
		"nil non-secret":   `{"env":[{"name":"A","secret":false}]}`,
		"nil, no existing": `{"env":[{"name":"NEW","secret":true}]}`,
		"NUL":              `{"env":[{"name":"A","value":"a\u0000b"}]}`,
		"too long value":   `{"env":[{"name":"A","value":"` + strings.Repeat("x", envfile.MaxValueLen+1) + `"}]}`,
		"missing env":      `{}`,
		"not json":         `nope`,
	}
	for name, body := range bad {
		rec := h.do(http.MethodPut, "/api/env-defaults", "alice", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d %s, want 400", name, rec.Code, rec.Body)
			continue
		}
		if e := decode[errJSON](t, rec); e.Error.Code != CodeValidation {
			t.Errorf("%s: code = %q", name, e.Error.Code)
		}
	}
	// A rejected PUT changed nothing.
	stored, _ = store.Load()
	if len(stored) != 1 || stored[0].Value != secretSentinel {
		t.Errorf("a rejected PUT altered the defaults: %+v", stored)
	}

	// Empty list clears everything.
	rec = h.do(http.MethodPut, "/api/env-defaults", "alice", `{"env":[]}`)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"env":[]}` {
		t.Errorf("clear = %d %s", rec.Code, rec.Body)
	}
}

// Defaults are merged in at create time, only as deviations, and can be
// overridden or dropped per session.
func TestCreateInheritsDefaults(t *testing.T) {
	h := newEnvHarness(t)
	store, _ := h.reg.EnvFor(h.users["alice"].ID)
	if err := store.Save([]envfile.Entry{
		{Name: "A", Value: "1"},
		{Name: "B", Value: "2"},
		{Name: "C_SECRET", Value: secretSentinel, Secret: true},
		{Name: "D", Value: "4"},
	}); err != nil {
		t.Fatal(err)
	}

	byName := func(r envSessionResp) map[string]struct {
		val       string
		hasVal    bool
		inherited bool
		secret    bool
	} {
		m := map[string]struct {
			val       string
			hasVal    bool
			inherited bool
			secret    bool
		}{}
		for _, e := range r.Env {
			v := m[e.Name]
			if e.Value != nil {
				v.val, v.hasVal = *e.Value, true
			}
			v.inherited, v.secret = e.Inherited, e.Secret
			m[e.Name] = v
		}
		return m
	}

	// Nothing sent: every default arrives, flagged inherited.
	rec := h.createLocal("alice", `{"name":"plain","directory":".","shell":"sh"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	assertNoSentinel(t, "create", rec.Body.String())
	m := byName(decode[envSessionResp](t, rec))
	if len(m) != 4 || !m["A"].inherited || !m["C_SECRET"].inherited || !m["C_SECRET"].secret || m["C_SECRET"].hasVal {
		t.Errorf("inherited env = %+v", m)
	}

	// Override B, drop A (and an unknown name, which is ignored), add E.
	rec = h.createLocal("alice", jsonBody(t, map[string]any{
		"name": "mixed", "directory": ".", "shell": "sh",
		"removeDefaults": []string{"A", "NOPE"},
		"env": []map[string]any{
			{"name": "B", "value": "override"},
			{"name": "E", "value": "5"},
		},
	}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	m = byName(decode[envSessionResp](t, rec))
	if _, ok := m["A"]; ok {
		t.Error("removed default A is still there")
	}
	if m["B"].val != "override" || m["B"].inherited {
		t.Errorf("B = %+v, want an own entry with the new value", m["B"])
	}
	if !m["D"].inherited || m["E"].inherited || m["E"].val != "5" || len(m) != 4 {
		t.Errorf("env = %+v", m)
	}

	// Another user's defaults are never inherited.
	rec = h.createLocal("bob", `{"name":"bobs","directory":".","shell":"sh"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if got := decode[envSessionResp](t, rec); len(got.Env) != 0 {
		t.Errorf("bob inherited %+v", got.Env)
	}

	// Validation: a missing value, a reserved name, a duplicate.
	for name, env := range map[string]string{
		"missing value": `[{"name":"X","secret":true}]`,
		"reserved":      `[{"name":"LANG","value":"x"}]`,
		"duplicate":     `[{"name":"X","value":"1"},{"name":"X","value":"2"}]`,
		"bad name":      `[{"name":"a-b","value":"1"}]`,
	} {
		rec := h.createLocal("alice", `{"name":"v","directory":".","shell":"sh","env":`+env+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: create = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
}

// For a host that cannot take variables, explicit entries are refused and
// defaults are not inherited.
func TestCreateEnvOnUnsupportedHost(t *testing.T) {
	h := newEnvHarness(t)
	alice := h.users["alice"].ID
	store, _ := h.reg.EnvFor(alice)
	if err := store.Save([]envfile.Entry{{Name: "A", Value: "1"}}); err != nil {
		t.Fatal(err)
	}
	hostStore, _ := h.reg.For(alice)
	win, err := hostStore.Create(hosts.Host{
		Name: "win", Address: "127.0.0.1:1", Username: "u", AuthMethod: hosts.AuthPassword,
		Password: "p", TargetOS: hosts.OSWindows, TerminalType: "powershell",
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, extra := range map[string]string{
		"env":        `,"env":[{"name":"X","value":"1"}]`,
		"loadDotenv": `,"loadDotenv":true`,
	} {
		rec := h.createLocal("alice", `{"name":"w","target":"ssh","hostId":"`+win.ID+`"`+extra+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: create = %d %s, want 400", name, rec.Code, rec.Body)
		}
		if e := decode[errJSON](t, rec); e.Error.Code != CodeValidation || e.Error.Message != errEnvUnsupportedHost {
			t.Errorf("%s: error = %+v", name, e.Error)
		}
	}

	// Defaults are skipped (not inherited) for the unsupported host.
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
	opts, ok := h.srv.createEnvOptions(ctx, alice, createSessionBody{}, false)
	if !ok || len(opts.Env) != 0 || opts.DotEnv {
		t.Errorf("unsupported host options = %+v ok=%v, want none", opts, ok)
	}
	opts, ok = h.srv.createEnvOptions(ctx, alice, createSessionBody{}, true)
	if !ok || len(opts.Env) != 1 || !opts.Env[0].Default {
		t.Errorf("supported host options = %+v ok=%v, want the inherited default", opts, ok)
	}
}

func TestUpdateEnvRequiresStopped(t *testing.T) {
	h := newEnvHarness(t)
	rec := h.createLocal("alice", `{"name":"u","directory":".","shell":"sh"}`)
	id := decode[envSessionResp](t, rec).ID

	for _, body := range []string{
		`{"env":[{"name":"A","value":"1"}]}`,
		`{"loadDotenv":true}`,
	} {
		r := h.do(http.MethodPatch, "/api/sessions/"+id, "alice", body)
		if r.Code != http.StatusConflict {
			t.Fatalf("update %s on a running session = %d %s, want 409", body, r.Code, r.Body)
		}
		if e := decode[errJSON](t, r); e.Error.Code != CodeConflict {
			t.Errorf("code = %q", e.Error.Code)
		}
	}
	// Name/group-only updates keep working while running.
	if r := h.do(http.MethodPatch, "/api/sessions/"+id, "alice", `{"name":"n2","group":"g"}`); r.Code != http.StatusOK {
		t.Errorf("rename = %d %s", r.Code, r.Body)
	}

	h.stop("alice", id)
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"ok":             {`{"env":[{"name":"A","value":"1"}],"loadDotenv":true}`, http.StatusOK},
		"reserved":       {`{"env":[{"name":"HISTFILE","value":"x"}]}`, http.StatusBadRequest},
		"nil non-secret": {`{"env":[{"name":"A"}]}`, http.StatusBadRequest},
		"rename w/o val": {`{"env":[{"name":"B","secret":true}]}`, http.StatusBadRequest},
	} {
		r := h.do(http.MethodPatch, "/api/sessions/"+id, "alice", tc.body)
		if r.Code != tc.want {
			t.Errorf("%s: update = %d %s, want %d", name, r.Code, r.Body, tc.want)
		}
	}
	// Another user cannot edit it.
	if r := h.do(http.MethodPatch, "/api/sessions/"+id, "bob", `{"env":[]}`); r.Code != http.StatusNotFound {
		t.Errorf("foreign update = %d, want 404", r.Code)
	}
}

func TestHostJSONSupportsEnv(t *testing.T) {
	h := newEnvHarness(t)
	store, _ := h.reg.For(h.users["alice"].ID)
	cases := []struct {
		name         string
		os           hosts.TargetOS
		terminalType string
		want         bool
	}{
		{"linux", hosts.OSLinux, "bash", true},
		{"empty-os", "", "bash", true},
		{"darwin", hosts.OSDarwin, "zsh", true},
		{"windows", hosts.OSWindows, "custom", false},
		{"cmd", hosts.OSLinux, "cmd", false},
		{"powershell", "", "powershell", false},
	}
	for _, c := range cases {
		if _, err := store.Create(hosts.Host{
			Name: c.name, Address: "a:22", Username: "u", AuthMethod: hosts.AuthPassword,
			TargetOS: c.os, TerminalType: c.terminalType,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec := h.do(http.MethodGet, "/api/hosts", "alice", "")
	var list []struct {
		Name        string `json:"name"`
		SupportsEnv *bool  `json:"supportsEnv"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range list {
		if e.SupportsEnv == nil {
			t.Fatalf("host %s has no supportsEnv", e.Name)
		}
		got[e.Name] = *e.SupportsEnv
	}
	for _, c := range cases {
		if got[c.name] != c.want {
			t.Errorf("%s: supportsEnv = %v, want %v", c.name, got[c.name], c.want)
		}
	}
}

// Deleting a user removes env.yml with the rest of users/<id>, drops the
// session rows (and with them the stored secrets), and evicts the cached
// store so a later lookup does not bring the directory back.
func TestDeleteUserRemovesEnvData(t *testing.T) {
	h := newEnvHarness(t)
	bob := h.users["bob"].ID

	if r := h.do(http.MethodPut, "/api/env-defaults", "bob", jsonBody(t, map[string]any{
		"env": []map[string]any{{"name": "T", "value": secretSentinel, "secret": true}},
	})); r.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", r.Code, r.Body)
	}
	rec := h.createLocal("bob", `{"name":"b","directory":".","shell":"sh"}`)
	id := decode[envSessionResp](t, rec).ID
	h.stop("bob", id)

	envPath := filepath.Join(filepath.Dir(h.dbPath), "users", bob, "env.yml")
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("env.yml not written: %v", err)
	}
	cached, _ := h.reg.EnvFor(bob)

	if r := h.do(http.MethodDelete, "/api/admin/users/"+bob, "alice", ""); r.Code != http.StatusNoContent {
		t.Fatalf("delete user = %d %s", r.Code, r.Body)
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(h.dbPath), "users", bob)); !os.IsNotExist(err) {
		t.Errorf("users/<id> still exists: %v", err)
	}
	db, err := sql.Open("sqlite", h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE user_id=?`, bob).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d session rows remain for the deleted user", n)
	}
	again, err := h.reg.EnvFor(bob)
	if err != nil {
		t.Fatal(err)
	}
	if again == cached {
		t.Error("env store cache entry survived the deletion")
	}
	if got, _ := again.Load(); len(got) != 0 {
		t.Errorf("deleted user's defaults came back: %+v", got)
	}
	if _, err := os.Stat(filepath.Dir(envPath)); !os.IsNotExist(err) {
		t.Error("EnvFor recreated the deleted directory")
	}
}

func TestRespondSessionErrorMapsEnvErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"invalid env", fmt.Errorf(`environment variable "TERM" is reserved: %w`, session.ErrInvalidEnv), 400, CodeValidation},
		{"envfile invalid", fmt.Errorf(`duplicate "A": %w`, envfile.ErrInvalid), 400, CodeValidation},
		{"unsupported", fmt.Errorf("restart: %w", session.ErrEnvUnsupported), 400, CodeValidation},
		{"sshpty unsupported", fmt.Errorf("start: %w", sshpty.ErrEnvUnsupported), 400, CodeValidation},
		{"sshpty too large", fmt.Errorf("start: %w", sshpty.ErrEnvTooLarge), 400, CodeValidation},
		{"running", session.ErrEnvRunning, 409, CodeConflict},
		{"other", errors.New("boom"), 400, CodeValidation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			(&Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}).respondSessionError(ctx, c.err)
			if rec.Code != c.wantCode {
				t.Fatalf("status = %d, want %d", rec.Code, c.wantCode)
			}
			var body errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != c.wantBody {
				t.Errorf("code = %q, want %q", body.Error.Code, c.wantBody)
			}
		})
	}
}
