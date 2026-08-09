//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	cloudproxy "server/desktop/cloud_proxy"

	"gorm.io/gorm"
)

func bootLocalAccountsFixture(t *testing.T) (string, string, *gorm.DB) {
	t.Helper()
	db := openLocalAccountsTestDB(t)
	base, tok := newServerFixtureWithDB(t, db, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("local accounts must never call upstream, got %s %s", r.Method, r.URL.Path)
	})
	return base, tok, db
}

func localAccountsRequest(t *testing.T, method, url, tok, body string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp, payload
}

func TestLocalAccountsListSeedsDefault(t *testing.T) {
	base, tok, _ := bootLocalAccountsFixture(t)
	resp, body := localAccountsRequest(t, http.MethodGet, base+"/local/accounts", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var parsed struct {
		Items []LocalAccount `json:"items"`
		Count int            `json:"count"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Count != 1 || len(parsed.Items) != 1 {
		t.Fatalf("expected the default account, got %s", body)
	}
	if parsed.Items[0].Name != defaultLocalAccountName || !parsed.Items[0].Active {
		t.Fatalf("default row wrong: %s", body)
	}
	// uid crosses JSON as a string: 2^62 does not survive float64 parsing.
	if !strings.Contains(string(body), `"uid":"`) {
		t.Fatalf("uid must serialize as a string: %s", body)
	}
}

func TestLocalAccountsCreateAndSelectFlow(t *testing.T) {
	base, tok, db := bootLocalAccountsFixture(t)
	resp, body := localAccountsRequest(
		t, http.MethodPost, base+"/local/accounts", tok, `{"name":"Ming"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d: %s", resp.StatusCode, body)
	}
	var created LocalAccount
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if created.Active {
		t.Fatal("created account must not be active")
	}
	// Creation alone must not move the active uid.
	if got := activeLocalAccountUID(db); got != localSingleUserUID {
		t.Fatalf("uid moved on create: %d", got)
	}

	resp, body = localAccountsRequest(
		t, http.MethodPost, base+"/local/accounts/2/select", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("select status %d: %s", resp.StatusCode, body)
	}
	if got := activeLocalAccountUID(db); got != localAccountUID(created.ID) {
		t.Fatalf("active uid = %d, want %d", got, localAccountUID(created.ID))
	}
}

func TestLocalAccountsCreateRejections(t *testing.T) {
	base, tok, _ := bootLocalAccountsFixture(t)
	cases := []struct {
		name   string
		body   string
		status int
		errKey string
	}{
		{"empty name", `{"name":"  "}`, http.StatusBadRequest, "invalid_name"},
		{"unknown field", `{"name":"a","admin":true}`, http.StatusBadRequest, "invalid_request"},
		{"not json", `name=a`, http.StatusBadRequest, "invalid_request"},
		{"trailing json", `{"name":"a"}{"name":"b"}`, http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range cases {
		resp, body := localAccountsRequest(t, http.MethodPost, base+"/local/accounts", tok, tc.body)
		if resp.StatusCode != tc.status || !strings.Contains(string(body), tc.errKey) {
			t.Errorf("%s: status %d body %s, want %d %q", tc.name, resp.StatusCode, body, tc.status, tc.errKey)
		}
	}

	// Duplicate → 409 name_taken.
	if resp, _ := localAccountsRequest(t, http.MethodPost, base+"/local/accounts", tok, `{"name":"Ming"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed create failed: %d", resp.StatusCode)
	}
	resp, body := localAccountsRequest(t, http.MethodPost, base+"/local/accounts", tok, `{"name":"Ming"}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "name_taken") {
		t.Fatalf("duplicate: status %d body %s", resp.StatusCode, body)
	}
}

func TestLocalAccountsSelectRejections(t *testing.T) {
	base, tok, db := bootLocalAccountsFixture(t)
	resp, body := localAccountsRequest(t, http.MethodPost, base+"/local/accounts/0/select", tok, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("id 0: status %d body %s", resp.StatusCode, body)
	}
	resp, body = localAccountsRequest(t, http.MethodPost, base+"/local/accounts/99/select", tok, "")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "account_not_found") {
		t.Fatalf("missing id: status %d body %s", resp.StatusCode, body)
	}
	// A failed select must not disturb the active account.
	if got := activeLocalAccountUID(db); got != localSingleUserUID {
		t.Fatalf("active uid moved on failed select: %d", got)
	}
}

// The perimeter check: like every loopback route, no local token → no answer.
func TestLocalAccountsRequireLocalToken(t *testing.T) {
	base, _, _ := bootLocalAccountsFixture(t)
	resp, _ := localAccountsRequest(t, http.MethodGet, base+"/local/accounts", "wrong", "")
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("list served without local token: %d", resp.StatusCode)
	}
}

// The wiring test: after switching accounts, a signed-out thread create must
// land under the NEW account's uid. This is what proves localRouteUID() is
// actually plumbed into the history routes rather than sitting unused.
func TestLocalAccountSwitchScopesThreadCreation(t *testing.T) {
	db := openMigratedTestDB(t)
	modelSettings := ensureLocalModelSettingsDB(t, db)
	// Signed-out creation requires the local model route to be active — the
	// same precondition the app itself needs before local-only work.
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

	// PUT create demands Proxy+TokenStore wiring even on the local branch,
	// but the token store stays EMPTY: this is the signed-out path.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("signed-out local create must not call cloud, got %s %s", r.Method, r.URL.Path)
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
		DeviceID:       "dev",
		TokenStore:     store,
		Proxy:          proxy,
		ModelSettings:  modelSettings,
		LocalInference: &fakeLocalRunner{},
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

	// Create + select the second account through the real routes.
	resp, body := localAccountsRequest(t, http.MethodPost, base+"/local/accounts", "tok", `{"name":"Ming"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create account: %d %s", resp.StatusCode, body)
	}
	var created LocalAccount
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("parse: %v", err)
	}
	resp, body = localAccountsRequest(
		t, http.MethodPost, fmt.Sprintf("%s/local/accounts/%d/select", base, created.ID), "tok", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("select account: %d %s", resp.StatusCode, body)
	}

	// Signed-out PUT create: no cloud session in this fixture, local route on.
	threadUUID := "de305d54-75b4-431b-adb2-eb6b9e546099"
	req, _ := http.NewRequest(http.MethodPut, base+"/agent/threads/"+threadUUID,
		strings.NewReader(`{"name":"Ming's thread","agent_mode":"ppt"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", "tok")
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put thread: %v", err)
	}
	defer putResp.Body.Close()
	putBody, _ := io.ReadAll(putResp.Body)
	if putResp.StatusCode != http.StatusCreated {
		t.Fatalf("put thread: %d %s", putResp.StatusCode, putBody)
	}

	var uid uint64
	if err := db.Raw(
		`SELECT uid FROM w_workagent_thread WHERE uuid = ?`, threadUUID,
	).Row().Scan(&uid); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if uid != localAccountUID(created.ID) {
		t.Fatalf("thread uid = %d, want the selected account's %d", uid, localAccountUID(created.ID))
	}
	if uid == localSingleUserUID {
		t.Fatal("thread landed on the default uid — account switch not wired into the route")
	}
}

func TestLocalAccountsRenameRoute(t *testing.T) {
	base, tok, db := bootLocalAccountsFixture(t)
	if resp, _ := localAccountsRequest(t, http.MethodPost, base+"/local/accounts", tok, `{"name":"Ming"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed: %d", resp.StatusCode)
	}
	resp, body := localAccountsRequest(t, http.MethodPatch, base+"/local/accounts/2", tok, `{"name":"明"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %s", resp.StatusCode, body)
	}
	var renamed LocalAccount
	if err := json.Unmarshal(body, &renamed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if renamed.Name != "明" || renamed.UID != localAccountUID(2) {
		t.Fatalf("renamed = %+v", renamed)
	}
	// The rename is a label change: the account still owns the same uid data.
	if resp, body := localAccountsRequest(t, http.MethodPatch, base+"/local/accounts/2", tok, `{"name":"Local"}`); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "name_taken") {
		t.Fatalf("dup rename: %d %s", resp.StatusCode, body)
	}
	if resp, _ := localAccountsRequest(t, http.MethodPatch, base+"/local/accounts/99", tok, `{"name":"x"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing rename: %d", resp.StatusCode)
	}
	_ = db
}

// Deleting an identity must take everything it owns with it — and must refuse
// while the identity is active or working.
func TestLocalAccountsDeleteCascades(t *testing.T) {
	db := openMigratedTestDB(t)
	dataDir := t.TempDir()
	modelSettings := ensureLocalModelSettingsDB(t, db)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		DeviceID:       "dev",
		DataDir:        dataDir,
		TokenStore:     cloudproxy.NewTokenStore(newMemKeychain()),
		ModelSettings:  modelSettings,
		LocalInference: &fakeLocalRunner{},
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

	// Account 2 with a thread, messages, an intent, and a workspace dir.
	resp, body := localAccountsRequest(t, http.MethodPost, base+"/local/accounts", "tok", `{"name":"Ming"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	uid := localAccountUID(2)
	mingThread := "de305d54-75b4-431b-adb2-eb6b9e546201"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state, agent_type) VALUES (?, ?, 'Ming work', 'local', 'general_agent')`,
		uid, mingThread,
	).Error; err != nil {
		t.Fatal(err)
	}
	var mingThreadID uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, mingThread).Row().Scan(&mingThreadID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text) VALUES (?, 'm-1', ?, 'q', 'a')`,
		uid, mingThreadID,
	).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(
		`INSERT INTO w_desktop_agent_turn_intent
		 (uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode, request_digest, state, created_at, updated_at)
		 VALUES (?, 'turn-done', ?, ?, 'q', 'ppt', 'd', 'completed', ?, ?)`,
		uid, mingThreadID, mingThread, now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(dataDir, "agent_workspace", "thread_"+mingThread)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "deck.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The default account's thread must survive the neighbour's deletion.
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state, agent_type) VALUES (?, 'de305d54-75b4-431b-adb2-eb6b9e546202', 'Keep me', 'local', 'general_agent')`,
		localSingleUserUID,
	).Error; err != nil {
		t.Fatal(err)
	}

	// Refusal 1: a running turn.
	if err := db.Exec(
		`INSERT INTO w_desktop_agent_turn_intent
		 (uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode, request_digest, state, created_at, updated_at)
		 VALUES (?, 'turn-live', ?, ?, 'q', 'ppt', 'd', 'streaming', ?, ?)`,
		uid, mingThreadID, mingThread, now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	resp, body = localAccountsRequest(t, http.MethodDelete, base+"/local/accounts/2", "tok", "")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "account_busy") {
		t.Fatalf("busy delete: %d %s", resp.StatusCode, body)
	}
	if err := db.Exec(`DELETE FROM w_desktop_agent_turn_intent WHERE turn_uuid = 'turn-live'`).Error; err != nil {
		t.Fatal(err)
	}

	// Refusal 2: the active account (id 1 is active by default).
	resp, body = localAccountsRequest(t, http.MethodDelete, base+"/local/accounts/1", "tok", "")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "account_active") {
		t.Fatalf("active delete: %d %s", resp.StatusCode, body)
	}

	// The cascade.
	resp, body = localAccountsRequest(t, http.MethodDelete, base+"/local/accounts/2", "tok", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	var deletion struct {
		Deleted  bool  `json:"deleted"`
		Threads  int   `json:"threads"`
		Messages int64 `json:"messages"`
	}
	if err := json.Unmarshal(body, &deletion); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !deletion.Deleted || deletion.Threads != 1 || deletion.Messages != 1 {
		t.Fatalf("deletion report = %+v", deletion)
	}
	count := func(query string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := db.Raw(query, args...).Row().Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM w_workagent_thread WHERE uid = ?`, uid); n != 0 {
		t.Errorf("threads remain: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM w_workagent_message WHERE uid = ?`, uid); n != 0 {
		t.Errorf("messages remain: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM w_desktop_agent_turn_intent WHERE uid = ?`, uid); n != 0 {
		t.Errorf("intents remain: %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM w_desktop_local_account WHERE id = 2`); n != 0 {
		t.Errorf("account row remains")
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Errorf("workspace dir survived the delete: %v", err)
	}
	if n := count(`SELECT COUNT(*) FROM w_workagent_thread WHERE uid = ?`, localSingleUserUID); n != 1 {
		t.Errorf("the neighbour's thread must survive, got %d", n)
	}
}
