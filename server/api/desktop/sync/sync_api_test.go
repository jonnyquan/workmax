package sync

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	systemReq "server/model/system/request"
	svc "server/service/desktop/sync"
)

// openSyncAPITestDB stands up SQLite with the thread schema the
// handler reads. Kept locally rather than imported from
// service/desktop/sync (the test helpers there are unexported on
// purpose — promoting them would make them part of the public test
// API surface).
func openSyncAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sync_api.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		model TEXT NOT NULL DEFAULT '',
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT,
		created_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create thread table: %v", err)
	}
	// P1.A.5b: tombstone table required for the merged-delete path
	// in ListThreads / ListMessages. Existing tests use these test
	// DBs; without the table the tombstone fetch 500s.
	if err := db.Exec(`CREATE TABLE w_workagent_tombstone (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL,
		entity_type TEXT NOT NULL,
		entity_id INTEGER NOT NULL,
		entity_uuid TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatalf("create tombstone table: %v", err)
	}
	return db
}

func seedTestThread(t *testing.T, db *gorm.DB, uid int, uuid, name string, updatedAt time.Time) {
	t.Helper()
	ts := updatedAt.UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread
		   (uid, uuid, name, agent_mode, agent_type, model, message_count,
		    msg_preview, file_count, is_public, updated_at, created_at)
		 VALUES (?, ?, ?, 'ppt', 'general_agent', 'work-pro', 5, 'preview', 0, 0, ?, ?)`,
		uid, uuid, name, ts, ts,
	).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// stubJWT injects a JWT claims object the way middleware.JWTAuth
// does at runtime — c.Set("claims", *CustomClaims). utils.GetUserID
// reads this same key, so the handler thinks the request was
// authenticated as `uid`.
//
// Centralizes the contract so a future change to the claims-context
// key only needs to update this helper. Without this, every handler
// test for /api/desktop/sync/* would re-implement the same setup.
func stubJWT(uid uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("claims", &systemReq.CustomClaims{
			BaseClaims: systemReq.BaseClaims{Id: uid},
		})
		c.Next()
	}
}

func newTestEngine(t *testing.T, db *gorm.DB, uid uint) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	api := NewSyncApi(db)
	r := gin.New()
	r.GET("/api/desktop/sync/threads", stubJWT(uid), api.ListThreads)
	return r
}

func decodeResp(t *testing.T, body []byte) listThreadsResponse {
	t.Helper()
	var out listThreadsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, body)
	}
	return out
}

func TestListThreads_HappyPath(t *testing.T) {
	db := openSyncAPITestDB(t)
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	seedTestThread(t, db, 7, "uuid-a", "A", base)
	seedTestThread(t, db, 7, "uuid-b", "B", base.Add(time.Minute))
	r := newTestEngine(t, db, 7)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil))
	if w.Code != 200 {
		t.Fatalf("status: %d (body: %s)", w.Code, w.Body.String())
	}
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 2 {
		t.Fatalf("items: %d, want 2", len(body.Items))
	}
	if body.Items[0].UUID != "uuid-a" || body.Items[1].UUID != "uuid-b" {
		t.Errorf("order: %v %v", body.Items[0].UUID, body.Items[1].UUID)
	}
	for _, it := range body.Items {
		if it.Action != ActionUpsert {
			t.Errorf("action: got %q, want upsert", it.Action)
		}
	}
	if body.HasMore {
		t.Error("has_more should be false")
	}
	if body.NextCursor == "" {
		t.Error("next_cursor should be populated even when has_more=false")
	}
}

func TestListThreads_NoUIDReturns401(t *testing.T) {
	db := openSyncAPITestDB(t)
	gin.SetMode(gin.TestMode)
	api := NewSyncApi(db)
	r := gin.New()
	// No JWT stub middleware — utils.GetUserID returns 0.
	r.GET("/api/desktop/sync/threads", api.ListThreads)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil))
	if w.Code != 401 {
		t.Errorf("status: %d, want 401", w.Code)
	}
}

func TestListThreads_IDORIsolation(t *testing.T) {
	db := openSyncAPITestDB(t)
	now := time.Now().UTC()
	seedTestThread(t, db, 7, "mine", "M", now)
	seedTestThread(t, db, 42, "theirs", "T", now)

	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil))
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 1 {
		t.Fatalf("uid=7 should see 1 thread, got %d", len(body.Items))
	}
	if body.Items[0].UUID != "mine" {
		t.Errorf("leaked another user's thread: %q", body.Items[0].UUID)
	}
}

func TestListThreads_LimitQuery(t *testing.T) {
	db := openSyncAPITestDB(t)
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		seedTestThread(t, db, 7, fmt.Sprintf("u-%d", i), "T", base.Add(time.Duration(i)*time.Second))
	}
	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads?limit=10", nil))
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 10 {
		t.Errorf("limit=10: got %d items", len(body.Items))
	}
	if !body.HasMore {
		t.Error("has_more should be true (25 total, limit 10)")
	}
}

func TestListThreads_LimitClampsAtMax(t *testing.T) {
	db := openSyncAPITestDB(t)
	for i := 0; i < 3; i++ {
		seedTestThread(t, db, 7, fmt.Sprintf("u-%d", i), "T", time.Now().UTC().Add(time.Duration(i)*time.Second))
	}
	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads?limit=99999", nil))
	// Should succeed with our 3 rows (ClampLimit reduced to MaxLimit).
	if w.Code != 200 {
		t.Errorf("status: %d, want 200", w.Code)
	}
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 3 {
		t.Errorf("expected all 3 rows (clamped limit still > total), got %d", len(body.Items))
	}
}

func TestListThreads_GarbageLimitFallsBackToDefault(t *testing.T) {
	db := openSyncAPITestDB(t)
	seedTestThread(t, db, 7, "u-1", "T", time.Now().UTC())
	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads?limit=nan", nil))
	if w.Code != 200 {
		t.Errorf("garbage limit should silently default, got %d", w.Code)
	}
}

func TestListThreads_PaginationViaSinceCursor(t *testing.T) {
	db := openSyncAPITestDB(t)
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		seedTestThread(t, db, 7, fmt.Sprintf("u-%d", i), "T", base.Add(time.Duration(i)*time.Second))
	}
	r := newTestEngine(t, db, 7)

	// First page, limit 2.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads?limit=2", nil))
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 2 || body.Items[0].UUID != "u-0" {
		t.Fatalf("page 1: %+v", body.Items)
	}
	if !body.HasMore {
		t.Error("has_more should be true")
	}

	// Second page using cursor.
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet,
		"/api/desktop/sync/threads?limit=2&since="+body.NextCursor, nil))
	body2 := decodeResp(t, w2.Body.Bytes())
	if len(body2.Items) != 2 || body2.Items[0].UUID != "u-2" {
		t.Fatalf("page 2: %+v", body2.Items)
	}

	// Third page (last 1 row).
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, httptest.NewRequest(http.MethodGet,
		"/api/desktop/sync/threads?limit=2&since="+body2.NextCursor, nil))
	body3 := decodeResp(t, w3.Body.Bytes())
	if len(body3.Items) != 1 || body3.Items[0].UUID != "u-4" {
		t.Errorf("page 3: %+v", body3.Items)
	}
	if body3.HasMore {
		t.Error("has_more should be false on final page")
	}
}

func TestListThreads_BadCursorReturns400(t *testing.T) {
	db := openSyncAPITestDB(t)
	r := newTestEngine(t, db, 7)
	cases := []struct {
		name   string
		cursor string
		want   string
	}{
		{"bad base64", "!!!notbase64!!!", "bad_encoding"},
		// "definitely not json" base64url-encoded:
		{"valid base64 not JSON", "ZGVmaW5pdGVseSBub3QganNvbg", "bad_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
				"/api/desktop/sync/threads?since="+tc.cursor, nil))
			if w.Code != 400 {
				t.Errorf("status: %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.want) {
				t.Errorf("expected reason %q in body: %s", tc.want, w.Body.String())
			}
		})
	}
}

func TestListThreads_EmptyResultWellFormed(t *testing.T) {
	db := openSyncAPITestDB(t)
	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil))
	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}
	raw := w.Body.String()
	// Items should be [] not null.
	if strings.Contains(raw, `"items":null`) {
		t.Errorf("empty result should serialize as [], not null: %s", raw)
	}
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 0 {
		t.Errorf("expected empty, got %d items", len(body.Items))
	}
	if body.HasMore {
		t.Error("empty: has_more should be false")
	}
}

// === GET /api/desktop/sync/messages tests ===

func openMessagesAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "messages_api.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		updated_at TEXT, created_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT, ai_text TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		content_type TEXT, structured_content TEXT, actions TEXT, metadata TEXT,
		use_images TEXT, use_files TEXT,
		user_rating INTEGER NOT NULL DEFAULT 0, user_feedback TEXT,
		updated_at TEXT, created_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	// P1.A.5b: tombstone table for the merged-delete path in
	// ListMessages.
	if err := db.Exec(`CREATE TABLE w_workagent_tombstone (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL,
		entity_type TEXT NOT NULL,
		entity_id INTEGER NOT NULL,
		entity_uuid TEXT NOT NULL,
		deleted_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func seedTestMsgThread(t *testing.T, db *gorm.DB, uid int, uuid string) uint64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, updated_at, created_at) VALUES (?, ?, 'T', ?, ?)`,
		uid, uuid, now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	var id uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, uuid).Row().Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedTestMessage(t *testing.T, db *gorm.DB, uid int, threadID uint64, uuid, userText, aiText string, updatedAt time.Time) {
	t.Helper()
	ts := updatedAt.UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text, chat_mode, updated_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 'ppt', ?, ?)`,
		uid, uuid, threadID, userText, aiText, ts, ts,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func newMessagesTestEngine(t *testing.T, db *gorm.DB, uid uint) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	api := NewSyncApi(db)
	r := gin.New()
	r.GET("/api/desktop/sync/messages", stubJWT(uid), api.ListMessages)
	return r
}

func decodeMessagesResp(t *testing.T, body []byte) listMessagesResponse {
	t.Helper()
	var out listMessagesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, body)
	}
	return out
}

func TestListMessages_HappyPath(t *testing.T) {
	db := openMessagesAPITestDB(t)
	threadID := seedTestMsgThread(t, db, 7, "thr-1")
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	seedTestMessage(t, db, 7, threadID, "m-1", "hi", "ok", base)
	seedTestMessage(t, db, 7, threadID, "m-2", "again", "world", base.Add(time.Minute))
	r := newMessagesTestEngine(t, db, 7)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/desktop/sync/messages?thread_id=%d", threadID), nil))
	if w.Code != 200 {
		t.Fatalf("status: %d (body: %s)", w.Code, w.Body.String())
	}
	body := decodeMessagesResp(t, w.Body.Bytes())
	if len(body.Items) != 2 {
		t.Fatalf("items: %d, want 2", len(body.Items))
	}
	if body.Items[0].UUID != "m-1" || body.Items[1].UUID != "m-2" {
		t.Errorf("order: %v %v", body.Items[0].UUID, body.Items[1].UUID)
	}
	for _, it := range body.Items {
		if it.Action != ActionUpsert {
			t.Errorf("action: %q, want upsert", it.Action)
		}
	}
	if body.Items[0].ThreadUUID != "thr-1" {
		t.Errorf("ThreadUUID should round-trip from joined lookup: %q", body.Items[0].ThreadUUID)
	}
}

func TestListMessages_MissingThreadIDReturns400(t *testing.T) {
	db := openMessagesAPITestDB(t)
	r := newMessagesTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/messages", nil))
	if w.Code != 400 {
		t.Errorf("status: %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "thread_id is required") {
		t.Errorf("body should explain: %s", w.Body.String())
	}
}

func TestListMessages_NonNumericThreadID(t *testing.T) {
	db := openMessagesAPITestDB(t)
	r := newMessagesTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/messages?thread_id=not-a-number", nil))
	if w.Code != 400 {
		t.Errorf("status: %d, want 400", w.Code)
	}
}

func TestListMessages_NoUIDReturns401(t *testing.T) {
	db := openMessagesAPITestDB(t)
	gin.SetMode(gin.TestMode)
	api := NewSyncApi(db)
	r := gin.New()
	r.GET("/api/desktop/sync/messages", api.ListMessages) // no stubJWT
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/messages?thread_id=1", nil))
	if w.Code != 401 {
		t.Errorf("status: %d, want 401", w.Code)
	}
}

func TestListMessages_IDOR404(t *testing.T) {
	db := openMessagesAPITestDB(t)
	mine := seedTestMsgThread(t, db, 7, "thr-mine")
	theirs := seedTestMsgThread(t, db, 42, "thr-theirs")
	seedTestMessage(t, db, 42, theirs, "m-secret", "private", "alsoprivate", time.Now().UTC())
	_ = mine

	r := newMessagesTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/desktop/sync/messages?thread_id=%d", theirs), nil))
	if w.Code != 404 {
		t.Errorf("IDOR: status %d, want 404 (don't leak existence)", w.Code)
	}
}

func TestListMessages_BadCursor(t *testing.T) {
	db := openMessagesAPITestDB(t)
	threadID := seedTestMsgThread(t, db, 7, "thr-1")
	r := newMessagesTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/desktop/sync/messages?thread_id=%d&since=%%21not-base64", threadID), nil))
	if w.Code != 400 {
		t.Errorf("bad cursor: status %d, want 400", w.Code)
	}
}

func TestListMessages_Pagination(t *testing.T) {
	db := openMessagesAPITestDB(t)
	threadID := seedTestMsgThread(t, db, 7, "thr-1")
	base := time.Date(2026, 5, 17, 22, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		seedTestMessage(t, db, 7, threadID, fmt.Sprintf("m-%d", i), "u", "a", base.Add(time.Duration(i)*time.Second))
	}
	r := newMessagesTestEngine(t, db, 7)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/desktop/sync/messages?thread_id=%d&limit=2", threadID), nil))
	body := decodeMessagesResp(t, w.Body.Bytes())
	if len(body.Items) != 2 || !body.HasMore {
		t.Fatalf("page 1: items=%d hasMore=%v want 2/true", len(body.Items), body.HasMore)
	}

	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/desktop/sync/messages?thread_id=%d&limit=2&since=%s", threadID, body.NextCursor), nil))
	body2 := decodeMessagesResp(t, w2.Body.Bytes())
	if len(body2.Items) == 0 {
		t.Fatalf("page 2 should have items, got 0")
	}
}

func TestListMessages_EnvelopeFieldsPresent(t *testing.T) {
	resp := listMessagesResponse{
		Items: []listMessagesItem{
			{Action: ActionUpsert, MessageDeltaRow: svc.MessageDeltaRow{
				CloudMessageID: "1", UUID: "m-x", ThreadUUID: "thr-1",
				UserText: "hi", AIText: "ok",
				UpdatedAt: time.Now().UTC(),
			}},
		},
		NextCursor: "c", HasMore: true,
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, key := range []string{`"items":`, `"next_cursor":`, `"has_more":`, `"server_time":`, `"action":"upsert"`, `"thread_uuid":`} {
		if !strings.Contains(got, key) {
			t.Errorf("missing %q in: %s", key, got)
		}
	}
}

// === GET /api/desktop/sync/threads/:id tests (P1.A.3) ===

// openThreadFullAPITestDB stands up a DB with the heavy columns
// the single-thread fetch reads (prompt, latest_plan, plan_history).
func openThreadFullAPITestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "thread_full_api.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		agent_mode TEXT NOT NULL DEFAULT 'ppt',
		agent_type TEXT NOT NULL DEFAULT 'general_agent',
		model TEXT NOT NULL DEFAULT '',
		workspace_path TEXT,
		max_tokens INTEGER NOT NULL DEFAULT 0,
		context_count INTEGER NOT NULL DEFAULT 0,
		presence_penalty REAL NOT NULL DEFAULT 0,
		frequency_penalty REAL NOT NULL DEFAULT 0,
		temperature REAL NOT NULL DEFAULT 0,
		prompt TEXT,
		message_count INTEGER NOT NULL DEFAULT 0,
		msg_preview TEXT NOT NULL DEFAULT '',
		file_count INTEGER NOT NULL DEFAULT 0,
		is_public INTEGER NOT NULL DEFAULT 0,
		latest_plan TEXT,
		plan_history TEXT,
		updated_at TEXT,
		created_at TEXT
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func newGetThreadTestEngine(t *testing.T, db *gorm.DB, uid uint) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	api := NewSyncApi(db)
	r := gin.New()
	r.GET("/api/desktop/sync/threads/:id", stubJWT(uid), api.GetThread)
	return r
}

func decodeGetThreadResp(t *testing.T, body []byte) getThreadResponse {
	t.Helper()
	var out getThreadResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, body)
	}
	return out
}

func TestGetThread_HappyPath(t *testing.T) {
	db := openThreadFullAPITestDB(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	db.Exec(
		`INSERT INTO w_workagent_thread
		   (uid, uuid, name, agent_mode, prompt, latest_plan, plan_history,
		    message_count, file_count, updated_at, created_at)
		 VALUES (7, 'u-1', 'Full', 'ppt',
		         'system prompt body', '{"step":"draft"}', '[{"t":"first"}]',
		         5, 2, ?, ?)`, now, now,
	)
	var id uint64
	db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, "u-1").Row().Scan(&id)
	r := newGetThreadTestEngine(t, db, 7)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/desktop/sync/threads/%d", id), nil))
	if w.Code != 200 {
		t.Fatalf("status: %d (body: %s)", w.Code, w.Body.String())
	}
	body := decodeGetThreadResp(t, w.Body.Bytes())
	if body.Thread.UUID != "u-1" || body.Thread.Name != "Full" {
		t.Errorf("identity: %+v", body.Thread)
	}
	if body.Thread.Prompt != "system prompt body" {
		t.Errorf("prompt: %q", body.Thread.Prompt)
	}
	if body.Thread.LatestPlan != `{"step":"draft"}` || body.Thread.PlanHistory != `[{"t":"first"}]` {
		t.Errorf("plan blobs: %+v", body.Thread)
	}
	if body.ServerTime == "" {
		t.Error("server_time should be populated")
	}
}

func TestGetThread_NoUIDReturns401(t *testing.T) {
	db := openThreadFullAPITestDB(t)
	gin.SetMode(gin.TestMode)
	api := NewSyncApi(db)
	r := gin.New()
	r.GET("/api/desktop/sync/threads/:id", api.GetThread) // no stubJWT
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads/1", nil))
	if w.Code != 401 {
		t.Errorf("status: %d, want 401", w.Code)
	}
}

func TestGetThread_NonNumericID(t *testing.T) {
	db := openThreadFullAPITestDB(t)
	r := newGetThreadTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads/abc", nil))
	if w.Code != 400 {
		t.Errorf("status: %d, want 400", w.Code)
	}
}

func TestGetThread_IDOR404(t *testing.T) {
	db := openThreadFullAPITestDB(t)
	db.Exec(`INSERT INTO w_workagent_thread (uid, uuid, name) VALUES (42, 'u-other', 'T')`)
	var id uint64
	db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, "u-other").Row().Scan(&id)
	r := newGetThreadTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/desktop/sync/threads/%d", id), nil))
	if w.Code != 404 {
		t.Errorf("IDOR: status %d, want 404", w.Code)
	}
}

func TestGetThread_MissingReturns404(t *testing.T) {
	db := openThreadFullAPITestDB(t)
	r := newGetThreadTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads/9999", nil))
	if w.Code != 404 {
		t.Errorf("status: %d, want 404 (don't leak existence)", w.Code)
	}
}

// === Tombstone merge tests (P1.A.5b) ===

// seedTombstone inserts a tombstone row. The test helpers
// (openSyncAPITestDB / openMessagesAPITestDB) create the tombstone
// table by default.
func seedTombstone(t *testing.T, db *gorm.DB, uid int, entityType string, entityID uint, entityUUID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_tombstone (uid, entity_type, entity_id, entity_uuid, deleted_at)
		 VALUES (?, ?, ?, ?, ?)`,
		uid, entityType, entityID, entityUUID, now,
	).Error; err != nil {
		t.Fatal(err)
	}
}

func TestListThreads_MergesTombstones(t *testing.T) {
	db := openSyncAPITestDB(t)
	now := time.Now().UTC()
	seedTestThread(t, db, 7, "thr-live", "Live", now)
	seedTombstone(t, db, 7, "thread", 99, "thr-gone")

	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil))
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 2 {
		t.Fatalf("expected 1 upsert + 1 delete = 2 items, got %d (%+v)",
			len(body.Items), body.Items)
	}
	var upserts, deletes int
	for _, it := range body.Items {
		switch it.Action {
		case ActionUpsert:
			upserts++
		case ActionDelete:
			deletes++
			if it.UUID != "thr-gone" {
				t.Errorf("delete item uuid: got %q, want thr-gone", it.UUID)
			}
		}
	}
	if upserts != 1 || deletes != 1 {
		t.Errorf("upserts=%d deletes=%d, want 1/1", upserts, deletes)
	}
}

func TestListThreads_TombstoneFiltersByUID(t *testing.T) {
	db := openSyncAPITestDB(t)
	seedTombstone(t, db, 7, "thread", 1, "mine-gone")
	seedTombstone(t, db, 42, "thread", 2, "theirs-gone")

	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil))
	body := decodeResp(t, w.Body.Bytes())
	var deletes []string
	for _, it := range body.Items {
		if it.Action == ActionDelete {
			deletes = append(deletes, it.UUID)
		}
	}
	if len(deletes) != 1 || deletes[0] != "mine-gone" {
		t.Errorf("uid filter leak: got %v", deletes)
	}
}

func TestListThreads_NoTombstonesReturnsOnlyUpserts(t *testing.T) {
	db := openSyncAPITestDB(t)
	seedTestThread(t, db, 7, "thr-only", "L", time.Now().UTC())
	r := newTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil))
	body := decodeResp(t, w.Body.Bytes())
	if len(body.Items) != 1 || body.Items[0].Action != ActionUpsert {
		t.Errorf("no tombstones: %+v", body.Items)
	}
}

func TestListMessages_MergesTombstones(t *testing.T) {
	db := openMessagesAPITestDB(t)
	threadID := seedTestMsgThread(t, db, 7, "thr-1")
	now := time.Now().UTC()
	seedTestMessage(t, db, 7, threadID, "m-live", "u", "a", now)
	seedTombstone(t, db, 7, "message", 999, "m-gone")

	r := newMessagesTestEngine(t, db, 7)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/desktop/sync/messages?thread_id=%d", threadID), nil))
	body := decodeMessagesResp(t, w.Body.Bytes())
	if len(body.Items) != 2 {
		t.Fatalf("expected 1 upsert + 1 delete, got %d (%+v)", len(body.Items), body.Items)
	}
	var sawDelete bool
	for _, it := range body.Items {
		if it.Action == ActionDelete && it.UUID == "m-gone" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Error("delete item not emitted")
	}
}

func TestListThreads_EnvelopeFieldsPresent(t *testing.T) {
	// Even without invoking the handler, sanity-check the response
	// struct serializes with the field order + types we promise.
	resp := listThreadsResponse{
		Items: []listThreadsItem{
			{Action: ActionUpsert, ThreadDeltaRow: svc.ThreadDeltaRow{
				CloudThreadID: "42",
				UUID:          "uuid-x",
				Name:          "Test",
				UpdatedAt:     time.Now().UTC(),
			}},
		},
		NextCursor: "abc",
		HasMore:    true,
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, key := range []string{`"items":`, `"next_cursor":`, `"has_more":`, `"server_time":`} {
		if !strings.Contains(got, key) {
			t.Errorf("missing %q in: %s", key, got)
		}
	}
	if !strings.Contains(got, `"action":"upsert"`) {
		t.Errorf("missing action field in item: %s", got)
	}
}
