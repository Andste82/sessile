package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/envfile"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// The §6 session shape is session.JSON, built by session.ToJSON. It lives in
// the session package because the event channel serialises it too and ws cannot
// import api — see the type's own comment.
//
// createSessionBody discriminates on target: "local" needs directory+shell,
// "ssh" needs hostId. Empty target is treated as "local" for compatibility
// with the pre-M17 body shape.
type createSessionBody struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	// Group is optional and free text; "" files the session under no group,
	// which is what every session created before §4.11 existed carries.
	Group     string `json:"group"`
	Directory string `json:"directory"`
	Shell     string `json:"shell"`
	HostID    string `json:"hostId"`

	// Env holds the entries the user added or overrode in the dialog; every
	// one must carry a value. Defaults the user left alone travel as nothing
	// - the server merges them in (envfile.Inherit), so the secret defaults
	// never have to round-trip through the browser. RemoveDefaults names the
	// defaults to drop for this session.
	Env            []envEntryBody `json:"env"`
	RemoveDefaults []string       `json:"removeDefaults"`
	LoadDotenv     bool           `json:"loadDotenv"`
}

// envEntryBody is one environment entry in a request. Value is a pointer so
// "omitted" (keep the stored value of a secret) differs from "".
type envEntryBody struct {
	Name   string  `json:"name"`
	Value  *string `json:"value"`
	Secret bool    `json:"secret"`
}

// patchEntries converts request entries for envfile.ApplyPatch.
func patchEntries(in []envEntryBody) []envfile.PatchEntry {
	out := make([]envfile.PatchEntry, 0, len(in))
	for _, e := range in {
		out = append(out, envfile.PatchEntry{Name: e.Name, Value: e.Value, Secret: e.Secret})
	}
	return out
}

// sessionWithWarnings is the create (201) and restart (200) response: the
// session plus whatever the start had to say about a .env it could not use.
// Warnings carry names and line numbers only, and are never stored - list,
// get, update and event shapes do not have the field.
type sessionWithWarnings struct {
	session.JSON
	Warnings []string `json:"warnings"`
}

func newSessionWithWarnings(info session.Info, warnings []string) sessionWithWarnings {
	if warnings == nil {
		warnings = []string{}
	}
	return sessionWithWarnings{JSON: session.ToJSON(info), Warnings: warnings}
}

// errEnvUnsupportedHost is the message for environment variables sent for a
// host that cannot take them.
const errEnvUnsupportedHost = "host does not support environment variables"

// createEnvOptions turns the environment part of a create body into
// session.CreateOptions, writing the 400 itself on failure. supportsEnv is
// false only for an SSH host that cannot take variables: explicit entries or
// loadDotenv are then refused, and the user's defaults are not inherited
// (so a Windows host does not fail just because the user keeps defaults).
func (s *Server) createEnvOptions(c *gin.Context, userID string, body createSessionBody, supportsEnv bool) (session.CreateOptions, bool) {
	if !supportsEnv {
		if len(body.Env) > 0 || body.LoadDotenv {
			respondError(c, http.StatusBadRequest, CodeValidation, errEnvUnsupportedHost)
			return session.CreateOptions{}, false
		}
		return session.CreateOptions{}, true
	}

	own := make([]envfile.Entry, 0, len(body.Env))
	for _, e := range body.Env {
		if e.Value == nil {
			respondError(c, http.StatusBadRequest, CodeValidation,
				"environment variable "+strconv.Quote(e.Name)+" needs a value")
			return session.CreateOptions{}, false
		}
		own = append(own, envfile.Entry{Name: e.Name, Value: *e.Value, Secret: e.Secret})
	}
	if err := envfile.Validate(own); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return session.CreateOptions{}, false
	}

	// The defaults are always the authenticated caller's own.
	var defaults []envfile.Entry
	if s.hosts != nil {
		store, err := s.hosts.EnvFor(userID)
		if err == nil {
			defaults, err = store.Load()
		}
		if err != nil {
			s.envStoreFailed(c, err)
			return session.CreateOptions{}, false
		}
	}
	return session.CreateOptions{
		Env:    envfile.Inherit(defaults, own, body.RemoveDefaults),
		DotEnv: body.LoadDotenv,
	}, true
}

// maxRemotePathLen bounds a remote path an API caller supplies.
const maxRemotePathLen = 4096

// isStartDirError reports whether err is one of sshpty's typed start-directory
// failures.
func isStartDirError(err error) bool {
	var nf *sshpty.ErrDirNotFound
	var un *sshpty.ErrSFTPUnavailable
	var ds *sshpty.ErrDirUnsupported
	return errors.As(err, &nf) || errors.As(err, &un) || errors.As(err, &ds)
}

// validateSSHStartDir checks the shape of an already-trimmed SSH start
// directory against host and returns a user-facing message, or "" if it is
// acceptable (including the empty "no start directory" case).
func validateSSHStartDir(host hosts.Host, dir string) string {
	if dir == "" {
		return ""
	}
	if len(dir) > maxRemotePathLen || strings.ContainsAny(dir, "\x00\n\r") || !strings.HasPrefix(dir, "/") {
		return "start directory must be an absolute path"
	}
	if !host.SupportsEnv() {
		return "start directory is not supported for Windows targets"
	}
	return ""
}

func (s *Server) listSessions(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	infos, err := s.manager.List(userID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, CodeInternal, "list sessions failed")
		return
	}
	out := make([]session.JSON, 0, len(infos))
	for _, i := range infos {
		out = append(out, session.ToJSON(i))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) createSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	var body createSessionBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}

	if body.Target == "ssh" {
		s.createSSHSession(c, userID, body)
		return
	}
	s.createLocalSession(c, userID, body)
}

func (s *Server) createLocalSession(c *gin.Context, userID string, body createSessionBody) {
	if !s.serverConfig.Get().AllowLocalHost {
		respondError(c, http.StatusForbidden, CodeForbidden, "local-host sessions are disabled")
		return
	}
	opts, ok := s.createEnvOptions(c, userID, body, true)
	if !ok {
		return
	}
	info, warnings, err := s.manager.CreateLocal(userID, body.Name, strings.TrimSpace(body.Group), body.Directory, body.Shell, opts)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newSessionWithWarnings(info, warnings))
}

func (s *Server) createSSHSession(c *gin.Context, userID string, body createSessionBody) {
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	host, found := store.Get(body.HostID)
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}

	// The start directory is a path on the user's own remote host, so it is
	// deliberately NOT passed through resolveDir (CLAUDE.md: remote paths are
	// exempt from the local workspace check). Only its shape is validated
	// here; existence is checked over SFTP before the shell starts.
	dir := strings.TrimSpace(body.Directory)
	if msg := validateSSHStartDir(host, dir); msg != "" {
		respondError(c, http.StatusBadRequest, CodeValidation, msg)
		return
	}

	opts, ok := s.createEnvOptions(c, userID, body, host.SupportsEnv())
	if !ok {
		return
	}

	info, warnings, err := s.manager.CreateSSH(userID, body.Name, strings.TrimSpace(body.Group), host.ID, host.Name, host.SSHTarget(), dir, opts)
	if err != nil {
		if s.respondHostKeyError(c, err) {
			return
		}
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusCreated, newSessionWithWarnings(info, warnings))
}

func (s *Server) getSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	info, err := s.manager.Get(c.Param("id"), userID)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

func (s *Server) deleteSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	if err := s.manager.Delete(c.Param("id"), userID); err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// updateBody carries the two metadata fields a session can be edited after
// creation. Both are pointers so an omitted field means "leave unchanged" and
// a present one always applies — the same distinction the host update body
// draws for its secrets (§6). Without it there would be no way to clear a
// group, since "" is the value that does it.
//
// Env is the FULL replacement list (nil = unchanged, [] = remove everything);
// a secret entry without a value keeps its stored value, which is the only
// way a secret survives an edit, since the API never returns it. Env changes
// are refused unless the session is stopped.
type updateBody struct {
	Name       *string         `json:"name"`
	Group      *string         `json:"group"`
	Env        *[]envEntryBody `json:"env"`
	LoadDotenv *bool           `json:"loadDotenv"`
}

func (s *Server) updateSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	var body updateBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	if body.Group != nil {
		trimmed := strings.TrimSpace(*body.Group)
		body.Group = &trimmed
	}
	var envUpdate *session.EnvUpdate
	if body.Env != nil || body.LoadDotenv != nil {
		envUpdate = &session.EnvUpdate{DotEnv: body.LoadDotenv}
		if body.Env != nil {
			entries := patchEntries(*body.Env)
			envUpdate.Entries = &entries
		}
	}
	info, err := s.manager.Update(c.Param("id"), userID, body.Name, body.Group, envUpdate)
	if err != nil {
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, session.ToJSON(info))
}

// restartSession gives a stopped session a new shell under the same id, with
// its scrollback and command history restored (§8).
func (s *Server) restartSession(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	id := c.Param("id")

	// Mirrors createLocalSession's gate: a local session created while
	// allowLocalHost was on must not become restartable again once an admin
	// turns it off. Restart branches on the session's own stored target type
	// (session/manager.go), never the request, so the gate has to be checked
	// here rather than inferred from the body. A lookup failure (unknown
	// session, wrong owner) falls through to Restart below, which reports the
	// same error it always would.
	if info, err := s.manager.Get(id, userID); err == nil &&
		info.TargetType == session.TargetLocal && !s.serverConfig.Get().AllowLocalHost {
		respondError(c, http.StatusForbidden, CodeForbidden, "local-host sessions are disabled")
		return
	}

	info, warnings, err := s.manager.Restart(id, userID)
	if err != nil {
		if s.respondHostKeyError(c, err) {
			return
		}
		s.respondSessionError(c, err)
		return
	}
	c.JSON(http.StatusOK, newSessionWithWarnings(info, warnings))
}

// respondSessionError maps domain errors to the unified error envelope.
func (s *Server) respondSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, session.ErrHostNotFound):
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, session.ErrAlreadyRunning):
		respondError(c, http.StatusConflict, CodeAlreadyRunning, err.Error())
	case errors.Is(err, session.ErrStopped),
		errors.Is(err, session.ErrRestarting),
		errors.Is(err, session.ErrEnvRunning):
		respondError(c, http.StatusConflict, CodeConflict, err.Error())
	case errors.Is(err, session.ErrInvalidGroup),
		errors.Is(err, session.ErrInvalidName),
		errors.Is(err, session.ErrInvalidShell):
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	case errors.Is(err, session.ErrInvalidEnv),
		errors.Is(err, envfile.ErrInvalid),
		errors.Is(err, session.ErrEnvUnsupported),
		errors.Is(err, sshpty.ErrEnvUnsupported),
		errors.Is(err, sshpty.ErrEnvTooLarge):
		// Messages name variables and .env line numbers, never values.
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	case errors.Is(err, session.ErrShuttingDown):
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, err.Error())
	case isStartDirError(err):
		// A missing/unverifiable SSH start directory fails visibly (create
		// and restart alike); there is no fallback to the home directory.
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	default:
		// resolveDir, sshpty dial failures and other validation-style failures
		// surface here as 400; treat unknown errors as validation to avoid
		// leaking internals, but log for diagnosis.
		s.log.Warn("session request failed", "err", err)
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
	}
}
