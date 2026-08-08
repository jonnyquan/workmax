//go:build desktop

package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
	desktopsync "server/desktop/sync"
)

// bootHistoryServer stands up a sidecar Server with the history-test
// schema (thread + message tables) seeded by the test. No Proxy /
// TokenStore needed since the read endpoints are local-only.
func bootHistoryServer(t *testing.T) (baseURL, tok string, db *gorm.DB) {
	t.Helper()
	db = openHistoryTestDB(t)
	return bootHistoryServerWithDBAndTokenStore(t, db, nil)
}

func bootHistoryServerWithDBAndTokenStore(t *testing.T, db *gorm.DB, store *cloudproxy.TokenStore) (baseURL, tok string, outDB *gorm.DB) {
	t.Helper()
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
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
	return "http://" + srv.listener.Addr().String(), "tok", db
}

func newHistoryTokenStore(t *testing.T, uid uint64) *cloudproxy.TokenStore {
	t.Helper()
	return newHistoryTokenStoreWithRefreshExpiry(t, uid, time.Now().UTC().Add(24*time.Hour))
}

func newHistoryTokenStoreWithRefreshExpiry(t *testing.T, uid uint64, refreshExpiresAt time.Time) *cloudproxy.TokenStore {
	t.Helper()
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(uid),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: refreshExpiresAt,
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func mintLocalHistoryJWT(uid uint64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"Id":` + uint64ToString(uid) + `,"exp":9999999999}`))
	signature := base64.RawURLEncoding.EncodeToString([]byte("test-signature"))
	return header + "." + payload + "." + signature
}

func uint64ToString(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func TestHandleListThreads_HappyPath(t *testing.T) {
	base, tok, db := bootHistoryServer(t)
	seedThread(t, db, 0, "thr_oldest", "Old", "ppt", 100)
	seedThread(t, db, 0, "thr_newest", "New", "ppt", 5)

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listThreadsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 || len(body.Items) != 2 {
		t.Fatalf("got %+v", body)
	}
	if body.Items[0].UUID != "thr_newest" {
		t.Errorf("first item: got %q, want thr_newest", body.Items[0].UUID)
	}
}

func TestHandleListThreads_LimitQuery(t *testing.T) {
	base, tok, db := bootHistoryServer(t)
	for i := 0; i < 5; i++ {
		seedThread(t, db, 0, "thr_"+string('a'+rune(i)), "T", "ppt", i*5)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads?limit=3", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body listThreadsResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Count != 3 {
		t.Errorf("?limit=3 gave count %d", body.Count)
	}
}

func TestHandleListThreads_RejectsBadLimit(t *testing.T) {
	base, tok, db := bootHistoryServer(t)
	seedThread(t, db, 0, "thr_1", "T", "ppt", 0)

	for _, query := range []string{
		"limit=nan",
		"limit=0",
		"limit=501",
		"limit=",
		"limit=1&limit=2",
		"limit=%201",
		"limit=1%0A",
	} {
		req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads?"+query, nil)
		req.Header.Set("X-Local-Token", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", query, resp.StatusCode)
		}
	}
}

func TestHandleListThreads_IncludePausedFalse(t *testing.T) {
	base, tok, db := bootHistoryServer(t)
	seedThread(t, db, 0, "thr_active", "Active", "ppt", 5)
	seedThread(t, db, 0, "thr_paused", "Paused", "ppt", 1)
	if err := db.Exec(`UPDATE w_workagent_thread SET cloud_sync_state = 'paused' WHERE uuid = 'thr_paused'`).Error; err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads?include_paused=false", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listThreadsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Items) != 1 || body.Items[0].UUID != "thr_active" {
		t.Fatalf("include_paused=false body: %+v", body)
	}
}

func TestHandleListThreads_RejectsBadIncludePaused(t *testing.T) {
	base, tok, db := bootHistoryServer(t)
	seedThread(t, db, 0, "thr_1", "T", "ppt", 0)

	for _, query := range []string{
		"include_paused=",
		"include_paused=1",
		"include_paused=False",
		"include_paused=false&include_paused=true",
		"include_paused=%20false",
		"include_paused=false%0A",
	} {
		req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads?"+query, nil)
		req.Header.Set("X-Local-Token", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", query, resp.StatusCode)
		}
	}
}

func TestHandleListThreads_FiltersByActiveTokenUID(t *testing.T) {
	db := openHistoryTestDB(t)
	seedThread(t, db, 7, "thr_user7", "Mine", "ppt", 5)
	seedThread(t, db, 99, "thr_user99", "Theirs", "ppt", 1)
	base, tok, _ := bootHistoryServerWithDBAndTokenStore(t, db, newHistoryTokenStore(t, 7))

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listThreadsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Items) != 1 || body.Items[0].UUID != "thr_user7" {
		t.Fatalf("active uid should only see its own cached threads: %+v", body)
	}
}

func TestHandleListThreads_ConfiguredTokenStoreWithoutSessionShowsNoCachedRows(t *testing.T) {
	db := openHistoryTestDB(t)
	seedThread(t, db, 7, "thr_user7", "Mine", "ppt", 5)
	base, tok, _ := bootHistoryServerWithDBAndTokenStore(t, db, cloudproxy.NewTokenStore(newMemKeychain()))

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listThreadsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Items) != 0 {
		t.Fatalf("configured TokenStore with no session should not expose cache: %+v", body)
	}
}

func TestHandleListThreads_ExpiredRefreshShowsNoCachedRows(t *testing.T) {
	db := openHistoryTestDB(t)
	seedThread(t, db, 7, "thr_user7", "Mine", "ppt", 5)
	store := newHistoryTokenStoreWithRefreshExpiry(t, 7, time.Now().UTC().Add(-time.Minute))
	base, tok, _ := bootHistoryServerWithDBAndTokenStore(t, db, store)

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listThreadsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Items) != 0 {
		t.Fatalf("expired refresh token should not expose cached threads: %+v", body)
	}
}

func TestHandleListMessages_HappyPath(t *testing.T) {
	base, tok, db := bootHistoryServer(t)
	threadID := seedThread(t, db, 0, "thr_chat", "Chat", "ppt", 0)
	seedMessage(t, db, threadID, "msg_1", "say hi", "hello", "ppt", "complete")
	seedMessage(t, db, threadID, "msg_2", "again", "world", "ppt", "partial")

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/thr_chat/messages", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 {
		t.Fatalf("count: %d, want 2", body.Count)
	}
	if body.Items[0].UUID != "msg_1" || body.Items[1].StreamingState != "partial" {
		t.Errorf("rows: %+v", body.Items)
	}
}

func TestHandleListMessages_FiltersByActiveTokenUID(t *testing.T) {
	db := openHistoryTestDB(t)
	mine := seedThread(t, db, 7, "thr_mine", "Mine", "ppt", 0)
	theirs := seedThread(t, db, 99, "thr_theirs", "Theirs", "ppt", 0)
	seedMessage(t, db, mine, "msg_mine", "mine", "ok", "ppt", "complete")
	seedMessage(t, db, theirs, "msg_theirs", "theirs", "hidden", "ppt", "complete")
	base, tok, _ := bootHistoryServerWithDBAndTokenStore(t, db, newHistoryTokenStore(t, 7))

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/thr_theirs/messages", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Items) != 0 {
		t.Fatalf("active uid should not see another account's cached messages: %+v", body)
	}
}

func TestHandleListMessages_FiltersMessageRowsByActiveTokenUID(t *testing.T) {
	db := openHistoryTestDB(t)
	threadID := seedThread(t, db, 7, "thr_mine", "Mine", "ppt", 0)
	seedMessage(t, db, threadID, "msg_mine", "mine", "ok", "ppt", "complete")
	if err := db.Exec(
		`INSERT INTO w_workagent_message
			(uid, uuid, thread_id, user_text, ai_text, chat_mode, streaming_state, created_at, updated_at)
		 VALUES
			(99, 'msg_wrong_uid', ?, 'other user', 'hidden', 'ppt', 'complete', '2026-05-21T00:00:00Z', '2026-05-21T00:00:00Z')`,
		threadID,
	).Error; err != nil {
		t.Fatalf("seed wrong uid message: %v", err)
	}
	base, tok, _ := bootHistoryServerWithDBAndTokenStore(t, db, newHistoryTokenStore(t, 7))

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/thr_mine/messages", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Items) != 1 || body.Items[0].UUID != "msg_mine" {
		t.Fatalf("active uid should not see mismatched-uid message rows on its own thread: %+v", body)
	}
}

func TestHandleListMessages_ConfiguredTokenStoreWithoutSessionShowsNoCachedRows(t *testing.T) {
	db := openHistoryTestDB(t)
	threadID := seedThread(t, db, 7, "thr_mine", "Mine", "ppt", 0)
	seedMessage(t, db, threadID, "msg_mine", "mine", "hidden-after-logout", "ppt", "complete")
	base, tok, _ := bootHistoryServerWithDBAndTokenStore(t, db, cloudproxy.NewTokenStore(newMemKeychain()))

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/thr_mine/messages", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Items) != 0 {
		t.Fatalf("configured TokenStore with no session should not expose cached messages: %+v", body)
	}
}

func TestHandleListMessages_ExpiredRefreshShowsNoCachedRows(t *testing.T) {
	db := openHistoryTestDB(t)
	threadID := seedThread(t, db, 7, "thr_mine", "Mine", "ppt", 0)
	seedMessage(t, db, threadID, "msg_mine", "mine", "ok", "ppt", "complete")
	store := newHistoryTokenStoreWithRefreshExpiry(t, 7, time.Now().UTC().Add(-time.Minute))
	base, tok, _ := bootHistoryServerWithDBAndTokenStore(t, db, store)

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/thr_mine/messages", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Items) != 0 {
		t.Fatalf("expired refresh token should not expose cached messages: %+v", body)
	}
}

func TestHandleListMessages_MissingThreadReturnsEmpty(t *testing.T) {
	base, tok, _ := bootHistoryServer(t)
	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/thr_missing/messages", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("missing thread: status %d, want 200", resp.StatusCode)
	}
	var body listMessagesResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Count != 0 {
		t.Errorf("missing thread should return empty list, got count %d", body.Count)
	}
}

func TestHandleListMessages_RejectsMalformedQueryAndUUID(t *testing.T) {
	base, tok, db := bootHistoryServer(t)
	threadID := seedThread(t, db, 0, "thr_chat", "Chat", "ppt", 0)
	seedMessage(t, db, threadID, "msg_1", "say hi", "hello", "ppt", "complete")

	cases := []string{
		"/agent/threads/thr_chat/messages?limit=nan",
		"/agent/threads/thr_chat/messages?limit=0",
		"/agent/threads/thr_chat/messages?limit=1001",
		"/agent/threads/thr_chat/messages?limit=",
		"/agent/threads/thr_chat/messages?limit=1&limit=2",
		"/agent/threads/%20thr_chat/messages",
		"/agent/threads/thr_chat%0A/messages",
	}
	for _, path := range cases {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.Header.Set("X-Local-Token", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, resp.StatusCode)
		}
	}
}

func TestHandleListThreads_NoDBConfigured(t *testing.T) {
	// Boot a Server with the desktop tag's bare minimum so the
	// nil-DB-but-everything-else-OK path is reachable. NewServer
	// requires DB, so we instead use a Server with a nil-checked
	// handler — verify via direct route call below isn't possible.
	// Instead, this test simply confirms NewServer rejects nil DB
	// (the defensive check).
	_, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             nil,
	})
	if err == nil {
		t.Error("NewServer should reject nil DB")
	}
}

func TestStrconvAtoi(t *testing.T) {
	cases := []struct {
		in   string
		ok   bool
		want int
	}{
		{"123", true, 123},
		{"0", true, 0},
		{"", false, 0},
		{"abc", false, 0},
		{"12x", false, 0},
		{"9999999", false, 0}, // overflow > 1M
	}
	for _, tc := range cases {
		got, err := strconvAtoi(tc.in)
		if tc.ok {
			if err != nil {
				t.Errorf("strconvAtoi(%q) err: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("strconvAtoi(%q) = %d, want %d", tc.in, got, tc.want)
			}
		} else {
			if err == nil {
				t.Errorf("strconvAtoi(%q) should error, got %d", tc.in, got)
			}
		}
	}
}

// TestHandleListMessages_TriggersMessagesSync pins the Server-to-syncer subject
// handoff. The same active UID used to select the local thread must be frozen
// into the background job; otherwise its first token check suppresses the cloud
// request and exposes a swapped/missing Trigger argument.
func TestHandleListMessages_TriggersMessagesSync(t *testing.T) {
	db := openHistoryTestDB(t)
	if err := db.Exec(`CREATE TABLE _local_meta (
		key TEXT PRIMARY KEY, value TEXT NOT NULL,
		updated_at TEXT DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, cloud_thread_id, updated_at, created_at)
		 VALUES (73, 'thr-cloud', 'T', 'ppt', '777',
		         '2026-05-17T22:00:00Z', '2026-05-17T22:00:00Z')`,
	).Error; err != nil {
		t.Fatal(err)
	}

	cloudCalled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cloudproxy.CloudRouteSyncMessages {
			t.Errorf("cloud path = %q, want messages sync", r.URL.Path)
		}
		if got := r.URL.Query().Get("thread_id"); got != "777" {
			t.Errorf("cloud thread_id = %q, want 777", got)
		}
		select {
		case cloudCalled <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	}))
	t.Cleanup(upstream.Close)
	store := newHistoryTokenStore(t, 73)
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	syncer := desktopsync.NewMessagesSyncer(desktopsync.MessagesSyncerDeps{
		DB:          db,
		Cloud:       cloud,
		TokenStore:  store,
		CursorStore: desktopsync.NewCursorStore(db),
	})
	t.Cleanup(syncer.Drain)

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		MessagesSyncer: syncer,
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

	req, _ := http.NewRequest(http.MethodGet,
		"http://"+srv.listener.Addr().String()+"/agent/threads/thr-cloud/messages", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: %d, want 200", resp.StatusCode)
	}
	select {
	case <-cloudCalled:
	case <-time.After(time.Second):
		t.Fatal("messages sync did not receive the active UID-bound trigger")
	}
}

// TestHandleListMessages_NoSyncerStillReturnsLocalRows: optional
// MessagesSyncer — when nil, the handler still serves local rows
// without crashing. Used by tests + diagnostic boots that don't
// stand up the full sync chain.
func TestHandleListMessages_NoSyncerStillReturnsLocalRows(t *testing.T) {
	base, tok, db := bootHistoryServer(t) // bootHistoryServer doesn't set MessagesSyncer
	threadID := seedThread(t, db, 0, "thr-x", "T", "ppt", 0)
	seedMessage(t, db, threadID, "m-1", "hi", "ok", "ppt", "complete")

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/thr-x/messages", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body listMessagesResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Count != 1 {
		t.Errorf("count: %d, want 1 (handler should still serve local rows without a syncer)", body.Count)
	}
}
