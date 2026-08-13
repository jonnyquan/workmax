//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	cloudproxy "server/desktop/cloud_proxy"
)

// memKeychain mirrors the cloud_proxy test fixture so we don't pull
// the real macOS Keychain into Go-side server tests.
type memKeychain struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemKeychain() *memKeychain            { return &memKeychain{data: map[string][]byte{}} }
func (m *memKeychain) key(s, a string) string { return s + "\x00" + a }
func (m *memKeychain) Read(s, a string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.data[m.key(s, a)]; ok {
		return append([]byte(nil), v...), nil
	}
	return nil, cloudproxy.ErrKeychainNoEntry
}
func (m *memKeychain) Write(s, a string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[m.key(s, a)] = append([]byte(nil), v...)
	return nil
}
func (m *memKeychain) Delete(s, a string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, m.key(s, a))
	return nil
}

func openServerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "server_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Just the message table — handler test exercises the wire path,
	// not migrations.
	if err := db.Exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT,
		ai_text TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		message_idempotency_key TEXT,
		agent_engine TEXT NOT NULL DEFAULT '',
		agent_model TEXT NOT NULL DEFAULT '',
		agent_mind TEXT NOT NULL DEFAULT '',
		streaming_state TEXT NOT NULL DEFAULT 'complete',
		created_at TEXT,
		updated_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	// Thread table for /agent/chat's uuid → id resolution (P1.B.6).
	// Trimmed to the columns the handler queries.
	if err := db.Exec(`CREATE TABLE w_workagent_thread (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		cloud_thread_id TEXT,
		updated_at TEXT,
		created_at TEXT
	)`).Error; err != nil {
		t.Fatalf("create thread table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE w_desktop_agent_turn_intent (
		uid INTEGER NOT NULL,
		turn_uuid TEXT NOT NULL PRIMARY KEY,
		thread_id INTEGER NOT NULL,
		thread_uuid TEXT NOT NULL,
		user_text TEXT NOT NULL,
		chat_mode TEXT NOT NULL,
		request_digest TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('starting','streaming','interrupted','completed','canceled')),
		last_error_kind TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create agent turn intent table: %v", err)
	}
	return db
}

const serverTestTurnUUID = "de305d54-75b4-431b-adb2-eb6b9e546014"

func TestDecodeAgentChatRequest_ExactTypedObject(t *testing.T) {
	valid := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hello","chat_mode":"ppt","payload":{"stream":true}}`
	tests := []struct {
		name    string
		body    []byte
		wantErr bool
	}{
		{name: "valid", body: []byte(valid)},
		{name: "unknown uid", body: []byte(strings.Replace(valid, `"thread_uuid"`, `"uid":42,"thread_uuid"`, 1)), wantErr: true},
		{name: "duplicate", body: []byte(strings.Replace(valid, `"thread_uuid":"thr_1"`, `"thread_uuid":"thr_1","thread_uuid":"thr_2"`, 1)), wantErr: true},
		{name: "trailing", body: []byte(valid + `{}`), wantErr: true},
		{name: "null scalar", body: []byte(strings.Replace(valid, `"user_text":"hello"`, `"user_text":null`, 1)), wantErr: true},
		{name: "null payload", body: []byte(strings.Replace(valid, `"payload":{"stream":true}`, `"payload":null`, 1)), wantErr: true},
		{name: "missing", body: []byte(strings.Replace(valid, `,"chat_mode":"ppt"`, ``, 1)), wantErr: true},
		{name: "array", body: []byte(`[]`), wantErr: true},
		{name: "invalid utf8", body: append([]byte(`{"turn_uuid":"`), 0xff), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/agent/chat", bytes.NewReader(test.body))
			got, err := decodeAgentChatRequest(request)
			if (err != nil) != test.wantErr {
				t.Fatalf("decode error=%v, wantErr=%v", err, test.wantErr)
			}
			if !test.wantErr && (got.TurnUUID != serverTestTurnUUID || got.UserText != "hello" || string(got.Payload) != `{"stream":true}`) {
				t.Fatalf("decoded request drifted: %+v payload=%s", got, got.Payload)
			}
		})
	}
}

func TestHandleAgentChat_RejectsTypedBoundaryValues(t *testing.T) {
	base, token := newServerFixture(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid typed request reached upstream")
	})
	baseBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hello","chat_mode":"ppt","payload":{"stream":true}}`
	tests := []struct {
		name string
		body string
	}{
		{name: "turn uppercase", body: strings.Replace(baseBody, serverTestTurnUUID, strings.ToUpper(serverTestTurnUUID), 1)},
		{name: "turn wrong version", body: strings.Replace(baseBody, serverTestTurnUUID, "de305d54-75b4-131b-adb2-eb6b9e546014", 1)},
		{name: "thread whitespace", body: strings.Replace(baseBody, `"thr_1"`, `" thr_1"`, 1)},
		{name: "thread slash", body: strings.Replace(baseBody, `"thr_1"`, `"thr/1"`, 1)},
		{name: "thread query", body: strings.Replace(baseBody, `"thr_1"`, `"thr?1"`, 1)},
		{name: "thread fragment", body: strings.Replace(baseBody, `"thr_1"`, `"thr#1"`, 1)},
		{name: "thread backslash", body: strings.Replace(baseBody, `"thr_1"`, `"thr\\\\1"`, 1)},
		{name: "user NUL", body: strings.Replace(baseBody, `"hello"`, `"bad\u0000text"`, 1)},
		{name: "user over bridge limit", body: strings.Replace(baseBody, `"hello"`, `"`+strings.Repeat("x", maxAgentTurnUserTextBytes+1)+`"`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Local-Token", token)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("status=%d body=%s, want 400", response.StatusCode, body)
			}
		})
	}
}

// fixture builds a Server backed by a stub workmax.app upstream and starts
// it on a test listener. Caller gets the running base URL + a teardown.
func seedServerTestThread(t *testing.T, db *gorm.DB, uid uint64, uuid string) uint64 {
	t.Helper()
	return seedServerTestThreadWithCloudID(t, db, uid, uuid, "9001")
}

func seedServerTestThreadWithCloudID(t *testing.T, db *gorm.DB, uid uint64, uuid, cloudThreadID string) uint64 {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_thread_id) VALUES (?, ?, ?, ?)`,
		uid, uuid, "Test Thread", cloudThreadID,
	).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	var id uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, uuid).Row().Scan(&id); err != nil {
		t.Fatalf("load seeded thread id: %v", err)
	}
	return id
}

func newServerFixture(t *testing.T, upstreamHandler http.HandlerFunc) (baseURL, localToken string) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr_1")
	return newServerFixtureWithDB(t, db, upstreamHandler)
}

func newServerFixtureWithDB(t *testing.T, db *gorm.DB, upstreamHandler http.HandlerFunc) (baseURL, localToken string) {
	t.Helper()
	_, baseURL, localToken = newServerFixtureExposingServer(t, db, upstreamHandler)
	return baseURL, localToken
}

// newServerFixtureExposingServer is newServerFixtureWithDB but also hands back
// the *Server, for tests that assert on internal state (e.g. the turn lock
// registry) after driving the HTTP surface.
func newServerFixtureExposingServer(t *testing.T, db *gorm.DB, upstreamHandler http.HandlerFunc) (srv *Server, baseURL, localToken string) {
	t.Helper()
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)

	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		DeviceID:       "dev",
		TokenStore:     store,
		Proxy:          proxy,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	// Serve on the server's listener in a goroutine and tear down on
	// test completion.
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	baseURL = "http://" + srv.listener.Addr().String()
	return srv, baseURL, "tok"
}

func TestHandleAgentChat_HappyPath(t *testing.T) {
	base, tok := newServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		io.WriteString(w, "event: text\ndata: {\"text\":\"hi\"}\n\n")
		flusher.Flush()
		io.WriteString(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	})

	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(`{
		"turn_uuid": "de305d54-75b4-431b-adb2-eb6b9e546014",
		"thread_uuid": "thr_1",
		"user_text": "hi",
		"chat_mode": "ppt",
		"payload": {"stream":true}
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type: got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	if !strings.Contains(got, "event: text") {
		t.Errorf("missing text event: %q", got)
	}
	if !strings.Contains(got, "event: done") {
		t.Errorf("missing done event: %q", got)
	}
}

func TestHandleAgentChat_MissingThreadUUID(t *testing.T) {
	base, tok := newServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not have been called")
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat",
		bytes.NewReader([]byte(`{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid_thread_uuid") {
		t.Errorf("body should mention thread_uuid: %q", body)
	}
}

func TestHandleAgentChat_RejectsBlankUserText(t *testing.T) {
	base, tok := newServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not have been called for blank user_text")
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat",
		bytes.NewReader([]byte(`{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"   ","chat_mode":"ppt","payload":{"stream":true}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "invalid_user_text") {
		t.Errorf("body should mention user_text: %q", body)
	}
}

func TestHandleAgentChat_NoProxyConfigured(t *testing.T) {
	db := openServerTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		// no Proxy
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
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat",
		bytes.NewReader([]byte(`{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`)))
	req.Header.Set("Content-Type", "application/json")
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

func TestHandleAgentChat_RejectsOversizeBody(t *testing.T) {
	base, tok := newServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not have been called for oversize local body")
	})
	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hi","chat_mode":"ppt","payload":{"stream":true,"blob":"` +
		strings.Repeat("x", maxAgentChatBodyBytes) +
		`"}}`
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "chat request body too large") {
		t.Fatalf("body should explain size rejection: %q", body)
	}
}

func TestHandleAgentChat_UUIDRequiresDB(t *testing.T) {
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cloud := cloudproxy.NewClient("https://example.invalid")
	proxy := cloudproxy.NewProxy(cloud, store, nil)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	srv := &Server{
		cfg: ServerConfig{
			SidecarVersion: "test",
			LocalToken:     "tok",
			TokenStore:     store,
			Proxy:          proxy,
			// DB intentionally nil: UUID-only requests need local SQLite
			// to resolve the cache writer's numeric thread_id.
		},
	}
	router.POST("/agent/chat", srv.handleAgentChat)

	req, _ := http.NewRequest(http.MethodPost, "/agent/chat",
		bytes.NewReader([]byte(`{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr-a","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "agent_turn_recovery_unavailable") {
		t.Errorf("body should mention missing db: %q", rec.Body.String())
	}
}

func TestHandleAgentChat_NewServerRequiresDB(t *testing.T) {
	_, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		// DB intentionally nil: production boot should fail before
		// any endpoint can dereference SQLite dependencies.
	})
	if err == nil || !strings.Contains(err.Error(), "ServerConfig.DB is required") {
		t.Fatalf("err: got %v, want DB required", err)
	}
}

func TestHandleAgentChat_RejectsMissingLocalToken(t *testing.T) {
	base, _ := newServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not have been called")
	})
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat",
		bytes.NewReader([]byte(`{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`)))
	req.Header.Set("Content-Type", "application/json")
	// no X-Local-Token
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// The middleware returns 403 (not 401) for a missing token —
	// matches workmax convention for "token present is mandatory":
	// 401 is reserved for "you have an identity but it's invalid",
	// 403 for "you didn't even try".
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status: got %d, want 403", resp.StatusCode)
	}
}

// TestHandleAgentChat_ResolvesUUIDToID: P1.B.6 prereq — the renderer
// only carries thread UUIDs (the cross-cloud-and-local stable id);
// the handler must resolve to the numeric local PK that cache_writer
// needs for w_workagent_message.thread_id.
func TestHandleAgentChat_ResolvesUUIDToID(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-uuid-target")
	baseURL, tok := newServerFixtureWithDB(t, db, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		io.WriteString(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	})

	// POST with thread_uuid only (no thread_id) — handler must resolve.
	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr-uuid-target","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/agent/chat", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Errorf("status: got %d, want 200; body: %s", resp.StatusCode, bodyBytes)
	}
}

func TestHandleAgentChat_UnknownUUIDReturns404(t *testing.T) {
	base, tok := newServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called when uuid resolution fails")
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"does-not-exist","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d, want 404 (uuid not in local SQLite)", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(bodyBytes), "thread_not_found") {
		t.Errorf("404 body should mention not-found, got: %s", bodyBytes)
	}
}

func TestHandleAgentChat_UnusableCloudThreadIDFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name          string
		cloudThreadID string
	}{
		{name: "missing", cloudThreadID: ""},
		{name: "zero", cloudThreadID: "0"},
		{name: "malformed", cloudThreadID: "not-a-number"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var cloudCalled atomic.Bool
			db := openServerTestDB(t)
			seedServerTestThreadWithCloudID(t, db, 42, "thr-local-only", test.cloudThreadID)
			base, tok := newServerFixtureWithDB(t, db, func(w http.ResponseWriter, r *http.Request) {
				cloudCalled.Store(true)
				w.WriteHeader(http.StatusInternalServerError)
			})

			reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr-local-only","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`
			req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(reqBody)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Local-Token", tok)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusConflict {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status: got %d, want 409; body: %s", resp.StatusCode, body)
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), "thread_not_ready") || !strings.Contains(string(body), "thr-local-only") {
				t.Fatalf("closed response should identify the unsynced thread without trusting payload: %s", body)
			}
			if cloudCalled.Load() {
				t.Fatal("chat with unusable cloud_thread_id reached the cloud upstream")
			}
			var state, errorKind string
			if err := db.Raw(
				`SELECT state, last_error_kind FROM w_desktop_agent_turn_intent WHERE turn_uuid = ?`,
				serverTestTurnUUID,
			).Row().Scan(&state, &errorKind); err != nil {
				t.Fatalf("load pre-SSE turn outcome: %v", err)
			}
			if state != agentTurnIntentInterrupted || errorKind != "thread_not_ready" {
				t.Fatalf("pre-SSE failure left intent %s/%s, want interrupted/thread_not_ready", state, errorKind)
			}
		})
	}
}

func TestHandleAgentChat_RejectsRendererUIDAuthority(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-owned")
	base, tok := newServerFixtureWithDB(t, db, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not accept renderer-supplied uid")
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr-owned","uid":99,"user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body: %s", resp.StatusCode, body)
	}
}

func TestHandleAgentChat_UUIDResolutionFiltersByActiveTokenUID(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 99, "thr-other-account")
	base, tok := newServerFixtureWithDB(t, db, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called for another account's local thread")
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr-other-account","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", resp.StatusCode)
	}
}

func TestHandleAgentChat_RejectsLegacyThreadIDAuthority(t *testing.T) {
	db := openServerTestDB(t)
	threadID := seedServerTestThread(t, db, 99, "thr-other-account")
	base, tok := newServerFixtureWithDB(t, db, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not accept renderer-supplied thread_id")
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_id":` + uint64ToString(threadID) + `,"thread_uuid":"thr-other-account","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestHandleAgentChat_RejectsAnyThreadIDAndUUIDCombination(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr-owned-a")
	threadB := seedServerTestThread(t, db, 42, "thr-owned-b")
	base, tok := newServerFixtureWithDB(t, db, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called for mismatched local identifiers")
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_id":` + uint64ToString(threadB) + `,"thread_uuid":"thr-owned-a","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", resp.StatusCode)
	}
}

func TestHandleAgentChat_ConfiguredTokenStoreWithoutSessionReturns401(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr_1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called without an active desktop session")
	}))
	t.Cleanup(upstream.Close)

	store := cloudproxy.NewTokenStore(newMemKeychain())
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
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

	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.listener.Addr().String()+"/agent/chat",
		bytes.NewReader([]byte(`{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", "tok")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", resp.StatusCode)
	}
}

func TestHandleAgentChat_ExpiredRefreshReturns401(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr_1")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be called with an expired desktop session")
	}))
	t.Cleanup(upstream.Close)

	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "expired-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(-time.Minute),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
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

	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.listener.Addr().String()+"/agent/chat",
		bytes.NewReader([]byte(`{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", "tok")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", resp.StatusCode)
	}
}
