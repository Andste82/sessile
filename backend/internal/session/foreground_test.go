package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Andste82/sessile/backend/internal/terminal"
)

// The label is the one place the chain is seen, so its shape is pinned here.
func TestCommandLabel(t *testing.T) {
	tests := []struct {
		name string
		fg   terminal.Foreground
		want string
	}{
		{"nothing known", terminal.Foreground{}, ""},
		{"no chain, only a leader", terminal.Foreground{Name: "claude"}, "claude"},
		{"a program started directly", terminal.Foreground{Name: "claude", Chain: []string{"claude"}}, "claude"},
		{
			"a script and what it runs",
			terminal.Foreground{Name: "bash", Chain: []string{"bash", "ping"}},
			"bash › ping",
		},
		{
			"at the cap, still whole",
			terminal.Foreground{Name: "bash", Chain: []string{"bash", "make", "cc"}},
			"bash › make › cc",
		},
		{
			// Past the cap the ends carry the meaning: what was started, and
			// what is running now.
			"past the cap, ends only",
			terminal.Foreground{Name: "bash", Chain: []string{"bash", "make", "sh", "cc", "cc1plus"}},
			"bash › … › cc1plus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commandLabel(tt.fg); got != tt.want {
				t.Errorf("commandLabel = %q, want %q", got, tt.want)
			}
		})
	}
}

// The sampler runs against a live shell: the session must name the shell it was
// started with as its foreground program.
func TestSampleForegroundReportsARealSession(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.CreateLocal("test-user", "probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID, "test-user") })

	waitForForeground(t, mgr, info.ID, func(i Info) bool { return i.Command == "sh" })
}

// End to end for the chain: a script is not what a session is doing, it is what
// started what the session is doing. Before this, such a session reported
// "bash" and said nothing about the program the user was actually waiting on.
func TestSampleForegroundNamesTheProgramInsideAScript(t *testing.T) {
	mgr, _, _ := testManager(t)
	// Outside the sandbox root on purpose: the path is one the user typed, and
	// §4.5 governs where a session may be *created*, not what it may run.
	script := filepath.Join(t.TempDir(), "work.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 20\n:\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	info, err := mgr.CreateLocal("test-user", "probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID, "test-user") })

	if err := mgr.WriteInput(info.ID, []byte("/bin/sh "+script+"\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	waitForForeground(t, mgr, info.ID, func(i Info) bool { return i.Command == "sh › sleep" })
}

// waitForForeground samples until ok accepts the session's state, and fails the
// test with what it last saw. The sampler is driven by hand rather than by its
// ticker so the test is not racing a one-second interval.
func waitForForeground(t *testing.T, m *Manager, id string, ok func(Info) bool) Info {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last Info
	for {
		m.sampleForeground()
		got, err := m.Get(id, "test-user")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		last = got
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("session never settled: command=%q cwd=%q", last.Command, last.Cwd)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A stopped session must not keep advertising the program it was running.
func TestSampleForegroundClearsAStoppedSession(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.CreateLocal("test-user", "probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.sampleForeground()

	if err := mgr.WriteInput(info.ID, []byte("exit\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mgr.sampleForeground()
		got, err := mgr.Get(info.ID, "test-user")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Status == StatusStopped {
			if got.Command != "" || got.Cwd != "" {
				t.Errorf("stopped session still reports command=%q cwd=%q", got.Command, got.Cwd)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session never stopped")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
