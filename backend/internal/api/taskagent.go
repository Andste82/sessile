package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/mcp"
	"github.com/Andste82/sessile/backend/internal/tasks"
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

// answerQuestion answers what a task asked with `ask` (§4.12.4). The agent
// is holding that tool call open, so the answer goes straight back to it.
func (s *Server) answerQuestion(c *gin.Context) {
	if s.mcp == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tools are not available")
		return
	}
	var body struct {
		Answer string `json:"answer"`
		CallID string `json:"callId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Answer) == "" {
		respondError(c, http.StatusBadRequest, CodeValidation, "an answer is required")
		return
	}
	userID := c.MustGet(userIDKey).(string)
	taskID := c.Param("id")
	var err error
	if body.CallID != "" {
		err = s.mcp.Answer(userID, taskID, body.CallID, body.Answer)
	} else {
		err = s.mcp.AnswerTask(userID, taskID, body.Answer)
	}
	if errors.Is(err, mcp.ErrNoSuchQuestion) {
		respondError(c, http.StatusNotFound, CodeNotFound, "that task isn't waiting on a question any more")
		return
	}
	c.Status(http.StatusNoContent)
}

// listQuestions returns what a task is waiting to be told, for a page that
// opens after the question was asked.
func (s *Server) listQuestions(c *gin.Context) {
	if s.mcp == nil || s.tasks == nil {
		c.JSON(http.StatusOK, []mcp.QuestionMsg{})
		return
	}
	userID := c.MustGet(userIDKey).(string)
	if _, err := s.tasks.Get(userID, c.Param("id")); err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, s.mcp.Questions(userID, c.Param("id")))
}

// listApprovals returns a task's held write calls (§4.17.3).
func (s *Server) listApprovals(c *gin.Context) {
	if s.mcp == nil || s.tasks == nil {
		c.JSON(http.StatusOK, []mcp.ApprovalMsg{})
		return
	}
	userID := c.MustGet(userIDKey).(string)
	if _, err := s.tasks.Get(userID, c.Param("id")); err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, s.mcp.Pending(userID, c.Param("id")))
}

// listTasks returns the caller's tasks, for summaries on the dashboard.
func (s *Server) listTasks(c *gin.Context) {
	if s.tasks == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "tasks are not available")
		return
	}
	userID := c.MustGet(userIDKey).(string)
	list, err := s.tasks.List(userID)
	if err != nil {
		s.log.Error("list tasks failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to list tasks")
		return
	}
	// With each task its held write calls, so a page that opens late still
	// shows what is waiting for the user (§4.17.4).
	type withApprovals struct {
		tasks.Task
		Approvals []mcp.ApprovalMsg `json:"approvals"`
	}
	out := make([]withApprovals, 0, len(list))
	for _, t := range list {
		w := withApprovals{Task: t, Approvals: []mcp.ApprovalMsg{}}
		if s.mcp != nil {
			w.Approvals = s.mcp.Pending(userID, t.ID)
		}
		out = append(out, w)
	}
	c.JSON(http.StatusOK, out)
}
