package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"server/model"
	systemReq "server/model/system/request"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

// global_catalog_api_get_resolve_test.go — covers the two read
// endpoints added 2026-05-16 alongside the existing /global/assets
// list + /global/assets/audit pair:
//
//   GET /global/assets/:id            → GetAsset
//   GET /global/assets/by-source?kind=...&id=...  → ResolveAssetBySource
//
// Both share the same wire-shape and the same 404-collapses-all-
// failures policy. Tests pin: happy path, cross-tenant denial,
// unknown source kind, malformed id, and unauth.

func seedGlobalAssetRow(t *testing.T, uid int, projectID *uint, sourceTable string, sourceID uint64, uuid, url string) model.GlobalAsset {
	t.Helper()
	return model.GlobalAsset{
		UID:           uid,
		ProjectID:     projectID,
		UUID:          uuid,
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTable,
		SourceID:      sourceID,
		SourceItemKey: uuid,
		URL:           url,
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
}

func newCallerCtx(t *testing.T, uid uint, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: uid},
	})
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return ctx, recorder
}

// ── GetAsset ────────────────────────────────────────────────────

func TestGlobalCatalogApi_GetAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	asset := seedGlobalAssetRow(t, 42, nil, "reference_upload", 1, "asset-a", "/uploads/a.png")
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, rec := newCallerCtx(t, 42, "/global/assets/"+itoa(int(asset.Id)))
	ctx.Params = gin.Params{{Key: "id", Value: itoa(int(asset.Id))}}

	api := &GlobalCatalogApi{}
	api.GetAsset(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data["url"] != "/uploads/a.png" {
		t.Errorf("url mismatch: %+v", body.Data)
	}
	if _, leaked := body.Data["sourceTable"]; leaked {
		t.Errorf("sourceTable must not leak in DTO: %+v", body.Data)
	}
}

func TestGlobalCatalogApi_GetAsset_CrossTenantReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	asset := seedGlobalAssetRow(t, 42, nil, "reference_upload", 2, "asset-b", "/uploads/b.png")
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, rec := newCallerCtx(t, 99, "/global/assets/"+itoa(int(asset.Id)))
	ctx.Params = gin.Params{{Key: "id", Value: itoa(int(asset.Id))}}

	api := &GlobalCatalogApi{}
	api.GetAsset(ctx)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGlobalCatalogApi_GetAsset_MalformedIDReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	ctx, rec := newCallerCtx(t, 42, "/global/assets/abc")
	ctx.Params = gin.Params{{Key: "id", Value: "abc"}}

	api := &GlobalCatalogApi{}
	api.GetAsset(ctx)
	if rec.Code != http.StatusNotFound {
		t.Errorf("malformed id must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGlobalCatalogApi_GetAsset_UnauthReturns401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/global/assets/1", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "1"}}

	api := &GlobalCatalogApi{}
	api.GetAsset(ctx)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no claims must 401; got %d", rec.Code)
	}
}

// ── ResolveAssetBySource ───────────────────────────────────────

func TestGlobalCatalogApi_ResolveBySource_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	asset := seedGlobalAssetRow(t, 42, nil, "w_workagent_thread_file", 555, "asset-resolve", "/uploads/r.png")
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, rec := newCallerCtx(t, 42, "/global/assets/by-source?kind=workagent_file&id=555")

	api := &GlobalCatalogApi{}
	api.ResolveAssetBySource(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if uint(body.Data["id"].(float64)) != asset.Id {
		t.Errorf("resolved id mismatch: got %v, want %d", body.Data["id"], asset.Id)
	}
}

func TestGlobalCatalogApi_ResolveBySource_UnknownKindReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	// Even with a row that matches a real source_id, an unknown
	// public kind name MUST 404 — the FE can't poke at internal
	// source_table values.
	asset := seedGlobalAssetRow(t, 42, nil, "w_workagent_thread_file", 666, "asset-unknown-kind", "/u/x.png")
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, rec := newCallerCtx(t, 42, "/global/assets/by-source?kind=mystery&id=666")
	api := &GlobalCatalogApi{}
	api.ResolveAssetBySource(ctx)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown kind must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGlobalCatalogApi_ResolveBySource_MissingParamsReturns400(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)

	// missing kind
	ctx, rec := newCallerCtx(t, 42, "/global/assets/by-source?id=1")
	api := &GlobalCatalogApi{}
	api.ResolveAssetBySource(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing kind must 400; got %d", rec.Code)
	}

	// missing id
	ctx2, rec2 := newCallerCtx(t, 42, "/global/assets/by-source?kind=workagent_file")
	api2 := &GlobalCatalogApi{}
	api2.ResolveAssetBySource(ctx2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("missing id must 400; got %d", rec2.Code)
	}
}

func TestGlobalCatalogApi_ResolveBySource_CrossTenantReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	asset := seedGlobalAssetRow(t, 42, nil, "w_generation_object", 888, "asset-cross", "/u/c.png")
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, rec := newCallerCtx(t, 99, "/global/assets/by-source?kind=generation_object&id=888")
	api := &GlobalCatalogApi{}
	api.ResolveAssetBySource(ctx)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant must 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

// itoa — small helper that keeps strconv out of the test file's
// import list. Test ids are small ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
