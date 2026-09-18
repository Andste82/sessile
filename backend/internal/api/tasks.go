package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

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
	if _, err := s.tasks.Check(userID, spec); err != nil {
		var ve *tasks.ValidationError
		if errors.As(err, &ve) {
			respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}
		s.respondSessionError(c, err)
		return
	}

	sessionID := uuid.NewString()
	taskID, err := s.tasks.Store(userID, sessionID, spec)
	if err != nil {
		s.log.Error("store task failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to create task")
		return
	}
	info, err := s.manager.CreateTask(sessionID, userID, spec.Name, taskID)
	if err != nil {
		s.tasks.Discard(taskID)
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
