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
// task's session. An interface so internal/session stays unaware of tasks
// beyond its TaskLauncher.
type Sessions interface {
	CreateTask(id, userID, name, taskID string) (session.Info, error)
	Get(id, userID string) (session.Info, error)
	Restart(id, userID string) (session.Info, error)
}

// ErrNoSessions is returned when the service was built without a manager.
var ErrNoSessions = errors.New("sessions are not available")

// ErrLocalDisabled is returned when local-host sessions are off (§4.6) and a
// task — or the orchestrator — wants to run on the server.
var ErrLocalDisabled = errors.New("local-host sessions are disabled")

// Create validates a spec, stores the task and starts its session. Both the
// task form and the orchestrator's create_task go through here, so they
// can't drift apart (§4.18.1).
func (s *Service) Create(userID string, spec Spec) (session.Info, error) {
	if s.Sessions == nil {
		return session.Info{}, ErrNoSessions
	}
	spec.Normalize()
	if spec.Target == "local" && s.AllowLocal != nil && !s.AllowLocal() {
		return session.Info{}, ErrLocalDisabled
	}
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
