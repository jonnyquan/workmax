//go:build desktop

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// The model gateway is the one place in the sidecar where a credential is
// deliberately handed to a process we do not control. Everything below is
// about the two properties that makes acceptable: the credential is worthless
// off this machine, and it stops working the moment the session it stands in
// for does.

const modelGatewayTestUID = uint64(90210)

type modelGatewayFixture struct {
	base     string
	server   *Server
	gateway  *ModelGateway
	settings *LocalModelSettingsStore
	tokens   *cloudproxy.TokenStore
	db       *gorm.DB

	// upstream is what the fake cloud does with a forwarded request.
	upstream *atomic.Value // func(http.ResponseWriter, *http.Request)
	// seen records the last forwarded request for assertions.
	seenPath  *atomic.Value // string
	seenAuth  *atomic.Value // string
	seenBody  *atomic.Value // string
	callCount *atomic.Int64
}

func newModelGatewayFixture(t *testing.T, bound bool) *modelGatewayFixture {
	t.Helper()
	db := openMigratedTestDB(t)
	settings := NewLocalModelSettingsStore(db, newMemKeychain())

	fixture := &modelGatewayFixture{
		db:        db,
		settings:  settings,
		upstream:  &atomic.Value{},
		seenPath:  &atomic.Value{},
		seenAuth:  &atomic.Value{},
		seenBody:  &atomic.Value{},
		callCount: &atomic.Int64{},
	}
	fixture.seenPath.Store("")
	fixture.seenAuth.Store("")
	fixture.seenBody.Store("")
	fixture.upstream.Store(modelGatewayStreamOK)

	cloudServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.callCount.Add(1)
		fixture.seenPath.Store(r.URL.Path)
		fixture.seenAuth.Store(r.Header.Get("Authorization"))
		raw, _ := io.ReadAll(r.Body)
		fixture.seenBody.Store(string(raw))
		fixture.upstream.Load().(func(http.ResponseWriter, *http.Request))(w, r)
	}))
	t.Cleanup(cloudServer.Close)

	tokens := cloudproxy.NewTokenStore(newMemKeychain())
	if bound {
		if err := tokens.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(modelGatewayTestUID), "refresh")); err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}
	cloud := cloudproxy.NewClient(cloudServer.URL)
	cloud.HTTPClient = cloudServer.Client()
	proxy := cloudproxy.NewProxy(cloud, tokens, db)
	proxy.HTTPClient = cloudServer.Client()

	gateway, err := NewModelGateway()
	if err != nil {
		t.Fatalf("NewModelGateway: %v", err)
	}
	gateway.client = cloudServer.Client()

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "model-gateway-test",
		LocalToken:     "gateway-local-token",
		DB:             db,
		TokenStore:     tokens,
		Proxy:          proxy,
		ModelSettings:  settings,
		ModelGateway:   gateway,
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

	fixture.base = "http://" + srv.listener.Addr().String()
	fixture.server = srv
	fixture.gateway = gateway
	fixture.tokens = tokens
	return fixture
}

// modelGatewayStreamOK is a minimal provider SSE stream: two frames and a
// terminator, flushed separately so a buffering relay would be visible.
func modelGatewayStreamOK(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range []string{
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"he\"}}\n\n",
		"event: content_block_delta\ndata: {\"delta\":{\"text\":\"llo\"}}\n\n",
		"event: message_stop\ndata: {}\n\n",
	} {
		_, _ = io.WriteString(w, frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (f *modelGatewayFixture) chooseOfficialModel(t *testing.T, protocol, modelID string) {
	t.Helper()
	if _, err := f.settings.Put(modelGatewayTestUID, LocalModelSettingsPut{
		PreferredRoute:  ModelRouteLocal,
		OfficialModelID: &modelID,
		Local:           &LocalModelProfilePut{Protocol: protocol},
	}); err != nil {
		t.Fatalf("choose official model: %v", err)
	}
}

// anthropicPost sends a request the way the claude CLI would: base URL plus
// /v1/messages, credential in x-api-key, no local token anywhere.
func (f *modelGatewayFixture) anthropicPost(t *testing.T, token, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.base+"/model-gateway/anthropic/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("x-api-key", token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST gateway: %v", err)
	}
	return response
}

func (f *modelGatewayFixture) openAIPost(t *testing.T, token, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.base+"/model-gateway/openai/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST gateway: %v", err)
	}
	return response
}

func readAllString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

// A local process that guessed the port must not be able to spend the user's
// membership. The loopback token is the only thing standing there, so a wrong
// one and a missing one both have to fail closed.
func TestModelGatewayRefusesAWrongOrMissingToken(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")

	for _, testCase := range []struct{ name, token string }{
		{"wrong token", "not-the-token"},
		{"no token", ""},
		{"the local token is not the gateway token", "gateway-local-token"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := fixture.anthropicPost(t, testCase.token, `{"messages":[]}`)
			body := readAllString(t, response)
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (%s)", response.StatusCode, body)
			}
			if fixture.callCount.Load() != 0 {
				t.Fatalf("an unauthenticated request reached the cloud")
			}
		})
	}
}

// The whole-port perimeter still applies to everything else under the gateway
// prefix: exempting the registered routes must not exempt the subtree.
func TestModelGatewayExemptionIsPathExactNotPrefixWide(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	request, _ := http.NewRequest(http.MethodPost, fixture.base+"/model-gateway/anthropic/v1/complete", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", fixture.gateway.Token())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := readAllString(t, response)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 from the local-token perimeter (%s)", response.StatusCode, body)
	}
}

// The forward: Anthropic shape in, cloud Bearer out, stream back unchanged.
func TestModelGatewayForwardsAnthropicAndStreamsBack(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")

	response := fixture.anthropicPost(t, fixture.gateway.Token(), `{"model":"whatever-the-cli-said","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	frames := []string{}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			frames = append(frames, line)
		}
	}
	joined := strings.Join(frames, "\n")
	if !strings.Contains(joined, `{"delta":{"text":"he"}}`) || !strings.Contains(joined, "message_stop") {
		t.Fatalf("upstream stream was not passed through: %s", joined)
	}

	if path := fixture.seenPath.Load().(string); path != cloudproxy.CloudRouteModelGatewayAnthropic {
		t.Fatalf("forwarded to %q", path)
	}
	if auth := fixture.seenAuth.Load().(string); auth != "Bearer "+mintLocalHistoryJWT(modelGatewayTestUID) {
		t.Fatalf("upstream authorization = %q, want the desktop OAuth bearer", auth)
	}
	// The model is the sidecar's answer, not the subprocess's: whatever the
	// CLI asked for, what leaves this machine is the catalog id the user
	// picked and the cloud can check entitlement against.
	var forwarded map[string]any
	if err := json.Unmarshal([]byte(fixture.seenBody.Load().(string)), &forwarded); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	if forwarded["model"] != "work-pro" {
		t.Fatalf("forwarded model = %v, want the identity's catalog choice", forwarded["model"])
	}
	if _, kept := forwarded["messages"]; !kept {
		t.Fatalf("rewriting the model must preserve the rest of the request: %v", forwarded)
	}
	// The loopback credential must not be anywhere near the cloud request.
	if strings.Contains(fixture.seenAuth.Load().(string), fixture.gateway.Token()) ||
		strings.Contains(fixture.seenBody.Load().(string), fixture.gateway.Token()) {
		t.Fatalf("the loopback token must not travel upstream")
	}
}

// Same forward, OpenAI shape, Bearer credential slot.
func TestModelGatewayForwardsOpenAIChatCompletions(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolOpenAICompatible, "work-plus")
	fixture.upstream.Store(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	})

	response := fixture.openAIPost(t, fixture.gateway.Token(), `{"messages":[],"stream":true}`)
	body := readAllString(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s)", response.StatusCode, body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("stream not passed through: %s", body)
	}
	if path := fixture.seenPath.Load().(string); path != cloudproxy.CloudRouteModelGatewayOpenAI {
		t.Fatalf("forwarded to %q", path)
	}
}

// Both spellings of each path exist because the provider clients disagree
// about where /v1 belongs. A base URL that works for one engine and 404s for
// the other would be a failure nobody could read.
func TestModelGatewayAcceptsBothPathSpellings(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")

	for _, path := range []string{
		"/model-gateway/anthropic/messages",
		"/model-gateway/openai/chat/completions",
	} {
		request, _ := http.NewRequest(http.MethodPost, fixture.base+path, strings.NewReader(`{"messages":[]}`))
		request.Header.Set("Content-Type", "application/json")
		if strings.Contains(path, "anthropic") {
			request.Header.Set("x-api-key", fixture.gateway.Token())
		} else {
			request.Header.Set("Authorization", "Bearer "+fixture.gateway.Token())
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		body := readAllString(t, response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("POST %s = %d (%s)", path, response.StatusCode, body)
		}
	}
}

// Signed out is not a reason to fall back to anything. It is a reason to say
// so, in words that name both ways forward.
func TestModelGatewayWithoutAnAccountFailsLoudly(t *testing.T) {
	fixture := newModelGatewayFixture(t, false)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")

	response := fixture.anthropicPost(t, fixture.gateway.Token(), `{"messages":[]}`)
	body := readAllString(t, response)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", response.StatusCode, body)
	}
	if !strings.Contains(body, "连接 WorkMax 账号") {
		t.Fatalf("the error must say an account is needed: %s", body)
	}
	if fixture.callCount.Load() != 0 {
		t.Fatalf("an unbound request must not reach the cloud")
	}
}

// A membership that does not cover the model is the cloud's verdict, arriving
// per request. It must reach the user as an upgrade/switch decision, not as an
// opaque 403 and not as a quiet downgrade to a model they did not pick.
func TestModelGatewayMembershipRefusalIsActionable(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-max")
	fixture.upstream.Store(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":7,"msg":"tier does not include this model"}`)
	})

	response := fixture.anthropicPost(t, fixture.gateway.Token(), `{"messages":[]}`)
	body := readAllString(t, response)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", response.StatusCode, body)
	}
	for _, want := range []string{"会员套餐", "升级"} {
		if !strings.Contains(body, want) {
			t.Fatalf("403 body must be actionable, missing %q: %s", want, body)
		}
	}
	if !strings.Contains(body, modelGatewayErrorPermission) {
		t.Fatalf("403 must use the provider's error vocabulary: %s", body)
	}
}

// A model that left the catalog is a settings problem, not an outage: the
// cloud says 404 and the user has to pick again.
func TestModelGatewayRetiredModelPointsAtTheSettings(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-retired")
	fixture.upstream.Store(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"class":"model_not_found"}}`)
	})

	response := fixture.anthropicPost(t, fixture.gateway.Token(), `{"messages":[]}`)
	body := readAllString(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", response.StatusCode, body)
	}
	if !strings.Contains(body, "重新选择一个模型") {
		t.Fatalf("a retired model must send the user back to the picker: %s", body)
	}
}

// An unreachable cloud is a 502 that says so. Critically, it is not an
// invitation to run the turn somewhere else.
func TestModelGatewayUnreachableCloudFailsExplicitly(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")
	// Point the client at a closed port: a real transport failure, not a
	// status code we invented.
	fixture.server.cfg.Proxy.CloudClient().BaseURL = "http://127.0.0.1:1"

	response := fixture.anthropicPost(t, fixture.gateway.Token(), `{"messages":[]}`)
	body := readAllString(t, response)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", response.StatusCode, body)
	}
	if !strings.Contains(body, "无法连接 WorkMax 云端") {
		t.Fatalf("unreachable cloud must say so: %s", body)
	}
}

// Nothing chosen is its own failure, distinct from "not signed in": the fix is
// a different click.
func TestModelGatewayWithoutAChosenModelFailsExplicitly(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "")

	response := fixture.anthropicPost(t, fixture.gateway.Token(), `{"messages":[]}`)
	body := readAllString(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", response.StatusCode, body)
	}
	if !strings.Contains(body, "尚未选择官方模型") {
		t.Fatalf("missing the actionable sentence: %s", body)
	}
}

// Logout is the revocation story. A subprocess holding the old credential must
// lose it immediately — not when it next restarts, and not when a cached
// entitlement expires.
func TestLogoutRotatesTheGatewayTokenAndRetiresTheOldOne(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")
	stale := fixture.gateway.Token()

	response := fixture.anthropicPost(t, stale, `{"messages":[]}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("precondition: a bound turn should work, got %d (%s)", response.StatusCode, readAllString(t, response))
	}
	response.Body.Close()

	logout, _ := http.NewRequest(http.MethodPost, fixture.base+"/auth/logout", nil)
	logout.Header.Set("X-Local-Token", "gateway-local-token")
	logoutResponse, err := http.DefaultClient.Do(logout)
	if err != nil {
		t.Fatalf("logout: %v", err)
	}
	logoutResponse.Body.Close()

	if fixture.gateway.Token() == stale {
		t.Fatalf("logout must rotate the loopback credential")
	}
	after := fixture.anthropicPost(t, stale, `{"messages":[]}`)
	body := readAllString(t, after)
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the retired token still works: %d (%s)", after.StatusCode, body)
	}
	// And the fresh one cannot resurrect the session either: there is no
	// account to bill any more.
	fresh := fixture.anthropicPost(t, fixture.gateway.Token(), `{"messages":[]}`)
	freshBody := readAllString(t, fresh)
	if fresh.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out, a fresh token must still have nothing to spend: %d (%s)", fresh.StatusCode, freshBody)
	}
}

// Rotation must reach requests that are already streaming. A logout that only
// takes effect on the next turn leaves an agent loop spending an account the
// user believes they left.
func TestRotationCancelsAForwardAlreadyInFlight(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")

	rotated := make(chan struct{})
	fixture.upstream.Store(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		writeFrame := func(name string) {
			_, _ = io.WriteString(w, "event: "+name+"\ndata: {}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeFrame("before-rotation")
		// Wait for the rotation, then keep producing. Everything written from
		// here on belongs to a session the user has left.
		select {
		case <-rotated:
		case <-time.After(5 * time.Second):
		}
		for i := 0; i < 50; i++ {
			writeFrame("after-rotation")
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	})

	// The rotation must land while the caller is genuinely mid-stream, so it
	// is triggered only once the pre-rotation bytes have actually arrived —
	// otherwise the test would be measuring a canceled connect instead of a
	// canceled stream.
	firstFrame := make(chan struct{})
	done := make(chan string, 1)
	go func() {
		response := fixture.anthropicPost(t, fixture.gateway.Token(), `{"messages":[]}`)
		defer response.Body.Close()
		var seen strings.Builder
		buf := make([]byte, 512)
		signaled := false
		for {
			n, err := response.Body.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				if !signaled && strings.Contains(seen.String(), "before-rotation") {
					signaled = true
					close(firstFrame)
				}
			}
			if err != nil {
				break
			}
		}
		done <- seen.String()
	}()

	select {
	case <-firstFrame:
	case <-time.After(5 * time.Second):
		t.Fatalf("the caller never received the pre-rotation stream")
	}
	if err := fixture.gateway.Rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	close(rotated)

	select {
	case body := <-done:
		if !strings.Contains(body, "before-rotation") {
			t.Fatalf("the forward should have delivered what it had: %q", body)
		}
		// The guarantee is that the stream ENDS, not that a byte already on
		// the wire is recalled: cancellation propagates through the transport
		// while the upstream may already have flushed one more frame. What
		// must not happen is the turn continuing — 50 frames were queued and
		// at most the one racing the cancel may appear.
		if leaked := strings.Count(body, "after-rotation"); leaked > 1 {
			t.Fatalf("forward survived rotation (%d post-rotation frames): %q", leaked, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("rotation did not end the in-flight forward")
	}
}

// A body that is not a JSON object cannot carry a model, so it cannot be made
// to satisfy the cloud contract. Rejecting here beats forwarding something the
// cloud will reject for reasons the user cannot see.
func TestModelGatewayRejectsANonObjectBody(t *testing.T) {
	fixture := newModelGatewayFixture(t, true)
	fixture.chooseOfficialModel(t, LocalProtocolAnthropicCompatible, "work-pro")

	response := fixture.anthropicPost(t, fixture.gateway.Token(), `["not","an","object"]`)
	body := readAllString(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", response.StatusCode, body)
	}
	if fixture.callCount.Load() != 0 {
		t.Fatalf("a malformed body must not reach the cloud")
	}
}

// The gateway token is minted per process and lives only in memory. This is
// the property the whole design rests on, so it is asserted rather than
// assumed.
func TestGatewayTokensAreFreshPerProcessAndConstantTimeChecked(t *testing.T) {
	first, err := NewModelGateway()
	if err != nil {
		t.Fatalf("NewModelGateway: %v", err)
	}
	second, err := NewModelGateway()
	if err != nil {
		t.Fatalf("NewModelGateway: %v", err)
	}
	if first.Token() == "" || first.Token() == second.Token() {
		t.Fatalf("two processes must not share a gateway token")
	}
	if !first.Matches(first.Token()) || first.Matches(second.Token()) || first.Matches("") {
		t.Fatalf("token comparison accepts the wrong credential")
	}
	first.Shutdown()
	if first.Matches(first.Token()) || first.Token() != "" {
		t.Fatalf("a shut-down gateway must not authenticate anything")
	}
}
