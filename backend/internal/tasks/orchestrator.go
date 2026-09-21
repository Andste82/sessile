package tasks

import (
	"errors"

	"github.com/google/uuid"

	"github.com/Andste82/sessile/backend/internal/session"
)

// Tool scopes (§4.18.1): what the sessile MCP server serves a connection.
const (
	ScopeTask         = "task"
	ScopeOrchestrator = "orchestrator"
)

// Sessions is the part of session.Manager tasks needs to start and reach a
// task's sessions. An interface so internal/session stays unaware of tasks
// beyond its TaskLauncher.
type Sessions interface {
	// CreateTask starts the agent session, on the sessile server.
	CreateTask(id, userID, name, taskID string) (session.Info, error)
	// CreateTaskShell starts the user's shell on the task's host.
	CreateTaskShell(id, userID, name, taskID string) (session.Info, error)
	Get(id, userID string) (session.Info, error)
	Restart(id, userID string) (session.Info, error)
}

// ErrNoSessions is returned when the service was built without a manager.
var ErrNoSessions = errors.New("sessions are not available")

// Create validates a spec, stores the task and starts its session. Both the
// task form and the orchestrator's create_task go through here, so they
// can't drift apart (§4.18.1).
func (s *Service) Create(userID string, spec Spec) (session.Info, error) {
	if s.Sessions == nil {
		return session.Info{}, ErrNoSessions
	}
	spec.Normalize()
	// allowLocalHost governs *shells* on the sessile server (§4.6). A task's
	// agent is not one: since v0.9 every agent runs here, confined to its own
	// folder with its own shell denied (§4.12.9), so gating a task on that
	// switch would gate every task — and would say "local-host sessions are
	// disabled" about something that is not a local-host session.
	if _, err := s.Check(userID, spec); err != nil {
		return session.Info{}, err
	}
	sessionID := uuid.NewString()
	taskID, err := s.Store(userID, sessionID, spec)
	if err != nil {
		return session.Info{}, err
	}
	info, err := s.Sessions.CreateTask(sessionID, userID, spec.Name, taskID)
	if err != nil {
		s.Discard(taskID)
		return session.Info{}, err
	}
	// The user's own shell on the host, beside the agent (§4.12, E10). A task
	// on the server has none, and a host that refuses a second connection
	// costs the task nothing: the agent is already running.
	if spec.Target != "local" {
		shellID := uuid.NewString()
		if _, err := s.Sessions.CreateTaskShell(shellID, userID, spec.Name+" (shell)", taskID); err != nil {
			s.warn("the task's shell pane could not be opened", err)
		} else if err := s.DB.SetTaskShellSession(taskID, shellID); err != nil {
			s.warn("could not record the task's shell session", err)
		}
	}
	return info, nil
}

// OpenShell starts (or restarts) a task's shell pane on demand, for a task
// whose pane was closed or never opened.
func (s *Service) OpenShell(userID, taskID string) (session.Info, error) {
	if s.Sessions == nil {
		return session.Info{}, ErrNoSessions
	}
	t, err := s.Get(userID, taskID)
	if err != nil {
		return session.Info{}, err
	}
	if t.Spec.Target == "local" {
		return session.Info{}, ErrLocalTask
	}
	if t.ShellSessionID != "" {
		info, err := s.Sessions.Get(t.ShellSessionID, userID)
		switch {
		case err == nil && info.Status == session.StatusRunning:
			return info, nil
		case err == nil:
			return s.Sessions.Restart(t.ShellSessionID, userID)
		case !errors.Is(err, session.ErrNotFound):
			return session.Info{}, err
		}
	}
	shellID := uuid.NewString()
	info, err := s.Sessions.CreateTaskShell(shellID, userID, t.Spec.Name+" (shell)", taskID)
	if err != nil {
		return session.Info{}, err
	}
	if err := s.DB.SetTaskShellSession(taskID, shellID); err != nil {
		s.warn("could not record the task's shell session", err)
	}
	return info, nil
}

// defaultProfile is the profile the orchestrator starts with when the caller
// names none: the user's task default, or their only profile.
func (s *Service) defaultProfile(userID string) string {
	store, err := s.Agents.For(userID)
	if err != nil {
		return ""
	}
	settings := store.Get()
	if id := settings.TaskDefaults.ProfileID; id != "" {
		return id
	}
	if len(settings.Profiles) == 1 {
		return settings.Profiles[0].ID
	}
	return ""
}

// Orchestrator returns the user's orchestrator task (§4.18), if it exists.
func (s *Service) Orchestrator(userID string) (Task, bool, error) {
	row, found, err := s.DB.OrchestratorTask(userID)
	if err != nil || !found {
		return Task{}, false, err
	}
	t, err := fromRow(row)
	return t, err == nil, err
}

// OpenOrchestrator returns the user's orchestrator session, starting it the
// first time and restarting it when it has stopped. There is exactly one per
// user: opening it again is how you get back to the same conversation.
func (s *Service) OpenOrchestrator(userID, profileID string) (session.Info, error) {
	if s.Sessions == nil {
		return session.Info{}, ErrNoSessions
	}
	t, found, err := s.Orchestrator(userID)
	if err != nil {
		return session.Info{}, err
	}
	if found {
		info, err := s.Sessions.Get(t.SessionID, userID)
		switch {
		case err == nil && info.Status == session.StatusRunning:
			return info, nil
		case err == nil:
			return s.Sessions.Restart(t.SessionID, userID)
		case errors.Is(err, session.ErrNotFound):
			// Its session was deleted; start a fresh one below.
			s.Discard(t.ID)
		default:
			return session.Info{}, err
		}
		if profileID == "" {
			profileID = t.Spec.Agent.ProfileID
		}
	}
	if profileID == "" {
		profileID = s.defaultProfile(userID)
	}
	if profileID == "" {
		return session.Info{}, invalid("set up an agent profile first: the orchestrator is an agent session")
	}
	return s.Create(userID, Spec{
		Name: "Orchestrator", Kind: KindOrchestrator, Target: "local",
		Agent: AgentSpec{ProfileID: profileID, Mode: ModeNormal},
	})
}
