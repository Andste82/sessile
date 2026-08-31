package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hosts"
)

// hostResponse is the §6 host shape: secrets masked to a present/absent
// flag, host-key fields echoed plainly (not secret — §4.5.1).
type hostResponse struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	Group                     string `json:"group"`
	Address                   string `json:"address"`
	Username                  string `json:"username"`
	AuthMethod                string `json:"authMethod"`
	HasPassword               bool   `json:"hasPassword"`
	HasPrivateKey             bool   `json:"hasPrivateKey"`
	TargetOS                  string `json:"targetOS"`
	TerminalType              string `json:"terminalType"`
	CustomCommand             string `json:"customCommand"`
	TrustedHostKeyType        string `json:"trustedHostKeyType"`
	TrustedHostKeyFingerprint string `json:"trustedHostKeyFingerprint"`
	Created                   string `json:"created"`
}

func toHostResponse(h hosts.Host) hostResponse {
	return hostResponse{
		ID: h.ID, Name: h.Name, Group: h.Group, Address: h.Address, Username: h.Username,
		AuthMethod:                string(h.AuthMethod),
		HasPassword:               h.Password != "",
		HasPrivateKey:             h.PrivateKey != "",
		TargetOS:                  string(h.TargetOS),
		TerminalType:              h.TerminalType,
		CustomCommand:             h.CustomCommand,
		TrustedHostKeyType:        h.TrustedHostKeyType,
		TrustedHostKeyFingerprint: h.TrustedHostKeyFingerprint,
		Created:                   h.Created.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// hostBody is the create/update request body. The three secret fields are
// pointers so a request can distinguish "omitted" (nil — leave unchanged on
// update) from "explicitly set to empty" (non-nil pointer to ""); Create
// treats a nil pointer as simply "no secret provided."
type hostBody struct {
	Name                 string  `json:"name"`
	Group                string  `json:"group"`
	Address              string  `json:"address"`
	Username             string  `json:"username"`
	AuthMethod           string  `json:"authMethod"`
	Password             *string `json:"password"`
	PrivateKey           *string `json:"privateKey"`
	PrivateKeyPassphrase *string `json:"privateKeyPassphrase"`
	TargetOS             string  `json:"targetOS"`
	TerminalType         string  `json:"terminalType"`
	CustomCommand        string  `json:"customCommand"`
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func validateHostBody(b hostBody) error {
	if l := len(strings.TrimSpace(b.Name)); l < 1 || l > 64 {
		return errors.New("name must be 1-64 characters")
	}
	if strings.TrimSpace(b.Address) == "" {
		return errors.New("address is required")
	}
	if strings.TrimSpace(b.Username) == "" {
		return errors.New("username is required")
	}
	switch hosts.AuthMethod(b.AuthMethod) {
	case hosts.AuthPassword, hosts.AuthPrivateKey:
	default:
		return errors.New(`authMethod must be "password" or "privateKey"`)
	}
	return nil
}

// hostStore resolves the caller's own hosts.Store — always keyed by the
// session's userID (§10), never a client-supplied id.
func (s *Server) hostStore(c *gin.Context) (*hosts.Store, bool) {
	userID := c.MustGet(userIDKey).(string)
	store, err := s.hosts.For(userID)
	if err != nil {
		s.log.Error("open hosts store failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to load hosts")
		return nil, false
	}
	return store, true
}

func (s *Server) listHosts(c *gin.Context) {
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	list := store.List()
	out := make([]hostResponse, 0, len(list))
	for _, h := range list {
		out = append(out, toHostResponse(h))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) createHost(c *gin.Context) {
	var body hostBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	if err := validateHostBody(body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}

	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	h, err := store.Create(hosts.Host{
		Name: body.Name, Group: body.Group, Address: body.Address, Username: body.Username,
		AuthMethod:           hosts.AuthMethod(body.AuthMethod),
		Password:             deref(body.Password),
		PrivateKey:           deref(body.PrivateKey),
		PrivateKeyPassphrase: deref(body.PrivateKeyPassphrase),
		TargetOS:             hosts.TargetOS(body.TargetOS),
		TerminalType:         body.TerminalType,
		CustomCommand:        body.CustomCommand,
	})
	if err != nil {
		s.log.Error("create host failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to create host")
		return
	}
	c.JSON(http.StatusCreated, toHostResponse(h))
}

func (s *Server) getHost(c *gin.Context) {
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	h, found := store.Get(c.Param("id"))
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}
	c.JSON(http.StatusOK, toHostResponse(h))
}

func (s *Server) updateHost(c *gin.Context) {
	var body hostBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	if err := validateHostBody(body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}

	store, ok := s.hostStore(c)
	if !ok {
		return
	}

	id := c.Param("id")
	existing, found := store.Get(id)
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}

	next := hosts.Host{
		Name: body.Name, Group: body.Group, Address: body.Address, Username: body.Username,
		AuthMethod:                hosts.AuthMethod(body.AuthMethod),
		TargetOS:                  hosts.TargetOS(body.TargetOS),
		TerminalType:              body.TerminalType,
		CustomCommand:             body.CustomCommand,
		TrustedHostKeyType:        existing.TrustedHostKeyType,
		TrustedHostKeyFingerprint: existing.TrustedHostKeyFingerprint,
	}

	// Omitted secret fields mean "leave unchanged." Switching auth method
	// drops the other method's stored secret rather than leaving stale
	// credentials behind that the UI no longer shows a way to edit.
	switch next.AuthMethod {
	case hosts.AuthPassword:
		next.Password = existing.Password
		if body.Password != nil {
			next.Password = *body.Password
		}
	case hosts.AuthPrivateKey:
		next.PrivateKey = existing.PrivateKey
		next.PrivateKeyPassphrase = existing.PrivateKeyPassphrase
		if body.PrivateKey != nil {
			next.PrivateKey = *body.PrivateKey
		}
		if body.PrivateKeyPassphrase != nil {
			next.PrivateKeyPassphrase = *body.PrivateKeyPassphrase
		}
	}

	updated, err := store.Update(id, next)
	if err != nil {
		if errors.Is(err, hosts.ErrNotFound) {
			respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
			return
		}
		s.log.Error("update host failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to update host")
		return
	}
	c.JSON(http.StatusOK, toHostResponse(updated))
}

func (s *Server) deleteHost(c *gin.Context) {
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	if err := store.Delete(c.Param("id")); err != nil {
		if errors.Is(err, hosts.ErrNotFound) {
			respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
			return
		}
		s.log.Error("delete host failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to delete host")
		return
	}
	c.Status(http.StatusNoContent)
}
