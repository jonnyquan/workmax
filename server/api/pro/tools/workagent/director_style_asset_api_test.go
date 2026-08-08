package workagent

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"server/globals"
	"server/model"
	"server/service/director_style"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildDirectorStyleAssetEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/director-style-assets", withClaims(uid), api.ListDirectorStyleAssets)
	r.GET("/director-style-assets/:id", withClaims(uid), api.GetDirectorStyleAsset)
	r.POST("/director-style-assets/:id/confirm", withClaims(uid), api.ConfirmDirectorStyleAsset)
	r.DELETE("/director-style-assets/:id", withClaims(uid), api.DeleteDirectorStyleAsset)
	return r
}

// seedDirectorStyleForAPI seeds a *model.DirectorStyle row into
// w_global_director_style for the API test suite. withRefs=true
// also inserts one row into w_global_director_style_reference so
// the descriptor's hasReferences signal lights up via the bulk
// JOIN lookup. Callers needing a soft-deleted row should call
// director_style.Default().SoftDelete after this returns.
func seedDirectorStyleForAPI(t *testing.T, uid int, confirmed bool, name, genre string, withRefs bool) *model.DirectorStyle {
	t.Helper()
	d := &model.DirectorStyle{
		UID:        uid,
		Name:       name,
		Slug:       name + "-slug",
		Genre:      genre,
		Status:     model.DirectorStyleStatusActive,
		Confirmed:  confirmed,
		SourceKind: model.DirectorStyleSourceExtracted,
		Lang:       "en",
	}
	if err := director_style.Default().Create(d); err != nil {
		t.Fatalf("seed director_style: %v", err)
	}
	if withRefs {
		ref := &model.DirectorStyleReference{
			DirectorStyleID: uint64(d.Id),
			UID:             uid,
			ImageURL:        "uid/" + name + "/reel.mp4",
			ReferenceType:   model.DirectorStyleReferenceTypeReelClip,
		}
		if err := globals.GraDBs["system"].Create(ref).Error; err != nil {
			t.Fatalf("seed director_style reference: %v", err)
		}
	}
	return d
}

func TestListDirectorStyleAssets_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedDirectorStyleForAPI(t, 42, true, "wes", "comedy-drama", true)
	seedDirectorStyleForAPI(t, 42, false, "kubrick", "noir", false)

	engine := buildDirectorStyleAssetEngine(t, 42)
	w := getRequest(engine, "/director-style-assets")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Items []directorStyleAssetSummary `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Items) != 2 {
		t.Errorf("got %d items, want 2", len(resp.Data.Items))
	}
	// hasReferences signal restored — the seed's withRefs=true
	// inserts a w_global_director_style_reference row, and the
	// descriptor's List path enriches each row's
	// HasReferencesCached via a bulk lookup before Summarise.
	withRefs, withoutRefs := 0, 0
	for _, it := range resp.Data.Items {
		if it.HasReferences {
			withRefs++
		} else {
			withoutRefs++
		}
	}
	if withRefs != 1 || withoutRefs != 1 {
		t.Errorf("hasReferences distribution: with=%d without=%d, want 1/1",
			withRefs, withoutRefs)
	}
}

// TestListDirectorStyleAssets_GenreFilter — `?genre=noir` hits the
// (genre) index and surfaces only matching rows.
func TestListDirectorStyleAssets_GenreFilter(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedDirectorStyleForAPI(t, 42, true, "fincher", "noir", true)
	seedDirectorStyleForAPI(t, 42, true, "kubrick", "noir", false)
	seedDirectorStyleForAPI(t, 42, true, "wes", "comedy-drama", true)

	engine := buildDirectorStyleAssetEngine(t, 42)
	w := getRequest(engine, "/director-style-assets?genre=noir")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Items      []directorStyleAssetSummary `json:"items"`
			Pagination map[string]any              `json:"pagination"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Items) != 2 {
		t.Errorf("got %d items, want 2 (noir only)", len(resp.Data.Items))
	}
	for _, it := range resp.Data.Items {
		if it.Genre != "noir" {
			t.Errorf("filter leak: genre=%q", it.Genre)
		}
	}
	// Pagination metadata echoes the genre filter so clients can
	// distinguish list vs filter mode without parsing the URL.
	if resp.Data.Pagination["genre"] != "noir" {
		t.Errorf("pagination should echo genre filter, got %v", resp.Data.Pagination["genre"])
	}
}

func TestListDirectorStyleAssets_CrossTenantIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedDirectorStyleForAPI(t, 42, true, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 99)
	w := getRequest(engine, "/director-style-assets")
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("uid=99 should see empty list, got %q", w.Body.String())
	}
}

func TestListDirectorStyleAssets_Unauthorized(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildDirectorStyleAssetEngine(t, 0)
	w := getRequest(engine, "/director-style-assets")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// Wire-shape regression — directorStyleLibraryAsset.MarshalJSON
// projects raw Status int8 onto the enum string so the drawer
// gate works. See brand_asset_api_test.go for the longer
// rationale.
func TestGetDirectorStyleAsset_StatusProjectedToEnumString(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	draft := seedDirectorStyleForAPI(t, 42, false, "wes-draft", "comedy", false)
	confirmed := seedDirectorStyleForAPI(t, 42, true, "wes-confirmed", "comedy", false)

	engine := buildDirectorStyleAssetEngine(t, 42)

	wDraft := getRequest(engine, "/director-style-assets/"+strFromUint(draft.Id))
	if !strings.Contains(wDraft.Body.String(), `"status":"draft"`) {
		t.Errorf("draft must ship status:\"draft\" string enum; got %s", wDraft.Body.String())
	}
	if strings.Contains(wDraft.Body.String(), `"status":0`) || strings.Contains(wDraft.Body.String(), `"status":1`) {
		t.Errorf("draft leaking raw int status: %s", wDraft.Body.String())
	}

	wConfirmed := getRequest(engine, "/director-style-assets/"+strFromUint(confirmed.Id))
	if !strings.Contains(wConfirmed.Body.String(), `"status":"confirmed"`) {
		t.Errorf("confirmed must ship status:\"confirmed\"; got %s", wConfirmed.Body.String())
	}
}

func TestGetDirectorStyleAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedDirectorStyleForAPI(t, 42, true, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 42)
	w := getRequest(engine, "/director-style-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name":"wes"`) {
		t.Errorf("response missing name: %s", w.Body.String())
	}
}

func TestGetDirectorStyleAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedDirectorStyleForAPI(t, 42, true, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 99)
	w := getRequest(engine, "/director-style-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant should 404, got %d", w.Code)
	}
}

func TestGetDirectorStyleAsset_BadID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildDirectorStyleAssetEngine(t, 42)
	w := getRequest(engine, "/director-style-assets/abc")
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id should 400, got %d", w.Code)
	}
}

func TestConfirmDirectorStyleAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedDirectorStyleForAPI(t, 42, false, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 42)
	w := postJSON(engine, "/director-style-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
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

func TestConfirmDirectorStyleAsset_Idempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedDirectorStyleForAPI(t, 42, true, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 42)
	w := postJSON(engine, "/director-style-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
	if w.Code != http.StatusOK {
		t.Errorf("second confirm should still 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestConfirmDirectorStyleAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedDirectorStyleForAPI(t, 42, false, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 99)
	w := postJSON(engine, "/director-style-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant should 404, got %d", w.Code)
	}
}

func TestDeleteDirectorStyleAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedDirectorStyleForAPI(t, 42, true, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 42)
	w := deleteRequest(engine, "/director-style-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	w = getRequest(engine, "/director-style-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("deleted style should 404 on subsequent get, got %d", w.Code)
	}
}

func TestDeleteDirectorStyleAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedDirectorStyleForAPI(t, 42, true, "wes", "comedy", true)

	engine := buildDirectorStyleAssetEngine(t, 99)
	w := deleteRequest(engine, "/director-style-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete should 404, got %d", w.Code)
	}
}
