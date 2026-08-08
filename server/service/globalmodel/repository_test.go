package globalmodel

import (
	"testing"

	"server/model"
	"server/utils/testutil"
)

func TestRepositoryUpsertAndLoadEnabled(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)

	created, err := repo.Upsert(UpsertInput{
		ModelID:       "veo-3.1-fast-generate-001",
		MediaType:     model.MediaTypeVideo,
		ProviderType:  model.ProviderTypeVertex,
		DisplayName:   "Veo 3.1 Fast",
		PricingStatus: "official",
		SortOrder:     100,
		Capabilities: model.JSONMap{
			"durations": []int{4, 6, 8},
		},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if created.Id == 0 {
		t.Fatal("created model id is zero")
	}

	loaded, err := repo.LoadEnabledByModelID("veo-3.1-fast-generate-001", model.MediaTypeVideo)
	if err != nil {
		t.Fatalf("LoadEnabledByModelID: %v", err)
	}
	if loaded.ProviderType != model.ProviderTypeVertex {
		t.Fatalf("providerType = %q", loaded.ProviderType)
	}
	if loaded.DisplayName != "Veo 3.1 Fast" {
		t.Fatalf("displayName = %q", loaded.DisplayName)
	}
}

func TestRepositoryUpsertIsIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)

	if _, err := repo.Upsert(UpsertInput{
		ModelID:      "gemini-3.1-flash-image-preview",
		MediaType:    model.MediaTypeImage,
		ProviderType: model.ProviderTypeGemini,
		DisplayName:  "Gemini Flash Image",
		SortOrder:    10,
	}); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if _, err := repo.Upsert(UpsertInput{
		ModelID:      "gemini-3.1-flash-image-preview",
		MediaType:    model.MediaTypeImage,
		ProviderType: model.ProviderTypeVertex,
		DisplayName:  "Gemini Flash Image Preview",
		SortOrder:    20,
	}); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	var count int64
	if err := db.Model(&model.GlobalModel{}).
		Where("model_id = ? AND media_type = ?", "gemini-3.1-flash-image-preview", model.MediaTypeImage).
		Count(&count).Error; err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("model row count = %d, want 1", count)
	}

	rows, err := repo.ListEnabled(model.MediaTypeImage)
	if err != nil {
		t.Fatalf("ListEnabled: %v", err)
	}
	if len(rows) != 1 || rows[0].ProviderType != model.ProviderTypeVertex || rows[0].SortOrder != 20 {
		t.Fatalf("updated row not returned: %+v", rows)
	}
}
