package tasks

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/agents"
	"github.com/Andste82/sessile/backend/internal/hosts"
	"github.com/Andste82/sessile/backend/internal/session"
	"github.com/Andste82/sessile/backend/internal/storage"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestSpecValidate(t *testing.T) {
	base := func() Spec {
		return Spec{Name: "DBG-142 print crash", HostID: "h1",
			Repo:  &Repo{URL: "https://github.com/moonlight-stream/moonlight-android", Ref: "main"},
			Agent: AgentSpec{ProfileID: "p1", Mode: ModePlan}}
	}
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{"valid", func(*Spec) {}, ""},
		{"valid scp url", func(s *Spec) { s.Repo.URL = "git@github.com:a/b.git" }, ""},
		{"valid ssh url", func(s *Spec) { s.Repo.URL = "ssh://git@host:2222/a/b.git" }, ""},
		{"valid local", func(s *Spec) { s.HostID, s.Target = "", "local" }, ""},
		{"no name", func(s *Spec) { s.Name = "" }, "name"},
		{"host and local", func(s *Spec) { s.Target = "local" }, "pick a host"},
		{"neither", func(s *Spec) { s.HostID = "" }, "pick a host"},
		{"option as url", func(s *Spec) { s.Repo.URL = "--upload-pack=touch /tmp/x" }, "not a repository URL"},
		{"quote in url", func(s *Spec) { s.Repo.URL = "https://h/a'b" }, "not a repository URL"},
		{"file url", func(s *Spec) { s.Repo.URL = "file:///etc" }, "not a repository URL"},
		{"password in url", func(s *Spec) { s.Repo.URL = "https://u:p@github.com/a/b" }, "Git account"},
		{"ref option", func(s *Spec) { s.Repo.Ref = "-b" }, "branch"},
		{"ref dots", func(s *Spec) { s.Repo.Ref = "a..b" }, "branch"},
		{"devcontainer without repo", func(s *Spec) { s.Repo = nil; s.Devcontainer = &Devcontainer{Mode: "auto"} }, "needs the main repo"},
		{"bad devcontainer mode", func(s *Spec) { s.Devcontainer = &Devcontainer{Mode: "x"} }, "mode"},
		{"no profile", func(s *Spec) { s.Agent.ProfileID = "" }, "profile"},
		{"bad model", func(s *Spec) { s.Agent.Model = "a;b" }, "model"},
		{"bad mode", func(s *Spec) { s.Agent.Mode = "yolo" }, "mode"},
		{"huge request", func(s *Spec) { s.Request = strings.Repeat("x", maxRequest+1) }, "64 KiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base()
			tt.mutate(&s)
			err := s.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewID(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}-[0-9a-f]{6}$`)
	for name, prefix := range map[string]string{
		"DBG-142: print crash!":       "dbg-142-print-crash-",
		"   ":                         "task-",
		"Ünïcödé only":                "n-c-d-only-",
		strings.Repeat("abcdefgh", 9): "abcdefghabcdefghabcdefghabcdefghabcdefgh-",
	} {
		id, err := NewID(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, prefix) || !re.MatchString(id) {
			t.Errorf("NewID(%q) = %q, want prefix %q and the session package's id shape", name, id, prefix)
		}
	}
}

func TestResolveLaunch(t *testing.T) {
	ln, _ := resolveLaunch(agents.AgentClaude, "claude-subscription", ModePlan, "opus", true)
	if got := strings.Join(ln.first, " "); got != "claude --permission-mode plan "+requestInstruction {
		t.Errorf("claude first = %q", got)
	}
	if got := strings.Join(ln.resume, " "); got != "claude --continue --permission-mode plan" {
		t.Errorf("claude resume = %q", got)
	}
	if len(ln.env) != 1 || ln.env[0] != [2]string{"ANTHROPIC_MODEL", "opus"} {
		t.Errorf("claude env = %v", ln.env)
	}

	ln, _ = resolveLaunch(agents.AgentCodex, "codex-api", ModeNormal, "gpt-5-codex", false)
	got := strings.Join(ln.first, " ")
	if !strings.HasPrefix(got, "codex -m gpt-5-codex -c ") || !strings.Contains(got, `env_key="OPENAI_API_KEY"`) {
		t.Errorf("codex first = %q", got)
	}

	ln, _ = resolveLaunch(agents.AgentGemini, "gemini-api", ModePlan, "", true)
	if got := strings.Join(ln.first, " "); got != "gemini --approval-mode plan -i "+requestInstruction {
		t.Errorf("gemini first = %q", got)
	}
	if len(ln.env) != 1 || ln.env[0][0] != "GEMINI_CLI_TRUST_WORKSPACE" {
		t.Errorf("gemini env = %v", ln.env)
	}
}

func goldenTask() Task {
	return Task{
		ID: "dbg-142-print-crash-3f9a1c",
		Spec: Spec{Name: "DBG-142 print crash", HostID: "h1",
			Repo:    &Repo{URL: "https://github.com/moonlight-stream/moonlight-android", Ref: "main"},
			Agent:   AgentSpec{ProfileID: "p1", Mode: ModePlan},
			Request: "Fix DBG-142, it's the print crash"},
		Summary: "Plan approved, implementing",
	}
}

func TestRenderGolden(t *testing.T) {
	task := goldenTask()
	ln, _ := resolveLaunch(agents.AgentClaude, "claude-subscription", ModePlan, "", true)
	acct := agents.GitAccount{Host: "github.com", Name: "O'Brien", Email: "ob@example.com", Username: "ob", Token: "ghp_x"}
	files, err := buildFiles(task, "/home/ob/.sessile/tasks/"+task.ID, ln, acct, []agents.GitAccount{acct},
		append(agents.Connection{Kind: "claude-subscription", Fields: map[string]string{"token": "oat-'x'"}}.Env(), gitEnv([]agents.GitAccount{acct})...),
		[]Note{{Slug: "repos", Title: "repos", Body: "- moonlight-android: the Android client\n", Always: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]file{}
	for _, f := range files {
		byName[f.name] = f
	}
	for _, name := range []string{"task.sh", "CLAUDE.md", ".env"} {
		f, ok := byName[name]
		if !ok {
			t.Fatalf("no %s", name)
		}
		golden := filepath.Join("testdata", strings.TrimPrefix(name, ".")+".golden")
		if *update {
			if err := os.WriteFile(golden, f.data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run go test -update)", err)
		}
		if string(want) != string(f.data) {
			t.Errorf("%s differs from %s:\n%s", name, golden, f.data)
		}
	}
	if byName[".env"].perm != 0o600 || byName["task.sh"].perm != 0o700 {
		t.Errorf("permissions: .env %o, task.sh %o", byName[".env"].perm, byName["task.sh"].perm)
	}
	if string(byName["PROMPT.md"].data) != task.Spec.Request+"\n" {
		t.Errorf("PROMPT.md = %q", byName["PROMPT.md"].data)
	}

	// Both shell files must parse.
	for _, name := range []string{"task.sh", ".env"} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(string(byName[name].data))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", name, err, out)
		}
	}
}

// TestEnvRoundTrip sources a rendered .env the way the bootstrap does and
// checks every value comes back exactly — quotes, dollars and all.
func TestEnvRoundTrip(t *testing.T) {
	vars := [][2]string{
		{"PLAIN", "abc"},
		{"QUOTES", `it's "quoted"`},
		{"DOLLAR", "$HOME `id` $(id)"},
		{"HELPER", `!f() { echo "username=$SESSILE_GIT_USERNAME_0"; }; f`},
	}
	data, err := renderEnv(vars)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "set -a; . " + shellQuote(envPath) + "; set +a; env"
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range vars {
		if !strings.Contains(string(out), kv[0]+"="+kv[1]+"\n") {
			t.Errorf("%s did not round-trip:\n%s", kv[0], out)
		}
	}
	if _, err := renderEnv([][2]string{{"bad-name", "x"}}); err == nil {
		t.Error("want error for an invalid name")
	}
}

// TestLocalTaskEndToEnd creates a local-host task with a fake agent on PATH
// and checks the whole start: files written, .env loaded and removed, the
// agent started with the registry's argv, then resumed on restart.
func TestLocalTaskEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dataDir := t.TempDir()
	root := t.TempDir()
	binDir := t.TempDir()
	// The fake claude prints its argv and the token it was given, then exits.
	fake := "#!/bin/sh\necho \"FAKE-CLAUDE args=[$*] token=$CLAUDE_CODE_OAUTH_TOKEN\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())  // keep a real ~/.local/bin/claude out of it
	t.Setenv("SHELL", "/bin/true") // so the bootstrap's closing shell exits at once

	db, err := storage.Open(filepath.Join(dataDir, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reg := agents.NewRegistry(dataDir)
	st, _ := reg.For("u1")
	if _, err := st.Update(func(s *agents.Settings) error {
		s.Connections = []agents.Connection{{ID: "c1", Name: "Max", Kind: "claude-subscription", Fields: map[string]string{"token": "tok-123"}}}
		s.Profiles = []agents.Profile{{ID: "p1", Name: "Claude", Agent: agents.AgentClaude, ConnectionID: "c1"}}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &Service{DB: db, Agents: reg, Hosts: hosts.NewRegistry(dataDir), Log: log, WorkspaceTasksDir: ".sessile/tasks"}
	mgr := session.NewManager(root, []string{"sh"}, 1<<16, "", db, log)
	mgr.SetTaskLauncher(svc)
	defer mgr.Shutdown()

	spec := Spec{Name: "Local task", Target: "local", Agent: AgentSpec{ProfileID: "p1"}, Request: "do it"}
	spec.Normalize()
	if _, err := svc.Check("u1", spec); err != nil {
		t.Fatal(err)
	}
	taskID, err := svc.Store("u1", "s1", spec)
	if err != nil {
		t.Fatal(err)
	}
	info, err := mgr.CreateTask("s1", "u1", spec.Name, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if info.TaskID != taskID || info.Group != session.TaskGroup {
		t.Fatalf("info = %+v", info)
	}
	waitStopped(t, mgr, "s1")

	dir := filepath.Join(root, ".sessile", "tasks", taskID)
	for _, name := range []string{"task.sh", "CLAUDE.md", "PROMPT.md", "task.json", ".agent-started"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env still on disk after the bootstrap loaded it (err=%v)", err)
	}
	task, err := svc.Get("u1", taskID)
	if err != nil || task.Dir != dir {
		t.Fatalf("task = %+v, %v; want dir %s", task, err, dir)
	}

	// The fake agent's output is only in the ring buffer; the restart below
	// seeds nothing without a data dir, so check the first run through a
	// second restart's resume argv instead: run the bootstrap by hand.
	out, err := exec.Command("sh", filepath.Join(dir, "task.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("rerun bootstrap: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "FAKE-CLAUDE args=[--continue --permission-mode plan]") {
		t.Errorf("second start should resume:\n%s", out)
	}

	if _, err := mgr.Restart("s1", "u1"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	waitStopped(t, mgr, "s1")
	if _, err := os.Stat(filepath.Join(dir, ".env")); !os.IsNotExist(err) {
		t.Errorf(".env left behind after restart")
	}
}

func waitStopped(t *testing.T, mgr *session.Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		info, err := mgr.Get(id, "u1")
		if err == nil && info.Status == session.StatusStopped {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s never stopped", id)
}

// TestGitEnvHelper runs the environment-only credential config (§4.16)
// through real git: git must answer from the task env, and nothing may end up
// in any git config file.
func TestGitEnvHelper(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	home := t.TempDir()
	data, err := renderEnv(gitEnv([]agents.GitAccount{
		{Host: "github.com", Username: "octo", Token: "ghp_tok'en"},
		{Host: "gitlab.example.com", Username: "lab", Token: "glpat"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(home, ".env")
	if err := os.WriteFile(envPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{
		"github.com":         "username=octo\npassword=ghp_tok'en\n",
		"gitlab.example.com": "username=lab\npassword=glpat\n",
	} {
		script := "set -a; . " + shellQuote(envPath) + "; set +a; printf 'protocol=https\\nhost=" + host + "\\n\\n' | git credential fill"
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", host, err, out)
		}
		if !strings.Contains(string(out), want) {
			t.Errorf("%s: got\n%s", host, out)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 1 {
		t.Errorf("git wrote into HOME: %v", entries)
	}
}
