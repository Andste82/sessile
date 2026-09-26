package mcp

import (
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/notes"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

// A task's agent can write what it learned into the user's notes, in
// folders, and read it back — the same tools the orchestrator has (§4.14).
func TestNotesToolsWriteAndRead(t *testing.T) {
	s, _, _, l := setup(t)
	s.Notes = notes.New(t.TempDir())
	c := connect(t, l, "the-token")

	text, isErr := callTool(t, c, "write_note", map[string]any{
		"name": "hosts/km-gaming",
		"body": "# km-gaming\n\nssh as root; the repos live in /srv.\n",
	})
	if isErr || !strings.Contains(text, "hosts/km-gaming") {
		t.Fatalf("write_note: %s", text)
	}
	back := decode(t, mustJSONArgs(t, c, "read_note", map[string]any{"name": "hosts/km-gaming"}))
	if !strings.Contains(back["body"].(string), "the repos live in /srv") {
		t.Errorf("read_note: %v", back)
	}
	listed := decode(t, mustJSON(t, c, "list_notes"))["notes"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["name"] != "hosts/km-gaming" {
		t.Errorf("list_notes: %v", listed)
	}

	// A name that would escape the notes directory is refused.
	if text, isErr := callTool(t, c, "write_note", map[string]any{"name": "../escape", "body": "x"}); !isErr {
		t.Errorf("a note name must stay inside the notes folder: %s", text)
	}
	// A secret in a note is saved but said back, because it reaches every task.
	warned, _ := callTool(t, c, "write_note", map[string]any{
		"name": "creds", "body": "token: ghp_0123456789abcdefghijklmnopqrstuvwxyzAB\n",
	})
	if !strings.Contains(warned, "Careful") {
		t.Errorf("a note that looks like it holds a secret should say so: %s", warned)
	}
}

// Both scopes have them: a task files what it learned, the orchestrator
// reads it back when briefing the next one.
func TestNotesToolsAreInBothScopes(t *testing.T) {
	for _, scope := range []string{tasks.ScopeTask, tasks.ScopeOrchestrator} {
		names := map[string]bool{}
		for _, tool := range (&Server{}).tools("u1", scope) {
			names[tool.Name] = true
		}
		for _, want := range []string{"list_notes", "read_note", "write_note"} {
			if !names[want] {
				t.Errorf("%s scope is missing %s", scope, want)
			}
		}
	}
}
