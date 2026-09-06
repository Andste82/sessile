package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hostops"
)

// downloadHostFile streams path to the response with no size cap — the
// only reason a cap ever existed was that the whole file used to be
// buffered in memory (ops.Files().Read) before the first byte was written;
// Open below streams instead, so the server's own memory footprint stays
// constant regardless of the remote file's size. An endless source (a
// device file on an unsandboxed SSH session, §4.5) streams for as long as
// the client keeps reading — that's accepted, not guarded against: the
// abort path that matters is the client disconnecting, which io.Copy
// detects on its own via a failed write, and a genuinely stalled remote
// (frozen network mid-read) has no local fix worth building — closing the
// SFTP client or its SSH channel doesn't unblock a hung SFTP read, only
// tearing down the whole SSH connection does, and that connection also
// carries the session's terminal. See PROJECT_PLAN.md §11.
func (s *Server) downloadHostFile(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	id := c.Param("id")
	ops, info, err := s.manager.HostOps(id, userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}

	userPath := c.Query("path")
	if userPath == "" {
		respondError(c, http.StatusBadRequest, CodeValidation, "path is required")
		return
	}
	resolvedPath, _, err := s.resolveHostopsPath(info, userPath)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}

	ctx := c.Request.Context()

	// Stat's reported size is only meaningful for a regular file — a
	// special file (e.g. /dev/zero) can report any size at all with no
	// relation to what reading it actually produces, so Content-Length is
	// only set when IsRegular says the size can be trusted. A Stat failure
	// isn't fatal here: Open below is the real read and reports its own
	// error if the path genuinely doesn't exist.
	stat, statErr := ops.Files().Stat(ctx, resolvedPath)

	f, err := ops.Files().Open(ctx, resolvedPath)
	if err != nil {
		s.log.Warn("download failed", "id", id, "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "download failed")
		return
	}
	defer f.Close()

	// path.Base, not filepath.Base: local paths are POSIX-separated too
	// (the server only ever runs on Linux, §2), and SSH paths always are
	// (SFTP is POSIX-separated by protocol regardless of remote OS).
	filename := path.Base(resolvedPath)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("Content-Type", "application/octet-stream")
	if statErr == nil && stat.IsRegular {
		c.Header("Content-Length", strconv.FormatInt(stat.Size, 10))
	}
	c.Status(http.StatusOK)

	if n, err := io.Copy(c.Writer, f); err != nil {
		s.log.Warn("download interrupted", "id", id, "written", n, "err", err)
	}
}

// uploadHostFile streams the request body into a ".part" staging file next
// to the real destination and only commits it (atomic overwrite rename)
// once the whole body has been written and closed with no error — an
// aborted or failed upload leaves the destination untouched, not a
// partially-written file. No size cap: Create/Copy stream, so nothing here
// holds a whole file in memory regardless of size (the same reasoning as
// downloadHostFile above).
//
// The request context (not a detached one) drives every step here: the
// old buffer-then-write shape used a detached context deliberately, so an
// aborted upload still finished writing what it already had — but with a
// staging-file-then-commit shape that reasoning inverts. An abort should
// stop the copy and clean up the stub; atomicity now comes from the commit
// rename, not from finishing the write no matter what.
func (s *Server) uploadHostFile(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	id := c.Param("id")
	ops, info, err := s.manager.HostOps(id, userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}

	userPath := c.Query("path")
	if userPath == "" {
		respondError(c, http.StatusBadRequest, CodeValidation, "path is required")
		return
	}
	resolvedPath, _, err := s.resolveHostopsPath(info, userPath)
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}

	ctx := c.Request.Context()
	tmp := resolvedPath + ".part"

	w, err := ops.Files().Create(ctx, tmp)
	if err != nil {
		s.log.Warn("upload failed", "id", id, "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "upload failed")
		return
	}

	if _, err := io.Copy(w, c.Request.Body); err != nil {
		w.Close()
		s.cleanupUploadStub(ctx, ops.Files(), tmp)
		respondError(c, http.StatusBadRequest, CodeValidation, "failed to read upload body")
		return
	}
	// Close is checked separately from Copy: a buffered writer surfaces a
	// full destination (ENOSPC/EDQUOT) here, not from the writes that
	// filled the buffer before it.
	if err := w.Close(); err != nil {
		s.cleanupUploadStub(ctx, ops.Files(), tmp)
		s.log.Warn("upload failed", "id", id, "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "upload failed")
		return
	}
	if err := ops.Files().Commit(ctx, tmp, resolvedPath); err != nil {
		s.cleanupUploadStub(ctx, ops.Files(), tmp)
		s.log.Warn("upload failed", "id", id, "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "upload failed")
		return
	}
	c.Status(http.StatusCreated)
}

// cleanupUploadStub best-effort removes a ".part" staging file after a
// failed or aborted upload — derived from ctx via context.WithoutCancel so
// any request-scoped values still carry through, but stripped of ctx's own
// cancellation, since cleanup must still happen after the same abort that
// triggered it, not be cancelled by it.
func (s *Server) cleanupUploadStub(ctx context.Context, files hostops.FileTransport, tmp string) {
	_ = files.Remove(context.WithoutCancel(ctx), tmp)
}
