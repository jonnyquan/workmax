package tools

import (
	"context"
	"testing"

	"server/model"
	"server/service/globalmodel"
	"server/utils/testutil"
)

func TestBuiltinGlobalModelInputsIncludeImageAndVideoModels(t *testing.T) {
	inputs := BuiltinGlobalModelInputs()
	if len(inputs) == 0 {
		t.Fatal("expected builtin global model inputs")
	}

	var sawVideo bool
	var sawImage bool
	for _, input := range inputs {
		switch input.ModelID {
		case model.VEO_31_FAST:
			sawVideo = input.MediaType == model.MediaTypeVideo && input.ProviderType != ""
		case model.NANO_BANANA_2:
			sawImage = input.MediaType == model.MediaTypeImage && input.DisplayName != ""
		}
	}
	if !sawVideo {
		t.Fatal("missing veo video model input")
	}
	if !sawImage {
		t.Fatal("missing nano banana image model input")
	}
}

func TestSyncBuiltinGlobalModelsUpsertsCatalogRows(t *testing.T) {
	db := testutil.NewTestDB(t)

	if err := SyncBuiltinGlobalModels(context.Background(), db); err != nil {
		t.Fatalf("SyncBuiltinGlobalModels: %v", err)
	}
	if err := SyncBuiltinGlobalModels(context.Background(), db); err != nil {
		t.Fatalf("second SyncBuiltinGlobalModels: %v", err)
	}

	repo := globalmodel.NewRepository(db)
	video, err := repo.LoadEnabledByModelID(model.VEO_31_FAST, model.MediaTypeVideo)
	if err != nil {
		t.Fatalf("load synced video model: %v", err)
	}
	if video.DisplayName != "Veo 3.1 Fast" {
		t.Fatalf("video displayName = %q", video.DisplayName)
	}

	var count int64
	if err := db.Model(&model.GlobalModel{}).
		Where("model_id = ? AND media_type = ?", model.VEO_31_FAST, model.MediaTypeVideo).
		Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("model row count = %d, want 1", count)
	}
}
