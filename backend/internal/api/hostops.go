package api

import (
	"context"
	"errors"
	"fmt"
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
// session's own processes; "all" always shows the whole target's process
// forest (rootPID nil — there is no portable single "whole host" root pid
// to hand back instead; see Platform.ProcessTree's doc comment).
//
// A local session has a real, known shell PID (info.PID, from the kernel —
// §4.7's same source) — always exact, no round trip needed, so that's used
// directly rather than routing through the Transport at all. Every other
// target type (SSH today; a future non-local transport later) asks its
// HostSession instead (SessionAware, §4.10) — SSH resolves it via a PID an
// exec preamble records for itself, with a socket-matching fallback for
// what that can't cover; a future transport answers however fits its own
// mechanism. scoped reports which one the caller actually got: a "session"
// request that couldn't be resolved falls back to the whole host (rootPID
// nil) with scoped=false, never a silently wrong narrower-looking answer.
func (s *Server) resolveProcessTreeRoot(c *gin.Context, ops *hostops.HostSession, info session.Info) (rootPID *int, scoped bool) {
	if c.Query("scope") == "all" {
		return nil, false
	}
	if info.TargetType == session.TargetLocal {
		pid := info.PID
		return &pid, true
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTimeout)
	defer cancel()
	if pid, ok := ops.SessionRootPID(ctx); ok {
		return &pid, true
	}
	return nil, false
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
	// rootPid is null in the response when this is a forest (scope=all, or
	// an unresolved scope=session) — there is no single root to name.
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

// resolveDestructiveHostopsPath is resolveHostopsPath plus a guard against
// the one path listing legitimately needs to resolve but a destructive
// operation must never accept: the target's own root.
//
// session.ResolvePath correctly returns the sandbox root itself for
// path="." or "" (so listHostFiles can list it) — but Delete/Move/Copy
// calling resolveHostopsPath directly inherited that same resolution with
// no guard against it, so path="." reached FileTransport.Remove/Rename/
// Copy pointed straight at the workspace root: DELETE …/files?path=.
// listed every entry under it, removed each one, then removed the root
// directory itself — os.RemoveAll on the shared local-host workspace,
// reachable by any authenticated user who owns any local session, not
// just the resolveDir/ResolvePath escape-the-sandbox class of bug the
// existing sandbox tests already cover (this path never left the sandbox
// — it targeted the sandbox itself).
//
// SSH has no sandbox root to compare against, so the same rule is applied
// to the two paths that unambiguously mean "here" or "everything" if left
// unresolved: "." and "/".
func (s *Server) resolveDestructiveHostopsPath(info session.Info, userPath string) (resolved, display string, err error) {
	resolved, display, err = s.resolveHostopsPath(info, userPath)
	if err != nil {
		return "", "", err
	}
	if info.TargetType == session.TargetSSH {
		if resolved == "." || resolved == "/" {
			return "", "", fmt.Errorf("refusing to operate on %q", resolved)
		}
		return resolved, display, nil
	}
	if root, rootErr := session.ResolvePath(s.workspaceRoot, "."); rootErr == nil && resolved == root {
		return "", "", errors.New("refusing to operate on the workspace root")
	}
	return resolved, display, nil
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

	// SSH isn't sandboxed (§4.5 only applies to local), so unlike the local
	// branch's normalizeRel, "path" here is worth canonicalizing to the
	// target's own real absolute form (via the SFTP protocol's own
	// REALPATH) — a synthetic relative starting point with no way "above"
	// it would defeat the point of having no sandbox to begin with: the
	// user can navigate anywhere their login already can, siblings and
	// parents included, exactly like a real shell would let them.
	if info.TargetType == session.TargetSSH {
		if real, err := ops.Files().Resolve(ctx, resolvedPath); err == nil {
			resolvedPath, displayPath = real, real
		}
	}

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

	resolvedSrc, _, err := s.resolveDestructiveHostopsPath(info, body.Src)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	resolvedDst, _, err := s.resolveDestructiveHostopsPath(info, body.Dst)
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
