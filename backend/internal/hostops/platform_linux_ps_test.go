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
		tree, err = NewLinuxPlatform().ProcessTree(context.Background(), NewLocal(), &shellPID)
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

// TestLinuxProcessTreeForestModeAgainstRealPS confirms rootPID=nil against
// this machine's real `ps` finds a genuine multi-root forest, not just a
// scripted one — PID 1 (init/systemd) should always be one of the roots
// on any real Linux box this runs on.
func TestLinuxProcessTreeForestModeAgainstRealPS(t *testing.T) {
	forest, err := NewLinuxPlatform().ProcessTree(context.Background(), NewLocal(), nil)
	if err != nil {
		t.Fatalf("ProcessTree: %v", err)
	}
	if len(forest) == 0 {
		t.Fatal("forest is empty, want at least pid 1")
	}
	found := false
	for _, p := range forest {
		if p.PID == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("forest = %+v, want pid 1 among the roots", forest)
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

// TestBuildProcessTreeSurvivesACycle reproduces the review's exact repro:
// two processes whose ppid points at each other. Before the seen-set
// guard, this recursed forever and crashed the whole server with an
// uncatchable stack-overflow fatal error — proven here by simply not
// hanging or crashing.
func TestBuildProcessTreeSurvivesACycle(t *testing.T) {
	flat := []flatProcess{
		{pid: 100, ppid: 200, command: "a"},
		{pid: 200, ppid: 100, command: "b"},
	}
	done := make(chan []Process, 1)
	go func() { done <- buildProcessTree(flat, 100) }()
	select {
	case tree := <-done:
		// 100 is the root; its only listed child is 200, which is fine to
		// include once. What must not happen is recursing back into 100.
		if len(tree) != 1 || tree[0].PID != 200 || len(tree[0].Children) != 0 {
			t.Fatalf("tree = %+v, want exactly one child (200) with no grandchildren", tree)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildProcessTree did not return — cycle guard failed")
	}
}

// TestBuildProcessTreeSurvivesASelfReferentialRoot covers a process whose
// own ppid equals its own pid (System Idle Process on Windows is commonly
// reported as pid 0, ppid 0) — the seen-set already marks rootPID visited
// up front, so this must not loop even when the root is its own parent.
func TestBuildProcessTreeSurvivesASelfReferentialRoot(t *testing.T) {
	flat := []flatProcess{{pid: 0, ppid: 0, command: "System Idle Process"}}
	done := make(chan []Process, 1)
	go func() { done <- buildProcessTree(flat, 0) }()
	select {
	case tree := <-done:
		if len(tree) != 0 {
			t.Fatalf("tree = %+v, want no children (root is its own only listed parent)", tree)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildProcessTree did not return — self-referential root not guarded")
	}
}

func TestBuildProcessForestFindsEveryRoot(t *testing.T) {
	flat := []flatProcess{
		{pid: 1, ppid: 0, command: "init"},
		{pid: 10, ppid: 1, command: "shell"},
		{pid: 4, ppid: 0, command: "System"},
		{pid: 40, ppid: 4, command: "svchost"},
	}
	forest := buildProcessForest(flat)
	if len(forest) != 2 {
		t.Fatalf("forest = %+v, want 2 roots (pid 1 and pid 4 — neither's ppid 0 appears as a pid)", forest)
	}
	byPID := map[int]Process{forest[0].PID: forest[0], forest[1].PID: forest[1]}
	if root, ok := byPID[1]; !ok || len(root.Children) != 1 || root.Children[0].PID != 10 {
		t.Errorf("root pid=1 = %+v, want one child pid=10", byPID[1])
	}
	if root, ok := byPID[4]; !ok || len(root.Children) != 1 || root.Children[0].PID != 40 {
		t.Errorf("root pid=4 = %+v, want one child pid=40", byPID[4])
	}
}

// TestBuildProcessForestSurvivesACycle: same guarantee as the rooted
// version, for the no-fixed-root path a Windows target's "whole host" view
// now uses instead of a hardcoded rootPID (§6 of the review).
func TestBuildProcessForestSurvivesACycle(t *testing.T) {
	flat := []flatProcess{
		{pid: 100, ppid: 200, command: "a"},
		{pid: 200, ppid: 100, command: "b"},
	}
	done := make(chan []Process, 1)
	go func() { done <- buildProcessForest(flat) }()
	select {
	case forest := <-done:
		// Neither ppid (100, 200) is missing from the pid set, so neither
		// process qualifies as a root under this listing — an empty forest
		// is the correct, non-crashing answer for data this malformed.
		if len(forest) != 0 {
			t.Fatalf("forest = %+v, want empty (both processes appear to have a listed parent)", forest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildProcessForest did not return — cycle guard failed")
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
