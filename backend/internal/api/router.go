// Package api wires the Gin HTTP router, REST handlers and static SPA serving.
package api

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/config"
)

// maxBodyBytes bounds JSON request bodies (PROJECT_PLAN.md §11).
const maxBodyBytes = 4 << 10 // 4 KiB

// Server holds the dependencies shared by the HTTP handlers.
type Server struct {
	cfg *config.Config
	log *slog.Logger
}

// NewServer constructs a Server.
func NewServer(cfg *config.Config, log *slog.Logger) *Server {
	return &Server{cfg: cfg, log: log}
}

// Router builds the Gin engine with all routes registered.
func (s *Server) Router(dist fs.FS) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestLogger(s.log))
	r.Use(limitBody(maxBodyBytes))

	apiGroup := r.Group("/api")
	{
		apiGroup.GET("/health", s.health)
		apiGroup.GET("/config", s.getConfig)
	}

	s.registerSPA(r, dist)
	return r
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// registerSPA serves embedded static assets and falls back to index.html for
// any non-/api, non-/ws GET so the Vue router can handle client-side routes.
func (s *Server) registerSPA(r *gin.Engine, dist fs.FS) {
	if dist == nil {
		return
	}
	fileServer := http.FileServer(http.FS(dist))
	index, _ := fs.ReadFile(dist, "index.html")

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
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}
