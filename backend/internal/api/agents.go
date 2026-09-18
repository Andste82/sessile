package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/agents"
)

// Agent settings (PROJECT_PLAN.md §4.13, §4.16): connections, profiles, task
// defaults and Git accounts. Secrets go in and never come back out — the
// response only says whether each one is set, the same rule hosts.yml's
// credentials follow.

type connectionJSON struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Agent       agents.Agent      `json:"agent"`
	Fields      map[string]string `json:"fields"`     // non-secret fields only
	SecretsSet  map[string]bool   `json:"secretsSet"` // secret field -> set?
	Expires     *string           `json:"expires"`
	Expired     bool              `json:"expired"`
	ExpiresSoon bool              `json:"expiresSoon"`
}

type profileJSON struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Agent        agents.Agent `json:"agent"`
	ConnectionID string       `json:"connectionId"`
	Model        string       `json:"model"`
}

type taskDefaultsJSON struct {
	HostID    string `json:"hostId"`
	ProfileID string `json:"profileId"`
}

type gitJSON struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Username string `json:"username"`
	HasToken bool   `json:"hasToken"`
}

type agentSettingsJSON struct {
	Connections  []connectionJSON `json:"connections"`
	Profiles     []profileJSON    `json:"profiles"`
	TaskDefaults taskDefaultsJSON `json:"taskDefaults"`
	Git          []gitJSON        `json:"git"`
}

func toAgentSettingsJSON(s agents.Settings, now time.Time) agentSettingsJSON {
	out := agentSettingsJSON{
		Connections:  []connectionJSON{},
		Profiles:     []profileJSON{},
		Git:          []gitJSON{},
		TaskDefaults: taskDefaultsJSON{HostID: s.TaskDefaults.HostID, ProfileID: s.TaskDefaults.ProfileID},
	}
	for _, c := range s.Connections {
		k, _ := agents.KindByID(c.Kind)
		cj := connectionJSON{
			ID: c.ID, Name: c.Name, Kind: c.Kind, Agent: k.Agent,
			Fields: map[string]string{}, SecretsSet: map[string]bool{},
			Expired: c.Expired(now), ExpiresSoon: c.ExpiresSoon(now),
		}
		for _, f := range k.Fields {
			if f.Type == agents.FieldSecret {
				cj.SecretsSet[f.Name] = c.Fields[f.Name] != ""
			} else {
				cj.Fields[f.Name] = c.Fields[f.Name]
			}
		}
		if c.Expires != nil {
			e := c.Expires.UTC().Format(time.RFC3339)
			cj.Expires = &e
		}
		out.Connections = append(out.Connections, cj)
	}
	for _, p := range s.Profiles {
		out.Profiles = append(out.Profiles, profileJSON{
			ID: p.ID, Name: p.Name, Agent: p.Agent, ConnectionID: p.ConnectionID, Model: p.Model,
		})
	}
	for _, g := range s.Git {
		out.Git = append(out.Git, gitJSON{
			ID: g.ID, Host: g.Host, Name: g.Name, Email: g.Email, Username: g.Username, HasToken: g.Token != "",
		})
	}
	return out
}

// The PUT body. Secret values are pointers: nil (omitted) keeps what an
// existing item with the same id already has, a string replaces it.
type connectionBody struct {
	ID      string             `json:"id"`
	Name    string             `json:"name"`
	Kind    string             `json:"kind"`
	Fields  map[string]*string `json:"fields"`
	Expires *string            `json:"expires"`
}

type profileBody struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ConnectionID string `json:"connectionId"`
	Model        string `json:"model"`
}

type gitBody struct {
	ID       string  `json:"id"`
	Host     string  `json:"host"`
	Name     string  `json:"name"`
	Email    string  `json:"email"`
	Username string  `json:"username"`
	Token    *string `json:"token"`
}

type agentSettingsBody struct {
	Connections  []connectionBody `json:"connections"`
	Profiles     []profileBody    `json:"profiles"`
	TaskDefaults taskDefaultsJSON `json:"taskDefaults"`
	Git          []gitBody        `json:"git"`
}

// clientIDRe is what a client may choose as a new item's id: a UUID, so a
// profile in the same PUT can already reference a connection that is new in
// it. Anything else gets a server-generated id.
var clientIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func pickID(id string) string {
	if clientIDRe.MatchString(id) {
		return id
	}
	return agents.NewID()
}

// merge builds the next Settings from the body and the current settings.
func (b agentSettingsBody) merge(cur agents.Settings) (agents.Settings, error) {
	next := agents.Settings{
		TaskDefaults: agents.TaskDefaults{HostID: b.TaskDefaults.HostID, ProfileID: b.TaskDefaults.ProfileID},
	}

	for _, cb := range b.Connections {
		k, ok := agents.KindByID(cb.Kind)
		if !ok {
			return agents.Settings{}, &validationErr{"unknown connection kind " + cb.Kind}
		}
		old, existed := cur.Connection(cb.ID)
		c := agents.Connection{ID: cb.ID, Name: strings.TrimSpace(cb.Name), Kind: cb.Kind, Fields: map[string]string{}}
		if !existed {
			c.ID = pickID(cb.ID)
		}
		for name := range cb.Fields {
			if !kindHasField(k, name) {
				return agents.Settings{}, &validationErr{"unknown field " + name + " for " + k.ID}
			}
		}
		for _, f := range k.Fields {
			v := cb.Fields[f.Name]
			switch {
			case v != nil:
				c.Fields[f.Name] = strings.TrimSpace(*v)
			case f.Type == agents.FieldSecret && existed && old.Kind == cb.Kind:
				c.Fields[f.Name] = old.Fields[f.Name]
			}
			if c.Fields[f.Name] == "" {
				delete(c.Fields, f.Name)
			}
		}
		if cb.Expires != nil && *cb.Expires != "" {
			t, err := parseExpiry(*cb.Expires)
			if err != nil {
				return agents.Settings{}, &validationErr{"connection " + c.Name + ": expires must be a date"}
			}
			c.Expires = &t
		}
		next.Connections = append(next.Connections, c)
	}

	for _, pb := range b.Profiles {
		p := agents.Profile{ID: pb.ID, Name: strings.TrimSpace(pb.Name), ConnectionID: pb.ConnectionID, Model: strings.TrimSpace(pb.Model)}
		if _, existed := cur.Profile(pb.ID); !existed {
			p.ID = pickID(pb.ID)
		}
		if c, ok := next.Connection(pb.ConnectionID); ok {
			k, _ := agents.KindByID(c.Kind)
			p.Agent = k.Agent
		}
		next.Profiles = append(next.Profiles, p)
	}

	for _, gb := range b.Git {
		g := agents.GitAccount{
			ID: gb.ID, Host: strings.ToLower(strings.TrimSpace(gb.Host)), Name: strings.TrimSpace(gb.Name),
			Email: strings.TrimSpace(gb.Email), Username: strings.TrimSpace(gb.Username),
		}
		old, existed := findGit(cur, gb.ID)
		if !existed {
			g.ID = pickID(gb.ID)
		}
		switch {
		case gb.Token != nil:
			g.Token = strings.TrimSpace(*gb.Token)
		case existed:
			g.Token = old.Token
		}
		next.Git = append(next.Git, g)
	}
	return next, nil
}

// parseExpiry accepts a full RFC 3339 time or a plain date (the date input's
// value), which means the end of that day in UTC.
func parseExpiry(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, err
	}
	return d.Add(24*time.Hour - time.Second).UTC(), nil
}

func kindHasField(k agents.Kind, name string) bool {
	for _, f := range k.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

func findGit(s agents.Settings, id string) (agents.GitAccount, bool) {
	for _, g := range s.Git {
		if g.ID == id {
			return g, true
		}
	}
	return agents.GitAccount{}, false
}

type validationErr struct{ msg string }

func (e *validationErr) Error() string { return e.msg }

// agentStore resolves the caller's own agents.Store (§14.5).
func (s *Server) agentStore(c *gin.Context) (*agents.Store, bool) {
	if s.agents == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "agent settings are not available")
		return nil, false
	}
	store, err := s.agents.For(c.MustGet(userIDKey).(string))
	if err != nil {
		s.log.Error("open agent store failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to load agent settings")
		return nil, false
	}
	return store, true
}

func (s *Server) listConnectionKinds(c *gin.Context) {
	c.JSON(http.StatusOK, agents.Kinds())
}

func (s *Server) getAgentSettings(c *gin.Context) {
	store, ok := s.agentStore(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, toAgentSettingsJSON(store.Get(), time.Now()))
}

func (s *Server) putAgentSettings(c *gin.Context) {
	var body agentSettingsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	store, ok := s.agentStore(c)
	if !ok {
		return
	}
	hostStore, ok := s.hostStore(c)
	if !ok {
		return
	}
	hostExists := func(id string) bool { _, found := hostStore.Get(id); return found }

	updated, err := store.Update(func(cur *agents.Settings) error {
		next, err := body.merge(*cur)
		if err != nil {
			return err
		}
		*cur = next
		return nil
	}, hostExists)
	if err != nil {
		var ve *validationErr
		var ave *agents.ValidationError
		if errors.As(err, &ve) || errors.As(err, &ave) {
			respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}
		s.log.Error("update agent settings failed", "err", err)
		respondError(c, http.StatusInternalServerError, CodeInternal, "failed to save agent settings")
		return
	}
	c.JSON(http.StatusOK, toAgentSettingsJSON(updated, time.Now()))
}

type connectionTestBody struct {
	ID     string             `json:"id"`
	Kind   string             `json:"kind"`
	Fields map[string]*string `json:"fields"`
}

type testResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// testConnection checks a credential, saved or not yet saved. An omitted
// secret uses the saved connection's value, so the dialog can test an
// edited connection without the user retyping its key.
func (s *Server) testConnection(c *gin.Context) {
	var body connectionTestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	k, found := agents.KindByID(body.Kind)
	if !found {
		respondError(c, http.StatusBadRequest, CodeValidation, "unknown connection kind")
		return
	}
	store, ok := s.agentStore(c)
	if !ok {
		return
	}
	saved, hasSaved := store.Get().Connection(body.ID)
	fields := map[string]string{}
	for _, f := range k.Fields {
		if v := body.Fields[f.Name]; v != nil {
			fields[f.Name] = strings.TrimSpace(*v)
		} else if hasSaved && saved.Kind == k.ID {
			fields[f.Name] = saved.Fields[f.Name]
		}
	}
	detail, err := s.prober.Test(c.Request.Context(), k, fields)
	if err != nil {
		c.JSON(http.StatusOK, testResult{OK: false, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, testResult{OK: true, Detail: detail})
}

type modelsResponse struct {
	agents.ModelList
	// DefaultModel is what a task gets when the profile sets no model:
	// the connection's pinned model, or "" for the CLI's own default
	// (§4.12.7). The host's settings file joins this resolution once a task
	// form has a host to read it from (§12e M36).
	DefaultModel  string `json:"defaultModel"`
	DefaultSource string `json:"defaultSource"` // "connection" | "cli"
}

func (s *Server) listConnectionModels(c *gin.Context) {
	store, ok := s.agentStore(c)
	if !ok {
		return
	}
	conn, found := store.Get().Connection(c.Param("id"))
	if !found {
		respondError(c, http.StatusNotFound, CodeNotFound, "connection not found")
		return
	}
	list := s.prober.Models(c.Request.Context(), conn, c.Query("refresh") == "1")
	resp := modelsResponse{ModelList: list, DefaultSource: "cli"}
	if k, _ := agents.KindByID(conn.Kind); k.ModelField != "" && conn.Fields[k.ModelField] != "" {
		resp.DefaultModel = conn.Fields[k.ModelField]
		resp.DefaultSource = "connection"
	}
	c.JSON(http.StatusOK, resp)
}

type gitTestBody struct {
	ID    string  `json:"id"`
	Host  string  `json:"host"`
	Token *string `json:"token"`
}

func (s *Server) testGitAccount(c *gin.Context) {
	var body gitTestBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	store, ok := s.agentStore(c)
	if !ok {
		return
	}
	token := ""
	if body.Token != nil {
		token = *body.Token
	} else if g, found := findGit(store.Get(), body.ID); found {
		token = g.Token
	}
	detail, err := s.prober.GitTest(c.Request.Context(), strings.TrimSpace(body.Host), token)
	if err != nil {
		c.JSON(http.StatusOK, testResult{OK: false, Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, testResult{OK: true, Detail: detail})
}
