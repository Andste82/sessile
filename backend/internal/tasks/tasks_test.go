package tasks

import (
	"encoding/json"
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
	"github.com/Andste82/sessile/backend/internal/confine"
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

// Auto mode starts each CLI in its own no-prompt mode, and plan and normal
// are untouched by it (§4.12.4b).
func TestResolveLaunchAutoMode(t *testing.T) {
	tests := map[agents.Agent]string{
		agents.AgentClaude: "--permission-mode auto",
		agents.AgentCodex:  "--sandbox workspace-write --ask-for-approval never",
		agents.AgentGemini: "--approval-mode yolo",
	}
	for agent, want := range tests {
		ln, ok := resolveLaunch(agent, "", ModeAuto, "", false)
		if !ok {
			t.Fatalf("%s: not in the registry", agent)
		}
		if got := strings.Join(ln.first, " "); !strings.Contains(got, want) {
			t.Errorf("%s auto = %q, want it to contain %q", agent, got, want)
		}
		plan, _ := resolveLaunch(agent, "", ModePlan, "", false)
		if strings.Contains(strings.Join(plan.first, " "), want) {
			t.Errorf("%s plan mode must not carry the auto flags", agent)
		}
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

// The agent reads its instructions from its own folder on the server, and
// nothing sessile writes there carries a credential (§4.12.2, §4.16).
func TestRenderInstructionsGolden(t *testing.T) {
	task := goldenTask()
	ln, _ := resolveLaunch(agents.AgentClaude, "claude-subscription", ModePlan, "", true)
	acct := agents.GitAccount{Host: "github.com", Name: "O'Brien", Email: "ob@example.com", Username: "ob", Token: "ghp_x"}
	files, err := buildFiles(task, "/srv/sessile/users/u1/tasks/"+task.ID, "build-01", "/home/ob/sessile-tasks/"+task.ID,
		ln, acct, []agents.GitAccount{acct},
		[]Note{{Slug: "repos", Title: "repos", Body: "- moonlight-android: the Android client\n", Always: true}}, "")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]file{}
	for _, f := range files {
		byName[f.name] = f
	}
	golden := filepath.Join("testdata", "CLAUDE.md.golden")
	if *update {
		if err := os.WriteFile(golden, byName["CLAUDE.md"].data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != string(byName["CLAUDE.md"].data) {
		t.Errorf("CLAUDE.md differs from %s:\n%s", golden, byName["CLAUDE.md"].data)
	}
	if got := string(byName["PROMPT.md"].data); got != task.Spec.Request+"\n" {
		t.Errorf("PROMPT.md = %q", got)
	}
	for name, f := range byName {
		if strings.Contains(string(f.data), acct.Token) {
			t.Errorf("%s contains the git token", name)
		}
	}
}

// A task's agent runs on the sessile server, in its own folder under the
// data dir, started by sessile itself — no bootstrap, no install on a host
// (§4.12, v0.9).
func TestAgentRunsOnTheServer(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dataDir, root, binDir := t.TempDir(), t.TempDir(), t.TempDir()
	fake := "#!/bin/sh\necho \"FAKE-CLAUDE args=[$*] token=$CLAUDE_CODE_OAUTH_TOKEN config=$CLAUDE_CONFIG_DIR\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())

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
	// No sessile binary under `go test`, so the confinement re-exec cannot
	// run here; TestAgentArgvIsConfined covers that it is asked for.
	svc := &Service{DB: db, Agents: reg, Hosts: hosts.NewRegistry(dataDir), Log: log, DataDir: dataDir,
		AllowUnconfined: true}
	mgr := session.NewManager(root, []string{"sh"}, 1<<16, dataDir, db, log)
	mgr.SetTaskLauncher(svc)
	svc.Sessions = mgr
	defer mgr.Shutdown()

	info, err := svc.Create("u1", Spec{Name: "Local task", Target: "local",
		Agent: AgentSpec{ProfileID: "p1"}, Request: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if info.Group != session.TaskGroup || info.TaskID == "" {
		t.Fatalf("info = %+v", info)
	}
	waitStopped(t, mgr, info.ID)

	dir := svc.AgentDir("u1", info.TaskID)
	for _, name := range []string{"CLAUDE.md", "PROMPT.md", "task.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The agent's state is its own, never the server user's home (E13).
	if _, err := os.Stat(filepath.Join(dir, agentStateDir)); err == nil {
		t.Log("agent state dir created")
	}
	// Nothing of the host pipeline survives: no bootstrap, no .env.
	for _, gone := range []string{"task.sh", "agent.sh", ".env", ".tools"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s should not exist any more", gone)
		}
	}
	out := readSession(t, mgr, info.ID)
	if !strings.Contains(out, "FAKE-CLAUDE") || !strings.Contains(out, "token=tok-123") {
		t.Errorf("the agent did not start with its connection:\n%s", out)
	}
	if !strings.Contains(out, "config="+filepath.Join(dir, agentStateDir, "claude")) {
		t.Errorf("the agent's config dir is not its own:\n%s", out)
	}
	// Auto is the default (§4.12.4b): the agent plans because its
	// instructions say to, not because a prompt stops it at every command.
	if !strings.Contains(out, "--permission-mode auto") {
		t.Errorf("a task starts in auto mode by default:\n%s", out)
	}
	if instructions, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(instructions), "Work out a plan before you touch anything") {
		t.Error("auto mode still asks for a plan first, in the instructions")
	}

	// The fake agent saved no conversation, so a restart must start one
	// rather than ask the CLI to continue nothing — which makes a real CLI
	// exit at once ("No conversation found to continue").
	if _, err := mgr.Restart(info.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitStopped(t, mgr, info.ID)
	if out := readSession(t, mgr, info.ID); strings.Count(out, "--continue") != 0 {
		t.Errorf("a restart with no saved conversation must not resume:\n%s", out)
	}

	// Once the agent has saved one, a restart continues it.
	history := filepath.Join(dir, agentStateDir, "claude", "projects", "task")
	if err := os.MkdirAll(history, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, "conversation.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Restart(info.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitStopped(t, mgr, info.ID)
	if out := readSession(t, mgr, info.ID); !strings.Contains(out, "--continue") {
		t.Errorf("a restart with a saved conversation should resume:\n%s", out)
	}

	// A fresh restart deletes the history, and so starts over.
	svc.RequestRestart(info.TaskID, RestartOptions{Fresh: true})
	if _, err := mgr.Restart(info.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	waitStopped(t, mgr, info.ID)
	out = readSession(t, mgr, info.ID)
	if last := out[strings.LastIndex(out, "FAKE-CLAUDE"):]; strings.Contains(last, "--continue") {
		t.Errorf("a fresh restart must not resume:\n%s", last)
	}
}

// readSession returns what a session has printed so far.
func readSession(t *testing.T, mgr *session.Manager, id string) string {
	t.Helper()
	out, err := mgr.Output(id, "u1", 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
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
// through real git: git must answer from the environment sessile sets on the
// one command that needs it, and nothing may end up in any git config file.
func TestGitEnvHelper(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	home := t.TempDir()
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_TERMINAL_PROMPT=0"}
	for _, kv := range gitEnv([]agents.GitAccount{
		{Host: "github.com", Username: "octo", Token: "ghp_tok'en"},
		{Host: "gitlab.example.com", Username: "lab", Token: "glpat"},
	}) {
		env = append(env, kv[0]+"="+kv[1])
	}
	for host, want := range map[string]string{
		"github.com":         "username=octo\npassword=ghp_tok'en\n",
		"gitlab.example.com": "username=lab\npassword=glpat\n",
	} {
		cmd := exec.Command("git", "credential", "fill")
		cmd.Env = env
		cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git credential fill for %s: %v", host, err)
		}
		if !strings.Contains(string(out), want) {
			t.Errorf("git answered %q for %s, want %q", out, host, want)
		}
	}
	// Nothing was written to a config file: the environment carried it all.
	for _, name := range []string{".gitconfig", ".git-credentials"} {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			t.Errorf("%s was created; credentials must stay in the environment", name)
		}
	}
}

func TestSFTPPath(t *testing.T) {
	for in, want := range map[string]string{
		".sessile/tasks": ".sessile/tasks",
		"/srv/tasks":     "/srv/tasks",
		`C:\work\tasks`:  "/C:/work/tasks",
		"D:/tasks":       "/D:/tasks",
		"/C:/x":          "/C:/x",
	} {
		if got := sftpPath(in); got != want {
			t.Errorf("sftpPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOrchestratorEndToEnd runs the orchestrator the way OpenOrchestrator
// does: one session per user, on the server, in its own folder, with its own
// instructions — and reopening it returns the same session (§4.18).
func TestOrchestratorEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dataDir := t.TempDir()
	root := t.TempDir()
	binDir := t.TempDir()
	fake := "#!/bin/sh\necho \"FAKE-CLAUDE args=[$*]\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/true")

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
	// No sessile binary under `go test`, so the confinement re-exec cannot
	// run here; TestAgentArgvIsConfined covers that it is asked for.
	svc := &Service{DB: db, Agents: reg, Hosts: hosts.NewRegistry(dataDir), Log: log, DataDir: dataDir,
		AllowUnconfined: true}
	mgr := session.NewManager(root, []string{"sh"}, 1<<16, dataDir, db, log)
	mgr.SetTaskLauncher(svc)
	svc.Sessions = mgr
	defer mgr.Shutdown()

	info, err := svc.OpenOrchestrator("u1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	task, found, err := svc.Orchestrator("u1")
	if err != nil || !found {
		t.Fatalf("orchestrator not stored: %v %v", found, err)
	}
	if task.Kind != KindOrchestrator || task.SessionID != info.ID {
		t.Fatalf("task = %+v, session %s", task, info.ID)
	}
	waitStopped(t, mgr, info.ID)

	// Its own folder under the data dir, with the orchestrator's instructions
	// rather than a task's.
	dir := svc.AgentDir("u1", task.ID)
	instructions, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"You are the orchestrator", "create_task", "wait_for_events"} {
		if !strings.Contains(string(instructions), want) {
			t.Errorf("instructions are missing %q:\n%s", want, instructions)
		}
	}

	// Reopening restarts the same session rather than starting a second one.
	again, err := svc.OpenOrchestrator("u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != info.ID {
		t.Errorf("reopened as %s, want the same session %s", again.ID, info.ID)
	}
	waitStopped(t, mgr, info.ID)
	list, err := svc.List("u1")
	if err != nil || len(list) != 1 {
		t.Fatalf("tasks = %d (%v), want the one orchestrator", len(list), err)
	}
}

// An agent's argv goes through the confinement step, and a task refuses to
// start rather than running an agent that cannot be confined (§4.12.9, E14).
func TestAgentArgvIsConfined(t *testing.T) {
	if !confine.Supported() {
		t.Skip("this kernel has no Landlock")
	}
	dir := t.TempDir()
	svc := &Service{SelfExe: "/usr/local/bin/sessile"}
	argv, err := svc.confined(dir, "/usr/bin/claude", []string{"/usr/bin/claude", "--continue"})
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "/usr/local/bin/sessile" || argv[1] != "confine-exec" || argv[3] != "--" {
		t.Fatalf("argv = %v", argv)
	}
	var rules confine.Rules
	if err := json.Unmarshal([]byte(argv[2]), &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules.ReadWrite) == 0 || rules.ReadWrite[0] != dir {
		t.Errorf("the task folder must be writable: %v", rules.ReadWrite)
	}
	for _, p := range append(rules.ReadWrite, rules.ReadOnly...) {
		if strings.HasPrefix(p, "/root/.claude") || strings.Contains(p, "/data/users") {
			t.Errorf("%s must not be reachable by an agent", p)
		}
	}
	if argv[len(argv)-1] != "--continue" {
		t.Errorf("the agent's own arguments were lost: %v", argv)
	}

	// Without a binary to re-exec, a task refuses rather than running free.
	if _, err := (&Service{}).confined(dir, "/usr/bin/claude", []string{"/usr/bin/claude"}); err == nil {
		t.Error("an agent that cannot be confined must not start")
	}
}
