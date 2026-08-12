//go:build desktop

package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	cloudproxy "server/desktop/cloud_proxy"
)

// messagesJobFixture parallels jobFixture from threads_job_test.go
// but seeds a thread row + lets each test parameterize the cloud
// handler.
type messagesJobFixture struct {
	db           *gorm.DB
	tokenStore   *cloudproxy.TokenStore
	cursorStore  *CursorStore
	upstream     *httptest.Server
	requestCount atomic.Int64
	threadUUID   string
	cloudID      uint64
}

func newMessagesJobFixture(t *testing.T, handler http.HandlerFunc) *messagesJobFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "messages_job.db")),
		&gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		updated_at TEXT, created_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT, ai_text TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		content_type TEXT, structured_content TEXT, actions TEXT, metadata TEXT,
		use_images TEXT, use_files TEXT,
		user_rating INTEGER NOT NULL DEFAULT 0, user_feedback TEXT,
		agent_engine TEXT NOT NULL DEFAULT '',
		agent_model TEXT NOT NULL DEFAULT '',
		streaming_state TEXT NOT NULL DEFAULT 'complete',
		created_at TEXT, updated_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	// Seed the thread (CloudID=1).
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (?, ?, ?)`,
		42, "thr-target", "T",
	).Error; err != nil {
		t.Fatal(err)
	}

	f := &messagesJobFixture{db: db, threadUUID: "thr-target", cloudID: 1}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requestCount.Add(1)
		handler(w, r)
	}))
	t.Cleanup(f.upstream.Close)

	f.tokenStore = cloudproxy.NewTokenStore(newMemKeychainForJob())
	tok := mintJWTWithUID(42)
	if err := f.tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      tok,
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
	}
	f.cursorStore = NewCursorStore(db)
	return f
}

func (f *messagesJobFixture) buildJob(t *testing.T) JobFunc {
	t.Helper()
	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	return NewMessagesJob(MessagesJobDeps{
		DB:            f.db,
		Cloud:         cloud,
		TokenStore:    f.tokenStore,
		CursorStore:   f.cursorStore,
		ThreadUUID:    f.threadUUID,
		CloudThreadID: f.cloudID,
		ExpectedUID:   42,
		PageLimit:     2,
	})
}

func TestMessagesJob_HappyPath_OnePage(t *testing.T) {
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"items": [
				{"action":"upsert","uuid":"m-1","thread_uuid":"thr-target",
				 "user_text":"hi","ai_text":"ok","chat_mode":"ppt",
				 "updated_at":"2026-05-17T22:00:00Z","created_at":"2026-05-17T22:00:00Z"},
				{"action":"upsert","uuid":"m-2","thread_uuid":"thr-target",
				 "user_text":"again","ai_text":"world","chat_mode":"ppt",
				 "updated_at":"2026-05-17T22:01:00Z","created_at":"2026-05-17T22:01:00Z"}
			],
			"next_cursor": "abc",
			"has_more": false,
			"server_time": "now"
		}`)
	})
	job := f.buildJob(t)

	if err := job(context.Background()); err != nil {
		t.Fatalf("job: %v", err)
	}
	if got := f.requestCount.Load(); got != 1 {
		t.Errorf("requests: got %d, want 1", got)
	}

	var count int64
	f.db.Raw(`SELECT count(*) FROM w_workagent_message WHERE thread_id = ?`, f.cloudID).Row().Scan(&count)
	if count != 2 {
		t.Errorf("rows: got %d, want 2", count)
	}

	cursor, _ := f.cursorStore.Get(CursorKeyMessagesPrefix + f.threadUUID)
	if cursor != "abc" {
		t.Errorf("cursor: got %q, want abc", cursor)
	}
}

func TestMessagesJob_NoSessionIsNoOp(t *testing.T) {
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("cloud should not be called when no session")
	})
	if err := f.tokenStore.Clear(); err != nil {
		t.Fatal(err)
	}
	job := f.buildJob(t)
	if err := job(context.Background()); err != nil {
		t.Errorf("no-session: want nil, got %v", err)
	}
}

func TestMessagesJob_InitialSubjectMismatchMakesNoCloudCallOrWrite(t *testing.T) {
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("cloud must not be called for a mismatched trigger subject: %s %s", r.Method, r.URL.Path)
	})
	if err := f.tokenStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(99),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "other-account-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("replace session: %v", err)
	}

	err := f.buildJob(t)(context.Background())
	if !errors.Is(err, errSyncSessionChanged) {
		t.Fatalf("error = %v, want errSyncSessionChanged", err)
	}
	if got := f.requestCount.Load(); got != 0 {
		t.Fatalf("cloud requests = %d, want 0", got)
	}
	assertMessagesJobMadeNoProgress(t, f)
}

func TestMessagesJob_ExpectedLeaseRequiresExactTokenStoreSession(t *testing.T) {
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("foreign expected lease must not reach cloud: %s %s", r.Method, r.URL.Path)
	})
	foreignStore := cloudproxy.NewTokenStore(newMemKeychainForJob())
	if err := foreignStore.Save(cloudproxy.TokenPair{
		AccessToken:      mintJWTWithUID(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "foreign-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed foreign session: %v", err)
	}
	foreignLease, err := foreignStore.AcquireSessionLease()
	if err != nil {
		t.Fatalf("acquire foreign lease: %v", err)
	}
	localLease, err := f.tokenStore.AcquireSessionLease()
	if err != nil {
		t.Fatalf("acquire local lease: %v", err)
	}
	if foreignLease.Epoch() != localLease.Epoch() {
		t.Fatalf("fixture requires equal numeric epochs; foreign=%d local=%d",
			foreignLease.Epoch(), localLease.Epoch())
	}
	if foreignLease.SameSession(localLease) {
		t.Fatal("leases from different TokenStores unexpectedly match")
	}

	cloud := cloudproxy.NewClient(f.upstream.URL)
	cloud.HTTPClient = f.upstream.Client()
	job := NewMessagesJob(MessagesJobDeps{
		DB:            f.db,
		Cloud:         cloud,
		TokenStore:    f.tokenStore,
		CursorStore:   f.cursorStore,
		ThreadUUID:    f.threadUUID,
		CloudThreadID: f.cloudID,
		ExpectedUID:   42,
		ExpectedLease: foreignLease,
	})
	err = job(context.Background())
	if !errors.Is(err, cloudproxy.ErrSessionChanged) {
		t.Fatalf("job error = %v, want ErrSessionChanged", err)
	}
	if got := f.requestCount.Load(); got != 0 {
		t.Fatalf("cloud requests = %d, want 0", got)
	}
	assertMessagesJobMadeNoProgress(t, f)
}

func TestMessagesJob_401RecoveryRetainsFrozenSubject(t *testing.T) {
	tests := []struct {
		name              string
		refreshedUID      uint
		wantErr           bool
		wantMessagesCalls int64
		wantRows          int64
	}{
		{
			name:              "same subject retries and writes",
			refreshedUID:      42,
			wantMessagesCalls: 2,
			wantRows:          1,
		},
		{
			name:              "different subject stops before retry and write",
			refreshedUID:      99,
			wantErr:           true,
			wantMessagesCalls: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var messagesCalls atomic.Int64
			var tokenCalls atomic.Int64
			freshAccess := mintJWTWithUID(tc.refreshedUID)
			f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case cloudproxy.CloudRouteSyncMessages:
					call := messagesCalls.Add(1)
					if call == 1 {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{
						"items":[{"action":"upsert","uuid":"m-after-401","thread_uuid":"thr-target",
						"user_text":"u","ai_text":"a","chat_mode":"ppt",
						"updated_at":"2026-05-17T22:00:00Z"}],
						"next_cursor":"after-401","has_more":false,"server_time":"now"
					}`)
				case cloudproxy.CloudRouteOAuthToken:
					tokenCalls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{
						"access_token":%q,"token_type":"Bearer","expires_in":3600,
						"refresh_token":"rotated-refresh","refresh_expires_in":7200,
						"scope":"workagent"
					}`, freshAccess)
				default:
					t.Fatalf("unexpected cloud request: %s %s", r.Method, r.URL.Path)
				}
			})

			err := f.buildJob(t)(context.Background())
			if tc.wantErr {
				if !errors.Is(err, errSyncSessionChanged) {
					t.Fatalf("error = %v, want errSyncSessionChanged", err)
				}
			} else if err != nil {
				t.Fatalf("job: %v", err)
			}
			if got := tokenCalls.Load(); got != 1 {
				t.Fatalf("token calls = %d, want 1", got)
			}
			if got := messagesCalls.Load(); got != tc.wantMessagesCalls {
				t.Fatalf("messages calls = %d, want %d", got, tc.wantMessagesCalls)
			}
			var rows int64
			f.db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&rows)
			if rows != tc.wantRows {
				t.Fatalf("message rows = %d, want %d", rows, tc.wantRows)
			}
			cursor, cursorErr := f.cursorStore.Get(CursorKeyMessagesPrefix + f.threadUUID)
			if cursorErr != nil {
				t.Fatalf("read cursor: %v", cursorErr)
			}
			if tc.wantErr && cursor != "" {
				t.Fatalf("mismatched recovery advanced cursor to %q", cursor)
			}
			if !tc.wantErr && cursor != "after-401" {
				t.Fatalf("same-subject recovery cursor = %q, want after-401", cursor)
			}
		})
	}
}

func assertMessagesJobMadeNoProgress(t *testing.T, f *messagesJobFixture) {
	t.Helper()
	var rows int64
	if err := f.db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&rows); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if rows != 0 {
		t.Fatalf("message rows = %d, want 0", rows)
	}
	cursor, err := f.cursorStore.Get(CursorKeyMessagesPrefix + f.threadUUID)
	if err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if cursor != "" {
		t.Fatalf("cursor = %q, want empty", cursor)
	}
}

func TestMessagesJob_AuthExpiredReturnsError(t *testing.T) {
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	})
	job := f.buildJob(t)
	err := job(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	cursor, _ := f.cursorStore.Get(CursorKeyMessagesPrefix + f.threadUUID)
	if cursor != "" {
		t.Errorf("cursor should NOT advance on auth failure, got %q", cursor)
	}
}

func TestMessagesJob_MissingParentThreadDoesNotAdvanceCursor(t *testing.T) {
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"items": [
				{"action":"upsert","uuid":"m-orphan","thread_uuid":"thr-not-local",
				 "user_text":"hi","ai_text":"ok","chat_mode":"ppt",
				 "updated_at":"2026-05-17T22:00:00Z","created_at":"2026-05-17T22:00:00Z"}
			],
			"next_cursor": "should-not-save",
			"has_more": false,
			"server_time": "now"
		}`)
	})
	job := f.buildJob(t)
	err := job(context.Background())
	if err == nil {
		t.Fatal("expected retryable missing-parent-thread error")
	}
	if !errors.Is(err, ErrMissingParentThread) {
		t.Fatalf("got %v, want ErrMissingParentThread", err)
	}
	cursor, _ := f.cursorStore.Get(CursorKeyMessagesPrefix + f.threadUUID)
	if cursor != "" {
		t.Fatalf("cursor should not advance past unwritten orphan message, got %q", cursor)
	}
	var count int64
	f.db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 0 {
		t.Fatalf("orphan message should not be written, got %d row(s)", count)
	}
}

func TestMessagesJob_ThreadNotOwnedIsSilentNoOp(t *testing.T) {
	// Cloud returns 404 (thread not owned by uid OR not in cloud).
	// The job should NOT return an error — next tick will re-evaluate.
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	})
	job := f.buildJob(t)
	if err := job(context.Background()); err != nil {
		t.Errorf("404 should map to silent no-op, got %v", err)
	}
}

func TestMessagesJob_PaginatesUntilDrained(t *testing.T) {
	calls := atomic.Int64{}
	pageBodies := [][]byte{
		[]byte(`{"items":[{"action":"upsert","uuid":"m-1","thread_uuid":"thr-target","user_text":"u","ai_text":"a","chat_mode":"ppt","updated_at":"2026-05-17T22:00:00Z"}],"next_cursor":"p2","has_more":true,"server_time":"now"}`),
		[]byte(`{"items":[{"action":"upsert","uuid":"m-2","thread_uuid":"thr-target","user_text":"u","ai_text":"a","chat_mode":"ppt","updated_at":"2026-05-17T22:01:00Z"}],"next_cursor":"p3","has_more":true,"server_time":"now"}`),
		[]byte(`{"items":[{"action":"upsert","uuid":"m-3","thread_uuid":"thr-target","user_text":"u","ai_text":"a","chat_mode":"ppt","updated_at":"2026-05-17T22:02:00Z"}],"next_cursor":"p4","has_more":false,"server_time":"now"}`),
	}
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		idx := calls.Add(1) - 1
		w.Header().Set("Content-Type", "application/json")
		if int(idx) >= len(pageBodies) {
			t.Errorf("unexpected extra request #%d", idx+1)
			return
		}
		w.Write(pageBodies[idx])
	})
	job := f.buildJob(t)

	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("requests: got %d, want 3 (one per page)", got)
	}
	var count int64
	f.db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 3 {
		t.Errorf("rows: got %d, want 3", count)
	}
	cursor, _ := f.cursorStore.Get(CursorKeyMessagesPrefix + f.threadUUID)
	if cursor != "p4" {
		t.Errorf("cursor: got %q, want p4", cursor)
	}
}

func TestMessagesJob_HasMoreWithoutNextCursorReturnsError(t *testing.T) {
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":true,"server_time":"now"}`)
	})
	job := f.buildJob(t)

	err := job(context.Background())
	if err == nil {
		t.Fatal("expected protocol error")
	}
	if !strings.Contains(err.Error(), "empty next_cursor") {
		t.Fatalf("got %v, want empty next_cursor error", err)
	}
	cursor, _ := f.cursorStore.Get(CursorKeyMessagesPrefix + f.threadUUID)
	if cursor != "" {
		t.Fatalf("cursor should not advance on malformed pagination page, got %q", cursor)
	}
}

func TestMessagesJob_CtxCancelBetweenPages(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newMessagesJobFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[{"action":"upsert","uuid":"m-1","thread_uuid":"thr-target","user_text":"u","ai_text":"a","chat_mode":"ppt","updated_at":"2026-05-17T22:00:00Z"}],"next_cursor":"p2","has_more":true,"server_time":"now"}`)
		cancel()
	})
	job := f.buildJob(t)
	err := job(ctx)
	if err == nil {
		t.Error("expected ctx error")
	}
}

func TestNewMessagesJob_PanicsOnBadDeps(t *testing.T) {
	cases := []struct {
		name string
		deps MessagesJobDeps
	}{
		{"nil db", MessagesJobDeps{Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}, ThreadUUID: "x", CloudThreadID: 1, ExpectedUID: 42}},
		{"empty thread uuid", MessagesJobDeps{DB: &gorm.DB{}, Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}, CloudThreadID: 1, ExpectedUID: 42}},
		{"zero cloud id", MessagesJobDeps{DB: &gorm.DB{}, Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}, ThreadUUID: "x", ExpectedUID: 42}},
		{"zero expected uid", MessagesJobDeps{DB: &gorm.DB{}, Cloud: &cloudproxy.Client{}, TokenStore: &cloudproxy.TokenStore{}, CursorStore: &CursorStore{}, ThreadUUID: "x", CloudThreadID: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Error("expected panic")
				}
			}()
			_ = NewMessagesJob(tc.deps)
		})
	}
}
