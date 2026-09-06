package hostops

import (
	"context"
	"testing"
)

// fakeTransport returns a scripted Result from Exec, for testing a Platform
// without a real (or even fake) remote — there's no Windows SSH target
// available to test against yet (§12c M23's note), so windowsPlatform's
// command-building and CSV parsing are unit-tested directly instead.
type fakeTransport struct {
	result Result
	err    error
}

func (f fakeTransport) Exec(context.Context, string) (Result, error) { return f.result, f.err }
func (fakeTransport) Files() FileTransport                           { panic("not used by these tests") }

func TestWindowsProcessTreeParsesCSV(t *testing.T) {
	csv := "\"ProcessId\",\"ParentProcessId\",\"Name\"\r\n" +
		"\"4\",\"0\",\"System\"\r\n" +
		"\"100\",\"4\",\"cmd.exe\"\r\n" +
		"\"200\",\"100\",\"powershell.exe\"\r\n"
	tr := fakeTransport{result: Result{Stdout: []byte(csv), ExitCode: 0}}

	rootPID := 4
	tree, err := NewWindowsPlatform().ProcessTree(context.Background(), tr, &rootPID)
	if err != nil {
		t.Fatalf("ProcessTree: %v", err)
	}
	if len(tree) != 1 || tree[0].PID != 100 || tree[0].Command != "cmd.exe" {
		t.Fatalf("tree = %+v, want one child pid=100 cmd.exe", tree)
	}
	if len(tree[0].Children) != 1 || tree[0].Children[0].PID != 200 {
		t.Fatalf("grandchildren = %+v, want one child pid=200", tree[0].Children)
	}
}

// TestWindowsProcessTreeForestModeFindsRootWithNoLinuxConvention proves
// finding #6's fix: Windows has no "1" (init) equivalent, so rootPID=nil
// (forest mode) is what makes "the whole target" reachable at all — the
// old hardcoded rootPID=1 never matched anything here and the whole
// windowsPlatform implementation was unreachable in practice.
func TestWindowsProcessTreeForestModeFindsRootWithNoLinuxConvention(t *testing.T) {
	csv := "\"ProcessId\",\"ParentProcessId\",\"Name\"\r\n" +
		"\"4\",\"0\",\"System\"\r\n" +
		"\"100\",\"4\",\"cmd.exe\"\r\n"
	tr := fakeTransport{result: Result{Stdout: []byte(csv), ExitCode: 0}}

	forest, err := NewWindowsPlatform().ProcessTree(context.Background(), tr, nil)
	if err != nil {
		t.Fatalf("ProcessTree: %v", err)
	}
	if len(forest) != 1 || forest[0].PID != 4 || len(forest[0].Children) != 1 || forest[0].Children[0].PID != 100 {
		t.Fatalf("forest = %+v, want one root pid=4 with one child pid=100", forest)
	}
}

func TestWindowsProcessTreeSurfacesNonZeroExit(t *testing.T) {
	tr := fakeTransport{result: Result{ExitCode: 1, Stderr: []byte("access denied")}}
	rootPID := 4
	if _, err := NewWindowsPlatform().ProcessTree(context.Background(), tr, &rootPID); err == nil {
		t.Fatal("ProcessTree returned nil error on non-zero exit")
	}
}

// TestParseWindowsProcessCSVDropsIdleProcess covers the real listing shape,
// not a trimmed one: Get-CimInstance Win32_Process is run unfiltered, so
// the System Idle Process (pid 0, ppid 0) is always present. Keeping it
// made buildProcessForest return nothing at all for every Windows target,
// because it turned pid 0 into a "visible parent" for every real
// top-level process — and for itself.
func TestParseWindowsProcessCSVDropsIdleProcess(t *testing.T) {
	const realistic = "\"ProcessId\",\"ParentProcessId\",\"Name\"\r\n" +
		"\"0\",\"0\",\"System Idle Process\"\r\n" +
		"\"4\",\"0\",\"System\"\r\n" +
		"\"108\",\"4\",\"Registry\"\r\n" +
		"\"500\",\"4\",\"smss.exe\"\r\n"

	flat, err := parseWindowsProcessCSV(realistic)
	if err != nil {
		t.Fatalf("parseWindowsProcessCSV: %v", err)
	}
	for _, p := range flat {
		if p.pid == 0 {
			t.Fatalf("pid 0 still present in %+v", flat)
		}
	}
	if len(flat) != 3 {
		t.Fatalf("parsed %d processes, want 3", len(flat))
	}

	// The point of dropping it: the whole-host view is no longer empty.
	forest := buildProcessForest(flat)
	if len(forest) != 1 || forest[0].PID != 4 {
		t.Fatalf("forest = %+v, want a single root pid=4", forest)
	}
}
