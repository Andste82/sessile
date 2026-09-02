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

// resolveProcessTreeRoot picks what ProcessTree roots at, per the optional
// `?scope=` query param — "session" (the default) narrows to just this
// session's own processes; "all" always shows the whole target.
//
// Local sessions have a real, known shell PID (info.PID, from the kernel —
// §4.7's same source), so "session" is always exact there. SSH sessions
// don't: Backend.Pid() is always 0 for SSH (§4.2, "no local meaning"), so
// "session" there depends on HostSession.SessionRootPID (§4.10) — the
// session's own started command records its own PID via an exec preamble
// sshpty.Start writes for it, read back here; a socket-matching fallback
// covers the rest (a Windows target, or the rare case the preamble read
// fails). scoped reports which one the caller actually got: a "session"
// request that couldn't be resolved by either falls back to the whole
// host (rootPID 1) with scoped=false, never a silently wrong
// narrower-looking answer.
func (s *Server) resolveProcessTreeRoot(c *gin.Context, ops *hostops.HostSession, info session.Info) (rootPID int, scoped bool) {
	if c.Query("scope") == "all" {
		return 1, false
	}
	if info.TargetType != session.TargetSSH {
		return info.PID, true
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTimeout)
	defer cancel()
	if pid, ok := ops.SessionRootPID(ctx); ok {
		return pid, true
	}
	return 1, false
}

func (s *Server) getProcessTree(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	ops, info, err := s.manager.HostOps(c.Param("id"), userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}

	rootPID, scoped := s.resolveProcessTreeRoot(c, ops, info)

	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTimeout)
	defer cancel()

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
	c.JSON(http.StatusOK, gin.H{"rootPid": rootPID, "scoped": scoped, "processes": out})
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
