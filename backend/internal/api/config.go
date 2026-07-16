package api

import (
	"net/http"
	"os/exec"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/config"
)

// configResponse mirrors GET /api/config (PROJECT_PLAN.md §6).
type configResponse struct {
	Root    string   `json:"root"`
	Shells  []string `json:"shells"`
	Version string   `json:"version"`
}

// getConfig returns the sandbox root, the installed shells from the allowlist,
// and the application version.
func (s *Server) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, configResponse{
		Root:    s.cfg.Root,
		Shells:  installedShells(s.cfg.Shells),
		Version: config.Version,
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
