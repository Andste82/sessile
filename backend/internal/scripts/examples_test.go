package scripts

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestExamplesAreValidAndZip(t *testing.T) {
	s := NewStore(t.TempDir())
	list, err := s.Examples("u1")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range list {
		names[e.Meta.Name] = true
		if e.Meta.Check == "" || len(e.Meta.Guidance) == 0 {
			t.Errorf("%s: examples should show a check and guidance", e.Meta.Name)
		}
		data, err := ExampleZip(e.Meta.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := readZip(data); err != nil {
			t.Errorf("%s: its own zip doesn't install: %v", e.Meta.Name, err)
		}
	}
	for _, want := range []string{"jira", "jenkins", "artifactory"} {
		if !names[want] {
			t.Errorf("missing example %s", want)
		}
	}
	if _, err := ExampleZip("../x"); err == nil {
		t.Error("ExampleZip accepted ../x")
	}
	if !newer("1.2.0", "1.1.9") || newer("1.0.0", "1.0.0") || newer("1.0.0", "2.0.0") {
		t.Error("newer() is wrong")
	}
}

// TestExamplesAgainstMockServices runs each example's functions through the
// real runner against a mock of its service: the requests they make, the auth
// they send, and the JSON they hand back. It needs PyPI for `requests`.
func TestExamplesAgainstMockServices(t *testing.T) {
	if testing.Short() {
		t.Skip("needs network for pip")
	}
	if err := exec.Command("python3", "-c", "import venv, ensurepip").Run(); err != nil {
		t.Skip("python3 without venv")
	}
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seen = append(seen, r.Method+" "+r.URL.Path+" "+auth)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/rest/api/2/myself":
			io.WriteString(w, `{"displayName":"Octo","accountId":"abc"}`)
		case r.URL.Path == "/rest/api/2/issue/DBG-142":
			io.WriteString(w, `{"key":"DBG-142","fields":{"summary":"Print crash","status":{"name":"Open"},"issuetype":{"name":"Bug"},
				"description":"Crashes on print","issuelinks":[{"type":{"outward":"blocks"},"outwardIssue":{"key":"DBG-7","fields":{"summary":"x"}}}],
				"comment":{"comments":[{"author":{"displayName":"A"},"body":"seen on 14"}]}}}`)
		case r.URL.Path == "/rest/api/2/search/jql":
			if r.URL.Query().Get("jql") == "" {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"issues":[{"key":"DBG-1","fields":{"summary":"s","status":{"name":"Done"}}}]}`)
		case r.URL.Path == "/me/api/json":
			io.WriteString(w, `{"id":"ci","fullName":"CI User"}`)
		case r.URL.Path == "/job/team/job/app/api/json":
			io.WriteString(w, `{"name":"app","lastBuild":{"number":42,"result":"FAILURE","building":false,"url":"u"}}`)
		case r.URL.Path == "/job/team/job/app/lastBuild/consoleText":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "line1\nline2\nERROR: boom\n")
		case r.URL.Path == "/api/repositories":
			io.WriteString(w, `[{"key":"libs-release"}]`)
		case r.URL.Path == "/api/storage/libs-release/com/app":
			io.WriteString(w, `{"children":[{"uri":"/1.0","folder":true}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	data := t.TempDir()
	s := NewStore(data)
	r := NewRunner(data, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	install := func(name string) (Meta, string) {
		zip, _ := ExampleZip(name)
		m, err := s.Install("u1", zip, "", false)
		if err != nil {
			t.Fatal(err)
		}
		return m, s.Dir("u1", name)
	}
	run := func(m Meta, dir string, st Settings, fn, input string) map[string]any {
		t.Helper()
		res, err := r.Run(ctx, "u1", m, dir, st, fn, json.RawMessage(input))
		if err != nil {
			t.Fatalf("%s.%s: %v", m.Name, fn, err)
		}
		var out map[string]any
		if err := json.Unmarshal(res.Output, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	jira, jdir := install("jira")
	jst := Settings{Values: map[string]string{"JIRA_URL": srv.URL, "JIRA_AUTH": "cloud", "JIRA_USER": "me@x", "JIRA_TOKEN": "jtok"}}
	if c := r.Check(ctx, "u1", jira, jdir, jst); !c.OK {
		t.Fatalf("jira check: %s", c.Message)
	}
	ticket := run(jira, jdir, jst, "read_ticket", `{"key":"DBG-142"}`)
	if ticket["summary"] != "Print crash" || ticket["status"] != "Open" || !strings.Contains(toJSON(ticket["links"]), "DBG-7") {
		t.Errorf("read_ticket = %v", ticket)
	}
	if res := run(jira, jdir, jst, "search", `{"jql":"project = DBG"}`); !strings.Contains(toJSON(res), "DBG-1") {
		t.Errorf("search = %v", res)
	}

	jenkins, kdir := install("jenkins")
	kst := Settings{Values: map[string]string{"JENKINS_URL": srv.URL, "JENKINS_USER": "ci", "JENKINS_TOKEN": "ktok"}}
	if c := r.Check(ctx, "u1", jenkins, kdir, kst); !c.OK {
		t.Fatalf("jenkins check: %s", c.Message)
	}
	status := run(jenkins, kdir, kst, "job_status", `{"job":"team/app"}`)
	if !strings.Contains(toJSON(status), `"FAILURE"`) {
		t.Errorf("job_status = %v", status)
	}
	if log := run(jenkins, kdir, kst, "build_log_tail", `{"job":"team/app","lines":1}`); !strings.Contains(toJSON(log), "ERROR: boom") || strings.Contains(toJSON(log), "line1") {
		t.Errorf("build_log_tail = %v", log)
	}
	if _, err := r.Run(ctx, "u1", jenkins, kdir, kst, "job_status", json.RawMessage(`{"job":"team/../../script"}`)); err == nil {
		t.Error("a job path with .. ran")
	}

	art, adir := install("artifactory")
	ast := Settings{Values: map[string]string{"ARTIFACTORY_URL": srv.URL, "ARTIFACTORY_REPO": "libs-release", "ARTIFACTORY_TOKEN": "atok"}}
	if c := r.Check(ctx, "u1", art, adir, ast); !c.OK {
		t.Fatalf("artifactory check: %s", c.Message)
	}
	if res := run(art, adir, ast, "list_path", `{"path":"com/app"}`); !strings.Contains(toJSON(res), "1.0") {
		t.Errorf("list_path = %v", res)
	}

	all := strings.Join(seen, "\n")
	for _, want := range []string{"GET /rest/api/2/myself Basic ", "GET /me/api/json Basic ", "GET /api/repositories Bearer atok"} {
		if !strings.Contains(all, want) {
			t.Errorf("no request %q in:\n%s", want, all)
		}
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
