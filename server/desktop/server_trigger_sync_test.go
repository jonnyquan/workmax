//go:build desktop

package desktop

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	desktopsync "server/desktop/sync"
)

func TestHandleTriggerSync_FiresThreadsSyncer(t *testing.T) {
	db := openHistoryTestDB(t)

	// Build a SyncWorker with a job we can observe. PeriodicInterval=0
	// disables auto-ticks so the only fire-source is our explicit
	// Trigger via the HTTP endpoint.
	jobCalled := make(chan struct{}, 1)
	job := func(ctx context.Context) error {
		select {
		case jobCalled <- struct{}{}:
		default:
		}
		return nil
	}
	worker := desktopsync.NewSyncWorker(job, desktopsync.Config{
		PeriodicInterval: 0, // pure trigger-driven for the test
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker.Start(ctx)
	// Drain the startup trigger that Start() fires automatically.
	<-jobCalled

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		ThreadsSyncer:  worker,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	req, _ := http.NewRequest(http.MethodPost, base+"/system/trigger-sync", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// Job should fire promptly via the new trigger.
	select {
	case <-jobCalled:
	case <-time.After(time.Second):
		t.Fatal("ThreadsSyncer.Trigger did not run the job within 1s")
	}
}

func TestHandleTriggerSync_503WhenNoSyncerConfigured(t *testing.T) {
	db := openHistoryTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		// No ThreadsSyncer.
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	req, _ := http.NewRequest(http.MethodPost, base+"/system/trigger-sync", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: %d, want 503", resp.StatusCode)
	}
}

func TestHandleTriggerSync_ExpiredRefreshDoesNotTrigger(t *testing.T) {
	db := openHistoryTestDB(t)
	store := newHistoryTokenStoreWithRefreshExpiry(t, 7, time.Now().UTC().Add(-time.Minute))

	jobCalled := make(chan struct{}, 1)
	job := func(ctx context.Context) error {
		select {
		case jobCalled <- struct{}{}:
		default:
		}
		return nil
	}
	worker := desktopsync.NewSyncWorker(job, desktopsync.Config{
		PeriodicInterval: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker.Start(ctx)
	// Drain the startup tick. After this, any additional call means
	// /system/trigger-sync incorrectly ran with an expired session.
	<-jobCalled

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		ThreadsSyncer:  worker,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	req, _ := http.NewRequest(http.MethodPost, base+"/system/trigger-sync", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: %d, want 401", resp.StatusCode)
	}
	select {
	case <-jobCalled:
		t.Fatal("ThreadsSyncer.Trigger ran despite expired refresh token")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestHandleTriggerSync_UnknownThreadIsSilent pins the silent-skip
// behavior — a ?thread=<uuid> for a UUID we don't have locally
// (or one without cloud_thread_id) should not error the request;
// the thread sync still fires.
func TestHandleTriggerSync_UnknownThreadIsSilent(t *testing.T) {
	db := openHistoryTestDB(t)
	// No thread seeded; the lookup will return "" → skip.

	threadsJobCalled := make(chan struct{}, 1)
	threadsJob := func(ctx context.Context) error {
		select {
		case threadsJobCalled <- struct{}{}:
		default:
		}
		return nil
	}
	threadsWorker := desktopsync.NewSyncWorker(threadsJob, desktopsync.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	threadsWorker.Start(ctx)
	<-threadsJobCalled

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		ThreadsSyncer:  threadsWorker,
		// MessagesSyncer left nil — triggerMessagesSync returns early.
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	req, _ := http.NewRequest(http.MethodPost, base+"/system/trigger-sync?thread=unknown", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status: %d, want 204 (unknown thread must not error the request)", resp.StatusCode)
	}
	// Threads should still have triggered.
	select {
	case <-threadsJobCalled:
	case <-time.After(time.Second):
		t.Fatal("threads worker did not fire within 1s")
	}
}

func TestHandleTriggerSync_RejectsMalformedThreadQueryBeforeTrigger(t *testing.T) {
	db := openHistoryTestDB(t)

	threadsJobCalled := make(chan struct{}, 1)
	threadsJob := func(ctx context.Context) error {
		select {
		case threadsJobCalled <- struct{}{}:
		default:
		}
		return nil
	}
	threadsWorker := desktopsync.NewSyncWorker(threadsJob, desktopsync.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	threadsWorker.Start(ctx)
	<-threadsJobCalled

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		ThreadsSyncer:  threadsWorker,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "empty", query: "thread="},
		{name: "leading space", query: "thread=%20abc"},
		{name: "trailing space", query: "thread=abc%20"},
		{name: "control char", query: "thread=abc%0Adef"},
		{name: "too long", query: "thread=" + strings.Repeat("a", 201)},
		{name: "duplicate", query: "thread=abc&thread=def"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, base+"/system/trigger-sync?"+tc.query, nil)
			req.Header.Set("X-Local-Token", "tok")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status: %d, want 400", resp.StatusCode)
			}
		})
	}

	select {
	case <-threadsJobCalled:
		t.Fatal("threads syncer triggered despite malformed thread query")
	case <-time.After(100 * time.Millisecond):
	}
}
