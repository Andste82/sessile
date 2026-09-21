package tasks

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/Andste82/sessile/backend/internal/agents"
)

//go:embed templates
var templateFS embed.FS

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

// psQuote wraps s as one single-quoted PowerShell string.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// psArg renders one native-command argument for task.ps1. An argument with a
// double quote in it is built from pieces joined by $q, which task.ps1 sets
// to what keeps the quote on the running PowerShell's version (5.1 strips a
// bare one from native arguments).
func psArg(s string) string {
	if !strings.Contains(s, `"`) {
		return psQuote(s)
	}
	parts := strings.Split(s, `"`)
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = psQuote(p)
	}
	return "(" + strings.Join(quoted, " + $q + ") + ")"
}

func psArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = psArg(a)
	}
	return strings.Join(parts, " ")
}

var templates = template.Must(template.New("").
	Funcs(template.FuncMap{"q": shellQuote, "argv": argvString, "ps": psQuote, "psargv": psArgv}).
	ParseFS(templateFS, "templates/*.tmpl"))

// bootstrapData fills task.sh.tmpl and task.ps1.tmpl.
type bootstrapData struct {
	ID, Dir           string
	Repo              *Repo
	GitName, GitEmail string
	Agent             string
	Install           string // the registry's user-space installer (sh), "" for none
	InstallPS         string // the same for Windows (PowerShell)
	First, Resume     []string
	Devcontainer      *Devcontainer
	// Local marks a task running on the sessile server itself, where $HOME
	// is the server's OS user's, not the task user's (§4.12.9).
	Local bool
}

// instructionsData fills instructions.md.tmpl.
type instructionsData struct {
	Name, Dir    string
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

// envNameRe is what may appear as a variable name in .env.
var envNameRe = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// renderEnv writes KEY='value' lines for the bootstrap to source (§4.12.9).
// The file is shell syntax, so every value is single-quoted.
func renderEnv(vars [][2]string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("# Written by sessile for this start; the bootstrap loads and deletes it.\n")
	for _, kv := range vars {
		if !envNameRe.MatchString(kv[0]) {
			return nil, fmt.Errorf("invalid environment variable name %q", kv[0])
		}
		if strings.ContainsAny(kv[1], "\x00") {
			return nil, fmt.Errorf("environment variable %s contains a NUL byte", kv[0])
		}
		fmt.Fprintf(&buf, "%s=%s\n", kv[0], shellQuote(kv[1]))
	}
	return buf.Bytes(), nil
}

// renderEnvJSON is .env for Windows: task.ps1 reads it with ConvertFrom-Json,
// which needs no quoting rules of its own.
func renderEnvJSON(vars [][2]string) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("{")
	for i, kv := range vars {
		if !envNameRe.MatchString(kv[0]) {
			return nil, fmt.Errorf("invalid environment variable name %q", kv[0])
		}
		if i > 0 {
			buf.WriteString(",")
		}
		k, _ := json.Marshal(kv[0])
		v, _ := json.Marshal(kv[1])
		fmt.Fprintf(&buf, "\n  %s: %s", k, v)
	}
	buf.WriteString("\n}\n")
	return buf.Bytes(), nil
}

// gitEnv is the environment-only git configuration of §4.16: for each
// account, reset any credential helper the host has for that URL, then add
// one that answers from this task's environment. Nothing reaches git config
// on disk, and the token is never part of a helper string or a URL.
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
