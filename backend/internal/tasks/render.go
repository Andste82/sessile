package tasks

import (
	"bytes"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/Andste82/sessile/backend/internal/agents"
)

//go:embed templates
var templateFS embed.FS

// templates are the files sessile renders into an agent's folder: its
// instructions, and the orchestrator's (§4.12.2). There is no bootstrap any
// more — the agent runs here, started by sessile itself.
var templates = template.Must(template.New("tasks").Funcs(template.FuncMap{
	"q":    shellQuote,
	"argv": argvString,
}).ParseFS(templateFS, "templates/*.tmpl"))

// instructionsData fills instructions.md.tmpl.
type instructionsData struct {
	Name, Dir string
	// Host is the machine the work is on, as the user named it.
	Host         string
	Repo         *Repo
	Devcontainer bool
	GitHosts     string
	GitHub       bool
	Notes        bool
	AlwaysNotes  []Note
	Tools        string
	HasRequest   bool
	Summary      string
}

// orchestratorData fills orchestrator.md.tmpl (§4.18).
type orchestratorData struct {
	Dir        string
	Tools      string
	HasRequest bool
}

// shellQuote wraps s as one single-quoted POSIX shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func argvString(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

// Note is one of the user's notes as a task sees it (§4.14).
type Note struct {
	Slug  string
	Title string
	Body  string
	// Always marks a note inlined into the instructions.
	Always bool
}

func render(name string, data any) ([]byte, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// gitEnv is the Git account environment for one command on the host: the
// credential helper reads the username and token from it, so nothing is
// written to disk there and the token is never part of a URL (§4.16).
func gitEnv(accounts []agents.GitAccount) [][2]string {
	if len(accounts) == 0 {
		return nil
	}
	var env [][2]string
	n := 0
	for i, g := range accounts {
		user := "SESSILE_GIT_USERNAME_" + strconv.Itoa(i)
		token := "SESSILE_GIT_TOKEN_" + strconv.Itoa(i)
		env = append(env, [2]string{user, g.Username}, [2]string{token, g.Token})
		key := "credential.https://" + strings.ToLower(g.Host) + ".helper"
		helper := fmt.Sprintf(`!f() { test "$1" = get || exit 0; echo "username=$%s"; echo "password=$%s"; }; f`, user, token)
		env = append(env,
			[2]string{"GIT_CONFIG_KEY_" + strconv.Itoa(n), key}, [2]string{"GIT_CONFIG_VALUE_" + strconv.Itoa(n), ""},
			[2]string{"GIT_CONFIG_KEY_" + strconv.Itoa(n+1), key}, [2]string{"GIT_CONFIG_VALUE_" + strconv.Itoa(n+1), helper},
		)
		n += 2
		if strings.EqualFold(g.Host, "github.com") {
			env = append(env, [2]string{"GH_TOKEN", g.Token})
		}
	}
	env = append(env, [2]string{"GIT_CONFIG_COUNT", strconv.Itoa(n)})
	return env
}

// gitHostsList is the "github.com (as me), gitlab.example.com (as you)" text
// for the instructions.
func gitHostsList(accounts []agents.GitAccount) (string, bool) {
	var parts []string
	github := false
	for _, g := range accounts {
		parts = append(parts, fmt.Sprintf("%s (as %s)", g.Host, g.Username))
		if strings.EqualFold(g.Host, "github.com") {
			github = true
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ", "), github
}

// serverEnvBlocked names, by prefix, what a local task's agent must not
// inherit from the sessile server's own process environment (§4.12.9).
//
// A local task is the one case where sessile's environment becomes an
// agent's: an SSH task gets its user's login environment on their own host,
// but a task on the server starts as a child of sessile. Whatever the
// operator's shell held when they started sessile would otherwise configure
// the agent — and silently win over the task's own connection, since a CLI
// that finds ANTHROPIC_API_KEY or AWS credentials in its environment uses
// them. It also hands the agent credentials that are not the user's to have.
//
// The same applies to an agent harness that started sessile: a Claude Code
// session exports CLAUDE_CODE_* (a session id, a messaging socket and its
// token), and an agent inheriting them believes it is a child of that
// session and tries to talk to it.
//
// Everything else is inherited on purpose: PATH, HOME, the locale, and the
// proxy variables an operator behind a corporate proxy relies on. sessile's
// own values are appended after this and so are unaffected.
var serverEnvBlocked = []string{
	"CLAUDE", // CLAUDECODE, CLAUDE_CODE_*, CLAUDE_PID, CLAUDE_CONFIG_DIR…
	"ANTHROPIC",
	"AWS_",    // Bedrock
	"GOOGLE_", // Vertex
	"GEMINI",
	"OPENAI",
	"CODEX",
	"GIT_", // the credential config sessile renders per task
	"GH_TOKEN",
	"GITHUB_TOKEN",
}
