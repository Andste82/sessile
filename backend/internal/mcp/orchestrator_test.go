package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/agents"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/storage"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

// fakeSessions is the session manager as the orchestrator's tools see it.
type fakeSessions struct {
	mu       sync.Mutex
	info     map[string]session.Info
	output   []byte
	typed    []byte
	restarts int
	shells   int
}

func (f *fakeSessions) CreateTask(id, userID, name, taskID string) (session.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := session.Info{ID: id, UserID: userID, Name: name, TaskID: taskID, Status: session.StatusRunning, Group: "Tasks"}
	if f.info == nil {
		f.info = map[string]session.Info{}
	}
	f.info[id] = info
	return info, nil
}

func (f *fakeSessions) CreateTaskShell(id, userID, name, taskID string) (session.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info := session.Info{ID: id, UserID: userID, Name: name, TaskID: taskID,
		TargetType: session.TargetSSH, Status: session.StatusRunning, Group: "Tasks"}
	if f.info == nil {
		f.info = map[string]session.Info{}
	}
	f.info[id] = info
	f.shells++
	return info, nil
}

func (f *fakeSessions) Get(id, userID string) (session.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.info[id]
	if !ok || info.UserID != userID {
		return session.Info{}, session.ErrNotFound
	}
	return info, nil
}

func (f *fakeSessions) Restart(id, userID string) (session.Info, error) {
	info, err := f.Get(id, userID)
	if err != nil {
		return session.Info{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	return info, nil
}

func (f *fakeSessions) Output(id, userID string, max int) ([]byte, error) {
	if _, err := f.Get(id, userID); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.output, nil
}

func (f *fakeSessions) Input(id, userID string, data []byte) error {
	if _, err := f.Get(id, userID); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typed = append(f.typed, data...)
	return nil
}

// orchSetup is setup(t) for the orchestrator scope: a user with one host and
// one agent profile, and a fake session manager behind the tools.
func orchSetup(t *testing.T) (*Server, *fakeSessions, net.Listener) {
	t.Helper()
	data := t.TempDir()
	db, err := storage.Open(filepath.Join(data, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	hostsReg := hosts.NewRegistry(data)
	hs, err := hostsReg.For("u1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Create(hosts.Host{Name: "build-01", Group: "CI", Address: "build-01.example:22",
		Username: "dev", AuthMethod: hosts.AuthPassword, Password: "x", TargetOS: hosts.OSLinux}); err != nil {
		t.Fatal(err)
	}
	host := hs.List()[0]

	agentsReg := agents.NewRegistry(data)
	as, err := agentsReg.For("u1")
	if err != nil {
		t.Fatal(err)
	}
	connID, profileID := agents.NewID(), agents.NewID()
	if _, err := as.Update(func(s *agents.Settings) error {
		s.Connections = []agents.Connection{{ID: connID, Name: "sub", Kind: "claude-subscription", Fields: map[string]string{"token": "oat-x"}}}
		s.Profiles = []agents.Profile{{ID: profileID, Name: "Claude", Agent: agents.AgentClaude, ConnectionID: connID}}
		s.TaskDefaults = agents.TaskDefaults{HostID: host.ID, ProfileID: profileID}
		return nil
	}, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}

	sessions := &fakeSessions{output: []byte("\x1b[2Jbuilding\r\nready\r\n")}
	svc := &tasks.Service{DB: db, Agents: agentsReg, Hosts: hostsReg, Sessions: sessions}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(nil, nil, svc, func(string, any) {}, log)
	s.Hosts, s.Agents, s.Sessions = hostsReg, agentsReg, sessions

	l, err := net.Listen("unix", filepath.Join(t.TempDir(), "o.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go s.Serve(l, "u1", "orchestrator-abcdef", "the-token", tasks.ScopeOrchestrator)
	return s, sessions, l
}

// callTool calls one tool and returns its text result.
func callTool(t *testing.T, c *client, name string, args any) (string, bool) {
	t.Helper()
	reply := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	result, ok := reply["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: no result: %v", name, reply)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("%s: no content: %v", name, result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("not JSON: %s", text)
	}
	return v
}

func TestOrchestratorScopeServesSessileTools(t *testing.T) {
	_, _, l := orchSetup(t)
	c := connect(t, l, "the-token")

	reply := c.call("tools/list", map[string]any{})
	names := map[string]bool{}
	for _, tool := range reply["result"].(map[string]any)["tools"].([]any) {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"list_hosts", "list_profiles", "create_task", "list_tasks", "task_status", "task_output", "restart_task", "send_to_task", "wait_for_events"} {
		if !names[want] {
			t.Errorf("orchestrator is missing %s", want)
		}
	}
	// A task's own tools are not the orchestrator's (§4.18.1).
	for _, unwanted := range []string{"set_task_state", "set_task_summary", "task_info"} {
		if names[unwanted] {
			t.Errorf("orchestrator should not have %s", unwanted)
		}
	}
	if text, isErr := callTool(t, c, "set_task_state", map[string]any{"state": "done"}); !isErr {
		t.Errorf("set_task_state in the orchestrator scope should fail, got %q", text)
	}
}

func TestOrchestratorCreatesAndWatchesATask(t *testing.T) {
	s, sessions, l := orchSetup(t)
	c := connect(t, l, "the-token")

	if text := mustJSON(t, c, "list_hosts"); !strings.Contains(text, "build-01") {
		t.Errorf("list_hosts: %s", text)
	}
	if text := mustJSON(t, c, "list_profiles"); !strings.Contains(text, "Claude") {
		t.Errorf("list_profiles: %s", text)
	}

	// The user asked for it in the conversation: no approval, started at once.
	text, isErr := callTool(t, c, "create_task", map[string]any{
		"name": "DBG-142 print crash", "request": "Fix the crash on print", "epic": "DBG-142",
	})
	if isErr {
		t.Fatalf("create_task: %s", text)
	}
	created := decode(t, text)
	taskID, _ := created["taskId"].(string)
	if taskID == "" {
		t.Fatalf("create_task returned no task id: %s", text)
	}

	// It is the user's task, with its epic, and its session is running.
	listed := decode(t, mustJSON(t, c, "list_tasks"))
	tasksOut, _ := listed["tasks"].([]any)
	if len(tasksOut) != 1 {
		t.Fatalf("list_tasks: %v", listed)
	}
	first := tasksOut[0].(map[string]any)
	if first["epic"] != "DBG-142" || first["session"] != "running" {
		t.Errorf("list_tasks: %v", first)
	}
	// Filtering by another epic hides it.
	other, _ := callTool(t, c, "list_tasks", map[string]any{"epic": "OTHER"})
	if empty := decode(t, other)["tasks"].([]any); len(empty) != 0 {
		t.Errorf("list_tasks(epic) should filter: %s", other)
	}

	// task_output strips the escape sequences and keeps the last lines.
	out, isErr := callTool(t, c, "task_output", map[string]any{"taskId": taskID, "lines": 1})
	if isErr {
		t.Fatalf("task_output: %s", out)
	}
	lines := decode(t, out)["lines"].([]any)
	if len(lines) != 1 || lines[0] != "ready" {
		t.Errorf("task_output: %s", out)
	}

	// A blocked task is answered without an approval, and goes back to working.
	if err := s.Tasks.SetState(taskID, tasks.StateBlocked, "waiting", "Which branch?"); err != nil {
		t.Fatal(err)
	}
	if status := decode(t, mustJSONArgs(t, c, "task_status", map[string]any{"taskId": taskID})); status["question"] != "Which branch?" {
		t.Errorf("task_status: %v", status)
	}
	if text, isErr := callTool(t, c, "send_to_task", map[string]any{"taskId": taskID, "text": "main"}); isErr {
		t.Fatalf("send_to_task: %s", text)
	}
	sessions.mu.Lock()
	typed := string(sessions.typed)
	sessions.mu.Unlock()
	if typed != "main\r" {
		t.Errorf("typed %q, want %q", typed, "main\r")
	}
	after, err := s.Tasks.Get("u1", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != tasks.StateWorking || after.Question != "" {
		t.Errorf("answering a blocked task should un-block it: %+v", after)
	}

	// A follow-up instruction reaches a task that is working, with no
	// approval in between: the user said it to the orchestrator already.
	sessions.mu.Lock()
	sessions.typed = nil
	sessions.mu.Unlock()
	if text, isErr := callTool(t, c, "send_to_task", map[string]any{"taskId": taskID, "text": "also run it twice"}); isErr {
		t.Fatalf("send_to_task to a working task: %s", text)
	}
	sessions.mu.Lock()
	typed = string(sessions.typed)
	sessions.mu.Unlock()
	if typed != "also run it twice\r" {
		t.Errorf("typed %q into a working task, want the instruction delivered at once", typed)
	}
	if pending := s.Pending("u1", taskID); len(pending) != 0 {
		t.Errorf("a follow-up instruction must not wait for approval: %v", pending)
	}

	// restart_task goes through the manager.
	if text, isErr := callTool(t, c, "restart_task", map[string]any{"taskId": taskID, "fresh": true}); isErr {
		t.Fatalf("restart_task: %s", text)
	}
	sessions.mu.Lock()
	restarts := sessions.restarts
	sessions.mu.Unlock()
	if restarts != 1 {
		t.Errorf("restarts = %d, want 1", restarts)
	}
}

func TestOrchestratorToolsAreScopedToTheirUser(t *testing.T) {
	s, _, _ := orchSetup(t)
	// Another user's connection sees their own (empty) sessile, not u1's.
	text, isErr := s.callOrchestrator(context.Background(), "u2", "", "list_hosts", json.RawMessage("{}"))
	if isErr || strings.Contains(text, "build-01") {
		t.Errorf("list_hosts leaked across users: %s", text)
	}
	text, isErr = s.callOrchestrator(context.Background(), "u2", "", "task_status", json.RawMessage(`{"taskId":"demo-abcdef"}`))
	if !isErr {
		t.Errorf("task_status should not find another user's task: %s", text)
	}
}

func TestWaitForEventsReportsWhatHappened(t *testing.T) {
	s, _, l := orchSetup(t)
	c := connect(t, l, "the-token")

	// The first call has no cursor: it returns the current position at once.
	start := time.Now()
	first := decode(t, mustJSON(t, c, "wait_for_events"))
	if time.Since(start) > 5*time.Second {
		t.Fatal("the first wait_for_events should return immediately")
	}
	cursor := first["cursor"]

	go func() {
		time.Sleep(50 * time.Millisecond)
		s.pushEvent("u1", Event{Type: "taskState", TaskID: "demo-abcdef", State: tasks.StateBlocked, Question: "Which branch?"})
	}()
	text, isErr := callTool(t, c, "wait_for_events", map[string]any{"since": cursor, "timeoutSeconds": 5})
	if isErr {
		t.Fatalf("wait_for_events: %s", text)
	}
	events, _ := decode(t, text)["events"].([]any)
	if len(events) != 1 {
		t.Fatalf("wait_for_events: %s", text)
	}
	got := events[0].(map[string]any)
	if got["type"] != "taskState" || got["question"] != "Which branch?" {
		t.Errorf("event: %v", got)
	}

	// Another user's events are not this one's.
	s.pushEvent("u2", Event{Type: "taskExited", TaskID: "other-abcdef"})
	text, _ = callTool(t, c, "wait_for_events", map[string]any{"since": decode(t, text)["cursor"], "timeoutSeconds": 1})
	if events := decode(t, text)["events"].([]any); len(events) != 0 {
		t.Errorf("events leaked across users: %s", text)
	}
}

func TestEventRingKeepsTheLastEvents(t *testing.T) {
	q := newEventQueue()
	for i := 0; i < ringSize+50; i++ {
		q.push(Event{Type: "taskSummary", TaskID: "t"})
	}
	var zero uint64
	events, cursor := q.wait(context.Background(), &zero, time.Second)
	if len(events) != ringSize {
		t.Errorf("kept %d events, want %d", len(events), ringSize)
	}
	if cursor != uint64(ringSize+50) {
		t.Errorf("cursor = %d, want %d", cursor, ringSize+50)
	}
	// Continuing from the last cursor yields nothing and times out.
	events, _ = q.wait(context.Background(), &cursor, 50*time.Millisecond)
	if len(events) != 0 {
		t.Errorf("events after the cursor: %v", events)
	}
}

func TestTaskStateIsSetByTheTaskAgent(t *testing.T) {
	s, _, taskID, l := setup(t)
	c := connect(t, l, "the-token")

	text, isErr := callTool(t, c, "set_task_state", map[string]any{
		"state": "blocked", "summary": "needs a decision", "question": "Which branch?",
	})
	if isErr {
		t.Fatalf("set_task_state: %s", text)
	}
	got, err := s.Tasks.Get("u1", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != tasks.StateBlocked || got.Summary != "needs a decision" || got.Question != "Which branch?" {
		t.Errorf("task = %+v", got)
	}
	// The orchestrator hears about it.
	events, _ := s.queue("u1").wait(context.Background(), new(uint64), time.Second)
	if len(events) == 0 || events[len(events)-1].Type != "taskState" {
		t.Errorf("no taskState event: %v", events)
	}
	// Leaving blocked clears the question.
	if text, isErr := callTool(t, c, "set_task_state", map[string]any{"state": "done"}); isErr {
		t.Fatalf("set_task_state: %s", text)
	}
	if got, _ := s.Tasks.Get("u1", taskID); got.Question != "" || got.State != tasks.StateDone {
		t.Errorf("task = %+v", got)
	}
	if text, isErr := callTool(t, c, "set_task_state", map[string]any{"state": "sleeping"}); !isErr {
		t.Errorf("an unknown state should fail: %s", text)
	}
	// Nor does a task's agent get the orchestrator's tools.
	if text, isErr := callTool(t, c, "create_task", map[string]any{"name": "x", "request": "y"}); !isErr {
		t.Errorf("a task should not be able to create tasks: %s", text)
	}
}

func mustJSON(t *testing.T, c *client, name string) string {
	t.Helper()
	return mustJSONArgs(t, c, name, map[string]any{})
}

func mustJSONArgs(t *testing.T, c *client, name string, args any) string {
	t.Helper()
	text, isErr := callTool(t, c, name, args)
	if isErr {
		t.Fatalf("%s: %s", name, text)
	}
	return text
}
