//go:build desktop

package desktop

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stubProbe lets each test drive probe outcomes deterministically.
type stubProbe struct {
	mu        atomicCounter // counts calls
	nextError atomic.Pointer[error]
}

type atomicCounter struct{ n atomic.Int64 }

func (s *stubProbe) calls() int64 { return s.mu.n.Load() }
func (s *stubProbe) Probe(ctx context.Context) error {
	s.mu.n.Add(1)
	if ptr := s.nextError.Load(); ptr != nil {
		return *ptr
	}
	return nil
}
func (s *stubProbe) setError(err error) {
	if err == nil {
		s.nextError.Store(nil)
		return
	}
	s.nextError.Store(&err)
}

func TestWatcher_FirstProbeFlipsToOnline(t *testing.T) {
	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	sub := w.Subscribe()
	defer w.Unsubscribe(sub)

	// First event from Subscribe is the seed (probing or already-online
	// if the probe completed before Subscribe ran).
	first := <-sub
	if first.State != NetworkStateProbing && first.State != NetworkStateOnline {
		t.Fatalf("first event should be probing or online; got %q", first.State)
	}
	// Next event should land within ~100ms; assert online state.
	select {
	case ev := <-sub:
		if ev.State != NetworkStateOnline {
			t.Errorf("expected online after first probe, got %q", ev.State)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no event after first probe")
	}
}

// waitForProbeCount blocks until probe has been called n times or the
// deadline elapses. Keeps the debounce test deterministic — we
// always know exactly how many probes have completed before asserting
// state, which the timing-sleep approach can't guarantee.
func waitForProbeCount(t *testing.T, p *stubProbe, n int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p.calls() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("probe never reached %d calls within %s (got %d)", n, within, p.calls())
}

func TestWatcher_DebouncesSingleFailure(t *testing.T) {
	probe := &stubProbe{}
	// Long interval so probes happen only when we explicitly wait for
	// them — debounce semantics depend on counting probes, not on
	// wall-clock sleeps.
	w := NewNetworkStateWatcher(probe, 500*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// First probe runs immediately at Start. Wait for it to succeed.
	waitForProbeCount(t, probe, 1, time.Second)
	// Tiny grace so publish() has run after probe returned.
	time.Sleep(10 * time.Millisecond)
	if w.Snapshot().State != NetworkStateOnline {
		t.Fatalf("after 1st probe: got %q, want online", w.Snapshot().State)
	}

	// Inject failure BEFORE next probe fires. Manually advance by
	// shrinking the interval — we can't trigger the loop directly, so
	// rebuild a watcher with a fast interval after seeding state.
	probe.setError(errors.New("transient blip"))
	// Wait for probe #2.
	waitForProbeCount(t, probe, 2, 2*time.Second)
	time.Sleep(10 * time.Millisecond)
	// After exactly 1 failure (probe 2), debounce keeps state online.
	if got := w.Snapshot().State; got != NetworkStateOnline {
		t.Errorf("after 1 failure: got %q, want still online (debounced)", got)
	}

	// Wait for probe #3 (second consecutive failure).
	waitForProbeCount(t, probe, 3, 2*time.Second)
	time.Sleep(10 * time.Millisecond)
	if got := w.Snapshot().State; got != NetworkStateOffline {
		t.Errorf("after 2 failures: got %q, want offline", got)
	}
}

func TestWatcher_OfflineRecoversToOnline(t *testing.T) {
	probe := &stubProbe{}
	probe.setError(errors.New("start offline"))
	w := NewNetworkStateWatcher(probe, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Wait until offline (2 failures).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && w.Snapshot().State != NetworkStateOffline {
		time.Sleep(10 * time.Millisecond)
	}
	if w.Snapshot().State != NetworkStateOffline {
		t.Fatalf("never reached offline; got %q", w.Snapshot().State)
	}

	// Single success flips back to online (no debounce on recovery
	// per cloud-proxy.md §7.2 — single success after offline counts).
	probe.setError(nil)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && w.Snapshot().State != NetworkStateOnline {
		time.Sleep(10 * time.Millisecond)
	}
	if got := w.Snapshot().State; got != NetworkStateOnline {
		t.Errorf("after recovery: got %q, want online", got)
	}
}

func TestWatcher_SubscribersReceiveStateChanges(t *testing.T) {
	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	sub := w.Subscribe()
	defer w.Unsubscribe(sub)

	// Drain "online" event.
	seen := map[NetworkStateKind]bool{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev, ok := <-sub:
			if !ok {
				t.Fatal("subscriber closed prematurely")
			}
			seen[ev.State] = true
			if seen[NetworkStateOnline] {
				goto online
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
online:
	if !seen[NetworkStateOnline] {
		t.Fatal("never received online event")
	}
	// Now induce offline → 2 failures.
	probe.setError(errors.New("down"))
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case ev, ok := <-sub:
			if !ok {
				t.Fatal("subscriber closed prematurely")
			}
			if ev.State == NetworkStateOffline {
				return
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatal("never received offline event after probe failures")
}

func TestWatcher_UnsubscribeClosesChannel(t *testing.T) {
	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	sub := w.Subscribe()
	_ = <-sub // drain initial
	w.Unsubscribe(sub)

	// After unsubscribe, channel should be closed.
	select {
	case _, ok := <-sub:
		if ok {
			// Drain anything pending then expect close.
			select {
			case _, ok2 := <-sub:
				if ok2 {
					t.Error("channel should be closed after unsubscribe")
				}
			case <-time.After(100 * time.Millisecond):
				t.Error("channel never closed")
			}
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("read after unsubscribe blocked")
	}
}

func TestWatcher_CtxCancellationClosesAllSubscribers(t *testing.T) {
	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	sub := w.Subscribe()

	cancel()
	// Channel should close within reasonable time.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, ok := <-sub
		if !ok {
			return
		}
	}
	t.Error("channel not closed after ctx cancel")
}

func TestWatcher_SnapshotInitialIsProbing(t *testing.T) {
	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, time.Hour) // long interval; no start
	if got := w.Snapshot().State; got != NetworkStateProbing {
		t.Errorf("initial state: got %q, want probing", got)
	}
}

func TestNetworkProbeFunc(t *testing.T) {
	called := atomic.Int64{}
	p := NetworkProbeFunc(func(ctx context.Context) error {
		called.Add(1)
		return nil
	})
	if err := p.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called.Load() != 1 {
		t.Errorf("call count: %d", called.Load())
	}
}
