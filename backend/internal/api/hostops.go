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

// hostopsTimeout bounds one hostops round-trip — a remote ps/PowerShell
// call, an SFTP list/rename — not something that should hang a request
// indefinitely. Generous because a directory listing or a remote command
// over a slow link (§11.1) is still meant to succeed, just not hang forever.
const hostopsTimeout = 15 * time.Second

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

	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTimeout)
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

// dirEntryJSON mirrors hostops.DirEntry for the wire (§6, §4.10).
type dirEntryJSON struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func toDirEntryJSON(e hostops.DirEntry) dirEntryJSON {
	return dirEntryJSON{Name: e.Name, IsDir: e.IsDir, Size: e.Size, ModTime: e.ModTime.UTC().Format(time.RFC3339)}
}

// resolveHostopsPath turns a caller-supplied path into what FileTransport
// should actually use, plus the display form the response echoes back.
//
// Local sessions are sandboxed exactly like /api/directories (§4.5): the
// path is relative to the workspace root, "" and "." both mean the root,
// and anything escaping it is rejected before it ever reaches hostops.
//
// SSH sessions are not sandboxed beyond the session ownership check that
// already gated reaching this handler (§4.10's design note, §11) — the
// user already has a full interactive shell on that host through this same
// session, so there is no narrower boundary to enforce. "" means the
// sftp-server's own default (typically the login user's home directory).
func (s *Server) resolveHostopsPath(info session.Info, userPath string) (resolved, display string, err error) {
	if info.TargetType == session.TargetSSH {
		if userPath == "" {
			userPath = "."
		}
		return userPath, userPath, nil
	}
	resolved, err = session.ResolvePath(s.workspaceRoot, userPath)
	if err != nil {
		return "", "", err
	}
	return resolved, normalizeRel(userPath), nil
}

func (s *Server) listHostFiles(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	ops, info, err := s.manager.HostOps(c.Param("id"), userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}

	resolvedPath, displayPath, err := s.resolveHostopsPath(info, c.Query("path"))
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTimeout)
	defer cancel()

	entries, err := ops.Files().List(ctx, resolvedPath)
	if err != nil {
		s.log.Warn("list files failed", "id", c.Param("id"), "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "list files failed")
		return
	}

	out := make([]dirEntryJSON, 0, len(entries))
	for _, e := range entries {
		out = append(out, toDirEntryJSON(e))
	}
	c.JSON(http.StatusOK, gin.H{"path": displayPath, "entries": out})
}

type moveFileBody struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

func (s *Server) moveHostFile(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	ops, info, err := s.manager.HostOps(c.Param("id"), userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}

	var body moveFileBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Src == "" || body.Dst == "" {
		respondError(c, http.StatusBadRequest, CodeValidation, "src and dst are required")
		return
	}

	resolvedSrc, _, err := s.resolveHostopsPath(info, body.Src)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	resolvedDst, _, err := s.resolveHostopsPath(info, body.Dst)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTimeout)
	defer cancel()

	if err := ops.Files().Rename(ctx, resolvedSrc, resolvedDst); err != nil {
		s.log.Warn("move file failed", "id", c.Param("id"), "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "move failed")
		return
	}
	c.Status(http.StatusNoContent)
}
