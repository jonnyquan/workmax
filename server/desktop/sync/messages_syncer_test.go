//go:build desktop

package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	cloudproxy "server/desktop/cloud_proxy"
)

// newSyncerFixture parallels the messagesJobFixture but exposes
// a MessagesSyncer instead of a raw JobFunc.
func newSyncerFixture(t *testing.T, handler http.HandlerFunc) (*MessagesSyncer, *gorm.DB, *atomic.Int64, *httptest.Server) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "syncer.db")),
		&gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE, name TEXT NOT NULL DEFAULT '',
		updated_at TEXT, created_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE, thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT, ai_text TEXT, chat_mode TEXT NOT NULL DEFAULT '',
		content_type TEXT, structured_content TEXT, actions TEXT, metadata TEXT,
		use_images TEXT, use_files TEXT,
		user_rating INTEGER NOT NULL DEFAULT 0, user_feedback TEXT,
		streaming_state TEXT NOT NULL DEFAULT 'complete',
		created_at TEXT, updated_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY, value TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (?, ?, ?)`,
		42, "thr-target", "T",
	).Error; err != nil {
		t.Fatal(err)
	}

	requestCount := atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)

	tokenStore := cloudproxy.NewTokenStore(newMemKeychainForJob())
	if err := tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()

	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB:          db,
		Cloud:       cloud,
		TokenStore:  tokenStore,
		CursorStore: NewCursorStore(db),
	})
	return syncer, db, &requestCount, upstream
}

// waitForRequests blocks until the upstream has received >= n
// requests OR deadline expires. Fails the test on timeout.
func waitForRequests(t *testing.T, c *atomic.Int64, n int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if c.Load() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("upstream received %d requests, want %d within %s", c.Load(), n, within)
}

// waitForActiveCount blocks until syncer reports active == n.
func waitForActiveCount(t *testing.T, s *MessagesSyncer, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if s.ActiveCount() == n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("active count never reached %d (got %d) within %s", n, s.ActiveCount(), within)
}

func TestMessagesSyncer_TriggerStartsBackgroundSync(t *testing.T) {
	syncer, db, requestCount, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"items": [{"action":"upsert","uuid":"m-1","thread_uuid":"thr-target",
			           "user_text":"hi","ai_text":"ok","chat_mode":"ppt",
			           "updated_at":"2026-05-17T22:00:00Z"}],
			"next_cursor":"c","has_more":false,"server_time":"now"
		}`)
	})

	started := syncer.Trigger("thr-target", 1, 42)
	if !started {
		t.Fatal("Trigger should report started")
	}
	waitForRequests(t, requestCount, 1, 2*time.Second)
	// Wait for the goroutine to finish.
	waitForActiveCount(t, syncer, 0, time.Second)

	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row synced, got %d", count)
	}
}

func TestMessagesSyncer_FreezesExactLeaseBeforeGoroutineAcquire(t *testing.T) {
	syncer, db, requestCount, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("replacement session must not reach cloud: %s %s", r.Method, r.URL.Path)
	})
	jobEntered := make(chan struct{})
	releaseAcquire := make(chan struct{})
	frozenEpoch := make(chan uint64, 1)
	syncer.jobForTest = func(
		threadUUID string,
		cloudThreadID, expectedUID uint64,
		lease cloudproxy.SessionLease,
	) func(context.Context) error {
		return func(ctx context.Context) error {
			frozenEpoch <- lease.Epoch()
			close(jobEntered)
			<-releaseAcquire
			return NewMessagesJob(MessagesJobDeps{
				DB:            syncer.deps.DB,
				Cloud:         syncer.deps.Cloud,
				TokenStore:    syncer.deps.TokenStore,
				CursorStore:   syncer.deps.CursorStore,
				ThreadUUID:    threadUUID,
				CloudThreadID: cloudThreadID,
				ExpectedUID:   expectedUID,
				ExpectedLease: lease,
			})(context.WithoutCancel(ctx))
		}
	}

	if !syncer.Trigger("thr-target", 1, 42) {
		t.Fatal("Trigger should start")
	}
	select {
	case <-jobEntered:
	case <-time.After(time.Second):
		t.Fatal("messages job did not reach pre-Acquire gate")
	}
	oldEpoch := <-frozenEpoch
	// A same-UID login changes after Trigger but before the goroutine's first
	// Acquire. context.WithoutCancel above deliberately simulates the narrow
	// window before context.AfterFunc delivers cancellation: exact lease
	// identity, not UID or async cancellation timing, must stop the old job.
	if err := syncer.deps.TokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "same-uid-replacement-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("replace session: %v", err)
	}
	currentLease, err := syncer.deps.TokenStore.AcquireSessionLease()
	if err != nil {
		t.Fatalf("acquire replacement lease: %v", err)
	}
	if currentLease.Epoch() == oldEpoch {
		t.Fatalf("same-UID login preserved epoch %d, want a new epoch", oldEpoch)
	}
	close(releaseAcquire)
	waitForActiveCount(t, syncer, 0, time.Second)

	if got := requestCount.Load(); got != 0 {
		t.Fatalf("cloud requests = %d, want 0", got)
	}
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&rows); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if rows != 0 {
		t.Fatalf("message rows = %d, want 0", rows)
	}
	cursor, err := syncer.deps.CursorStore.Get(CursorKeyMessagesPrefix + "thr-target")
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != "" {
		t.Fatalf("cursor = %q, want empty", cursor)
	}
}

func TestMessagesSyncer_CoalescesConcurrentTriggers(t *testing.T) {
	// Block the cloud handler until we explicitly release it, then
	// fire 10 triggers for the same thread. Only the first should
	// actually start a sync.
	gate := make(chan struct{})
	syncer, _, requestCount, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		<-gate // block until released
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})

	startedFirst := syncer.Trigger("thr-target", 1, 42)
	if !startedFirst {
		t.Fatal("first Trigger should start")
	}
	// Wait for the goroutine to actually be in flight.
	waitForRequests(t, requestCount, 1, time.Second)

	// Fire 10 more triggers — all coalesce away.
	startedCount := 0
	for i := 0; i < 10; i++ {
		if syncer.Trigger("thr-target", 1, 42) {
			startedCount++
		}
	}
	if startedCount != 0 {
		t.Errorf("expected 0 additional starts (all coalesced), got %d", startedCount)
	}

	// Release the gate; goroutine finishes; active count drops.
	close(gate)
	waitForActiveCount(t, syncer, 0, 2*time.Second)

	// Total triggered (started a sync) should be 1.
	if got := syncer.TotalTriggered(); got != 1 {
		t.Errorf("TotalTriggered: %d, want 1", got)
	}
	// Only 1 HTTP request despite 11 Trigger calls.
	if got := requestCount.Load(); got != 1 {
		t.Errorf("upstream requests: %d, want 1 (10 coalesced)", got)
	}
}

func TestMessagesSyncer_PerThreadCoalesce(t *testing.T) {
	// Two different threads CAN sync concurrently — coalesce is
	// per-thread, not global.
	gate := make(chan struct{})
	requests := atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-gate
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	}))
	t.Cleanup(upstream.Close)

	db, _ := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "two.db")), &gorm.Config{Logger: gormlogger.Discard})
	for _, ddl := range []string{
		`CREATE TABLE w_workagent_thread (id INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER, uuid TEXT NOT NULL UNIQUE, name TEXT, updated_at TEXT, created_at TEXT)`,
		`CREATE TABLE w_workagent_message (id INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER, uuid TEXT NOT NULL UNIQUE, thread_id INTEGER, user_text TEXT, ai_text TEXT, chat_mode TEXT, content_type TEXT, structured_content TEXT, actions TEXT, metadata TEXT, use_images TEXT, use_files TEXT, user_rating INTEGER, user_feedback TEXT, streaming_state TEXT DEFAULT 'complete', created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE _local_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (42, 'thr-a', 'A'), (42, 'thr-b', 'B')`)

	tokenStore := cloudproxy.NewTokenStore(newMemKeychainForJob())
	tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "r",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	})
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: db, Cloud: cloud, TokenStore: tokenStore, CursorStore: NewCursorStore(db),
	})

	// Trigger two different threads.
	if !syncer.Trigger("thr-a", 1, 42) {
		t.Fatal("thr-a should start")
	}
	if !syncer.Trigger("thr-b", 2, 42) {
		t.Fatal("thr-b should start (different thread)")
	}
	waitForRequests(t, &requests, 2, 2*time.Second)
	if got := syncer.ActiveCount(); got != 2 {
		t.Errorf("ActiveCount: %d, want 2 (two threads syncing concurrently)", got)
	}
	close(gate)
	waitForActiveCount(t, syncer, 0, 2*time.Second)
}

func TestMessagesSyncer_RetriggerAfterCompletion(t *testing.T) {
	// After a sync completes, the same thread can be triggered again.
	syncer, _, requestCount, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})

	syncer.Trigger("thr-target", 1, 42)
	waitForRequests(t, requestCount, 1, time.Second)
	waitForActiveCount(t, syncer, 0, time.Second)

	if !syncer.Trigger("thr-target", 1, 42) {
		t.Error("re-trigger after completion should start")
	}
	waitForRequests(t, requestCount, 2, time.Second)
	waitForActiveCount(t, syncer, 0, time.Second)
}

func TestMessagesSyncer_RejectsEmptyArgs(t *testing.T) {
	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	if syncer.Trigger("", 1, 42) {
		t.Error("empty uuid should not start")
	}
	if syncer.Trigger("thr", 0, 42) {
		t.Error("zero cloud_thread_id should not start")
	}
	if syncer.Trigger("thr", 1, 0) {
		t.Error("zero expected uid should not start")
	}
}

func TestMessagesSyncer_TriggerForSessionRejectsForeignStoreWithSameEpoch(t *testing.T) {
	syncer, _, requestCount, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("foreign required lease must not reach cloud: %s %s", r.Method, r.URL.Path)
	})
	foreignStore := cloudproxy.NewTokenStore(newMemKeychainForJob())
	if err := foreignStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "foreign-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed foreign store: %v", err)
	}
	foreignLease, err := foreignStore.AcquireSessionLease()
	if err != nil {
		t.Fatalf("acquire foreign lease: %v", err)
	}
	localLease, err := syncer.deps.TokenStore.AcquireSessionLease()
	if err != nil {
		t.Fatalf("acquire local lease: %v", err)
	}
	if foreignLease.Epoch() != localLease.Epoch() {
		t.Fatalf("fixture requires equal numeric epochs; foreign=%d local=%d",
			foreignLease.Epoch(), localLease.Epoch())
	}
	if syncer.triggerForSession(foreignLease, "thr-target", 1, 42) {
		t.Fatal("foreign TokenStore lease started messages sync")
	}
	if got := syncer.TotalTriggered(); got != 0 {
		t.Fatalf("TotalTriggered = %d, want 0", got)
	}
	if got := requestCount.Load(); got != 0 {
		t.Fatalf("cloud requests = %d, want 0", got)
	}
}

func TestMessagesSyncer_ParentCtxCancellationStopsInFlight(t *testing.T) {
	// Cancel the parent ctx; an in-flight sync should observe it.
	gate := make(chan struct{})
	requests := atomic.Int64{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-gate
	}))
	t.Cleanup(upstream.Close)

	db, _ := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ctx.db")), &gorm.Config{Logger: gormlogger.Discard})
	for _, ddl := range []string{
		`CREATE TABLE w_workagent_thread (id INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER, uuid TEXT NOT NULL UNIQUE, name TEXT, updated_at TEXT, created_at TEXT)`,
		`CREATE TABLE w_workagent_message (id INTEGER PRIMARY KEY AUTOINCREMENT, uid INTEGER, uuid TEXT NOT NULL UNIQUE, thread_id INTEGER, user_text TEXT, ai_text TEXT, chat_mode TEXT, content_type TEXT, structured_content TEXT, actions TEXT, metadata TEXT, use_images TEXT, use_files TEXT, user_rating INTEGER, user_feedback TEXT, streaming_state TEXT DEFAULT 'complete', created_at TEXT, updated_at TEXT)`,
		`CREATE TABLE _local_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (42, 'thr-target', 'T')`)

	tokenStore := cloudproxy.NewTokenStore(newMemKeychainForJob())
	tokenStore.Save(cloudproxy.TokenPair{
		AccessToken: mintJWTWithUID(42), AccessExpiresAt: time.Now().UTC().Add(time.Hour),
		RefreshToken: "r", RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	})
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()

	parentCtx, parentCancel := context.WithCancel(context.Background())
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: db, Cloud: cloud, TokenStore: tokenStore,
		CursorStore: NewCursorStore(db), ParentCtx: parentCtx,
	})

	syncer.Trigger("thr-target", 1, 42)
	waitForRequests(t, &requests, 1, time.Second)

	// Cancel parent; the in-flight HTTP call should error out via
	// http.Client respecting ctx.
	parentCancel()
	// Release gate so the handler returns.
	close(gate)
	// Active count should drain.
	waitForActiveCount(t, syncer, 0, 2*time.Second)
}

func TestMessagesSyncer_SameUIDReloginCancelsInFlightSession(t *testing.T) {
	requestEntered := make(chan struct{})
	requestCanceled := make(chan struct{})
	var enteredOnce sync.Once
	var canceledOnce sync.Once
	syncer, db, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(requestEntered) })
		<-r.Context().Done()
		canceledOnce.Do(func() { close(requestCanceled) })
	})

	if !syncer.Trigger("thr-target", 1, 42) {
		t.Fatal("Trigger should start")
	}
	select {
	case <-requestEntered:
	case <-time.After(time.Second):
		t.Fatal("messages request did not reach cloud")
	}

	// Same subject is deliberately not enough to preserve an in-flight job:
	// unconditional Save is a new authentication epoch and must retire every
	// bearer request created by the previous login.
	if err := syncer.deps.TokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "same-user-new-login",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("same-uid login: %v", err)
	}

	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("same-UID login did not cancel old messages HTTP request")
	}
	waitForActiveCount(t, syncer, 0, time.Second)

	var rows int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&rows); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if rows != 0 {
		t.Fatalf("old session committed %d message row(s)", rows)
	}
	cursor, err := syncer.deps.CursorStore.Get(CursorKeyMessagesPrefix + "thr-target")
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != "" {
		t.Fatalf("old session advanced messages cursor to %q", cursor)
	}
}

// TestMessagesSyncer_RecoversJobPanic pins that a panic inside the
// per-thread JobFunc doesn't crash the sidecar. The active-flag
// must be cleared (so a follow-up Trigger for the same thread
// proceeds) and the goroutine must exit cleanly.
func TestMessagesSyncer_RecoversJobPanic(t *testing.T) {
	// Build a syncer with the standard fixture, then override the
	// job-factory to panic on first invocation, succeed on second.
	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	invocations := atomic.Int64{}
	syncer.jobForTest = func(threadUUID string, cloudThreadID, expectedUID uint64, _ cloudproxy.SessionLease) func(context.Context) error {
		return func(ctx context.Context) error {
			n := invocations.Add(1)
			if n == 1 {
				panic("simulated job panic")
			}
			return nil
		}
	}

	// First trigger panics; must not crash the test process and must
	// clear the active-flag.
	if !syncer.Trigger("thr-target", 42, 42) {
		t.Fatal("first Trigger should start sync")
	}
	waitForActiveCount(t, syncer, 0, time.Second)
	if invocations.Load() != 1 {
		t.Fatalf("invocations after first trigger: %d, want 1", invocations.Load())
	}

	// Second trigger must proceed normally — coalesce-on-active
	// would otherwise mean the panic leaked the active-flag and
	// no future sync can fire.
	if !syncer.Trigger("thr-target", 42, 42) {
		t.Fatal("second Trigger should start sync (active-flag must have been cleared on panic)")
	}
	waitForActiveCount(t, syncer, 0, time.Second)
	if invocations.Load() != 2 {
		t.Fatalf("invocations after second trigger: %d, want 2", invocations.Load())
	}
}

func TestNewMessagesSyncer_PanicsOnNilDeps(t *testing.T) {
	cases := []struct {
		name string
		deps MessagesSyncerDeps
	}{
		{"nil db", MessagesSyncerDeps{Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}}},
		{"nil cloud", MessagesSyncerDeps{DB: &gorm.DB{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}}},
		{"nil tokenStore", MessagesSyncerDeps{DB: &gorm.DB{}, Cloud: &cloudproxy.Client{}, CursorStore: &CursorStore{}}},
		{"nil cursorStore", MessagesSyncerDeps{DB: &gorm.DB{}, Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for %s", tc.name)
				}
			}()
			_ = NewMessagesSyncer(tc.deps)
		})
	}
}

// TestMessagesSyncer_StuckWarnPerThread pins the [sync-stuck:messages]
// WARN parity with SyncWorker. Three consecutive failures on the
// same thread should emit ONE log line — not three. A recovery
// resets the gate so a later stuck stretch re-emits.
func TestMessagesSyncer_StuckWarnPerThread(t *testing.T) {
	var buf safeBufferMsg
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	syncer.jobForTest = func(threadUUID string, cloudThreadID, expectedUID uint64, _ cloudproxy.SessionLease) func(context.Context) error {
		return func(ctx context.Context) error {
			return errors.New("simulated cloud failure")
		}
	}

	// Fire three triggers on the same thread, waiting between each
	// for the previous to settle (per-thread coalesce would drop
	// concurrent triggers).
	for i := 0; i < 3; i++ {
		if !syncer.Trigger("thr-stuck", 42, 42) {
			t.Fatalf("trigger %d: should have started sync", i+1)
		}
		waitForActiveCount(t, syncer, 0, time.Second)
	}

	got := buf.String()
	stuckLines := strings.Count(got, "[sync-stuck:messages]")
	if stuckLines != 1 {
		t.Errorf("[sync-stuck:messages] should fire exactly once, fired %d times.\nLog output:\n%s",
			stuckLines, got)
	}
	if !strings.Contains(got, "thr-stuck") {
		t.Errorf("warn line should include the thread UUID; got:\n%s", got)
	}
	if !strings.Contains(got, "failed 3 consecutive ticks") {
		t.Errorf("warn line should mention consecutive count = 3; got:\n%s", got)
	}
}

// TestMessagesSyncer_StuckWarnResetsOnSuccess pins that a success
// after a stuck stretch re-arms the warn for that thread.
func TestMessagesSyncer_StuckWarnResetsOnSuccess(t *testing.T) {
	var buf safeBufferMsg
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	calls := atomic.Int64{}
	syncer.jobForTest = func(threadUUID string, cloudThreadID, expectedUID uint64, _ cloudproxy.SessionLease) func(context.Context) error {
		return func(ctx context.Context) error {
			n := calls.Add(1)
			// fail 1-3 → trip warn; succeed 4 → reset; fail 5-7 → trip warn again
			if n == 4 {
				return nil
			}
			return errors.New("simulated cloud failure")
		}
	}

	for i := 0; i < 7; i++ {
		if !syncer.Trigger("thr-stuck", 42, 42) {
			t.Fatalf("trigger %d: should have started sync", i+1)
		}
		waitForActiveCount(t, syncer, 0, time.Second)
	}

	got := buf.String()
	stuckLines := strings.Count(got, "[sync-stuck:messages]")
	if stuckLines != 2 {
		t.Errorf("[sync-stuck:messages] should fire twice (once per stretch), fired %d times.\nLog output:\n%s",
			stuckLines, got)
	}
}

// TestMessagesSyncer_StuckWarnPerThreadIndependent pins that the
// gate is per-thread, not global — two different threads each
// stuck should emit their own WARN.
func TestMessagesSyncer_StuckWarnPerThreadIndependent(t *testing.T) {
	var buf safeBufferMsg
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	syncer.jobForTest = func(threadUUID string, cloudThreadID, expectedUID uint64, _ cloudproxy.SessionLease) func(context.Context) error {
		return func(ctx context.Context) error {
			return errors.New("simulated cloud failure")
		}
	}

	// Seed an additional thread row so the second thread can also
	// trigger (newSyncerFixture only seeds 'thr-target').
	db := syncer.deps.DB
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (?, ?, ?)`,
		42, "thr-other", "Other",
	).Error; err != nil {
		t.Fatal(err)
	}

	// Fire 3 triggers on thread A, then 3 on thread B.
	for _, uuid := range []string{"thr-target", "thr-other"} {
		for i := 0; i < 3; i++ {
			if !syncer.Trigger(uuid, 42, 42) {
				t.Fatalf("trigger %s/%d: should have started", uuid, i+1)
			}
			waitForActiveCount(t, syncer, 0, time.Second)
		}
	}

	got := buf.String()
	stuckLines := strings.Count(got, "[sync-stuck:messages]")
	if stuckLines != 2 {
		t.Errorf("[sync-stuck:messages] should fire once per thread, fired %d times.\nLog output:\n%s",
			stuckLines, got)
	}
	// Both threads should be named in the output.
	if !strings.Contains(got, "thr-target") || !strings.Contains(got, "thr-other") {
		t.Errorf("WARN should name both threads:\n%s", got)
	}
}

// safeBufferMsg mirrors safeBuffer in worker_test.go — Go's log
// writer is called from goroutines so a bytes.Buffer isn't safe.
type safeBufferMsg struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *safeBufferMsg) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBufferMsg) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestMessagesSyncer_DrainWaitsForInflight pins the core Drain
// contract: after Drain returns, no goroutine is still touching
// the DB. Without it, the sidecar's DB.Close race could corrupt
// the SQLite WAL on shutdown (rare but recovery requires the
// user to wipe their local cache).
func TestMessagesSyncer_DrainWaitsForInflight(t *testing.T) {
	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	// Inject a slow job so we can fire Drain() while it's still
	// running, then assert Drain blocks until the goroutine exits.
	jobReleased := make(chan struct{})
	jobExited := atomic.Bool{}
	syncer.jobForTest = func(threadUUID string, cloudThreadID, expectedUID uint64, _ cloudproxy.SessionLease) func(context.Context) error {
		return func(ctx context.Context) error {
			<-jobReleased
			jobExited.Store(true)
			return nil
		}
	}

	if !syncer.Trigger("thr-target", 42, 42) {
		t.Fatal("trigger should start")
	}

	// Drain in a goroutine; meanwhile we observe that it's still
	// blocking (jobExited not yet true).
	drainReturned := make(chan struct{})
	go func() {
		syncer.Drain()
		close(drainReturned)
	}()

	// Drain should NOT have returned yet — job is still pending.
	select {
	case <-drainReturned:
		t.Fatal("Drain returned before in-flight job exited")
	case <-time.After(50 * time.Millisecond):
	}

	// Release the job; Drain unwinds.
	close(jobReleased)
	select {
	case <-drainReturned:
	case <-time.After(time.Second):
		t.Fatal("Drain did not unwind within 1s after job released")
	}
	if !jobExited.Load() {
		t.Error("job goroutine should have completed before Drain returned")
	}
}

// TestMessagesSyncer_DrainRejectsNewTriggers pins that post-Drain
// Trigger calls return false (treated as a no-op). Without this,
// a stray late Trigger after main.go starts draining could spawn
// a goroutine racing the DB close.
func TestMessagesSyncer_DrainRejectsNewTriggers(t *testing.T) {
	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	syncer.jobForTest = func(string, uint64, uint64, cloudproxy.SessionLease) func(context.Context) error {
		return func(context.Context) error { return nil }
	}

	syncer.Drain() // synchronous on idle: no goroutines to wait for

	if started := syncer.Trigger("thr-target", 42, 42); started {
		t.Error("Trigger after Drain should return false")
	}
	// TotalTriggered should NOT have incremented — closed-state
	// Triggers short-circuit before the atomic.Add.
	if got := syncer.TotalTriggered(); got != 0 {
		t.Errorf("TotalTriggered after Drain+Trigger: got %d, want 0", got)
	}
}

func TestMessagesSyncer_DrainContextTimeoutKeepsSyncerClosed(t *testing.T) {
	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	jobEntered := make(chan struct{})
	releaseJob := make(chan struct{})
	syncer.jobForTest = func(string, uint64, uint64, cloudproxy.SessionLease) func(context.Context) error {
		return func(context.Context) error {
			close(jobEntered)
			<-releaseJob
			return nil
		}
	}
	if !syncer.Trigger("thr-timeout", 42, 42) {
		t.Fatal("trigger should start")
	}
	select {
	case <-jobEntered:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := syncer.DrainContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DrainContext error = %v, want deadline exceeded", err)
	}
	if syncer.Trigger("post-timeout", 43, 42) {
		t.Fatal("timed-out drain reopened Trigger admission")
	}

	close(releaseJob)
	done := make(chan struct{})
	go func() {
		syncer.Drain()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Drain did not observe eventual job exit")
	}
}

// TestMessagesSyncer_DrainConcurrentWithTriggers pins the shutdown
// coordination rule: Drain and Trigger may race during sidecar
// shutdown, but they must not misuse the WaitGroup or leave work
// running after Drain has returned.
func TestMessagesSyncer_DrainConcurrentWithTriggers(t *testing.T) {
	for iter := 0; iter < 100; iter++ {
		syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})
		jobEntered := atomic.Int64{}
		syncer.jobForTest = func(string, uint64, uint64, cloudproxy.SessionLease) func(context.Context) error {
			return func(context.Context) error {
				jobEntered.Add(1)
				return nil
			}
		}

		start := make(chan struct{})
		done := make(chan struct{})
		for i := 0; i < 16; i++ {
			i := i
			go func() {
				<-start
				_ = syncer.Trigger(fmt.Sprintf("thr-%d-%d", iter, i), uint64(i+1), 42)
				done <- struct{}{}
			}()
		}
		go func() {
			<-start
			syncer.Drain()
			done <- struct{}{}
		}()

		close(start)
		for i := 0; i < 17; i++ {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("iteration %d: concurrent Drain/Trigger calls did not finish", iter)
			}
		}

		if got := syncer.ActiveCount(); got != 0 {
			t.Fatalf("iteration %d: ActiveCount after Drain = %d, want 0", iter, got)
		}
		if syncer.Trigger("post-drain", 999, 42) {
			t.Fatalf("iteration %d: Trigger after Drain should return false", iter)
		}
		if jobEntered.Load() > syncer.TotalTriggered() {
			t.Fatalf("iteration %d: jobEntered=%d exceeds TotalTriggered=%d",
				iter, jobEntered.Load(), syncer.TotalTriggered())
		}
	}
}

// TestMessagesSyncer_DrainIdempotent pins that calling Drain
// repeatedly is safe — main.go may call it from multiple shutdown
// paths (signal handler + normal return), and a second Drain must
// not block forever (Wait on already-zero counter would otherwise
// be fine, but keeping the closed-state transition idempotent makes
// the semantics explicit).
func TestMessagesSyncer_DrainIdempotent(t *testing.T) {
	syncer, _, _, _ := newSyncerFixture(t, func(w http.ResponseWriter, r *http.Request) {})

	done := make(chan struct{})
	go func() {
		syncer.Drain()
		syncer.Drain()
		syncer.Drain()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("repeated Drain calls deadlocked")
	}
}
