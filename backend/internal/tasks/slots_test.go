package tasks

import (
	"context"
	"testing"
	"time"
)

// A host runs a few heavy commands at once; the rest wait their turn and
// keep their place (§4.12.5).
func TestSlotQueue(t *testing.T) {
	q := &slotQueue{limit: 1, running: map[string]time.Time{}, ready: make(chan struct{})}
	ctx := t.Context()

	first, _, ok := q.Acquire(ctx, "task-a", "call-1")
	if !ok {
		t.Fatal("the first heavy command should start at once")
	}

	// The second waits. Cancelling its context is how a caller gives up.
	give, cancel := context.WithCancel(ctx)
	cancel()
	_, state, ok := q.Acquire(give, "task-b", "call-2")
	if ok {
		t.Fatal("a second command must not start while the slot is taken")
	}
	if state.Running != 1 {
		t.Errorf("state = %+v, want one running", state)
	}

	// Once the first gives it back, the next one in gets it.
	first()
	second, _, ok := q.Acquire(ctx, "task-b", "call-3")
	if !ok {
		t.Fatal("the slot should be free once the first command ended")
	}
	second()

	// Releasing twice is harmless: a command ends once, however many paths
	// lead there.
	second()
	if len(q.running) != 0 {
		t.Errorf("running = %v, want empty", q.running)
	}
}

// A task told to come back keeps its place, so retrying does not send it to
// the back of the queue behind whoever asked in the meantime.
func TestSlotQueueKeepsYourPlace(t *testing.T) {
	q := &slotQueue{limit: 1, wait: 10 * time.Millisecond,
		running: map[string]time.Time{}, ready: make(chan struct{})}
	ctx := t.Context()
	held, _, _ := q.Acquire(ctx, "task-a", "call-1")

	// b asks first and is told to come back; c asks while b is away.
	if _, _, ok := q.Acquire(ctx, "task-b", "call-b0"); ok {
		t.Fatal("the slot was taken; b should have been told to wait")
	}
	q.enqueue("task-c")

	held()
	// c must not jump the queue while b is still waiting.
	if _, got := q.try("task-c", "call-c"); got {
		t.Error("a task that arrived later took the slot first")
	}
	if _, got := q.try("task-b", "call-b"); !got {
		t.Error("the task that waited longest should get the slot")
	}
}

// The build parallelism a slot holder gets is the operator's number, in the
// variables each toolchain actually reads.
func TestBuildEnv(t *testing.T) {
	s := &Service{Limits: func() (int, int) { return 2, 6 }}
	got := map[string]string{}
	for _, kv := range s.BuildEnv(16, false) {
		got[kv[0]] = kv[1]
	}
	if got["MAKEFLAGS"] != "-j6" || got["CMAKE_BUILD_PARALLEL_LEVEL"] != "6" ||
		got["CARGO_BUILD_JOBS"] != "6" || got["GOFLAGS"] != "-p=6" {
		t.Errorf("posix build env = %v", got)
	}
	win := map[string]string{}
	for _, kv := range s.BuildEnv(16, true) {
		win[kv[0]] = kv[1]
	}
	if win["CL"] != "/MP6" || win["MAKEFLAGS"] != "" {
		t.Errorf("windows build env = %v", win)
	}
}
