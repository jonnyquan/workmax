//go:build desktop

package sync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

// jobFixture stands up a full slice end-to-end:
//   - SQLite with the thread + _local_meta tables
//   - cloud_proxy Client pointed at an httptest server
//   - TokenStore seeded with a valid JWT (Id=42)
//   - CursorStore atop the SQLite
//
// Returns a built JobFunc + handles for assertions.
type jobFixture struct {
	t            *testing.T
	db           *gorm.DB
	tokenStore   *cloudproxy.TokenStore
	cursorStore  *CursorStore
	upstream     *httptest.Server
	job          JobFunc
	requestCount atomic.Int64
}

func newJobFixture(t *testing.T, handler http.HandlerFunc) *jobFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "job.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		model TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 0,
		cloud_sync_state TEXT NOT NULL DEFAULT 'synced',
		cloud_thread_id TEXT,
		last_synced_at TEXT,
		created_at TEXT,
		updated_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create thread table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create meta table: %v", err)
	}

	f := &jobFixture{t: t, db: db}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requestCount.Add(1)
		handler(w, r)
	}))
	t.Cleanup(f.upstream.Close)

	f.tokenStore = cloudproxy.NewTokenStore(newMemKeychainForJob())
	// Mint a JWT with Id=42.
	tok := mintJWTWithUID(42)
	if err := f.tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      tok,
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-tok",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	f.cursorStore = NewCursorStore(db)

	f.job = NewThreadsJob(ThreadsJobDeps{
		DB:          db,
		Cloud:       cloud,
		TokenStore:  f.tokenStore,
		CursorStore: f.cursorStore,
		PageLimit:   2, // small to exercise pagination
	})
	return f
}

// memKeychainForJob is a process-local Keychain stub since the
// real Darwin Keychain isn't available in CI.
type memKeychainForJob struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemKeychainForJob() *memKeychainForJob      { return &memKeychainForJob{data: map[string][]byte{}} }
func (m *memKeychainForJob) key(s, a string) string { return s + "\x00" + a }
func (m *memKeychainForJob) Read(s, a string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.data[m.key(s, a)]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, cloudproxy.ErrKeychainNoEntry
}
func (m *memKeychainForJob) Write(s, a string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.key(s, a)] = append([]byte(nil), v...)
	return nil
}
func (m *memKeychainForJob) Delete(s, a string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.key(s, a))
	return nil
}

func mintJWTWithUID(uid uint) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	// Note: payload field is Id (capital, matching server/model/system/request.BaseClaims).
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"Id":` + uintToStr(uid) + `,"exp":9999999999}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("test-sig"))
	return hdr + "." + payload + "." + sig
}

func uintToStr(u uint) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for u > 0 {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	return string(buf[i:])
}

func mustThreadsCursorKey(tb testing.TB, uid uint) string {
	tb.Helper()
	key, err := ThreadsCursorKey(uid)
	if err != nil {
		tb.Fatalf("threads cursor key for uid %d: %v", uid, err)
	}
	return key
}

func TestThreadsJob_HappyPath_OnePage(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"items": [
				{"action":"upsert","cloud_thread_id":"1","uuid":"u-1","name":"A",
				 "agent_mode":"ppt","agent_type":"general_agent","model":"work-pro",
				 "message_count":2,"msg_preview":"hi","file_count":0,"is_public":false,
				 "updated_at":"2026-05-17T22:00:00Z","created_at":"2026-05-17T22:00:00Z"},
				{"action":"upsert","cloud_thread_id":"2","uuid":"u-2","name":"B",
				 "agent_mode":"ppt","agent_type":"general_agent","model":"work-pro",
				 "message_count":3,"msg_preview":"hey","file_count":0,"is_public":false,
				 "updated_at":"2026-05-17T22:01:00Z","created_at":"2026-05-17T22:01:00Z"}
			],
			"next_cursor": "cur-next",
			"has_more": false,
			"server_time": "2026-05-17T22:05:00Z"
		}`)
	})

	if err := f.job(context.Background()); err != nil {
		t.Fatalf("job: %v", err)
	}
	if f.requestCount.Load() != 1 {
		t.Errorf("requests: got %d, want 1 (has_more=false)", f.requestCount.Load())
	}

	// Both rows landed.
	var count int64
	f.db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&count)
	if count != 2 {
		t.Errorf("rows: got %d, want 2", count)
	}
	// Both rows tagged with uid=42 from the seeded JWT.
	var uid42 int64
	f.db.Raw(`SELECT count(*) FROM w_workagent_thread WHERE uid = 42`).Row().Scan(&uid42)
	if uid42 != 2 {
		t.Errorf("uid=42 rows: got %d, want 2 (JWT-derived uid)", uid42)
	}
	// Cursor advanced.
	cursor, _ := f.cursorStore.Get(mustThreadsCursorKey(t, 42))
	if cursor != "cur-next" {
		t.Errorf("cursor: got %q, want cur-next", cursor)
	}
}

func TestThreadsJob_PaginatesUntilDrained(t *testing.T) {
	calls := atomic.Int64{}
	pageBodies := [][]byte{
		[]byte(`{"items":[{"action":"upsert","cloud_thread_id":"1","uuid":"u-1","name":"A","agent_mode":"ppt","updated_at":"2026-05-17T22:00:00Z"}],"next_cursor":"p2","has_more":true,"server_time":"now"}`),
		[]byte(`{"items":[{"action":"upsert","cloud_thread_id":"2","uuid":"u-2","name":"B","agent_mode":"ppt","updated_at":"2026-05-17T22:01:00Z"}],"next_cursor":"p3","has_more":true,"server_time":"now"}`),
		[]byte(`{"items":[{"action":"upsert","cloud_thread_id":"3","uuid":"u-3","name":"C","agent_mode":"ppt","updated_at":"2026-05-17T22:02:00Z"}],"next_cursor":"p4","has_more":false,"server_time":"now"}`),
	}
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		idx := calls.Add(1) - 1
		w.Header().Set("Content-Type", "application/json")
		if int(idx) >= len(pageBodies) {
			t.Errorf("unexpected extra request #%d", idx+1)
			return
		}
		w.Write(pageBodies[idx])
	})

	if err := f.job(context.Background()); err != nil {
		t.Fatalf("job: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("requests: got %d, want 3 (one per page)", got)
	}
	var count int64
	f.db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&count)
	if count != 3 {
		t.Errorf("rows: got %d, want 3", count)
	}
	cursor, _ := f.cursorStore.Get(mustThreadsCursorKey(t, 42))
	if cursor != "p4" {
		t.Errorf("cursor: got %q, want p4 (last page's NextCursor)", cursor)
	}
}

func TestThreadsJob_HasMoreWithoutNextCursorReturnsError(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":true,"server_time":"now"}`)
	})

	err := f.job(context.Background())
	if err == nil {
		t.Fatal("expected protocol error")
	}
	if !strings.Contains(err.Error(), "empty next_cursor") {
		t.Fatalf("got %v, want empty next_cursor error", err)
	}
	cursor, _ := f.cursorStore.Get(mustThreadsCursorKey(t, 42))
	if cursor != "" {
		t.Fatalf("cursor should not advance on malformed pagination page, got %q", cursor)
	}
}

func TestThreadsJob_ResumesFromStoredCursor(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo back the since= we received so the test can assert.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Test-Cursor", r.URL.Query().Get("since"))
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})
	// Pre-seed the cursor.
	if err := f.cursorStore.Set(mustThreadsCursorKey(t, 42), "saved-cursor"); err != nil {
		t.Fatal(err)
	}

	// Capture the since query param on the next request.
	var capturedSince string
	f.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedSince = r.URL.Query().Get("since")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})

	if err := f.job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if capturedSince != "saved-cursor" {
		t.Errorf("job did not resume from stored cursor: got %q, want saved-cursor", capturedSince)
	}
}

func TestThreadsJob_CursorsAreIsolatedAcrossAccountSwitches(t *testing.T) {
	var mu sync.Mutex
	var capturedSince []string
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedSince = append(capturedSince, r.URL.Query().Get("since"))
		call := len(capturedSince)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":"account-a-next","has_more":false,"server_time":"now"}`)
		case 2:
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":"account-b-next","has_more":false,"server_time":"now"}`)
		case 3:
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
		default:
			t.Errorf("unexpected threads request #%d", call)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})

	// Simulate an existing installation whose old, account-agnostic cursor
	// was written by account A. No production tick may consume this key.
	if err := f.cursorStore.Set(CursorKeyThreads, "legacy-global-cursor"); err != nil {
		t.Fatal(err)
	}

	// Account A starts fresh and receives its own resume point.
	if err := f.job(context.Background()); err != nil {
		t.Fatalf("account A first tick: %v", err)
	}

	// Switch the durable session to account B. Its first request must not
	// inherit either the legacy global cursor or account A's scoped cursor.
	if err := f.tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(84),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-b",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.job(context.Background()); err != nil {
		t.Fatalf("account B first tick: %v", err)
	}

	// Switching back to A resumes A's cursor, while B's cursor remains
	// independently persisted.
	if err := f.tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-a-again",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.job(context.Background()); err != nil {
		t.Fatalf("account A resume tick: %v", err)
	}

	mu.Lock()
	gotSince := append([]string(nil), capturedSince...)
	mu.Unlock()
	wantSince := []string{"", "", "account-a-next"}
	if fmt.Sprint(gotSince) != fmt.Sprint(wantSince) {
		t.Fatalf("since cursors = %v, want %v (A fresh, B fresh, A resume)", gotSince, wantSince)
	}

	cursorA, err := f.cursorStore.Get(mustThreadsCursorKey(t, 42))
	if err != nil || cursorA != "account-a-next" {
		t.Fatalf("account A cursor = %q, err=%v", cursorA, err)
	}
	cursorB, err := f.cursorStore.Get(mustThreadsCursorKey(t, 84))
	if err != nil || cursorB != "account-b-next" {
		t.Fatalf("account B cursor = %q, err=%v", cursorB, err)
	}
	legacy, err := f.cursorStore.Get(CursorKeyThreads)
	if err != nil || legacy != "legacy-global-cursor" {
		t.Fatalf("legacy cursor should remain ignored and unchanged: value=%q err=%v", legacy, err)
	}
}

func TestThreadsJob_RejectsZeroUIDBeforeCursorOrCloudAccess(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cloud should not be called for a token without a positive UID: %s", r.URL.Path)
	})
	if err := f.tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(0),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-zero",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	err := f.job(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid uid") {
		t.Fatalf("zero UID error = %v, want invalid uid", err)
	}
	if got := f.requestCount.Load(); got != 0 {
		t.Fatalf("zero UID made %d cloud request(s), want 0", got)
	}
	var scopedCursorCount int64
	if err := f.db.Raw(
		`SELECT count(*) FROM _local_meta WHERE key LIKE ?`, CursorKeyThreadsPrefix+"%",
	).Row().Scan(&scopedCursorCount); err != nil {
		t.Fatal(err)
	}
	if scopedCursorCount != 0 {
		t.Fatalf("zero UID created %d scoped cursor row(s), want 0", scopedCursorCount)
	}
}

func TestThreadsJob_NoSessionIsNoOp(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("cloud should not be called when no session")
	})
	// Clear the token store so AcquireAccessToken returns ErrNoSession.
	if err := f.tokenStore.Clear(); err != nil {
		t.Fatal(err)
	}
	if err := f.job(context.Background()); err != nil {
		t.Errorf("no-session tick should return nil, got: %v", err)
	}
}

func TestThreadsJob_LogoutCancelsInFlightSessionBeforeWrite(t *testing.T) {
	requestEntered := make(chan struct{})
	requestCanceled := make(chan struct{})
	var enteredOnce sync.Once
	var canceledOnce sync.Once
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(requestEntered) })
		<-r.Context().Done()
		canceledOnce.Do(func() { close(requestCanceled) })
	})

	jobDone := make(chan error, 1)
	go func() { jobDone <- f.job(context.Background()) }()
	select {
	case <-requestEntered:
	case <-time.After(time.Second):
		t.Fatal("threads request did not reach cloud")
	}
	if err := f.tokenStore.Clear(); err != nil {
		t.Fatalf("logout: %v", err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("logout did not cancel old threads HTTP request")
	}
	select {
	case err := <-jobDone:
		if !errors.Is(err, cloudproxy.ErrSessionChanged) {
			t.Fatalf("job error = %v, want ErrSessionChanged", err)
		}
	case <-time.After(time.Second):
		t.Fatal("threads job did not exit after logout")
	}

	var rows int64
	if err := f.db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&rows); err != nil {
		t.Fatalf("count threads: %v", err)
	}
	if rows != 0 {
		t.Fatalf("logged-out session committed %d thread row(s)", rows)
	}
	cursor, err := f.cursorStore.Get(mustThreadsCursorKey(t, 42))
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != "" {
		t.Fatalf("logged-out session advanced threads cursor to %q", cursor)
	}
}

func TestThreadsJob_AuthExpiredReturnsError(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	})
	err := f.job(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	// Confirm cursor was NOT advanced.
	cursor, _ := f.cursorStore.Get(mustThreadsCursorKey(t, 42))
	if cursor != "" {
		t.Errorf("cursor should remain unset on auth failure, got %q", cursor)
	}
}

func TestThreadsJob_UnauthorizedForcesRefreshAndRetriesOnce(t *testing.T) {
	oldAccess := mintJWTWithUID(42)
	// Keep a syntactically valid three-segment JWT while changing the exact
	// credential bytes used by the Authorization header.
	freshAccess := strings.TrimSuffix(oldAccess, "."+strings.Split(oldAccess, ".")[2]) + ".fresh-signature"
	var syncCalls atomic.Int64
	var refreshCalls atomic.Int64

	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case cloudproxy.CloudRouteOAuthToken:
			refreshCalls.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse refresh form: %v", err)
			}
			if got := r.Form.Get("refresh_token"); got != "refresh-tok" {
				t.Errorf("refresh token = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":       freshAccess,
				"token_type":         "Bearer",
				"expires_in":         900,
				"refresh_token":      "rotated-refresh",
				"refresh_expires_in": 86400,
				"scope":              "workagent",
			})
		case cloudproxy.CloudRouteSyncThreads:
			syncCalls.Add(1)
			switch r.Header.Get("Authorization") {
			case "Bearer " + oldAccess:
				w.WriteHeader(http.StatusUnauthorized)
			case "Bearer " + freshAccess:
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
			default:
				t.Errorf("unexpected Authorization: %q", r.Header.Get("Authorization"))
				w.WriteHeader(http.StatusUnauthorized)
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	if err := f.tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      oldAccess,
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-tok",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
	}

	if err := f.job(context.Background()); err != nil {
		t.Fatalf("job after 401 recovery: %v", err)
	}
	if got := refreshCalls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	if got := syncCalls.Load(); got != 2 {
		t.Fatalf("sync calls = %d, want rejected request plus one retry", got)
	}
	pair, err := f.tokenStore.Get()
	if err != nil || pair.AccessToken != freshAccess || pair.RefreshToken != "rotated-refresh" {
		t.Fatalf("stored rotated pair = %+v, err=%v", pair, err)
	}
}

func TestThreadsJob_BoundedByMaxPagesPerTick(t *testing.T) {
	// Cloud always says HasMore=true with a fresh NextCursor →
	// would loop forever without the safety bound. Verify the
	// job stops after MaxPagesPerTick.
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[{"action":"upsert","cloud_thread_id":"x","uuid":"`+
			randomUUIDForTest()+`","name":"A","agent_mode":"ppt","updated_at":"2026-05-17T22:00:00Z"}],
			"next_cursor":"never-ends","has_more":true,"server_time":"now"}`)
	})
	// Reduce MaxPagesPerTick so the test runs quickly.
	f.job = NewThreadsJob(ThreadsJobDeps{
		DB:              f.db,
		Cloud:           cloudproxy.NewClient(f.upstream.URL),
		TokenStore:      f.tokenStore,
		CursorStore:     f.cursorStore,
		PageLimit:       1,
		MaxPagesPerTick: 5,
	})
	if err := f.job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.requestCount.Load(); got != 5 {
		t.Errorf("requests: got %d, want 5 (MaxPagesPerTick)", got)
	}
}

func TestThreadsJob_CtxCancelBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[{"action":"upsert","cloud_thread_id":"x","uuid":"u-1","name":"A","agent_mode":"ppt","updated_at":"2026-05-17T22:00:00Z"}],"next_cursor":"p2","has_more":true,"server_time":"now"}`)
		// Cancel after the first response so the second page sees ctx done.
		cancel()
	})
	err := f.job(ctx)
	if err == nil {
		t.Error("expected context error after cancel")
	}
}

// randomUUIDForTest mints a unique-ish uuid so each page's upsert
// hits a distinct row (instead of clobbering one). Tiny helper —
// real UUIDs aren't needed.
var uuidCounter atomic.Int64

func randomUUIDForTest() string {
	n := uuidCounter.Add(1)
	return "u-test-" + uintToStr(uint(n))
}

// === P1.B.3.x.3: periodic message-sync fan-out tests ===

// newJobFixtureWithMessages stands up the fixture AND seeds the
// w_workagent_thread table with some pre-existing local rows so
// the post-tick triggerPeriodicMessageSync has data to walk.
func newJobFixtureWithMessages(t *testing.T, handler http.HandlerFunc) *jobFixture {
	t.Helper()
	f := newJobFixture(t, handler)
	// Seed 3 local threads with message_count>0 + valid cloud_thread_id
	// + 1 with =0. The periodic fan-out must call message sync
	// with cloud_thread_id, not the local SQLite id.
	// The triggerPeriodicMessageSync should skip the empty one.
	now := time.Now().UTC()
	f.db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, message_count, cloud_thread_id, updated_at, created_at)
		 VALUES
		   (42, 'thr-recent-a', 'A', 'ppt', 3, '101', ?, ?),
		   (42, 'thr-recent-b', 'B', 'ppt', 5, '102', ?, ?),
		   (42, 'thr-recent-c', 'C', 'ppt', 2, '103', ?, ?),
		   (42, 'thr-empty',    'E', 'ppt', 0, '104', ?, ?)`,
		now.Add(-1*time.Second).Format(time.RFC3339Nano), now.Add(-1*time.Second).Format(time.RFC3339Nano),
		now.Add(-2*time.Second).Format(time.RFC3339Nano), now.Add(-2*time.Second).Format(time.RFC3339Nano),
		now.Add(-3*time.Second).Format(time.RFC3339Nano), now.Add(-3*time.Second).Format(time.RFC3339Nano),
		now.Add(-4*time.Second).Format(time.RFC3339Nano), now.Add(-4*time.Second).Format(time.RFC3339Nano),
	)
	return f
}

func TestThreadsJob_FiresPeriodicMessageSyncForRecentThreads(t *testing.T) {
	// Cloud handler returns an empty threads-delta + a no-op
	// messages-delta. We just want to confirm the post-tick path
	// fans out triggers for the seeded local threads.
	f := newJobFixtureWithMessages(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Both endpoints accept this empty shape.
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})

	// Build a MessagesSyncer pointed at the same fixture so the
	// triggered goroutines have somewhere to call.
	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: f.db, Cloud: cloud, TokenStore: f.tokenStore, CursorStore: f.cursorStore,
	})
	expectedUIDs := make(chan uint64, 3)
	syncer.jobForTest = func(threadUUID string, cloudThreadID, expectedUID uint64, _ cloudproxy.SessionLease) func(context.Context) error {
		return func(context.Context) error {
			expectedUIDs <- expectedUID
			return nil
		}
	}

	// Replace the fixture's job with one wired to the syncer.
	job := NewThreadsJob(ThreadsJobDeps{
		DB:                  f.db,
		Cloud:               cloud,
		TokenStore:          f.tokenStore,
		CursorStore:         f.cursorStore,
		PageLimit:           50,
		MessagesSyncer:      syncer,
		RecentThreadsToSync: 20, // cover all 3 non-empty
	})
	if err := job(context.Background()); err != nil {
		t.Fatalf("job: %v", err)
	}

	// Each in-flight trigger spawns a goroutine; wait for them to
	// drain. TotalTriggered counts STARTED triggers (post-coalesce);
	// the 3 non-empty threads should each have started once.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && syncer.TotalTriggered() < 3 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := syncer.TotalTriggered(); got != 3 {
		t.Errorf("TotalTriggered: got %d, want 3 (3 non-empty threads)", got)
	}
	// Wait for all the message-sync goroutines to finish before
	// the fixture's t.Cleanup tears down the SQLite + httptest
	// (otherwise we leak background work into the next test).
	for time.Now().Before(deadline) && syncer.ActiveCount() > 0 {
		time.Sleep(2 * time.Millisecond)
	}
	for i := 0; i < 3; i++ {
		select {
		case got := <-expectedUIDs:
			if got != 42 {
				t.Fatalf("periodic message expected UID = %d, want 42", got)
			}
		default:
			t.Fatalf("periodic message trigger %d did not forward an expected UID", i+1)
		}
	}
}

func TestThreadsJob_PeriodicMessageSyncUsesCloudThreadID(t *testing.T) {
	var seen []string
	var seenMu sync.Mutex
	f := newJobFixtureWithMessages(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/desktop/sync/messages") {
			seenMu.Lock()
			seen = append(seen, r.URL.Query().Get("thread_id"))
			seenMu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})

	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: f.db, Cloud: cloud, TokenStore: f.tokenStore, CursorStore: f.cursorStore,
	})

	job := NewThreadsJob(ThreadsJobDeps{
		DB:                  f.db,
		Cloud:               cloud,
		TokenStore:          f.tokenStore,
		CursorStore:         f.cursorStore,
		MessagesSyncer:      syncer,
		RecentThreadsToSync: 20,
	})
	if err := job(context.Background()); err != nil {
		t.Fatalf("job: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && syncer.TotalTriggered() < 3 {
		time.Sleep(2 * time.Millisecond)
	}
	for time.Now().Before(deadline) && syncer.ActiveCount() > 0 {
		time.Sleep(2 * time.Millisecond)
	}

	seenMu.Lock()
	defer seenMu.Unlock()
	got := map[string]bool{}
	for _, id := range seen {
		got[id] = true
	}
	for _, want := range []string{"101", "102", "103"} {
		if !got[want] {
			t.Fatalf("message sync did not use cloud_thread_id %s; saw %v", want, seen)
		}
	}
	for _, localID := range []string{"1", "2", "3"} {
		if got[localID] {
			t.Fatalf("message sync used local SQLite id %s instead of cloud_thread_id; saw %v", localID, seen)
		}
	}
}

func TestThreadsJob_RecentThreadsToSyncZeroDisablesFanOut(t *testing.T) {
	f := newJobFixtureWithMessages(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})

	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: f.db, Cloud: cloud, TokenStore: f.tokenStore, CursorStore: f.cursorStore,
	})

	job := NewThreadsJob(ThreadsJobDeps{
		DB:                  f.db,
		Cloud:               cloud,
		TokenStore:          f.tokenStore,
		CursorStore:         f.cursorStore,
		MessagesSyncer:      syncer,
		RecentThreadsToSync: 0, // explicit disable
	})
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := syncer.TotalTriggered(); got != 0 {
		t.Errorf("RecentThreadsToSync=0 should disable fan-out, got %d triggers", got)
	}
}

func TestThreadsJob_NilSyncerSkipsFanOutEvenIfRecentSet(t *testing.T) {
	f := newJobFixtureWithMessages(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	})

	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	job := NewThreadsJob(ThreadsJobDeps{
		DB:                  f.db,
		Cloud:               cloud,
		TokenStore:          f.tokenStore,
		CursorStore:         f.cursorStore,
		MessagesSyncer:      nil, // explicit nil
		RecentThreadsToSync: 20,
	})
	// Should run cleanly even though RecentThreadsToSync > 0 —
	// nil-check guards the fan-out call.
	if err := job(context.Background()); err != nil {
		t.Fatalf("job: %v", err)
	}
}

func TestTriggerPeriodicMessageSync_RespectsLimit(t *testing.T) {
	// Seed 5 threads, limit=2 → only 2 triggers fire.
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		f.db.Exec(
			`INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, message_count, cloud_thread_id, updated_at, created_at)
			 VALUES (42, ?, ?, 'ppt', 1, ?, ?, ?)`,
			"thr-"+string('a'+rune(i)), "T", uintToStr(uint(200+i)),
			now.Add(time.Duration(-i)*time.Second).Format(time.RFC3339Nano),
			now.Add(time.Duration(-i)*time.Second).Format(time.RFC3339Nano),
		)
	}
	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: f.db, Cloud: cloud, TokenStore: f.tokenStore, CursorStore: f.cursorStore,
	})

	triggerPeriodicMessageSync(f.db, 42, syncer, 2)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && syncer.TotalTriggered() < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := syncer.TotalTriggered(); got != 2 {
		t.Errorf("limit=2: got %d triggers, want 2", got)
	}
	// Drain goroutines so cleanup is clean.
	for time.Now().Before(deadline) && syncer.ActiveCount() > 0 {
		time.Sleep(2 * time.Millisecond)
	}
}

func TestTriggerPeriodicMessageSync_RejectsNonPositiveUID(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := f.db.Exec(
		`INSERT INTO w_workagent_thread
			(uid, uuid, name, agent_mode, message_count, cloud_thread_id, updated_at, created_at)
		 VALUES (-1, 'thr-negative-uid', 'N', 'ppt', 1, '401', ?, ?)`,
		now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: f.db, Cloud: cloud, TokenStore: f.tokenStore, CursorStore: f.cursorStore,
	})
	syncer.jobForTest = func(string, uint64, uint64, cloudproxy.SessionLease) func(context.Context) error {
		return func(context.Context) error { return nil }
	}

	triggerPeriodicMessageSync(f.db, 0, syncer, 20)
	triggerPeriodicMessageSync(f.db, -1, syncer, 20)
	if got := syncer.TotalTriggered(); got != 0 {
		t.Fatalf("non-positive UID started %d message sync(s), want 0", got)
	}
}

func TestTriggerPeriodicMessageSync_SkipsPausedThreads(t *testing.T) {
	f := newJobFixture(t, func(w http.ResponseWriter, r *http.Request) {})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := f.db.Exec(
		`INSERT INTO w_workagent_thread
			(uid, uuid, name, agent_mode, message_count, cloud_thread_id, cloud_sync_state, updated_at, created_at)
		 VALUES
			(42, 'thr-active', 'A', 'ppt', 1, '301', 'synced', ?, ?),
			(42, 'thr-paused', 'P', 'ppt', 1, '302', 'paused', ?, ?)`,
		now, now, now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	syncer := NewMessagesSyncer(MessagesSyncerDeps{
		DB: f.db, Cloud: cloud, TokenStore: f.tokenStore, CursorStore: f.cursorStore,
	})

	triggerPeriodicMessageSync(f.db, 42, syncer, 20)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && syncer.TotalTriggered() < 1 {
		time.Sleep(2 * time.Millisecond)
	}
	if got := syncer.TotalTriggered(); got != 1 {
		t.Fatalf("got %d triggers, want only the active thread", got)
	}
	for time.Now().Before(deadline) && syncer.ActiveCount() > 0 {
		time.Sleep(2 * time.Millisecond)
	}
}

func TestNewThreadsJob_PanicsOnNilDeps(t *testing.T) {
	cases := []struct {
		name string
		deps ThreadsJobDeps
	}{
		{"nil db", ThreadsJobDeps{Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}}},
		{"nil cloud", ThreadsJobDeps{DB: &gorm.DB{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}}},
		{"nil tokenStore", ThreadsJobDeps{DB: &gorm.DB{}, Cloud: &cloudproxy.Client{}, CursorStore: &CursorStore{}}},
		{"nil cursorStore", ThreadsJobDeps{DB: &gorm.DB{}, Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for %s", tc.name)
				}
			}()
			_ = NewThreadsJob(tc.deps)
		})
	}
}

func BenchmarkThreadsJob_ColdStart1000Threads(b *testing.B) {
	pages := makeThreadDeltaPagesForBench(b, 1000, 100)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageIndex := 0
		if since := r.URL.Query().Get("since"); since != "" {
			n, err := strconv.Atoi(since)
			if err != nil {
				b.Fatalf("bad since cursor %q: %v", since, err)
			}
			pageIndex = n
		}
		if pageIndex < 0 || pageIndex >= len(pages) {
			b.Fatalf("unexpected page index %d", pageIndex)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(pages[pageIndex])
	}))
	defer upstream.Close()

	dbDir := b.TempDir()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db := openThreadsJobBenchDB(b, filepath.Join(dbDir, fmt.Sprintf("cold-%06d.db", i)))
		tokenStore := cloudproxy.NewTokenStore(newMemKeychainForJob())
		if err := tokenStore.Save(cloudproxy.TokenPair{
			AccessToken:      mintJWTWithUID(42),
			AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
			RefreshToken:     "refresh-tok",
			RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		}); err != nil {
			b.Fatalf("seed token: %v", err)
		}
		cloud := cloudproxy.NewClient(upstream.URL)
		cloud.HTTPClient = upstream.Client()
		job := NewThreadsJob(ThreadsJobDeps{
			DB:              db,
			Cloud:           cloud,
			TokenStore:      tokenStore,
			CursorStore:     NewCursorStore(db),
			PageLimit:       100,
			MaxPagesPerTick: 20,
		})
		if err := job(context.Background()); err != nil {
			b.Fatalf("job: %v", err)
		}
		var count int64
		if err := db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&count); err != nil {
			b.Fatalf("count rows: %v", err)
		}
		if count != 1000 {
			b.Fatalf("synced rows: got %d, want 1000", count)
		}
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	}
}

func openThreadsJobBenchDB(tb testing.TB, dbPath string) *gorm.DB {
	tb.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		model TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 0,
		cloud_sync_state TEXT NOT NULL DEFAULT 'synced',
		cloud_thread_id TEXT,
		last_synced_at TEXT,
		created_at TEXT,
		updated_at TEXT
	)`).Error; err != nil {
		tb.Fatalf("create thread table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		tb.Fatalf("create meta table: %v", err)
	}
	return db
}

func makeThreadDeltaPagesForBench(tb testing.TB, total, pageSize int) [][]byte {
	tb.Helper()
	if total%pageSize != 0 {
		tb.Fatalf("total %d must be divisible by pageSize %d", total, pageSize)
	}
	pageCount := total / pageSize
	pages := make([][]byte, 0, pageCount)
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	for page := 0; page < pageCount; page++ {
		items := make([]cloudproxy.ThreadDeltaItem, 0, pageSize)
		for i := 0; i < pageSize; i++ {
			n := page*pageSize + i
			ts := base.Add(time.Duration(n) * time.Second).Format(time.RFC3339Nano)
			items = append(items, cloudproxy.ThreadDeltaItem{
				Action:        "upsert",
				CloudThreadID: strconv.Itoa(n + 1),
				UUID:          fmt.Sprintf("thr_%04d", n),
				Name:          fmt.Sprintf("Thread %04d", n),
				AgentMode:     "ppt",
				AgentType:     "general_agent",
				Model:         "work-pro",
				MessageCount:  n % 200,
				MsgPreview:    "preview",
				FileCount:     n % 5,
				IsPublic:      false,
				UpdatedAt:     ts,
				CreatedAt:     ts,
			})
		}
		nextCursor := ""
		if page < pageCount-1 {
			nextCursor = strconv.Itoa(page + 1)
		}
		body, err := json.Marshal(cloudproxy.ThreadsDeltaPage{
			Items:      items,
			NextCursor: nextCursor,
			HasMore:    page < pageCount-1,
			ServerTime: base.Add(time.Duration(total) * time.Second).Format(time.RFC3339Nano),
		})
		if err != nil {
			tb.Fatalf("marshal page %d: %v", page, err)
		}
		pages = append(pages, body)
	}
	return pages
}
