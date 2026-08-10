//go:build desktop

package desktop

import (
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

// The catalog endpoint has one job the renderer cannot do for itself: say
// whether the model this identity already picked is still allowed. Everything
// below is about that verdict never being softened into a silent substitution.

type modelCatalogFixture struct {
	base      string
	db        *gorm.DB
	settings  *LocalModelSettingsStore
	callCount *atomic.Int64
	// catalogJSON is what the fake cloud answers; swap it between requests to
	// simulate a membership changing underneath the desktop.
	catalogJSON *atomic.Value
}

const modelCatalogTestUID = uint64(4242)

func newModelCatalogFixture(t *testing.T, bound bool) modelCatalogFixture {
	t.Helper()
	db := openMigratedTestDB(t)
	settings := NewLocalModelSettingsStore(db, newMemKeychain())

	calls := &atomic.Int64{}
	body := &atomic.Value{}
	body.Store(`{"items":[],"tier":"free"}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == cloudproxy.CloudRouteModelsList {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body.Load().(string))
			return
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(upstream.Close)

	tokens := cloudproxy.NewTokenStore(newMemKeychain())
	if bound {
		if err := tokens.Save(sessionLeaseTokenPair(mintLocalHistoryJWT(modelCatalogTestUID), "refresh")); err != nil {
			t.Fatalf("seed session: %v", err)
		}
	}
	cloud := cloudproxy.NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()
	proxy := cloudproxy.NewProxy(cloud, tokens, db)
	proxy.HTTPClient = upstream.Client()

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "model-catalog-test",
		LocalToken:     "catalog-token",
		DB:             db,
		TokenStore:     tokens,
		Proxy:          proxy,
		ModelSettings:  settings,
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

	return modelCatalogFixture{
		base:        "http://" + srv.listener.Addr().String(),
		db:          db,
		settings:    settings,
		callCount:   calls,
		catalogJSON: body,
	}
}

func (f modelCatalogFixture) get(t *testing.T) modelCatalogResponse {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, f.base+"/settings/model-catalog", nil)
	request.Header.Set("X-Local-Token", "catalog-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET model catalog: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET model catalog = %d %s", response.StatusCode, raw)
	}
	if strings.Contains(string(raw), "api_key") || strings.Contains(string(raw), "baseUrl") {
		t.Fatalf("catalog response must carry metadata only: %s", raw)
	}
	var body modelCatalogResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode catalog: %v (%s)", err, raw)
	}
	return body
}

// No account connected is an ordinary state, not a failure: the local route is
// a complete way to use this app, and answering 401 would make the settings
// form look broken to somebody who simply has not signed in.
func TestModelCatalogWithoutAnAccountIsEmptyRatherThanAnError(t *testing.T) {
	fixture := newModelCatalogFixture(t, false)
	body := fixture.get(t)
	if body.State != ModelCatalogUnbound {
		t.Fatalf("state = %q, want %q", body.State, ModelCatalogUnbound)
	}
	if len(body.Items) != 0 || body.Count != 0 {
		t.Fatalf("unbound catalog must be empty: %+v", body)
	}
	if body.SelectionState != ModelSelectionUnset {
		t.Fatalf("selection_state = %q", body.SelectionState)
	}
	if fixture.callCount.Load() != 0 {
		t.Fatal("an unbound desktop must not call the cloud at all")
	}
}

func TestModelCatalogReportsEntitlementAndCachesTheAnswer(t *testing.T) {
	fixture := newModelCatalogFixture(t, true)
	fixture.catalogJSON.Store(`{"items":[
		{"modelId":"work-pro","displayName":"WorkMax Pro","requiredTier":"free","permissions":["use"],"default":true},
		{"modelId":"work-plus","displayName":"WorkMax Plus","requiredTier":"pro","permissions":[]}
	],"tier":"free","tierExpiresAt":"2026-09-01T00:00:00Z"}`)

	body := fixture.get(t)
	if body.State != ModelCatalogReady || body.Count != 2 {
		t.Fatalf("catalog = %+v", body)
	}
	if body.Tier != "free" || body.TierExpiresAt != "2026-09-01T00:00:00Z" {
		t.Fatalf("tier metadata dropped: %+v", body)
	}
	// The locked model is RETURNED, not filtered: a paid tier the user cannot
	// see is a paid tier the user cannot want.
	if body.Items[1].ModelID != "work-plus" || body.Items[1].Usable() {
		t.Fatalf("locked item = %+v", body.Items[1])
	}
	if body.SelectionState != ModelSelectionUnset {
		t.Fatalf("nothing chosen yet, got selection_state %q", body.SelectionState)
	}

	// Second read inside the TTL is served from cache: opening Settings must
	// not be a cloud round trip every time.
	fixture.get(t)
	if got := fixture.callCount.Load(); got != 1 {
		t.Fatalf("cloud called %d times, want 1 (short-TTL cache)", got)
	}
}

// The heart of it: a stored choice that stopped being allowed is reported as a
// question, never quietly replaced with something that would work.
func TestModelCatalogSurfacesADowngradedSelectionInsteadOfFallingBack(t *testing.T) {
	fixture := newModelCatalogFixture(t, true)
	if _, err := fixture.settings.Put(modelCatalogTestUID, LocalModelSettingsPut{
		PreferredRoute:  ModelRouteOfficial,
		OfficialModelID: strPtr("work-plus"),
	}); err != nil {
		t.Fatalf("store selection: %v", err)
	}

	fixture.catalogJSON.Store(`{"items":[
		{"modelId":"work-pro","displayName":"WorkMax Pro","requiredTier":"free","permissions":["use"],"default":true},
		{"modelId":"work-plus","displayName":"WorkMax Plus","requiredTier":"pro","permissions":[]}
	],"tier":"free"}`)

	body := fixture.get(t)
	if body.SelectionState != ModelSelectionNotAllowed {
		t.Fatalf("selection_state = %q, want %q", body.SelectionState, ModelSelectionNotAllowed)
	}
	if body.SelectedModelID != "work-plus" {
		t.Fatalf("selected_model_id = %q — the user's choice must be reported back, not replaced", body.SelectedModelID)
	}
	// And the sidecar has NOT rewritten the stored preference behind the
	// user's back.
	stored, err := fixture.settings.Get(modelCatalogTestUID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.OfficialModelID != "work-plus" {
		t.Fatalf("stored selection was silently changed to %q", stored.OfficialModelID)
	}
}

func TestModelCatalogClassifiesARetiredSelectionAsUnknown(t *testing.T) {
	fixture := newModelCatalogFixture(t, true)
	if _, err := fixture.settings.Put(modelCatalogTestUID, LocalModelSettingsPut{
		PreferredRoute:  ModelRouteOfficial,
		OfficialModelID: strPtr("work-legacy"),
	}); err != nil {
		t.Fatal(err)
	}
	fixture.catalogJSON.Store(`{"items":[{"modelId":"work-pro","displayName":"Pro","requiredTier":"free","permissions":["use"],"default":true}],"tier":"free"}`)
	if got := fixture.get(t).SelectionState; got != ModelSelectionUnknown {
		t.Fatalf("selection_state = %q, want %q", got, ModelSelectionUnknown)
	}
}

// A cloud we could not reach must not read as "your plan has no models".
func TestModelCatalogUnreachableCloudIsUnavailableNotEmptyEntitlement(t *testing.T) {
	fixture := newModelCatalogFixture(t, true)
	fixture.catalogJSON.Store(`not json at all`)
	body := fixture.get(t)
	if body.State != ModelCatalogUnavailable {
		t.Fatalf("state = %q, want %q", body.State, ModelCatalogUnavailable)
	}
}

func strPtr(value string) *string { return &value }

// The selection reaches the cloud through the field the chat contract already
// has for it, and an unchosen model omits the field entirely so the account
// default still applies.
func TestChatPayloadCarriesTheChosenOfficialModelAsModelTier(t *testing.T) {
	withModel, err := payloadWithDesktopChatContract(nil, "42", "ppt", "hi", "work-plus")
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(withModel, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Metadata["modelTier"] != "work-plus" {
		t.Fatalf("metadata.modelTier = %v", parsed.Metadata["modelTier"])
	}

	withoutModel, err := payloadWithDesktopChatContract(nil, "42", "ppt", "hi", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutModel), "modelTier") {
		t.Fatalf("an unchosen model must not pin a tier: %s", withoutModel)
	}
}
