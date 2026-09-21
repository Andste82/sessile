package mcp

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/hosttools"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

// A tool's path argument means what an agent expects it to mean: relative to
// the repository it is working in, absolute when it says so.
func TestHostPath(t *testing.T) {
	dir, repo := "/home/ob/sessile-tasks/dbg-142", "/home/ob/sessile-tasks/dbg-142/repo"
	tests := map[string]string{
		"":                 repo,
		".":                repo,
		"src/main.go":      repo + "/src/main.go",
		"/etc/hosts":       "/etc/hosts",
		"~/notes.md":       "~/notes.md",
		"../task.json":     repo + "/../task.json",
		"repos/other/x.py": repo + "/repos/other/x.py",
	}
	for in, want := range tests {
		if got := hostPath(dir, repo, in); got != want {
			t.Errorf("hostPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// An agent acts on what a failure tells it, so a failed edit has to say what
// to do next rather than just what went wrong.
func TestHostErrorsAreActionable(t *testing.T) {
	notUnique := hostErr(&hosttools.ErrNotUnique{Path: "invoice.py", Count: 3})
	for _, want := range []string{"3 times", "invoice.py", "replace_all"} {
		if !strings.Contains(notUnique, want) {
			t.Errorf("a non-unique edit should mention %q: %s", want, notUnique)
		}
	}
	if got := hostErr(hosttools.ErrNoMatch); !strings.Contains(got, "read it again") {
		t.Errorf("a missing match should say what to do: %s", got)
	}
	if got := hostErr(errors.New("permission denied")); got != "permission denied" {
		t.Errorf("an ordinary error should come through as it is: %s", got)
	}
}

// The two scopes stay apart: a task's agent reaches its host, the
// orchestrator manages tasks, and neither has the other's tools (§4.18.1).
func TestScopesDoNotOverlap(t *testing.T) {
	for _, tool := range hostTools {
		if orchestratorToolNames[tool.Name] {
			t.Errorf("%s is in both scopes", tool.Name)
		}
	}
	for _, name := range []string{"read_file", "write_file", "edit_file", "list_dir", "glob", "grep", "run"} {
		if !hostToolNames[name] {
			t.Errorf("the task scope is missing %s", name)
		}
	}
	// The agent that runs on the server must not be handed a host's shell in
	// the orchestrator scope.
	s := &Server{}
	if text, isErr := s.call(t.Context(), "u1", "", tasks.ScopeOrchestrator, "run",
		[]byte(`{"command":"whoami"}`)); !isErr || !strings.Contains(text, "start a task") {
		t.Errorf("the orchestrator should not run commands on hosts: %q", text)
	}
}

// Output from a command is coalesced: one message per tick, not one per write.
func TestOutputPumpCoalesces(t *testing.T) {
	var chunks []string
	p := &outputPump{publish: func(c string) { chunks = append(chunks, c) }}
	stop := p.start(time.Hour) // never ticks; the stop flushes
	p.write([]byte("compiling "))
	p.write([]byte("... done\n"))
	stop()
	if len(chunks) != 1 || chunks[0] != "compiling ... done\n" {
		t.Errorf("chunks = %q, want one coalesced message", chunks)
	}
}
