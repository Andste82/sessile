package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/session"
)

// The §6 session shape is session.JSON, built by session.ToJSON. It lives in
// the session package because the event channel serialises it too and ws cannot
// import api — see the type's own comment.
type createSessionBody struct {
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Shell     string `json:"shell"`
}

func (s *Server) listSessions(c *gin.Context) {
	infos, err := s.manager.List()
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
	var body createSessionBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	info, err := s.manager.Create(body.Name, body.Directory, body.Shell)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, session.ToJSON(info))
}

func (s *Server) getSession(c *gin.Context) {
	info, err := s.manager.Get(c.Param("id"))
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

func (s *Server) deleteSession(c *gin.Context) {
	if err := s.manager.Delete(c.Param("id")); err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type renameBody struct {
	Name string `json:"name"`
}

func (s *Server) renameSession(c *gin.Context) {
	var body renameBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	info, err := s.manager.Rename(c.Param("id"), body.Name)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

// restartSession gives a stopped session a new shell under the same id, with
// its scrollback and command history restored (§8).
func (s *Server) restartSession(c *gin.Context) {
	info, err := s.manager.Restart(c.Param("id"))
	if err != nil {
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
		// resolveDir and other validation-style failures surface here as 400;
		// treat unknown errors as validation to avoid leaking internals, but
		// log for diagnosis.
		s.log.Warn("session request failed", "err", err)
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	}
}
