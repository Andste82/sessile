package notes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPutGetListDelete(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put("u1", "repos", ContextAlways, "# Repos\n\n- wolf: the streaming server\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("u1", "jira", "", "Workflow: Epic > Story\n"); err != nil {
		t.Fatal(err)
	}
	n, err := s.Get("u1", "repos")
	if err != nil || n.Title != "Repos" || n.Context != ContextAlways || !strings.HasPrefix(n.Body, "# Repos") {
		t.Fatalf("get = %+v, %v", n, err)
	}
	list, _ := s.List("u1")
	if len(list) != 2 || list[0].Slug != "jira" || list[0].Body != "" || list[0].Context != ContextOnDemand {
		t.Fatalf("list = %+v", list)
	}
	if other, _ := s.List("u2"); len(other) != 0 {
		t.Fatalf("u2 sees u1's notes: %+v", other)
	}
	tn, _ := s.TaskNotes("u1")
	if len(tn) != 2 || !tn[1].Always || tn[1].Body == "" {
		t.Fatalf("task notes = %+v", tn)
	}
	if err := s.Delete("u1", "jira"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("u1", "jira"); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}

func TestSlugsCantEscape(t *testing.T) {
	s := New(t.TempDir())
	for _, slug := range []string{
		"../x", "a/../../x", "/x", "x/", "a//b", "", "A", ".hidden",
		strings.Repeat("a", 65), "a/b/c/d/e", // deeper than notes may be filed
	} {
		if _, err := s.Put("u1", slug, "", "x"); err == nil {
			t.Errorf("Put(%q) accepted", slug)
		}
		if _, err := s.Get("u1", slug); err == nil {
			t.Errorf("Get(%q) found something", slug)
		}
	}
}

// Notes may be filed in folders, so an agent can keep what it learns in
// order: hosts/km-gaming, runbooks/deploy (§4.14).
func TestNotesInFolders(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Put("u1", "hosts/km-gaming", ContextOnDemand, "ssh in as root\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("u1", "runbooks/deploy/staging", ContextAlways, "helm upgrade\n"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("u1", "hosts/km-gaming")
	if err != nil || got.Body != "ssh in as root\n" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	list, err := s.List("u1")
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, n := range list {
		slugs = append(slugs, n.Slug)
	}
	if len(slugs) != 2 || slugs[0] != "hosts/km-gaming" || slugs[1] != "runbooks/deploy/staging" {
		t.Fatalf("list = %v", slugs)
	}
	// A task sees them like any other note.
	tn, err := s.TaskNotes("u1")
	if err != nil || len(tn) != 2 {
		t.Fatalf("task notes = %+v, %v", tn, err)
	}
	if err := s.Delete("u1", "hosts/km-gaming"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.dir("u1"), "hosts")); !os.IsNotExist(err) {
		t.Errorf("the empty folder should go with its last note: %v", err)
	}
}

func TestHandEditedFrontMatter(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	p := filepath.Join(dir, "users", "u1", "agent", "notes")
	os.MkdirAll(p, 0o700)
	os.WriteFile(filepath.Join(p, "a.md"), []byte("---\r\ncontext: always\r\ntags: x\r\n---\r\nbody\r\n"), 0o600)
	os.WriteFile(filepath.Join(p, "b.md"), []byte("no front matter\n"), 0o600)
	a, _ := s.Get("u1", "a")
	b, _ := s.Get("u1", "b")
	if a.Context != ContextAlways || a.Body != "body\n" || b.Context != ContextOnDemand || b.Body != "no front matter\n" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestSecretWarnings(t *testing.T) {
	for body, want := range map[string]bool{
		"access-token: abcdefgh12345":                        true,
		"key AKIAABCDEFGHIJKLMNOP here":                      true,
		"ghp_" + strings.Repeat("a", 36):                     true,
		"-----BEGIN OPENSSH PRIVATE KEY-----":                true,
		"URL: https://jira.example.com\nProject: Debugger\n": false,
		"the token is in the Jira settings":                  false,
	} {
		if got := len(SecretWarnings(body)) > 0; got != want {
			t.Errorf("%q: warned=%v, want %v", body, got, want)
		}
	}
}
