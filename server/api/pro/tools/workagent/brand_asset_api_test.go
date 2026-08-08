package workagent

// brand_asset_api_test.go — Backlog #11 4/n REST coverage. Same
// in-memory-DB + httptest pattern as agent_direction_api_test.go.
// Pins three load-bearing contracts:
//
//   1. IDOR: cross-tenant id reads collapse to 404
//   2. Status filter: list excludes archived; show surfaces all
//      non-soft-deleted statuses (draft / confirmed)
//   3. Confirm idempotence: clicking twice doesn't error

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"server/model"
	"server/service/brand"
	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildBrandAssetEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/brand-assets", withClaims(uid), api.ListBrandAssets)
	r.GET("/brand-assets/:id", withClaims(uid), api.GetBrandAsset)
	r.POST("/brand-assets/:id/confirm", withClaims(uid), api.ConfirmBrandAsset)
	r.POST("/brand-assets/:id/restore", withClaims(uid), api.RestoreBrandAsset)
	r.DELETE("/brand-assets/:id", withClaims(uid), api.DeleteBrandAsset)
	return r
}

// getRequest / deleteRequest — siblings of postJSON
// (agent_api_handle_chat_test.go) for the methods this file needs.
// Same *httptest.ResponseRecorder return shape so assertions read
// the same way (w.Code, w.Body.String()).
func getRequest(engine *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	engine.ServeHTTP(w, req)
	return w
}

func deleteRequest(engine *gin.Engine, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	engine.ServeHTTP(w, req)
	return w
}

// seedBrandAssetForAPI seeds a *model.Brand row into w_global_brand
// for the API test suite. Sprint-E retargeted the brand descriptor
// at the platform table; this fixture writes there directly.
// confirmed=true → ready (visible to FindLatestActiveForOwner);
// confirmed=false → draft (rides preflight under the M4 watermark).
//
// Callers that need a soft-deleted row should call
// brand.Default().SoftDelete after this returns — the fixture
// stays single-purpose to keep the call sites readable.
func seedBrandAssetForAPI(t *testing.T, uid int, confirmed bool, name string) *model.Brand {
	t.Helper()
	b := &model.Brand{
		UID:        uid,
		Name:       name,
		Slug:       name + "-slug",
		Status:     model.BrandStatusActive,
		Confirmed:  confirmed,
		SourceKind: model.BrandSourceExtracted,
		Lang:       "en",
	}
	// brand.Default().Create internally Select("*")s so an explicit
	// Confirmed=false survives the DDL DEFAULT 1 (zero-value-skip
	// would otherwise clobber it).
	if err := brand.Default().Create(b); err != nil {
		t.Fatalf("seed brand: %v", err)
	}
	return b
}

// TestListBrandAssets_HappyPath — list returns the user's brands;
// summary shape (full JSON sections excluded to keep payload light).
func TestListBrandAssets_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	seedBrandAssetForAPI(t, 42, true, "acme")
	seedBrandAssetForAPI(t, 42, false, "draft-brand")

	engine := buildBrandAssetEngine(t, 42)
	w := getRequest(engine, "/brand-assets")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Items []brandAssetSummary `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Items) != 2 {
		t.Errorf("got %d items, want 2", len(resp.Data.Items))
	}
	for _, it := range resp.Data.Items {
		if it.UID != 42 {
			t.Errorf("cross-tenant leak: uid=%d", it.UID)
		}
	}
}

// TestListBrandAssets_CrossTenantIsolation — uid=99 sees zero rows
// when only uid=42 has brands.
func TestListBrandAssets_CrossTenantIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 99)
	w := getRequest(engine, "/brand-assets")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("uid=99 should see empty list, got %q", w.Body.String())
	}
}

// TestListBrandAssets_RespectsLimit — ?limit= clamps result count.
func TestListBrandAssets_RespectsLimit(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	for _, n := range []string{"a", "b", "c"} {
		seedBrandAssetForAPI(t, 42, true, n)
	}
	engine := buildBrandAssetEngine(t, 42)
	w := getRequest(engine, "/brand-assets?limit=2")
	var resp struct {
		Data struct {
			Items      []brandAssetSummary `json:"items"`
			Pagination struct {
				HasMore bool `json:"hasMore"`
			} `json:"pagination"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Items) != 2 {
		t.Errorf("limit=2 should return 2 items, got %d", len(resp.Data.Items))
	}
	if !resp.Data.Pagination.HasMore {
		t.Error("hasMore should be true when filling the limit")
	}
}

func TestListBrandAssets_Unauthorized(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildBrandAssetEngine(t, 0)
	w := getRequest(engine, "/brand-assets")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// Wire-shape regression — brandLibraryAsset.MarshalJSON projects
// raw Status int8 onto the asset_library enum string so the
// frontend AssetDetailDrawer's `detail.status === 'draft'` gate
// works. Without MarshalJSON, draft brand assets ship
// `"status": 1` and the drawer's Confirm button never appears.
// Mirrors the parallel pin on product_asset_api_test +
// character_asset_api_test + director_style_asset_api_test.
func TestGetBrandAsset_StatusProjectedToEnumString(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	draft := seedBrandAssetForAPI(t, 42, false, "draft-brand")
	confirmed := seedBrandAssetForAPI(t, 42, true, "confirmed-brand")

	engine := buildBrandAssetEngine(t, 42)

	wDraft := getRequest(engine, "/brand-assets/"+strFromUint(draft.Id))
	if wDraft.Code != http.StatusOK {
		t.Fatalf("draft GET = %d, want 200", wDraft.Code)
	}
	if !strings.Contains(wDraft.Body.String(), `"status":"draft"`) {
		t.Errorf("draft must ship status:\"draft\" string enum; got %s", wDraft.Body.String())
	}
	if strings.Contains(wDraft.Body.String(), `"status":0`) || strings.Contains(wDraft.Body.String(), `"status":1`) {
		t.Errorf("draft leaking raw int status: %s", wDraft.Body.String())
	}

	wConfirmed := getRequest(engine, "/brand-assets/"+strFromUint(confirmed.Id))
	if !strings.Contains(wConfirmed.Body.String(), `"status":"confirmed"`) {
		t.Errorf("confirmed must ship status:\"confirmed\"; got %s", wConfirmed.Body.String())
	}
}

// TestGetBrandAsset_HappyPath — full row including JSON sections.
func TestGetBrandAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 42)
	w := getRequest(engine, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name":"acme"`) {
		t.Errorf("response missing brand name: %s", w.Body.String())
	}
}

// TestGetBrandAsset_CrossTenant404 — IDOR defence.
func TestGetBrandAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 99) // not the owner
	w := getRequest(engine, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant should 404, got %d", w.Code)
	}
}

func TestGetBrandAsset_UnknownID404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildBrandAssetEngine(t, 42)
	w := getRequest(engine, "/brand-assets/999")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown id should 404, got %d", w.Code)
	}
}

func TestGetBrandAsset_BadID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildBrandAssetEngine(t, 42)
	w := getRequest(engine, "/brand-assets/abc")
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id should 400, got %d", w.Code)
	}
}

// TestConfirmBrandAsset_HappyPath — draft → confirmed atomically.
func TestConfirmBrandAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, false, "acme")

	engine := buildBrandAssetEngine(t, 42)
	w := postJSON(engine, "/brand-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"persisted":true`) {
		t.Errorf("body missing persisted=true: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"confirmed"`) {
		t.Errorf("body missing confirmed status: %s", w.Body.String())
	}
}

// TestConfirmBrandAsset_Idempotent — confirming an already-confirmed
// brand returns 200 (not 404). The repo's idempotent path returns
// ErrRecordNotFound but we disambiguate by re-loading; an
// already-confirmed row reports persisted=true.
func TestConfirmBrandAsset_Idempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 42)
	w := postJSON(engine, "/brand-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
	if w.Code != http.StatusOK {
		t.Errorf("second confirm should still 200, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestConfirmBrandAsset_CrossTenant404 — uid=99 confirming uid=42's
// brand collapses to 404 (no existence oracle).
func TestConfirmBrandAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, false, "acme")

	engine := buildBrandAssetEngine(t, 99)
	w := postJSON(engine, "/brand-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant confirm should 404, got %d", w.Code)
	}
}

// TestDeleteBrandAsset_HappyPath — soft-delete; subsequent show
// returns 404 (deleted rows hidden by repo predicate).
func TestDeleteBrandAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 42)
	w := deleteRequest(engine, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Subsequent get returns 404.
	w = getRequest(engine, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("deleted brand should 404 on subsequent get, got %d", w.Code)
	}
}

// TestRestoreBrandAsset_HappyPath — soft-delete → restore round-trip
// surfaces the row again with status=draft.
// TestExtractPaletteSwatches — schema-flexible extraction. Pulls
// recognisable hex colors regardless of surrounding key structure.
func TestExtractPaletteSwatches(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "conventional M4 palette",
			raw:  `{"primary":"#3151c4","accent":"#b75240","muted":"#a8a8a8","bg":"#fafafa","fg":"#1a1a1a"}`,
			// Conventional ordering primary→accent→muted→bg, capped at 4.
			want: []string{"#3151c4", "#b75240", "#a8a8a8", "#fafafa"},
		},
		{
			name: "non-standard keys walked alphabetically (deterministic)",
			raw:  `{"brand_main":"#ff0000","brand_alt":"#00ff00"}`,
			// brand_alt < brand_main alphabetically.
			want: []string{"#00ff00", "#ff0000"},
		},
		{
			name: "nested palette object — keys sorted by walk",
			raw:  `{"palette":{"support":"#abcdef","hero":"#123456"}}`,
			// hero < support alphabetically.
			want: []string{"#123456", "#abcdef"},
		},
		{
			name: "short hex (#abc) accepted, sorted by key",
			raw:  `{"y":"#def","x":"#abc"}`,
			// x < y alphabetically.
			want: []string{"#abc", "#def"},
		},
		{
			name: "non-hex strings ignored",
			raw:  `{"primary":"#3151c4","tone":"warm","note":"see brief"}`,
			want: []string{"#3151c4"},
		},
		{
			name: "duplicates de-duped, case-insensitive",
			raw:  `{"a":"#abc","b":"#ABC","c":"#def"}`,
			// Sorted walk: a → "#abc" inserted; b → "#ABC" deduped; c → "#def".
			want: []string{"#abc", "#def"},
		},
		{
			name: "more than 4 colors capped at 4",
			raw:  `{"primary":"#111111","accent":"#222222","muted":"#333333","bg":"#444444","extra1":"#555555","extra2":"#666666"}`,
			want: []string{"#111111", "#222222", "#333333", "#444444"},
		},
		{
			name: "null returns nil",
			raw:  "null",
			want: nil,
		},
		{
			name: "empty object returns nil",
			raw:  "{}",
			want: nil,
		},
		{
			name: "malformed JSON returns nil",
			raw:  `{not json`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := workagentService.ExtractPaletteSwatches([]byte(tc.raw))
			if len(got) != len(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
				return
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got %v, want %v", got, tc.want)
					return
				}
			}
		})
	}
}

// TestListBrandAssets_ReturnsSwatches — end-to-end: list response
// includes paletteSwatches when the brand has colors.
func TestListBrandAssets_ReturnsSwatches(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	b := &model.Brand{
		UID:        42,
		Name:       "Acme",
		Slug:       "acme",
		Status:     model.BrandStatusActive,
		Confirmed:  true,
		SourceKind: model.BrandSourceExtracted,
		Lang:       "en",
		Colors:     model.JSONMap{"primary": "#3151c4", "accent": "#b75240"},
	}
	if err := brand.Default().Create(b); err != nil {
		t.Fatalf("create: %v", err)
	}

	engine := buildBrandAssetEngine(t, 42)
	w := getRequest(engine, "/brand-assets")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"paletteSwatches":["#3151c4","#b75240"]`) {
		t.Errorf("response missing palette swatches: %s", w.Body.String())
	}
}

func TestRestoreBrandAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 42)
	w := deleteRequest(engine, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d", w.Code)
	}
	w = postJSON(engine, "/brand-assets/"+strFromUint(asset.Id)+"/restore", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("restore status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"restored":true`) {
		t.Errorf("body missing restored=true: %s", w.Body.String())
	}

	// Subsequent get surfaces the restored row at draft state.
	// Sprint-E wire shape: platform model emits Status as int8 + a
	// Confirmed bool. "Draft" = Confirmed=false (Status stays at
	// Active=1 throughout). Older asserts looking for "status":"draft"
	// migrated to "confirmed":false.
	w = getRequest(engine, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("get after restore = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"confirmed":false`) {
		t.Errorf("restored brand should land unconfirmed (confirmed:false), got %s", w.Body.String())
	}
}

// TestRestoreBrandAsset_NotDeleted404 — restore on a row that isn't
// soft-deleted (active OR cross-tenant) collapses to 404.
func TestRestoreBrandAsset_NotDeleted404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 42)
	// Active row (never deleted) → 404.
	w := postJSON(engine, "/brand-assets/"+strFromUint(asset.Id)+"/restore", map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Errorf("restore on active row should 404, got %d", w.Code)
	}
}

func TestRestoreBrandAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	// Owner deletes the asset.
	owner := buildBrandAssetEngine(t, 42)
	w := deleteRequest(owner, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatal("delete failed")
	}

	// Cross-tenant attempt to restore — collapse to 404.
	other := buildBrandAssetEngine(t, 99)
	w = postJSON(other, "/brand-assets/"+strFromUint(asset.Id)+"/restore", map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant restore should 404, got %d", w.Code)
	}
}

func TestDeleteBrandAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedBrandAssetForAPI(t, 42, true, "acme")

	engine := buildBrandAssetEngine(t, 99)
	w := deleteRequest(engine, "/brand-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete should 404, got %d", w.Code)
	}
}
