package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// escapeCmd starts a child that leaves the shell's process group but keeps the
// terminal open — the shape of anything that daemonises. setsid gives it a new
// session, so a signal aimed at the shell's group cannot reach it, while its
// stdin/stdout/stderr still refer to the pty slave and hold the master short of
// EOF.
const escapeCmd = "setsid sleep 120 &\n"

func requireSetsid(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
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

	info, err := mgr.Create("wedged", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.WriteInput(info.ID, []byte(escapeCmd)); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let the child reach setsid

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- mgr.Delete(info.ID) }()

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
	if _, err := mgr.Get(info.ID); err == nil {
		t.Error("session still listed after delete")
	}
	if _, found, err := store.Get(info.ID); err == nil && found {
		t.Error("session row survived the delete")
	}
}

// The read loop finishes late in that case, after the files have been removed.
// It must not write a scrollback snapshot then: nothing refers to that id any
// more, so the file would sit in the data directory forever.
func TestDiscardedSessionWritesNoScrollbackWhenItsReadLoopFinishesLate(t *testing.T) {
	requireSetsid(t)
	mgr, _, dataDir := testManager(t)

	info, err := mgr.Create("wedged", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.WriteInput(info.ID, []byte(escapeCmd)); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Produce output so there is something a snapshot would be worth writing.
	if err := mgr.WriteInput(info.ID, []byte("echo marker\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	if err := mgr.Delete(info.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Release the holder; the read loop now sees EOF and runs its cleanup.
	for _, pid := range sleepPIDs(t) {
		_ = exec.Command("kill", "-9", itoa(pid)).Run()
	}

	snapshot := filepath.Join(dataDir, "scrollback", info.ID+".bin")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(snapshot); err == nil {
			t.Fatalf("read loop recreated %s for a deleted session", snapshot)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A shutdown must not be stalled by the same case: the snapshot is already on
// disk, so the session comes back intact, and the process has to be able to
// exit.
func TestShutdownReturnsWhenAProcessOutlivesTheShell(t *testing.T) {
	requireSetsid(t)
	mgr, _, _ := testManager(t)

	info, err := mgr.Create("wedged", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.WriteInput(info.ID, []byte(escapeCmd)); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	t.Cleanup(func() {
		for _, pid := range sleepPIDs(t) {
			_ = exec.Command("kill", "-9", itoa(pid)).Run()
		}
	})

	done := make(chan struct{})
	go func() { mgr.Shutdown(); close(done) }()

	select {
	case <-done:
	case <-time.After(4*killGrace + 5*time.Second):
		t.Fatal("Shutdown never returned: one wedged session stalled the whole process")
	}
	_ = info
}

// sleepPIDs finds the escaped children this file starts, so a test can release
// the terminal on purpose.
func sleepPIDs(t *testing.T) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-x", "sleep").Output()
	if err != nil {
		return nil // pgrep exits non-zero when nothing matches
	}
	var pids []int
	cur, has := 0, false
	for _, b := range out {
		if b >= '0' && b <= '9' {
			cur, has = cur*10+int(b-'0'), true
			continue
		}
		if has {
			pids = append(pids, cur)
		}
		cur, has = 0, false
	}
	if has {
		pids = append(pids, cur)
	}
	return pids
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
