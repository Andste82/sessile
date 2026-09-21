package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/tasks"
)

// Tasks (PROJECT_PLAN.md §4.12): POST /api/tasks creates the task and starts
// its session; everything after that — attach, restart, delete — is the
// session's own routes.

// SetTasks wires the task service (§4.12).
func (s *Server) SetTasks(svc *tasks.Service) {
	s.tasks = svc
}

func (s *Server) createTask(c *gin.Context) {
	if s.tasks == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tasks are not available")
		return
	}
	userID := c.MustGet(userIDKey).(string)
	var spec tasks.Spec
	if err := c.ShouldBindJSON(&spec); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	spec.Normalize()
	if spec.Target == "local" && !s.serverConfig.Get().AllowLocalHost {
		respondError(c, http.StatusForbidden, CodeForbidden, "local-host sessions are disabled")
		return
	}
	info, err := s.tasks.Create(userID, spec)
	if err != nil {
		var ve *tasks.ValidationError
		if errors.As(err, &ve) {
			respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}
		if s.respondHostKeyError(c, err) {
			return
		}
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, session.ToJSON(info))
}

func (s *Server) getTask(c *gin.Context) {
	if s.tasks == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tasks are not available")
		return
	}
	t, err := s.tasks.Get(c.MustGet(userIDKey).(string), c.Param("id"))
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// openTaskShell opens (or reopens) a task's shell pane on its host (§4.12).
func (s *Server) openTaskShell(c *gin.Context) {
	if s.tasks == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tasks are not available")
		return
	}
	info, err := s.tasks.OpenShell(c.MustGet(userIDKey).(string), c.Param("id"))
	if err != nil {
		if errors.Is(err, tasks.ErrLocalTask) {
			respondError(c, http.StatusBadRequest, CodeValidation, "this task runs on the sessile server; it has no host shell")
			return
		}
		if s.respondHostKeyError(c, err) {
			return
		}
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

// The orchestrator (§4.18): one session per user, on the server itself.
// GET reports whether it exists; POST opens it — created the first time,
// restarted when it has stopped, and otherwise handed back as it is.

func (s *Server) getOrchestrator(c *gin.Context) {
	if s.tasks == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tasks are not available")
		return
	}
	t, found, err := s.tasks.Orchestrator(c.MustGet(userIDKey).(string))
	if err != nil {
		s.log.Error("read orchestrator failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to read the orchestrator")
		return
	}
	if !found {
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sessionId": t.SessionID, "taskId": t.ID})
}

func (s *Server) openOrchestrator(c *gin.Context) {
	if s.tasks == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tasks are not available")
		return
	}
	// It runs on the server itself, like any local-host session (§4.5).
	if !s.serverConfig.Get().AllowLocalHost {
		respondError(c, http.StatusForbidden, CodeForbidden, "local-host sessions are disabled")
		return
	}
	var body struct {
		ProfileID string `json:"profileId"`
	}
	_ = c.ShouldBindJSON(&body)
	info, err := s.tasks.OpenOrchestrator(c.MustGet(userIDKey).(string), body.ProfileID)
	if err != nil {
		var ve *tasks.ValidationError
		if errors.As(err, &ve) {
			respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}
		if s.respondHostKeyError(c, err) {
			return
		}
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}
