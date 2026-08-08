//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
	desktopsync "server/desktop/sync"
)

const sidecarPutThreadTestUUID = "de305d54-75b4-431b-adb2-eb6b9e546014"

func openSidecarPutThreadTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := openHistoryTestDB(t)
	statements := []string{
		`ALTER TABLE w_workagent_thread ADD COLUMN model TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE w_workagent_thread ADD COLUMN msg_preview TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE w_workagent_thread ADD COLUMN file_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE w_workagent_thread ADD COLUMN is_public INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE w_workagent_thread ADD COLUMN last_synced_at TEXT`,
		`CREATE UNIQUE INDEX uk_sidecar_put_thread_uuid ON w_workagent_thread(uuid)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare PUT thread DB: %v", err)
		}
	}
	return db
}

func sidecarPutThreadCloudResponse(threadUUID string, created bool) string {
	return fmt.Sprintf(`{"thread":{"cloud_thread_id":"42","uuid":%q,"name":"Design deck","agent_mode":"ppt","agent_type":"general_agent","model":"work-pro","message_count":0,"msg_preview":"","file_count":0,"is_public":false,"updated_at":"2026-08-06T10:00:00Z","created_at":"2026-08-06T09:00:00Z"},"created":%t}`, threadUUID, created)
}

type sidecarPutThreadFixture struct {
	baseURL string
	db      *gorm.DB
	store   *cloudproxy.TokenStore
}

func bootSidecarPutThreadFixture(
	t *testing.T,
	db *gorm.DB,
	upstreamHandler http.Handler,
	threadsSyncer *desktopsync.SyncWorker,
) sidecarPutThreadFixture {
	t.Helper()
	upstream := httptest.NewServer(upstreamHandler)
	t.Cleanup(upstream.Close)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(42), "old-refresh")); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "thread-put-test",
		LocalToken:     "thread-put-token",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
		ThreadsSyncer:  threadsSyncer,
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
	return sidecarPutThreadFixture{
		baseURL: "http://" + srv.listener.Addr().String(),
		db:      db,
		store:   store,
	}
}

func doSidecarPutThread(t *testing.T, baseURL, threadUUID, body string) (int, string) {
	t.Helper()
	status, responseBody, err := executeSidecarPutThread(baseURL, threadUUID, body)
	if err != nil {
		t.Fatalf("PUT local thread: %v", err)
	}
	return status, responseBody
}

func executeSidecarPutThread(baseURL, threadUUID, body string) (int, string, error) {
	request, err := http.NewRequest(
		http.MethodPut,
		baseURL+"/agent/threads/"+threadUUID,
		strings.NewReader(body),
	)
	if err != nil {
		return 0, "", err
	}
	request.Header.Set("X-Local-Token", "thread-put-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", err
	}
	return response.StatusCode, strings.TrimSpace(string(responseBody)), nil
}

// TestHandlePutAgentThread_LocalRoute 验证 preferred_route=local 时线程创建在
// 本地 SQLite 完成，不调云端 PutThread：返回 201 + LocalThreadRow、cloud_thread_id
// 为 NULL、cloud_sync_state='local'；重复 PUT 同 uuid 幂等返回 200 created=false。
func TestHandlePutAgentThread_LocalRoute(t *testing.T) {
	db := openSidecarPutThreadTestDB(t)
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS w_desktop_model_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    preferred_route TEXT NOT NULL DEFAULT 'official',
    local_protocol TEXT NOT NULL DEFAULT '',
    local_base_url TEXT NOT NULL DEFAULT '',
    local_model_id TEXT NOT NULL DEFAULT '',
    local_api_key_present INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
)`).Error; err != nil {
		t.Fatalf("create model_settings table: %v", err)
	}
	modelSettings := NewLocalModelSettingsStore(db, newMemKeychain())
	if _, err := modelSettings.Put(LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
		},
	}); err != nil {
		t.Fatalf("put local settings: %v", err)
	}

	var cloudPutCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/api/desktop/agent/threads/") {
			cloudPutCalls.Add(1)
		}
		w.WriteHeader(http.StatusTeapot) // 不应被调用；若被调会让 cloudPutCalls 断言失败
	}))
	t.Cleanup(upstream.Close)

	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(42), "refresh")); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "l3a-test",
		LocalToken:     "thread-put-token",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
		ModelSettings:  modelSettings,
		LocalInference: &fakeLocalRunner{}, // 满足 shouldUseLocalRoute；本地建线程不实际调用它
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
	baseURL := "http://" + srv.listener.Addr().String()

	body := `{"name":"My Local Thread","agent_mode":"ppt"}`
	status, respBody := doSidecarPutThread(t, baseURL, sidecarPutThreadTestUUID, body)
	if status != http.StatusCreated {
		t.Fatalf("first PUT status=%d body=%s, want 201", status, respBody)
	}
	if cloudPutCalls.Load() != 0 {
		t.Fatalf("local route must not call cloud PutThread, got %d call(s)", cloudPutCalls.Load())
	}
	var (
		cloudID   *string
		syncState string
	)
	if err := db.Raw(`SELECT cloud_thread_id, cloud_sync_state FROM w_workagent_thread WHERE uuid = ?`, sidecarPutThreadTestUUID).Row().Scan(&cloudID, &syncState); err != nil {
		t.Fatalf("scan thread: %v", err)
	}
	if cloudID != nil {
		t.Fatalf("local thread cloud_thread_id = %q, want NULL", *cloudID)
	}
	if syncState != "local" {
		t.Fatalf("cloud_sync_state = %q, want local", syncState)
	}

	// 幂等：重复 PUT 同 uuid → 200 created=false，仍 1 行。
	status2, _ := doSidecarPutThread(t, baseURL, sidecarPutThreadTestUUID, body)
	if status2 != http.StatusOK {
		t.Fatalf("idempotent re-PUT status=%d, want 200", status2)
	}
	var rowCount int
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_thread WHERE uuid = ?`, sidecarPutThreadTestUUID).Row().Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("idempotent re-PUT left %d rows, want 1", rowCount)
	}
}

func TestHandlePutAgentThread_401RefreshRetriesOnceWithSameUUIDAndBody(t *testing.T) {
	oldAccess := mintLocalHistoryJWT(42)
	freshAccess := oldAccess + "fresh-signature"
	var threadCalls atomic.Int32
	var refreshCalls atomic.Int32
	var requestBodiesMu sync.Mutex
	var requestBodies []string
	var authorizations []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/agent/threads/"+sidecarPutThreadTestUUID, func(w http.ResponseWriter, r *http.Request) {
		call := threadCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		requestBodiesMu.Lock()
		requestBodies = append(requestBodies, string(body))
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		requestBodiesMu.Unlock()
		if call == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			// Error bodies are not part of the create contract. Even an oversized,
			// non-JSON 401 must reach the once-only refresh path.
			_, _ = io.WriteString(w, strings.Repeat("untrusted-401-body", 8<<10))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, sidecarPutThreadCloudResponse(sidecarPutThreadTestUUID, true))
	})
	mux.HandleFunc(cloudproxy.CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse refresh: %v", err)
		}
		if got := r.Form.Get("refresh_token"); got != "old-refresh" {
			t.Errorf("refresh token = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"refresh_token":"new-refresh","refresh_expires_in":86400,"scope":"workagent"}`, freshAccess)
	})
	db := openSidecarPutThreadTestDB(t)
	fixture := bootSidecarPutThreadFixture(t, db, mux, nil)

	status, body := doSidecarPutThread(
		t,
		fixture.baseURL,
		sidecarPutThreadTestUUID,
		`{"name":"Design deck","agent_mode":"ppt"}`,
	)
	if status != http.StatusCreated {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if threadCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("thread calls=%d refresh calls=%d, want 2/1", threadCalls.Load(), refreshCalls.Load())
	}
	requestBodiesMu.Lock()
	if len(requestBodies) != 2 || requestBodies[0] != requestBodies[1] ||
		requestBodies[0] != `{"name":"Design deck","agent_mode":"ppt"}` {
		t.Fatalf("retry bodies = %#v", requestBodies)
	}
	if len(authorizations) != 2 || authorizations[0] != "Bearer "+oldAccess || authorizations[1] != "Bearer "+freshAccess {
		t.Fatalf("retry authorizations = %#v", authorizations)
	}
	requestBodiesMu.Unlock()
	if strings.Contains(body, `"uid"`) || strings.Contains(body, "cloud_thread_id") || strings.Contains(body, `"id"`) {
		t.Fatalf("response leaked cloud/account identity: %s", body)
	}
	var rowCount int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_thread WHERE uid = 42 AND uuid = ? AND cloud_thread_id = '42'`, sidecarPutThreadTestUUID).Row().Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("local canonical rows=%d, want 1", rowCount)
	}
}

func TestHandlePutAgentThread_ReplayReturnsCommittedPausedLocalRow(t *testing.T) {
	db := openSidecarPutThreadTestDB(t)
	if err := db.Exec(`
		INSERT INTO w_workagent_thread
			(uid, uuid, name, agent_mode, agent_type, model, message_count,
			 msg_preview, file_count, is_public, cloud_sync_state,
			 cloud_thread_id, created_at, updated_at)
		VALUES
			(42, ?, 'Old local name', 'ppt', 'general_agent', 'old-model', 3,
			 'old preview', 1, 0, 'paused', '42',
			 '2026-08-06T08:00:00Z', '2026-08-06T08:00:00Z')`,
		sidecarPutThreadTestUUID,
	).Error; err != nil {
		t.Fatalf("seed paused row: %v", err)
	}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sidecarPutThreadCloudResponse(sidecarPutThreadTestUUID, false))
	})
	fixture := bootSidecarPutThreadFixture(t, db, upstream, nil)

	status, body := doSidecarPutThread(t, fixture.baseURL, sidecarPutThreadTestUUID, `{"name":"Design deck","agent_mode":"ppt"}`)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var response putAgentThreadResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.State != "ready" || response.Created ||
		response.Thread.UUID != sidecarPutThreadTestUUID ||
		response.Thread.Name != "Design deck" ||
		response.Thread.MessageCount != 0 ||
		response.Thread.CloudSync != "paused" {
		t.Fatalf("response did not reflect committed row: %+v", response)
	}
	if strings.Contains(body, "cloud_thread_id") || strings.Contains(body, `"uid"`) {
		t.Fatalf("response leaked cloud/account identity: %s", body)
	}
}

func TestHandlePutAgentThread_Second401StopsWithoutLocalWrite(t *testing.T) {
	var threadCalls atomic.Int32
	var refreshCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/agent/threads/"+sidecarPutThreadTestUUID, func(w http.ResponseWriter, r *http.Request) {
		threadCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc(cloudproxy.CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		freshAccess := mintLocalHistoryJWT(42) + "fresh-signature"
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"refresh_token":"new-refresh","refresh_expires_in":86400,"scope":"workagent"}`, freshAccess)
	})
	db := openSidecarPutThreadTestDB(t)
	fixture := bootSidecarPutThreadFixture(t, db, mux, nil)

	status, body := doSidecarPutThread(t, fixture.baseURL, sidecarPutThreadTestUUID, `{"name":"Design deck","agent_mode":"ppt"}`)
	if status != http.StatusUnauthorized || !strings.Contains(body, "authentication_required") {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if threadCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("thread calls=%d refresh calls=%d", threadCalls.Load(), refreshCalls.Load())
	}
	assertNoSidecarPutThreadRows(t, db)
}

func TestHandlePutAgentThread_RefreshSubjectMismatchFencesSession(t *testing.T) {
	var threadCalls atomic.Int32
	var refreshCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/agent/threads/"+sidecarPutThreadTestUUID, func(w http.ResponseWriter, r *http.Request) {
		threadCalls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc(cloudproxy.CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600,"refresh_token":"wrong-subject-refresh","refresh_expires_in":86400,"scope":"workagent"}`, mintLocalHistoryJWT(84))
	})
	db := openSidecarPutThreadTestDB(t)
	fixture := bootSidecarPutThreadFixture(t, db, mux, nil)

	status, body := doSidecarPutThread(t, fixture.baseURL, sidecarPutThreadTestUUID, `{"name":"Design deck","agent_mode":"ppt"}`)
	if status != http.StatusUnauthorized || !strings.Contains(body, "authentication_required") {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if threadCalls.Load() != 1 || refreshCalls.Load() != 1 {
		t.Fatalf("wrong-subject refresh replayed: thread=%d refresh=%d", threadCalls.Load(), refreshCalls.Load())
	}
	if _, err := fixture.store.AcquireSessionLease(); !errors.Is(err, cloudproxy.ErrSessionChanged) {
		t.Fatalf("mismatched refresh session remained usable: %v", err)
	}
	assertNoSidecarPutThreadRows(t, db)
}

func TestHandlePutAgentThread_LocalFailureReturns202AndTriggersReconcile(t *testing.T) {
	db := openSidecarPutThreadTestDB(t)
	if err := db.Exec(`CREATE TRIGGER reject_sidecar_created_thread
		BEFORE INSERT ON w_workagent_thread
		BEGIN
			SELECT RAISE(ABORT, 'forced local failure');
		END`).Error; err != nil {
		t.Fatal(err)
	}
	jobCalled := make(chan struct{}, 2)
	worker := desktopsync.NewSyncWorker(func(ctx context.Context) error {
		jobCalled <- struct{}{}
		return nil
	}, desktopsync.Config{PeriodicInterval: 0})
	workerContext, cancelWorker := context.WithCancel(context.Background())
	worker.Start(workerContext)
	select {
	case <-jobCalled: // startup
	case <-time.After(time.Second):
		t.Fatal("startup sync did not run")
	}
	t.Cleanup(func() {
		cancelWorker()
		select {
		case <-worker.Done():
		case <-time.After(time.Second):
			t.Error("sync worker did not stop")
		}
	})
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, sidecarPutThreadCloudResponse(sidecarPutThreadTestUUID, true))
	})
	fixture := bootSidecarPutThreadFixture(t, db, upstream, worker)

	status, body := doSidecarPutThread(t, fixture.baseURL, sidecarPutThreadTestUUID, `{"name":"Design deck","agent_mode":"ppt"}`)
	if status != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if body != `{"state":"pending_local_sync","thread_uuid":"`+sidecarPutThreadTestUUID+`"}` {
		t.Fatalf("pending response=%s", body)
	}
	if strings.Contains(body, "cloud_thread_id") || strings.Contains(body, `"uid"`) {
		t.Fatalf("pending response leaked identity: %s", body)
	}
	select {
	case <-jobCalled:
	case <-time.After(time.Second):
		t.Fatal("local failure did not trigger reconciliation")
	}
	assertNoSidecarPutThreadRows(t, db)
}

func TestHandlePutAgentThread_SessionReplacementReturns409AndWritesNoAccount(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, sidecarPutThreadCloudResponse(sidecarPutThreadTestUUID, true))
	})
	db := openSidecarPutThreadTestDB(t)
	fixture := bootSidecarPutThreadFixture(t, db, upstream, nil)
	type result struct {
		status int
		body   string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		status, body, err := executeSidecarPutThread(fixture.baseURL, sidecarPutThreadTestUUID, `{"name":"Design deck","agent_mode":"ppt"}`)
		done <- result{status: status, body: body, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cloud PUT did not start")
	}
	if err := fixture.store.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(84), "replacement-refresh")); err != nil {
		t.Fatalf("replace session: %v", err)
	}
	close(release)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("PUT local thread: %v", got.err)
		}
		if got.status != http.StatusConflict || got.body != `{"error":"session_changed"}` {
			t.Fatalf("status=%d body=%s", got.status, got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("session-changed PUT did not finish")
	}
	assertNoSidecarPutThreadRows(t, db)
}

func TestHandlePutAgentThread_CloudConflictDoesNotLeakOrWrite(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	db := openSidecarPutThreadTestDB(t)
	fixture := bootSidecarPutThreadFixture(t, db, upstream, nil)
	status, body := doSidecarPutThread(t, fixture.baseURL, sidecarPutThreadTestUUID, `{"name":"Design deck","agent_mode":"ppt"}`)
	if status != http.StatusConflict || body != `{"error":"thread_uuid_conflict"}` {
		t.Fatalf("status=%d body=%s", status, body)
	}
	assertNoSidecarPutThreadRows(t, db)
}

func TestPutAgentThreadStrictLocalInput(t *testing.T) {
	validBody := `{"name":"Design deck","agent_mode":"ppt"}`
	request := httptest.NewRequest(http.MethodPut, "/agent/threads/"+sidecarPutThreadTestUUID, strings.NewReader(validBody))
	decoded, err := decodePutAgentThreadRequest(request)
	if err != nil || decoded.Name != "Design deck" || decoded.AgentMode != "ppt" {
		t.Fatalf("valid body decoded=%+v err=%v", decoded, err)
	}

	invalidBodies := [][]byte{
		[]byte(`{"name":"N","agent_mode":"ppt","extra":1}`),
		[]byte(`{"name":"N","name":"N","agent_mode":"ppt"}`),
		[]byte(`{"name":"N"}`),
		[]byte(`{"name":"N","agent_mode":"ppt"}{}`),
		[]byte(`[]`),
		bytes.Repeat([]byte("x"), maxAgentThreadPutBodyBytes+1),
		{0xff, 0xfe},
	}
	for index, body := range invalidBodies {
		request := httptest.NewRequest(http.MethodPut, "/agent/threads/"+sidecarPutThreadTestUUID, bytes.NewReader(body))
		if _, err := decodePutAgentThreadRequest(request); err == nil {
			t.Errorf("invalid body %d was accepted", index)
		}
	}

	for _, candidate := range []string{
		"",
		"de305d54-75b4-11d3-adb2-eb6b9e546014",
		"de305d54-75b4-431b-c456-eb6b9e546014",
		strings.ToUpper(sidecarPutThreadTestUUID),
		" " + sidecarPutThreadTestUUID,
	} {
		if _, err := canonicalDesktopThreadUUID(candidate); err == nil {
			t.Errorf("invalid UUID %q was accepted", candidate)
		}
	}
	if got, err := canonicalDesktopThreadUUID(sidecarPutThreadTestUUID); err != nil || got != sidecarPutThreadTestUUID {
		t.Fatalf("canonical UUID got=%q err=%v", got, err)
	}

	for _, candidate := range []string{"", " ", "bad\nname", strings.Repeat("n", 201)} {
		if _, err := normalizeDesktopThreadName(candidate); err == nil {
			t.Errorf("invalid name was accepted: %q", candidate)
		}
	}
}

func assertNoSidecarPutThreadRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	var rows int64
	if err := db.Raw(`SELECT count(*) FROM w_workagent_thread`).Row().Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("unexpected local thread rows=%d", rows)
	}
}

func TestPutAgentThreadReadyResponseShapeIsClosed(t *testing.T) {
	response := putAgentThreadResponse{
		State:   "ready",
		Created: true,
		Thread: LocalThreadRow{
			UUID:         sidecarPutThreadTestUUID,
			Name:         "Design deck",
			AgentMode:    "ppt",
			MessageCount: 0,
			UpdatedAt:    time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC),
			CloudSync:    "synced",
		},
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "cloud_thread_id") || strings.Contains(string(body), `"uid"`) {
		t.Fatalf("ready response leaked authority: %s", body)
	}
}
