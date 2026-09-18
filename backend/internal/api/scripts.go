package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Andste82/sessile/backend/internal/agents"
	"github.com/Andste82/sessile/backend/internal/scripts"
)

// Script extensions (PROJECT_PLAN.md §4.15). Reading one's own scripts and
// settings always works; installing and running them needs the operator's
// allowAgentScripts (on by default), since a script is code run on this
// server (§11).

// SetScripts wires the script store and runner.
func (s *Server) SetScripts(store *scripts.Store, runner *scripts.Runner) {
	s.scriptStore = store
	s.scriptRunner = runner
}

func (s *Server) scriptsOK(c *gin.Context, run bool) bool {
	if s.scriptStore == nil || s.scriptRunner == nil {
		respondError(c, http.StatusServiceUnavailable, CodeUnavailable, "scripts are not available")
		return false
	}
	if run && !s.serverConfig.Get().ScriptsAllowed() {
		respondError(c, http.StatusForbidden, CodeForbidden, "scripts are turned off on this server")
		return false
	}
	return true
}

type scriptSettingJSON struct {
	scripts.Setting
	Value string `json:"value,omitempty"` // non-secret settings only
	Set   bool   `json:"set"`
	Git   string `json:"git,omitempty"` // git host a secret is taken from
}

type scriptJSON struct {
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Description string               `json:"description"`
	Functions   []scriptFunctionJSON `json:"functions"`
	Settings    []scriptSettingJSON  `json:"settings"`
	Guidance    []string             `json:"guidance"`
	Check       string               `json:"check"`
	Status      string               `json:"status"` // preparing | needs_setup | check_failed | ready
	Missing     []string             `json:"missing"`
	Venv        scripts.VenvState    `json:"venv"`
	VenvError   string               `json:"venvError,omitempty"`
	LastCheck   *scripts.CheckResult `json:"lastCheck,omitempty"`
}

type scriptFunctionJSON struct {
	Name        string          `json:"name"`
	Effect      string          `json:"effect"`
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input,omitempty"`
}

func (s *Server) scriptView(userID string, m scripts.Meta) scriptJSON {
	st, _ := s.scriptStore.GetSettings(userID, m.Name)
	v := scriptJSON{
		Name: m.Name, Version: m.Version, Description: m.Description, Check: m.Check,
		Guidance: append([]string{}, m.Guidance...), Missing: scripts.Missing(m, st),
		Functions: []scriptFunctionJSON{}, Settings: []scriptSettingJSON{},
	}
	for _, f := range m.Functions {
		v.Functions = append(v.Functions, scriptFunctionJSON{Name: f.Name, Effect: f.Effect, Description: f.Description, Input: f.Input})
	}
	for _, d := range m.Settings {
		sj := scriptSettingJSON{Setting: d, Git: st.Git[d.Name]}
		sj.Set = st.Values[d.Name] != "" || sj.Git != ""
		if d.Type != "secret" {
			sj.Value = st.Values[d.Name]
		}
		v.Settings = append(v.Settings, sj)
	}
	v.Venv, v.VenvError = s.scriptRunner.VenvStatus(m, s.scriptStore.Dir(userID, m.Name))
	if c, ok := s.scriptRunner.LastCheck(userID, m.Name); ok {
		v.LastCheck = &c
	}
	switch {
	case v.Venv == scripts.VenvPreparing:
		v.Status = "preparing"
	case len(v.Missing) > 0:
		v.Status = "needs_setup"
	case v.LastCheck != nil && !v.LastCheck.OK:
		v.Status = "check_failed"
	default:
		v.Status = "ready"
	}
	return v
}

type scriptListJSON struct {
	Allowed bool              `json:"allowed"`
	Scripts []scriptJSON      `json:"scripts"`
	Broken  map[string]string `json:"broken"`
}

func (s *Server) listScripts(c *gin.Context) {
	if !s.scriptsOK(c, false) {
		return
	}
	userID := c.MustGet(userIDKey).(string)
	list, broken := s.scriptStore.List(userID)
	out := scriptListJSON{Allowed: s.serverConfig.Get().ScriptsAllowed(), Scripts: []scriptJSON{}, Broken: broken}
	for _, m := range list {
		out.Scripts = append(out.Scripts, s.scriptView(userID, m))
	}
	c.JSON(http.StatusOK, out)
}

// installScript takes a zip as the raw body (§4.15.1). It sits outside the
// 32 KiB JSON group with a cap of its own.
func (s *Server) installScript(c *gin.Context) {
	if !s.scriptsOK(c, true) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, scripts.MaxZip+1))
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "could not read the upload")
		return
	}
	s.installScriptZip(c, data)
}

func (s *Server) installScriptZip(c *gin.Context, data []byte) {
	userID := c.MustGet(userIDKey).(string)
	m, err := s.scriptStore.Install(userID, data, c.Query("as"), c.Query("update") == "true")
	if err != nil {
		var exists *scripts.ExistsError
		if errors.As(err, &exists) {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": gin.H{
				"code": CodeScriptExists, "message": err.Error(),
				"installed": exists.Installed, "uploaded": exists.Uploaded,
			}})
			return
		}
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	s.scriptRunner.ForgetCheck(userID, m.Name)
	s.scriptRunner.Prepare(m, s.scriptStore.Dir(userID, m.Name))
	c.JSON(http.StatusCreated, s.scriptView(userID, m))
}

func (s *Server) exportScript(c *gin.Context) {
	if !s.scriptsOK(c, false) {
		return
	}
	userID := c.MustGet(userIDKey).(string)
	name := c.Param("name")
	m, err := s.scriptStore.Get(userID, name)
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.zip"`, m.Name, m.Version))
	if err := s.scriptStore.Export(userID, name, c.Writer); err != nil {
		s.log.Error("export script failed", "err", err)
	}
}

func (s *Server) removeScript(c *gin.Context) {
	if !s.scriptsOK(c, false) {
		return
	}
	userID := c.MustGet(userIDKey).(string)
	if err := s.scriptStore.Remove(userID, c.Param("name")); err != nil {
		s.respondScriptError(c, err)
		return
	}
	s.scriptRunner.ForgetCheck(userID, c.Param("name"))
	c.Status(http.StatusNoContent)
}

// scriptSettingsBody: a secret that is omitted keeps its saved value, a
// string replaces it ("" clears it), same rule as every other credential.
// Git maps a secret setting to a Git account's token by git host ("" unmaps).
type scriptSettingsBody struct {
	Values map[string]*string `json:"values"`
	Git    map[string]string  `json:"git"`
}

// merge applies the body to the saved settings, checked against meta.json.
func (b scriptSettingsBody) merge(m scripts.Meta, cur scripts.Settings, gitHosts map[string]bool) (scripts.Settings, error) {
	next := scripts.Settings{Values: map[string]string{}, Git: map[string]string{}}
	declared := map[string]scripts.Setting{}
	for _, d := range m.Settings {
		declared[d.Name] = d
	}
	for k := range b.Values {
		if _, ok := declared[k]; !ok {
			return next, fmt.Errorf("%s declares no setting %s", m.Name, k)
		}
	}
	for k, host := range b.Git {
		d, ok := declared[k]
		if !ok || d.Type != "secret" {
			return next, fmt.Errorf("only a secret setting can take a Git account's token (%s)", k)
		}
		if host != "" && !gitHosts[strings.ToLower(host)] {
			return next, fmt.Errorf("no Git account for %s", host)
		}
	}
	for _, d := range m.Settings {
		v := b.Values[d.Name]
		switch {
		case v != nil:
			next.Values[d.Name] = strings.TrimSpace(*v)
		case d.Type == "secret":
			next.Values[d.Name] = cur.Values[d.Name]
		default:
			next.Values[d.Name] = ""
		}
		if strings.ContainsAny(next.Values[d.Name], "\x00\r\n") {
			return next, fmt.Errorf("%s must be a single line", d.Label)
		}
		if d.Type == "choice" && next.Values[d.Name] != "" && !contains(d.Options, next.Values[d.Name]) {
			return next, fmt.Errorf("%s must be one of %s", d.Label, strings.Join(d.Options, ", "))
		}
		if d.Type == "bool" && next.Values[d.Name] != "" && next.Values[d.Name] != "true" && next.Values[d.Name] != "false" {
			return next, fmt.Errorf("%s must be true or false", d.Label)
		}
		if host, ok := b.Git[d.Name]; ok {
			next.Git[d.Name] = strings.ToLower(host)
		} else if cur.Git[d.Name] != "" && v == nil {
			next.Git[d.Name] = cur.Git[d.Name]
		}
		if next.Git[d.Name] == "" {
			delete(next.Git, d.Name)
		} else {
			next.Values[d.Name] = ""
		}
		if next.Values[d.Name] == "" {
			delete(next.Values, d.Name)
		}
	}
	return next, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Server) gitHosts(userID string) map[string]bool {
	out := map[string]bool{}
	if s.agents == nil {
		return out
	}
	store, err := s.agents.For(userID)
	if err != nil {
		return out
	}
	for _, g := range store.Get().Git {
		out[strings.ToLower(g.Host)] = true
	}
	return out
}

func (s *Server) putScriptSettings(c *gin.Context) {
	if !s.scriptsOK(c, false) {
		return
	}
	userID := c.MustGet(userIDKey).(string)
	var body scriptSettingsBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	m, err := s.scriptStore.Get(userID, c.Param("name"))
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	cur, err := s.scriptStore.GetSettings(userID, m.Name)
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	next, err := body.merge(m, cur, s.gitHosts(userID))
	if err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	if err := s.scriptStore.PutSettings(userID, m.Name, next); err != nil {
		s.respondScriptError(c, err)
		return
	}
	s.scriptRunner.ForgetCheck(userID, m.Name)
	c.JSON(http.StatusOK, s.scriptView(userID, m))
}

// checkScript runs the check function — with the saved settings, or with
// unsaved ones from the body (the Configure form's Test connection).
func (s *Server) checkScript(c *gin.Context) {
	if !s.scriptsOK(c, true) {
		return
	}
	userID := c.MustGet(userIDKey).(string)
	var body scriptSettingsBody
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
			return
		}
	}
	m, err := s.scriptStore.Get(userID, c.Param("name"))
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	st, err := s.scriptStore.GetSettings(userID, m.Name)
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	if body.Values != nil || body.Git != nil {
		if st, err = body.merge(m, st, s.gitHosts(userID)); err != nil {
			respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
			return
		}
	}
	c.JSON(http.StatusOK, s.scriptRunner.Check(c.Request.Context(), userID, m, s.scriptStore.Dir(userID, m.Name), st))
}

type runScriptBody struct {
	Function string          `json:"function"`
	Input    json.RawMessage `json:"input"`
}

// runScript is the Scripts page's Test run: one function, saved settings.
// Write functions run here too: the user pressed the button.
func (s *Server) runScript(c *gin.Context) {
	if !s.scriptsOK(c, true) {
		return
	}
	userID := c.MustGet(userIDKey).(string)
	var body runScriptBody
	if err := c.ShouldBindJSON(&body); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, "invalid request body")
		return
	}
	m, err := s.scriptStore.Get(userID, c.Param("name"))
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	st, err := s.scriptStore.GetSettings(userID, m.Name)
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	res, err := s.scriptRunner.Run(c.Request.Context(), userID, m, s.scriptStore.Dir(userID, m.Name), st, body.Function, body.Input)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error(), "stderr": res.Stderr})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "output": res.Output, "stderr": res.Stderr})
}

func (s *Server) rebuildScript(c *gin.Context) {
	if !s.scriptsOK(c, true) {
		return
	}
	userID := c.MustGet(userIDKey).(string)
	m, err := s.scriptStore.Get(userID, c.Param("name"))
	if err != nil {
		s.respondScriptError(c, err)
		return
	}
	if err := s.scriptRunner.Rebuild(c.Request.Context(), m, s.scriptStore.Dir(userID, m.Name)); err != nil {
		respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
		return
	}
	c.JSON(http.StatusOK, s.scriptView(userID, m))
}

func (s *Server) respondScriptError(c *gin.Context, err error) {
	if errors.Is(err, scripts.ErrNotFound) {
		respondError(c, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	s.log.Error("script request failed", "err", err)
	respondError(c, http.StatusBadRequest, CodeValidation, err.Error())
}

// gitTokenResolver lets the runner take a Git account's token for a setting
// mapped to it (§4.15.3), scoped to the script's own user.
func GitTokenResolver(reg *agents.Registry) func(userID, host string) (string, bool) {
	return func(userID, host string) (string, bool) {
		store, err := reg.For(userID)
		if err != nil {
			return "", false
		}
		for _, g := range store.Get().Git {
			if strings.EqualFold(g.Host, host) && g.Token != "" {
				return g.Token, true
			}
		}
		return "", false
	}
}
