package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// These are the only calls sessile makes to a model vendor: checking that a
// credential works, and listing the models it can use (§4.13, §1 — sessile
// has no LLM client). Plain HTTP, with the default transport's
// ProxyFromEnvironment, so sessile's own proxy settings apply (§9).

// Endpoints are the vendor base URLs, overridable in tests.
type Endpoints struct {
	Anthropic string // https://api.anthropic.com
	OpenAI    string // https://api.openai.com
	Gemini    string // https://generativelanguage.googleapis.com
	// Bedrock builds the control-plane URL for a region.
	Bedrock func(region string) string
	GitHub  string // https://api.github.com
}

// DefaultEndpoints are the real vendor URLs.
func DefaultEndpoints() Endpoints {
	return Endpoints{
		Anthropic: "https://api.anthropic.com",
		OpenAI:    "https://api.openai.com",
		Gemini:    "https://generativelanguage.googleapis.com",
		Bedrock:   func(region string) string { return "https://bedrock." + region + ".amazonaws.com" },
		GitHub:    "https://api.github.com",
	}
}

// Model is one entry of a model list.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ModelList is what the model picker shows for a connection.
type ModelList struct {
	Models []Model `json:"models"`
	// Listed is true when Models came from the vendor, false when they are
	// the kind's built-in aliases.
	Listed bool `json:"listed"`
	// Error is set when the vendor call failed; Models then falls back to the
	// aliases, so the picker is never empty.
	Error string `json:"error,omitempty"`
}

// Prober performs connection tests and model listing, with a one-hour
// per-credential cache for model lists (§4.13).
type Prober struct {
	HTTP      *http.Client
	Endpoints Endpoints
	TTL       time.Duration

	mu    sync.Mutex
	cache map[string]*modelEntry
}

// modelEntry is single-flight: the first caller fetches, concurrent callers
// for the same key wait on done instead of issuing their own request.
type modelEntry struct {
	done    chan struct{}
	list    ModelList
	fetched time.Time
}

// NewProber returns a Prober against the real vendor endpoints.
func NewProber() *Prober {
	return &Prober{
		HTTP:      &http.Client{Timeout: 15 * time.Second},
		Endpoints: DefaultEndpoints(),
		TTL:       time.Hour,
		cache:     map[string]*modelEntry{},
	}
}

// Test checks a credential. For kinds sessile can't check against the
// vendor (Kind.Testable false) it only checks that required fields are set,
// and says so in the result.
func (p *Prober) Test(ctx context.Context, k Kind, fields map[string]string) (string, error) {
	for _, f := range k.Fields {
		if f.Required && strings.TrimSpace(fields[f.Name]) == "" {
			return "", fmt.Errorf("%s is required", f.Label)
		}
	}
	if !k.Testable {
		return "Saved fields look complete. This kind can't be checked from sessile; it is verified the first time a task uses it.", nil
	}
	models, err := p.fetch(ctx, k, fields)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Credential accepted. %d models available.", len(models)), nil
}

// Models returns the model list for a connection, from the cache when it is
// fresh. refresh bypasses the cache.
func (p *Prober) Models(ctx context.Context, c Connection, refresh bool) ModelList {
	k, ok := KindByID(c.Kind)
	if !ok {
		return ModelList{Models: []Model{}}
	}
	aliases := aliasModels(k)
	if !k.ListsModels {
		return ModelList{Models: aliases}
	}

	key := cacheKey(c)
	p.mu.Lock()
	if e, ok := p.cache[key]; ok && !refresh {
		select {
		case <-e.done:
			if time.Since(e.fetched) < p.TTL {
				p.mu.Unlock()
				return e.list
			}
		default:
			// In flight: wait for it rather than fetching a second time.
			p.mu.Unlock()
			select {
			case <-e.done:
				return e.list
			case <-ctx.Done():
				return ModelList{Models: aliases, Error: ctx.Err().Error()}
			}
		}
	}
	e := &modelEntry{done: make(chan struct{})}
	p.cache[key] = e
	p.mu.Unlock()

	models, err := p.fetch(ctx, k, c.Fields)
	if err != nil {
		e.list = ModelList{Models: aliases, Error: err.Error()}
	} else {
		e.list = ModelList{Models: models, Listed: true}
	}
	e.fetched = time.Now()
	close(e.done)
	if err != nil {
		// Don't keep a failure for an hour: the next open of the picker retries.
		p.mu.Lock()
		if p.cache[key] == e {
			delete(p.cache, key)
		}
		p.mu.Unlock()
	}
	return e.list
}

// cacheKey identifies a credential without keeping it in the key in the
// clear: a hash over kind and field values, so a changed key is a new entry.
func cacheKey(c Connection) string {
	names := make([]string, 0, len(c.Fields))
	for n := range c.Fields {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	h.Write([]byte(c.Kind))
	for _, n := range names {
		h.Write([]byte{0})
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(c.Fields[n]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func aliasModels(k Kind) []Model {
	out := make([]Model, 0, len(k.Aliases))
	for _, a := range k.Aliases {
		out = append(out, Model{ID: a, Name: a})
	}
	return out
}

func (p *Prober) fetch(ctx context.Context, k Kind, fields map[string]string) ([]Model, error) {
	switch k.ID {
	case "claude-api":
		return p.anthropicModels(ctx, fields["apiKey"])
	case "claude-bedrock":
		return p.bedrockModels(ctx, fields["region"], fields["token"])
	case "codex-api":
		return p.openAIModels(ctx, fields["apiKey"])
	case "gemini-api":
		return p.geminiModels(ctx, fields["apiKey"])
	}
	return nil, fmt.Errorf("%s can't be queried", k.Label)
}

func (p *Prober) getJSON(ctx context.Context, rawURL string, header http.Header, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", scrubURL(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the vendor rejected the credential (HTTP %d)", resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("the vendor answered HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unexpected response: %w", err)
	}
	return nil
}

// scrubURL drops the URL from a transport error: the Gemini key travels in a
// header, but a *url.Error would still print the request URL, and nothing
// that might carry a credential belongs in an API error or a log line.
func scrubURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (p *Prober) anthropicModels(ctx context.Context, key string) ([]Model, error) {
	var out []Model
	after := ""
	for page := 0; page < 10; page++ {
		u := p.Endpoints.Anthropic + "/v1/models?limit=1000"
		if after != "" {
			u += "&after_id=" + url.QueryEscape(after)
		}
		var resp struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		h := http.Header{"X-Api-Key": {key}, "Anthropic-Version": {"2023-06-01"}}
		if err := p.getJSON(ctx, u, h, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data {
			out = append(out, Model{ID: m.ID, Name: firstNonEmpty(m.DisplayName, m.ID)})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

func (p *Prober) bedrockModels(ctx context.Context, region, token string) ([]Model, error) {
	if !regionRe.MatchString(region) {
		return nil, fmt.Errorf("%q is not an AWS region", region)
	}
	var out []Model
	next := ""
	for page := 0; page < 10; page++ {
		u := p.Endpoints.Bedrock(region) + "/inference-profiles?maxResults=1000"
		if next != "" {
			u += "&nextToken=" + url.QueryEscape(next)
		}
		var resp struct {
			Summaries []struct {
				ID   string `json:"inferenceProfileId"`
				Name string `json:"inferenceProfileName"`
			} `json:"inferenceProfileSummaries"`
			NextToken string `json:"nextToken"`
		}
		if err := p.getJSON(ctx, u, http.Header{"Authorization": {"Bearer " + token}}, &resp); err != nil {
			return nil, err
		}
		for _, s := range resp.Summaries {
			if strings.Contains(s.ID, "anthropic") {
				out = append(out, Model{ID: s.ID, Name: firstNonEmpty(s.Name, s.ID)})
			}
		}
		if resp.NextToken == "" {
			break
		}
		next = resp.NextToken
	}
	return out, nil
}

func (p *Prober) openAIModels(ctx context.Context, key string) ([]Model, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := p.getJSON(ctx, p.Endpoints.OpenAI+"/v1/models", http.Header{"Authorization": {"Bearer " + key}}, &resp); err != nil {
		return nil, err
	}
	var out []Model
	for _, m := range resp.Data {
		// codex runs on the GPT family; embeddings, audio, image and
		// moderation models would only clutter the picker.
		if strings.HasPrefix(m.ID, "gpt-") || oSeriesRe.MatchString(m.ID) || strings.Contains(m.ID, "codex") {
			out = append(out, Model{ID: m.ID, Name: m.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (p *Prober) geminiModels(ctx context.Context, key string) ([]Model, error) {
	var out []Model
	next := ""
	for page := 0; page < 10; page++ {
		u := p.Endpoints.Gemini + "/v1beta/models?pageSize=1000"
		if next != "" {
			u += "&pageToken=" + url.QueryEscape(next)
		}
		var resp struct {
			Models []struct {
				Name        string   `json:"name"`
				DisplayName string   `json:"displayName"`
				Methods     []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := p.getJSON(ctx, u, http.Header{"X-Goog-Api-Key": {key}}, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Models {
			if !contains(m.Methods, "generateContent") {
				continue
			}
			id := strings.TrimPrefix(m.Name, "models/")
			out = append(out, Model{ID: id, Name: firstNonEmpty(m.DisplayName, id)})
		}
		if resp.NextPageToken == "" {
			break
		}
		next = resp.NextPageToken
	}
	return out, nil
}

// GitTest checks a Git account. For github.com it asks the GitHub API who
// the token belongs to; other hosts have no common API, so they report that
// the account is saved and gets checked by the first clone.
func (p *Prober) GitTest(ctx context.Context, host, token string) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", errors.New("token is required")
	}
	if !strings.EqualFold(host, "github.com") {
		return "Saved. Only github.com accounts can be checked from sessile; this one is checked by the first clone that uses it.", nil
	}
	var resp struct {
		Login string `json:"login"`
	}
	h := http.Header{"Authorization": {"Bearer " + token}, "Accept": {"application/vnd.github+json"}}
	if err := p.getJSON(ctx, p.Endpoints.GitHub+"/user", h, &resp); err != nil {
		return "", err
	}
	return "Token belongs to " + resp.Login + ".", nil
}

var (
	regionRe  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	oSeriesRe = regexp.MustCompile(`^o[0-9]`)
)

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
