// Package tasks sets sessions up for a piece of work (PROJECT_PLAN.md
// §4.12): a task folder on the target, the main repo cloned into it, and the
// user's coding agent started in it.
//
// Nothing here runs a caller-supplied command. A task is a typed Spec; the
// bootstrap is rendered from fixed templates in templates/, every value in
// it validated and shell-quoted, and every agent argv comes from the
// built-in registry (§14.8).
package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Andste82/sessile/backend/internal/agents"
)

// Kinds of task (§4.18): an ordinary task, or the user's one orchestrator.
const (
	KindTask         = "task"
	KindOrchestrator = "orchestrator"
)

// Spec is what the task form sends (§4.12.1).
type Spec struct {
	Name string `json:"name"`
	// Kind is "task" (the default) or "orchestrator" (§4.18).
	Kind string `json:"kind,omitempty"`
	// Epic groups tasks that belong together; it is the session's group
	// (§4.18.3), so the sidebar and dashboard fold by it already.
	Epic string `json:"epic,omitempty"`
	// HostID names one of the user's hosts; Target "local" means the server
	// itself instead. Exactly one of the two is set.
	HostID       string        `json:"hostId,omitempty"`
	Target       string        `json:"target,omitempty"`
	Repo         *Repo         `json:"repo,omitempty"`
	Devcontainer *Devcontainer `json:"devcontainer,omitempty"`
	Agent        AgentSpec     `json:"agent"`
	// Request is the optional first message, written to PROMPT.md.
	Request string `json:"request,omitempty"`
}

// Repo is the main repository of a task.
type Repo struct {
	URL string `json:"url"`
	Ref string `json:"ref,omitempty"`
}

// Devcontainer asks for the task to run in the main repo's devcontainer
// (§4.12.3).
type Devcontainer struct {
	Mode         string `json:"mode"` // auto | repo | generic
	DockerSocket bool   `json:"dockerSocket"`
}

// AgentSpec picks the agent profile, and optionally a model and mode.
type AgentSpec struct {
	ProfileID string `json:"profileId"`
	Model     string `json:"model,omitempty"`
	Mode      string `json:"mode,omitempty"` // plan (default) | normal | auto
}

// Agent modes (§4.12.4b). Auto is the default: the agent plans first because
// its instructions tell it to, not because a permission prompt stops it —
// approving every command turned out to be the thing that made tasks
// tiresome. Plan is the CLI's own plan mode, for work the user wants to
// approve step by step; normal is the CLI's default in between.
const (
	ModePlan   = "plan"
	ModeNormal = "normal"
	ModeAuto   = "auto"
)

// Task states (§4.18.2): what a task's agent says it is doing. "" until it
// says anything.
const (
	StateWorking = "working"
	StateBlocked = "blocked"
	StateDone    = "done"
)

// ValidState reports whether a state is one an agent may set.
func ValidState(s string) bool {
	return s == StateWorking || s == StateBlocked || s == StateDone
}

// maxRequest bounds the first message (§4.12.1).
const maxRequest = 64 << 10

var (
	// scpRepoRe is git's scp-like form, user@host:path.
	scpRepoRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[A-Za-z0-9._~/-]+$`)
	// refRe is a conservative subset of git check-ref-format: a branch, tag
	// or commit name, never an option.
	refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

// ValidationError is a user-facing spec error.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &ValidationError{msg: fmt.Sprintf(format, args...)}
}

// Normalize trims the spec's free-text fields and fills defaults.
func (s *Spec) Normalize() {
	s.Name = strings.TrimSpace(s.Name)
	s.HostID = strings.TrimSpace(s.HostID)
	s.Target = strings.TrimSpace(s.Target)
	if s.Repo != nil {
		s.Repo.URL = strings.TrimSpace(s.Repo.URL)
		s.Repo.Ref = strings.TrimSpace(s.Repo.Ref)
		if s.Repo.URL == "" {
			s.Repo = nil
		}
	}
	if s.Devcontainer != nil && s.Devcontainer.Mode == "" {
		s.Devcontainer.Mode = "auto"
	}
	if s.Kind == "" {
		s.Kind = KindTask
	}
	s.Epic = strings.TrimSpace(s.Epic)
	s.Agent.Model = strings.TrimSpace(s.Agent.Model)
	if s.Agent.Mode == "" {
		s.Agent.Mode = ModeAuto
	}
	s.Request = strings.TrimSpace(s.Request)
}

// Validate checks everything that doesn't need the user's stores.
func (s Spec) Validate() error {
	if l := len(s.Name); l < 1 || l > 64 {
		return invalid("name must be 1-64 characters")
	}
	switch {
	case s.Target == "local" && s.HostID == "":
	case s.Target == "" && s.HostID != "":
	default:
		return invalid(`pick a host, or target "local"`)
	}
	if len(s.Epic) > 64 {
		return invalid("an epic name is at most 64 characters")
	}
	switch s.Kind {
	case KindTask, "": // "" is a task; Normalize fills it in
	case KindOrchestrator:
		// The orchestrator is sessile's own session on the server (§4.18).
		if s.Target != "local" || s.Repo != nil || s.Devcontainer != nil {
			return invalid("the orchestrator runs on the server, without a repo")
		}
	default:
		return invalid("unknown task kind %q", s.Kind)
	}
	if s.Repo != nil {
		if err := validRepoURL(s.Repo.URL); err != nil {
			return err
		}
		if s.Repo.Ref != "" && !validRef(s.Repo.Ref) {
			return invalid("%q is not a branch, tag or commit name", s.Repo.Ref)
		}
	}
	if s.Devcontainer != nil {
		if s.Repo == nil {
			return invalid("a devcontainer needs the main repo: its config lives there")
		}
		switch s.Devcontainer.Mode {
		case "auto", "repo", "generic":
		default:
			return invalid("devcontainer mode must be auto, repo or generic")
		}
	}
	if s.Agent.ProfileID == "" {
		return invalid("pick an agent profile")
	}
	if !agents.ValidModel(s.Agent.Model) {
		return invalid("invalid model id")
	}
	switch s.Agent.Mode {
	case ModePlan, ModeNormal, ModeAuto:
	default:
		return invalid("mode must be plan, normal or auto")
	}
	if len(s.Request) > maxRequest {
		return invalid("the request is longer than 64 KiB")
	}
	return nil
}

func validRepoURL(raw string) error {
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, " \t\r\n\x00'\"`$\\") {
		return invalid("%q is not a repository URL", raw)
	}
	if scpRepoRe.MatchString(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "ssh") || u.Host == "" || u.Path == "" || u.Path == "/" {
		return invalid("%q is not a repository URL (https://, ssh:// or git@host:path)", raw)
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return invalid("put credentials in a Git account, not in the repository URL")
		}
	}
	return nil
}

func validRef(ref string) bool {
	if !refRe.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") || strings.HasSuffix(ref, ".") {
		return false
	}
	return true
}

// slugRe collapses everything that isn't a lowercase letter or digit.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// NewID returns a task id: the slugged name plus six random hex digits, e.g.
// dbg-142-print-crash-3f9a1c (§4.12.1). It becomes a directory name.
func NewID(name string) (string, error) {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		slug = "task"
	}
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("generate task id")
	}
	return slug + "-" + hex.EncodeToString(b), nil
}
