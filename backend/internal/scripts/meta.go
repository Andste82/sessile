// Package scripts is each user's script extensions (PROJECT_PLAN.md §4.15):
// zip-installed Python scripts whose functions become the task agent's tools.
// Scripts run on the sessile server, as sessile's OS user — equal to shell
// access there, and documented as such (§11); nothing here is a sandbox.
//
// A script's code (scripts/<name>/) and its settings (settings/<name>.yml)
// are stored apart, so an exported zip never carries a token.
package scripts

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Meta is a script's meta.json (§4.15.2).
type Meta struct {
	Name           string     `json:"name"`
	Version        string     `json:"version"`
	Description    string     `json:"description"`
	Runtime        string     `json:"runtime"`
	Entry          string     `json:"entry,omitempty"` // default main.py
	Settings       []Setting  `json:"settings,omitempty"`
	Check          string     `json:"check,omitempty"`
	Guidance       []string   `json:"guidance,omitempty"`
	TimeoutSeconds int        `json:"timeoutSeconds,omitempty"`
	Functions      []Function `json:"functions"`
}

// Setting is one value a script needs to reach its service; it becomes an
// environment variable of the same name when the script runs.
type Setting struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // string | url | secret | bool | choice
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
	// Context marks a non-secret setting as context for the task agent: it is
	// written into the tool descriptions (§4.15.2).
	Context bool   `json:"context,omitempty"`
	Help    string `json:"help,omitempty"`
}

// Function is one tool.
type Function struct {
	Name        string          `json:"name"`
	Effect      string          `json:"effect"` // read | write
	Description string          `json:"description"`
	Input       json.RawMessage `json:"input,omitempty"`
}

// Effects (§4.17.3): a write waits for the user's approval.
const (
	EffectRead  = "read"
	EffectWrite = "write"
)

const (
	defaultTimeout = 60
	maxTimeout     = 300
)

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	settingRe  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	functionRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	versionRe  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
	entryRe    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]{0,127}\.py$`)
)

// ValidName reports whether s may name a script (and so a directory and a
// tool-name prefix).
func ValidName(s string) bool { return nameRe.MatchString(s) }

// reserved setting names are what the runner itself sets.
var reserved = map[string]bool{"PATH": true, "HOME": true, "LANG": true, "PYTHONPATH": true, "PYTHONHOME": true}

// ParseMeta reads and validates meta.json.
func ParseMeta(data []byte) (Meta, error) {
	var m Meta
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Meta{}, fmt.Errorf("meta.json: %w", err)
	}
	if m.Runtime == "" {
		m.Runtime = "python3"
	}
	if m.Entry == "" {
		m.Entry = "main.py"
	}
	if m.TimeoutSeconds == 0 {
		m.TimeoutSeconds = defaultTimeout
	}
	return m, m.Validate()
}

// Validate checks meta.json against §4.15.2.
func (m Meta) Validate() error {
	if !ValidName(m.Name) {
		return fmt.Errorf("meta.json: name must be lowercase letters, digits and dashes, starting with a letter (got %q)", m.Name)
	}
	if !versionRe.MatchString(m.Version) {
		return fmt.Errorf("meta.json: version must be semver like 1.2.0 (got %q)", m.Version)
	}
	if m.Runtime != "python3" {
		return fmt.Errorf("meta.json: runtime %q is not supported (python3 only)", m.Runtime)
	}
	if !entryRe.MatchString(m.Entry) || strings.Contains(m.Entry, "..") {
		return fmt.Errorf("meta.json: entry must be a .py file inside the script (got %q)", m.Entry)
	}
	if m.TimeoutSeconds < 1 || m.TimeoutSeconds > maxTimeout {
		return fmt.Errorf("meta.json: timeoutSeconds must be 1-%d", maxTimeout)
	}
	seen := map[string]bool{}
	for _, s := range m.Settings {
		if !settingRe.MatchString(s.Name) || reserved[s.Name] || strings.HasPrefix(s.Name, "SESSILE_") {
			return fmt.Errorf("meta.json: setting name %q must be UPPER_SNAKE_CASE and not a reserved variable", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("meta.json: duplicate setting %s", s.Name)
		}
		seen[s.Name] = true
		switch s.Type {
		case "string", "url", "secret", "bool":
		case "choice":
			if len(s.Options) == 0 {
				return fmt.Errorf("meta.json: setting %s is a choice without options", s.Name)
			}
		default:
			return fmt.Errorf("meta.json: setting %s has unknown type %q", s.Name, s.Type)
		}
		if s.Type == "secret" && s.Context {
			return fmt.Errorf("meta.json: setting %s is secret and can't be context: secrets never reach the agent", s.Name)
		}
	}
	if len(m.Functions) == 0 {
		return fmt.Errorf("meta.json: no functions")
	}
	fns := map[string]Function{}
	for _, f := range m.Functions {
		if !functionRe.MatchString(f.Name) {
			return fmt.Errorf("meta.json: function name %q must be lowercase snake_case", f.Name)
		}
		if _, dup := fns[f.Name]; dup {
			return fmt.Errorf("meta.json: duplicate function %s", f.Name)
		}
		fns[f.Name] = f
		if f.Effect != EffectRead && f.Effect != EffectWrite {
			return fmt.Errorf("meta.json: function %s: effect must be read or write", f.Name)
		}
		if strings.TrimSpace(f.Description) == "" {
			return fmt.Errorf("meta.json: function %s needs a description (it's what the agent reads)", f.Name)
		}
		if len(f.Input) > 0 {
			if _, err := parseSchema(f.Input); err != nil {
				return fmt.Errorf("meta.json: function %s: input schema: %w", f.Name, err)
			}
		}
	}
	if m.Check != "" {
		f, ok := fns[m.Check]
		if !ok {
			return fmt.Errorf("meta.json: check names %q, which isn't a function", m.Check)
		}
		if f.Effect != EffectRead {
			return fmt.Errorf("meta.json: check function %s must be read-only", m.Check)
		}
	}
	return nil
}

// Function returns the named function.
func (m Meta) Function(name string) (Function, bool) {
	for _, f := range m.Functions {
		if f.Name == name {
			return f, true
		}
	}
	return Function{}, false
}

// Secrets lists the names of the secret settings.
func (m Meta) Secrets() []string {
	var out []string
	for _, s := range m.Settings {
		if s.Type == "secret" {
			out = append(out, s.Name)
		}
	}
	return out
}
