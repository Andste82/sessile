package api

import (
	"net/http"
	"os"
	"sort"

	"github.com/gin-gonic/gin"
)

// listDirectories returns the immediate subdirectories of the sandbox root
// (§6). Only one level deep; entries are directory names relative to root.
func (s *Server) listDirectories(c *gin.Context) {
	entries, err := os.ReadDir(s.cfg.Root)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "read root failed")
		return
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// Skip the internal state dir and hidden entries.
		if name == ".tsm" || name[0] == '.' {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, name)
		}
	}
	sort.Strings(dirs)
	c.JSON(http.StatusOK, gin.H{"directories": dirs})
}
