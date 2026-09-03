package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requireSetsid(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
}

// startEscapedChild runs a process inside the session that leaves the shell's
// process group but keeps the terminal open — the shape of anything that
// daemonises. setsid gives it a new session, so a signal aimed at the shell's
// group cannot reach it, while its stdio still refers to the pty slave and holds
// the master short of EOF.
//
// It returns the child's pid so a test can release the terminal deliberately.
// The child reports its own pid rather than being looked up by name: finding it
// with `pgrep sleep` would make these tests fail whenever anything else on the
// machine happened to be sleeping, and kill that process too.
func startEscapedChild(t *testing.T, mgr *Manager, sessionID string) int {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "escaped.pid")
	// sh records its pid and then execs, so the pid it wrote is the sleep's.
	cmd := "setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 120' &\n"
	if err := mgr.WriteInput(sessionID, []byte(cmd)); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the escaped child never reported its pid")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForReadLoop blocks until a session's read loop has finished. Tests call
// it after releasing the terminal so the loop's cleanup happens inside the test
// rather than racing t.TempDir's removal at the end of it.
func waitForReadLoop(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("the read loop never finished after the terminal was released")
	}
}

// Deleting a session whose terminal is held open from outside must return.
//
// It used to wait on s.exited forever after the SIGKILL, so the HTTP request
// that asked for the delete never came back — and the process that caused it is
// nothing exotic: tmux, screen and every daemon call setsid().
func TestDeleteReturnsWhenAProcessOutlivesTheShell(t *testing.T) {
	requireSetsid(t)
	mgr, store, _ := testManager(t)

	info, err := mgr.CreateLocal("test-user", "wedged", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	escaped := startEscapedChild(t, mgr, info.ID)
	s := mgr.live(info.ID) // captured before Delete removes it from the map

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- mgr.Delete(info.ID, "test-user") }()

	// Two grace periods plus room for scheduling. Without the bound this never
	// fires at all.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
	case <-time.After(4*killGrace + 5*time.Second):
		t.Fatal("Delete never returned: the shell outlived its process group and the wait was unbounded")
	}
	if elapsed := time.Since(start); elapsed < killGrace {
		t.Logf("delete returned in %s (the shell was reaped normally, not the wedged path)", elapsed)
	}

	// The session must really be gone, not merely abandoned mid-delete.
	if _, err := mgr.Get(info.ID, "test-user"); err == nil {
		t.Error("session still listed after delete")
	}
	if _, found, err := store.Get(info.ID); err == nil && found {
		t.Error("session row survived the delete")
	}

	_ = syscall.Kill(escaped, syscall.SIGKILL)
	waitForReadLoop(t, s)
}

// The read loop finishes late in that case, after the files have been removed.
// It must not write a scrollback snapshot then: nothing refers to that id any
// more, so the file would sit in the data directory forever.
func TestDiscardedSessionWritesNoScrollbackWhenItsReadLoopFinishesLate(t *testing.T) {
	requireSetsid(t)
	mgr, _, dataDir := testManager(t)

	info, err := mgr.CreateLocal("test-user", "wedged", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	escaped := startEscapedChild(t, mgr, info.ID)
	// Produce output so there is something a snapshot would be worth writing.
	if err := mgr.WriteInput(info.ID, []byte("echo marker\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := mgr.live(info.ID) // captured before Delete removes it from the map

	if err := mgr.Delete(info.ID, "test-user"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Release the holder, then wait for the loop to run its whole cleanup. That
	// is the moment the snapshot would be written, so asserting afterwards is
	// exact rather than a guess at how long to watch for.
	_ = syscall.Kill(escaped, syscall.SIGKILL)
	waitForReadLoop(t, s)

	snapshot := filepath.Join(dataDir, "scrollback", info.ID+".bin")
	if _, err := os.Stat(snapshot); err == nil {
		t.Fatalf("read loop recreated %s for a deleted session", snapshot)
	}
}

// A shutdown must not be stalled by the same case: the snapshot is already on
// disk, so the session comes back intact, and the process has to be able to
// exit.
func TestShutdownReturnsWhenAProcessOutlivesTheShell(t *testing.T) {
	requireSetsid(t)
	mgr, _, _ := testManager(t)

	info, err := mgr.CreateLocal("test-user", "wedged", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	escaped := startEscapedChild(t, mgr, info.ID)
	// Captured before Shutdown drains the session map.
	s := mgr.live(info.ID)

	done := make(chan struct{})
	go func() { mgr.Shutdown(); close(done) }()

	select {
	case <-done:
	case <-time.After(4*killGrace + 5*time.Second):
		t.Fatal("Shutdown never returned: one wedged session stalled the whole process")
	}

	// Let the read loop finish inside the test rather than during cleanup.
	// Unlike a delete, a shutdown keeps its sessions for a later restart, so the
	// loop does write a scrollback snapshot when it wakes — into a temp
	// directory that t.Cleanup is removing at the same moment. Waiting here is
	// what keeps the two apart.
	_ = syscall.Kill(escaped, syscall.SIGKILL)
	waitForReadLoop(t, s)
}
