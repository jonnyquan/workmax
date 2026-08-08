package workagent

// product_asset_api_test.go — P1 #5 slice 3. Mirrors
// brand_asset_api_test.go's pattern: same in-memory-DB +
// httptest harness, same load-bearing contracts (IDOR, status
// filter, confirm idempotence). Product surface uses the
// generic asset_library handlers so passing here ≈ product
// joins brand/character/director-style as a first-class kind.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"server/model"
	"server/service/product"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildProductAssetEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/product-assets", withClaims(uid), api.ListProductAssets)
	r.GET("/product-assets/:id", withClaims(uid), api.GetProductAsset)
	r.POST("/product-assets/:id/confirm", withClaims(uid), api.ConfirmProductAsset)
	r.POST("/product-assets/:id/restore", withClaims(uid), api.RestoreProductAsset)
	r.DELETE("/product-assets/:id", withClaims(uid), api.DeleteProductAsset)
	return r
}

// seedProductAssetForAPI seeds a *model.Product row into
// w_global_product. Mirrors seedBrandAssetForAPI's contract:
// confirmed=true → ready; confirmed=false → draft.
func seedProductAssetForAPI(t *testing.T, uid int, confirmed bool, name string) *model.Product {
	t.Helper()
	p := &model.Product{
		UID:        uid,
		Name:       name,
		Slug:       name + "-slug",
		SKU:        "SKU-" + name,
		Category:   "shoes",
		Status:     model.ProductStatusActive,
		Confirmed:  confirmed,
		SourceKind: model.ProductSourceExtracted,
		Lang:       "en",
	}
	if err := product.Default().Create(p); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------
// ListProductAssets
// ---------------------------------------------------------------------

func TestListProductAssets_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	seedProductAssetForAPI(t, 42, true, "acme-runner")
	seedProductAssetForAPI(t, 42, false, "draft-sku")

	engine := buildProductAssetEngine(t, 42)
	w := getRequest(engine, "/product-assets")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Items []productAssetSummary `json:"items"`
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
		if it.SKU == "" {
			t.Errorf("SKU should be exposed at top level for picker filter; got empty")
		}
	}
}

func TestListProductAssets_CrossTenantIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	seedProductAssetForAPI(t, 42, true, "acme-runner")

	engine := buildProductAssetEngine(t, 99)
	w := getRequest(engine, "/product-assets")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("uid=99 should see empty list, got %q", w.Body.String())
	}
}

func TestListProductAssets_Unauthorized(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildProductAssetEngine(t, 0)
	w := getRequest(engine, "/product-assets")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// ---------------------------------------------------------------------
// GetProductAsset
// ---------------------------------------------------------------------

func TestGetProductAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedProductAssetForAPI(t, 42, true, "acme-runner")

	engine := buildProductAssetEngine(t, 42)
	w := getRequest(engine, "/product-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	// Detail response carries full row; verify the SKU is there.
	if !strings.Contains(w.Body.String(), "SKU-acme-runner") {
		t.Errorf("detail response missing SKU; got %s", w.Body.String())
	}
}

// Wire-shape regression — Polish #17 fix. The wrapper's
// MarshalJSON projects raw Status int8 onto the asset_library
// enum string ("draft" / "confirmed" / "archived") so the
// frontend AssetDetailDrawer's `detail.status === 'draft'`
// gate works. Without MarshalJSON, GET /:id ships
// `"status": 1` and every draft asset's Confirm button is
// hidden in the drawer.
//
// Test verifies both polarities: a draft row ships "draft",
// a confirmed row ships "confirmed".
func TestGetProductAsset_StatusProjectedToEnumString(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	draft := seedProductAssetForAPI(t, 42, false, "draft-row")
	confirmed := seedProductAssetForAPI(t, 42, true, "confirmed-row")

	engine := buildProductAssetEngine(t, 42)

	wDraft := getRequest(engine, "/product-assets/"+strFromUint(draft.Id))
	if wDraft.Code != http.StatusOK {
		t.Fatalf("draft status = %d, want 200", wDraft.Code)
	}
	if !strings.Contains(wDraft.Body.String(), `"status":"draft"`) {
		t.Errorf("draft response must ship status:\"draft\" (string enum), got %s", wDraft.Body.String())
	}
	if strings.Contains(wDraft.Body.String(), `"status":0`) || strings.Contains(wDraft.Body.String(), `"status":1`) {
		t.Errorf("draft response leaking raw int status: %s", wDraft.Body.String())
	}

	wConfirmed := getRequest(engine, "/product-assets/"+strFromUint(confirmed.Id))
	if !strings.Contains(wConfirmed.Body.String(), `"status":"confirmed"`) {
		t.Errorf("confirmed response must ship status:\"confirmed\", got %s", wConfirmed.Body.String())
	}
}

func TestGetProductAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedProductAssetForAPI(t, 42, true, "owned-by-42")

	// Caller is uid=99; must collapse to 404 not leak existence.
	engine := buildProductAssetEngine(t, 99)
	w := getRequest(engine, "/product-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant should 404, got %d; body=%s", w.Code, w.Body.String())
	}
}

func TestGetProductAsset_UnknownID404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildProductAssetEngine(t, 42)
	w := getRequest(engine, "/product-assets/999999")
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown id should 404, got %d", w.Code)
	}
}

func TestGetProductAsset_BadID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildProductAssetEngine(t, 42)
	w := getRequest(engine, "/product-assets/not-a-number")
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad id should 400, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------
// ConfirmProductAsset
// ---------------------------------------------------------------------

func TestConfirmProductAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedProductAssetForAPI(t, 42, false /* draft */, "to-confirm")

	engine := buildProductAssetEngine(t, 42)
	w := postJSON(engine, "/product-assets/"+strFromUint(asset.Id)+"/confirm", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Re-read; should be confirmed now.
	got, err := product.Default().LoadByIDForOwner(asset.Id, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Confirmed {
		t.Errorf("Confirmed = false after confirm")
	}
}

func TestConfirmProductAsset_Idempotent(t *testing.T) {
	// Already-confirmed → handler MUST return ok-shaped (200);
	// the generic handler does a LoadByID-after-NotFound so
	// idempotent confirms succeed.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedProductAssetForAPI(t, 42, true /* already confirmed */, "already-on")

	engine := buildProductAssetEngine(t, 42)
	w := postJSON(engine, "/product-assets/"+strFromUint(asset.Id)+"/confirm", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("idempotent confirm should 200, got %d; body=%s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------
// DeleteProductAsset + RestoreProductAsset
// ---------------------------------------------------------------------

func TestDeleteProductAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedProductAssetForAPI(t, 42, true, "to-delete")

	engine := buildProductAssetEngine(t, 42)
	w := deleteRequest(engine, "/product-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Listing should no longer show the deleted row.
	wList := getRequest(engine, "/product-assets")
	if strings.Contains(wList.Body.String(), "to-delete") {
		t.Errorf("deleted product still appears in list: %s", wList.Body.String())
	}
}

func TestRestoreProductAsset_ClearsDeletedAt(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedProductAssetForAPI(t, 42, true, "to-restore")
	// Soft-delete first.
	if err := product.Default().SoftDelete(asset.Id, 42); err != nil {
		t.Fatal(err)
	}

	engine := buildProductAssetEngine(t, 42)
	w := postJSON(engine, "/product-assets/"+strFromUint(asset.Id)+"/restore", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("restore: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Restore resets Confirmed=false per the repo contract; row
	// should be visible in the list again.
	wList := getRequest(engine, "/product-assets")
	if !strings.Contains(wList.Body.String(), "to-restore") {
		t.Errorf("restored product should reappear in list: %s", wList.Body.String())
	}
}
