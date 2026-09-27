package tasks

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Heavy work on one host runs a few commands at a time (PROJECT_PLAN.md
// §4.12.5). Four agents each building at full parallelism is how a machine
// ends up swapping, and a swapping machine is slower for all four than a
// queue would have been — including for the tasks that only wanted to read a
// file.
//
// What takes a slot is the agent's own call: it says `weight: "heavy"` on a
// build or a test suite, and sessile defaults the obvious ones. Everything
// else runs at once, because most of what an agent runs costs nothing and
// waiting for it would be the slower arrangement again.
//
// Nothing is ever suspended. A slot is taken before a command starts and
// given back when it ends; a command already running is left alone.

// How long a caller waits for a slot before it is told to come back, and how
// long its place is then held. Short enough that no agent CLI's tool timeout
// is in play (the shortest is codex's 60 s), long enough that the common
// case — a slot frees in a moment — costs no round trip.
const (
	slotWait    = 45 * time.Second
	slotHold    = 5 * time.Minute
	slotTimeout = 2 * time.Hour // a slot a command never gives back
)

// slotQueue is one host's heavy-work queue.
type slotQueue struct {
	mu    sync.Mutex
	limit int
	// wait is how long Acquire blocks before telling the caller to come
	// back; tests set it short.
	wait time.Duration
	// running is the slots in use, by call id.
	running map[string]time.Time
	// waiting is the FIFO of tasks that asked and were told to come back.
	// A task keeps its place, so calling again does not send it to the back.
	waiting []reservation
	// ready is closed and replaced whenever a slot frees, so waiters wake.
	ready chan struct{}
}

type reservation struct {
	taskID string
	since  time.Time
}

// Slots returns the queue for a host, creating it on first use. hostKey is
// the host's id, or "local" for the sessile server itself: the resource is
// the machine, so every task on it shares one queue.
func (s *Service) slots(hostKey string, limit int) *slotQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queues == nil {
		s.queues = map[string]*slotQueue{}
	}
	q := s.queues[hostKey]
	if q == nil {
		q = &slotQueue{limit: limit, wait: slotWait, running: map[string]time.Time{}, ready: make(chan struct{})}
		s.queues[hostKey] = q
	}
	q.mu.Lock()
	q.limit = limit // settings may have changed since the queue was made
	q.mu.Unlock()
	return q
}

// SlotState is what a caller is told when it cannot have a slot yet.
type SlotState struct {
	// Ahead is how many tasks are in front of it.
	Ahead int
	// Running is how many heavy commands are going.
	Running int
	// Oldest is how long the longest-running one has been going.
	Oldest time.Duration
}

// Acquire takes a slot for a heavy command, waiting a short while. It
// returns a release function, or ok=false with the state to report back.
//
// A task that is told to come back keeps its place in the queue for
// slotHold, so an agent that does something else and retries is not sent to
// the back of the line.
func (q *slotQueue) Acquire(ctx context.Context, taskID, callID string) (release func(), state SlotState, ok bool) {
	wait := q.wait
	if wait <= 0 {
		wait = slotWait
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	q.enqueue(taskID)
	for {
		if rel, got := q.try(taskID, callID); got {
			return rel, SlotState{}, true
		}
		q.mu.Lock()
		wake := q.ready
		q.mu.Unlock()
		select {
		case <-wake:
			// A slot freed; go round and see whether it is this task's turn.
		case <-deadline.C:
			// Told to come back: the place stays, so an agent that does
			// something else and retries is not sent to the back.
			return nil, q.state(taskID), false
		case <-ctx.Done():
			// The caller is gone — its session died, or the CLI gave up.
			// Nobody is coming back for this place.
			q.drop(taskID)
			return nil, q.state(taskID), false
		}
	}
}

// enqueue puts a task in the queue if it is not already waiting.
func (q *slotQueue) enqueue(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prune()
	for _, w := range q.waiting {
		if w.taskID == taskID {
			return
		}
	}
	q.waiting = append(q.waiting, reservation{taskID: taskID, since: time.Now()})
}

// try takes a slot if one is free and this task is at the front.
func (q *slotQueue) try(taskID, callID string) (func(), bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.prune()
	if len(q.running) >= q.limit {
		return nil, false
	}
	if len(q.waiting) > 0 && q.waiting[0].taskID != taskID {
		return nil, false
	}
	for i, w := range q.waiting {
		if w.taskID == taskID {
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			break
		}
	}
	q.running[callID] = time.Now()
	var once sync.Once
	return func() { once.Do(func() { q.release(callID) }) }, true
}

func (q *slotQueue) release(callID string) {
	q.mu.Lock()
	delete(q.running, callID)
	close(q.ready)
	q.ready = make(chan struct{})
	q.mu.Unlock()
}

// drop gives up a task's place, for a call that was cancelled.
func (q *slotQueue) drop(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, w := range q.waiting {
		if w.taskID == taskID {
			q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
			return
		}
	}
}

// prune forgets reservations nobody came back for, and slots a command never
// gave back — a session that died mid-build would otherwise hold one for
// ever. Callers hold the lock.
func (q *slotQueue) prune() {
	now := time.Now()
	kept := q.waiting[:0]
	for _, w := range q.waiting {
		if now.Sub(w.since) < slotHold {
			kept = append(kept, w)
		}
	}
	q.waiting = kept
	for id, started := range q.running {
		if now.Sub(started) > slotTimeout {
			delete(q.running, id)
		}
	}
}

// state describes the queue from one task's point of view.
func (q *slotQueue) state(taskID string) SlotState {
	q.mu.Lock()
	defer q.mu.Unlock()
	st := SlotState{Running: len(q.running)}
	for _, w := range q.waiting {
		if w.taskID == taskID {
			break
		}
		st.Ahead++
	}
	now := time.Now()
	for _, started := range q.running {
		if d := now.Sub(started); d > st.Oldest {
			st.Oldest = d
		}
	}
	return st
}

// Slot takes one of a host's heavy-work slots for a command, or reports why
// it could not. Callers release it when the command ends.
//
// hostKey is the task's host id, or "local" for the sessile server: the
// resource is the machine, so every task on it queues together.
func (s *Service) Slot(ctx context.Context, hostKey, taskID, callID string) (func(), SlotState, bool) {
	slots, _ := s.limits()
	return s.slots(hostKey, slots).Acquire(ctx, taskID, callID)
}

// BuildEnv is the parallelism a slot holder may use, as the environment
// every build tool reads. Sessile sets it rather than leaving each agent to
// guess from the host's core count, which is how four tasks end up running
// sixteen compilers each (§4.12.5).
func (s *Service) BuildEnv(cores int, windows bool) [][2]string {
	_, jobs := s.limits()
	if jobs <= 0 {
		jobs = 1
	}
	n := strconv.Itoa(jobs)
	if windows {
		// MSVC's own parallel switch; MSBuild and dotnet take -m on the
		// command line, which sessile does not write for them.
		return [][2]string{{"CL", "/MP" + n}, {"CMAKE_BUILD_PARALLEL_LEVEL", n}, {"CARGO_BUILD_JOBS", n}}
	}
	return [][2]string{
		{"MAKEFLAGS", "-j" + n},
		{"CMAKE_BUILD_PARALLEL_LEVEL", n},
		{"CARGO_BUILD_JOBS", n},
		{"GOFLAGS", "-p=" + n},
	}
}

// limits resolves the operator's settings, with the defaults of §4.12.5.
func (s *Service) limits() (slots, jobs int) {
	if s.Limits != nil {
		slots, jobs = s.Limits()
	}
	if slots <= 0 {
		slots = 2
	}
	if jobs <= 0 {
		jobs = 4
	}
	return slots, jobs
}
