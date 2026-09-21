package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Andste82/sessile/backend/internal/tasks"
)

// ask (PROJECT_PLAN.md §4.12.4, E15): a task's agent needs a decision and
// waits for it. The call is held open, the question reaches the user's task
// panel and the orchestrator's event stream, and the first answer — from
// either — releases it.
//
// It replaces marking yourself blocked and then watching your own terminal
// for someone to type: nothing can interrupt an agent sitting at its prompt,
// so that only ever worked while a person was looking.

// askTimeout is how long a question waits before the agent is told to carry
// on. Long, because a question is for a person, and they may be at lunch.
const askTimeout = 30 * time.Minute

// askTool is served in the task scope.
var askTool = Tool{
	Name: "ask",
	Description: "Ask the user a question and wait for the answer. Use it when a decision is theirs to make — " +
		"which branch, which approach, whether to go ahead. The question reaches them in sessile, and their " +
		"orchestrator can answer it too. Prefer this over guessing, and over waiting silently.",
	InputSchema: json.RawMessage(`{"type":"object","required":["question"],"properties":{
		"question":{"type":"string","minLength":1,"maxLength":2000},
		"options":{"type":"array","items":{"type":"string"},"maxItems":6,"description":"Suggested answers, if the choice is between a few things"}}}`),
}

// question is one held ask.
type question struct {
	userID, taskID string
	text           string
	options        []string
	asked          time.Time
	answered       chan string
}

// callAsk holds the call until someone answers.
func (s *Server) callAsk(ctx context.Context, userID, taskID string, raw json.RawMessage) (string, bool) {
	var a struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Question) == "" {
		return "question is required", true
	}
	text := clip(a.Question, 2000)
	callID := newCallID()
	q := &question{userID: userID, taskID: taskID, text: text, options: a.Options,
		asked: time.Now().UTC(), answered: make(chan string, 1)}

	s.mu.Lock()
	if s.questions == nil {
		s.questions = map[string]*question{}
	}
	s.questions[callID] = q
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.questions, callID)
		s.mu.Unlock()
	}()

	// The task is blocked while it waits, so the sidebar, the dashboard and
	// list_tasks all say so without anyone reading this terminal (§4.18.2).
	previous := ""
	if t, err := s.Tasks.Get(userID, taskID); err == nil {
		previous = t.Summary
	}
	_ = s.Tasks.SetState(taskID, tasks.StateBlocked, previous, text)
	s.publish(userID, StateMsg{Type: "taskState", TaskID: taskID, State: tasks.StateBlocked,
		Summary: previous, Question: text})
	s.publish(userID, QuestionMsg{Type: "taskQuestion", TaskID: taskID, CallID: callID,
		Question: text, Options: a.Options, Status: "pending"})
	s.pushEvent(userID, Event{Type: "taskState", TaskID: taskID, State: tasks.StateBlocked,
		Summary: previous, Question: text})

	var answer string
	var status string
	select {
	case answer = <-q.answered:
		status = "answered"
	case <-time.After(askTimeout):
		status = "expired"
	case <-ctx.Done():
		status = "gone"
	}
	s.publish(userID, QuestionMsg{Type: "taskQuestion", TaskID: taskID, CallID: callID, Status: status})

	if status != "answered" {
		// Nobody came. The task is working again, and says what it assumed.
		s.setWorking(userID, taskID, previous)
		if status == "expired" {
			return "No one answered within " + askTimeout.String() +
				". Carry on with your best judgement, and say what you assumed.", false
		}
		return "The question was dropped.", true
	}
	s.setWorking(userID, taskID, previous)
	return answer, false
}

// setWorking takes the task out of blocked once its question is settled.
func (s *Server) setWorking(userID, taskID, summary string) {
	if err := s.Tasks.SetState(taskID, tasks.StateWorking, summary, ""); err != nil {
		return
	}
	s.publish(userID, StateMsg{Type: "taskState", TaskID: taskID, State: tasks.StateWorking, Summary: summary})
	s.pushEvent(userID, Event{Type: "taskState", TaskID: taskID, State: tasks.StateWorking, Summary: summary})
}

// ErrNoSuchQuestion is an answer to a question that is not waiting (any more).
var ErrNoSuchQuestion = errors.New("no such question")

// Answer releases a held ask, scoped to its user. Both the user's own answer
// from the task panel and the orchestrator's answer_task arrive here, and the
// first one wins.
func (s *Server) Answer(userID, taskID, callID, answer string) error {
	s.mu.Lock()
	q := s.questions[callID]
	s.mu.Unlock()
	if q == nil || q.userID != userID || (taskID != "" && q.taskID != taskID) {
		return ErrNoSuchQuestion
	}
	select {
	case q.answered <- answer:
		return nil
	default:
		return ErrNoSuchQuestion
	}
}

// AnswerTask releases whichever question a task is waiting on, for a caller
// that knows the task but not the call — the orchestrator, and the task page
// after a refresh.
func (s *Server) AnswerTask(userID, taskID, answer string) error {
	s.mu.Lock()
	var newest *question
	var newestID string
	for id, q := range s.questions {
		if q.userID == userID && q.taskID == taskID {
			if newest == nil || q.asked.After(newest.asked) {
				newest, newestID = q, id
			}
		}
	}
	s.mu.Unlock()
	if newest == nil {
		return ErrNoSuchQuestion
	}
	return s.Answer(userID, taskID, newestID, answer)
}

// Questions lists what a user's tasks are waiting to be told, so a task page
// that opens after the question was asked still shows it.
func (s *Server) Questions(userID, taskID string) []QuestionMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []QuestionMsg{}
	for id, q := range s.questions {
		if q.userID == userID && (taskID == "" || q.taskID == taskID) {
			out = append(out, QuestionMsg{Type: "taskQuestion", TaskID: q.taskID, CallID: id,
				Question: q.text, Options: q.options, Status: "pending"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CallID < out[j].CallID })
	return out
}

// QuestionMsg carries a held question to the browser (§5.3).
type QuestionMsg struct {
	Type     string   `json:"type"` // "taskQuestion"
	TaskID   string   `json:"taskId"`
	CallID   string   `json:"callId"`
	Question string   `json:"question,omitempty"`
	Options  []string `json:"options,omitempty"`
	Status   string   `json:"status"` // pending | answered | expired | gone
}
