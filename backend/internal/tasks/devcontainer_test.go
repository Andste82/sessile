package tasks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Andste82/sessile/backend/internal/agents"
)

func filesByName(files []file) map[string]file {
	m := map[string]file{}
	for _, f := range files {
		m[f.name] = f
	}
	return m
}

func checkGolden(t *testing.T, name string, data []byte) {
	t.Helper()
	golden := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(golden, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != string(data) {
		t.Errorf("%s differs from %s:\n%s", name, golden, data)
	}
}

func TestRenderDevcontainerGolden(t *testing.T) {
	task := goldenTask()
	task.Spec.Devcontainer = &Devcontainer{Mode: "auto", DockerSocket: true}
	ln, _ := resolveLaunch(agents.AgentClaude, "claude-bedrock", ModePlan, "", true)
	files, err := buildFiles(task, "/home/ob/.sessile/tasks/"+task.ID, false, ln, agents.GitAccount{}, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	m := filesByName(files)
	for _, name := range []string{"task.sh", "agent.sh", ".env", "CLAUDE.md", ".devcontainer-generic/devcontainer.json"} {
		if _, ok := m[name]; !ok {
			t.Fatalf("no %s", name)
		}
	}
	checkGolden(t, "devcontainer-task.sh", m["task.sh"].data)
	checkGolden(t, "agent.sh", m["agent.sh"].data)
	if !strings.Contains(string(m["CLAUDE.md"].data), "`/sessile/task`") {
		t.Error("instructions must name the container's task folder")
	}
	for _, name := range []string{"task.sh", "agent.sh"} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(string(m[name].data))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", name, err, out)
		}
	}
	// No secret may reach a command line: the container gets them from the
	// mounted .env, never from --remote-env.
	if strings.Contains(string(m["task.sh"].data), "--remote-env") &&
		!strings.Contains(string(m["task.sh"].data), "--remote-env SESSILE_IN_CONTAINER=1 ") {
		t.Error("task.sh passes something other than the fixed marker through --remote-env")
	}

	// "repo" mode never writes the generic config.
	task.Spec.Devcontainer.Mode = "repo"
	files, _ = buildFiles(task, "/d", false, ln, agents.GitAccount{}, nil, nil, nil, "")
	if _, ok := filesByName(files)[".devcontainer-generic/devcontainer.json"]; ok {
		t.Error("repo mode wrote the generic config")
	}
}

func TestRenderWindowsDevcontainerGolden(t *testing.T) {
	task := goldenTask()
	task.Spec.Devcontainer = &Devcontainer{Mode: "repo"}
	ln, _ := resolveLaunch(agents.AgentClaude, "claude-subscription", ModePlan, "", true)
	files, err := buildFiles(task, `C:\Users\ob\.sessile\tasks\`+task.ID, true, ln, agents.GitAccount{}, nil, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	m := filesByName(files)
	// The Windows host runs task.ps1 with .env.json; the Linux container runs
	// agent.sh with the POSIX .env.
	for _, name := range []string{"task.ps1", ".env.json", "agent.sh", ".env"} {
		if _, ok := m[name]; !ok {
			t.Fatalf("no %s", name)
		}
	}
	checkGolden(t, "windows-devcontainer-task.ps1", m["task.ps1"].data)
}
