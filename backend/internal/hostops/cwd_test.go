package hostops

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// Cwd asks one fixed question over the session's own transport, so the local
// path is exercisable directly: this process has a pid and a working
// directory, and /proc knows both.
func TestCwdReadsTheWorkingDirectory(t *testing.T) {
	h := NewHostSession(NewLocal(), nil)
	want, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := h.Cwd(context.Background(), os.Getpid())
	if !ok {
		t.Skip("no /proc on this host — nothing to assert")
	}
	if got != want {
		t.Errorf("Cwd = %q, want %q", got, want)
	}
}

// "Unknown" has to stay unknown: a caller that got "/" back for a pid that
// doesn't exist would show the wrong directory with full confidence.
func TestCwdReportsUnknownRatherThanGuessing(t *testing.T) {
	h := NewHostSession(NewLocal(), nil)
	for _, pid := range []int{0, -1, 1 << 30} {
		if got, ok := h.Cwd(context.Background(), pid); ok {
			t.Errorf("Cwd(%s) = %q, ok — want unknown", strconv.Itoa(pid), got)
		}
	}
}
