package session

import "sync"

// Subscriber is a connection listening for session list state on /ws/events
// (PROJECT_PLAN.md §5.1). ws.Client satisfies it, which is the point: the event
// channel gets the one-writer rule, the ping/pong keep-alive and the bounded
// write queue without a second implementation of any of them.
//
// SendControl must be non-blocking and report false when the queue is full, the
// same contract Client makes for terminal bytes.
type Subscriber interface {
	SendControl(v any) bool
	Close(code int, reason string)
}

// Subscribe registers sub for session state changes and returns the function
// that unregisters it, which the caller must always call — a subscriber that
// goes away without unsubscribing would be retried on every publish until its
// queue filled up.
//
// Subscribe deliberately does not take the snapshot. The caller registers
// first and reads the list second (§5.1), which is what makes the sequence
// safe without holding two locks: a change landing in between is enqueued
// ahead of the snapshot, and the snapshot is taken later, so it already
// contains that change. The client sees one redundant update followed by a
// newer full list. Taking the snapshot first would instead lose such a change
// entirely, and taking both under one lock would nest subMu inside the session
// lock in one place and outside it in another.
//
// subMu is therefore never held while acquiring any other lock, which is why
// publish is safe to call from anywhere, including from under m.mu.
func (m *Manager) Subscribe(sub Subscriber) func() {
	m.subMu.Lock()
	if m.subs == nil {
		m.subs = make(map[Subscriber]struct{})
	}
	m.subs[sub] = struct{}{}
	m.subMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.subMu.Lock()
			defer m.subMu.Unlock()
			delete(m.subs, sub)
		})
	}
}

// publishSession tells every subscriber that one session changed.
func (m *Manager) publishSession(i Info) {
	m.publish(SessionMsg{Type: "session", Session: ToJSON(i)})
}

// publishGone tells every subscriber that a session no longer exists.
func (m *Manager) publishGone(id string) {
	m.publish(SessionGoneMsg{Type: "sessionGone", SessionID: id})
}

// publish fans a message out. A subscriber whose queue is full is a slow
// consumer and is dropped and closed, exactly as an attached terminal client
// would be (§4.4) — a dashboard that cannot keep up must not be able to slow
// down the sampler that feeds every other one.
//
// Close runs on its own goroutine because it can block on the write pump, and
// this is called from the foreground sampler's single loop.
func (m *Manager) publish(v any) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for sub := range m.subs {
		if !sub.SendControl(v) {
			delete(m.subs, sub)
			go sub.Close(closeSlowConsumer, "slow consumer")
		}
	}
}
