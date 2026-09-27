package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/agents"
)

func strp(s string) *string { return &s }

func TestAgentSettingsMergeKeepsOmittedSecrets(t *testing.T) {
	cur := agents.Settings{
		Connections: []agents.Connection{{ID: "c1", Name: "Bedrock", Kind: "claude-bedrock",
			Fields: map[string]string{"region": "eu-central-1", "token": "secret-token"}}},
		Git: []agents.GitAccount{{ID: "g1", Host: "github.com", Username: "me", Token: "ghp_secret"}},
	}
	body := agentSettingsBody{
		Connections: []connectionBody{
			// Edit: region changes, token omitted -> kept.
			{ID: "c1", Name: "Bedrock EU", Kind: "claude-bedrock", Fields: map[string]*string{"region": strp("eu-west-1")}},
			// New, with a client-chosen UUID a profile references in the same PUT.
			{ID: "6f1c3a2e-0b7d-4c55-9a1e-2d3f4b5c6d7e", Name: "Max", Kind: "claude-subscription",
				Fields: map[string]*string{"token": strp("oat")}, Expires: strp("2027-09-18")},
		},
		Profiles: []profileBody{{Name: "Claude (Max)", ConnectionID: "6f1c3a2e-0b7d-4c55-9a1e-2d3f4b5c6d7e", Model: "sonnet"}},
		Git:      []gitBody{{ID: "g1", Host: "GitHub.com", Username: "me2"}},
	}
	next, err := body.merge(cur)
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Validate(nil); err != nil {
		t.Fatal(err)
	}
	c1, _ := next.Connection("c1")
	if c1.Fields["token"] != "secret-token" || c1.Fields["region"] != "eu-west-1" {
		t.Fatalf("c1 fields = %v", c1.Fields)
	}
	max, ok := next.Connection("6f1c3a2e-0b7d-4c55-9a1e-2d3f4b5c6d7e")
	if !ok || max.Expires == nil || max.Expires.Format("2006-01-02") != "2027-09-18" {
		t.Fatalf("new connection = %+v", max)
	}
	if p := next.Profiles[0]; p.Agent != agents.AgentClaude || p.ID == "" {
		t.Fatalf("profile = %+v, want agent derived and an id assigned", p)
	}
	if g := next.Git[0]; g.Token != "ghp_secret" || g.Host != "github.com" || g.Username != "me2" {
		t.Fatalf("git = %+v", g)
	}

	// A kind change doesn't carry the old kind's secret over.
	body.Connections[0] = connectionBody{ID: "c1", Name: "Now API", Kind: "claude-api"}
	next, err = body.merge(cur)
	if err != nil {
		t.Fatal(err)
	}
	c1, _ = next.Connection("c1")
	if len(c1.Fields) != 0 {
		t.Fatalf("fields after kind change = %v", c1.Fields)
	}
}

func TestAgentSettingsMergeRejectsUnknownField(t *testing.T) {
	body := agentSettingsBody{Connections: []connectionBody{{Name: "x", Kind: "claude-api",
		Fields: map[string]*string{"apiKey": strp("k"), "region": strp("eu")}}}}
	if _, err := body.merge(agents.Settings{}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentSettingsJSONNeverContainsSecrets(t *testing.T) {
	exp := time.Now().Add(24 * time.Hour)
	s := agents.Settings{
		Connections: []agents.Connection{{ID: "c1", Name: "B", Kind: "claude-bedrock", Expires: &exp,
			Fields: map[string]string{"region": "eu-central-1", "token": "TOPSECRET-1"}}},
		Git: []agents.GitAccount{{ID: "g1", Host: "github.com", Username: "me", Token: "TOPSECRET-2"}},
	}
	out, err := json.Marshal(toAgentSettingsJSON(s, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	js := string(out)
	if strings.Contains(js, "TOPSECRET") {
		t.Fatalf("response leaks a secret: %s", js)
	}
	for _, want := range []string{`"token":true`, `"hasToken":true`, `"region":"eu-central-1"`, `"expiresSoon":true`} {
		if !strings.Contains(js, want) {
			t.Errorf("response missing %s: %s", want, js)
		}
	}
}
