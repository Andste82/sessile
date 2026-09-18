package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

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

// ToolsSource renders the Tools section of a task's instructions (§4.17.2).
type ToolsSource interface {
	ToolsSection(userID string) string
}

// Service creates tasks and launches their sessions. It is the
// session.TaskLauncher.
type Service struct {
	DB     *storage.Store
	Agents *agents.Registry
	Hosts  *hosts.Registry
	Log    *slog.Logger
	Notes  NotesSource
	Tools  ToolsSource
	// WorkspaceTasksDir is a local-host task's folder root relative to the
	// workspace (§4.12).
	WorkspaceTasksDir string

	mu      sync.Mutex
	pending map[string]RestartOptions // by task id
}

// Task is a stored task, decoded.
type Task struct {
	ID        string    `json:"id"`
	SessionID string    `json:"sessionId"`
	HostID    string    `json:"hostId"`
	Dir       string    `json:"dir"`
	Spec      Spec      `json:"spec"`
	Summary   string    `json:"summary"`
	Created   time.Time `json:"created"`
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
	var spec Spec
	if err := json.Unmarshal([]byte(row.SpecJSON), &spec); err != nil {
		return Task{}, fmt.Errorf("decode task spec: %w", err)
	}
	return Task{ID: row.ID, SessionID: row.SessionID, HostID: row.HostID, Dir: row.Dir,
		Spec: spec, Summary: row.Summary, Created: row.Created}, nil
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
	id, err := NewID(spec.Name)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	if err := s.DB.InsertTask(storage.TaskRow{
		ID: id, SessionID: sessionID, UserID: userID, HostID: spec.HostID,
		SpecJSON: string(data), Created: time.Now().UTC(),
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

// Launch resolves a task to its session start (session.TaskLauncher). It runs
// on every start, so a restart picks up the current connection token, notes
// and scripts, and rewrites the bootstrap (§4.12.6).
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

	var accounts []agents.GitAccount
	var identity agents.GitAccount
	if t.Spec.Repo != nil {
		if g, ok := settings.GitFor(t.Spec.Repo.URL); ok {
			accounts = []agents.GitAccount{g}
			identity = g
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
	tools := ""
	if s.Tools != nil {
		tools = s.Tools.ToolsSection(userID)
	}

	env := append(append(conn.Env(), ln.env...), gitEnv(accounts)...)
	// files renders the folder for one target; displayDir is the folder as
	// the target itself names it, which is what the bootstrap and the
	// instructions use.
	restart := s.takeRestart(t.ID)
	files := func(displayDir string, windows bool) func(string) ([]file, error) {
		return func(string) ([]file, error) {
			out, err := buildFiles(t, displayDir, windows, ln, identity, accounts, env, notes, tools)
			if err != nil {
				return nil, err
			}
			// One-shot markers the bootstrap consumes (§4.12.3, §4.12.6).
			if restart.RebuildContainer {
				out = append(out, file{".rebuild-container", nil, 0o600})
			}
			if restart.Fresh {
				out = append(out, file{".restart-fresh", nil, 0o600})
			}
			return out, nil
		}
	}

	if t.Spec.Target == "local" {
		rel := s.WorkspaceTasksDir + "/" + t.ID
		return session.TaskLaunch{
			LocalDir: rel,
			LocalPrepare: func(absDir string) ([]string, []string, error) {
				fs := localFS{}
				if err := writeFiles(fs, absDir, files(absDir, false)); err != nil {
					return nil, nil, err
				}
				s.recordDir(t.ID, absDir)
				return []string{"/bin/sh", fs.Join(absDir, "task.sh")}, nil, nil
			},
		}, nil
	}

	hs, err := s.Hosts.For(userID)
	if err != nil {
		return session.TaskLaunch{}, err
	}
	host, ok := hs.Get(t.HostID)
	if !ok {
		return session.TaskLaunch{}, session.ErrHostNotFound
	}
	if !supportedOS(host.TargetOS) {
		return session.TaskLaunch{}, ErrUnsupportedTarget
	}
	target := host.SSHTarget()
	tasksDir := host.EffectiveTasksDir()
	windows := host.TargetOS == hosts.OSWindows
	target.Task = func(client *ssh.Client) (string, error) {
		fs, err := newSFTPFS(client)
		if err != nil {
			return "", err
		}
		defer fs.Close()
		base := sftpPath(tasksDir)
		if !strings.HasPrefix(base, "/") {
			home, err := fs.Home()
			if err != nil {
				return "", fmt.Errorf("find the login directory: %w", err)
			}
			base = fs.Join(home, base)
		}
		dir := fs.Join(base, t.ID)
		if windows {
			native := windowsPath(dir)
			if err := writeFiles(fs, dir, files(native, true)); err != nil {
				return "", err
			}
			s.recordDir(t.ID, native)
			// Run by Win32-OpenSSH's own shell (cmd.exe or PowerShell), both of
			// which take a double-quoted path; tasksDir can't contain a quote.
			return `powershell -NoProfile -ExecutionPolicy Bypass -File "` + native + `\task.ps1"`, nil
		}
		if err := writeFiles(fs, dir, files(dir, false)); err != nil {
			return "", err
		}
		s.recordDir(t.ID, dir)
		return "sh " + shellQuote(fs.Join(dir, "task.sh")), nil
	}
	return session.TaskLaunch{SSH: &target, HostID: host.ID, HostDisplayName: host.Name}, nil
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
		if err := fs.WriteFile(p, f.data, osMode(f.perm)); err != nil {
			return err
		}
	}
	return nil
}

// buildFiles renders every file of a task folder for one start (§4.12.2).
func buildFiles(t Task, dir string, windows bool, ln launch, identity agents.GitAccount, accounts []agents.GitAccount,
	env [][2]string, notes []Note, tools string) ([]file, error) {
	dc := t.Spec.Devcontainer
	data := bootstrapData{
		ID: t.ID, Dir: dir, Repo: t.Spec.Repo,
		GitName: identity.Name, GitEmail: identity.Email,
		Agent: ln.def.Binary, Install: installFor(ln.def.Binary, dc != nil), InstallPS: installForPS(ln.def.Binary),
		First: ln.first, Resume: ln.resume, Devcontainer: dc,
	}
	var files []file
	add := func(name, tmpl string, perm uint32) error {
		out, err := render(tmpl, data)
		if err != nil {
			return err
		}
		files = append(files, file{name, out, perm})
		return nil
	}
	addEnv := func(name string, fn func([][2]string) ([]byte, error)) error {
		out, err := fn(env)
		if err != nil {
			return err
		}
		files = append(files, file{name, out, 0o600})
		return nil
	}

	// The host side: task.sh + agent.sh on a POSIX host, task.ps1 on Windows.
	// Wherever the agent itself runs POSIX — any devcontainer — agent.sh and
	// the POSIX .env go along too.
	if windows {
		if err := add("task.ps1", "task.ps1.tmpl", 0o700); err != nil {
			return nil, err
		}
		if err := addEnv(".env.json", renderEnvJSON); err != nil {
			return nil, err
		}
	} else if err := add("task.sh", "task.sh.tmpl", 0o700); err != nil {
		return nil, err
	}
	if !windows || dc != nil {
		if err := add("agent.sh", "agent.sh.tmpl", 0o700); err != nil {
			return nil, err
		}
		if err := addEnv(".env", renderEnv); err != nil {
			return nil, err
		}
	}
	if dc != nil && dc.Mode != "repo" {
		files = append(files, file{".devcontainer-generic/devcontainer.json", genericDevcontainer, 0o600})
	}

	instrDir := dir
	if dc != nil {
		instrDir = "/sessile/task"
	}
	gitHosts, github := gitHostsList(accounts)
	var always []Note
	for _, n := range notes {
		if n.Always {
			always = append(always, n)
		}
	}
	instr, err := render("instructions.md.tmpl", instructionsData{
		Name: t.Spec.Name, Dir: instrDir, Repo: t.Spec.Repo, Devcontainer: dc != nil,
		GitHosts: gitHosts, GitHub: github,
		Notes: len(notes) > 0, AlwaysNotes: always, Tools: tools,
		HasRequest: t.Spec.Request != "", Summary: t.Summary,
	})
	if err != nil {
		return nil, err
	}
	spec, err := json.MarshalIndent(t.Spec, "", "  ")
	if err != nil {
		return nil, err
	}
	files = append(files,
		file{"task.json", append(spec, '\n'), 0o600},
		file{ln.def.InstructionsFile, instr, 0o600},
	)
	if t.Spec.Request != "" {
		files = append(files, file{"PROMPT.md", []byte(t.Spec.Request + "\n"), 0o600})
	}
	for _, n := range notes {
		files = append(files, file{"notes/" + n.Slug + ".md", []byte(n.Body), 0o600})
	}
	return files, nil
}
