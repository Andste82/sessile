package session

import (
	"sync"
	"testing"
	"time"
)

// Activity sampling added a second goroutine that touches a live session while
// the read loop is broadcasting into it, and a subscriber set that the sampler
// publishes to while connections come and go. §4.4 calls the concurrency model
// non-negotiable, so this drives every one of those edges at once and is meant
// to be run under -race:
//
//	CGO_ENABLED=1 go test ./internal/session/ -race -run Concurrent -count=5
//
// It asserts nothing beyond "does not deadlock or corrupt". The race detector
// is the assertion; without it this only proves the paths do not panic.
func TestConcurrentSamplingBroadcastAndSubscribers(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}

	mgr, _, _ := testManager(t)

	const sessions = 3
	ids := make([]string, 0, sessions)
	for range sessions {
		info, err := mgr.Create("load", ".", "sh")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		ids = append(ids, info.ID)
		// `yes` keeps the read loop broadcasting continuously, so the scanner,
		// the byte counter and LastActivity are all being written while the
		// sampler reads them.
		//
		// Foreground only, and nothing backgrounded with `&`: a child that
		// outlives the shell keeps the pty slave open, the read loop never sees
		// EOF, and Delete waits on s.exited for as long as the child lives —
		// which turned this test's own cleanup into a ten-minute hang before
		// the load it was written for had a chance to prove anything.
		if err := mgr.WriteInput(info.ID, []byte("yes\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_ = mgr.Delete(id)
		}
	})

	stop := make(chan struct{})
	var wg sync.WaitGroup

	spin := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					f()
				}
			}
		}()
	}

	// The sampler itself, far faster than its real 1 Hz.
	spin(func() { mgr.sampleActivity() })

	// Readers of the same fields, through the paths the API uses.
	spin(func() { _, _ = mgr.List() })
	spin(func() {
		for _, id := range ids {
			_, _ = mgr.Get(id)
		}
	})

	// Subscribers arriving and leaving while the sampler publishes to them.
	spin(func() {
		sub := newFakeSubscriber()
		unsub := mgr.Subscribe(sub)
		unsub()
	})

	// Terminal clients attaching and detaching, which mutates the same session
	// lock the sampler takes and triggers a buffer replay.
	spin(func() {
		for _, id := range ids {
			c := &recordingClient{id: "racer"}
			if _, err := mgr.Attach(id, c); err == nil {
				mgr.Detach(id, c)
			}
		}
	})

	// Input and resize, the two other writers into a live session.
	spin(func() {
		for _, id := range ids {
			_ = mgr.WriteInput(id, []byte(""))
		}
	})
	spin(func() {
		for _, id := range ids {
			c := &recordingClient{id: "sizer"}
			_ = mgr.Resize(id, c, 30, 100)
		}
	})

	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()

	// Everything must still be answering after the load.
	for _, id := range ids {
		if _, err := mgr.Get(id); err != nil {
			t.Errorf("session %s unusable after concurrent load: %v", id, err)
		}
	}
}

// Shutdown races the sampler by construction: it closes the stop channel while
// a sample may be mid-flight, and closeSubscribers empties the set the sampler
// publishes into.
func TestConcurrentShutdownDuringSampling(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}

	mgr, _, _ := testManager(t)
	for range 2 {
		info, err := mgr.Create("load", ".", "sh")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := mgr.WriteInput(info.ID, []byte("yes\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	subs := make([]*fakeSubscriber, 4)
	for i := range subs {
		subs[i] = newFakeSubscriber()
		mgr.Subscribe(subs[i])
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					mgr.sampleActivity()
				}
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	mgr.Shutdown()
	close(stop)
	wg.Wait()

	for i, sub := range subs {
		sub.mu.Lock()
		closed := sub.closed
		sub.mu.Unlock()
		if !closed {
			t.Errorf("subscriber %d was not closed by shutdown", i)
		}
	}
}
