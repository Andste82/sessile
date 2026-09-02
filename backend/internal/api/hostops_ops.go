package api

import (
	"context"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Andste82/sessile/backend/internal/hostops"
	"github.com/Andste82/sessile/backend/internal/session"
)

// hostopLongTimeout bounds one Delete/Copy — generous, since these can
// genuinely take a while (many files, a large file), unlike the quick
// metadata calls hostopsTimeout bounds. Detached from the request's own
// context, since the response returns before the operation finishes.
const hostopLongTimeout = 10 * time.Minute

// hostopRetention is how long a finished op's status stays queryable — long
// enough for a client whose WS event (§5.2) was missed to still poll it
// once as a fallback, short enough not to leak memory on a long-running
// server.
const hostopRetention = 2 * time.Minute

// hostopStatus tracks one in-flight or finished Delete/Copy, polled via GET
// .../hostops/ops/:opId as the fallback for when the WS event channel is
// down — the same reason §5.1's list poll exists.
type hostopStatus struct {
	id     string
	userID string // set once at creation; op ids are otherwise unguessable UUIDs, but never trust that alone (§4.5's discipline)

	mu      sync.Mutex
	kind    string // "delete" | "copy"
	done    int64
	total   int64
	status  string // "running" | "done" | "error"
	message string
}

type hostopStatusJSON struct {
	OpID    string `json:"opId"`
	Kind    string `json:"kind"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (h *hostopStatus) snapshot() hostopStatusJSON {
	h.mu.Lock()
	defer h.mu.Unlock()
	return hostopStatusJSON{OpID: h.id, Kind: h.kind, Done: h.done, Total: h.total, Status: h.status, Message: h.message}
}

func (h *hostopStatus) setProgress(done, total int64) {
	h.mu.Lock()
	h.done, h.total = done, total
	h.mu.Unlock()
}

func (h *hostopStatus) finish(status, message string) {
	h.mu.Lock()
	h.status, h.message = status, message
	h.mu.Unlock()
}

func (s *Server) startOp(userID, kind string) *hostopStatus {
	op := &hostopStatus{id: uuid.NewString(), userID: userID, kind: kind, status: "running"}
	s.opsMu.Lock()
	s.ops[op.id] = op
	s.opsMu.Unlock()
	return op
}

func (s *Server) retireOp(id string) {
	time.AfterFunc(hostopRetention, func() {
		s.opsMu.Lock()
		delete(s.ops, id)
		s.opsMu.Unlock()
	})
}

func (s *Server) finishOpOK(op *hostopStatus, sessionID, userID string, done, total int64) {
	op.setProgress(done, total)
	op.finish("ok", "")
	s.manager.PublishHostop(userID, session.HostopDoneMsg{Type: "hostopDone", SessionID: sessionID, OpID: op.id, Status: "ok"})
}

func (s *Server) finishOpError(op *hostopStatus, sessionID, userID string, err error) {
	s.log.Warn("hostop failed", "opId", op.id, "kind", op.kind, "err", err)
	op.finish("error", err.Error())
	s.manager.PublishHostop(userID, session.HostopDoneMsg{Type: "hostopDone", SessionID: sessionID, OpID: op.id, Status: "error", Message: err.Error()})
}

func (s *Server) getHostopStatus(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	s.opsMu.Lock()
	op, ok := s.ops[c.Param("opId")]
	s.opsMu.Unlock()
	// An op that never existed and one that belongs to someone else are
	// reported identically — same "not found, not forbidden" discipline
	// as every other id lookup in this API (§4.5, §10).
	if !ok || op.userID != userID {
		respondError(c, http.StatusNotFound, CodeNotFound, "operation not found")
		return
	}
	c.JSON(http.StatusOK, op.snapshot())
}

func (s *Server) deleteHostFile(c *gin.Context) {
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

	op := s.startOp(userID, "delete")
	go s.runDelete(op, ops, id, userID, resolvedPath)
	c.JSON(http.StatusAccepted, gin.H{"opId": op.id})
}

// runDelete deletes path — a single file directly, or a directory by
// listing its immediate children, deleting each (a subdirectory is removed
// whole by its own recursive Remove call, counted here as one unit
// regardless of how much it contains), then the now-empty directory
// itself. Progress is reported at that granularity — real, not simulated,
// but coarser than a full deep walk would give; §4.10/§5.2 document this
// as the deliberate tradeoff for keeping this simple.
func (s *Server) runDelete(op *hostopStatus, ops *hostops.HostSession, sessionID, userID, target string) {
	defer s.retireOp(op.id)
	ctx, cancel := context.WithTimeout(context.Background(), hostopLongTimeout)
	defer cancel()

	s.manager.PublishHostop(userID, session.HostopStartedMsg{
		Type: "hostopStarted", SessionID: sessionID, OpID: op.id, Kind: "delete", Path: target,
	})

	entries, err := ops.Files().List(ctx, target)
	if err != nil {
		// Not a directory (or doesn't behave like one) — treat as one file.
		if rmErr := ops.Files().Remove(ctx, target); rmErr != nil {
			s.finishOpError(op, sessionID, userID, rmErr)
			return
		}
		s.finishOpOK(op, sessionID, userID, 1, 1)
		return
	}

	total := int64(len(entries))
	s.reportProgress(op, sessionID, userID, 0, total)

	for i, e := range entries {
		// path.Join, not filepath.Join: both local and SSH targets here are
		// always POSIX-separated (the server only ever runs on Linux, §2;
		// SFTP paths are POSIX-separated by protocol regardless of remote
		// OS) — see §4.10.
		if err := ops.Files().Remove(ctx, path.Join(target, e.Name)); err != nil {
			s.finishOpError(op, sessionID, userID, err)
			return
		}
		s.reportProgress(op, sessionID, userID, int64(i+1), total)
	}

	if err := ops.Files().Remove(ctx, target); err != nil {
		s.finishOpError(op, sessionID, userID, err)
		return
	}
	s.finishOpOK(op, sessionID, userID, total, total)
}

func (s *Server) reportProgress(op *hostopStatus, sessionID, userID string, done, total int64) {
	op.setProgress(done, total)
	s.manager.PublishHostop(userID, session.HostopProgressMsg{
		Type: "hostopProgress", SessionID: sessionID, OpID: op.id, Done: done, Total: total,
	})
}

type copyFileBody struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

func (s *Server) copyHostFile(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	id := c.Param("id")
	ops, info, err := s.manager.HostOps(id, userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}

	var body copyFileBody
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

	op := s.startOp(userID, "copy")
	go s.runCopy(op, ops, id, userID, resolvedSrc, resolvedDst)
	c.JSON(http.StatusAccepted, gin.H{"opId": op.id})
}

// runCopy Stats src first so total is the real file size before the first
// byte moves (§5.2) — Copy itself is not chunked (it's Read-then-Write,
// §4.10), so progress here is necessarily start/complete rather than a
// live byte counter; documented as the deliberate simplification it is.
func (s *Server) runCopy(op *hostopStatus, ops *hostops.HostSession, sessionID, userID, src, dst string) {
	defer s.retireOp(op.id)
	ctx, cancel := context.WithTimeout(context.Background(), hostopLongTimeout)
	defer cancel()

	s.manager.PublishHostop(userID, session.HostopStartedMsg{
		Type: "hostopStarted", SessionID: sessionID, OpID: op.id, Kind: "copy", Path: src,
	})

	stat, err := ops.Files().Stat(ctx, src)
	if err != nil {
		s.finishOpError(op, sessionID, userID, err)
		return
	}
	s.reportProgress(op, sessionID, userID, 0, stat.Size)

	if err := ops.Files().Copy(ctx, src, dst); err != nil {
		s.finishOpError(op, sessionID, userID, err)
		return
	}
	s.finishOpOK(op, sessionID, userID, stat.Size, stat.Size)
}
