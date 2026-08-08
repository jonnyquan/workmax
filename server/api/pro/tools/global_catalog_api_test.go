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

func TestGlobalCatalogApiListModels(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	if err := db.Create(&model.GlobalModel{
		ModelID:      "model-a",
		MediaType:    model.MediaTypeVideo,
		ProviderType: model.ProviderTypeVertex,
		DisplayName:  "Model A",
		Status:       model.GlobalModelStatusEnabled,
		SortOrder:    10,
	}).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	if err := db.Create(&model.GlobalModel{
		ModelID:     "model-disabled",
		MediaType:   model.MediaTypeVideo,
		DisplayName: "Disabled",
	}).Error; err != nil {
		t.Fatalf("seed disabled model: %v", err)
	}
	if err := db.Model(&model.GlobalModel{}).
		Where("model_id = ?", "model-disabled").
		Update("status", model.GlobalModelStatusDisabled).Error; err != nil {
		t.Fatalf("disable model: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := GlobalCatalogApi{}
	r.GET("/global/models", api.ListModels)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/global/models?mediaType=video", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			List []model.GlobalModel `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data.List) != 1 || body.Data.List[0].ModelID != "model-a" {
		t.Fatalf("unexpected models response: %+v", body.Data.List)
	}
}

func TestGlobalCatalogApiListAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projectID := uint(7)
	if err := db.Create(&model.GlobalAsset{
		UID:           42,
		ProjectID:     &projectID,
		UUID:          "asset-uuid-1",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "reference_upload",
		SourceID:      1,
		SourceItemKey: "upload-1",
		URL:           "/uploads/references/uid/42/ref.png",
		MimeType:      "image/png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 42},
	})
	req := httptest.NewRequest(http.MethodGet, "/global/assets?projectId=7", nil)
	ctx.Request = req

	api := GlobalCatalogApi{}
	api.ListAssets(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data struct {
			List []map[string]any `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data.List) != 1 || body.Data.List[0]["url"] != "/uploads/references/uid/42/ref.png" {
		t.Fatalf("unexpected assets response: %+v", body.Data.List)
	}
	if _, ok := body.Data.List[0]["sourceTable"]; ok {
		t.Fatalf("sourceTable leaked in public asset DTO: %+v", body.Data.List[0])
	}
	if _, ok := body.Data.List[0]["uid"]; ok {
		t.Fatalf("uid leaked in public asset DTO: %+v", body.Data.List[0])
	}
}

func TestGlobalCatalogApiAuditAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	if err := db.Create(&model.GenerationObject{
		UID:         42,
		TaskID:      "task-missing-bridge",
		ToolID:      "image-generator",
		Provider:    "r2",
		Bucket:      "bucket",
		ObjectKey:   "images/task.png",
		AssetKind:   "image",
		ContentType: "image/png",
		PublicURL:   "/uploads/generations/uid/42/task.png",
		Status:      model.GenerationObjectStatusActive,
	}).Error; err != nil {
		t.Fatalf("seed generation object: %v", err)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/global/assets/audit", nil)

	api := GlobalCatalogApi{}
	api.AuditAssets(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data struct {
			MissingGenerationObjects int64 `json:"missingGenerationObjects"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.MissingGenerationObjects != 1 {
		t.Fatalf("missingGenerationObjects = %d, want 1", body.Data.MissingGenerationObjects)
	}
}
