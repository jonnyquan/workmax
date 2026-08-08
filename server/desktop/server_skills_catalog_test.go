//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

// newServerFixtureWithUpstream is a variant of the agent_chat fixture
// that lets each test wire its own /api/work-agent/skills handler
// alongside any chat endpoints it needs.
func newServerFixtureWithSkills(t *testing.T, skillsHandler http.HandlerFunc) (string, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/work-agent/skills", skillsHandler)
	mux.HandleFunc("/api/work-agent/chat/agent", func(w http.ResponseWriter, r *http.Request) {
		// Drain anything chat-side; tests in this file don't exercise it.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "stub-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
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
		DeviceID:       "dev",
		TokenStore:     store,
		Proxy:          proxy,
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

	return "http://" + srv.listener.Addr().String(), "tok"
}

// TestSkillsCatalog_FiltersToAllowlist: cloud returns 3 skills, sidecar
// returns only the allowlisted ones (ppt).
func TestSkillsCatalog_FiltersToAllowlist(t *testing.T) {
	base, tok := newServerFixtureWithSkills(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"code": 1, "message": "ok",
			"data": {
				"items": [
					{"agentMode":"ppt","version":"2.0.0","hasQuestionForm":true,"hasDirectionsFallback":true,"hasPostScripts":true,"labelKey":"k.ppt.name","descriptionKey":"k.ppt.desc"},
					{"agentMode":"flashCard","version":"1.0.0","hasQuestionForm":false,"hasDirectionsFallback":false,"hasPostScripts":false,"labelKey":"k.fc.name","descriptionKey":"k.fc.desc"},
					{"agentMode":"character","version":"1.0.0","hasQuestionForm":false,"hasDirectionsFallback":false,"hasPostScripts":false,"labelKey":"k.ch.name","descriptionKey":"k.ch.desc"}
				],
				"count": 3
			}
		}`)
	})

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/skills/catalog", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body skillsCatalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Items) != 1 {
		t.Fatalf("expected 1 item after filter, got %+v", body)
	}
	if body.Items[0].AgentMode != "ppt" {
		t.Errorf("first item mode: got %q, want ppt", body.Items[0].AgentMode)
	}
	if body.Items[0].Version != "2.0.0" {
		t.Errorf("version should round-trip from cloud: got %q", body.Items[0].Version)
	}
	if len(body.AllowedModes) != 1 || body.AllowedModes[0] != "ppt" {
		t.Errorf("allowed_modes: %+v", body.AllowedModes)
	}
}

func TestSkillsCatalog_DropsMalformedAllowedItems(t *testing.T) {
	base, tok := newServerFixtureWithSkills(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"code": 1, "message": "ok",
			"data": {
				"items": [
					{"agentMode":"ppt","version":"","hasQuestionForm":true,"hasDirectionsFallback":true,"hasPostScripts":true,"labelKey":"k.ppt.name","descriptionKey":"k.ppt.desc"},
					{"agentMode":"ppt","version":"2.0.0","hasQuestionForm":true,"hasDirectionsFallback":true,"hasPostScripts":true,"labelKey":"","descriptionKey":"k.ppt.desc"},
					{"agentMode":"ppt","version":"2.1.0","hasQuestionForm":true,"hasDirectionsFallback":true,"hasPostScripts":true,"labelKey":"k.ppt.name","descriptionKey":"k.ppt.desc"}
				],
				"count": 3
			}
		}`)
	})

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/skills/catalog", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body skillsCatalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Items) != 1 {
		t.Fatalf("expected only the well-formed ppt item, got %+v", body)
	}
	if body.Items[0].Version != "2.1.0" {
		t.Fatalf("wrong item survived filtering: %+v", body.Items[0])
	}
}

// TestSkillsCatalog_CloudUnreachable: cloud returns 500; sidecar
// degrades to empty Items + still surfaces the allowlist so the
// renderer's UI has something to show.
func TestSkillsCatalog_CloudUnreachable(t *testing.T) {
	base, tok := newServerFixtureWithSkills(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, "boom")
	})

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/skills/catalog", nil)
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("soft-fail should still return 200, got %d", resp.StatusCode)
	}
	var body skillsCatalogResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Items) != 0 {
		t.Errorf("items should be empty on cloud failure, got %+v", body.Items)
	}
	if len(body.AllowedModes) != 1 || body.AllowedModes[0] != "ppt" {
		t.Errorf("allowed_modes should always surface: got %+v", body.AllowedModes)
	}
}

// TestSkillsCatalog_RefreshesExpiredAccessToken pins that the catalog
// route uses the same token rotation path as chat/sync/userinfo. An
// expired access token with a valid refresh token should rotate once
// and then call the skills endpoint with the fresh access token, not
// silently degrade to an empty catalog.
func TestSkillsCatalog_RefreshesExpiredAccessToken(t *testing.T) {
	var sawFreshAccess atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/desktop/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.Form.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type: got %q, want refresh_token", got)
		}
		if got := r.Form.Get("refresh_token"); got != "old-refresh" {
			t.Errorf("refresh_token: got %q, want old-refresh", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"access_token":"fresh-access",
			"token_type":"Bearer",
			"expires_in":900,
			"refresh_token":"fresh-refresh",
			"refresh_expires_in":7776000,
			"scope":"workagent"
		}`)
	})
	mux.HandleFunc("/api/work-agent/skills", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "Bearer fresh-access" {
			sawFreshAccess.Store(true)
		} else {
			t.Errorf("Authorization: got %q, want Bearer fresh-access", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"code": 1, "message": "ok",
			"data": {
				"items": [
					{"agentMode":"ppt","version":"2.1.0","hasQuestionForm":true,"hasDirectionsFallback":true,"hasPostScripts":true,"labelKey":"k.ppt.name","descriptionKey":"k.ppt.desc"}
				],
				"count": 1
			}
		}`)
	})
	mux.HandleFunc("/api/work-agent/chat/agent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "stale-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
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
		DeviceID:       "dev",
		TokenStore:     store,
		Proxy:          proxy,
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

	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.listener.Addr().String()+"/agent/skills/catalog", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body skillsCatalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !sawFreshAccess.Load() {
		t.Fatal("skills endpoint was not called with the refreshed access token")
	}
	if body.Count != 1 || body.Items[0].AgentMode != "ppt" {
		t.Fatalf("expected refreshed catalog with ppt item, got %+v", body)
	}
	pair, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if pair.AccessToken != "fresh-access" || pair.RefreshToken != "fresh-refresh" {
		t.Fatalf("rotated pair not saved: %+v", pair)
	}
}

func TestSkillsCatalog_UnauthorizedForcesOneRefreshAndRetry(t *testing.T) {
	var skillsCalls atomic.Int64
	var refreshCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc(cloudproxy.CloudRouteOAuthToken, func(w http.ResponseWriter, r *http.Request) {
		refreshCalls.Add(1)
		if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "old-refresh" {
			t.Errorf("unexpected refresh request: err=%v form=%v", err, r.Form)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"access_token":"fresh-access","token_type":"Bearer","expires_in":900,
			"refresh_token":"fresh-refresh","refresh_expires_in":86400,"scope":"workagent"
		}`)
	})
	mux.HandleFunc(cloudproxy.CloudRouteSkillsList, func(w http.ResponseWriter, r *http.Request) {
		skillsCalls.Add(1)
		switch r.Header.Get("Authorization") {
		case "Bearer old-access":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "bare-upstream-secret")
		case "Bearer fresh-access":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"code":1,"message":"ok","data":{"items":[
					{"agentMode":"ppt","version":"2.2.0","labelKey":"k.name","descriptionKey":"k.desc"}
				],"count":1}
			}`)
		default:
			t.Errorf("unexpected Authorization: %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "old-access",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "old-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatal(err)
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
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

	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.listener.Addr().String()+"/agent/skills/catalog", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body skillsCatalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body.Count != 1 || body.Items[0].Version != "2.2.0" {
		t.Fatalf("catalog after 401 recovery: status=%d body=%+v", resp.StatusCode, body)
	}
	if skillsCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("calls: skills=%d refresh=%d, want 2/1", skillsCalls.Load(), refreshCalls.Load())
	}
}

// TestSkillsCatalog_NoSession: with no Keychain entry, return 401 so
// the renderer routes to LoginPage.
func TestSkillsCatalog_NoSession(t *testing.T) {
	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	cloud := cloudproxy.NewClient("http://nope")
	proxy := cloudproxy.NewProxy(cloud, store, db)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
		Proxy:          proxy,
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

	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.listener.Addr().String()+"/agent/skills/catalog", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(body)); got != `{"error":"authentication_required"}` {
		t.Fatalf("body = %s, want closed authentication_required error", body)
	}
}

func TestSkillsCatalog_SessionLoadFailureIsUnavailable(t *testing.T) {
	const secret = "skills-keychain-private-marker"
	store := cloudproxy.NewTokenStore(userInfoReadFailureKeychain{err: errors.New(secret)})
	proxy := cloudproxy.NewProxy(cloudproxy.NewClient("https://example.invalid"), store, nil)
	srv := &Server{cfg: ServerConfig{TokenStore: store, Proxy: proxy}}

	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/agent/skills/catalog", nil)
	srv.handleSkillsCatalog(ginContext)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != `{"error":"session_unavailable"}` {
		t.Fatalf("body = %s, want closed session_unavailable error", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("Renderer response leaked session load detail: %s", recorder.Body.String())
	}
}

// TestSkillsCatalog_NoProxyConfigured: ServerConfig without Proxy
// returns 503 (matches the chat handler convention).
func TestSkillsCatalog_NoProxyConfigured(t *testing.T) {
	db := openServerTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
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

	req, _ := http.NewRequest(http.MethodGet, "http://"+srv.listener.Addr().String()+"/agent/skills/catalog", nil)
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

// TestAgentChat_RejectsNonAllowlistedMode: even with a valid request,
// chat_mode=flashCard is refused at the sidecar gate so the cloud is
// never even called.
func TestAgentChat_RejectsNonAllowlistedMode(t *testing.T) {
	var cloudCalled atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/work-agent/chat/agent", func(w http.ResponseWriter, r *http.Request) {
		cloudCalled.Store(true)
		w.WriteHeader(200)
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr_1")
	store := cloudproxy.NewTokenStore(newMemKeychain())
	_ = store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	})
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()
	srv, _ := NewServer(ServerConfig{
		SidecarVersion: "test", LocalToken: "tok", DB: db,
		TokenStore: store, Proxy: proxy,
	})
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hi","chat_mode":"flashCard","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.listener.Addr().String()+"/agent/chat", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "chat_mode not enabled") {
		t.Errorf("body should explain the rejection: %q", body)
	}
	if cloudCalled.Load() {
		t.Error("cloud should never be called when chat_mode is rejected")
	}
}

// TestAgentChat_AllowsEmptyMode: empty chat_mode is "use default" —
// the sidecar normalizes it to the explicit Desktop default before
// forwarding to the cloud.
func TestAgentChat_AllowsEmptyMode(t *testing.T) {
	var cloudCalled atomic.Bool
	var upstreamBody atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/api/work-agent/chat/agent", func(w http.ResponseWriter, r *http.Request) {
		cloudCalled.Store(true)
		body, _ := io.ReadAll(r.Body)
		upstreamBody.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		io.WriteString(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr_1")
	store := cloudproxy.NewTokenStore(newMemKeychain())
	_ = store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	})
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()
	srv, _ := NewServer(ServerConfig{
		SidecarVersion: "test", LocalToken: "tok", DB: db,
		TokenStore: store, Proxy: proxy,
	})
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"hi","chat_mode":"","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.listener.Addr().String()+"/agent/chat", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	// Drain the response body before checking — proxy.Chat returns
	// after PipeUpstream completes, but the sidecar's response close
	// is what guarantees cloudCalled was written + visible. Drain
	// forces that happens-before edge.
	_, _ = io.ReadAll(resp.Body)
	if !cloudCalled.Load() {
		t.Error("cloud should be called when chat_mode is empty (default)")
	}
	var forwarded map[string]any
	if err := json.Unmarshal(upstreamBody.Load().([]byte), &forwarded); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if forwarded["chatMode"] != "agent" {
		t.Fatalf("cloud payload.chatMode should select the agent handler, got body %s", upstreamBody.Load().([]byte))
	}
	if forwarded["conversationId"] != "9001" {
		t.Fatalf("cloud payload.conversationId should come from the local thread mapping, got body %s", upstreamBody.Load().([]byte))
	}
	metadata, ok := forwarded["metadata"].(map[string]any)
	if !ok || metadata["agentMode"] != "ppt" {
		t.Fatalf("empty chat_mode should normalize payload.metadata.agentMode to ppt, got body %s", upstreamBody.Load().([]byte))
	}
	messages, ok := forwarded["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("cloud payload should contain exactly one canonical message, got body %s", upstreamBody.Load().([]byte))
	}
	message, ok := messages[0].(map[string]any)
	if !ok || message["role"] != "user" || message["content"] != "hi" {
		t.Fatalf("canonical message should match user_text, got body %s", upstreamBody.Load().([]byte))
	}
}

func TestAgentChat_BuildsFrozenCloudPayloadFromTypedIntent(t *testing.T) {
	var upstreamBody atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/api/work-agent/chat/agent", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		io.WriteString(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	db := openServerTestDB(t)
	seedServerTestThreadWithCloudID(t, db, 42, "thr_1", "424242")
	store := cloudproxy.NewTokenStore(newMemKeychain())
	_ = store.Save(cloudproxy.TokenPair{
		AccessToken:      mintLocalHistoryJWT(42),
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "stub",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	})
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, store, db)
	proxy.HTTPClient = upstream.Client()
	srv, _ := NewServer(ServerConfig{
		SidecarVersion: "test", LocalToken: "tok", DB: db,
		TokenStore: store, Proxy: proxy,
	})
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	reqBody := `{"turn_uuid":"de305d54-75b4-431b-adb2-eb6b9e546014","thread_uuid":"thr_1","user_text":"  canonical prompt  ","chat_mode":"ppt","payload":{"stream":true}}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.listener.Addr().String()+"/agent/chat", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
	_, _ = io.ReadAll(resp.Body)

	var forwarded map[string]any
	if err := json.Unmarshal(upstreamBody.Load().([]byte), &forwarded); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if forwarded["conversationId"] != "424242" {
		t.Fatalf("payload.conversationId should use the authoritative cloud thread id, got body %s", upstreamBody.Load().([]byte))
	}
	if _, ok := forwarded["conversation_id"]; ok {
		t.Fatalf("payload.conversation_id should be removed to avoid contradictory conversation fields: %s", upstreamBody.Load().([]byte))
	}
	if forwarded["chatMode"] != "agent" {
		t.Fatalf("payload.chatMode should be rewritten to agent, got body %s", upstreamBody.Load().([]byte))
	}
	if _, ok := forwarded["chat_mode"]; ok {
		t.Fatalf("payload.chat_mode should be removed to avoid contradictory mode fields: %s", upstreamBody.Load().([]byte))
	}
	metadata, ok := forwarded["metadata"].(map[string]any)
	if !ok || metadata["agentMode"] != "ppt" || metadata["threadId"] != "424242" {
		t.Fatalf("payload.metadata should use the frozen Desktop intent: %s", upstreamBody.Load().([]byte))
	}
	if _, ok := metadata["agent_mode"]; ok {
		t.Fatalf("payload.metadata.agent_mode should be removed to avoid contradictory mode fields: %s", upstreamBody.Load().([]byte))
	}
	if _, ok := metadata["thread_id"]; ok {
		t.Fatalf("payload.metadata.thread_id should be removed to avoid contradictory thread fields: %s", upstreamBody.Load().([]byte))
	}
	messages, ok := forwarded["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("payload.messages should be replaced by one canonical user message: %s", upstreamBody.Load().([]byte))
	}
	message, ok := messages[0].(map[string]any)
	if !ok || message["role"] != "user" || message["content"] != "  canonical prompt  " {
		t.Fatalf("payload.messages should preserve frozen user_text bytes: %s", upstreamBody.Load().([]byte))
	}
	if forwarded["stream"] != true {
		t.Fatalf("typed Bridge stream preference should be preserved: %s", upstreamBody.Load().([]byte))
	}
	if len(forwarded) != 5 {
		t.Fatalf("cloud payload should contain only stream plus canonical contract fields: %s", upstreamBody.Load().([]byte))
	}
}
