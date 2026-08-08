//go:build desktop

package desktop

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// NetworkStateKind is the closed enum the sidecar publishes over the
// network_state SSE channel. Lives here (not in cloud_proxy) because
// it's a sidecar-coordinator concept — cloud_proxy's classifier
// publishes 9 fine-grained error kinds; this is the coarse 3-state
// view the renderer needs to drive its banner / input-disabling UI.
//
// Matches cloud-proxy.md §7.1 (state machine) collapsed to what the
// renderer can actually distinguish without auth context: probing /
// online / offline. The auth-related "expired" + "unauthenticated"
// states ride on /auth/status instead — keeping the two SSEs single-
// concern makes each one easier to test in isolation.
type NetworkStateKind string

const (
	NetworkStateProbing NetworkStateKind = "probing" // initial state before first probe completes
	NetworkStateOnline  NetworkStateKind = "online"
	NetworkStateOffline NetworkStateKind = "offline"
)

// NetworkState is the snapshot the SSE channel emits + that
// GET /system/network-state's first event always carries (so a
// late-subscribing renderer sees the current state immediately,
// not just future deltas).
type NetworkState struct {
	State NetworkStateKind `json:"state"`
	// Since is the timestamp the state was entered. Lets the renderer
	// show "offline for 47 seconds" UX without a separate counter.
	Since time.Time `json:"since"`
	// LastProbeAt is the timestamp of the most recent probe attempt
	// (success or failure). Useful for diagnostics ("haven't probed
	// since X" might indicate the watcher died).
	LastProbeAt time.Time `json:"last_probe_at"`
}

// NetworkProbe is the interface NetworkStateWatcher calls to decide
// whether the cloud is reachable. Production hooks a cheap HTTP HEAD
// against the configured cloud base URL; tests substitute a
// controllable stub.
type NetworkProbe interface {
	Probe(ctx context.Context) error
}

// NetworkProbeFunc adapts a plain func to the NetworkProbe interface
// so production wiring + tests can both stay terse.
type NetworkProbeFunc func(ctx context.Context) error

func (f NetworkProbeFunc) Probe(ctx context.Context) error { return f(ctx) }

// HTTPNetworkProbe is the production probe — HEAD on the cloud's
// base URL with a snug 5s timeout. Any 1xx-5xx response (including
// 4xx like 403/404) counts as "online" — we're testing reachability,
// not authentication or specific endpoint existence. Only transport
// errors (DNS / TCP / TLS / timeout) count as "offline".
//
// HEAD chosen over GET so we don't pay bandwidth on every probe.
// Since any HTTP response counts as reachable, even a 405 still
// means "online"; only transport errors mark the cloud offline.
type HTTPNetworkProbe struct {
	BaseURL string
	Client  *http.Client
}

// NewHTTPNetworkProbe wires a probe against the given base URL.
// Caller may inject an http.Client (tests) or accept the default
// 5s-timeout one.
func NewHTTPNetworkProbe(baseURL string) *HTTPNetworkProbe {
	return &HTTPNetworkProbe{
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (p *HTTPNetworkProbe) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, p.BaseURL+"/", nil)
	if err != nil {
		return err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Any HTTP response means the server is reachable.
	return nil
}

// NetworkStateWatcher runs a background loop probing the cloud and
// fans state changes out to all subscribed channels. Safe for
// concurrent Subscribe / Unsubscribe calls.
//
// Lifecycle:
//   - NewNetworkStateWatcher constructs but does NOT start the loop.
//   - Start(ctx) launches the goroutine; cancel ctx to stop it.
//   - Subscribers receive the current state on subscribe + every
//     subsequent change. Channels are buffered (size 8) so a slow
//     consumer can briefly fall behind without blocking the watcher;
//     a consistently slow consumer drops events.
type NetworkStateWatcher struct {
	probe    NetworkProbe
	interval time.Duration

	// stateAtomic holds the current NetworkState as a *NetworkState
	// for racy reads (snapshot). Writes serialized by mu.
	stateAtomic atomic.Pointer[NetworkState]

	// failureRunCount counts back-to-back probe failures. Renderer-
	// facing flip from online → offline happens after 2 consecutive
	// failures (debouncing per cloud-proxy.md §7.2). Single failure
	// is treated as transient.
	failureRunCount int

	mu          sync.Mutex
	subscribers map[chan NetworkState]struct{}
}

// NewNetworkStateWatcher returns a watcher with the given probe and
// probe interval. Production uses 30s; tests pass a faster interval.
// Initial state is probing.
func NewNetworkStateWatcher(probe NetworkProbe, interval time.Duration) *NetworkStateWatcher {
	w := &NetworkStateWatcher{
		probe:       probe,
		interval:    interval,
		subscribers: make(map[chan NetworkState]struct{}),
	}
	now := time.Now().UTC()
	w.stateAtomic.Store(&NetworkState{State: NetworkStateProbing, Since: now})
	return w
}

// Snapshot returns the current state. Safe to call from any goroutine
// without acquiring the watcher's mutex.
func (w *NetworkStateWatcher) Snapshot() NetworkState {
	if ptr := w.stateAtomic.Load(); ptr != nil {
		return *ptr
	}
	return NetworkState{State: NetworkStateProbing, Since: time.Now().UTC()}
}

// Subscribe registers a channel that receives state events. The
// caller MUST call Unsubscribe (typically via defer) when done; the
// channel is closed there. Subscribers receive the current state
// immediately so they don't have to wait for the next change.
//
// Returns the channel for receive-only consumption.
func (w *NetworkStateWatcher) Subscribe() <-chan NetworkState {
	ch := make(chan NetworkState, 8)
	w.mu.Lock()
	w.subscribers[ch] = struct{}{}
	w.mu.Unlock()
	// Push current state non-blockingly (just allocated the channel,
	// won't be full).
	ch <- w.Snapshot()
	return ch
}

// Unsubscribe removes a channel and closes it. Idempotent — safe to
// call on an already-removed channel.
func (w *NetworkStateWatcher) Unsubscribe(ch <-chan NetworkState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Find the writable channel pointer matching the receive-only
	// reference. Subscribers are keyed by writable channels.
	for k := range w.subscribers {
		if (<-chan NetworkState)(k) == ch {
			delete(w.subscribers, k)
			close(k)
			return
		}
	}
}

// Start launches the probe loop. Returns immediately. Ctx cancellation
// stops the loop + closes all subscriber channels.
func (w *NetworkStateWatcher) Start(ctx context.Context) {
	go w.run(ctx)
}

func (w *NetworkStateWatcher) run(ctx context.Context) {
	// Fire one probe immediately so first-event-after-start isn't
	// delayed by the full interval (matters for tests + for renderer
	// startup feel).
	w.runOnce(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.closeAllSubscribers()
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *NetworkStateWatcher) runOnce(ctx context.Context) {
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	err := w.probe.Probe(probeCtx)
	now := time.Now().UTC()
	cur := w.Snapshot()
	cur.LastProbeAt = now

	if err == nil {
		w.failureRunCount = 0
		if cur.State != NetworkStateOnline {
			cur.State = NetworkStateOnline
			cur.Since = now
		}
		w.publish(cur)
		return
	}

	w.failureRunCount++
	// Debounce: flip to offline only after 2 consecutive failures
	// (cloud-proxy.md §7.2). First failure keeps the prior state +
	// updates LastProbeAt only.
	if cur.State == NetworkStateOnline && w.failureRunCount < 2 {
		w.publish(cur) // last_probe_at update without state flip
		return
	}
	if cur.State != NetworkStateOffline {
		cur.State = NetworkStateOffline
		cur.Since = now
	}
	w.publish(cur)
}

// publish atomically stores the new state + fans it to subscribers.
// Skips subscribers whose buffer is full (they're falling behind;
// dropping is preferable to blocking the watcher).
func (w *NetworkStateWatcher) publish(s NetworkState) {
	w.stateAtomic.Store(&s)
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.subscribers {
		select {
		case ch <- s:
		default:
			// Slow consumer; drop. Don't error — the renderer's
			// SSE handler is the only consumer and it'll re-emit
			// from Snapshot() on next event anyway.
		}
	}
}

func (w *NetworkStateWatcher) closeAllSubscribers() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.subscribers {
		close(ch)
		delete(w.subscribers, ch)
	}
}
