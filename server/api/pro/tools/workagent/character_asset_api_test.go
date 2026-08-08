package workagent

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"server/model"
	"server/service/character"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildCharacterAssetEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/character-assets", withClaims(uid), api.ListCharacterAssets)
	r.GET("/character-assets/:id", withClaims(uid), api.GetCharacterAsset)
	r.POST("/character-assets/:id/confirm", withClaims(uid), api.ConfirmCharacterAsset)
	r.DELETE("/character-assets/:id", withClaims(uid), api.DeleteCharacterAsset)
	return r
}

// seedCharacterAssetForAPI seeds a *model.Character row into
// w_global_character for the API test suite. withImage=true sets
// AvatarImageURL so the hasReference signal lights up. Callers
// needing a soft-deleted row should call character.Default()
// .SoftDelete after this returns.
func seedCharacterAssetForAPI(t *testing.T, uid int, confirmed bool, name string, withImage bool) *model.Character {
	t.Helper()
	c := &model.Character{
		UID:        uid,
		Name:       name,
		Slug:       name + "-slug",
		RoleType:   model.CharacterRoleSupporting,
		Status:     model.CharacterStatusActive,
		Confirmed:  confirmed,
		SourceKind: model.CharacterSourceManual,
		Lang:       "en",
	}
	if withImage {
		c.AvatarImageURL = "uid/" + name + "/canonical.png"
	}
	if err := character.Default().Create(c); err != nil {
		t.Fatalf("seed character: %v", err)
	}
	return c
}

func TestListCharacterAssets_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedCharacterAssetForAPI(t, 42, true, "lin", true)
	seedCharacterAssetForAPI(t, 42, false, "draft", false)

	engine := buildCharacterAssetEngine(t, 42)
	w := getRequest(engine, "/character-assets")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Items []characterAssetSummary `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Items) != 2 {
		t.Errorf("got %d items, want 2", len(resp.Data.Items))
	}
	// Verify HasReference signal tracks the canonical_image_path
	// presence — the library card relies on this hint to choose
	// between rendering the avatar vs a description-only fallback.
	withImg, withoutImg := 0, 0
	for _, it := range resp.Data.Items {
		if it.HasReference {
			withImg++
		} else {
			withoutImg++
		}
	}
	if withImg != 1 || withoutImg != 1 {
		t.Errorf("hasReference distribution wrong: with=%d without=%d, want 1/1", withImg, withoutImg)
	}
}

func TestListCharacterAssets_CrossTenantIsolation(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedCharacterAssetForAPI(t, 42, true, "lin", true)

	engine := buildCharacterAssetEngine(t, 99)
	w := getRequest(engine, "/character-assets")
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Errorf("uid=99 should see empty list, got %q", w.Body.String())
	}
}

func TestListCharacterAssets_Unauthorized(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildCharacterAssetEngine(t, 0)
	w := getRequest(engine, "/character-assets")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// Wire-shape regression — characterLibraryAsset.MarshalJSON
// projects raw Status int8 onto the enum string so the drawer
// gate works. See brand_asset_api_test.go for the longer
// rationale.
func TestGetCharacterAsset_StatusProjectedToEnumString(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	draft := seedCharacterAssetForAPI(t, 42, false, "lin-draft", false)
	confirmed := seedCharacterAssetForAPI(t, 42, true, "lin-confirmed", false)

	engine := buildCharacterAssetEngine(t, 42)

	wDraft := getRequest(engine, "/character-assets/"+strFromUint(draft.Id))
	if !strings.Contains(wDraft.Body.String(), `"status":"draft"`) {
		t.Errorf("draft must ship status:\"draft\" string enum; got %s", wDraft.Body.String())
	}
	if strings.Contains(wDraft.Body.String(), `"status":0`) || strings.Contains(wDraft.Body.String(), `"status":1`) {
		t.Errorf("draft leaking raw int status: %s", wDraft.Body.String())
	}

	wConfirmed := getRequest(engine, "/character-assets/"+strFromUint(confirmed.Id))
	if !strings.Contains(wConfirmed.Body.String(), `"status":"confirmed"`) {
		t.Errorf("confirmed must ship status:\"confirmed\"; got %s", wConfirmed.Body.String())
	}
}

func TestGetCharacterAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedCharacterAssetForAPI(t, 42, true, "lin", true)

	engine := buildCharacterAssetEngine(t, 42)
	w := getRequest(engine, "/character-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name":"lin"`) {
		t.Errorf("response missing character name: %s", w.Body.String())
	}
}

func TestGetCharacterAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedCharacterAssetForAPI(t, 42, true, "lin", true)

	engine := buildCharacterAssetEngine(t, 99)
	w := getRequest(engine, "/character-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant should 404, got %d", w.Code)
	}
}

func TestGetCharacterAsset_BadID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildCharacterAssetEngine(t, 42)
	w := getRequest(engine, "/character-assets/abc")
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-numeric id should 400, got %d", w.Code)
	}
}

func TestConfirmCharacterAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedCharacterAssetForAPI(t, 42, false, "lin", true)

	engine := buildCharacterAssetEngine(t, 42)
	w := postJSON(engine, "/character-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
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

func TestConfirmCharacterAsset_Idempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedCharacterAssetForAPI(t, 42, true, "lin", true)

	engine := buildCharacterAssetEngine(t, 42)
	w := postJSON(engine, "/character-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
	if w.Code != http.StatusOK {
		t.Errorf("second confirm should still 200, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestConfirmCharacterAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedCharacterAssetForAPI(t, 42, false, "lin", true)

	engine := buildCharacterAssetEngine(t, 99)
	w := postJSON(engine, "/character-assets/"+strFromUint(asset.Id)+"/confirm", map[string]any{})
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant should 404, got %d", w.Code)
	}
}

func TestDeleteCharacterAsset_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedCharacterAssetForAPI(t, 42, true, "lin", true)

	engine := buildCharacterAssetEngine(t, 42)
	w := deleteRequest(engine, "/character-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	w = getRequest(engine, "/character-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("deleted character should 404 on subsequent get, got %d", w.Code)
	}
}

func TestDeleteCharacterAsset_CrossTenant404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	asset := seedCharacterAssetForAPI(t, 42, true, "lin", true)

	engine := buildCharacterAssetEngine(t, 99)
	w := deleteRequest(engine, "/character-assets/"+strFromUint(asset.Id))
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-tenant delete should 404, got %d", w.Code)
	}
}
