package session

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/Andste82/sessile/backend/internal/hostops"
	"github.com/Andste82/sessile/backend/internal/sshpty"
	"github.com/Andste82/sessile/backend/internal/terminal"
)

// TaskGroup is the group every task session is created in (§4.12). The user
// can move a task out of it like any other session.
const TaskGroup = "Tasks"

// TaskLaunch is how a task session (§4.12) starts, as resolved by the
// TaskLauncher for the task's current settings: over SSH, or on the server.
type TaskLaunch struct {
	// SSH is the target to dial, its Task hook set to write the task folder
	// and name the bootstrap command. nil for a local-host task.
	SSH             *sshpty.Target
	HostID          string
	HostDisplayName string

	// Group is the session group the task is filed under: its epic
	// (§4.18.3), or TaskGroup.
	Group string

	// LocalDir is a local-host task's folder, relative to the workspace root
	// (§4.5). LocalPrepare writes it and returns the bootstrap's argv and any
	// extra environment.
	LocalDir     string
	LocalPrepare func(absDir string) (argv []string, env []string, err error)
}

// TaskLauncher resolves a task to a TaskLaunch. Implemented by
// internal/tasks, and consulted on every start — create and restart alike —
// so a restart picks up the task's current connection, notes and scripts
// rather than anything saved when it was created.
type TaskLauncher interface {
	Launch(userID, taskID string) (TaskLaunch, error)
}

// ErrNoTaskLauncher is returned for a task session when no launcher is wired.
var ErrNoTaskLauncher = errors.New("tasks are not available")

// taskIDRe is the shape internal/tasks gives task ids (§4.12.1). Checked
// again here because the id becomes a directory name.
var taskIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}-[0-9a-f]{6}$`)

// TaskEvents is told when a task's session stops, so the user's orchestrator
// hears that its agent finished (§4.18.2). Implemented by internal/mcp.
type TaskEvents interface {
	TaskExited(userID, taskID, name string)
	TaskCreated(userID, taskID, sessionID, name string)
}

// SetTaskLauncher wires the task launcher. Called once at startup, like
// SetHostResolver.
func (m *Manager) SetTaskLauncher(l TaskLauncher) {
	m.taskLauncher = l
}

// SetTaskEvents wires the task event sink. Called once at startup.
func (m *Manager) SetTaskEvents(e TaskEvents) {
	m.taskEvents = e
}

// taskExited reports a stopped task session. Ordinary sessions have no task
// id and are not reported.
func (m *Manager) taskExited(info Info) {
	if m.taskEvents != nil && info.TaskID != "" {
		m.taskEvents.TaskExited(info.UserID, info.TaskID, info.Name)
	}
}

// CreateTask starts the session for a task (§4.12) under the given session
// id, in the Tasks group. Host-key errors come back unwrapped, exactly as
// from CreateSSH, and leave no session behind.
func (m *Manager) CreateTask(id, userID, name, taskID string) (Info, error) {
	s, err := m.spawnTask(id, userID, name, taskID, timeNow())
	if err != nil {
		return Info{}, err
	}
	s.Group = TaskGroup
	if s.taskGroup != "" {
		s.Group = s.taskGroup
	}
	info, err := m.register(s)
	if err != nil {
		return Info{}, err
	}
	m.log.Info("task session created", "id", s.ID, "name", name, "taskId", taskID)
	m.publishSession(info)
	if m.taskEvents != nil {
		m.taskEvents.TaskCreated(userID, taskID, s.ID, name)
	}
	return info, nil
}

func (m *Manager) spawnTask(id, userID, name, taskID string, created time.Time) (*Session, error) {
	if m.taskLauncher == nil {
		return nil, ErrNoTaskLauncher
	}
	if !taskIDRe.MatchString(taskID) {
		return nil, errors.New("invalid task id")
	}
	launch, err := m.taskLauncher.Launch(userID, taskID)
	if err != nil {
		return nil, err
	}
	var s *Session
	if launch.SSH != nil {
		s, err = m.spawnSSH(id, userID, name, launch.HostID, launch.HostDisplayName, *launch.SSH, created)
	} else {
		s, err = m.spawnLocalTask(id, userID, name, taskID, launch, created)
	}
	if err != nil {
		return nil, err
	}
	s.TaskID = taskID
	s.taskGroup = launch.Group
	return s, nil
}

// Output returns the tail of a session's terminal, owner-scoped exactly like
// Get. It is how the orchestrator reads what a task's agent is doing
// (§4.18.1): the live buffer for a running session, the saved scrollback for
// a stopped one. max bounds the bytes returned, newest kept.
func (m *Manager) Output(id, userID string, max int) ([]byte, error) {
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	var data []byte
	if ok {
		if s.Info().UserID != userID {
			return nil, ErrNotFound
		}
		// A stopped session's buffer has been released; its scrollback on disk
		// is the real one.
		data, ok = s.snapshotRunning()
	}
	if !ok {
		if _, err := m.Get(id, userID); err != nil {
			return nil, err
		}
		if m.scrollback == nil {
			return nil, nil
		}
		var err error
		if data, err = m.scrollback.Load(id); err != nil {
			return nil, err
		}
	}
	if max > 0 && len(data) > max {
		data = data[len(data)-max:]
	}
	return data, nil
}

// Input types into a session's terminal on the owner's behalf — the
// orchestrator answering a blocked task (§4.18.1). WriteInput is the
// WebSocket path and has already checked ownership through the session's
// client; this is the same write with the check done here.
func (m *Manager) Input(id, userID string, data []byte) error {
	if _, err := m.Get(id, userID); err != nil {
		return err
	}
	return m.WriteInput(id, data)
}

// spawnLocalTask runs a task's bootstrap on the server itself, in its folder
// under the workspace root. The folder is sessile's own choice of path, but it
// still goes through the §4.5 check like every local path.
func (m *Manager) spawnLocalTask(id, userID, name, taskID string, launch TaskLaunch, created time.Time) (*Session, error) {
	if l := len(name); l < 1 || l > 64 {
		return nil, ErrInvalidName
	}
	if launch.LocalPrepare == nil || launch.LocalDir == "" {
		return nil, errors.New("local task launch is incomplete")
	}
	if err := os.MkdirAll(filepath.Join(m.root, launch.LocalDir), 0o700); err != nil {
		return nil, err
	}
	absDir, err := resolveDir(m.root, launch.LocalDir)
	if err != nil {
		return nil, err
	}
	argv, env, err := launch.LocalPrepare(absDir)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("local task launch has no command")
	}
	pty, err := terminal.StartArgs(argv[0], argv[1:], absDir, defaultRows, defaultCols, env)
	if err != nil {
		return nil, err
	}
	now := timeNow()
	return &Session{
		ID:           id,
		Name:         name,
		UserID:       userID,
		TargetType:   TargetLocal,
		Directory:    launch.LocalDir,
		Shell:        "sh",
		Status:       StatusRunning,
		PID:          pty.Pid(),
		Created:      created,
		LastActivity: now,
		Rows:         defaultRows,
		Cols:         defaultCols,
		backend:      pty,
		hostOps:      hostops.NewHostSession(hostops.NewLocal(), hostops.NewLinuxPlatform()),
		buffer:       NewRingBuffer(m.bufferSize),
		clients:      make(map[Client]clientGeom),
		lastPersist:  now,
		exited:       make(chan struct{}),
	}, nil
}
