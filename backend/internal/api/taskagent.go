package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/mcp"
)

// The task agent's tools (PROJECT_PLAN.md §4.17): approvals for held write
// calls, and telling connected agents when their tool list changed.

// SetMCP wires the sessile MCP server.
func (s *Server) SetMCP(m *mcp.Server) { s.mcp = m }

// toolsChanged is called after anything that changes a user's tools.
func (s *Server) toolsChanged(userID string) {
	if s.mcp != nil {
		s.mcp.ToolsChanged(userID)
	}
}

type approvalBody struct {
	Approve bool `json:"approve"`
}

func (s *Server) decideApproval(c *gin.Context) {
	if s.mcp == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tools are not available")
		return
	}
	var body approvalBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	err := s.mcp.Decide(c.MustGet(userIDKey).(string), c.Param("id"), c.Param("callId"), body.Approve)
	if errors.Is(err, mcp.ErrNoSuchApproval) {
		respondError(c, http.StatusNotFound, CodeNotFound, "that call isn't waiting for approval any more")
		return
	}
	c.Status(http.StatusNoContent)
}

// listTasks returns the caller's tasks, for summaries on the dashboard.
func (s *Server) listTasks(c *gin.Context) {
	if s.tasks == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tasks are not available")
		return
	}
	list, err := s.tasks.List(c.MustGet(userIDKey).(string))
	if err != nil {
		s.log.Error("list tasks failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to list tasks")
		return
	}
	c.JSON(http.StatusOK, list)
}
