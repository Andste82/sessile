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

	tree, err := NewWindowsPlatform().ProcessTree(context.Background(), tr, 4)
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

func TestWindowsProcessTreeSurfacesNonZeroExit(t *testing.T) {
	tr := fakeTransport{result: Result{ExitCode: 1, Stderr: []byte("access denied")}}
	if _, err := NewWindowsPlatform().ProcessTree(context.Background(), tr, 4); err == nil {
		t.Fatal("ProcessTree returned nil error on non-zero exit")
	}
}
