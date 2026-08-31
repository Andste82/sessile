package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/session"
)

// The §6 session shape is session.JSON, built by session.ToJSON. It lives in
// the session package because the event channel serialises it too and ws cannot
// import api — see the type's own comment.
//
// createSessionBody discriminates on target: "local" needs directory+shell,
// "ssh" needs hostId. Empty target is treated as "local" for compatibility
// with the pre-M17 body shape.
type createSessionBody struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Directory string `json:"directory"`
	Shell     string `json:"shell"`
	HostID    string `json:"hostId"`
}

func (s *Server) listSessions(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	infos, err := s.manager.List(userID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "list sessions failed")
		return
	}
	out := make([]session.JSON, 0, len(infos))
	for _, i := range infos {
		out = append(out, session.ToJSON(i))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) createSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	var body createSessionBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}

	if body.Target == "ssh" {
		s.createSSHSession(c, userID, body)
		return
	}
	s.createLocalSession(c, userID, body)
}

func (s *Server) createLocalSession(c *gin.Context, userID string, body createSessionBody) {
	if !s.serverConfig.Get().AllowLocalHost {
		respondError(c, http.StatusForbidden, CodeForbidden, "local-host sessions are disabled")
		return
	}
	info, err := s.manager.CreateLocal(userID, body.Name, body.Directory, body.Shell)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, session.ToJSON(info))
}

func (s *Server) createSSHSession(c *gin.Context, userID string, body createSessionBody) {
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	host, found := store.Get(body.HostID)
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}

	info, err := s.manager.CreateSSH(userID, body.Name, host.ID, host.Name, host.SSHTarget())
	if err != nil {
		if s.respondHostKeyError(c, err) {
			return
		}
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, session.ToJSON(info))
}

func (s *Server) getSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	info, err := s.manager.Get(c.Param("id"), userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

func (s *Server) deleteSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	if err := s.manager.Delete(c.Param("id"), userID); err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type renameBody struct {
	Name string `json:"name"`
}

func (s *Server) renameSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	var body renameBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	info, err := s.manager.Rename(c.Param("id"), userID, body.Name)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

// restartSession gives a stopped session a new shell under the same id, with
// its scrollback and command history restored (§8).
func (s *Server) restartSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	info, err := s.manager.Restart(c.Param("id"), userID)
	if err != nil {
		if s.respondHostKeyError(c, err) {
			return
		}
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

// respondSessionError maps domain errors to the unified error envelope.
func (s *Server) respondSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, session.ErrHostNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, session.ErrAlreadyRunning):
		respondError(c, http.StatusConflict, CodeAlreadyRunning, err.Error())
	case errors.Is(err, session.ErrStopped),
		errors.Is(err, session.ErrRestarting):
		respondError(c, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, session.ErrInvalidName),
		errors.Is(err, session.ErrInvalidShell):
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	case errors.Is(err, session.ErrShuttingDown):
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, err.Error())
	default:
		// resolveDir, sshpty dial failures and other validation-style failures
		// surface here as 400; treat unknown errors as validation to avoid
		// leaking internals, but log for diagnosis.
		s.log.Warn("session request failed", "err", err)
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	}
}
