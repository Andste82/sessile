package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// Host-key trust-on-first-use (PROJECT_PLAN.md §4.5.1): a host's SSH key is
// never accepted silently. This file is the API surface for it — probing a
// host's current key without connecting, pinning a key explicitly, and
// mapping sshpty's typed host-key errors to the 409 responses that let
// session creation/restart trigger the same trust flow inline.

type hostKeyErrorDetail struct {
	Code                string `json:"code"`
	Message             string `json:"message"`
	KeyType             string `json:"keyType"`
	Fingerprint         string `json:"fingerprint"`
	PreviousFingerprint string `json:"previousFingerprint,omitempty"`
}

type hostKeyErrorBody struct {
	Error hostKeyErrorDetail `json:"error"`
}

// respondHostKeyError writes the 409 host_key_unverified/host_key_changed
// response if err is one of sshpty's typed host-key errors, and reports
// whether it did — callers fall through to generic error handling
// otherwise. No session or store row exists at this point (§6): the caller
// retries the same request after the user trusts the key.
func (s *Server) respondHostKeyError(c *gin.Context, err error) bool {
	var unknown *sshpty.ErrHostKeyUnknown
	if errors.As(err, &unknown) {
		c.AbortWithStatusJSON(http.StatusConflict, hostKeyErrorBody{Error: hostKeyErrorDetail{
			Code: "host_key_unverified", Message: err.Error(),
			KeyType: unknown.KeyType, Fingerprint: unknown.Fingerprint,
		}})
		return true
	}
	var changed *sshpty.ErrHostKeyChanged
	if errors.As(err, &changed) {
		c.AbortWithStatusJSON(http.StatusConflict, hostKeyErrorBody{Error: hostKeyErrorDetail{
			Code: "host_key_changed", Message: err.Error(),
			KeyType: changed.KeyType, Fingerprint: changed.Fingerprint, PreviousFingerprint: changed.Previous,
		}})
		return true
	}
	return false
}

type hostKeyProbeResponse struct {
	KeyType             string `json:"keyType"`
	Fingerprint         string `json:"fingerprint"`
	Status              string `json:"status"` // "new" | "unchanged" | "changed"
	PreviousFingerprint string `json:"previousFingerprint,omitempty"`
}

// probeHostKey dials the host and reports the key it presents, without
// creating a session — the Hosts page's "Verify host key" action.
func (s *Server) probeHostKey(c *gin.Context) {
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	host, found := store.Get(c.Param("id"))
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}

	keyType, fingerprint, err := sshpty.ProbeHostKey(host.Address)
	if err != nil {
		respondError(c, http.StatusBadGateway, CodeInternal, err.Error())
		return
	}

	resp := hostKeyProbeResponse{KeyType: keyType, Fingerprint: fingerprint}
	switch {
	case host.TrustedHostKeyFingerprint == "":
		resp.Status = "new"
	case host.TrustedHostKeyFingerprint == fingerprint:
		resp.Status = "unchanged"
	default:
		resp.Status = "changed"
		resp.PreviousFingerprint = host.TrustedHostKeyFingerprint
	}
	c.JSON(http.StatusOK, resp)
}

type trustHostKeyBody struct {
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"keyType"`
}

// trustHostKey pins a host's key. Re-probes server-side before writing —
// TOCTOU-safe — rather than trusting whatever fingerprint the client sends,
// so a stale or forged value can never be persisted.
func (s *Server) trustHostKey(c *gin.Context) {
	var body trustHostKeyBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
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

	keyType, fingerprint, err := sshpty.ProbeHostKey(host.Address)
	if err != nil {
		respondError(c, http.StatusBadGateway, CodeInternal, err.Error())
		return
	}
	if fingerprint != body.Fingerprint {
		respondError(c, http.StatusConflict, CodeConflict,
			"the host key changed again since it was checked; verify and retry")
		return
	}

	host.TrustedHostKeyType = keyType
	host.TrustedHostKeyFingerprint = fingerprint
	updated, err := store.Update(host.ID, host)
	if err != nil {
		s.log.Error("trust host key failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to update host")
		return
	}
	c.JSON(http.StatusOK, toHostResponse(updated))
}
