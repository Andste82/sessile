package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/auth"
)

// listUsers returns every account (§12b M11), admin-only. No password
// hashes — userResponse (auth.go) never carries them.
func (s *Server) listUsers(c *gin.Context) {
	users := s.users.List()
	out := make([]userResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toUserResponse(u))
	}
	c.JSON(http.StatusOK, out)
}

// deleteUser removes an account and revokes any web sessions it still has
// open — otherwise a browser that was already logged in would keep working
// against an account that no longer exists.
func (s *Server) deleteUser(c *gin.Context) {
	id := c.Param("id")
	if err := s.users.Delete(id); err != nil {
		s.respondUserManagementError(c, err)
		return
	}
	s.webSessions.RevokeByUser(id)
	c.Status(http.StatusNoContent)
}

type setAdminBody struct {
	IsAdmin bool `json:"isAdmin"`
}

// setUserAdmin promotes or demotes an account.
func (s *Server) setUserAdmin(c *gin.Context) {
	var body setAdminBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}

	id := c.Param("id")
	if err := s.users.SetAdmin(id, body.IsAdmin); err != nil {
		s.respondUserManagementError(c, err)
		return
	}
	user, _ := s.users.FindByID(id)
	c.JSON(http.StatusOK, toUserResponse(user))
}

// respondUserManagementError maps auth.UserStore's admin-management errors
// to the unified error envelope.
func (s *Server) respondUserManagementError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, auth.ErrLastAdmin):
		// The admin panel must never be able to lock itself out — this is a
		// conflict with the current state, not a validation failure.
		respondError(c, http.StatusConflict, CodeConflict, err.Error())
	default:
		s.log.Warn("user management request failed", "err", err)
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	}
}
