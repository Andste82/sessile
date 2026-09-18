// Package notes is each user's markdown notes (PROJECT_PLAN.md §4.14): one
// file per note in <data-dir>/users/<user-id>/agent/notes/<slug>.md, hand-
// editable like hosts.yml, given to every task as context.
//
// Notes reach the agent — and so its model — as they are. Secrets belong in
// script settings or connections instead; a note that looks like it holds
// one gets a warning (never a block) when it is saved.
package notes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Andste82/sessile/backend/internal/tasks"
)

// MaxBody bounds a note, inside the API's 32 KiB JSON body cap.
const MaxBody = 30 << 10

// Context says how a task gets a note (§4.14).
type Context string

const (
	// ContextOnDemand notes are copied into the task's notes/ folder, where
	// the agent reads them when they sound relevant.
	ContextOnDemand Context = "on-demand"
	// ContextAlways notes are also inlined into the task's instructions.
	ContextAlways Context = "always"
)

// Note is one note.
type Note struct {
	Slug    string    `json:"slug"`
	Title   string    `json:"title"`
	Context Context   `json:"context"`
	Body    string    `json:"body,omitempty"` // without the front matter
	Updated time.Time `json:"updated"`
	// Warnings are the secret-lint findings for the body (§4.14).
	Warnings []string `json:"warnings,omitempty"`
}

var (
	ErrNotFound = errors.New("note not found")
	slugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// ValidSlug reports whether s may name a note (and so a file).
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// Store is the notes of every user; a user's notes are only ever reached
// through their own id (§14.5).
type Store struct {
	dataDir string
	mu      sync.Mutex
}

// New returns a Store rooted at <dataDir>/users.
func New(dataDir string) *Store { return &Store{dataDir: dataDir} }

func (s *Store) dir(userID string) string {
	return filepath.Join(s.dataDir, "users", userID, "agent", "notes")
}

// List returns the user's notes without bodies, sorted by slug.
func (s *Store) List(userID string) ([]Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir(userID))
	if os.IsNotExist(err) {
		return []Note{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read notes: %w", err)
	}
	out := []Note{}
	for _, e := range entries {
		slug, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || e.IsDir() || !ValidSlug(slug) {
			continue
		}
		n, err := s.read(userID, slug)
		if err != nil {
			continue
		}
		n.Body = ""
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

// Get returns one note with its body.
func (s *Store) Get(userID, slug string) (Note, error) {
	if !ValidSlug(slug) {
		return Note{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(userID, slug)
}

func (s *Store) read(userID, slug string) (Note, error) {
	p := filepath.Join(s.dir(userID), slug+".md")
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return Note{}, ErrNotFound
	}
	if err != nil {
		return Note{}, fmt.Errorf("read note: %w", err)
	}
	info, _ := os.Stat(p)
	ctx, body := splitFrontMatter(string(data))
	n := Note{Slug: slug, Context: ctx, Body: body, Title: title(slug, body), Warnings: SecretWarnings(body)}
	if info != nil {
		n.Updated = info.ModTime().UTC()
	}
	return n, nil
}

// Put creates or replaces a note.
func (s *Store) Put(userID, slug string, ctx Context, body string) (Note, error) {
	if !ValidSlug(slug) {
		return Note{}, fmt.Errorf("a note name is lowercase letters, digits and dashes (up to 64)")
	}
	if ctx != ContextAlways {
		ctx = ContextOnDemand
	}
	if len(body) > MaxBody {
		return Note{}, fmt.Errorf("a note is at most %d KiB", MaxBody>>10)
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.dir(userID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Note{}, fmt.Errorf("create notes dir: %w", err)
	}
	content := "---\ncontext: " + string(ctx) + "\n---\n" + body
	tmp, err := os.CreateTemp(dir, ".note-*.tmp")
	if err != nil {
		return Note{}, fmt.Errorf("write note: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return Note{}, err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return Note{}, err
	}
	if err := tmp.Close(); err != nil {
		return Note{}, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, slug+".md")); err != nil {
		return Note{}, fmt.Errorf("replace note: %w", err)
	}
	return s.read(userID, slug)
}

// Delete removes a note.
func (s *Store) Delete(userID, slug string) error {
	if !ValidSlug(slug) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.dir(userID), slug+".md"))
	if os.IsNotExist(err) {
		return ErrNotFound
	}
	return err
}

// TaskNotes is tasks.NotesSource: every note, with its body, as a task
// sees it.
func (s *Store) TaskNotes(userID string) ([]tasks.Note, error) {
	list, err := s.List(userID)
	if err != nil {
		return nil, err
	}
	out := make([]tasks.Note, 0, len(list))
	for _, n := range list {
		full, err := s.Get(userID, n.Slug)
		if err != nil {
			continue
		}
		out = append(out, tasks.Note{Slug: n.Slug, Title: full.Title, Body: full.Body, Always: full.Context == ContextAlways})
	}
	return out, nil
}

// splitFrontMatter reads an optional leading "---\ncontext: …\n---\n" block,
// the only front-matter key sessile uses; a note without one is on-demand.
func splitFrontMatter(s string) (Context, string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return ContextOnDemand, s
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return ContextOnDemand, s
	}
	ctx := ContextOnDemand
	for _, line := range strings.Split(s[4:4+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(k) == "context" && strings.TrimSpace(v) == string(ContextAlways) {
			ctx = ContextAlways
		}
	}
	return ctx, s[4+end+5:]
}

// title is the note's first markdown heading, or its slug.
func title(slug, body string) string {
	for _, line := range strings.Split(body, "\n") {
		if t, ok := strings.CutPrefix(strings.TrimSpace(line), "#"); ok {
			if t = strings.TrimSpace(strings.TrimLeft(t, "#")); t != "" {
				return t
			}
		}
	}
	return slug
}

// secretPatterns are the shapes a secret usually has. False positives only
// cost a warning; a secret that slips past still reaches only the user's own
// agent — but through its model, which is why the warning exists at all.
var secretPatterns = []struct {
	re   *regexp.Regexp
	what string
}{
	{regexp.MustCompile(`(?i)\b(access[-_ ]?token|api[-_ ]?key|secret|password|passwd|token)\b\s*[:=]\s*\S{8,}`), "a line that assigns a token, key or password"},
	{regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), "an AWS access key"},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`), "a GitHub token"},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`), "a GitLab token"},
	{regexp.MustCompile(`\bsk-(ant-|proj-)?[A-Za-z0-9_-]{20,}\b`), "an Anthropic or OpenAI key"},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`), "a Slack token"},
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), "a private key"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), "a JSON Web Token"},
}

// SecretWarnings lists what in body looks like a secret.
func SecretWarnings(body string) []string {
	var out []string
	for _, p := range secretPatterns {
		if p.re.MatchString(body) {
			out = append(out, "This note seems to contain "+p.what+". Notes are sent to your agent's model; keep secrets in script settings or connections instead.")
		}
	}
	return out
}
