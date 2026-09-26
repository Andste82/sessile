package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/sshpty"
)

// "Import from host" (§4.16): read the git identity a host already has, to
// pre-fill the Git account form. The token stays on the server under a
// short-lived import id the form saves with; the API never returns it.

const gitImportTTL = 10 * time.Minute

type gitImport struct {
	userID  string
	token   string
	expires time.Time
}

// gitImports holds imported tokens until the form saves them.
type gitImports struct {
	mu sync.Mutex
	m  map[string]gitImport
}

func (s *Server) putGitImport(userID, token string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	s.imports.mu.Lock()
	defer s.imports.mu.Unlock()
	if s.imports.m == nil {
		s.imports.m = map[string]gitImport{}
	}
	now := time.Now()
	for k, v := range s.imports.m {
		if now.After(v.expires) {
			delete(s.imports.m, k)
		}
	}
	s.imports.m[id] = gitImport{userID: userID, token: token, expires: now.Add(gitImportTTL)}
	return id
}

// takeGitImport returns and forgets an import, scoped to its user.
func (s *Server) takeGitImport(userID, id string) (string, bool) {
	s.imports.mu.Lock()
	defer s.imports.mu.Unlock()
	imp, ok := s.imports.m[id]
	if !ok || imp.userID != userID || time.Now().After(imp.expires) {
		return "", false
	}
	delete(s.imports.m, id)
	return imp.token, true
}

type gitImportBody struct {
	HostID  string `json:"hostId"`
	GitHost string `json:"gitHost"`
}

type gitImportResponse struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Username      string `json:"username"`
	HasToken      bool   `json:"hasToken"`
	TokenImportID string `json:"tokenImportId,omitempty"`
}

func (s *Server) importGitIdentity(c *gin.Context) {
	var body gitImportBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	store, ok := s.hostStore(c)
	if !ok {
		return
	}
	host, found := store.Get(body.HostID)
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, hosts.ErrNotFound.Error())
		return
	}
	id, err := sshpty.ReadGitIdentity(host.SSHTarget(), strings.ToLower(strings.TrimSpace(body.GitHost)))
	if err != nil {
		if s.respondHostKeyError(c, err) {
			return
		}
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	resp := gitImportResponse{Name: id.Name, Email: id.Email, Username: id.Username}
	if id.Token != "" {
		resp.HasToken = true
		resp.TokenImportID = s.putGitImport(c.MustGet(userIDKey).(string), id.Token)
	}
	c.JSON(http.StatusOK, resp)
}
