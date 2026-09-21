package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

// The orchestrator's tools (PROJECT_PLAN.md §4.18.1): sessile itself. They
// exist only in the orchestrator scope; a task's agent never gets them.

// orchestratorTools are served in addition to the scripts and the task
// tools.
var orchestratorTools = []Tool{
	{
		Name:        "list_hosts",
		Description: "The user's hosts: name, group and OS. Never addresses or credentials.",
		InputSchema: emptySchema,
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "list_profiles",
		Description: "The user's agent profiles: which agent and model a task can be started with.",
		InputSchema: emptySchema,
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "list_notes",
		Description: "The user's notes, with their text. Background about their repos, team and workflow.",
		InputSchema: emptySchema,
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name: "create_task",
		Description: "Start a task: sessile sets up a folder on the host, clones the repo, and starts the agent in it. " +
			"Use the user's words for `request` — it is the first message the task's agent gets. " +
			"Ask the user in the terminal about anything unclear before calling this; no separate confirmation is needed.",
		InputSchema: json.RawMessage(`{"type":"object","required":["name","request"],"properties":{
			"name":{"type":"string","minLength":1,"maxLength":64,"description":"Short name, e.g. \"DBG-142 print crash\""},
			"request":{"type":"string","minLength":1,"maxLength":65536,"description":"What the task's agent should do, briefed like a colleague"},
			"hostId":{"type":"string","description":"One of list_hosts' ids; omit with target \"local\""},
			"target":{"type":"string","enum":["local"],"description":"\"local\": run on the sessile server itself"},
			"epic":{"type":"string","maxLength":64,"description":"Groups tasks that belong together"},
			"profileId":{"type":"string","description":"One of list_profiles' ids; default: the user's default profile"},
			"model":{"type":"string","description":"Optional model id for this task"},
			"mode":{"type":"string","enum":["plan","normal"],"description":"plan (default): the task's agent plans before it acts"},
			"repo":{"type":"object","properties":{"url":{"type":"string"},"ref":{"type":"string"}},"description":"Main repository to clone"},
			"devcontainer":{"type":"object","properties":{"mode":{"type":"string","enum":["auto","repo","generic"]},"dockerSocket":{"type":"boolean"}},"description":"Run the task in the repo's devcontainer; needs repo"}}}`),
	},
	{
		Name:        "list_tasks",
		Description: "The user's tasks with their state, status line and session status. Optionally only one epic's.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"epic":{"type":"string"}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "task_status",
		Description: "One task: what it is, the state and status line its agent set, what it is waiting for, and whether its session is running.",
		InputSchema: json.RawMessage(`{"type":"object","required":["taskId"],"properties":{"taskId":{"type":"string"}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "task_output",
		Description: "The tail of a task's terminal, as text. Use it to see what its agent is actually doing.",
		InputSchema: json.RawMessage(`{"type":"object","required":["taskId"],"properties":{"taskId":{"type":"string"},"lines":{"type":"integer","minimum":1,"maximum":500}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "restart_task",
		Description: "Restart a stopped task. fresh: start the agent's conversation again instead of resuming. rebuildContainer: recreate its devcontainer.",
		InputSchema: json.RawMessage(`{"type":"object","required":["taskId"],"properties":{"taskId":{"type":"string"},"fresh":{"type":"boolean"},"rebuildContainer":{"type":"boolean"}}}`),
	},
	{
		Name: "send_to_task",
		Description: "Type a line into a task's terminal — how you answer a task that marked itself blocked. " +
			"A task that isn't blocked waits for the user's approval first.",
		InputSchema: json.RawMessage(`{"type":"object","required":["taskId","text"],"properties":{"taskId":{"type":"string"},"text":{"type":"string","minLength":1,"maxLength":4000}}}`),
	},
	{
		Name: "wait_for_events",
		Description: "Wait for something to happen to the user's tasks: a task changed state or status, a session exited, a task started, " +
			"or an approval is waiting. Returns what happened and a cursor to continue from. Use this instead of polling.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"since":{"type":"integer","minimum":0},"timeoutSeconds":{"type":"integer","minimum":1,"maximum":60}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
}

// orchestratorToolNames is the set of names above, for dispatch.
var orchestratorToolNames = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range orchestratorTools {
		m[t.Name] = true
	}
	return m
}()

// callOrchestrator runs one of §4.18.1's tools.
func (s *Server) callOrchestrator(ctx context.Context, userID, name string, args json.RawMessage) (string, bool) {
	switch name {
	case "list_hosts":
		return s.listHosts(userID)
	case "list_profiles":
		return s.listProfiles(userID)
	case "list_notes":
		return s.listNotes(userID)
	case "create_task":
		return s.createTask(userID, args)
	case "list_tasks":
		return s.listTasks(userID, args)
	case "task_status":
		return s.taskStatus(userID, args)
	case "task_output":
		return s.taskOutput(userID, args)
	case "restart_task":
		return s.restartTask(userID, args)
	case "send_to_task":
		return s.sendToTask(ctx, userID, args)
	case "wait_for_events":
		return s.waitForEvents(ctx, userID, args)
	}
	return "unknown tool " + name, true
}

func asJSON(v any) (string, bool) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "could not encode the result", true
	}
	return string(b), false
}

func (s *Server) listHosts(userID string) (string, bool) {
	if s.Hosts == nil {
		return "hosts are not available", true
	}
	store, err := s.Hosts.For(userID)
	if err != nil {
		return "could not read the hosts", true
	}
	type hostJSON struct {
		ID, Name, Group, OS string
	}
	out := []hostJSON{}
	for _, h := range store.List() {
		out = append(out, hostJSON{ID: h.ID, Name: h.Name, Group: h.Group, OS: string(h.TargetOS)})
	}
	return asJSON(map[string]any{"hosts": out})
}

func (s *Server) listProfiles(userID string) (string, bool) {
	if s.Agents == nil {
		return "profiles are not available", true
	}
	store, err := s.Agents.For(userID)
	if err != nil {
		return "could not read the profiles", true
	}
	settings := store.Get()
	type profileJSON struct {
		ID, Name, Agent, Model string
		Default                bool
	}
	out := []profileJSON{}
	for _, p := range settings.Profiles {
		out = append(out, profileJSON{ID: p.ID, Name: p.Name, Agent: string(p.Agent), Model: p.Model,
			Default: p.ID == settings.TaskDefaults.ProfileID})
	}
	return asJSON(map[string]any{"profiles": out, "defaultHostId": settings.TaskDefaults.HostID})
}

func (s *Server) listNotes(userID string) (string, bool) {
	if s.Notes == nil {
		return "notes are not available", true
	}
	notes, err := s.Notes.TaskNotes(userID)
	if err != nil {
		return "could not read the notes", true
	}
	out := []map[string]string{}
	for _, n := range notes {
		out = append(out, map[string]string{"slug": n.Slug, "title": n.Title, "body": n.Body})
	}
	return asJSON(map[string]any{"notes": out})
}

// createTaskArgs is the orchestrator's flattened form of a TaskSpec.
type createTaskArgs struct {
	Name         string              `json:"name"`
	Request      string              `json:"request"`
	HostID       string              `json:"hostId"`
	Target       string              `json:"target"`
	Epic         string              `json:"epic"`
	ProfileID    string              `json:"profileId"`
	Model        string              `json:"model"`
	Mode         string              `json:"mode"`
	Repo         *tasks.Repo         `json:"repo"`
	Devcontainer *tasks.Devcontainer `json:"devcontainer"`
}

func (s *Server) createTask(userID string, raw json.RawMessage) (string, bool) {
	var a createTaskArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "invalid input: " + err.Error(), true
	}
	profileID := a.ProfileID
	if profileID == "" && s.Agents != nil {
		if store, err := s.Agents.For(userID); err == nil {
			profileID = store.Get().TaskDefaults.ProfileID
		}
	}
	if profileID == "" {
		return "no profile given and the user has no default profile: call list_profiles and pick one", true
	}
	spec := tasks.Spec{
		Name: a.Name, Epic: a.Epic, HostID: a.HostID, Target: a.Target,
		Repo: a.Repo, Devcontainer: a.Devcontainer, Request: a.Request,
		Agent: tasks.AgentSpec{ProfileID: profileID, Model: a.Model, Mode: a.Mode},
	}
	if spec.HostID == "" && spec.Target == "" && s.Agents != nil {
		if store, err := s.Agents.For(userID); err == nil {
			if def := store.Get().TaskDefaults.HostID; def == "local" {
				spec.Target = "local"
			} else {
				spec.HostID = def
			}
		}
	}
	info, err := s.Tasks.Create(userID, spec)
	if err != nil {
		return "could not start the task: " + err.Error(), true
	}
	// The manager already pushed taskCreated.
	return asJSON(map[string]any{
		"taskId": info.TaskID, "sessionId": info.ID, "name": info.Name, "epic": info.Group,
		"note": "The task is running. Its agent starts in " + spec.Agent.Mode + " mode; watch it with wait_for_events or task_status.",
	})
}

// taskView is one task as the orchestrator sees it.
func (s *Server) taskView(userID string, t tasks.Task) map[string]any {
	status := "unknown"
	if s.Sessions != nil {
		if info, err := s.Sessions.Get(t.SessionID, userID); err == nil {
			status = string(info.Status)
		} else if err == session.ErrNotFound {
			status = "gone"
		}
	}
	v := map[string]any{
		"taskId": t.ID, "name": t.Spec.Name, "epic": t.Spec.Epic, "session": status,
		"state": t.State, "summary": t.Summary, "kind": t.Kind,
	}
	if t.Question != "" {
		v["question"] = t.Question
	}
	if t.Spec.Repo != nil {
		v["repo"] = t.Spec.Repo.URL
	}
	return v
}

func (s *Server) listTasks(userID string, raw json.RawMessage) (string, bool) {
	var a struct {
		Epic string `json:"epic"`
	}
	_ = json.Unmarshal(raw, &a)
	list, err := s.Tasks.List(userID)
	if err != nil {
		return "could not list the tasks", true
	}
	out := []map[string]any{}
	for _, t := range list {
		if t.Kind == tasks.KindOrchestrator {
			continue
		}
		if a.Epic != "" && !strings.EqualFold(a.Epic, t.Spec.Epic) {
			continue
		}
		out = append(out, s.taskView(userID, t))
	}
	return asJSON(map[string]any{"tasks": out})
}

func (s *Server) taskArg(userID string, raw json.RawMessage) (tasks.Task, string, bool) {
	var a struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.TaskID == "" {
		return tasks.Task{}, "taskId is required", true
	}
	t, err := s.Tasks.Get(userID, a.TaskID)
	if err != nil {
		return tasks.Task{}, "no such task: " + a.TaskID, true
	}
	return t, "", false
}

func (s *Server) taskStatus(userID string, raw json.RawMessage) (string, bool) {
	t, msg, bad := s.taskArg(userID, raw)
	if bad {
		return msg, true
	}
	return asJSON(s.taskView(userID, t))
}

// ansi strips escape sequences from replayed terminal output: the
// orchestrator wants what the screen says, not how it was drawn.
var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]|[\x00-\x08\x0b\x0c\x0e-\x1f]`)

func (s *Server) taskOutput(userID string, raw json.RawMessage) (string, bool) {
	t, msg, bad := s.taskArg(userID, raw)
	if bad {
		return msg, true
	}
	var a struct {
		Lines int `json:"lines"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.Lines <= 0 || a.Lines > 500 {
		a.Lines = 80
	}
	if s.Sessions == nil {
		return "sessions are not available", true
	}
	out, err := s.Sessions.Output(t.SessionID, userID, 256<<10)
	if err != nil {
		return "could not read the task's terminal: " + err.Error(), true
	}
	text := ansi.ReplaceAllString(strings.ReplaceAll(string(out), "\r\n", "\n"), "")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > a.Lines {
		lines = lines[len(lines)-a.Lines:]
	}
	return asJSON(map[string]any{"taskId": t.ID, "lines": lines})
}

func (s *Server) restartTask(userID string, raw json.RawMessage) (string, bool) {
	t, msg, bad := s.taskArg(userID, raw)
	if bad {
		return msg, true
	}
	var a struct {
		Fresh            bool `json:"fresh"`
		RebuildContainer bool `json:"rebuildContainer"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.Fresh || a.RebuildContainer {
		s.Tasks.RequestRestart(t.ID, tasks.RestartOptions{Fresh: a.Fresh, RebuildContainer: a.RebuildContainer})
		defer s.Tasks.ClearRestart(t.ID)
	}
	if s.Sessions == nil {
		return "sessions are not available", true
	}
	if _, err := s.Sessions.Restart(t.SessionID, userID); err != nil {
		return "could not restart the task: " + err.Error(), true
	}
	return asJSON(map[string]any{"taskId": t.ID, "restarted": true, "fresh": a.Fresh})
}

func (s *Server) sendToTask(ctx context.Context, userID string, raw json.RawMessage) (string, bool) {
	t, msg, bad := s.taskArg(userID, raw)
	if bad {
		return msg, true
	}
	var a struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &a)
	text := strings.TrimRight(a.Text, "\r\n")
	if strings.TrimSpace(text) == "" {
		return "text is required", true
	}
	if s.Sessions == nil {
		return "sessions are not available", true
	}
	// Answering a task that asked is the point; nudging one that didn't is
	// the user's call (§4.18.1).
	if t.State != tasks.StateBlocked {
		callID := newCallID()
		approved, status := s.awaitApproval(ctx, userID, t.ID, callID, "send_to_task", raw)
		if !approved {
			if status == "expired" {
				return "Not sent: the user didn't approve this within " + approvalTimeout.String() + ".", true
			}
			return "Not sent: denied by the user.", true
		}
	}
	if err := s.Sessions.Input(t.SessionID, userID, []byte(text+"\r")); err != nil {
		return "could not reach the task's terminal: " + err.Error(), true
	}
	// It answered; the task is working again until it says otherwise.
	if t.State == tasks.StateBlocked {
		if err := s.Tasks.SetState(t.ID, tasks.StateWorking, t.Summary, ""); err == nil {
			s.pushEvent(userID, Event{Type: "taskState", TaskID: t.ID, State: tasks.StateWorking, Summary: t.Summary})
			s.publish(userID, StateMsg{Type: "taskState", TaskID: t.ID, State: tasks.StateWorking, Summary: t.Summary})
		}
	}
	return asJSON(map[string]any{"taskId": t.ID, "sent": true})
}

func (s *Server) waitForEvents(ctx context.Context, userID string, raw json.RawMessage) (string, bool) {
	var a struct {
		Since          *uint64 `json:"since"`
		TimeoutSeconds int     `json:"timeoutSeconds"`
	}
	_ = json.Unmarshal(raw, &a)
	timeout := 25 * time.Second
	if a.TimeoutSeconds > 0 {
		timeout = time.Duration(min(a.TimeoutSeconds, 60)) * time.Second
	}
	q := s.queue(userID)
	events, cursor := q.wait(ctx, a.Since, timeout)
	if events == nil {
		events = []Event{}
	}
	note := ""
	if len(events) == 0 {
		note = fmt.Sprintf("Nothing happened in %s. Call again with since=%d to keep waiting.", timeout, cursor)
	}
	return asJSON(map[string]any{"events": events, "cursor": cursor, "note": note})
}
