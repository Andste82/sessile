package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"

	"github.com/Andste82/sessile/backend/internal/hostops"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// Session-less remote directory lister (PROJECT_PLAN.md §4.10): feeds the
// start-directory chooser in NewSessionDialog before any session exists. It
// is not a command surface — one path argument, directories only, read-only,
// scoped to the caller's own host. The dial goes through sshpty.Dial, i.e.
// the pinned host-key callback: an unknown or changed key is a 409 here too,
// never a silent accept (§4.5.1).

const (
	// browseIdleTimeout is how long a cached browse connection survives
	// without a request. The frontend closes it explicitly (DELETE) when the
	// dialog goes away; this is the backstop.
	browseIdleTimeout = 60 * time.Second
	// browseMaxPerUser caps cached connections per user; the least recently
	// used one is evicted and closed.
	browseMaxPerUser = 2
	// maxBrowsePathBytes bounds a caller-supplied remote path.
	maxBrowsePathBytes = 4096

	// CodeTimeout is for a browse request that exceeded its deadline.
	CodeTimeout = "timeout"
)

type browseKey struct{ userID, hostID string }

// browseEntry is one cached connection. Fields after mu's comment are
// guarded by browseCache.mu.
type browseEntry struct {
	key browseKey

	// Set once, before ready is closed.
	client  *ssh.Client
	browser *hostops.DirBrowser
	err     error
	ready   chan struct{}

	cancelDial context.CancelFunc

	// guarded by browseCache.mu
	waiters  int // callers still waiting for the dial
	busy     int // calls in flight on browser
	lastUsed time.Time
	timer    *time.Timer
	dropped  bool
}

// browseCache holds at most browseMaxPerUser live browse connections per
// user. The zero value is not usable; use newBrowseCache.
type browseCache struct {
	mu      sync.Mutex
	entries map[browseKey]*browseEntry
	closed  bool

	idle time.Duration
	dial func(ctx context.Context, t sshpty.Target) (*ssh.Client, error)
	now  func() time.Time
}

func newBrowseCache() *browseCache {
	return &browseCache{
		entries: make(map[browseKey]*browseEntry),
		idle:    browseIdleTimeout,
		dial:    sshpty.Dial,
		now:     time.Now,
	}
}

var errBrowseClosed = errors.New("browse connection was closed; retry")

// acquire returns a ready entry for key, dialing (single-flight) if needed,
// and marks it busy. The caller must call release. ctx only bounds this
// caller's wait: the dial is cancelled only when no waiter is left.
func (bc *browseCache) acquire(ctx context.Context, key browseKey, target sshpty.Target) (*browseEntry, error) {
	e, err := bc.acquireOnce(ctx, key, target)
	if errors.Is(err, context.Canceled) && ctx.Err() == nil {
		// We joined a dial that its last other waiter had just abandoned.
		e, err = bc.acquireOnce(ctx, key, target)
	}
	return e, err
}

func (bc *browseCache) acquireOnce(ctx context.Context, key browseKey, target sshpty.Target) (*browseEntry, error) {
	bc.mu.Lock()
	if bc.closed {
		bc.mu.Unlock()
		return nil, errBrowseClosed
	}
	e := bc.entries[key]
	if e == nil {
		e = bc.startDialLocked(key, target)
	}
	e.waiters++
	bc.mu.Unlock()

	select {
	case <-e.ready:
	case <-ctx.Done():
		bc.mu.Lock()
		e.waiters--
		if e.waiters == 0 && e.client == nil && e.err == nil {
			// Nobody is waiting on this dial any more.
			e.cancelDial()
		}
		bc.mu.Unlock()
		return nil, ctx.Err()
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	e.waiters--
	if e.err != nil {
		return nil, e.err
	}
	if e.dropped {
		return nil, errBrowseClosed
	}
	e.busy++
	e.lastUsed = bc.now()
	return e, nil
}

// startDialLocked registers a new entry (evicting the user's LRU one when at
// the cap) and starts its dial. bc.mu must be held.
func (bc *browseCache) startDialLocked(key browseKey, target sshpty.Target) *browseEntry {
	var mine []*browseEntry
	for k, other := range bc.entries {
		if k.userID == key.userID {
			mine = append(mine, other)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].lastUsed.Before(mine[j].lastUsed) })
	for len(mine) >= browseMaxPerUser {
		bc.removeLocked(mine[0])
		mine = mine[1:]
	}

	dctx, cancel := context.WithCancel(context.Background())
	e := &browseEntry{key: key, ready: make(chan struct{}), cancelDial: cancel, lastUsed: bc.now()}
	bc.entries[key] = e
	go bc.runDial(dctx, e, target)
	return e
}

func (bc *browseCache) runDial(ctx context.Context, e *browseEntry, target sshpty.Target) {
	defer e.cancelDial()
	client, err := bc.dial(ctx, target)
	var browser *hostops.DirBrowser
	if err == nil {
		browser, err = hostops.NewDirBrowserContext(ctx, client)
		if err != nil {
			_ = client.Close()
			client = nil
		}
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	if err != nil {
		e.err = err
		// A failed dial creates no entry.
		if bc.entries[e.key] == e {
			delete(bc.entries, e.key)
		}
		close(e.ready)
		return
	}
	e.client, e.browser = client, browser
	if e.dropped || bc.closed {
		// Dropped while dialing: the connection is not wanted.
		e.err = errBrowseClosed
		_ = browser.Close()
		_ = client.Close()
		close(e.ready)
		return
	}
	e.timer = time.AfterFunc(bc.idle, func() { bc.expire(e) })
	close(e.ready)
}

// release ends a busy period; the idle clock restarts from here.
func (bc *browseCache) release(e *browseEntry) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	e.busy--
	e.lastUsed = bc.now()
}

// expire is the idle timer. A connection that is busy or was used recently
// is given the remainder of its idle window instead of being closed.
func (bc *browseCache) expire(e *browseEntry) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if e.dropped {
		return
	}
	if e.busy > 0 {
		e.timer.Reset(bc.idle)
		return
	}
	if rest := bc.idle - bc.now().Sub(e.lastUsed); rest > 0 {
		e.timer.Reset(rest)
		return
	}
	bc.removeLocked(e)
}

// removeLocked unregisters e and closes its connection (or cancels its dial).
func (bc *browseCache) removeLocked(e *browseEntry) {
	if e.dropped {
		return
	}
	e.dropped = true
	if bc.entries[e.key] == e {
		delete(bc.entries, e.key)
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	if e.browser == nil {
		e.cancelDial() // still dialing; runDial discards the result
		return
	}
	_ = e.browser.Close()
	_ = e.client.Close()
}

// drop closes the cached connection for (userID, hostID), if any.
func (bc *browseCache) drop(userID, hostID string) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if e := bc.entries[browseKey{userID, hostID}]; e != nil {
		bc.removeLocked(e)
	}
}

// dropEntry closes e specifically (a dead-connection error): a newer entry
// for the same key must survive.
func (bc *browseCache) dropEntry(e *browseEntry) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.removeLocked(e)
}

// isDropped reports whether e has been unregistered and closed.
func (bc *browseCache) isDropped(e *browseEntry) bool {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return e.dropped
}

// closeAll shuts the cache down for good; later acquires fail.
func (bc *browseCache) closeAll() {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.closed = true
	for _, e := range bc.entries {
		bc.removeLocked(e)
	}
}

// len reports the number of registered entries (tests).
func (bc *browseCache) len() int {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return len(bc.entries)
}

// Close releases resources held by the server beyond the HTTP listener: the
// cached directory-browse connections. Safe to call more than once.
func (s *Server) Close() {
	if s.browse != nil {
		s.browse.closeAll()
	}
}

// dropHostBrowse closes the caller's cached browse connection for a host.
// Called whenever the host's address, credentials or pinned key may have
// changed, or the host is gone.
func (s *Server) dropHostBrowse(userID, hostID string) {
	if s.browse != nil {
		s.browse.drop(userID, hostID)
	}
}

type directoriesResponse struct {
	Path        string   `json:"path"`
	Parent      *string  `json:"parent"`
	Directories []string `json:"directories"`
}

// validateBrowsePath checks a caller-supplied remote path. Remote paths are
// exempt from the workspace check by design (CLAUDE.md); this only bounds
// the input.
func validateBrowsePath(p string) error {
	if p == "" || p == "." {
		return nil
	}
	if len(p) > maxBrowsePathBytes {
		return errors.New("path is too long")
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("path must not contain NUL")
	}
	if !strings.HasPrefix(p, "/") {
		return errors.New("path must be absolute")
	}
	return nil
}

func isWindowsHost(h hosts.Host) bool {
	return h.TargetOS == hosts.TargetOS("windows") || h.TerminalType == "cmd" || h.TerminalType == "powershell"
}

// listHostDirectories handles GET /api/hosts/:id/directories?path=.
func (s *Server) listHostDirectories(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	host, found := store.Get(c.Param("id"))
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}
	p := c.Query("path")
	if err := validateBrowsePath(p); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	if isWindowsHost(host) {
		respondError(c, http.StatusBadRequest, CodeValidation, "directory browsing is not supported for Windows targets")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), hostopsTimeout)
	defer cancel()

	e, err := s.browse.acquire(ctx, browseKey{userID, host.ID}, host.SSHTarget())
	if err != nil {
		s.respondBrowseError(c, ctx, err)
		return
	}
	defer s.browse.release(e)

	// SFTP calls ignore ctx: run in a goroutine and, on ctx.Done, close the
	// connection so the call (and the goroutine) unblocks.
	type result struct {
		res hostops.BrowseResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := e.browser.Browse(p)
		done <- result{res, err}
	}()

	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		s.browse.dropEntry(e)
		s.respondBrowseError(c, ctx, ctx.Err())
		return
	}
	if r.err != nil {
		if hostops.IsConnectionError(r.err) {
			s.browse.dropEntry(e)
		}
		if s.browse.isDropped(e) {
			// Evicted or dropped under an in-flight call: transient, retry.
			s.respondBrowseError(c, ctx, errBrowseClosed)
			return
		}
		s.respondBrowseError(c, ctx, r.err)
		return
	}

	out := directoriesResponse{Path: r.res.Path, Directories: r.res.Directories}
	if r.res.Parent != "" {
		parent := r.res.Parent
		out.Parent = &parent
	}
	c.JSON(http.StatusOK, out)
}

// respondBrowseError maps browse failures. Host-key errors keep the exact
// 409 body of session creation; everything else dial-related reuses the
// session-create mapping.
func (s *Server) respondBrowseError(c *gin.Context, ctx context.Context, err error) {
	if s.respondHostKeyError(c, err) {
		return
	}
	var nf *hostops.BrowseNotFoundError
	switch {
	case errors.As(err, &nf):
		respondError(c, http.StatusBadRequest, CodeValidation, nf.Error())
	case errors.Is(err, hostops.ErrBrowseUnavailable):
		respondError(c, http.StatusBadRequest, CodeValidation,
			"cannot browse directories: "+hostops.ErrBrowseUnavailable.Error())
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		respondError(c, http.StatusGatewayTimeout, CodeTimeout, "the host did not answer in time")
	case errors.Is(err, context.Canceled):
		// Client went away; nobody reads this, but keep the status honest.
		respondError(c, http.StatusRequestTimeout, CodeTimeout, "request cancelled")
	case errors.Is(err, errBrowseClosed):
		respondError(c, http.StatusConflict, CodeConflict, err.Error())
	default:
		s.respondSessionError(c, err)
	}
}

// closeHostBrowse handles DELETE /api/hosts/:id/directories: idempotent, 204
// whether or not a connection was cached; 404 only for an unknown host.
func (s *Server) closeHostBrowse(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	host, found := store.Get(c.Param("id"))
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}
	s.dropHostBrowse(userID, host.ID)
	c.Status(http.StatusNoContent)
}
