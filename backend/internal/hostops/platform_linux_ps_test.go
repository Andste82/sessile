package hostops

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestLinuxProcessTreeAgainstRealShell spawns a real shell with two known
// children and confirms ProcessTree finds both against the local, real
// `ps` — not a scripted response.
func TestLinuxProcessTreeAgainstRealShell(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 5 & sleep 6 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start shell: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	shellPID := cmd.Process.Pid

	// Give the shell a moment to fork both children.
	var tree []Process
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		tree, err = NewLinuxPlatform().ProcessTree(context.Background(), NewLocal(), shellPID)
		if err != nil {
			t.Fatalf("ProcessTree: %v", err)
		}
		if len(tree) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(tree) != 2 {
		t.Fatalf("ProcessTree(%d) = %d children, want 2: %+v", shellPID, len(tree), tree)
	}
	for _, p := range tree {
		if p.PPID != shellPID {
			t.Errorf("child %+v has PPID %d, want %d", p, p.PPID, shellPID)
		}
		if p.Command != "sleep" {
			t.Errorf("child %+v command = %q, want sleep", p, p.Command)
		}
	}
}

func TestBuildProcessTreeNestsGrandchildren(t *testing.T) {
	flat := []flatProcess{
		{pid: 1, ppid: 0, command: "init"},
		{pid: 10, ppid: 1, command: "shell"},
		{pid: 11, ppid: 10, command: "make"},
		{pid: 12, ppid: 11, command: "cc1"},
		{pid: 99, ppid: 1, command: "unrelated"},
	}

	tree := buildProcessTree(flat, 10)
	if len(tree) != 1 || tree[0].PID != 11 {
		t.Fatalf("tree = %+v, want one child pid=11", tree)
	}
	grand := tree[0].Children
	if len(grand) != 1 || grand[0].PID != 12 {
		t.Fatalf("grandchildren = %+v, want one child pid=12", grand)
	}
}

func TestParsePSSkipsMalformedLines(t *testing.T) {
	out := "  1   0 init\n\n   garbage line\n 42  1 sleep\n"
	flat, err := parsePS(out)
	if err != nil {
		t.Fatalf("parsePS: %v", err)
	}
	if len(flat) != 2 {
		t.Fatalf("parsePS = %+v, want 2 entries", flat)
	}
	if flat[0] != (flatProcess{pid: 1, ppid: 0, command: "init"}) {
		t.Errorf("flat[0] = %+v", flat[0])
	}
	if flat[1] != (flatProcess{pid: 42, ppid: 1, command: "sleep"}) {
		t.Errorf("flat[1] = %+v", flat[1])
	}
}
