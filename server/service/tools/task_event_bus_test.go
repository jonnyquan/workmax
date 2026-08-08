package tools

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// drainChan reads at most one signal from ch with a tight deadline.
// Used to assert "the bus woke us" without hanging the test.
func drainChan(ch <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

func TestTaskEventBus_PublishWakesSubscriber(t *testing.T) {
	bus := newTaskEventBus()
	ch, unsub := bus.Subscribe("task-1")
	defer unsub()

	bus.Publish("task-1")
	if !drainChan(ch, 100*time.Millisecond) {
		t.Fatalf("expected publish to wake subscriber")
	}
}

func TestTaskEventBus_PublishToWrongTaskDoesNothing(t *testing.T) {
	bus := newTaskEventBus()
	ch, unsub := bus.Subscribe("task-1")
	defer unsub()

	bus.Publish("task-2")
	if drainChan(ch, 50*time.Millisecond) {
		t.Fatalf("subscriber to task-1 must not be woken by publish to task-2")
	}
}

func TestTaskEventBus_MultipleSubscribersAllWake(t *testing.T) {
	bus := newTaskEventBus()
	ch1, unsub1 := bus.Subscribe("task-1")
	ch2, unsub2 := bus.Subscribe("task-1")
	defer unsub1()
	defer unsub2()

	bus.Publish("task-1")
	if !drainChan(ch1, 100*time.Millisecond) {
		t.Fatalf("first subscriber should wake")
	}
	if !drainChan(ch2, 100*time.Millisecond) {
		t.Fatalf("second subscriber should wake")
	}
}

func TestTaskEventBus_PublishCoalesces(t *testing.T) {
	// Three back-to-back publishes against a single buffer-1 channel
	// should coalesce into ONE pending signal — consumers don't need
	// per-event accounting because they re-fetch state on each wake.
	bus := newTaskEventBus()
	ch, unsub := bus.Subscribe("task-1")
	defer unsub()

	bus.Publish("task-1")
	bus.Publish("task-1")
	bus.Publish("task-1")

	if !drainChan(ch, 100*time.Millisecond) {
		t.Fatalf("first drain should yield a signal")
	}
	if drainChan(ch, 50*time.Millisecond) {
		t.Fatalf("subsequent drain should NOT yield another signal — buffer-1 coalesces")
	}
}

func TestTaskEventBus_PublishNonBlockingWhenSubscriberFull(t *testing.T) {
	// If a subscriber never drains, Publish must NOT block — the bus's
	// guarantee is that mutation sites can fire-and-forget without
	// caring about consumer liveness. We assert by timing a 100ms cap
	// on a publish that has a full buffer.
	bus := newTaskEventBus()
	_, unsub := bus.Subscribe("task-1")
	defer unsub()

	bus.Publish("task-1") // fills the buffer

	done := make(chan struct{})
	go func() {
		bus.Publish("task-1") // would block if not for the non-blocking send
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("Publish blocked despite full subscriber buffer")
	}
}

func TestTaskEventBus_UnsubscribeRemovesSubscriber(t *testing.T) {
	bus := newTaskEventBus()
	ch, unsub := bus.Subscribe("task-1")
	if got := bus.SubscriberCount("task-1"); got != 1 {
		t.Fatalf("expected 1 subscriber, got %d", got)
	}
	unsub()
	if got := bus.SubscriberCount("task-1"); got != 0 {
		t.Fatalf("expected 0 subscribers after unsubscribe, got %d", got)
	}
	// Publishing to the now-empty key shouldn't blow up
	bus.Publish("task-1")
	// Channel is still readable but won't receive — drain to make sure
	// nothing leaked through.
	if drainChan(ch, 50*time.Millisecond) {
		t.Fatalf("publish after unsubscribe must not deliver")
	}
}

func TestTaskEventBus_UnsubscribeIsIdempotent(t *testing.T) {
	bus := newTaskEventBus()
	_, unsub := bus.Subscribe("task-1")
	unsub()
	unsub() // second call should be a no-op, not panic
	if got := bus.SubscriberCount("task-1"); got != 0 {
		t.Fatalf("expected 0 subscribers, got %d", got)
	}
}

func TestTaskEventBus_EmptyTaskIDYieldsClosedChannel(t *testing.T) {
	// Defensive: empty taskID yields an immediately-closed channel
	// so callers' select{} doesn't block forever on misuse.
	bus := newTaskEventBus()
	ch, unsub := bus.Subscribe("")
	defer unsub()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatalf("expected closed channel for empty taskID")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("expected immediately-readable closed channel")
	}
}

func TestTaskEventBus_ConcurrentSubscribePublish(t *testing.T) {
	// Race-detector smoke: 10 goroutines subscribing/unsubscribing
	// while another publishes constantly. Asserts no deadlock and no
	// data race (the race detector validates the latter when run with
	// `go test -race`).
	bus := newTaskEventBus()
	stop := make(chan struct{})
	var wokenCount int64

	var wg sync.WaitGroup
	for i := 0; i < 10; i += 1 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				ch, unsub := bus.Subscribe("hot-task")
				select {
				case <-ch:
					atomic.AddInt64(&wokenCount, 1)
				case <-time.After(2 * time.Millisecond):
				}
				unsub()
			}
		}()
	}

	publisherStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-publisherStop:
				return
			case <-ticker.C:
				bus.Publish("hot-task")
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(publisherStop)
	close(stop)
	wg.Wait()

	if atomic.LoadInt64(&wokenCount) == 0 {
		t.Fatalf("expected at least one subscriber to be woken")
	}
	if got := bus.SubscriberCount("hot-task"); got != 0 {
		t.Fatalf("expected all subscribers cleaned up, got %d", got)
	}
}

func TestPublishTaskEvent_GlobalBusReachable(t *testing.T) {
	// Smoke test: the package-level shortcuts wire to globalTaskEventBus.
	ch, unsub := SubscribeTaskEvent("global-task")
	defer unsub()
	PublishTaskEvent("global-task")
	if !drainChan(ch, 100*time.Millisecond) {
		t.Fatalf("global bus shortcuts should round-trip")
	}
}
