package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"time"

	"github.com/gin-gonic/gin"
)

// hostopsUploadMaxBytes bounds one upload. Generous — files this feature
// targets, not a bulk-transfer tool (§1) — but it is also the real memory
// cost of one upload: FileTransport.Write takes the whole body at once
// (§4.10), no streaming, so this cap is what it sounds like, not just a
// request-size sanity check.
const hostopsUploadMaxBytes = 512 << 20 // 512 MiB

// hostopsDownloadMaxBytes bounds one download the same way
// hostopsUploadMaxBytes bounds one upload — see downloadHostFile.
const hostopsDownloadMaxBytes = hostopsUploadMaxBytes

// hostopsTransferTimeout bounds one download/upload — longer than
// hostopsTimeout's quick-metadata budget, since a file read/write can
// legitimately take longer than a ps call or a directory listing.
const hostopsTransferTimeout = 5 * time.Minute

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

	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTransferTimeout)
	defer cancel()

	// Stat first so a file whose reported size already exceeds the cap is
	// rejected before a single byte moves — the common case (a large but
	// honest regular file). This is a courtesy, not the safety mechanism:
	// a special file (e.g. /dev/zero) can report a size that has nothing to
	// do with what reading it actually produces, which is exactly why the
	// stream below is also capped independently.
	if stat, err := ops.Files().Stat(ctx, resolvedPath); err == nil && stat.Size > hostopsDownloadMaxBytes {
		respondError(c, http.StatusRequestEntityTooLarge, CodeValidation, "file exceeds the download size limit")
		return
	}

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
	// Streamed via io.Copy, not buffered whole (contrast the old
	// ops.Files().Read + c.Data): the response is written as bytes arrive
	// over SFTP/disk, so this handler's own memory footprint stays constant
	// regardless of the remote file's real size. Capped at
	// hostopsDownloadMaxBytes+1 bytes so a file whose Stat lied (a device
	// or pseudo-file) can't stream forever either — it's silently truncated
	// rather than erroring, since headers are already written by this
	// point and the status code can't change mid-response.
	_, _ = io.CopyN(c.Writer, f, hostopsDownloadMaxBytes+1)
}

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

	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			respondError(c, http.StatusRequestEntityTooLarge, CodeValidation, "upload exceeds the size limit")
			return
		}
		respondError(c, http.StatusBadRequest, CodeValidation, "failed to read upload body")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), hostopsTransferTimeout)
	defer cancel()

	if err := ops.Files().Write(ctx, resolvedPath, data); err != nil {
		s.log.Warn("upload failed", "id", id, "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "upload failed")
		return
	}
	c.Status(http.StatusCreated)
}
