package mcp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/tasks"
)

// A task asks and waits; the first answer — the user's from the panel, or
// the orchestrator's — releases the call, and the task goes back to working
// (§4.12.4, E15).
func TestAskIsAnsweredByWhoeverGetsThereFirst(t *testing.T) {
	s, _, taskID, l := setup(t)
	c := connect(t, l, "the-token")

	done := make(chan [2]any, 1)
	go func() {
		text, isErr := callTool(t, c, "ask", map[string]any{
			"question": "Which branch should I target?",
			"options":  []string{"main", "release/0.8"},
		})
		done <- [2]any{text, isErr}
	}()

	// The question surfaces for the user, and the task reads as blocked.
	waitFor(t, func() bool { return len(s.Questions("u1", taskID)) == 1 })
	if got, _ := s.Tasks.Get("u1", taskID); got.State != tasks.StateBlocked || got.Question == "" {
		t.Errorf("the task should be blocked while it waits: %+v", got)
	}
	q := s.Questions("u1", taskID)[0]
	if q.Question != "Which branch should I target?" || len(q.Options) != 2 {
		t.Errorf("question = %+v", q)
	}

	if err := s.AnswerTask("u1", taskID, "main, and rebase before you push"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got[1] != false || got[0] != "main, and rebase before you push" {
			t.Errorf("the agent got %v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ask never returned")
	}
	after, _ := s.Tasks.Get("u1", taskID)
	if after.State != tasks.StateWorking || after.Question != "" {
		t.Errorf("an answered task is working again: %+v", after)
	}
	// Nothing is waiting any more, so a second answer is refused.
	if err := s.AnswerTask("u1", taskID, "too late"); err == nil {
		t.Error("answering twice should fail")
	}
}

func TestAskIsScopedToItsOwner(t *testing.T) {
	s, _, taskID, l := setup(t)
	c := connect(t, l, "the-token")
	done := make(chan struct{})
	go func() {
		defer close(done)
		callTool(t, c, "ask", map[string]any{"question": "Which branch?"})
	}()
	waitFor(t, func() bool { return len(s.Questions("u1", taskID)) == 1 })

	if err := s.AnswerTask("u2", taskID, "not yours"); err == nil {
		t.Error("another user must not answer this question")
	}
	if qs := s.Questions("u2", ""); len(qs) != 0 {
		t.Errorf("another user must not see it: %v", qs)
	}
	if err := s.AnswerTask("u1", taskID, "main"); err != nil {
		t.Fatal(err)
	}
	<-done
}

// The orchestrator manages tasks and talks to the user in its own terminal,
// so it has no ask of its own.
func TestOrchestratorHasNoAsk(t *testing.T) {
	s, _, _, _ := setup(t)
	text, isErr := s.call(t.Context(), "u1", "", tasks.ScopeOrchestrator, "ask",
		json.RawMessage(`{"question":"?"}`))
	if !isErr {
		t.Errorf("the orchestrator should not have ask: %s", text)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
