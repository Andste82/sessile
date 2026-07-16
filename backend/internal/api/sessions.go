package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/session"
)

// sessionJSON mirrors the §6 session shape (single source of truth; kept in
// sync with frontend/src/api/types.ts).
type sessionJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Directory    string `json:"directory"`
	Shell        string `json:"shell"`
	Status       string `json:"status"`
	PID          int    `json:"pid"`
	Created      string `json:"created"`
	LastActivity string `json:"lastActivity"`
	Rows         uint16 `json:"rows"`
	Cols         uint16 `json:"cols"`
	ClientCount  int    `json:"clientCount"`
}

func toJSON(i session.Info) sessionJSON {
	return sessionJSON{
		ID:           i.ID,
		Name:         i.Name,
		Directory:    i.Directory,
		Shell:        i.Shell,
		Status:       string(i.Status),
		PID:          i.PID,
		Created:      i.Created.UTC().Format(time.RFC3339),
		LastActivity: i.LastActivity.UTC().Format(time.RFC3339),
		Rows:         i.Rows,
		Cols:         i.Cols,
		ClientCount:  i.ClientCount,
	}
}

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
	out := make([]sessionJSON, 0, len(infos))
	for _, i := range infos {
		out = append(out, toJSON(i))
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
	c.JSON(http.StatusCreated, toJSON(info))
}

func (s *Server) getSession(c *gin.Context) {
	info, err := s.manager.Get(c.Param("id"))
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, toJSON(info))
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
	c.JSON(http.StatusOK, toJSON(info))
}

// respondSessionError maps domain errors to the unified error envelope.
func (s *Server) respondSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, session.ErrStopped):
		respondError(c, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, session.ErrInvalidName),
		errors.Is(err, session.ErrInvalidShell):
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	default:
		// resolveDir and other validation-style failures surface here as 400;
		// treat unknown errors as validation to avoid leaking internals, but
		// log for diagnosis.
		s.log.Warn("session request failed", "err", err)
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	}
}
