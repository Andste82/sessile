package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Andste82/sessile/backend/internal/agents"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/storage"
)

// Errors a task start can fail with, beyond the session ones.
var (
	ErrNotFound          = errors.New("task not found")
	ErrProfileNotFound   = errors.New("the task's agent profile no longer exists")
	ErrConnectionExpired = errors.New("the profile's connection has expired; renew it in Agent settings")
	ErrUnsupportedTarget = errors.New("tasks aren't supported on this host's OS yet")
)

// NotesSource lists a user's notes for a task's context (§4.14). nil until
// notes exist.
type NotesSource interface {
	TaskNotes(userID string) ([]Note, error)
}

// Service creates tasks and launches their sessions. It is the
// session.TaskLauncher.
type Service struct {
	DB     *storage.Store
	Agents *agents.Registry
	Hosts  *hosts.Registry
	Log    *slog.Logger
	Notes  NotesSource
	// Tools is the sessile MCP server (§4.17.3); nil runs tasks without tools.
	Tools ToolServer
	// Sessions starts and reaches task sessions (§4.18.1).
	Sessions Sessions
	// DataDir is sessile's data directory: a task's agent lives under
	// <DataDir>/users/<uid>/tasks/<taskID> and nowhere else (§4.12.9).
	DataDir string
	// AllowLocal reports whether local-host sessions are enabled (§4.6); nil
	// means they are. Checked in Create, so the form, the orchestrator and
	// create_task all honour the setting.
	AllowLocal func() bool
	// WorkspaceTasksDir is a local-host task's folder root relative to the
	// workspace (§4.12).
	WorkspaceTasksDir string

	mu      sync.Mutex
	pending map[string]RestartOptions // by task id
	local   map[string]net.Listener   // each agent's tool socket, by task id
	conns   map[string]*hostConn      // each task's host connection, by task id
	preps   map[string]*prepare       // host preparation in flight, by task id
}

// Task is a stored task, decoded.
type Task struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	// ShellSessionID is the user's shell pane on the task's host (v0.9), "" for
	// a task that runs on the server.
	ShellSessionID string `json:"shellSessionId,omitempty"`
	HostID         string `json:"hostId"`
	Dir            string `json:"dir"`
	Spec           Spec   `json:"spec"`
	Summary        string `json:"summary"`
	// State is what the task's agent says it is doing (§4.18.2), "" until it
	// says anything; Question is what a blocked task is waiting for.
	State    string    `json:"state"`
	Question string    `json:"question"`
	Kind     string    `json:"kind"`
	Created  time.Time `json:"created"`
}

// fromRow decodes a stored task.
func fromRow(row storage.TaskRow) (Task, error) {
	var spec Spec
	if err := json.Unmarshal([]byte(row.SpecJSON), &spec); err != nil {
		return Task{}, fmt.Errorf("decode task spec: %w", err)
	}
	kind := row.Kind
	if kind == "" {
		kind = KindTask
	}
	return Task{ID: row.ID, SessionID: row.SessionID, ShellSessionID: row.ShellSessionID, HostID: row.HostID, Dir: row.Dir,
		Spec: spec, Summary: row.Summary, State: row.State, Question: row.Question,
		Kind: kind, Created: row.Created}, nil
}

// Get returns a task, scoped to userID.
func (s *Service) Get(userID, id string) (Task, error) {
	row, found, err := s.DB.GetTask(id, userID)
	if err != nil {
		return Task{}, err
	}
	if !found {
		return Task{}, ErrNotFound
	}
	return fromRow(row)
}

// Check validates a spec against the user's own settings and hosts: what
// Create needs before it stores anything. It returns the host (zero for a
// local task).
func (s *Service) Check(userID string, spec Spec) (hosts.Host, error) {
	if err := spec.Validate(); err != nil {
		return hosts.Host{}, err
	}
	store, err := s.Agents.For(userID)
	if err != nil {
		return hosts.Host{}, err
	}
	settings := store.Get()
	if _, _, err := resolveProfile(settings, spec.Agent.ProfileID); err != nil {
		return hosts.Host{}, err
	}
	if spec.Target == "local" {
		return hosts.Host{}, nil
	}
	hs, err := s.Hosts.For(userID)
	if err != nil {
		return hosts.Host{}, err
	}
	host, ok := hs.Get(spec.HostID)
	if !ok {
		return hosts.Host{}, session.ErrHostNotFound
	}
	if !supportedOS(host.TargetOS) {
		return hosts.Host{}, ErrUnsupportedTarget
	}
	return host, nil
}

// supportedOS: the POSIX bootstrap runs on Linux and macOS, and on a host
// whose OS was never set (most of them); Windows gets task.ps1. "other" has
// no bootstrap.
func supportedOS(os hosts.TargetOS) bool {
	switch os {
	case hosts.OSLinux, hosts.OSDarwin, hosts.OSWindows, "":
		return true
	}
	return false
}

// winDriveRe matches a Windows drive path as SFTP spells it (/C:/…) or as a
// user types it (C:/…, C:\…).
var winDriveRe = regexp.MustCompile(`^/?[A-Za-z]:[/\\]`)

// sftpPath turns a user-typed tasksDir into SFTP's form: an absolute Windows
// path C:/x becomes /C:/x, which Win32-OpenSSH's SFTP server expects.
func sftpPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if winDriveRe.MatchString(p) && !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// windowsPath turns an SFTP path (/C:/Users/x/…) into the native one
// (C:\Users\x\…) that PowerShell and the agents see.
func windowsPath(p string) string {
	if winDriveRe.MatchString(p) {
		p = strings.TrimPrefix(p, "/")
	}
	return strings.ReplaceAll(p, "/", `\`)
}

func resolveProfile(settings agents.Settings, profileID string) (agents.Profile, agents.Connection, error) {
	p, ok := settings.Profile(profileID)
	if !ok {
		return agents.Profile{}, agents.Connection{}, ErrProfileNotFound
	}
	c, ok := settings.Connection(p.ConnectionID)
	if !ok {
		return agents.Profile{}, agents.Connection{}, ErrProfileNotFound
	}
	if c.Expired(time.Now()) {
		return agents.Profile{}, agents.Connection{}, ErrConnectionExpired
	}
	return p, c, nil
}

// Store records a new, checked task for the session that is about to run it.
func (s *Service) Store(userID, sessionID string, spec Spec) (string, error) {
	name := spec.Name
	if spec.Kind == KindOrchestrator {
		name = "orchestrator"
	}
	id, err := NewID(name)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	if err := s.DB.InsertTask(storage.TaskRow{
		ID: id, SessionID: sessionID, UserID: userID, HostID: spec.HostID,
		SpecJSON: string(data), Kind: spec.Kind, Created: time.Now().UTC(),
	}); err != nil {
		return "", err
	}
	return id, nil
}

// Discard removes a task whose session never started.
func (s *Service) Discard(id string) {
	if err := s.DB.DeleteTask(id); err != nil && s.Log != nil {
		s.Log.Error("discard task failed", "taskId", id, "err", err)
	}
}

// RestartOptions are what a user may ask of a task's next start (§4.12.6):
// rebuild its devcontainer, or begin a new agent conversation instead of
// resuming.
type RestartOptions struct {
	RebuildContainer bool `json:"rebuildContainer"`
	Fresh            bool `json:"fresh"`
}

// RequestRestart records options for the task's next start; Launch consumes
// them, so they apply exactly once.
func (s *Service) RequestRestart(taskID string, o RestartOptions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		s.pending = map[string]RestartOptions{}
	}
	s.pending[taskID] = o
}

// ClearRestart drops options whose restart didn't happen.
func (s *Service) ClearRestart(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, taskID)
}

func (s *Service) takeRestart(taskID string) RestartOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.pending[taskID]
	delete(s.pending, taskID)
	return o
}

// SetSummary records the agent's status line (§4.17.3).
func (s *Service) SetSummary(taskID, summary string) error {
	return s.DB.SetTaskSummary(taskID, summary)
}

// SetState records what a task's agent says it is doing (§4.18.2).
func (s *Service) SetState(taskID, state, summary, question string) error {
	return s.DB.SetTaskState(taskID, state, summary, question)
}

// Launch resolves a task to its agent session (session.TaskLauncher). The
// agent runs on the sessile server (§4.12, v0.9), in the task's own folder
// under the data dir, and reaches its host through the tools of §4.12.4. It
// runs on every start, so a restart picks up the connection's current token,
// the user's current notes and scripts, and rewrites what the agent reads.
func (s *Service) Launch(userID, taskID string) (session.TaskLaunch, error) {
	t, err := s.Get(userID, taskID)
	if err != nil {
		return session.TaskLaunch{}, err
	}
	store, err := s.Agents.For(userID)
	if err != nil {
		return session.TaskLaunch{}, err
	}
	settings := store.Get()
	profile, conn, err := resolveProfile(settings, t.Spec.Agent.ProfileID)
	if err != nil {
		return session.TaskLaunch{}, err
	}
	model := t.Spec.Agent.Model
	if model == "" {
		model = profile.Model
	}
	ln, ok := resolveLaunch(profile.Agent, conn.Kind, t.Spec.Agent.Mode, model, t.Spec.Request != "")
	if !ok {
		return session.TaskLaunch{}, fmt.Errorf("unknown agent %q", profile.Agent)
	}
	// The CLI is installed once, on the server, rather than on every host.
	binary, err := exec.LookPath(ln.def.Binary)
	if err != nil {
		return session.TaskLaunch{}, fmt.Errorf("%s is not installed on the sessile server: %w", ln.def.Binary, err)
	}

	var accounts []agents.GitAccount
	var identity agents.GitAccount
	if t.Spec.Repo != nil {
		if g, ok := settings.GitFor(t.Spec.Repo.URL); ok {
			accounts, identity = []agents.GitAccount{g}, g
		}
	} else {
		accounts = settings.Git
	}

	var notes []Note
	if s.Notes != nil {
		if notes, err = s.Notes.TaskNotes(userID); err != nil {
			return session.TaskLaunch{}, fmt.Errorf("load notes: %w", err)
		}
	}
	scope := ScopeTask
	if t.Kind == KindOrchestrator {
		scope = ScopeOrchestrator
	}
	toolsText := ""
	if s.Tools != nil {
		toolsText = s.Tools.ToolsSection(userID, scope)
	}
	restart := s.takeRestart(t.ID)
	dir := s.AgentDir(userID, t.ID)

	return session.TaskLaunch{
		Group:        taskGroup(t),
		AgentDir:     dir,
		LocalDropEnv: serverEnvBlocked,
		LocalPrepare: func(absDir string) ([]string, []string, error) {
			if restart.Fresh {
				// A fresh start is a new conversation: the agent's state goes,
				// the work on the host stays.
				_ = os.RemoveAll(filepath.Join(absDir, agentStateDir))
			}
			tools := s.localTools(userID, t.ID, absDir, scope)
			launchFor := ln
			text := ""
			if tools.enabled {
				launchFor.first, launchFor.resume = ln.argv(toolArgs(ln.agent, absDir, tools.bridge, false))
				text = toolsText
			}
			files, err := buildFiles(t, absDir, launchFor, identity, accounts, notes, text)
			if err != nil {
				return nil, nil, err
			}
			files = append(files, tools.files...)
			files = append(files, mcpFiles(ln.agent, tools)...)
			if t.Kind == KindOrchestrator {
				instr, err := render("orchestrator.md.tmpl", orchestratorData{
					Dir: absDir, Tools: text, HasRequest: t.Spec.Request != "",
				})
				if err != nil {
					return nil, nil, err
				}
				files = replaceFile(files, ln.def.InstructionsFile, instr)
			}
			if err := writeFiles(localFS{}, absDir, func(string) ([]file, error) { return files, nil }); err != nil {
				return nil, nil, err
			}

			// The host, in the background: the folder is made on dial, and a
			// clone of any size runs while the agent is already reading its
			// instructions (§4.12.2). The tools wait for it.
			if t.Spec.Target != "local" {
				s.startPrepare(userID, t, accounts, identity, restart)
			}

			marker := filepath.Join(absDir, ".agent-started")
			_, seen := os.Stat(marker)
			argv := append([]string{binary}, launchFor.start(seen == nil)...)
			_ = os.WriteFile(marker, nil, 0o600)
			env := agentEnv(absDir, conn, ln, model)
			return argv, env, nil
		},
	}, nil
}

// agentStateDir holds the agent CLI's own settings and history, per task, so
// it never touches the server user's home (§4.12.9).
const agentStateDir = ".agent"

// agentEnv is the environment the agent CLI starts with: its connection's
// credentials, the agent's own arguments-as-env, and state directories inside
// the task folder. Nothing of the server's own is inherited (serverEnvBlocked).
func agentEnv(dir string, conn agents.Connection, ln launch, model string) []string {
	pairs := append(conn.Env(), ln.env...)
	state := filepath.Join(dir, agentStateDir)
	pairs = append(pairs,
		[2]string{"CLAUDE_CONFIG_DIR", filepath.Join(state, "claude")},
		[2]string{"CODEX_HOME", filepath.Join(state, "codex")},
		[2]string{"GEMINI_CLI_HOME", filepath.Join(state, "gemini")},
		[2]string{"HOME", dir},
	)
	out := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// AgentDir is where a task's agent lives on the server: per user, per task,
// and the only place it can write (§4.12.9, E13).
func (s *Service) AgentDir(userID, taskID string) string {
	return filepath.Join(s.DataDir, "users", userID, "tasks", taskID)
}

// taskGroup is the session group a task is filed under: its epic, or the
// default (§4.18.3). The orchestrator stands on its own.
func taskGroup(t Task) string {
	switch {
	case t.Kind == KindOrchestrator:
		return "Orchestrator"
	case t.Spec.Epic != "":
		return t.Spec.Epic
	}
	return session.TaskGroup
}

// replaceFile swaps one rendered file for another of the same name.
func replaceFile(files []file, name string, data []byte) []file {
	for i := range files {
		if files[i].name == name {
			files[i].data = data
			return files
		}
	}
	return append(files, file{name, data, 0o600})
}

func (s *Service) recordDir(taskID, dir string) {
	if err := s.DB.SetTaskDir(taskID, dir); err != nil && s.Log != nil {
		s.Log.Error("record task dir failed", "taskId", taskID, "err", err)
	}
}

// file is one file of a task folder.
type file struct {
	name string
	data []byte
	perm uint32
}

func writeFiles(fs FS, dir string, build func(string) ([]file, error)) error {
	files, err := build(dir)
	if err != nil {
		return err
	}
	if err := fs.MkdirAll(dir); err != nil {
		return err
	}
	if err := fs.Chmod(dir, 0o700); err != nil {
		return err
	}
	for _, f := range files {
		p := fs.Join(dir, f.name)
		if strings.Contains(f.name, "/") {
			if err := fs.MkdirAll(fs.Join(dir, f.name[:strings.LastIndex(f.name, "/")])); err != nil {
				return err
			}
		}
		if err := fs.WriteFile(p, f.data, os.FileMode(f.perm)); err != nil {
			return err
		}
	}
	return nil
}

// buildFiles renders every file of a task folder for one start (§4.12.2).
// buildFiles renders what the agent reads in its folder on the server: its
// instructions, the request, and copies of the user's notes. There is no
// bootstrap any more — sessile starts the CLI itself, and the host is
// prepared over SSH (§4.12.2, v0.9).
func buildFiles(t Task, dir string, ln launch, identity agents.GitAccount, accounts []agents.GitAccount,
	notes []Note, tools string) ([]file, error) {
	gitHosts, github := gitHostsList(accounts)
	var always []Note
	for _, n := range notes {
		if n.Always {
			always = append(always, n)
		}
	}
	instructions, err := render("instructions.md.tmpl", instructionsData{
		Name: t.Spec.Name, Dir: dir, Repo: t.Spec.Repo,
		Devcontainer: t.Spec.Devcontainer != nil,
		GitHosts:     gitHosts, GitHub: github,
		Notes: len(notes) > 0, AlwaysNotes: always,
		Tools: tools, HasRequest: t.Spec.Request != "", Summary: t.Summary,
	})
	if err != nil {
		return nil, err
	}
	files := []file{{ln.def.InstructionsFile, instructions, 0o600}}
	if t.Spec.Request != "" {
		files = append(files, file{"PROMPT.md", []byte(t.Spec.Request + "\n"), 0o600})
	}
	for _, n := range notes {
		files = append(files, file{"notes/" + n.Slug + ".md", []byte(n.Body), 0o600})
	}
	meta, err := json.MarshalIndent(map[string]any{
		"id": t.ID, "name": t.Spec.Name, "epic": t.Spec.Epic, "host": t.HostID,
		"repo": t.Spec.Repo, "devcontainer": t.Spec.Devcontainer, "mode": t.Spec.Agent.Mode,
		"identity": map[string]string{"name": identity.Name, "email": identity.Email},
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(files, file{"task.json", append(meta, '\n'), 0o600}), nil
}
