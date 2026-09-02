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

	data, err := ops.Files().Read(ctx, resolvedPath)
	if err != nil {
		s.log.Warn("download failed", "id", id, "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "download failed")
		return
	}

	// path.Base, not filepath.Base: local paths are POSIX-separated too
	// (the server only ever runs on Linux, §2), and SSH paths always are
	// (SFTP is POSIX-separated by protocol regardless of remote OS).
	filename := path.Base(resolvedPath)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Data(http.StatusOK, "application/octet-stream", data)
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
