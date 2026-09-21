package mcp

import (
	"context"
	"sync"
	"time"

	"github.com/Andste82/sessile/backend/internal/tasks"
)

// The per-user event ring (§4.18.2). The orchestrator watches its user's
// tasks through wait_for_events rather than polling; nothing is ever pushed
// into its terminal.

// ringSize is how much an orchestrator that was away can catch up on.
const ringSize = 200

// Event is one thing that happened to a user's tasks.
type Event struct {
	Seq       uint64    `json:"seq"`
	At        time.Time `json:"at"`
	Type      string    `json:"type"` // taskState | taskSummary | taskExited | taskCreated | approvalPending
	TaskID    string    `json:"taskId,omitempty"`
	SessionID string    `json:"sessionId,omitempty"`
	Name      string    `json:"name,omitempty"`
	State     string    `json:"state,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Question  string    `json:"question,omitempty"`
}

// eventQueue is one user's ring plus the waiters on it.
type eventQueue struct {
	mu     sync.Mutex
	next   uint64
	events []Event
	wake   chan struct{}
}

func newEventQueue() *eventQueue {
	return &eventQueue{next: 1, wake: make(chan struct{})}
}

func (q *eventQueue) push(e Event) {
	q.mu.Lock()
	q.next++
	e.Seq = q.next - 1
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	q.events = append(q.events, e)
	if len(q.events) > ringSize {
		q.events = append(q.events[:0], q.events[len(q.events)-ringSize:]...)
	}
	// Wake every waiter at once: a closed channel replaced under the lock.
	close(q.wake)
	q.wake = make(chan struct{})
	q.mu.Unlock()
}

// since returns the events after a cursor and the cursor to continue from.
// A nil cursor means "from now": a first call gets the current position, not
// a replay of the whole ring.
func (q *eventQueue) since(cursor *uint64) ([]Event, uint64, chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if cursor == nil {
		return nil, q.next - 1, q.wake
	}
	out := []Event{}
	for _, e := range q.events {
		if e.Seq > *cursor {
			out = append(out, e)
		}
	}
	return out, q.next - 1, q.wake
}

// wait returns the events after cursor, blocking up to timeout for the first
// one. A first call (cursor nil) returns immediately with the current
// position, so the orchestrator doesn't wait out a timeout before it has a
// cursor.
func (q *eventQueue) wait(ctx context.Context, cursor *uint64, timeout time.Duration) ([]Event, uint64) {
	events, at, wake := q.since(cursor)
	if len(events) > 0 || cursor == nil {
		return events, at
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-wake:
			events, at, wake = q.since(cursor)
			if len(events) > 0 {
				return events, at
			}
		case <-timer.C:
			return nil, at
		case <-ctx.Done():
			return nil, at
		}
	}
}

// queue returns a user's ring, creating it on first use.
func (s *Server) queue(userID string) *eventQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = map[string]*eventQueue{}
	}
	q := s.events[userID]
	if q == nil {
		q = newEventQueue()
		s.events[userID] = q
	}
	return q
}

// pushEvent records an event for a user's orchestrator.
func (s *Server) pushEvent(userID string, e Event) {
	s.queue(userID).push(e)
}

// TaskExited records that a task's session stopped (§4.18.2). Called by the
// session manager's task hook — the orchestrator is how the user hears that
// an agent finished. Its own exit is not an event: nothing is listening then.
func (s *Server) TaskExited(userID, taskID, name string) {
	if t, err := s.Tasks.Get(userID, taskID); err == nil && t.Kind == tasks.KindOrchestrator {
		return
	}
	s.pushEvent(userID, Event{Type: "taskExited", TaskID: taskID, Name: name})
}

// TaskCreated records a task the user started. The orchestrator's own
// session is not one of them: it is the reader of these events, not a task
// it watches.
func (s *Server) TaskCreated(userID, taskID, sessionID, name string) {
	if t, err := s.Tasks.Get(userID, taskID); err == nil && t.Kind == tasks.KindOrchestrator {
		return
	}
	s.pushEvent(userID, Event{Type: "taskCreated", TaskID: taskID, SessionID: sessionID, Name: name})
}
