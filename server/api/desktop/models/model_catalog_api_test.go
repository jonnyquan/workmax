package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/model"
	request "server/model/system/request"
	"server/utils/testutil"
)

// The catalog's contract in one sentence: every caller sees every model, and
// permissions — not visibility — encode entitlement. A locked model that
// disappears cannot be upsold.

func newCatalogEngine(t *testing.T, uid uint) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testutil.NewTestDB(t)
	api := NewModelCatalogApi(db)

	engine := gin.New()
	engine.GET("/models", func(c *gin.Context) {
		if uid != 0 {
			c.Set("claims", &request.CustomClaims{BaseClaims: request.BaseClaims{Id: uid}})
		}
		c.Next()
	}, api.ListModels)
	return engine, db
}

// seedConversationModels mirrors migrations/20260814_add_global_model_required_tier.sql.
func seedConversationModels(t *testing.T, db *gorm.DB) {
	t.Helper()
	rows := []model.GlobalModel{
		{
			ModelID: "work-pro", MediaType: model.MediaTypeText, DisplayName: "Work Pro",
			Status: model.GlobalModelStatusEnabled, SortOrder: 200,
			RequiredTier: model.MemberTierFree,
			Capabilities: model.JSONMap{"conversation": true, "default": true},
			Metadata:     model.JSONMap{model.GlobalModelMetadataDescription: "Balanced everyday model."},
		},
		{
			ModelID: "work-plus", MediaType: model.MediaTypeText, DisplayName: "Work Plus",
			Status: model.GlobalModelStatusEnabled, SortOrder: 100,
			RequiredTier: model.MemberTierPro,
			Capabilities: model.JSONMap{"conversation": true, "default": false},
			Metadata:     model.JSONMap{model.GlobalModelMetadataDescription: "Highest-capability model."},
		},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed model %s: %v", row.ModelID, err)
		}
	}
}

func seedCatalogUser(t *testing.T, db *gorm.DB, member int, endTime time.Time) uint {
	t.Helper()
	user := model.User{
		Email: "catalog@example.com", Nickname: "Catalog",
		Member: member, MemberEndTime: endTime,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return user.Id
}

func fetchCatalog(t *testing.T, engine *gin.Engine) catalogResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp catalogResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body %s)", err, recorder.Body.String())
	}
	return resp
}

func itemByID(t *testing.T, resp catalogResponse, modelID string) catalogItem {
	t.Helper()
	for _, item := range resp.Items {
		if item.ModelID == modelID {
			return item
		}
	}
	t.Fatalf("model %q missing from catalog %+v", modelID, resp.Items)
	return catalogItem{}
}

func hasUse(item catalogItem) bool {
	for _, permission := range item.Permissions {
		if permission == "use" {
			return true
		}
	}
	return false
}

func TestListModels_FreeUserSeesEveryModelButCannotUsePaidOne(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedConversationModels(t, db)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	resp := fetchCatalog(t, engine)

	if len(resp.Items) != 2 {
		t.Fatalf("items = %d, want both models visible: %+v", len(resp.Items), resp.Items)
	}
	if resp.Tier != model.MemberTierFree {
		t.Errorf("tier = %q, want free", resp.Tier)
	}
	if resp.TierExpiresAt != "" {
		t.Errorf("tierExpiresAt = %q, want empty for a free caller", resp.TierExpiresAt)
	}

	free := itemByID(t, resp, "work-pro")
	if !hasUse(free) {
		t.Errorf("work-pro must be usable on the free tier, got %+v", free.Permissions)
	}
	if !free.Default {
		t.Error("work-pro should be the preselected default")
	}
	if free.Description == "" || free.DisplayName != "Work Pro" {
		t.Errorf("free item is missing display metadata: %+v", free)
	}

	paid := itemByID(t, resp, "work-plus")
	if hasUse(paid) {
		t.Errorf("work-plus must be locked for a free caller, got %+v", paid.Permissions)
	}
	if paid.RequiredTier != model.MemberTierPro {
		t.Errorf("requiredTier = %q, want pro so the client can explain the lock", paid.RequiredTier)
	}
}

func TestListModels_PaidUserUnlocksPaidModel(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedConversationModels(t, db)
	expiry := time.Now().Add(30 * 24 * time.Hour)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_PRO, expiry)

	resp := fetchCatalog(t, engine)

	if resp.Tier != model.MemberTierPro {
		t.Fatalf("tier = %q, want pro", resp.Tier)
	}
	if resp.TierExpiresAt == "" {
		t.Error("tierExpiresAt must be set for an active paid membership")
	}
	if got := itemByID(t, resp, "work-plus"); !hasUse(got) {
		t.Errorf("work-plus must be usable for a paid caller, got %+v", got.Permissions)
	}
	if got := itemByID(t, resp, "work-pro"); !hasUse(got) {
		t.Errorf("work-pro must stay usable for a paid caller, got %+v", got.Permissions)
	}
}

// A lapsed membership is handled exactly like an unpaid one — the catalog must
// not keep a model unlocked past the window the payment bought.
func TestListModels_ExpiredMemberIsTreatedAsUnpaid(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedConversationModels(t, db)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(-time.Hour))

	resp := fetchCatalog(t, engine)

	if resp.Tier != model.MemberTierFree {
		t.Fatalf("tier = %q, want free once the paid window has passed", resp.Tier)
	}
	if resp.TierExpiresAt != "" {
		t.Errorf("tierExpiresAt = %q, want empty rather than a date in the past", resp.TierExpiresAt)
	}
	if got := itemByID(t, resp, "work-plus"); hasUse(got) {
		t.Errorf("work-plus must lock again after expiry, got %+v", got.Permissions)
	}
}

// A free-plan member (member=1) is not a paying customer. This is the level
// the retired second enum used to read as a paid "Creator" tier.
func TestListModels_FreePlanMemberDoesNotUnlockPaidModel(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedConversationModels(t, db)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_FREE, time.Now().Add(30*24*time.Hour))

	resp := fetchCatalog(t, engine)

	if resp.Tier != model.MemberTierFree {
		t.Fatalf("tier = %q, want free for a free-plan member", resp.Tier)
	}
	if got := itemByID(t, resp, "work-plus"); hasUse(got) {
		t.Errorf("work-plus must stay locked for a free-plan member, got %+v", got.Permissions)
	}
}

// permissions must serialize as [] rather than null: clients index into it.
func TestListModels_LockedPermissionsSerializeAsEmptyArray(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedConversationModels(t, db)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models", nil))

	body := recorder.Body.String()
	for _, want := range []string{`"permissions":[]`, `"permissions":["use"]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body must carry %s, got %s", want, body)
		}
	}
}

// A disabled row is an ops kill-switch: it must leave the catalog entirely,
// not merely lose its permissions.
func TestListModels_DisabledRowIsNotServed(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedConversationModels(t, db)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))
	if err := db.Model(&model.GlobalModel{}).
		Where("model_id = ? AND media_type = ?", "work-plus", model.MediaTypeText).
		Update("status", model.GlobalModelStatusDisabled).Error; err != nil {
		t.Fatalf("disable model: %v", err)
	}

	resp := fetchCatalog(t, engine)

	for _, item := range resp.Items {
		if item.ModelID == "work-plus" {
			t.Fatalf("disabled model still served: %+v", item)
		}
	}
}

// Image/video rows share the table. They are a different product surface and
// must never leak into the conversation-model picker.
func TestListModels_IgnoresNonConversationMediaTypes(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedConversationModels(t, db)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_NONE, time.Time{})
	if err := db.Create(&model.GlobalModel{
		ModelID: "some-image-model", MediaType: model.MediaTypeImage,
		DisplayName: "Image Model", Status: model.GlobalModelStatusEnabled,
	}).Error; err != nil {
		t.Fatalf("seed image model: %v", err)
	}

	resp := fetchCatalog(t, engine)

	for _, item := range resp.Items {
		if item.ModelID == "some-image-model" {
			t.Fatalf("image model leaked into the conversation catalog: %+v", item)
		}
	}
}

func TestListModels_RejectsUnauthenticatedCaller(t *testing.T) {
	engine, _ := newCatalogEngine(t, 0)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

// The response must never become a credential channel. Provider identity is in
// the row (provider_type) but has no business on the wire.
func TestListModels_NeverLeaksProviderOrCredentialFields(t *testing.T) {
	engine, db := newCatalogEngine(t, 1)
	seedCatalogUser(t, db, model.MEMBER_SUBSCRIPTION_PRO, time.Now().Add(time.Hour))
	if err := db.Create(&model.GlobalModel{
		ModelID: "work-pro", MediaType: model.MediaTypeText, DisplayName: "Work Pro",
		ProviderType: "anthropic", Status: model.GlobalModelStatusEnabled,
		RequiredTier: model.MemberTierFree,
		Metadata:     model.JSONMap{model.GlobalModelMetadataDescription: "Balanced everyday model."},
	}).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/models", nil))

	body := recorder.Body.String()
	for _, forbidden := range []string{"anthropic", "providerType", "apiKey", "endpoint", "baseUrl"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("catalog leaked %q: %s", forbidden, body)
		}
	}
}
