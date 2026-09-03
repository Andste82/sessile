package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// Passwordless-login setup (PROJECT_PLAN.md §4.5.2, decision #11): the
// supplied password is used for exactly one SSH connection, in memory only,
// and is never written to hosts.yml or anywhere else — enforced here
// server-side regardless of what the client sends alongside it.

type exchangeKeysBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type exchangeKeysResponse struct {
	Success     bool   `json:"success"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

func (s *Server) exchangeHostKeys(c *gin.Context) {
	var body exchangeKeysBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	if strings.TrimSpace(body.Username) == "" || body.Password == "" {
		respondError(c, http.StatusBadRequest, CodeValidation, "username and password are required")
		return
	}

	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	host, found := store.Get(c.Param("id"))
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}

	target := host.SSHTarget()
	privateKeyPEM, _, keyType, fingerprint, _, err := sshpty.ExchangeKeys(target, body.Username, body.Password)
	if err != nil {
		if s.respondHostKeyError(c, err) {
			return
		}
		respondError(c, http.StatusBadRequest, CodeExchangeFailed, err.Error())
		return
	}

	// The key was installed for body.Username on the remote host — not
	// necessarily host.Username, which the exchange dialog prefills but lets
	// the caller override. Persisting it here keeps the stored host pointed
	// at the account that can actually authenticate with the new key; leaving
	// the old value behind would silently break every later connection.
	host.Username = strings.TrimSpace(body.Username)
	host.AuthMethod = hosts.AuthPrivateKey
	host.PrivateKey = privateKeyPEM
	host.PrivateKeyPassphrase = ""
	// The decision #11 guarantee: password auth is retired, not kept as a
	// fallback — nothing about this endpoint ever writes body.Password.
	host.Password = ""
	if _, err := store.Update(host.ID, host); err != nil {
		s.log.Error("save exchanged key failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to save host")
		return
	}

	c.JSON(http.StatusOK, exchangeKeysResponse{
		Success:     true,
		KeyType:     keyType,
		Fingerprint: fingerprint,
	})
}
