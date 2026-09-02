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

// deleteUser removes an account, revokes any web sessions it still has open
// — otherwise a browser that was already logged in would keep working
// against an account that no longer exists — and cascades to what an
// orphaned account would otherwise leave behind: its plaintext hosts.yml
// (SSH passwords and private keys, §11) sitting on disk indefinitely, and
// its live terminal sessions running on as orphans nobody can list, attach
// to, or delete (Manager.Delete requires a matching userID, and the account
// that matched is gone). Cascade failures are logged, not fatal — the
// account is already gone either way, and there is no request left to fail.
func (s *Server) deleteUser(c *gin.Context) {
	id := c.Param("id")
	if err := s.users.Delete(id); err != nil {
		s.respondUserManagementError(c, err)
		return
	}
	s.webSessions.RevokeByUser(id)

	if infos, err := s.manager.List(id); err != nil {
		s.log.Error("list sessions for deleted user failed", "id", id, "err", err)
	} else {
		for _, info := range infos {
			if err := s.manager.Delete(info.ID, id); err != nil {
				s.log.Error("delete session for deleted user failed", "sessionId", info.ID, "userId", id, "err", err)
			}
		}
	}
	if s.hosts != nil {
		if err := s.hosts.Remove(id); err != nil {
			s.log.Error("remove hosts.yml for deleted user failed", "id", id, "err", err)
		}
	}

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
