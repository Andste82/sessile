package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// userIDKey is the gin.Context key requireAuth sets, and every authed
// handler reads from — never from a client-supplied value in the request
// body or query string (§10, §11).
const userIDKey = "userID"

// requireAuth reads the session cookie, looks up its owner — which slides
// the token's TTL (auth.SessionStore.Lookup) — and stores it under
// userIDKey for downstream handlers. A missing or invalid/expired cookie is
// answered with 401 and the request goes no further.
func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := c.Cookie(sessionCookieName)
		if err != nil || token == "" {
			respondError(c, http.StatusUnauthorized, CodeUnauthorized, "not authenticated")
			c.Abort()
			return
		}
		userID, ok := s.webSessions.Lookup(token)
		if !ok {
			respondError(c, http.StatusUnauthorized, CodeUnauthorized, "not authenticated")
			c.Abort()
			return
		}
		c.Set(userIDKey, userID)
		c.Next()
	}
}

// requireAdmin must run after requireAuth. It 403s a valid session that
// isn't an admin account.
func (s *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet(userIDKey).(string)
		user, ok := s.users.FindByID(userID)
		if !ok || !user.IsAdmin {
			respondError(c, http.StatusForbidden, CodeForbidden, "admin access required")
			c.Abort()
			return
		}
		c.Next()
	}
}

// requestLogger logs one structured line per request via slog.
func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("http",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"dur_ms", time.Since(start).Milliseconds(),
		)
	}
}

// limitBody caps the size of request bodies to guard the JSON endpoints.
func limitBody(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		c.Next()
	}
}
