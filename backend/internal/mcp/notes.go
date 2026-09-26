package mcp

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/Andste82/sessile/backend/internal/notes"
)

// The notes tools (PROJECT_PLAN.md §4.14). Notes are the user's own memory
// of their estate — which repo is what, how a host is reached, what a
// runbook says — and until now only the user could write them, which left an
// agent that learned something with nowhere to put it.
//
// They are the user's notes, not the agent's scratchpad: they are read into
// every task, and the user reads and edits them in sessile. An agent writes
// what the user would want to find next time, not its working state — that
// belongs in its own folder.

var noteTools = []Tool{
	{
		Name:        "list_notes",
		Description: "The user's notes, with their text. Background about their repos, hosts, team and workflow.",
		InputSchema: emptySchema,
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "read_note",
		Description: "One note, by its name (\"hosts/km-gaming\").",
		InputSchema: json.RawMessage(`{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name: "write_note",
		Description: "Write a note into the user's notes, creating or replacing it. Use it for something worth having " +
			"next time — how a host is reached, what a repo is for, a runbook — not for your own working state. " +
			"Names may use folders: \"hosts/km-gaming\", \"runbooks/deploy\". Say in the conversation that you wrote it.",
		InputSchema: json.RawMessage(`{"type":"object","required":["name","body"],"properties":{
			"name":{"type":"string","description":"Lowercase letters, digits and dashes, optionally in folders (up to 4 deep)"},
			"body":{"type":"string","description":"Markdown. Start with a # heading; it becomes the note's title"},
			"always":{"type":"boolean","description":"true puts this note into every task's instructions — only for something every task needs"}}}`),
	},
}

var noteToolNames = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range noteTools {
		m[t.Name] = true
	}
	return m
}()

// callNote runs one of the notes tools, for a task's agent or the
// orchestrator — both write to the same notes, which are the user's.
func (s *Server) callNote(userID, name string, raw json.RawMessage) (string, bool) {
	if s.Notes == nil {
		return "notes are not available", true
	}
	var a struct {
		Name   string `json:"name"`
		Body   string `json:"body"`
		Always bool   `json:"always"`
	}
	_ = json.Unmarshal(raw, &a)

	switch name {
	case "list_notes":
		list, err := s.Notes.List(userID)
		if err != nil {
			return "could not read the notes", true
		}
		out := []map[string]any{}
		for _, n := range list {
			full, err := s.Notes.Get(userID, n.Slug)
			if err != nil {
				continue
			}
			out = append(out, map[string]any{
				"name": n.Slug, "title": n.Title, "always": n.Context == notes.ContextAlways,
				"body": full.Body,
			})
		}
		return asJSON(map[string]any{"notes": out})

	case "read_note":
		n, err := s.Notes.Get(userID, strings.TrimSpace(a.Name))
		if errors.Is(err, notes.ErrNotFound) {
			return "there is no note called " + a.Name, true
		}
		if err != nil {
			return "could not read that note", true
		}
		return asJSON(map[string]any{"name": n.Slug, "title": n.Title,
			"always": n.Context == notes.ContextAlways, "body": n.Body})

	case "write_note":
		ctx := notes.ContextOnDemand
		if a.Always {
			ctx = notes.ContextAlways
		}
		n, err := s.Notes.Put(userID, strings.TrimSpace(a.Name), ctx, a.Body)
		if err != nil {
			return err.Error(), true
		}
		s.publish(userID, NoteMsg{Type: "noteWritten", Name: n.Slug, Title: n.Title})
		msg := "Saved as " + n.Slug + "."
		if len(n.Warnings) > 0 {
			// The note reaches every task, and so its model. Saying this back
			// is the whole point of the lint: the agent can fix it now.
			msg += " Careful: " + strings.Join(n.Warnings, "; ") +
				". Secrets belong in a connection or a script's settings, not in a note."
		}
		return msg, false
	}
	return "unknown tool " + name, true
}

// asJSON is how every tool that returns structured data answers.
func asJSON(v any) (string, bool) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "could not encode the result", true
	}
	return string(b), false
}

// NoteMsg tells the browser a note changed, so an open Notes page refreshes
// (§5.3).
type NoteMsg struct {
	Type  string `json:"type"` // "noteWritten"
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
}
