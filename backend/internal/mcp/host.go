package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Andste82/sessile/backend/internal/hosttools"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

// The host tools (PROJECT_PLAN.md §4.12.4, v0.9). The agent runs on the
// sessile server; these are how it reaches the machine the work is on, over
// the task's own SSH connection. Every one is scoped to the task's own host,
// and a task's agent can reach no other.

// hostTools are served in the task scope, beside the user's scripts.
var hostTools = []Tool{
	{
		Name:        "read_file",
		Description: "Read a file on the host, with line numbers. Paths are relative to the task folder, or absolute.",
		InputSchema: json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"First line to return, 0-based"},"limit":{"type":"integer","description":"How many lines"}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "write_file",
		Description: "Create or overwrite a file on the host.",
		InputSchema: json.RawMessage(`{"type":"object","required":["path","content"],"properties":{"path":{"type":"string"},"content":{"type":"string"}}}`),
	},
	{
		Name: "edit_file",
		Description: "Replace an exact string in a file on the host. old_string must appear exactly once " +
			"unless replace_all is set — include surrounding lines to make it unique.",
		InputSchema: json.RawMessage(`{"type":"object","required":["path","old_string","new_string"],"properties":{"path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean"}}}`),
	},
	{
		Name:        "list_dir",
		Description: "List one directory on the host.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "glob",
		Description: "Find files on the host by name pattern, e.g. \"*.py\". Searches the task folder unless another path is given.",
		InputSchema: json.RawMessage(`{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string"},"path":{"type":"string"}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name:        "grep",
		Description: "Search file contents on the host. Returns matching lines with their file and line number.",
		InputSchema: json.RawMessage(`{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string"},"path":{"type":"string"},"glob":{"type":"string","description":"Only files matching this pattern, e.g. \"*.go\""}}}`),
		Annotations: map[string]any{"readOnlyHint": true},
	},
	{
		Name: "run",
		Description: "Run a shell command on the host — builds, tests, git, anything. " +
			"It runs in the task's repo by default, or in cwd. Set where:\"container\" to run it inside the task's devcontainer. " +
			"Returns the exit code and the output; the user sees it live.",
		InputSchema: json.RawMessage(`{"type":"object","required":["command"],"properties":{
			"command":{"type":"string"},
			"cwd":{"type":"string","description":"Relative to the task folder, or absolute"},
			"where":{"type":"string","enum":["host","container"]},
			"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600}}}`),
	},
}

var hostToolNames = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range hostTools {
		m[t.Name] = true
	}
	return m
}()

// hostArgs is every argument the host tools take, in one shape.
type hostArgs struct {
	Path           string `json:"path"`
	Content        string `json:"content"`
	OldString      string `json:"old_string"`
	NewString      string `json:"new_string"`
	ReplaceAll     bool   `json:"replace_all"`
	Offset         int    `json:"offset"`
	Limit          int    `json:"limit"`
	Pattern        string `json:"pattern"`
	Glob           string `json:"glob"`
	Command        string `json:"command"`
	Cwd            string `json:"cwd"`
	Where          string `json:"where"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// callHost runs one host tool for a task.
func (s *Server) callHost(ctx context.Context, userID, taskID, name string, raw json.RawMessage) (string, bool) {
	var a hostArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "invalid input: " + err.Error(), true
	}
	client, dir, err := s.Tasks.Host(userID, taskID)
	if err != nil {
		if errors.Is(err, tasks.ErrLocalTask) {
			return "This task runs on the sessile server itself, so it has no host to reach.", true
		}
		return "could not reach the task's host: " + err.Error(), true
	}
	// A clone of any size runs while the agent is already reading its
	// instructions; a tool that needs the repo waits for it rather than
	// finding an empty folder (§4.12.2).
	if err := s.Tasks.Prepared(taskID).Wait(ctx, 2*time.Minute); err != nil {
		return err.Error(), true
	}
	repo := path(dir, "repo")

	switch name {
	case "read_file":
		out, err := client.ReadFile(hostPath(dir, repo, a.Path), a.Offset, a.Limit)
		if err != nil {
			return hostErr(err), true
		}
		return out, false

	case "write_file":
		if err := client.WriteFile(hostPath(dir, repo, a.Path), a.Content); err != nil {
			return hostErr(err), true
		}
		return fmt.Sprintf("Wrote %d bytes to %s.", len(a.Content), a.Path), false

	case "edit_file":
		n, err := client.EditFile(hostPath(dir, repo, a.Path), a.OldString, a.NewString, a.ReplaceAll)
		if err != nil {
			return hostErr(err), true
		}
		return fmt.Sprintf("Replaced %d occurrence(s) in %s.", n, a.Path), false

	case "list_dir":
		entries, err := client.ListDir(hostPath(dir, repo, a.Path))
		if err != nil {
			return hostErr(err), true
		}
		var b strings.Builder
		for _, e := range entries {
			if e.IsDir {
				fmt.Fprintf(&b, "%s/\n", e.Name)
			} else {
				fmt.Fprintf(&b, "%s\t%d\n", e.Name, e.Size)
			}
		}
		if b.Len() == 0 {
			return "(empty directory)", false
		}
		return b.String(), false

	case "glob":
		out, err := client.Glob(ctx, hostPath(dir, repo, a.Path), a.Pattern)
		if err != nil {
			return hostErr(err), true
		}
		if strings.TrimSpace(out) == "" {
			return "(nothing matched)", false
		}
		return out, false

	case "grep":
		out, err := client.Grep(ctx, hostPath(dir, repo, a.Path), a.Pattern, a.Glob)
		if err != nil {
			return hostErr(err), true
		}
		if strings.TrimSpace(out) == "" {
			return "(no matches)", false
		}
		return out, false

	case "run":
		return s.runOnHost(ctx, userID, taskID, client, dir, repo, a)
	}
	return "unknown tool " + name, true
}

// runOnHost is `run`: one command on the host, logged and streamed to the
// task panel as it goes, so the user watches the build rather than waiting
// for a verdict (§4.12.4).
func (s *Server) runOnHost(ctx context.Context, userID, taskID string, client *hosttools.Client, dir, repo string, a hostArgs) (string, bool) {
	if strings.TrimSpace(a.Command) == "" {
		return "command is required", true
	}
	cwd := repo
	if a.Cwd != "" {
		cwd = hostPath(dir, repo, a.Cwd)
	}
	callID := newCallID()
	s.publish(userID, RunMsg{Type: "taskRun", TaskID: taskID, CallID: callID,
		Command: a.Command, Cwd: cwd, Status: "running"})

	// A build writes faster than anyone can read it, and every event goes to
	// every attached browser, so output is coalesced into one message per
	// tick rather than one per write (§5, "broadcasts must never block").
	out := &outputPump{publish: func(chunk string) {
		s.publish(userID, RunMsg{Type: "taskRun", TaskID: taskID, CallID: callID,
			Status: "output", Output: chunk})
	}}
	stop := out.start(200 * time.Millisecond)

	req := hosttools.RunRequest{
		Command: a.Command, Dir: cwd,
		Timeout:   time.Duration(a.TimeoutSeconds) * time.Second,
		Container: a.Where == "container",
		Workspace: repo,
		Env:       s.gitEnvFor(userID, taskID),
		OnOutput:  out.write,
	}
	res, err := client.Run(ctx, req)
	stop()
	status := "ok"
	if err != nil || res.ExitCode != 0 {
		status = "error"
	}
	s.publish(userID, RunMsg{Type: "taskRun", TaskID: taskID, CallID: callID,
		Status: status, ExitCode: res.ExitCode})
	if s.Log != nil {
		s.Log.Info("task ran a command on its host",
			"userId", userID, "taskId", taskID, "exit", res.ExitCode,
			"where", a.Where, "command", a.Command)
	}
	if err != nil {
		if res.TimedOut {
			return fmt.Sprintf("%s\n\n%s", err, res.Output), true
		}
		return "could not run it: " + err.Error(), true
	}
	text := res.Output
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	if res.Truncated {
		text = "... earlier output dropped ...\n" + text
	}
	// The directory is on the line, because a command that assumed a
	// different one ("cd repo" when it is already in the repo) otherwise
	// fails with no clue why.
	return fmt.Sprintf("exit %d (in %s)\n%s", res.ExitCode, cwd, text), false
}

// outputPump coalesces a command's output into one message per tick.
type outputPump struct {
	mu      sync.Mutex
	buf     strings.Builder
	publish func(string)
	done    chan struct{}
}

func (p *outputPump) write(b []byte) {
	p.mu.Lock()
	p.buf.Write(b)
	p.mu.Unlock()
}

// flush sends whatever has arrived since the last one.
func (p *outputPump) flush() {
	p.mu.Lock()
	chunk := p.buf.String()
	p.buf.Reset()
	p.mu.Unlock()
	if chunk != "" {
		p.publish(chunk)
	}
}

// start pumps until the returned stop is called, which flushes the rest.
func (p *outputPump) start(every time.Duration) func() {
	p.done = make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.flush()
			case <-p.done:
				return
			}
		}
	}()
	return func() {
		close(p.done)
		p.flush()
	}
}

// gitEnvFor is the task's Git credential environment, set on this one command
// so a `git push` works without anything being written to the host (§4.16).
func (s *Server) gitEnvFor(userID, taskID string) [][2]string {
	if s.Tasks == nil {
		return nil
	}
	return s.Tasks.GitEnv(userID, taskID)
}

// hostPath resolves a tool's path argument: absolute as given, and otherwise
// relative to the repo when there is one, so an agent can say "src/main.go"
// and mean what it says.
func hostPath(dir, repo, p string) string {
	switch {
	case p == "" || p == ".":
		return repo
	case strings.HasPrefix(p, "/"):
		return p
	case strings.HasPrefix(p, "~/"):
		return p
	}
	return path(repo, p)
}

// path joins host path elements. Always forward slashes: SFTP speaks them on
// every platform, Windows included.
func path(elem ...string) string {
	parts := make([]string, 0, len(elem))
	for _, e := range elem {
		if e = strings.TrimSuffix(e, "/"); e != "" {
			parts = append(parts, e)
		}
	}
	return strings.Join(parts, "/")
}

// hostErr turns a host-side failure into something an agent can act on.
func hostErr(err error) string {
	var notUnique *hosttools.ErrNotUnique
	switch {
	case errors.As(err, &notUnique):
		return notUnique.Error()
	case errors.Is(err, hosttools.ErrNoMatch):
		return "that text is not in the file — read it again and match it exactly"
	}
	return err.Error()
}

// RunMsg carries a command on the host to the task panel (§5.3).
type RunMsg struct {
	Type     string `json:"type"` // "taskRun"
	TaskID   string `json:"taskId"`
	CallID   string `json:"callId"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Status   string `json:"status"` // running | output | ok | error
	Output   string `json:"output,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
}
