// Package mcp is the `sessile` MCP server a task's agent reaches its tools
// through (PROJECT_PLAN.md §4.17.3): the user's ready scripts, one tool per
// function, plus set_task_summary and task_info.
//
// It speaks MCP's stdio framing — newline-delimited JSON-RPC 2.0 — over one
// stream per agent connection. The stream arrives through a reverse forward
// on the task session's own SSH connection (tasks opens it), and the agent's
// end is the cmd/sessile-mcp bridge. A hand-written handler for the four
// methods a tool server needs: no MCP SDK (§2).
//
// Write-effect calls wait for the user's approval in sessile. Pre-allowing
// sessile's tools in the agent's own settings doesn't get past that: the
// gate is here, not in the CLI.
package mcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Andste82/sessile/backend/internal/agents"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/notes"
	"github.com/Andste82/sessile/backend/internal/scripts"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

// approvalTimeout is how long a write call waits for the user (§4.17.3).
const approvalTimeout = 5 * time.Minute

// maxLine bounds one JSON-RPC message from the agent.
const maxLine = 4 << 20

// Sessions is what the orchestrator's tools need from the session manager
// (§4.18.1). Every method is owner-scoped.
type Sessions interface {
	Get(id, userID string) (session.Info, error)
	Restart(id, userID string) (session.Info, error)
	Output(id, userID string, max int) ([]byte, error)
	Input(id, userID string, data []byte) error
}

// Server serves the sessile tools to every task's agent.
type Server struct {
	Scripts      *scripts.Store
	Runner       *scripts.Runner
	Tasks        *tasks.Service
	Publish      func(userID string, v any)
	AllowScripts func() bool
	Version      string
	Log          *slog.Logger

	// The orchestrator's view of sessile (§4.18.1). Unset in a task-only
	// server: its tools then report that they are unavailable rather than
	// panicking.
	Hosts    *hosts.Registry
	Agents   *agents.Registry
	Notes    *notes.Store
	Sessions Sessions

	mu        sync.Mutex
	conns     map[string]map[*conn]struct{} // by user id
	approvals map[string]*approval          // by call id
	questions map[string]*question          // held asks, by call id
	events    map[string]*eventQueue        // by user id
}

type approval struct {
	userID, taskID string
	name           string
	input          json.RawMessage
	decided        chan bool
}

// New returns a Server.
func New(store *scripts.Store, runner *scripts.Runner, svc *tasks.Service, publish func(string, any), log *slog.Logger) *Server {
	return &Server{Scripts: store, Runner: runner, Tasks: svc, Publish: publish, Log: log,
		AllowScripts: func() bool { return true },
		conns:        map[string]map[*conn]struct{}{}, approvals: map[string]*approval{},
		questions: map[string]*question{}, events: map[string]*eventQueue{}}
}

// Serve accepts agent connections on l until it closes — which, for an SSH
// task, is when the session's connection goes away. token is this start's
// secret; every connection has to present it first. scope decides which tools
// the connection gets (§4.18.1): tasks.ScopeTask, or the orchestrator's.
func (s *Server) Serve(l net.Listener, userID, taskID, token, scope string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go s.handle(c, userID, taskID, token, scope)
	}
}

type conn struct {
	s                     *Server
	userID, taskID, scope string
	w                     io.Writer
	wmu                   sync.Mutex
}

func (c *conn) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.w.Write(append(b, '\n'))
}

func (s *Server) handle(nc net.Conn, userID, taskID, token, scope string) {
	defer nc.Close()
	r := bufio.NewReaderSize(nc, 64<<10)

	// The bridge's first line is the task's token (§4.17.3).
	_ = nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	got := strings.TrimSpace(strings.TrimPrefix(line, "SESSILE-TOKEN "))
	if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		return
	}
	_ = nc.SetReadDeadline(time.Time{})

	c := &conn{s: s, userID: userID, taskID: taskID, scope: scope, w: nc}
	s.mu.Lock()
	if s.conns[userID] == nil {
		s.conns[userID] = map[*conn]struct{}{}
	}
	s.conns[userID][c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns[userID], c)
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	var inflight sync.WaitGroup
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), maxLine)
	for scanner.Scan() {
		msg := append([]byte(nil), scanner.Bytes()...)
		if len(strings.TrimSpace(string(msg))) == 0 {
			continue
		}
		// Calls run concurrently: a held write must not block reads.
		inflight.Add(1)
		go func() {
			defer inflight.Done()
			c.dispatch(ctx, msg)
		}()
	}
	// The agent closed its side: let the calls it already made answer before
	// the connection goes. A read error (the tunnel itself went away)
	// cancels them instead — there is no one left to answer.
	if scanner.Err() != nil {
		cancel()
	}
	inflight.Wait()
	cancel()
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

func (c *conn) dispatch(ctx context.Context, raw []byte) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		c.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	isNotification := len(req.ID) == 0
	reply := func(result any, err *rpcError) {
		if isNotification {
			return
		}
		c.send(response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: err})
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		reply(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "sessile", "version": c.s.Version},
			"instructions":    instructions(c.scope),
		}, nil)
	case "notifications/initialized", "notifications/cancelled":
	case "ping":
		reply(map[string]any{}, nil)
	case "tools/list":
		reply(map[string]any{"tools": c.s.tools(c.userID, c.scope)}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			reply(nil, &rpcError{Code: -32602, Message: "invalid params"})
			return
		}
		text, isErr := c.s.call(ctx, c.userID, c.taskID, c.scope, p.Name, p.Arguments)
		reply(map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}, nil)
	default:
		reply(nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

// instructions is the server's one-paragraph introduction, per scope.
func instructions(scope string) string {
	if scope == tasks.ScopeOrchestrator {
		return "Tools from sessile: the user's hosts, agent profiles and notes, their tasks — " +
			"start one, read its state and terminal, restart it, answer it — and their own scripts " +
			"(tickets, CI, artifacts). wait_for_events tells you when something happens."
	}
	return "Tools from sessile. The work is on this task's host, not on this machine: read_file, write_file, " +
		"edit_file, list_dir, glob, grep and run reach it. Also the user's own scripts (tickets, CI, artifacts) " +
		"and this task's status line. Calls marked as writes wait for the user's approval in sessile."
}

// Tool is one MCP tool.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations map[string]any  `json:"annotations,omitempty"`
}

var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// builtins are the task's own tools.
var builtins = []Tool{
	{
		Name: "set_task_state",
		Description: "Tell sessile what you are doing: working, blocked (you need a decision — say what in `question`) or done. " +
			"Mark yourself blocked instead of waiting silently: that is how the user and the orchestrator find out you are stuck.",
		InputSchema: json.RawMessage(`{"type":"object","required":["state"],"properties":{
			"state":{"type":"string","enum":["working","blocked","done"]},
			"summary":{"type":"string","maxLength":200,"description":"One-line status; keeps the sidebar current"},
			"question":{"type":"string","maxLength":2000,"description":"What you are waiting for, when blocked"}}}`),
	},
	{
		Name:        "set_task_summary",
		Description: "Set this task's one-line status, shown to the user in sessile's sidebar and dashboard. Keep it current: e.g. \"Plan approved, implementing\", \"PR #412 open, CI running\".",
		InputSchema: json.RawMessage(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string","minLength":1,"maxLength":200}}}`),
	},
	{
		Name:        "task_info",
		Description: "This task's setup: its name, host, main repository, devcontainer and agent mode.",
		InputSchema: emptySchema,
		Annotations: map[string]any{"readOnlyHint": true},
	},
}

// ready is a script whose tools a task agent may use: installed, set up, its
// venv not broken and its last check (if any) not failed (§4.17.2).
type ready struct {
	meta     scripts.Meta
	settings scripts.Settings
	dir      string
}

func (s *Server) readyScripts(userID string) []ready {
	if s.Scripts == nil || s.Runner == nil || (s.AllowScripts != nil && !s.AllowScripts()) {
		return nil
	}
	list, _ := s.Scripts.List(userID)
	var out []ready
	for _, m := range list {
		st, err := s.Scripts.GetSettings(userID, m.Name)
		if err != nil || len(scripts.Missing(m, st)) > 0 {
			continue
		}
		dir := s.Scripts.Dir(userID, m.Name)
		if v, _ := s.Runner.VenvStatus(m, dir); v == scripts.VenvFailed {
			continue
		}
		if c, ok := s.Runner.LastCheck(userID, m.Name); ok && !c.OK {
			continue
		}
		out = append(out, ready{meta: m, settings: st, dir: dir})
	}
	return out
}

// contextLine is the script's description plus its context settings, e.g.
// "Jira issue tracker. Server URL: https://jira.example.com; Default project
// key: DBG" (§4.15.2). Secrets never appear: they can't be context.
func contextLine(r ready) string {
	var parts []string
	for _, d := range r.meta.Settings {
		if d.Context && d.Type != "secret" && r.settings.Values[d.Name] != "" {
			parts = append(parts, d.Label+": "+r.settings.Values[d.Name])
		}
	}
	line := strings.TrimSpace(r.meta.Description)
	if len(parts) > 0 {
		line += " " + strings.Join(parts, "; ")
	}
	return line
}

func toolName(script, fn string) string { return script + "__" + fn }

func (s *Server) tools(userID, scope string) []Tool {
	var out []Tool
	if scope == tasks.ScopeOrchestrator {
		out = append(out, orchestratorTools...)
	} else {
		// A task's agent runs on the server; these are how it reaches the
		// machine its work is on (§4.12.4).
		out = append(out, hostTools...)
		out = append(out, askTool)
		out = append(out, builtins...)
	}
	for _, r := range s.readyScripts(userID) {
		ctx := contextLine(r)
		for _, f := range r.meta.Functions {
			schema := f.Input
			if len(schema) == 0 {
				schema = emptySchema
			}
			desc := f.Description
			if ctx != "" {
				desc += " (" + r.meta.Name + ": " + ctx + ")"
			}
			if f.Effect == scripts.EffectWrite {
				desc += " This changes something in the service and waits for the user's approval."
			}
			t := Tool{Name: toolName(r.meta.Name, f.Name), Description: desc, InputSchema: schema}
			if f.Effect == scripts.EffectRead {
				t.Annotations = map[string]any{"readOnlyHint": true}
			} else {
				t.Annotations = map[string]any{"destructiveHint": true}
			}
			out = append(out, t)
		}
	}
	return out
}

// Event messages on /ws/events (§5.3).
type ToolMsg struct {
	Type    string `json:"type"` // "taskTool"
	TaskID  string `json:"taskId"`
	CallID  string `json:"callId"`
	Name    string `json:"name"`
	Status  string `json:"status"` // running | ok | error | denied
	Message string `json:"message,omitempty"`
}

type ApprovalMsg struct {
	Type   string          `json:"type"` // "taskApproval"
	TaskID string          `json:"taskId"`
	CallID string          `json:"callId"`
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input,omitempty"`
	Status string          `json:"status"` // pending | approved | denied | expired
}

// StateMsg carries a task's state and question (§4.18.2, §5.3).
type StateMsg struct {
	Type     string `json:"type"` // "taskState"
	TaskID   string `json:"taskId"`
	State    string `json:"state"`
	Summary  string `json:"summary,omitempty"`
	Question string `json:"question,omitempty"`
}

type SummaryMsg struct {
	Type    string `json:"type"` // "taskSummary"
	TaskID  string `json:"taskId"`
	Summary string `json:"summary"`
}

func (s *Server) publish(userID string, v any) {
	if s.Publish != nil {
		s.Publish(userID, v)
	}
}

func newCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// call runs one tool call and returns its text result and whether it failed.
func (s *Server) call(ctx context.Context, userID, taskID, scope, name string, args json.RawMessage) (string, bool) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if scope == tasks.ScopeOrchestrator {
		if orchestratorToolNames[name] {
			return s.callOrchestrator(ctx, userID, name, args)
		}
		if hostToolNames[name] {
			// The orchestrator manages tasks; it does not do their work.
			return "unknown tool " + name + " — start a task for work on a host", true
		}
	} else if orchestratorToolNames[name] {
		// A task's agent gets its own task's tools, never sessile's (§4.18.1).
		return "unknown tool " + name, true
	} else if hostToolNames[name] {
		return s.callHost(ctx, userID, taskID, name, args)
	}
	if name == askTool.Name {
		if scope == tasks.ScopeOrchestrator {
			// The orchestrator is talking to the user already: it asks in its
			// own terminal, where they are.
			return "unknown tool " + name + " — ask the user here, in this conversation", true
		}
		return s.callAsk(ctx, userID, taskID, args)
	}
	switch name {
	case "set_task_state":
		if scope == tasks.ScopeOrchestrator {
			return "unknown tool " + name, true
		}
		var p struct {
			State    string `json:"state"`
			Summary  string `json:"summary"`
			Question string `json:"question"`
		}
		if err := json.Unmarshal(args, &p); err != nil || !tasks.ValidState(p.State) {
			return "state must be working, blocked or done", true
		}
		summary := clip(p.Summary, 200)
		question := clip(p.Question, 2000)
		if summary == "" {
			if t, err := s.Tasks.Get(userID, taskID); err == nil {
				summary = t.Summary
			}
		}
		if p.State != tasks.StateBlocked {
			question = ""
		}
		if err := s.Tasks.SetState(taskID, p.State, summary, question); err != nil {
			return "could not save the state", true
		}
		msg := StateMsg{Type: "taskState", TaskID: taskID, State: p.State, Summary: summary, Question: question}
		s.publish(userID, msg)
		s.pushEvent(userID, Event{Type: "taskState", TaskID: taskID, State: p.State, Summary: summary, Question: question})
		if p.State == tasks.StateBlocked {
			return "Saved. The user and the orchestrator can see you are waiting; keep reading this terminal for their answer.", false
		}
		return "Saved.", false
	case "set_task_summary":
		if scope == tasks.ScopeOrchestrator {
			return "unknown tool " + name, true
		}
		var p struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal(args, &p); err != nil || strings.TrimSpace(p.Summary) == "" {
			return "summary is required", true
		}
		summary := strings.TrimSpace(strings.ReplaceAll(p.Summary, "\n", " "))
		if len([]rune(summary)) > 200 {
			summary = string([]rune(summary)[:200])
		}
		if err := s.Tasks.SetSummary(taskID, summary); err != nil {
			return "could not save the summary", true
		}
		s.publish(userID, SummaryMsg{Type: "taskSummary", TaskID: taskID, Summary: summary})
		s.pushEvent(userID, Event{Type: "taskSummary", TaskID: taskID, Summary: summary})
		return "Saved.", false
	case "task_info":
		if scope == tasks.ScopeOrchestrator {
			return "unknown tool " + name, true
		}
		t, err := s.Tasks.Get(userID, taskID)
		if err != nil {
			return "task not found", true
		}
		b, _ := json.MarshalIndent(map[string]any{
			"id": t.ID, "name": t.Spec.Name, "dir": t.Dir, "repo": t.Spec.Repo,
			"devcontainer": t.Spec.Devcontainer, "mode": t.Spec.Agent.Mode, "summary": t.Summary,
		}, "", "  ")
		return string(b), false
	}

	script, fn, ok := strings.Cut(name, "__")
	if !ok {
		return "unknown tool " + name, true
	}
	var r *ready
	for _, x := range s.readyScripts(userID) {
		if x.meta.Name == script {
			x := x
			r = &x
			break
		}
	}
	if r == nil {
		return "the script " + script + " is not installed or not set up in sessile", true
	}
	f, ok := r.meta.Function(fn)
	if !ok {
		return "unknown tool " + name, true
	}
	if err := scripts.ValidateInput(f, args); err != nil {
		return "invalid input: " + err.Error(), true
	}

	callID := newCallID()
	if f.Effect == scripts.EffectWrite {
		approved, status := s.awaitApproval(ctx, userID, taskID, callID, name, args)
		if !approved {
			s.publish(userID, ToolMsg{Type: "taskTool", TaskID: taskID, CallID: callID, Name: name, Status: "denied"})
			if status == "expired" {
				return "Not run: the user didn't approve this within " + approvalTimeout.String() + ".", true
			}
			return "Not run: denied by the user.", true
		}
	}
	s.publish(userID, ToolMsg{Type: "taskTool", TaskID: taskID, CallID: callID, Name: name, Status: "running"})
	res, err := s.Runner.Run(ctx, userID, r.meta, r.dir, r.settings, fn, args)
	if err != nil {
		msg := err.Error()
		s.publish(userID, ToolMsg{Type: "taskTool", TaskID: taskID, CallID: callID, Name: name, Status: "error", Message: msg})
		return msg, true
	}
	s.publish(userID, ToolMsg{Type: "taskTool", TaskID: taskID, CallID: callID, Name: name, Status: "ok"})
	return string(res.Output), false
}

func (s *Server) awaitApproval(ctx context.Context, userID, taskID, callID, name string, args json.RawMessage) (bool, string) {
	a := &approval{userID: userID, taskID: taskID, name: name, input: args, decided: make(chan bool, 1)}
	s.mu.Lock()
	s.approvals[callID] = a
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.approvals, callID)
		s.mu.Unlock()
	}()
	s.publish(userID, ApprovalMsg{Type: "taskApproval", TaskID: taskID, CallID: callID, Name: name, Input: args, Status: "pending"})
	status := "expired"
	approved := false
	select {
	case approved = <-a.decided:
		status = map[bool]string{true: "approved", false: "denied"}[approved]
	case <-time.After(approvalTimeout):
	case <-ctx.Done():
	}
	s.publish(userID, ApprovalMsg{Type: "taskApproval", TaskID: taskID, CallID: callID, Name: name, Status: status})
	return approved, status
}

// ErrNoSuchApproval is a decision for a call that isn't waiting (any more).
var ErrNoSuchApproval = errors.New("no such pending call")

// Decide answers a held write call, scoped to its user and task.
func (s *Server) Decide(userID, taskID, callID string, approve bool) error {
	s.mu.Lock()
	a, ok := s.approvals[callID]
	s.mu.Unlock()
	if !ok || a.userID != userID || a.taskID != taskID {
		return ErrNoSuchApproval
	}
	select {
	case a.decided <- approve:
		return nil
	default:
		return ErrNoSuchApproval
	}
}

// Pending lists a task's held write calls, for a task page that opens after
// the request was published.
func (s *Server) Pending(userID, taskID string) []ApprovalMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ApprovalMsg{}
	for id, a := range s.approvals {
		if a.userID == userID && a.taskID == taskID {
			out = append(out, ApprovalMsg{Type: "taskApproval", TaskID: taskID, CallID: id, Name: a.name, Input: a.input, Status: "pending"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CallID < out[j].CallID })
	return out
}

// ToolsChanged tells a user's connected agents that their tool list changed
// (a script installed, removed or set up), for CLIs that re-read it.
func (s *Server) ToolsChanged(userID string) {
	s.mu.Lock()
	var list []*conn
	for c := range s.conns[userID] {
		list = append(list, c)
	}
	s.mu.Unlock()
	for _, c := range list {
		c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
	}
}

// clip trims a one-line field to n runes.
func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// ToolsSection is tasks.ToolServer: the "Tools from sessile" section of a
// task's or the orchestrator's instructions (§4.17.2, §4.18.1), naming each
// ready script with what it is for and its guidance.
func (s *Server) ToolsSection(userID, scope string) string {
	var b strings.Builder
	b.WriteString("## Tools from sessile\n\n")
	b.WriteString("Call these through the `sessile` MCP server. Results come from the user's own\n")
	b.WriteString("services. Calls marked (write) wait for the user's approval in sessile.\n")
	list := s.readyScripts(userID)
	sort.Slice(list, func(i, j int) bool { return list[i].meta.Name < list[j].meta.Name })
	for _, r := range list {
		fmt.Fprintf(&b, "\n### %s: %s\n", r.meta.Name, contextLine(r))
		var fns []string
		for _, f := range r.meta.Functions {
			n := toolName(r.meta.Name, f.Name)
			if f.Effect == scripts.EffectWrite {
				n += " (write)"
			}
			fns = append(fns, n)
		}
		fmt.Fprintf(&b, "- %s\n", strings.Join(fns, ", "))
		for _, g := range r.meta.Guidance {
			fmt.Fprintf(&b, "- %s\n", g)
		}
	}
	if scope == tasks.ScopeOrchestrator {
		b.WriteString("\nAlso, sessile itself: `list_hosts`, `list_profiles`, `list_notes`, `create_task`,\n")
		b.WriteString("`list_tasks`, `task_status`, `task_output`, `restart_task`, `send_to_task` and\n")
		b.WriteString("`wait_for_events`.\n")
	} else {
		b.WriteString("\nAlso: `set_task_state` — working, blocked (say what you need) or done — \n")
		b.WriteString("`set_task_summary`, and `task_info`.\n")
	}
	return b.String()
}
