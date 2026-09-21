package tasks

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Andste82/sessile/backend/internal/agents"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/hosttools"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// A task's connection to its host (v0.9). The agent runs on the sessile
// server, so the connection belongs to the task rather than to any one
// terminal: it outlives the shell pane, and both starts and restarts reuse
// it. One dial per task, with the host's pinned key.

// hostConn is a task's cached connection.
type hostConn struct {
	client *hosttools.Client
	dir    string // the task folder on the host
	os     hosts.TargetOS
}

// Host returns the task's live connection, dialling on first use and again
// after a host reboot has taken the old one down. Owner-scoped: the task is
// looked up for this user, and the host with it.
func (s *Service) Host(userID, taskID string) (*hosttools.Client, string, error) {
	t, err := s.Get(userID, taskID)
	if err != nil {
		return nil, "", err
	}
	if t.Spec.Target == "local" {
		return nil, "", ErrLocalTask
	}
	s.mu.Lock()
	if c := s.conns[taskID]; c != nil {
		if c.client.Alive() {
			dir := c.dir
			client := c.client
			s.mu.Unlock()
			return client, dir, nil
		}
		c.client.Close()
		delete(s.conns, taskID)
	}
	s.mu.Unlock()

	target, host, err := s.hostTarget(userID, t)
	if err != nil {
		return nil, "", err
	}
	client, err := hosttools.Dial(target)
	if err != nil {
		return nil, "", err
	}
	dir, err := s.hostTaskDir(client, host, t)
	if err != nil {
		client.Close()
		return nil, "", err
	}
	s.mu.Lock()
	if s.conns == nil {
		s.conns = map[string]*hostConn{}
	}
	// Another start may have dialled while this one was connecting.
	if existing := s.conns[taskID]; existing != nil && existing.client.Alive() {
		s.mu.Unlock()
		client.Close()
		return existing.client, existing.dir, nil
	}
	s.conns[taskID] = &hostConn{client: client, dir: dir, os: host.TargetOS}
	s.mu.Unlock()
	s.recordDir(taskID, dir)
	return client, dir, nil
}

// ErrLocalTask is returned for host operations on a task that runs on the
// server itself — the orchestrator, and any task targeting "local".
var ErrLocalTask = errors.New("this task has no host: it runs on the sessile server")

// CloseHost drops a task's connection, on delete or on shutdown.
func (s *Service) CloseHost(taskID string) {
	s.mu.Lock()
	c := s.conns[taskID]
	delete(s.conns, taskID)
	s.mu.Unlock()
	if c != nil {
		c.client.Close()
	}
}

// CloseHosts drops every connection, for graceful shutdown.
func (s *Service) CloseHosts() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		c.client.Close()
	}
}

// hostTarget resolves a task's host to an SSH target, carrying the pinned
// fingerprint so an unknown or changed key is refused exactly as it is for a
// session (§4.5.1).
func (s *Service) hostTarget(userID string, t Task) (sshpty.Target, hosts.Host, error) {
	store, err := s.Hosts.For(userID)
	if err != nil {
		return sshpty.Target{}, hosts.Host{}, err
	}
	host, ok := store.Get(t.HostID)
	if !ok {
		return sshpty.Target{}, hosts.Host{}, session.ErrHostNotFound
	}
	if !supportedOS(host.TargetOS) {
		return sshpty.Target{}, hosts.Host{}, ErrUnsupportedTarget
	}
	return host.SSHTarget(), host, nil
}

// hostTaskDir makes the task's folder on the host and returns it as the host
// itself names it. It is sessile's own path — `<tasksDir>/<task id>` — never
// anything a caller supplied.
func (s *Service) hostTaskDir(c *hosttools.Client, host hosts.Host, t Task) (string, error) {
	base := sftpPath(host.EffectiveTasksDir())
	if !strings.HasPrefix(base, "/") && !winDriveRe.MatchString(base) {
		home, err := c.SFTP().Getwd()
		if err != nil {
			return "", fmt.Errorf("find the login directory: %w", err)
		}
		base = path.Join(home, base)
	}
	dir := path.Join(base, t.ID)
	if err := c.MkdirAll(dir); err != nil {
		return "", fmt.Errorf("create the task folder: %w", err)
	}
	if host.TargetOS == hosts.OSWindows {
		return windowsPath(dir), nil
	}
	return dir, nil
}

// prepareHost gets the host ready for the agent: the task folder, the main
// repo cloned into it, and the devcontainer up when the task asked for one.
// Every step is idempotent, so a restart re-runs it and does nothing.
//
// It runs from the server, with the Git account's credentials in the
// environment of the one command that needs them — the agent never holds
// them, and nothing is left on the host (§4.12.9).
func (s *Service) prepareHost(ctx context.Context, userID string, t Task, accounts []agents.GitAccount, identity agents.GitAccount, progress func(string)) (string, error) {
	client, dir, err := s.Host(userID, t.ID)
	if err != nil {
		return "", err
	}
	say := func(format string, args ...any) {
		if progress != nil {
			progress(fmt.Sprintf(format, args...))
		}
	}
	if t.Spec.Repo == nil {
		return dir, nil
	}

	repoDir := path.Join(dir, "repo")
	gitEnvPairs := gitEnv(accounts)
	if _, err := client.Stat(path.Join(repoDir, ".git")); err != nil {
		say("cloning %s", t.Spec.Repo.URL)
		res, err := client.Run(ctx, hosttools.RunRequest{
			Command: fmt.Sprintf("git clone -- %s repo", shellQuote(t.Spec.Repo.URL)),
			Dir:     dir, Env: gitEnvPairs, Timeout: 30 * time.Minute,
		})
		if err != nil {
			return "", fmt.Errorf("clone %s: %w", t.Spec.Repo.URL, err)
		}
		if res.ExitCode != 0 {
			return "", fmt.Errorf("clone %s failed:\n%s", t.Spec.Repo.URL, res.Output)
		}
	}
	if ref := t.Spec.Repo.Ref; ref != "" {
		say("checking out %s", ref)
		res, err := client.Run(ctx, hosttools.RunRequest{
			Command: fmt.Sprintf("git checkout -- %s || git checkout %s", shellQuote(ref), shellQuote(ref)),
			Dir:     repoDir, Env: gitEnvPairs, Timeout: 5 * time.Minute,
		})
		if err == nil && res.ExitCode != 0 {
			say("could not check out %s; staying on the default branch", ref)
		}
	}
	if identity.Name != "" || identity.Email != "" {
		_, _ = client.Run(ctx, hosttools.RunRequest{
			Command: fmt.Sprintf("git config user.name %s && git config user.email %s",
				shellQuote(identity.Name), shellQuote(identity.Email)),
			Dir: repoDir, Timeout: time.Minute,
		})
	}
	if t.Spec.Devcontainer != nil {
		say("bringing the devcontainer up")
		res, err := client.Run(ctx, hosttools.RunRequest{
			Command: devcontainerUp(t.Spec.Devcontainer, dir),
			Dir:     dir, Timeout: 30 * time.Minute,
		})
		if err != nil {
			return "", fmt.Errorf("devcontainer up: %w", err)
		}
		if res.ExitCode != 0 {
			return "", fmt.Errorf("devcontainer up failed:\n%s", res.Output)
		}
	}
	return dir, nil
}

// devcontainerUp is the fixed command that starts a task's container: the CLI
// that is already on the host, the repo as its workspace, and the mounts of
// §4.12.3. Sessile uses devcontainers; it does not manage them.
func devcontainerUp(dc *Devcontainer, dir string) string {
	args := []string{"devcontainer", "up", "--workspace-folder", shellQuote(path.Join(dir, "repo"))}
	if dc.Mode == "generic" {
		args = append(args, "--config", shellQuote(path.Join(dir, ".devcontainer-generic", "devcontainer.json")))
	}
	if dc.DockerSocket {
		args = append(args, "--mount", shellQuote("type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock"))
	}
	return strings.Join(args, " ")
}

// hostShellTarget is the target for a task's shell pane: the host's own
// terminal, started in the task folder so the user lands where the work is.
func (s *Service) hostShellTarget(userID string, t Task) (sshpty.Target, hosts.Host, error) {
	target, host, err := s.hostTarget(userID, t)
	if err != nil {
		return sshpty.Target{}, hosts.Host{}, err
	}
	dir := s.taskDirOf(t)
	if dir == "" {
		return target, host, nil
	}
	shell := target.TerminalType
	if shell == "custom" {
		shell = target.CustomCommand
	}
	if host.TargetOS == hosts.OSWindows {
		target.TerminalType, target.CustomCommand = "custom",
			fmt.Sprintf("cmd /c \"cd /d %s && %s\"", dir, shell)
		return target, host, nil
	}
	target.TerminalType, target.CustomCommand = "custom",
		fmt.Sprintf("cd %s 2>/dev/null; exec %s", shellQuote(dir), shell)
	return target, host, nil
}

// hostNaming is how the instructions refer to the task's host and its folder
// there. Both are best-effort: the folder is known once the host has been
// dialled, and until then the instructions name the host alone.
func (s *Service) hostNaming(userID string, t Task) (name, dir string) {
	if t.Spec.Target == "local" {
		return "this server", s.AgentDir(userID, t.ID)
	}
	name = "the task's host"
	if store, err := s.Hosts.For(userID); err == nil {
		if h, ok := store.Get(t.HostID); ok {
			name = h.Name
		}
	}
	return name, s.taskDirOf(t)
}

// GitEnv is the task's Git credential environment, for one command on its
// host: the helper answers from it, so nothing is written there and the token
// is never part of a URL (§4.16). Empty when the user has no Git account for
// the task's repo.
func (s *Service) GitEnv(userID, taskID string) [][2]string {
	t, err := s.Get(userID, taskID)
	if err != nil {
		return nil
	}
	store, err := s.Agents.For(userID)
	if err != nil {
		return nil
	}
	settings := store.Get()
	if t.Spec.Repo != nil {
		if g, ok := settings.GitFor(t.Spec.Repo.URL); ok {
			return gitEnv([]agents.GitAccount{g})
		}
		return nil
	}
	return gitEnv(settings.Git)
}

// ShellLaunch is the user's pane on a task's host (session.TaskLauncher):
// the host's own terminal, started in the task folder.
func (s *Service) ShellLaunch(userID, taskID string) (session.TaskShell, error) {
	t, err := s.Get(userID, taskID)
	if err != nil {
		return session.TaskShell{}, err
	}
	if t.Spec.Target == "local" {
		return session.TaskShell{}, ErrLocalTask
	}
	target, host, err := s.hostShellTarget(userID, t)
	if err != nil {
		return session.TaskShell{}, err
	}
	return session.TaskShell{
		Target: target, HostID: host.ID, HostDisplayName: host.Name, Group: taskGroup(t),
	}, nil
}

// taskDirOf is the task's folder on the host as last recorded, or "" before
// the first start has made one.
func (s *Service) taskDirOf(t Task) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.conns[t.ID]; c != nil {
		return c.dir
	}
	return t.Dir
}

// prepare is one task's host preparation, in flight or finished. The agent
// starts at once and reads its instructions while this runs; the host tools
// wait for it (§4.12.2).
type prepare struct {
	done chan struct{}
	err  error
	last string // the step it is on, for the task panel and for a waiting tool
	dir  string
}

// Wait blocks until preparation finishes, or until the context or timeout
// runs out — in which case it reports what it is still doing.
func (p *prepare) Wait(ctx context.Context, timeout time.Duration) error {
	if p == nil {
		return nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.err
	case <-timer.C:
		return fmt.Errorf("the task's host is still being prepared (%s); try again in a moment", p.last)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startPrepare runs the host preparation for a task, once. A second start
// while one is running joins it rather than cloning twice.
func (s *Service) startPrepare(userID string, t Task, accounts []agents.GitAccount, identity agents.GitAccount, restart RestartOptions) *prepare {
	s.mu.Lock()
	if s.preps == nil {
		s.preps = map[string]*prepare{}
	}
	if p := s.preps[t.ID]; p != nil {
		select {
		case <-p.done:
			// A finished preparation is reused, unless this start asked for
			// the container to be rebuilt.
			if p.err == nil && !restart.RebuildContainer {
				s.mu.Unlock()
				return p
			}
		default:
			s.mu.Unlock()
			return p
		}
	}
	p := &prepare{done: make(chan struct{}), last: "connecting"}
	s.preps[t.ID] = p
	s.mu.Unlock()

	go func() {
		defer close(p.done)
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		if restart.RebuildContainer && t.Spec.Devcontainer != nil {
			p.last = "rebuilding the devcontainer"
			if c, dir, err := s.Host(userID, t.ID); err == nil {
				_, _ = c.Run(ctx, hosttools.RunRequest{
					Command: "devcontainer up --remove-existing-container --workspace-folder " + shellQuote(path.Join(dir, "repo")),
					Dir:     dir, Timeout: 30 * time.Minute,
				})
			}
		}
		dir, err := s.prepareHost(ctx, userID, t, accounts, identity, func(step string) {
			p.last = step
			if s.Log != nil {
				s.Log.Info("task host", "taskId", t.ID, "step", step)
			}
		})
		p.dir, p.err = dir, err
		if err != nil && s.Log != nil {
			s.Log.Error("task host preparation failed", "taskId", t.ID, "err", err)
		}
	}()
	return p
}

// Prepared returns a task's preparation, if one has been started.
func (s *Service) Prepared(taskID string) *prepare {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preps[taskID]
}
