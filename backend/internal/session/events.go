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

// Subscribe registers sub for session state changes owned by userID and
// returns the function that unregisters it, which the caller must always
// call — a subscriber that goes away without unsubscribing would be retried
// on every publish until its queue filled up.
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
func (m *Manager) Subscribe(sub Subscriber, userID string) func() {
	m.subMu.Lock()
	if m.subs == nil {
		m.subs = make(map[Subscriber]string)
	}
	m.subs[sub] = userID
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

// publishSession tells every subscriber that owns i one session changed.
// Strictly owner-scoped — no subscriber ever sees another user's session,
// admins included (§10, §12b M17).
func (m *Manager) publishSession(i Info) {
	m.publish(i.UserID, SessionMsg{Type: "session", Session: ToJSON(i)})
}

// publishGone tells userID's subscribers that session id no longer exists.
func (m *Manager) publishGone(id, userID string) {
	m.publish(userID, SessionGoneMsg{Type: "sessionGone", SessionID: id})
}

// PublishHostop fans a Delete/Copy progress message (§5.2) out to userID's
// /ws/events subscribers — the API layer's hostop orchestration (internal/
// api's ops tracking) calls this directly, since it owns the goroutine that
// runs the operation and knows its progress; Manager only owns the fan-out.
func (m *Manager) PublishHostop(userID string, v any) {
	m.publish(userID, v)
}

// publish fans a message out to subscribers owned by userID. A subscriber
// whose queue is full is a slow consumer and is dropped and closed, exactly
// as an attached terminal client would be (§4.4) — a dashboard that cannot
// keep up must not be able to slow down the sampler that feeds every other
// one.
//
// Close runs on its own goroutine because it can block on the write pump, and
// this is called from the foreground sampler's single loop.
func (m *Manager) publish(userID string, v any) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for sub, subUserID := range m.subs {
		if subUserID != userID {
			continue
		}
		if !sub.SendControl(v) {
			delete(m.subs, sub)
			go sub.Close(closeSlowConsumer, "slow consumer")
		}
	}
}
