package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hostops"
	"github.com/Andste82/sessile/backend/internal/session"
)

// processTreeTimeout bounds one process-tree fetch — a remote `ps`/
// PowerShell round-trip over SSH, not something that should ever hang the
// request indefinitely.
const processTreeTimeout = 15 * time.Second

// processJSON mirrors hostops.Process for the wire (§6, §4.10).
type processJSON struct {
	PID      int           `json:"pid"`
	PPID     int           `json:"ppid"`
	Command  string        `json:"command"`
	Children []processJSON `json:"children"`
}

func toProcessJSON(p hostops.Process) processJSON {
	children := make([]processJSON, 0, len(p.Children))
	for _, c := range p.Children {
		children = append(children, toProcessJSON(c))
	}
	return processJSON{PID: p.PID, PPID: p.PPID, Command: p.Command, Children: children}
}

// processTreeRootPID picks what to root the tree at. Local sessions have a
// real, known shell PID (info.PID, from the kernel — §4.7's same source).
// SSH sessions do not: Backend.Pid() is always 0 for SSH (§4.2, "no local
// meaning"), and there is no reliable, non-heuristic way to learn a remote
// shell's own PID over the SSH protocol without either running an
// intrusive probe on the interactive channel itself (risking interleaving
// with real user input/output) or a tty-matching heuristic — exactly the
// class of unreliable inference PROJECT_PLAN.md already rejected once for
// a different feature (§4.7's removed activity classifier). So an SSH
// session's tree is rooted at PID 1: the whole target's process tree,
// honestly scoped rather than pretending to be session-scoped.
func processTreeRootPID(info session.Info) int {
	if info.TargetType == session.TargetSSH {
		return 1
	}
	return info.PID
}

func (s *Server) getProcessTree(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	ops, info, err := s.manager.HostOps(c.Param("id"), userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), processTreeTimeout)
	defer cancel()

	rootPID := processTreeRootPID(info)
	procs, err := ops.ProcessTree(ctx, rootPID)
	if err != nil {
		if errors.Is(err, hostops.ErrUnsupportedPlatform) {
			respondError(c, http.StatusNotImplemented, CodeUnsupportedPlatform, err.Error())
			return
		}
		s.log.Warn("process tree failed", "id", c.Param("id"), "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "process tree failed")
		return
	}

	out := make([]processJSON, 0, len(procs))
	for _, p := range procs {
		out = append(out, toProcessJSON(p))
	}
	c.JSON(http.StatusOK, gin.H{"rootPid": rootPID, "processes": out})
}
