package api

import (
	"net/http"
	"os/exec"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/config"
)

// configResponse mirrors GET /api/config (PROJECT_PLAN.md §6). Root is gone
// as of M17 — a local-host workspace path means nothing to a user who isn't
// permitted to use it, and now that permission is exactly what
// allowLocalHost reports.
type configResponse struct {
	Shells         []string `json:"shells"`
	Version        string   `json:"version"`
	AllowLocalHost bool     `json:"allowLocalHost"`
}

// getConfig returns the installed shells from the allowlist, the
// application version, and whether local-host sessions are permitted.
func (s *Server) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, configResponse{
		Shells:         installedShells(s.cfg.Shells),
		Version:        config.Version,
		AllowLocalHost: s.serverConfig.Get().AllowLocalHost,
	})
}

// installedShells returns the subset of the allowlist actually found on PATH.
func installedShells(allowlist []string) []string {
	found := make([]string, 0, len(allowlist))
	for _, sh := range allowlist {
		if _, err := exec.LookPath(sh); err == nil {
			found = append(found, sh)
		}
	}
	return found
}
