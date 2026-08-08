package storage

import (
	"server/config"
	"testing"
)

func TestBuildPublicObjectURLPrefersCDN(t *testing.T) {
	cfg := ObjectStoreConfig{
		Provider: "r2",
		Bucket:   "workmax-generations",
		CDNUrl:   "https://cdn.workmax.app",
	}

	got := BuildPublicObjectURL(cfg, "generations/2026/03/15/test.png")
	want := "https://cdn.workmax.app/generations/2026/03/15/test.png"
	if got != want {
		t.Fatalf("BuildPublicObjectURL() = %q, want %q", got, want)
	}
}

func TestNewObjectStoreFromGeneratorConfigBuildsR2Store(t *testing.T) {
	store, ok, err := NewObjectStoreFromGeneratorConfig(config.StorageConfig{
		Type: "r2",
		R2: config.R2Storage{
			Endpoint:          "https://example.r2.cloudflarestorage.com",
			Region:            "auto",
			Bucket:            "workmax-generations",
			PathPrefix:        "generations",
			ForcePathStyle:    true,
			UsePresignedURL:   true,
			PresignTTLSeconds: 7200,
		},
	})
	if err != nil {
		t.Fatalf("NewObjectStoreFromGeneratorConfig() error = %v", err)
	}
	if !ok {
		t.Fatalf("expected object store config to be supported")
	}
	if store == nil {
		t.Fatalf("expected store to be initialized")
	}
	if store.Provider() != "r2" {
		t.Fatalf("expected provider r2, got %q", store.Provider())
	}
}

func TestObjectStoreConfigFromStorageIncludesPresignOptions(t *testing.T) {
	cfg, ok := objectStoreConfigFromStorage(config.StorageConfig{
		Type: "r2",
		R2: config.R2Storage{
			Endpoint:          "https://example.r2.cloudflarestorage.com",
			Region:            "auto",
			Bucket:            "workmax-generations",
			UsePresignedURL:   true,
			PresignTTLSeconds: 7200,
		},
	})
	if !ok {
		t.Fatalf("expected object store config to be supported")
	}
	if !cfg.UsePresignedURL {
		t.Fatalf("expected UsePresignedURL to be true")
	}
	if cfg.PresignTTLSeconds != 7200 {
		t.Fatalf("expected PresignTTLSeconds to be 7200, got %d", cfg.PresignTTLSeconds)
	}
}

func TestObjectStoreConfigFromStorageIncludesMultipartOptions(t *testing.T) {
	cfg, ok := objectStoreConfigFromStorage(config.StorageConfig{
		Type: "r2",
		R2: config.R2Storage{
			Endpoint:             "https://example.r2.cloudflarestorage.com",
			Region:               "auto",
			Bucket:               "workmax-generations",
			MultipartThresholdMB: 96,
			MultipartPartSizeMB:  24,
		},
	})
	if !ok {
		t.Fatalf("expected object store config to be supported")
	}
	if cfg.MultipartThresholdBytes != 96*1024*1024 {
		t.Fatalf("expected MultipartThresholdBytes to be 96MB, got %d", cfg.MultipartThresholdBytes)
	}
	if cfg.MultipartPartSizeBytes != 24*1024*1024 {
		t.Fatalf("expected MultipartPartSizeBytes to be 24MB, got %d", cfg.MultipartPartSizeBytes)
	}
}

func TestNormalizeMultipartDefaults(t *testing.T) {
	if got := NormalizeMultipartThresholdBytes(0); got != 64*1024*1024 {
		t.Fatalf("NormalizeMultipartThresholdBytes(0) = %d", got)
	}
	if got := NormalizeMultipartPartSizeBytes(0); got != 16*1024*1024 {
		t.Fatalf("NormalizeMultipartPartSizeBytes(0) = %d", got)
	}
	if got := NormalizeMultipartPartSizeBytes(4 * 1024 * 1024); got != 16*1024*1024 {
		t.Fatalf("expected part size below minimum to fall back, got %d", got)
	}
}

func TestNewObjectStoreForProviderBucketOverridesBucket(t *testing.T) {
	store, ok, err := NewObjectStoreForProviderBucket(config.StorageConfig{
		Type: "r2",
		R2: config.R2Storage{
			Endpoint:       "https://example.r2.cloudflarestorage.com",
			Region:         "auto",
			Bucket:         "primary-bucket",
			CDNUrl:         "https://cdn.example.com",
			ForcePathStyle: true,
		},
	}, "r2", "historical-bucket")
	if err != nil {
		t.Fatalf("NewObjectStoreForProviderBucket() error = %v", err)
	}
	if !ok || store == nil {
		t.Fatalf("expected object store to be initialized")
	}
	if store.Bucket() != "historical-bucket" {
		t.Fatalf("expected bucket override, got %q", store.Bucket())
	}
}

func TestNewR2CompatibleStoreCacheKeyIncludesDeliveryFields(t *testing.T) {
	cfg := ObjectStoreConfig{
		Provider:          "r2",
		Endpoint:          "https://example.r2.cloudflarestorage.com",
		Region:            "auto",
		Bucket:            "workmax-generations",
		AccessKeyID:       "key",
		SecretAccessKey:   "secret-a",
		CDNUrl:            "https://cdn-a.example.com",
		PathPrefix:        "generations",
		UsePresignedURL:   false,
		PresignTTLSeconds: 3600,
	}

	storeA, err := NewR2CompatibleStore(cfg)
	if err != nil {
		t.Fatalf("NewR2CompatibleStore() error = %v", err)
	}

	cfg.CDNUrl = "https://cdn-b.example.com"
	storeB, err := NewR2CompatibleStore(cfg)
	if err != nil {
		t.Fatalf("NewR2CompatibleStore() error = %v", err)
	}

	if storeA == storeB {
		t.Fatalf("expected store cache to differentiate CDN changes")
	}

	cfg.CDNUrl = "https://cdn-a.example.com"
	cfg.UsePresignedURL = true
	storeC, err := NewR2CompatibleStore(cfg)
	if err != nil {
		t.Fatalf("NewR2CompatibleStore() error = %v", err)
	}

	if storeA == storeC {
		t.Fatalf("expected store cache to differentiate presigned mode changes")
	}
}
