package tools

import (
	"sync"
)

// TaskEventBus is a per-process notification primitive that lets
// long-poll handlers learn about task state changes without a tight
// DB-poll. Subscribers register a buffered signal channel keyed by
// taskID; mutation sites in the queue (UpdateTaskProgress,
// updateTaskStatus, CancelTask, etc.) call Publish to wake the
// channel.
//
// Coalescing semantics: each subscriber's channel has buffer 1, and
// Publish does a non-blocking send. Multiple publishes between drains
// collapse to one signal, which is fine because consumers re-read the
// task from DB after every wake — they always observe the latest state.
//
// Multi-process note: this is in-memory. A horizontally-scaled
// deployment with multiple API replicas would need a Redis/NATS
// equivalent. At our current scale (single Go process) this is the
// right level of investment; the public API is shaped so a Redis-backed
// implementation could replace it without touching callers.
type TaskEventBus struct {
	mu          sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func newTaskEventBus() *TaskEventBus {
	return &TaskEventBus{
		subscribers: make(map[string]map[chan struct{}]struct{}),
	}
}

// Subscribe returns a buffered receive-only signal channel and a
// cleanup function. Callers MUST defer the cleanup; otherwise a
// long-lived task with many transient subscribers would leak channels
// into the bus's map. The returned channel may already have a queued
// signal if a publish raced with subscribe — that's intentional, the
// caller does an initial DB read after subscribing to catch up.
func (b *TaskEventBus) Subscribe(taskID string) (<-chan struct{}, func()) {
	if taskID == "" {
		// Subscribers without a key get a closed channel — they'll
		// drain immediately and fall through to the deadline path.
		// This is safer than panicking on a misuse.
		ch := make(chan struct{})
		close(ch)
		return ch, func() {}
	}
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	set, ok := b.subscribers[taskID]
	if !ok {
		set = make(map[chan struct{}]struct{})
		b.subscribers[taskID] = set
	}
	set[ch] = struct{}{}
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		set, ok := b.subscribers[taskID]
		if !ok {
			return
		}
		delete(set, ch)
		if len(set) == 0 {
			delete(b.subscribers, taskID)
		}
	}
	return ch, unsubscribe
}

// Publish wakes every subscriber waiting on taskID. Non-blocking — if a
// subscriber's channel buffer is full (a prior publish hasn't been
// drained yet) the send is dropped, because the subscriber will see
// fresh state on its next DB re-read regardless of how many wakes it
// missed.
func (b *TaskEventBus) Publish(taskID string) {
	if taskID == "" {
		return
	}
	b.mu.Lock()
	set := b.subscribers[taskID]
	// Snapshot the channels under lock so Publish doesn't hold the
	// mutex during the non-blocking sends — keeps the lock window
	// tight even with many subscribers per task.
	channels := make([]chan struct{}, 0, len(set))
	for ch := range set {
		channels = append(channels, ch)
	}
	b.mu.Unlock()
	for _, ch := range channels {
		select {
		case ch <- struct{}{}:
		default:
			// Buffer full — coalesced into existing pending wake.
		}
	}
}

// SubscriberCount is exported for tests / debugging only. Returns how
// many active subscribers are registered for the given taskID.
func (b *TaskEventBus) SubscriberCount(taskID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers[taskID])
}

// Global singleton used by the queue + handlers. Tests construct their
// own bus instances via newTaskEventBus() to avoid cross-test pollution.
var globalTaskEventBus = newTaskEventBus()

// PublishTaskEvent is a package-level shorthand for the global bus.
// Mutation sites in the queue call this after writing to the task row.
func PublishTaskEvent(taskID string) {
	globalTaskEventBus.Publish(taskID)
}

// SubscribeTaskEvent is a package-level shorthand for handlers that
// want to wait on the global bus.
func SubscribeTaskEvent(taskID string) (<-chan struct{}, func()) {
	return globalTaskEventBus.Subscribe(taskID)
}
