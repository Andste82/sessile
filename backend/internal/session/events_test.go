package session

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSubscriber records what it was sent and can be told to refuse, which is
// how a full write queue looks to the hub.
type fakeSubscriber struct {
	mu       sync.Mutex
	msgs     []any
	full     bool
	closed   bool
	closeArg int
	closedCh chan struct{}
}

func newFakeSubscriber() *fakeSubscriber {
	return &fakeSubscriber{closedCh: make(chan struct{})}
}

func (f *fakeSubscriber) SendControl(v any) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.full {
		return false
	}
	f.msgs = append(f.msgs, v)
	return true
}

func (f *fakeSubscriber) Close(code int, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed, f.closeArg = true, code
	close(f.closedCh)
}

func (f *fakeSubscriber) received() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.msgs...)
}

func (f *fakeSubscriber) setFull(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.full = v
}

// waitFor polls cond until it holds, so tests do not depend on the sampler's
// tick or on goroutine scheduling.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Create, rename and delete each reach a subscriber. This is what makes a
// second browser tab see a new session at once rather than at the next poll.
func TestSubscriberSeesTheSessionLifecycle(t *testing.T) {
	mgr, _, _ := testManager(t)
	sub := newFakeSubscriber()
	unsub := mgr.Subscribe(sub)
	defer unsub()

	info, err := mgr.Create("first", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, "the create message", func() bool {
		for _, m := range sub.received() {
			if s, ok := m.(SessionMsg); ok && s.Session.ID == info.ID && s.Type == "session" {
				return true
			}
		}
		return false
	})

	if _, err := mgr.Rename(info.ID, "second"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	waitFor(t, "the rename message", func() bool {
		for _, m := range sub.received() {
			if s, ok := m.(SessionMsg); ok && s.Session.Name == "second" {
				return true
			}
		}
		return false
	})

	if err := mgr.Delete(info.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	waitFor(t, "the delete message", func() bool {
		for _, m := range sub.received() {
			if g, ok := m.(SessionGoneMsg); ok && g.SessionID == info.ID && g.Type == "sessionGone" {
				return true
			}
		}
		return false
	})
}

// A dashboard that stops reading must not be able to slow down the sampler that
// feeds every other one — the same slow-consumer policy attached terminals get.
func TestPublishDropsAndClosesASlowSubscriber(t *testing.T) {
	mgr, _, _ := testManager(t)
	slow := newFakeSubscriber()
	healthy := newFakeSubscriber()
	defer mgr.Subscribe(slow)()
	defer mgr.Subscribe(healthy)()

	slow.setFull(true)
	mgr.publishGone("some-id")

	select {
	case <-slow.closedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("slow subscriber was never closed")
	}
	if slow.closeArg != closeSlowConsumer {
		t.Errorf("close code %d, want %d", slow.closeArg, closeSlowConsumer)
	}

	// It must also be gone from the set, not merely closed: a second publish
	// would otherwise keep finding it.
	slow.setFull(false)
	mgr.publishGone("another-id")
	if got := len(slow.received()); got != 0 {
		t.Errorf("dropped subscriber still received %d messages", got)
	}
	if len(healthy.received()) != 2 {
		t.Errorf("healthy subscriber got %d messages, want 2", len(healthy.received()))
	}
}

// Unsubscribing must stop delivery, and must be safe to call more than once —
// the ws handler calls it from a defer on a path that can also have returned
// early.
func TestUnsubscribeStopsDeliveryAndIsIdempotent(t *testing.T) {
	mgr, _, _ := testManager(t)
	sub := newFakeSubscriber()
	unsub := mgr.Subscribe(sub)

	mgr.publishGone("before")
	unsub()
	unsub()
	mgr.publishGone("after")

	msgs := sub.received()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want only the one published before unsubscribing", len(msgs))
	}
	if g, ok := msgs[0].(SessionGoneMsg); !ok || g.SessionID != "before" {
		t.Errorf("unexpected message %#v", msgs[0])
	}
}

// The activity sampler publishes only on a real change. Without that, every
// session would produce a message every second whether or not anything moved.
func TestSamplerPublishesOnlyOnChange(t *testing.T) {
	mgr, _, _ := testManager(t)
	info, err := mgr.Create("probe", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Delete(info.ID) })

	// Let the session settle before subscribing, so the transitions of startup
	// are not counted.
	waitFor(t, "the session to settle", func() bool {
		mgr.sampleActivity()
		got, err := mgr.Get(info.ID)
		return err == nil && got.Activity == ActivityIdle
	})

	sub := newFakeSubscriber()
	defer mgr.Subscribe(sub)()

	for range 5 {
		mgr.sampleActivity()
	}
	if got := len(sub.received()); got != 0 {
		t.Errorf("a settled session published %d messages across 5 samples, want 0", got)
	}
}

// A read loop that finishes after its session was deleted must stay quiet.
//
// The two halves of this only meet once both exist: terminate now gives up on a
// shell whose terminal is held open from outside its process group, so the read
// loop can reach its stop announcement long after Delete has already published
// sessionGone. Announcing it there put the deleted session back on every
// dashboard as a stopped entry that nothing would ever remove.
func TestDeletedSessionIsNotRepublishedByItsLateReadLoop(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available")
	}
	mgr, _, _ := testManager(t)

	info, err := mgr.Create("wedged", ".", "sh")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// setsid leaves the shell's process group but keeps the terminal open, so
	// the read loop cannot finish until that child does.
	if err := mgr.WriteInput(info.ID, []byte("setsid sleep 120 &\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	sub := newFakeSubscriber()
	defer mgr.Subscribe(sub)()

	if err := mgr.Delete(info.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	waitFor(t, "the sessionGone message", func() bool {
		for _, m := range sub.received() {
			if g, ok := m.(SessionGoneMsg); ok && g.SessionID == info.ID {
				return true
			}
		}
		return false
	})

	// Release the holder; the read loop now runs its cleanup.
	out, _ := exec.Command("pgrep", "-x", "sleep").Output()
	for _, line := range strings.Fields(string(out)) {
		_ = exec.Command("kill", "-9", line).Run()
	}

	// Nothing more about this session may arrive.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range sub.received() {
			if s, ok := m.(SessionMsg); ok && s.Session.ID == info.ID {
				t.Fatalf("deleted session was republished as %q after sessionGone", s.Session.Status)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
