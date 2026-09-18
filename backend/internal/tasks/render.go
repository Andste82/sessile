package tasks

import (
	"bytes"
	"embed"
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

var templates = template.Must(template.New("").
	Funcs(template.FuncMap{"q": shellQuote, "argv": argvString}).
	ParseFS(templateFS, "templates/*.tmpl"))

// bootstrapData fills task.sh.tmpl.
type bootstrapData struct {
	ID, Dir           string
	Repo              *Repo
	GitName, GitEmail string
	Agent             string
	Install           string // the registry's user-space installer, "" for none
	First, Resume     []string
}

// instructionsData fills instructions.md.tmpl.
type instructionsData struct {
	Name, Dir   string
	Repo        *Repo
	GitHosts    string
	GitHub      bool
	Notes       bool
	AlwaysNotes []Note
	Tools       string
	HasRequest  bool
	Summary     string
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
