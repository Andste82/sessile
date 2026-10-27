package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/envfile"
)

// defaultEnvJSON is one default variable on the wire. It is the session env
// shape without "inherited" (a default is not inherited from anything), and
// like it never carries the value of a secret.
type defaultEnvJSON struct {
	Name   string  `json:"name"`
	Value  *string `json:"value,omitempty"`
	Secret bool    `json:"secret"`
	IsSet  bool    `json:"isSet"`
}

type envDefaultsResponse struct {
	Env []defaultEnvJSON `json:"env"`
}

type envDefaultsBody struct {
	// Env is a pointer so a body without the field is a 400 rather than a
	// silent "delete every default".
	Env *[]envEntryBody `json:"env"`
}

func toEnvDefaultsResponse(entries []envfile.Entry) envDefaultsResponse {
	out := envDefaultsResponse{Env: make([]defaultEnvJSON, 0, len(entries))}
	for _, e := range entries {
		j := defaultEnvJSON{Name: e.Name, Secret: e.Secret, IsSet: true}
		if !e.Secret {
			v := e.Value
			j.Value = &v
		}
		out.Env = append(out.Env, j)
	}
	return out
}

// getEnvDefaults handles GET /api/env-defaults: the caller's own default
// environment variables (users/<id>/env.yml). Secret values are never sent.
func (s *Server) getEnvDefaults(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	store, err := s.hosts.EnvFor(userID)
	if err != nil {
		s.envStoreFailed(c, err)
		return
	}
	entries, err := store.Load()
	if err != nil {
		s.envStoreFailed(c, err)
		return
	}
	c.JSON(http.StatusOK, toEnvDefaultsResponse(entries))
}

// putEnvDefaults handles PUT /api/env-defaults: full replacement of the
// caller's defaults. A secret entry sent without a value keeps its stored
// value; names missing from the body are deleted. Defaults reach sessions at
// create time only - existing sessions are untouched.
func (s *Server) putEnvDefaults(c *gin.Context) {
	userID := c.MustGet(userIDKey).(string)
	var body envDefaultsBody
	if err := c.ShouldBindJSON(&body); err != nil || body.Env == nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	store, err := s.hosts.EnvFor(userID)
	if err != nil {
		s.envStoreFailed(c, err)
		return
	}
	patch := patchEntries(*body.Env)
	next, err := store.Update(func(existing []envfile.Entry) ([]envfile.Entry, error) {
		return envfile.ApplyPatch(existing, patch)
	})
	if err != nil {
		if errors.Is(err, envfile.ErrInvalid) {
			respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}
		s.log.Error("save env defaults failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to save environment defaults")
		return
	}
	c.JSON(http.StatusOK, toEnvDefaultsResponse(next))
}

// envStoreFailed answers a failed open/load of the caller's env.yml. A file
// that fails validation (hand-edited) is named as the cause; the message
// carries entry names only, never values.
func (s *Server) envStoreFailed(c *gin.Context, err error) {
	s.log.Error("load env defaults failed", "err", err)
	if errors.Is(err, envfile.ErrInvalid) {
		respondError(c, http.StatusInternalServerError, CodeInternal,
			"your env.yml is invalid, fix or remove it: "+err.Error())
		return
	}
	respondError(c, http.StatusInternalServerError, CodeInternal, "failed to load environment defaults")
}
