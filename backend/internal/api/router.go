// Package api wires the Gin HTTP router, REST handlers and static SPA serving.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/auth"
	"github.com/Andste82/sessile/backend/internal/config"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/serverconfig"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/ws"
)

// maxBodyBytes bounds JSON request bodies (PROJECT_PLAN.md §11).
const maxBodyBytes = 4 << 10 // 4 KiB

// Server holds the dependencies shared by the HTTP handlers.
type Server struct {
	cfg     *config.Config
	manager *session.Manager
	ws      *ws.Handler
	log     *slog.Logger

	// workspaceRoot is the local-host sandbox root, <data-dir>/workspace
	// (PROJECT_PLAN.md §4.5, §9) — fixed, not operator-supplied.
	workspaceRoot string
	// serverConfig holds config.yml (displayName, allowRegistration,
	// allowLocalHost). Not yet consumed by any handler beyond auth.go's admin
	// endpoints — the allowLocalHost gate on session creation/directories
	// lands with SSH sessions (§12b M17).
	serverConfig *serverconfig.Store

	users       *auth.UserStore
	webSessions *auth.SessionStore
	hosts       *hosts.Registry
}

// NewServer constructs a Server.
func NewServer(cfg *config.Config, manager *session.Manager, wsHandler *ws.Handler, log *slog.Logger, workspaceRoot string, serverCfg *serverconfig.Store, users *auth.UserStore, webSessions *auth.SessionStore, hostsRegistry *hosts.Registry) *Server {
	return &Server{
		cfg: cfg, manager: manager, ws: wsHandler, log: log,
		workspaceRoot: workspaceRoot, serverConfig: serverCfg,
		users: users, webSessions: webSessions, hosts: hostsRegistry,
	}
}

// Router builds the Gin engine with all routes registered.
func (s *Server) Router(dist fs.FS) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger(s.log))
	r.Use(limitBody(maxBodyBytes))

	// Public: no session cookie required. Everything else in /api/* is
	// gated by requireAuth (§10, §11) — a client-supplied user id is never
	// trusted, so every authed handler resolves its actor from the cookie,
	// not from anything in the request body or query string.
	publicGroup := r.Group("/api")
	{
		publicGroup.GET("/health", s.health)
		publicGroup.GET("/auth/status", s.authStatus)
		publicGroup.POST("/auth/bootstrap", s.authBootstrap)
		publicGroup.POST("/auth/register", s.authRegister)
		publicGroup.POST("/auth/login", s.authLogin)
	}

	authGroup := r.Group("/api")
	authGroup.Use(s.requireAuth())
	{
		authGroup.POST("/auth/logout", s.authLogout)
		authGroup.GET("/auth/me", s.authMe)

		admin := authGroup.Group("")
		admin.Use(s.requireAdmin())
		{
			admin.GET("/admin/config", s.getAdminConfig)
			admin.PUT("/admin/config", s.updateAdminConfig)
			admin.GET("/admin/users", s.listUsers)
			admin.DELETE("/admin/users/:id", s.deleteUser)
			admin.PATCH("/admin/users/:id", s.setUserAdmin)
		}

		authGroup.GET("/config", s.getConfig)
		authGroup.GET("/hosts", s.listHosts)
		authGroup.POST("/hosts", s.createHost)
		authGroup.GET("/hosts/:id", s.getHost)
		authGroup.PUT("/hosts/:id", s.updateHost)
		authGroup.DELETE("/hosts/:id", s.deleteHost)
		authGroup.POST("/hosts/:id/host-key/probe", s.probeHostKey)
		authGroup.POST("/hosts/:id/host-key/trust", s.trustHostKey)
		authGroup.POST("/hosts/:id/exchange-keys", s.exchangeHostKeys)
		authGroup.GET("/sessions", s.listSessions)
		authGroup.POST("/sessions", s.createSession)
		authGroup.GET("/sessions/:id", s.getSession)
		authGroup.DELETE("/sessions/:id", s.deleteSession)
		authGroup.PATCH("/sessions/:id", s.renameSession)
		authGroup.POST("/sessions/:id/restart", s.restartSession)
		authGroup.GET("/directories", s.listDirectories)
	}

	if s.ws != nil {
		r.GET("/ws/sessions/:id", s.requireAuth(), func(c *gin.Context) {
			s.ws.Handle(c.Writer, c.Request, c.Param("id"), c.MustGet(userIDKey).(string))
		})
		// Session list state rather than terminal bytes (§5.1). Separate from
		// the terminal socket because the dashboard mounts no terminal.
		r.GET("/ws/events", s.requireAuth(), func(c *gin.Context) {
			s.ws.HandleEvents(c.Writer, c.Request, c.MustGet(userIDKey).(string))
		})
	}

	s.registerSPA(r, dist)
	return r
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Cache-Control for the SPA (§10: the frontend is built with content hashes and
// embedded in the binary).
//
// The build gives everything under /assets a content hash, so those files are
// immutable by construction: a changed file is a changed URL. index.html is the
// opposite — one URL whose contents name the current hashed bundles — and it is
// the whole reason this exists. Served with no cache headers at all, a browser
// is free to apply a heuristic freshness lifetime, and Firefox does: after an
// upgrade it went on running the previous bundle out of its cache, talking to
// the new backend, until someone reloaded past the cache by hand.
const (
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// registerSPA serves embedded static assets and falls back to index.html for
// any non-/api, non-/ws GET so the Vue router can handle client-side routes.
func (s *Server) registerSPA(r *gin.Engine, dist fs.FS) {
	if dist == nil {
		return
	}
	fileServer := http.FileServer(http.FS(dist))
	index, _ := fs.ReadFile(dist, "index.html")
	// "no-cache" means revalidate, not "do not store", so the usual answer to a
	// reload is a 304 with no body — the ETag is what keeps that cheap.
	indexETag := etag(index)

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if c.Request.Method != http.MethodGet ||
			strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/ws") {
			respondError(c, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		// Serve the asset if it exists; otherwise fall back to index.html.
		if p != "/" {
			if f, err := dist.Open(strings.TrimPrefix(p, "/")); err == nil {
				_ = f.Close()
				if strings.HasPrefix(p, "/assets/") {
					c.Header("Cache-Control", cacheImmutable)
				} else {
					// favicon.svg and anything else the build leaves unhashed:
					// one URL, changing contents, same problem as index.html.
					c.Header("Cache-Control", cacheRevalidate)
				}
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}

		c.Header("Cache-Control", cacheRevalidate)
		c.Header("ETag", indexETag)
		if match := c.GetHeader("If-None-Match"); match != "" && strings.Contains(match, indexETag) {
			c.Status(http.StatusNotModified)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}

// etag returns a strong entity tag for body: the first 16 hex digits of its
// SHA-256, quoted as RFC 9110 requires.
func etag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}
