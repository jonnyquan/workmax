package modelgateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"server/config"
	"server/model"
	"server/utils/testutil"
)

// The gateway's whole reason to exist is that the Desktop can use official
// models without ever holding a provider key. Every test below is ultimately
// about one of two promises: the credential never crosses the boundary, and
// the platform never pays for a call it did not admit.

// providerAPIKey is deliberately distinctive so a leak assertion can search
// for it in a whole response — headers, body, and error prose alike.
const providerAPIKey = "sk-ant-platform-secret-key-DO-NOT-LEAK"

type fakeAccountSource struct {
	account *ProviderAccount
	err     error

	mu        sync.Mutex
	successes []uint64
	failures  []uint64
}

func (f *fakeAccountSource) AccountForModel(string, Protocol) (*ProviderAccount, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.account, nil
}

func (f *fakeAccountSource) RecordSuccess(id uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes = append(f.successes, id)
}

func (f *fakeAccountSource) RecordFailure(id uint64, _ error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, id)
}

func (f *fakeAccountSource) failureCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.failures)
}

type memoryRecorder struct {
	mu      sync.Mutex
	records []UsageRecord
}

func (m *memoryRecorder) Record(record UsageRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, record)
}

func (m *memoryRecorder) last(t *testing.T) UsageRecord {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.records) == 0 {
		t.Fatal("no usage row was recorded; every gateway exit path must leave one")
	}
	return m.records[len(m.records)-1]
}

func (m *memoryRecorder) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

// harness is a live HTTP stack: a real client talks to a real server which
// proxies to a real upstream. httptest.NewServer rather than a recorder
// because a ResponseRecorder cannot show whether a frame reached the wire
// before the next one was produced — which is the entire streaming contract.
type harness struct {
	t         *testing.T
	db        *gorm.DB
	upstream  *httptest.Server
	gateway   *httptest.Server
	accounts  *fakeAccountSource
	recorder  *memoryRecorder
	uid       uint
	lastCalls []*http.Request
	mu        sync.Mutex
}

func newHarness(t *testing.T, cfg *config.ModelGateway, upstreamHandler http.HandlerFunc) *harness {
	t.Helper()

	db := testutil.NewTestDB(t)
	h := &harness{t: t, db: db, recorder: &memoryRecorder{}, uid: 1}

	h.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured := r.Clone(r.Context())
		captured.Body = io.NopCloser(strings.NewReader(string(body)))
		h.mu.Lock()
		h.lastCalls = append(h.lastCalls, captured)
		h.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		upstreamHandler(w, r)
	}))
	t.Cleanup(h.upstream.Close)

	h.accounts = &fakeAccountSource{account: &ProviderAccount{
		ID:       77,
		Name:     "platform-anthropic-1",
		Provider: "anthropic",
		BaseURL:  h.upstream.URL,
		APIKey:   providerAPIKey,
	}}

	gw := New(db, cfg, Options{Accounts: h.accounts, Usage: h.recorder})

	mux := http.NewServeMux()
	mux.HandleFunc("/anthropic", func(w http.ResponseWriter, r *http.Request) {
		gw.Handle(w, r, ProtocolAnthropic, h.uid)
	})
	mux.HandleFunc("/openai", func(w http.ResponseWriter, r *http.Request) {
		gw.Handle(w, r, ProtocolOpenAI, h.uid)
	})
	h.gateway = httptest.NewServer(mux)
	t.Cleanup(h.gateway.Close)

	return h
}

func (h *harness) upstreamRequest(t *testing.T) *http.Request {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.lastCalls) == 0 {
		t.Fatal("the gateway never called upstream")
	}
	return h.lastCalls[len(h.lastCalls)-1]
}

func (h *harness) upstreamCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.lastCalls)
}

func (h *harness) post(t *testing.T, path, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(h.gateway.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("gateway request: %v", err)
	}
	return resp
}

func seedUser(t *testing.T, db *gorm.DB, member int, endTime time.Time) uint {
	t.Helper()
	user := model.User{Email: "gw@example.com", Nickname: "GW", Member: member, MemberEndTime: endTime}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return user.Id
}

func seedModels(t *testing.T, db *gorm.DB) {
	t.Helper()
	rows := []model.GlobalModel{
		{
			ModelID: "work-pro", MediaType: model.MediaTypeText, DisplayName: "Work Pro",
			Status: model.GlobalModelStatusEnabled, RequiredTier: model.MemberTierFree,
			ProviderType: "anthropic",
			Metadata:     model.JSONMap{model.GlobalModelMetadataUpstreamModel: "claude-sonnet-5"},
		},
		{
			ModelID: "work-plus", MediaType: model.MediaTypeText, DisplayName: "Work Plus",
			Status: model.GlobalModelStatusEnabled, RequiredTier: model.MemberTierPro,
			ProviderType: "anthropic",
			Metadata:     model.JSONMap{model.GlobalModelMetadataUpstreamModel: "claude-opus-5"},
		},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed model %s: %v", row.ModelID, err)
		}
	}
}

func jsonUpstream(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func decodeError(t *testing.T, resp *http.Response) (string, string) {
	t.Helper()
	var body struct {
		Error struct {
			Type        string `json:"type"`
			Message     string `json:"message"`
			GatewayCode string `json:"gateway_code"`
			Code        string `json:"code"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error body: %v (raw %s)", err, raw)
	}
	code := body.Error.GatewayCode
	if code == "" {
		code = body.Error.Code
	}
	return body.Error.Type, code
}

// ---------------------------------------------------------------------------
// Entitlement
// ---------------------------------------------------------------------------

// A free caller reaching for a paid model must be refused BEFORE any provider
// call. The check is on the live user row, not on a claim baked into the
// token, so a lapsed membership stops buying inference on its very next call.
func TestGateway_InsufficientTierIsRefusedWithoutCallingUpstream(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{"usage":{"input_tokens":1}}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := h.post(t, "/anthropic", `{"model":"work-plus","messages":[]}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if h.upstreamCallCount() != 0 {
		t.Fatal("a refused caller must never reach the provider — platform money was spent on a denied request")
	}
	errType, code := decodeError(t, resp)
	if code != ErrClassInsufficientTier {
		t.Errorf("gateway_code = %q, want %q", code, ErrClassInsufficientTier)
	}
	if errType != "permission_error" {
		t.Errorf("error.type = %q, want permission_error", errType)
	}

	row := h.recorder.last(t)
	if row.ErrorClass != ErrClassInsufficientTier || row.HTTPStatus != http.StatusForbidden {
		t.Errorf("usage row did not record the refusal: %+v", row)
	}
}

// A 403 must explain the bar without publishing the menu. The catalog
// endpoint answers "what can I use"; this one must not become a second,
// cheaper enumeration oracle for someone probing with a stolen token.
func TestGateway_TierRefusalDoesNotEnumerateOtherModels(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := h.post(t, "/anthropic", `{"model":"work-plus"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	for _, forbidden := range []string{"work-pro", "claude-sonnet-5", "claude-opus-5", "items", "permissions"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("tier refusal leaked catalog contents (%q): %s", forbidden, raw)
		}
	}
}

// An expired paid membership is an unpaid one. Nothing may keep working past
// the window the payment bought.
func TestGateway_ExpiredMembershipLosesThePaidModel(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(-time.Hour))

	resp := h.post(t, "/anthropic", `{"model":"work-plus"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 once the paid window has passed", resp.StatusCode)
	}
}

func TestGateway_PaidMemberReachesThePaidModel(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{"usage":{"input_tokens":10,"output_tokens":4}}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))

	resp := h.post(t, "/anthropic", `{"model":"work-plus"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
}

// ---------------------------------------------------------------------------
// Catalog admission
// ---------------------------------------------------------------------------

// Only a catalog modelId is spendable. An arbitrary string must not become a
// pass-through to whatever the provider happens to serve — that would let a
// free caller name an expensive model the catalog never offered.
func TestGateway_ModelOutsideCatalogIsRejected(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))

	for _, name := range []string{"claude-opus-5", "gpt-4o", "work-ultra", ""} {
		t.Run("model="+name, func(t *testing.T) {
			resp := h.post(t, "/anthropic", fmt.Sprintf(`{"model":%q}`, name))
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				t.Fatalf("model %q was accepted; only catalog ids may be spent", name)
			}
			if h.upstreamCallCount() != 0 {
				t.Fatalf("model %q reached the provider", name)
			}
		})
	}
}

// A disabled row is an ops kill switch. It must read as "no such model",
// not as a transient error a client retries into.
func TestGateway_DisabledCatalogRowIsRejected(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))
	if err := h.db.Model(&model.GlobalModel{}).
		Where("model_id = ? AND media_type = ?", "work-pro", model.MediaTypeText).
		Update("status", model.GlobalModelStatusDisabled).Error; err != nil {
		t.Fatalf("disable model: %v", err)
	}

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if h.upstreamCallCount() != 0 {
		t.Fatal("a disabled model still reached the provider")
	}
}

// An image/video row shares the table but is a different product surface. It
// must never be spendable through the conversation gateway.
func TestGateway_NonTextMediaTypeIsNotSpendable(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))
	if err := h.db.Create(&model.GlobalModel{
		ModelID: "some-image-model", MediaType: model.MediaTypeImage,
		Status: model.GlobalModelStatusEnabled, RequiredTier: model.MemberTierFree,
		Metadata: model.JSONMap{model.GlobalModelMetadataUpstreamModel: "imagen"},
	}).Error; err != nil {
		t.Fatalf("seed image model: %v", err)
	}

	resp := h.post(t, "/anthropic", `{"model":"some-image-model"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// A catalog row with no upstream mapping fails closed. Forwarding the product
// name would surface as a confusing provider 404 rather than the ops gap it is.
func TestGateway_ModelWithoutUpstreamMappingFailsClosed(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))
	if err := h.db.Create(&model.GlobalModel{
		ModelID: "work-pro", MediaType: model.MediaTypeText,
		Status: model.GlobalModelStatusEnabled, RequiredTier: model.MemberTierFree,
	}).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if h.upstreamCallCount() != 0 {
		t.Fatal("an unmapped model reached the provider")
	}
	_, code := decodeError(t, resp)
	if code != ErrClassModelNotConfigured {
		t.Errorf("gateway_code = %q, want %q", code, ErrClassModelNotConfigured)
	}
}

// The catalog id is a product name; the provider must be sent the real model.
// Everything else in the body has to survive untouched — the gateway is a
// credential shim, not a request rewriter.
func TestGateway_CatalogIDIsTranslatedAndTheRestOfTheBodySurvives(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{"usage":{"input_tokens":3}}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := h.post(t, "/anthropic",
		`{"model":"work-pro","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"x"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	forwarded, _ := io.ReadAll(h.upstreamRequest(t).Body)
	var sent map[string]any
	if err := json.Unmarshal(forwarded, &sent); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if sent["model"] != "claude-sonnet-5" {
		t.Errorf("upstream model = %v, want claude-sonnet-5 (the catalog id must not go out)", sent["model"])
	}
	if sent["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens was not preserved: %v", sent["max_tokens"])
	}
	if _, ok := sent["tools"]; !ok {
		t.Error("tools was dropped; the gateway must forward fields it does not understand")
	}
}

// ---------------------------------------------------------------------------
// Credential containment
// ---------------------------------------------------------------------------

// The one promise that cannot bend: nothing about the platform credential —
// key, base URL, account name — may appear in any response, on any path.
// Mirrors the catalog's TestListModels_NeverLeaksProviderOrCredentialFields.
func TestGateway_NeverLeaksProviderCredentialOnAnyPath(t *testing.T) {
	seedForbidden := func(h *harness) []string {
		return []string{
			providerAPIKey,
			"sk-ant-platform",
			h.upstream.URL,
			"platform-anthropic-1",
			"apiKey",
			"api_key",
			"baseUrl",
			"base_url",
			"x-api-key",
		}
	}

	cases := []struct {
		name     string
		upstream http.HandlerFunc
		body     string
		member   int
	}{
		{
			name: "upstream rejects our key",
			upstream: jsonUpstream(http.StatusUnauthorized,
				`{"error":{"type":"authentication_error","message":"invalid x-api-key `+providerAPIKey+` for account platform-anthropic-1"}}`),
			body:   `{"model":"work-pro"}`,
			member: model.MEMBER_SUBSCRIPTION_NONE,
		},
		{
			name: "upstream server error echoes our config",
			upstream: jsonUpstream(http.StatusInternalServerError,
				`{"error":"backend for `+providerAPIKey+` exploded"}`),
			body:   `{"model":"work-pro"}`,
			member: model.MEMBER_SUBSCRIPTION_NONE,
		},
		{
			name:     "tier refusal",
			upstream: jsonUpstream(200, `{}`),
			body:     `{"model":"work-plus"}`,
			member:   model.MEMBER_SUBSCRIPTION_NONE,
		},
		{
			name:     "success path",
			upstream: jsonUpstream(200, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`),
			body:     `{"model":"work-pro"}`,
			member:   model.MEMBER_SUBSCRIPTION_NONE,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, tc.upstream)
			seedModels(t, h.db)
			h.uid = seedUser(t, h.db, tc.member, time.Time{})

			resp := h.post(t, "/anthropic", tc.body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)

			whole := string(raw)
			for name, values := range resp.Header {
				whole += "\n" + name + ": " + strings.Join(values, ",")
			}
			for _, forbidden := range seedForbidden(h) {
				if strings.Contains(whole, forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, whole)
				}
			}
		})
	}
}

// The Desktop's own bearer must not be handed to the provider, and the
// platform key must be. An allowlist is what makes both true.
func TestGateway_OutboundHeadersCarryPlatformKeyAndNotTheCallerToken(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	req, err := http.NewRequest(http.MethodPost, h.gateway.URL+"/anthropic",
		strings.NewReader(`{"model":"work-pro"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer desktop-oauth-token-xyz")
	req.Header.Set("X-WorkMax-Device-Id", "device-abc")
	req.Header.Set("anthropic-beta", "some-beta-2026-01-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("gateway request: %v", err)
	}
	defer resp.Body.Close()

	upstreamReq := h.upstreamRequest(t)
	if got := upstreamReq.Header.Get("x-api-key"); got != providerAPIKey {
		t.Errorf("x-api-key = %q, want the platform key", got)
	}
	if got := upstreamReq.Header.Get("Authorization"); got != "Bearer "+providerAPIKey {
		t.Errorf("Authorization = %q, want the platform key — the caller's token must be replaced, not forwarded", got)
	}
	if strings.Contains(upstreamReq.Header.Get("Authorization"), "desktop-oauth-token") {
		t.Error("the Desktop's own OAuth token was forwarded to the provider")
	}
	if upstreamReq.Header.Get("X-WorkMax-Device-Id") != "" {
		t.Error("device metadata was forwarded to the provider; the header list is an allowlist for a reason")
	}
	if got := upstreamReq.Header.Get("anthropic-beta"); got != "some-beta-2026-01-01" {
		t.Errorf("anthropic-beta = %q, want it forwarded — it is protocol negotiation, not identity", got)
	}
	if upstreamReq.Header.Get("anthropic-version") == "" {
		t.Error("anthropic-version should be defaulted when the client omits it")
	}
}

// ---------------------------------------------------------------------------
// Upstream error classification
// ---------------------------------------------------------------------------

func TestGateway_ClassifiesUpstreamFailures(t *testing.T) {
	cases := []struct {
		upstreamStatus int
		wantStatus     int
		wantClass      string
	}{
		// Our credential failing is never the caller's 401 — echoing it would
		// tell a prober that a key exists behind the gateway.
		{http.StatusUnauthorized, http.StatusBadGateway, ErrClassUpstreamAuth},
		{http.StatusForbidden, http.StatusBadGateway, ErrClassUpstreamAuth},
		// A model we admitted but the provider does not know is our mapping bug.
		{http.StatusNotFound, http.StatusBadGateway, ErrClassUpstreamError},
		// Only the request-shape rejection is the caller's to fix.
		{http.StatusBadRequest, http.StatusBadRequest, ErrClassUpstreamInvalidRequest},
		{http.StatusUnprocessableEntity, http.StatusBadRequest, ErrClassUpstreamInvalidRequest},
		{http.StatusTooManyRequests, http.StatusTooManyRequests, ErrClassUpstreamRateLimited},
		{http.StatusInternalServerError, http.StatusBadGateway, ErrClassUpstreamError},
		{http.StatusBadGateway, http.StatusBadGateway, ErrClassUpstreamError},
		{http.StatusServiceUnavailable, http.StatusBadGateway, ErrClassUpstreamError},
		{http.StatusGatewayTimeout, http.StatusGatewayTimeout, ErrClassUpstreamTimeout},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("upstream_%d", tc.upstreamStatus), func(t *testing.T) {
			h := newHarness(t, nil, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.upstreamStatus == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "7")
				}
				w.WriteHeader(tc.upstreamStatus)
				_, _ = io.WriteString(w, `{"error":{"message":"upstream prose about `+providerAPIKey+`"}}`)
			})
			seedModels(t, h.db)
			h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

			resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			_, code := decodeError(t, resp)
			if code != tc.wantClass {
				t.Errorf("gateway_code = %q, want %q", code, tc.wantClass)
			}
			if tc.upstreamStatus == http.StatusTooManyRequests && resp.Header.Get("Retry-After") != "7" {
				t.Errorf("Retry-After = %q, want the numeric upstream hint passed through", resp.Header.Get("Retry-After"))
			}
			row := h.recorder.last(t)
			if row.ErrorClass != tc.wantClass {
				t.Errorf("usage row error_class = %q, want %q", row.ErrorClass, tc.wantClass)
			}
			if h.accounts.failureCount() == 0 {
				t.Error("an upstream failure must feed the account pool's breaker, or a dead key is hammered forever")
			}
		})
	}
}

// A provider's error body routinely names the account or key it rejected. It
// is drained and discarded, never proxied.
func TestGateway_UpstreamErrorBodyIsNeverProxied(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(http.StatusInternalServerError,
		`{"error":{"message":"account platform-anthropic-1 key `+providerAPIKey+` is over quota","request_id":"req_upstream_123"}}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	for _, forbidden := range []string{providerAPIKey, "platform-anthropic-1", "req_upstream_123", "over quota"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("upstream error body was proxied (%q): %s", forbidden, raw)
		}
	}
}

// A dead upstream must not hold a concurrency slot until the process dies.
func TestGateway_UnreachableUpstreamIsClassifiedNotHung(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})
	// Point at a closed listener.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	h.accounts.account.BaseURL = deadURL

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway && resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 502 or 504", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), deadURL) {
		t.Fatalf("the provider host leaked in a transport error: %s", raw)
	}
}

// No healthy credential is a capacity problem, not a client problem, and the
// caller must not learn why.
func TestGateway_NoProviderAccountIsAServiceUnavailable(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})
	h.accounts.err = errNoAccountForProtocol

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	_, code := decodeError(t, resp)
	if code != ErrClassProviderUnavailable {
		t.Errorf("gateway_code = %q, want %q", code, ErrClassProviderUnavailable)
	}
}

// ---------------------------------------------------------------------------
// Streaming
// ---------------------------------------------------------------------------

// The contract a tool loop depends on: frame N reaches the client before the
// upstream has produced frame N+1. A recorder cannot prove this — the test
// gates the upstream on the client having actually read the first frame, so
// it deadlocks (and fails) if the gateway buffers.
func TestGateway_StreamsFramesThroughWithoutBuffering(t *testing.T) {
	firstFrameRead := make(chan struct{})
	upstreamDone := make(chan struct{})

	h := newHarness(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream test server cannot flush")
			return
		}
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11,\"cache_read_input_tokens\":3}}}\n\n")
		flusher.Flush()

		select {
		case <-firstFrameRead:
		case <-time.After(5 * time.Second):
			t.Error("client never received the first frame before the second was produced — the gateway buffered the stream")
			return
		}

		_, _ = io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		flusher.Flush()
	})
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := h.post(t, "/anthropic", `{"model":"work-pro","stream":true}`)
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream passed through", got)
	}

	reader := bufio.NewReader(resp.Body)
	first, err := readSSEFrame(reader)
	if err != nil {
		t.Fatalf("read first frame: %v", err)
	}
	if !strings.Contains(first, "message_start") {
		t.Fatalf("first frame = %q, want message_start", first)
	}
	// Only now does the upstream get to produce anything else.
	close(firstFrameRead)

	rest, _ := io.ReadAll(reader)
	if !strings.Contains(string(rest), "message_delta") || !strings.Contains(string(rest), "message_stop") {
		t.Fatalf("later frames missing: %q", rest)
	}
	<-upstreamDone

	row := h.recorder.last(t)
	if !row.Stream {
		t.Error("usage row did not mark the call as streaming")
	}
	// Usage is scraped from the stream in flight, so a streamed turn is
	// metered as accurately as a buffered one.
	if row.Usage.InputTokens != 11 || row.Usage.OutputTokens != 42 || row.Usage.CacheReadTokens != 3 {
		t.Errorf("streamed usage = %+v, want input 11 / output 42 / cache-read 3", row.Usage)
	}
}

// readSSEFrame reads up to and including the blank line that terminates one
// SSE frame, so the test can assert on frame boundaries rather than bytes.
func readSSEFrame(reader *bufio.Reader) (string, error) {
	var builder strings.Builder
	for {
		line, err := reader.ReadString('\n')
		builder.WriteString(line)
		if err != nil {
			return builder.String(), err
		}
		if strings.TrimSpace(line) == "" && builder.Len() > 1 {
			return builder.String(), nil
		}
	}
}

// ---------------------------------------------------------------------------
// Metering
// ---------------------------------------------------------------------------

func TestGateway_MetersBufferedResponses(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200,
		`{"usage":{"input_tokens":100,"output_tokens":25,"cache_read_input_tokens":7,"cache_creation_input_tokens":2}}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	row := h.recorder.last(t)
	if row.Usage.InputTokens != 100 || row.Usage.OutputTokens != 25 {
		t.Fatalf("usage = %+v, want input 100 / output 25", row.Usage)
	}
	if row.Usage.Total() != 134 {
		t.Errorf("total = %d, want 134", row.Usage.Total())
	}
	if row.ProviderAccountID != 77 {
		t.Errorf("provider_account_id = %d, want the account that paid", row.ProviderAccountID)
	}
	if row.ModelID != "work-pro" || row.UpstreamModel != "claude-sonnet-5" {
		t.Errorf("row does not record both names: %+v", row)
	}
	if row.Status != model.DesktopModelGatewayUsageStatusCompleted {
		t.Errorf("status = %q, want completed", row.Status)
	}
	if row.RequestID == "" {
		t.Error("request id is empty; a support report has nothing to join on")
	}
	if got := resp.Header.Get(GatewayRequestIDHeader); got != row.RequestID {
		t.Errorf("response request id %q != recorded %q", got, row.RequestID)
	}
}

// The DB recorder is what production uses; exercise it against the real schema
// so a column rename cannot pass the fake-recorder tests.
func TestGateway_UsageRowLandsInTheDatabase(t *testing.T) {
	db := testutil.NewTestDB(t)
	recorder := NewDBUsageRecorder(db)
	recorder.Record(UsageRecord{
		UID: 5, RequestID: "req-1", Protocol: ProtocolOpenAI, ModelID: "work-pro",
		UpstreamModel: "claude-sonnet-5", ProviderAccountID: 9, Stream: true,
		Status: model.DesktopModelGatewayUsageStatusCompleted, HTTPStatus: 200,
		Usage:     TokenUsage{InputTokens: 4, OutputTokens: 6},
		StartedAt: time.Now(), Duration: 1500 * time.Millisecond,
	})

	var row model.DesktopModelGatewayUsage
	if err := db.Where("request_id = ?", "req-1").First(&row).Error; err != nil {
		t.Fatalf("usage row not persisted: %v", err)
	}
	if row.TotalTokens != 10 {
		t.Errorf("total_tokens = %d, want 10", row.TotalTokens)
	}
	if row.DurationMS != 1500 {
		t.Errorf("duration_ms = %d, want 1500", row.DurationMS)
	}
	if row.ProviderAccountID != 9 {
		t.Errorf("provider_account_id = %d, want 9", row.ProviderAccountID)
	}
}

// ---------------------------------------------------------------------------
// Abuse guards
// ---------------------------------------------------------------------------

// The gateway spends platform money, so the tap has to close. A per-minute
// budget of one proves the counter is real and that the refusal happens before
// the provider is called.
func TestGateway_PerUserRateLimitStopsSpending(t *testing.T) {
	cfg := &config.ModelGateway{
		RateLimit: config.ModelGatewayRateLimit{PerMinute: 1, PerUserConcurrent: 4, GlobalConcurrent: 8},
	}
	h := newHarness(t, cfg, jsonUpstream(200, `{"usage":{"input_tokens":1}}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	first := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	_, _ = io.ReadAll(first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", first.StatusCode)
	}

	second := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", second.StatusCode)
	}
	if h.upstreamCallCount() != 1 {
		t.Fatalf("upstream was called %d times; a throttled request must not spend", h.upstreamCallCount())
	}
	if second.Header.Get("Retry-After") == "" {
		t.Error("a 429 without Retry-After tells a client nothing but 'try again now'")
	}
	_, code := decodeError(t, second)
	if code != ErrClassRateLimited {
		t.Errorf("gateway_code = %q, want %q", code, ErrClassRateLimited)
	}
	if h.recorder.count() != 2 {
		t.Errorf("recorded %d rows, want 2 — a throttled call is still evidence", h.recorder.count())
	}
}

// Both protocol routes draw on the same budget: a user must not double their
// spend by alternating between them.
func TestGateway_RateLimitIsSharedAcrossProtocols(t *testing.T) {
	cfg := &config.ModelGateway{
		RateLimit: config.ModelGatewayRateLimit{PerMinute: 1, PerUserConcurrent: 4, GlobalConcurrent: 8},
	}
	h := newHarness(t, cfg, jsonUpstream(200, `{"usage":{"prompt_tokens":1}}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})
	h.accounts.account.Provider = "openai"

	first := h.post(t, "/openai", `{"model":"work-pro"}`)
	_, _ = io.ReadAll(first.Body)
	first.Body.Close()

	second := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 — the two routes must share one budget", second.StatusCode)
	}
}

// A body cap that only rejects after buffering is not a cap.
func TestGateway_OversizedBodyIsRejected(t *testing.T) {
	cfg := &config.ModelGateway{MaxRequestBytes: 512}
	h := newHarness(t, cfg, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	big := `{"model":"work-pro","padding":"` + strings.Repeat("x", 4096) + `"}`
	resp := h.post(t, "/anthropic", big)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if h.upstreamCallCount() != 0 {
		t.Fatal("an oversized body reached the provider")
	}
}

func TestGateway_DisabledGatewayRefusesEverything(t *testing.T) {
	disabled := false
	cfg := &config.ModelGateway{Enabled: &disabled}
	h := newHarness(t, cfg, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if h.upstreamCallCount() != 0 {
		t.Fatal("a disabled gateway still spent platform money")
	}
}

func TestGateway_UnauthenticatedCallerIsRefused(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = 0

	resp := h.post(t, "/anthropic", `{"model":"work-pro"}`)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestGateway_MalformedBodyIsRejected(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	for _, body := range []string{``, `not json`, `[]`, `"a string"`} {
		resp := h.post(t, "/anthropic", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if h.upstreamCallCount() != 0 {
		t.Fatal("a malformed body reached the provider")
	}
}

// An upstream that announces a body and then hangs up must not produce a
// half-written 200. Headers have not gone out yet at that point, so the
// honest answer is still a clean error — in the CALLER's protocol, which is
// the part that is easy to get wrong on a rarely-exercised path.
func TestGateway_TruncatedBufferedResponseErrorsInTheCallersProtocol(t *testing.T) {
	h := newHarness(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"usage":`)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Returning without writing the promised bytes makes net/http close
		// the connection, which the gateway sees as a truncated body.
	})
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})
	h.accounts.account.Provider = "openai"

	resp := h.post(t, "/openai", `{"model":"work-pro"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", resp.StatusCode, raw)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v (raw %s)", err, raw)
	}
	if _, ok := body["type"]; ok {
		t.Errorf("an OpenAI caller got the Anthropic error shape: %s", raw)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object: %s", raw)
	}
	if errObj["code"] != ErrClassUpstreamError {
		t.Errorf("error.code = %v, want %q", errObj["code"], ErrClassUpstreamError)
	}
	if row := h.recorder.last(t); row.Status != model.DesktopModelGatewayUsageStatusFailed {
		t.Errorf("usage row status = %q, want failed", row.Status)
	}
}

// The OpenAI route must answer in the OpenAI error shape — a client that
// switches on `error.type` should not have to special-case our gateway.
func TestGateway_OpenAIRouteUsesOpenAIErrorShape(t *testing.T) {
	h := newHarness(t, nil, jsonUpstream(200, `{}`))
	seedModels(t, h.db)
	h.uid = seedUser(t, h.db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := h.post(t, "/openai", `{"model":"work-plus"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["type"]; ok {
		t.Errorf("OpenAI error body must not carry the Anthropic top-level `type`: %s", raw)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object: %s", raw)
	}
	if errObj["code"] != ErrClassInsufficientTier {
		t.Errorf("error.code = %v, want %q", errObj["code"], ErrClassInsufficientTier)
	}
}
