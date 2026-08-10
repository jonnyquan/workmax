//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
	localrender "server/desktop/local_render"
)

// The signed-out availability matrix.
//
// Six handlers used to answer 503 unless Proxy AND DB AND TokenStore were all
// wired, which on a signed-out machine meant "the app you installed to run
// locally cannot open its own history". These tests state, per route, what a
// machine with NO cloud account and NO local model can do — and the one thing
// it genuinely cannot: send a turn. That refusal is physics (a turn needs a
// model), not a login wall, so it stays.

type signedOutFixture struct {
	baseURL string
	token   string
	db      *gorm.DB
	store   *cloudproxy.TokenStore
}

// bootSignedOutFixture is the honest first-run configuration: migrated SQLite,
// a real (empty) TokenStore, a Proxy whose upstream fails the test if anything
// reaches for it, and preferred_route left at the "official" default — i.e. no
// local model has been configured either.
func bootSignedOutFixture(t *testing.T) signedOutFixture {
	t.Helper()
	db := openMigratedTestDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a signed-out machine must not call the cloud, got %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(upstream.Close)

	store := cloudproxy.NewTokenStore(newMemKeychain())
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()

	dataDir := t.TempDir()
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "signed-out-test",
		LocalToken:     "signed-out-token",
		DB:             db,
		DeviceID:       "dev",
		DataDir:        dataDir,
		TokenStore:     store,
		Proxy:          proxy,
		ModelSettings:  NewLocalModelSettingsStore(db, newMemKeychain()),
		LocalFiles:     localrender.NewStore(db, filepath.Join(dataDir, "thread_files")),
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
	return signedOutFixture{
		baseURL: "http://" + srv.listener.Addr().String(),
		token:   "signed-out-token",
		db:      db,
		store:   store,
	}
}

const signedOutThreadUUID = "8f14e45f-ceea-4e6f-a8bc-ea0d3b2c1a77"

// Browse, create, configure: everything except sending.
func TestSignedOutMachineCanUseItsOwnWorkbench(t *testing.T) {
	fixture := bootSignedOutFixture(t)

	readable := []string{
		"/agent/threads",
		"/agent/threads/" + signedOutThreadUUID + "/messages",
		"/local/accounts",
		"/settings/model-route",
		"/agent/skills/modes",
		"/agent/turns/recoverable",
		"/agent/search?q=hello",
	}
	for _, path := range readable {
		response := doSidecarRequest(t, http.MethodGet, fixture.baseURL+path, fixture.token, nil)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200 without an account", path, response.StatusCode, body)
		}
	}

	// Creating a conversation: no account, no local model, still a real
	// thread — owned by this machine's local identity, marked local, and with
	// no cloud call behind it.
	response := doSidecarRequest(
		t, http.MethodPut, fixture.baseURL+"/agent/threads/"+signedOutThreadUUID,
		fixture.token, []byte(`{"name":"First run","agent_mode":"ppt"}`),
	)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("signed-out create = %d %s, want 201", response.StatusCode, body)
	}
	var created putAgentThreadResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Thread.CloudSync != "local" {
		t.Fatalf("cloud_sync_state = %q, want local", created.Thread.CloudSync)
	}
	var ownerUID uint64
	if err := fixture.db.Raw(
		`SELECT uid FROM w_workagent_thread WHERE uuid = ?`, signedOutThreadUUID,
	).Row().Scan(&ownerUID); err != nil {
		t.Fatalf("scan owner: %v", err)
	}
	if ownerUID != activeLocalAccountUID(fixture.db) {
		t.Fatalf("thread owner = %d, want the active local account %d", ownerUID, activeLocalAccountUID(fixture.db))
	}

	// And the thread the machine just made is visible to the machine.
	list := doSidecarRequest(t, http.MethodGet, fixture.baseURL+"/agent/threads", fixture.token, nil)
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if !strings.Contains(string(listBody), signedOutThreadUUID) {
		t.Fatalf("local history did not include the thread just created: %s", listBody)
	}

	// Recovery bookkeeping is reachable rather than 503: a turn that never
	// existed is not found, which is a different answer from "unavailable".
	cancel := doSidecarRequest(
		t, http.MethodPost,
		fixture.baseURL+"/agent/turns/"+serverTestTurnUUID+"/cancel", fixture.token, nil,
	)
	cancelBody, _ := io.ReadAll(cancel.Body)
	cancel.Body.Close()
	if cancel.StatusCode != http.StatusNotFound || !strings.Contains(string(cancelBody), "turn_not_found") {
		t.Fatalf("cancel = %d %s, want 404 turn_not_found", cancel.StatusCode, cancelBody)
	}
}

// The physical limit, stated honestly: with neither a connected account nor a
// local model, a turn has nowhere to run. It must say so as authentication,
// not as "unavailable", and it must not have touched the cloud to find out.
func TestSignedOutWithoutALocalModelCannotSendATurn(t *testing.T) {
	fixture := bootSignedOutFixture(t)
	create := doSidecarRequest(
		t, http.MethodPut, fixture.baseURL+"/agent/threads/"+signedOutThreadUUID,
		fixture.token, []byte(`{"name":"First run","agent_mode":"ppt"}`),
	)
	create.Body.Close()

	response := doSidecarRequest(
		t, http.MethodPost, fixture.baseURL+"/agent/chat", fixture.token,
		[]byte(`{"turn_uuid":"`+serverTestTurnUUID+`","thread_uuid":"`+signedOutThreadUUID+
			`","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`),
	)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "authentication_required") {
		t.Fatalf("signed-out cloud-route chat = %d %s, want 401 authentication_required", response.StatusCode, body)
	}
}

// With a local model configured, the same machine sends. This is the whole
// bargain: the workbench is free, the model is the choice.
func TestSignedOutWithALocalModelCanSendATurn(t *testing.T) {
	db := openMigratedTestDB(t)
	settings := NewLocalModelSettingsStore(db, newMemKeychain())
	if _, err := settings.Put(LocalModelSettingsPut{
		PreferredRoute: ModelRouteLocal,
		Local: &LocalModelProfilePut{
			Protocol: LocalProtocolOpenAICompatible,
			BaseURL:  "http://127.0.0.1:11434/v1",
			ModelID:  "llama3.2",
		},
	}); err != nil {
		t.Fatalf("configure local model: %v", err)
	}
	runner := &fakeLocalRunner{}
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "signed-out-local-test",
		LocalToken:     "tok",
		DB:             db,
		DeviceID:       "dev",
		TokenStore:     cloudproxy.NewTokenStore(newMemKeychain()),
		ModelSettings:  settings,
		LocalInference: runner,
		// No Proxy at all: a local turn must not need one.
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

	create := doSidecarRequest(
		t, http.MethodPut, base+"/agent/threads/"+signedOutThreadUUID, "tok",
		[]byte(`{"name":"Local work","agent_mode":"ppt"}`),
	)
	createBody, _ := io.ReadAll(create.Body)
	create.Body.Close()
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("local create = %d %s", create.StatusCode, createBody)
	}

	response := doSidecarRequest(
		t, http.MethodPost, base+"/agent/chat", "tok",
		[]byte(`{"turn_uuid":"`+serverTestTurnUUID+`","thread_uuid":"`+signedOutThreadUUID+
			`","user_text":"hi","chat_mode":"ppt","payload":{"stream":true}}`),
	)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("signed-out local chat = %d %s, want 200", response.StatusCode, body)
	}
	if !runner.called.Load() {
		t.Fatal("the local engine was never asked to run the turn")
	}
}

// Disconnecting must land back on a usable local identity — the account
// leaves, the machine's own workspace does not.
func TestDisconnectingAnAccountLeavesTheLocalIdentityUsable(t *testing.T) {
	fixture := bootSignedOutFixture(t)
	if err := fixture.store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("connect account: %v", err)
	}
	bound := doSidecarRequest(t, http.MethodGet, fixture.baseURL+"/local/accounts", fixture.token, nil)
	boundBody, _ := io.ReadAll(bound.Body)
	bound.Body.Close()
	if !strings.Contains(string(boundBody), `"state":"bound"`) || !strings.Contains(string(boundBody), `"user_id":"…42"`) {
		t.Fatalf("bound listing did not name the connected account: %s", boundBody)
	}

	// Disconnect is the existing logout — no data moves, in either direction.
	if err := fixture.store.Clear(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	after := doSidecarRequest(t, http.MethodGet, fixture.baseURL+"/local/accounts", fixture.token, nil)
	afterBody, _ := io.ReadAll(after.Body)
	after.Body.Close()
	if after.StatusCode != http.StatusOK || !strings.Contains(string(afterBody), `"state":"unbound"`) {
		t.Fatalf("after disconnect = %d %s, want an unbound local identity", after.StatusCode, afterBody)
	}
	threads := doSidecarRequest(t, http.MethodGet, fixture.baseURL+"/agent/threads", fixture.token, nil)
	threads.Body.Close()
	if threads.StatusCode != http.StatusOK {
		t.Fatalf("history after disconnect = %d, want 200", threads.StatusCode)
	}
}

// Attachments are part of the workbench too: sources belong to a thread, not
// to a model. This route used to demand a TokenStore-backed session and
// answered 503 to a machine that had one perfectly good identity.
func TestSignedOutMachineCanAttachFiles(t *testing.T) {
	fixture := bootSignedOutFixture(t)
	create := doSidecarRequest(
		t, http.MethodPut, fixture.baseURL+"/agent/threads/"+signedOutThreadUUID,
		fixture.token, []byte(`{"name":"With sources","agent_mode":"ppt"}`),
	)
	create.Body.Close()

	var body strings.Builder
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	if _, err := io.WriteString(part, "a plain note the next turn can read\n"); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	request, err := http.NewRequest(
		http.MethodPost,
		fixture.baseURL+"/agent/threads/"+signedOutThreadUUID+"/files",
		strings.NewReader(body.String()),
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("X-Local-Token", fixture.token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	uploadBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("signed-out upload = %d %s, want 201", response.StatusCode, uploadBody)
	}

	list := doSidecarRequest(
		t, http.MethodGet, fixture.baseURL+"/agent/threads/"+signedOutThreadUUID+"/files",
		fixture.token, nil,
	)
	listBody, _ := io.ReadAll(list.Body)
	list.Body.Close()
	if list.StatusCode != http.StatusOK || !strings.Contains(string(listBody), "notes.txt") {
		t.Fatalf("signed-out file list = %d %s, want the file back", list.StatusCode, listBody)
	}
}
