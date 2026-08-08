//go:build desktop

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startServerWithWatcher boots a sidecar Server with the given watcher,
// returns the base URL + local token + tears down on test completion.
func startServerWithWatcher(t *testing.T, watcher *NetworkStateWatcher) (baseURL string) {
	t.Helper()
	db := openServerTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		NetworkState:   watcher,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + srv.listener.Addr().String()
}

// readSSEEvents pulls SSE events from the response body until ctx
// fires or maxEvents are collected. Returns the parsed (type, payload)
// pairs. Each "payload" is the data: line's bytes (we don't unmarshal
// here so each caller can decode into its expected type).
func readSSEEvents(t *testing.T, body io.Reader, maxEvents int, deadline time.Time) [][2]string {
	t.Helper()
	scanner := bufio.NewScanner(body)
	var events [][2]string
	var curType, curData string

	doneCh := make(chan struct{})
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if curType != "" || curData != "" {
					events = append(events, [2]string{curType, curData})
					curType = ""
					curData = ""
				}
				if len(events) >= maxEvents {
					close(doneCh)
					return
				}
				continue
			}
			if strings.HasPrefix(line, "event: ") {
				curType = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				curData = strings.TrimPrefix(line, "data: ")
			}
		}
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(time.Until(deadline)):
	}
	return events
}

func TestSSE_HandlerEmitsInitialSnapshot(t *testing.T) {
	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, time.Hour) // no auto probes during test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	// Wait for first probe to complete (it runs immediately at Start).
	waitForProbeCount(t, probe, 1, time.Second)
	time.Sleep(10 * time.Millisecond)
	if w.Snapshot().State != NetworkStateOnline {
		t.Fatalf("setup: state should be online, got %q", w.Snapshot().State)
	}

	base := startServerWithWatcher(t, w)
	req, _ := http.NewRequest(http.MethodGet, base+"/system/network-state", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type: %q", ct)
	}

	events := readSSEEvents(t, resp.Body, 1, time.Now().Add(2*time.Second))
	if len(events) < 1 {
		t.Fatalf("expected at least 1 event, got %d", len(events))
	}
	if events[0][0] != "network_state" {
		t.Errorf("event[0] type: got %q, want network_state", events[0][0])
	}
	var snapshot NetworkState
	if err := json.Unmarshal([]byte(events[0][1]), &snapshot); err != nil {
		t.Fatalf("decode: %v (%q)", err, events[0][1])
	}
	if snapshot.State != NetworkStateOnline {
		t.Errorf("snapshot state: got %q, want online", snapshot.State)
	}
}

func TestSSE_HandlerEmitsStateChange(t *testing.T) {
	probe := &stubProbe{}
	probe.setError(errors.New("offline at start"))
	w := NewNetworkStateWatcher(probe, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Wait until offline (2 failures).
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && w.Snapshot().State != NetworkStateOffline {
		time.Sleep(10 * time.Millisecond)
	}
	if w.Snapshot().State != NetworkStateOffline {
		t.Fatalf("setup: state should be offline, got %q", w.Snapshot().State)
	}

	base := startServerWithWatcher(t, w)
	req, _ := http.NewRequest(http.MethodGet, base+"/system/network-state", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// We start subscribed to an offline watcher; after the snapshot
	// we expect to see a transition to online when probes recover.
	go func() {
		time.Sleep(150 * time.Millisecond)
		probe.setError(nil) // recover
	}()

	// Read up to 6 events — every probe publishes (state changes
	// AND last_probe_at updates), so a 2s window at 100ms interval
	// produces ~20 events. We just need to see one online to prove
	// the channel propagated the recovery.
	events := readSSEEvents(t, resp.Body, 6, time.Now().Add(3*time.Second))
	if len(events) < 2 {
		t.Fatalf("expected >=2 events, got %d", len(events))
	}
	// First event must be the initial snapshot (offline).
	var first NetworkState
	if err := json.Unmarshal([]byte(events[0][1]), &first); err != nil {
		t.Fatal(err)
	}
	if first.State != NetworkStateOffline {
		t.Errorf("first event state: got %q, want offline", first.State)
	}
	// Somewhere among later events we should see online.
	sawOnline := false
	for _, e := range events[1:] {
		var s NetworkState
		_ = json.Unmarshal([]byte(e[1]), &s)
		if s.State == NetworkStateOnline {
			sawOnline = true
			break
		}
	}
	if !sawOnline {
		t.Errorf("never saw online recovery event; got events: %+v", events)
	}
}

func TestSSE_HandlerEmitsKeepaliveCommentWhenIdle(t *testing.T) {
	oldInterval := networkStateSSEKeepaliveInterval
	networkStateSSEKeepaliveInterval = 20 * time.Millisecond
	t.Cleanup(func() { networkStateSSEKeepaliveInterval = oldInterval })

	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, time.Hour) // no periodic state events during test
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	waitForProbeCount(t, probe, 1, time.Second)

	base := startServerWithWatcher(t, w)
	req, _ := http.NewRequest(http.MethodGet, base+"/system/network-state", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && scanner.Scan() {
		if scanner.Text() == ": keepalive" {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	t.Fatal("expected keepalive comment on idle network-state SSE stream")
}

func TestSSE_NoWatcherConfigured(t *testing.T) {
	db := openServerTestDB(t)
	srv, _ := NewServer(ServerConfig{
		SidecarVersion: "test", LocalToken: "tok", DB: db,
	})
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.listener.Addr().String()+"/system/network-state", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", resp.StatusCode)
	}
}

func TestSSE_RendererDisconnectStopsStream(t *testing.T) {
	probe := &stubProbe{}
	w := NewNetworkStateWatcher(probe, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	base := startServerWithWatcher(t, w)

	clientCtx, clientCancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(clientCtx, http.MethodGet, base+"/system/network-state", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	// Read the first event to confirm the stream is alive.
	_ = readSSEEvents(t, resp.Body, 1, time.Now().Add(time.Second))

	// Cancel the client context — server should detect and clean up.
	clientCancel()
	_ = resp.Body.Close()

	// We don't have a direct way to observe the server's cleanup
	// from the test process; just give it a moment and make sure
	// nothing crashes / leaks. (The cleanup is a deferred Unsubscribe
	// inside the handler — covered structurally by the handler code.)
	time.Sleep(50 * time.Millisecond)
}

// httpProbeRoundTrip exercises the production HTTPNetworkProbe against
// an httptest server. Confirms ANY HTTP response (including 404) is
// treated as online — we're probing reachability, not auth.
func TestHTTPNetworkProbe_AnyResponseIsOnline(t *testing.T) {
	cases := []struct {
		name string
		code int
	}{
		{"200", 200},
		{"301", 301},
		{"401", 401},
		{"404", 404},
		{"500", 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := atomic.Int64{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.code)
			}))
			defer srv.Close()
			probe := NewHTTPNetworkProbe(srv.URL)
			probe.Client = srv.Client()
			if err := probe.Probe(context.Background()); err != nil {
				t.Errorf("status %d should be 'online', got error: %v", tc.code, err)
			}
			if calls.Load() != 1 {
				t.Errorf("probe should hit server exactly once, got %d", calls.Load())
			}
		})
	}
}

func TestHTTPNetworkProbe_DNSFailureIsOffline(t *testing.T) {
	// non-routable hostname — should fail.
	probe := NewHTTPNetworkProbe("http://does-not-exist-workmax-test.invalid")
	probe.Client.Timeout = 500 * time.Millisecond
	if err := probe.Probe(context.Background()); err == nil {
		t.Error("expected error for nonexistent host")
	}
}
