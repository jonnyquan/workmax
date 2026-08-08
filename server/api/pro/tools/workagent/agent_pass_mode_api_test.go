package workagent

import (
	"net/http"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func buildPassModeEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.POST("/threads/:id/pass-mode", withClaims(uid), api.SubmitPassMode)
	return r
}

func TestSubmitPassMode_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	artifact, file := seedVariationDraftArtifact(t, db, 42, uintFromStr(t, threadIDStr), workagentModel.ArtifactStatusDraft)
	engine := buildPassModeEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/pass-mode", map[string]any{
		"mode":                   "finalize",
		"source":                 "variation_picker",
		"selected_variation_id":  "balanced",
		"selected_artifact_id":   "artifact-" + uintToStr(artifact.Id),
		"selected_file_id":       "file-" + uintToStr(file.Id),
		"design_system_basename": "modern-minimal",
		"asset_contract":         "brand locked",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"pass_mode":"finalize"`) {
		t.Fatalf("body should report pass_mode finalize, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"selected_variation_id":"balanced"`) {
		t.Fatalf("body should echo selected variation, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"selected_artifact_id":"artifact-`+uintToStr(artifact.Id)+`"`) {
		t.Fatalf("body should echo selected artifact, got %q", w.Body.String())
	}

	repo := workagentService.DefaultMessageRepository()
	msg, err := repo.FindMostRecentByMetadataKind(uintFromStr(t, threadIDStr), "workagent_pass_mode")
	if err != nil || msg == nil {
		t.Fatalf("expected pass mode marker, got msg=%v err=%v", msg, err)
	}
	if !strings.Contains(msg.Metadata, `"mode":"finalize"`) {
		t.Fatalf("metadata should carry finalize mode, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"source":"variation_picker"`) {
		t.Fatalf("metadata should carry variation source, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"selected_variation_id":"balanced"`) {
		t.Fatalf("metadata should carry selected variation, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"selected_artifact_id":"artifact-`+uintToStr(artifact.Id)+`"`) {
		t.Fatalf("metadata should carry selected artifact, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"selected_file_id":"file-`+uintToStr(file.Id)+`"`) {
		t.Fatalf("metadata should carry selected file, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"design_system_basename":"modern-minimal"`) {
		t.Fatalf("metadata should carry selected design system, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"asset_contract":"brand locked"`) {
		t.Fatalf("metadata should carry selected asset contract, got %q", msg.Metadata)
	}
}

func TestSubmitPassMode_AcceptsVariationFinalizeWithDraftFileOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	_, file := seedVariationDraftArtifact(t, db, 42, uintFromStr(t, threadIDStr), workagentModel.ArtifactStatusNeedsReview)
	engine := buildPassModeEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/pass-mode", map[string]any{
		"mode":                  "finalize",
		"source":                "variation_picker",
		"selected_variation_id": "bold",
		"selected_file_id":      uintToStr(file.Id),
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"selected_file_id":"`+uintToStr(file.Id)+`"`) {
		t.Fatalf("body should echo selected file, got %q", w.Body.String())
	}
}

func TestSubmitPassMode_RejectsVariationFinalizeWithFinalArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	artifact, file := seedVariationDraftArtifact(t, db, 42, uintFromStr(t, threadIDStr), workagentModel.ArtifactStatusFinal)
	engine := buildPassModeEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/pass-mode", map[string]any{
		"mode":                  "finalize",
		"source":                "variation_picker",
		"selected_variation_id": "balanced",
		"selected_artifact_id":  "artifact-" + uintToStr(artifact.Id),
		"selected_file_id":      uintToStr(file.Id),
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "must still be a draft") {
		t.Fatalf("body should explain draft status requirement, got %q", w.Body.String())
	}
}

func TestSubmitPassMode_RejectsVariationFinalizeWithCrossThreadArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	otherThreadIDStr := seedThread(t, db, 42)
	artifact, _ := seedVariationDraftArtifact(t, db, 42, uintFromStr(t, otherThreadIDStr), workagentModel.ArtifactStatusDraft)
	engine := buildPassModeEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/pass-mode", map[string]any{
		"mode":                  "finalize",
		"source":                "variation_picker",
		"selected_variation_id": "bold",
		"selected_artifact_id":  "artifact-" + uintToStr(artifact.Id),
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "selected artifact was not found") {
		t.Fatalf("body should explain missing selected artifact, got %q", w.Body.String())
	}
}

func TestSubmitPassMode_VariationFinalizeRequiresDraftArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildPassModeEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/pass-mode", map[string]any{
		"mode":                  "finalize",
		"source":                "variation_picker",
		"selected_variation_id": "bold",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "requires a draft artifact") {
		t.Fatalf("body should explain draft artifact requirement, got %q", w.Body.String())
	}
}

func TestSubmitPassMode_CrossTenantReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 99)
	engine := buildPassModeEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/pass-mode", map[string]any{
		"mode": "finalize",
	})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestSubmitPassMode_RejectsUnknownMode(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildPassModeEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/pass-mode", map[string]any{
		"mode": "shipping",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func seedVariationDraftArtifact(t *testing.T, db *gorm.DB, uid int, threadID uint, status string) (*workagentModel.ArtifactRegistry, *workagentModel.ThreadFile) {
	t.Helper()
	file := workagentModel.ThreadFile{
		UID:          uid,
		ThreadID:     threadID,
		FileName:     "balanced-draft.html",
		DisplayName:  "Balanced draft",
		FilePath:     "outputs/balanced-draft.html",
		FileSource:   workagentModel.FileSourceOutput,
		MimeType:     "text/html",
		ExistsOnDisk: true,
		Description:  `{"kind":"workagent_variation_draft","variation_id":"balanced"}`,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed variation draft file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed variation draft artifact: %v", err)
	}
	if status != "" && status != artifact.Status {
		updated, err := workagentService.NewArtifactRegistryRepository(db).UpdateLifecycle(uid, threadID, artifact.Id, status, workagentModel.ArtifactReviewNone)
		if err != nil {
			t.Fatalf("set variation draft artifact status: %v", err)
		}
		artifact = updated
	}
	return artifact, &file
}
