package agents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func validSettings() Settings {
	return Settings{
		Connections: []Connection{
			{ID: "c1", Name: "Bedrock", Kind: "claude-bedrock", Fields: map[string]string{"region": "eu-central-1", "token": "tok"}},
			{ID: "c2", Name: "Max", Kind: "claude-subscription", Fields: map[string]string{"token": "oat"}},
		},
		Profiles: []Profile{
			{ID: "p1", Name: "Claude (Bedrock)", Agent: AgentClaude, ConnectionID: "c1"},
		},
		TaskDefaults: TaskDefaults{ProfileID: "p1"},
		Git:          []GitAccount{{ID: "g1", Host: "github.com", Username: "me", Token: "ghp"}},
	}
}

func TestSettingsValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr string
	}{
		{"valid", func(*Settings) {}, ""},
		{"unknown kind", func(s *Settings) { s.Connections[0].Kind = "nope" }, "unknown kind"},
		{"missing required field", func(s *Settings) { delete(s.Connections[0].Fields, "token") }, "is required"},
		{"unknown field", func(s *Settings) { s.Connections[1].Fields["region"] = "x" }, "unknown field"},
		{"newline in field", func(s *Settings) { s.Connections[0].Fields["region"] = "eu\nx" }, "single line"},
		{"bad pinned model", func(s *Settings) { s.Connections[0].Fields["model"] = "a b" }, "invalid model"},
		{"duplicate connection id", func(s *Settings) { s.Connections[1].ID = "c1" }, "duplicate"},
		{"profile on missing connection", func(s *Settings) { s.Profiles[0].ConnectionID = "zz" }, "connection not found"},
		{"profile agent mismatch", func(s *Settings) { s.Profiles[0].Agent = AgentGemini }, "doesn't match"},
		{"profile bad model", func(s *Settings) { s.Profiles[0].Model = "x;rm -rf /" }, "invalid model"},
		{"default profile missing", func(s *Settings) { s.TaskDefaults.ProfileID = "zz" }, "default profile"},
		{"default host missing", func(s *Settings) { s.TaskDefaults.HostID = "h9" }, "default host"},
		{"git bad host", func(s *Settings) { s.Git[0].Host = "https://github.com" }, "not a host name"},
		{"git duplicate host", func(s *Settings) {
			s.Git = append(s.Git, GitAccount{ID: "g2", Host: "GitHub.com", Username: "x", Token: "y"})
		}, "one account per host"},
		{"git missing token", func(s *Settings) { s.Git[0].Token = "" }, "required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validSettings()
			tt.mutate(&s)
			err := s.Validate(func(id string) bool { return id == "h1" })
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestConnectionEnv(t *testing.T) {
	c := validSettings().Connections[0]
	got := c.Env()
	want := [][2]string{
		{"AWS_BEARER_TOKEN_BEDROCK", "tok"},
		{"AWS_REGION", "eu-central-1"},
		{"CLAUDE_CODE_USE_BEDROCK", "1"},
	}
	if len(got) != len(want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("env[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestExpiry(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	past, soon, later := now.Add(-time.Hour), now.Add(3*24*time.Hour), now.Add(60*24*time.Hour)
	for _, tt := range []struct {
		exp           *time.Time
		expired, soon bool
	}{{nil, false, false}, {&past, true, false}, {&soon, false, true}, {&later, false, false}} {
		c := Connection{Expires: tt.exp}
		if c.Expired(now) != tt.expired || c.ExpiresSoon(now) != tt.soon {
			t.Errorf("expires %v: expired=%v soon=%v", tt.exp, c.Expired(now), c.ExpiresSoon(now))
		}
	}
}

func TestGitFor(t *testing.T) {
	s := validSettings()
	if g, ok := s.GitFor("https://GitHub.com/moonlight-stream/moonlight-android"); !ok || g.ID != "g1" {
		t.Fatalf("https github.com: got %v %v", g, ok)
	}
	for _, u := range []string{"git@github.com:a/b.git", "ssh://git@github.com/a/b", "https://gitlab.com/a/b"} {
		if _, ok := s.GitFor(u); ok {
			t.Errorf("%s: want no account", u)
		}
	}
}

func TestStorePersistsAndRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent", "agent.yml")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("opening must not create the file, stat err = %v", err)
	}
	if _, err := st.Update(func(s *Settings) error { *s = validSettings(); return nil }, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("agent.yml mode = %v, want 0600", info.Mode().Perm())
	}

	// An invalid update leaves the stored settings untouched.
	if _, err := st.Update(func(s *Settings) error { s.Profiles[0].ConnectionID = "gone"; return nil }, nil); err == nil {
		t.Fatal("want validation error")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Get(); len(got.Profiles) != 1 || got.Profiles[0].ConnectionID != "c1" {
		t.Fatalf("reopened settings = %+v", got)
	}

	// Get returns a copy: mutating it doesn't reach the store.
	g := st.Get()
	g.Connections[0].Fields["token"] = "changed"
	if st.Get().Connections[0].Fields["token"] != "tok" {
		t.Fatal("Get leaked a reference to the stored map")
	}
}

func TestProberModelsCachesAndSingleFlights(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("X-Api-Key") != "key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		<-release
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-5","display_name":"Sonnet 5"}],"has_more":false}`))
	}))
	defer srv.Close()

	p := NewProber()
	p.Endpoints.Anthropic = srv.URL
	c := Connection{ID: "c", Kind: "claude-api", Fields: map[string]string{"apiKey": "key"}}

	var wg sync.WaitGroup
	results := make([]ModelList, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = p.Models(context.Background(), c, false)
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := hits.Load(); n != 1 {
		t.Fatalf("vendor hit %d times, want 1 (single-flight)", n)
	}
	for _, r := range results {
		if !r.Listed || len(r.Models) != 1 || r.Models[0].Name != "Sonnet 5" {
			t.Fatalf("result = %+v", r)
		}
	}
	p.Models(context.Background(), c, false)
	if n := hits.Load(); n != 1 {
		t.Fatalf("cached call hit the vendor (%d)", n)
	}
	p.Models(context.Background(), c, true)
	if n := hits.Load(); n != 2 {
		t.Fatalf("refresh didn't hit the vendor (%d)", n)
	}
}

func TestProberModelsFallsBackToAliasesOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	p := NewProber()
	p.Endpoints.Anthropic = srv.URL
	got := p.Models(context.Background(), Connection{Kind: "claude-api", Fields: map[string]string{"apiKey": "bad"}}, false)
	if got.Listed || got.Error == "" || len(got.Models) != len(claudeAliases) {
		t.Fatalf("got %+v, want aliases with an error", got)
	}
	if strings.Contains(got.Error, "bad") {
		t.Fatalf("error leaks the key: %q", got.Error)
	}
}

func TestProberTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1beta/models"):
			if r.Header.Get("X-Goog-Api-Key") != "g" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-3-pro","supportedGenerationMethods":["generateContent"]},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}]}`))
		case r.URL.Path == "/inference-profiles":
			_, _ = w.Write([]byte(`{"inferenceProfileSummaries":[{"inferenceProfileId":"eu.anthropic.claude-opus-5","inferenceProfileName":"Opus"},{"inferenceProfileId":"eu.meta.llama","inferenceProfileName":"Llama"}]}`))
		case r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"octo"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := NewProber()
	p.Endpoints.Gemini = srv.URL
	p.Endpoints.Bedrock = func(string) string { return srv.URL }
	p.Endpoints.GitHub = srv.URL
	ctx := context.Background()

	gem, _ := KindByID("gemini-api")
	if d, err := p.Test(ctx, gem, map[string]string{"apiKey": "g"}); err != nil || !strings.Contains(d, "1 models") {
		t.Fatalf("gemini test: %q %v", d, err)
	}
	if _, err := p.Test(ctx, gem, map[string]string{"apiKey": "wrong"}); err == nil {
		t.Fatal("gemini: want rejection")
	}
	if _, err := p.Test(ctx, gem, map[string]string{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("gemini missing key: %v", err)
	}

	bed, _ := KindByID("claude-bedrock")
	models, err := p.fetch(ctx, bed, map[string]string{"region": "eu-central-1", "token": "t"})
	if err != nil || len(models) != 1 || models[0].ID != "eu.anthropic.claude-opus-5" {
		t.Fatalf("bedrock models = %v, %v", models, err)
	}
	if _, err := p.fetch(ctx, bed, map[string]string{"region": "../evil", "token": "t"}); err == nil {
		t.Fatal("bedrock: want region rejection")
	}

	sub, _ := KindByID("claude-subscription")
	if d, err := p.Test(ctx, sub, map[string]string{"token": "x"}); err != nil || !strings.Contains(d, "can't be checked") {
		t.Fatalf("subscription test: %q %v", d, err)
	}

	if d, err := p.GitTest(ctx, "github.com", "t"); err != nil || !strings.Contains(d, "octo") {
		t.Fatalf("git test: %q %v", d, err)
	}
	if d, err := p.GitTest(ctx, "gitlab.example.com", "t"); err != nil || !strings.Contains(d, "Only github.com") {
		t.Fatalf("git test other host: %q %v", d, err)
	}
}
