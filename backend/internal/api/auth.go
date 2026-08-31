package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/auth"
	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/serverconfig"
)

// sessionCookieName is the browser's web-session cookie (§10, §11) — not to
// be confused with a terminal session.
const sessionCookieName = "sessile_session"

type authStatusResponse struct {
	NeedsSetup        bool   `json:"needsSetup"`
	AllowRegistration bool   `json:"allowRegistration"`
	DisplayName       string `json:"displayName"`
	Version           string `json:"version"`
}

type credentialsBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
}

func toUserResponse(u auth.User) userResponse {
	return userResponse{ID: u.ID, Username: u.Username, IsAdmin: u.IsAdmin}
}

// authStatus reports whether the server is in its "unlocked" first-run state
// and what the login page should offer (§10).
func (s *Server) authStatus(c *gin.Context) {
	cfg := s.serverConfig.Get()
	c.JSON(http.StatusOK, authStatusResponse{
		NeedsSetup:        s.users.Count() == 0,
		AllowRegistration: cfg.AllowRegistration,
		DisplayName:       cfg.DisplayName,
		Version:           config.Version,
	})
}

// authBootstrap creates the first (admin) account. Only reachable while no
// user exists yet — the server's "unlocked" state (§10, §11).
func (s *Server) authBootstrap(c *gin.Context) {
	if s.users.Count() != 0 {
		respondError(c, http.StatusConflict, CodeConflict, auth.ErrAlreadyBootstrapped.Error())
		return
	}

	var body credentialsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}

	user, err := s.users.Create(body.Username, body.Password, true)
	if err != nil {
		s.respondAuthError(c, err)
		return
	}

	s.setSessionCookie(c, s.webSessions.Create(user.ID))
	c.JSON(http.StatusCreated, toUserResponse(user))
}

// authRegister is self-service signup, only available once the server is
// bootstrapped and an admin has turned it on (§9's allowRegistration,
// default off). Registers a non-admin account and logs it straight in,
// mirroring bootstrap's own flow.
func (s *Server) authRegister(c *gin.Context) {
	if s.users.Count() == 0 {
		respondError(c, http.StatusConflict, CodeConflict, "server has not been set up yet")
		return
	}
	if !s.serverConfig.Get().AllowRegistration {
		respondError(c, http.StatusForbidden, CodeForbidden, "registration is disabled")
		return
	}

	var body credentialsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}

	user, err := s.users.Create(body.Username, body.Password, false)
	if err != nil {
		s.respondAuthError(c, err)
		return
	}

	s.setSessionCookie(c, s.webSessions.Create(user.ID))
	c.JSON(http.StatusCreated, toUserResponse(user))
}

// authLogin verifies credentials and starts a web session. A generic 401 on
// failure — whether the username or the password was wrong stays
// unspecified, so the response can't be used to enumerate usernames.
func (s *Server) authLogin(c *gin.Context) {
	var body credentialsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}

	user, err := s.users.Verify(body.Username, body.Password)
	if err != nil {
		respondError(c, http.StatusUnauthorized, CodeUnauthorized, "invalid username or password")
		return
	}

	s.setSessionCookie(c, s.webSessions.Create(user.ID))
	c.JSON(http.StatusOK, toUserResponse(user))
}

// authLogout revokes the current web session and clears its cookie.
func (s *Server) authLogout(c *gin.Context) {
	if token, err := c.Cookie(sessionCookieName); err == nil && token != "" {
		s.webSessions.Revoke(token)
	}
	s.clearSessionCookie(c)
	c.Status(http.StatusNoContent)
}

// authMe returns the current user.
func (s *Server) authMe(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	user, ok := s.users.FindByID(userID)
	if !ok {
		// The account backing this session was deleted after the cookie was
		// issued (§12b M11 revokes its tokens too, but close any residual
		// gap defensively rather than 500ing).
		respondError(c, http.StatusUnauthorized, CodeUnauthorized, "not authenticated")
		return
	}
	c.JSON(http.StatusOK, toUserResponse(user))
}

// getAdminConfig / updateAdminConfig expose config.yml to admins (§6, §9).
func (s *Server) getAdminConfig(c *gin.Context) {
	c.JSON(http.StatusOK, s.serverConfig.Get())
}

func (s *Server) updateAdminConfig(c *gin.Context) {
	var body serverconfig.Config
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	if err := s.serverConfig.Set(body); err != nil {
		s.log.Error("update config.yml failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to save config")
		return
	}
	c.JSON(http.StatusOK, s.serverConfig.Get())
}

// respondAuthError maps auth.UserStore errors to the unified error envelope.
func (s *Server) respondAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrUserExists):
		respondError(c, http.StatusConflict, CodeConflict, err.Error())
	default:
		// Username/password length validation and anything else from
		// UserStore.Create surfaces here as 400.
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	}
}

// setSessionCookie issues the web-session cookie: HttpOnly always, Secure
// unless --dev (matching --dev's existing role of relaxing security for the
// Vite proxy, which runs over plain HTTP), SameSite=Lax — same-origin
// fetch/WS plus SameSite is what keeps CSRF risk low without a token (§11).
func (s *Server) setSessionCookie(c *gin.Context, token string) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, token, int(auth.DefaultSessionTTL.Seconds()), "/", "", !s.cfg.Dev, true)
}

// clearSessionCookie expires the cookie immediately (logout).
func (s *Server) clearSessionCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, "", -1, "/", "", !s.cfg.Dev, true)
}
