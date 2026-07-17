package api

import (
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/session"
)

// listDirectories returns the immediate subdirectories of a path under the
// sandbox root (§6). The optional `path` query navigates into subdirectories
// (relative to root; empty or "." is the root). The path is validated by the
// sandbox check, so traversal or symlink escapes are rejected.
func (s *Server) listDirectories(c *gin.Context) {
	path := c.Query("path")

	dirs, err := session.ListDirs(s.cfg.Root, path)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid directory")
		return
	}

	clean := normalizeRel(path)
	c.JSON(http.StatusOK, gin.H{
		"path":        clean,
		"parent":      parentRel(clean),
		"directories": dirs,
	})
}

// normalizeRel cleans a user-supplied relative path into the canonical
// forward-slash form the API echoes back; "" and "." both mean the root.
func normalizeRel(p string) string {
	if p == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// parentRel returns the parent of a cleaned relative path, or nil at the root.
func parentRel(clean string) *string {
	if clean == "." || clean == "" {
		return nil
	}
	parent := filepath.ToSlash(filepath.Dir(clean))
	return &parent
}
