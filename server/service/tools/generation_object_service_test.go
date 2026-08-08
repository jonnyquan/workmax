package tools

import (
	"context"
	"io"
	"server/config"
	"strings"
	"testing"
	"time"

	"server/globals"
	"server/model"
	storageService "server/service/storage"
)

type fakeObjectStore struct {
	provider   string
	bucket     string
	publicBase string
}

func (f *fakeObjectStore) Put(ctx context.Context, req *storageService.PutObjectRequest) (*storageService.PutObjectResult, error) {
	return nil, nil
}

func (f *fakeObjectStore) Get(ctx context.Context, key string) (io.ReadCloser, string, error) {
	return io.NopCloser(strings.NewReader("")), "application/octet-stream", nil
}

func (f *fakeObjectStore) Delete(ctx context.Context, key string) error { return nil }

func (f *fakeObjectStore) PublicURL(key string) string {
	if strings.TrimSpace(f.publicBase) == "" {
		return key
	}
	return strings.TrimRight(strings.TrimSpace(f.publicBase), "/") + "/" + strings.TrimLeft(key, "/")
}

func (f *fakeObjectStore) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return key, nil
}

func (f *fakeObjectStore) DownloadURLWithDisposition(ctx context.Context, key string, ttl time.Duration, contentDisposition string) (string, error) {
	return key, nil
}

func (f *fakeObjectStore) Provider() string { return f.provider }

func (f *fakeObjectStore) Bucket() string { return f.bucket }

func withGenerationObjectTestStorageConfig(t *testing.T) {
	t.Helper()
	original := globals.GraConf.Generator.Storage
	globals.GraConf.Generator.Storage = config.StorageConfig{
		Type: "r2",
		R2: config.R2Storage{
			Endpoint:        "https://example.r2.cloudflarestorage.com",
			Region:          "auto",
			Bucket:          "nano-generated",
			CDNUrl:          "https://cdn.example.com",
			ForcePathStyle:  true,
			PathPrefix:      "",
			UsePresignedURL: false,
		},
	}
	t.Cleanup(func() {
		globals.GraConf.Generator.Storage = original
	})
}

func TestNormalizeGenerationObjectTaskIDs(t *testing.T) {
	got := normalizeGenerationObjectTaskIDs([]string{" task-1 ", "", "task-2", "task-1", "  "})
	if len(got) != 2 {
		t.Fatalf("normalizeGenerationObjectTaskIDs() length = %d, want 2", len(got))
	}
	if got[0] != "task-1" || got[1] != "task-2" {
		t.Fatalf("normalizeGenerationObjectTaskIDs() = %#v", got)
	}
}

func TestNormalizeGenerationObjectRecordIDs(t *testing.T) {
	got := normalizeGenerationObjectRecordIDs([]uint{0, 12, 12, 7, 0, 7, 9})
	if len(got) != 3 {
		t.Fatalf("normalizeGenerationObjectRecordIDs() length = %d, want 3", len(got))
	}
	if got[0] != 12 || got[1] != 7 || got[2] != 9 {
		t.Fatalf("normalizeGenerationObjectRecordIDs() = %#v", got)
	}
}

func TestMatchesConfiguredObjectStore(t *testing.T) {
	store := &fakeObjectStore{provider: "r2", bucket: "nano-generated", publicBase: "https://cdn.example.com"}

	if !matchesConfiguredObjectStore(store, &model.GenerationObject{
		Provider:  "r2",
		Bucket:    "nano-generated",
		ObjectKey: "images/2026/03/15/a.png",
	}) {
		t.Fatalf("expected object to match configured store")
	}

	if matchesConfiguredObjectStore(store, &model.GenerationObject{
		Provider:  "local",
		Bucket:    "nano-generated",
		ObjectKey: "images/2026/03/15/a.png",
	}) {
		t.Fatalf("expected provider mismatch to be rejected")
	}

	if matchesConfiguredObjectStore(store, &model.GenerationObject{
		Provider:  "r2",
		Bucket:    "other-bucket",
		ObjectKey: "images/2026/03/15/a.png",
	}) {
		t.Fatalf("expected bucket mismatch to be rejected")
	}
}

func TestResolveGenerationObjectURLsRewritesMatchedPublicURLs(t *testing.T) {
	withGenerationObjectTestStorageConfig(t)

	objects := []model.GenerationObject{
		{
			GraMODEL:  globals.GraMODEL{Id: 1},
			Provider:  "r2",
			Bucket:    "nano-generated",
			ObjectKey: "videos/2026/03/15/task-1/main.mp4",
			PublicURL: "https://cdn.example.com/videos/task-1.mp4",
		},
	}

	got := resolveGenerationObjectURLs(context.Background(), objects, []string{"https://cdn.example.com/videos/task-1.mp4"})
	if len(got) != 1 {
		t.Fatalf("resolveGenerationObjectURLs() length = %d, want 1", len(got))
	}
	if got[0] != "https://cdn.example.com/videos/2026/03/15/task-1/main.mp4" {
		t.Fatalf("resolveGenerationObjectURLs() = %#v", got)
	}
}

func TestResolveGenerationObjectURLsUsesSingleObjectFallback(t *testing.T) {
	withGenerationObjectTestStorageConfig(t)

	objects := []model.GenerationObject{
		{
			GraMODEL:  globals.GraMODEL{Id: 2},
			Provider:  "r2",
			Bucket:    "nano-generated",
			ObjectKey: "assets/2026/03/15/task-2/model.glb",
			PublicURL: "https://cdn.example.com/assets/task-2/model.glb",
		},
	}

	got := resolveGenerationObjectURLs(context.Background(), objects, []string{"https://historical.example.com/model.glb"})
	if len(got) != 1 {
		t.Fatalf("resolveGenerationObjectURLs() length = %d, want 1", len(got))
	}
	if got[0] != "https://cdn.example.com/assets/2026/03/15/task-2/model.glb" {
		t.Fatalf("resolveGenerationObjectURLs() = %#v", got)
	}
}

func TestResolveGenerationObjectURLsLeavesUnmatchedURLsUntouched(t *testing.T) {
	withGenerationObjectTestStorageConfig(t)

	objects := []model.GenerationObject{
		{
			GraMODEL:  globals.GraMODEL{Id: 3},
			Provider:  "r2",
			Bucket:    "nano-generated",
			ObjectKey: "videos/2026/03/15/task-3/main.mp4",
			PublicURL: "https://cdn.example.com/videos/task-3.mp4",
		},
	}

	input := []string{"https://other.example.com/videos/task-999.mp4", "https://cdn.example.com/videos/task-3.mp4"}
	got := resolveGenerationObjectURLs(context.Background(), objects, input)
	if len(got) != 2 {
		t.Fatalf("resolveGenerationObjectURLs() length = %d, want 2", len(got))
	}
	if got[0] != input[0] {
		t.Fatalf("first URL should remain untouched, got %q", got[0])
	}
	if got[1] != "https://cdn.example.com/videos/2026/03/15/task-3/main.mp4" {
		t.Fatalf("second URL should resolve to download URL, got %q", got[1])
	}
}

func TestResolveGenerationObjectURLsFallsBackToPublicURLWhenStoreUnavailable(t *testing.T) {
	objects := []model.GenerationObject{
		{
			GraMODEL:  globals.GraMODEL{Id: 4},
			Provider:  "historical-r2",
			Bucket:    "legacy-bucket",
			ObjectKey: "videos/2026/03/15/task-4/main.mp4",
			PublicURL: "https://legacy.example.com/videos/task-4.mp4",
		},
	}

	got := resolveGenerationObjectURLs(context.Background(), objects, []string{"https://old.example.com/task-4.mp4"})
	if len(got) != 1 {
		t.Fatalf("resolveGenerationObjectURLs() length = %d, want 1", len(got))
	}
	if got[0] != "https://legacy.example.com/videos/task-4.mp4" {
		t.Fatalf("resolveGenerationObjectURLs() = %#v", got)
	}
}

func TestResolveGenerationObjectDeliveryURLReturnsPublicURLForLocal(t *testing.T) {
	url, err := ResolveGenerationObjectDeliveryURL(context.Background(), &model.GenerationObject{
		Provider:  "local",
		Bucket:    "local",
		ObjectKey: "uid/26/images/2026/03/24/task-1_0.png",
		PublicURL: "/uploads/generations/uid/26/images/2026/03/24/task-1_0.png",
	}, 0)
	if err != nil {
		t.Fatalf("ResolveGenerationObjectDeliveryURL() error = %v", err)
	}
	if url != "/uploads/generations/uid/26/images/2026/03/24/task-1_0.png" {
		t.Fatalf("ResolveGenerationObjectDeliveryURL() = %q", url)
	}
}

func TestBuildGenerationObjectBackfillCandidate(t *testing.T) {
	withGenerationObjectTestStorageConfig(t)
	store := &fakeObjectStore{provider: "r2", bucket: "nano-generated", publicBase: "https://cdn.example.com"}

	candidate, ok := buildGenerationObjectBackfillCandidate(globals.GraConf.Generator.Storage, store, true, store.PublicURL("assets/2026/03/15/task-1/model.glb?token=123"))
	if !ok {
		t.Fatal("expected candidate to be resolved")
	}
	if candidate.Provider != "r2" || candidate.Bucket != "nano-generated" {
		t.Fatalf("provider/bucket = %q/%q", candidate.Provider, candidate.Bucket)
	}
	if candidate.ObjectKey != "assets/2026/03/15/task-1/model.glb" {
		t.Fatalf("object key = %q", candidate.ObjectKey)
	}
	if candidate.AssetKind != "asset" {
		t.Fatalf("asset kind = %q", candidate.AssetKind)
	}
	if candidate.ContentType != "model/gltf-binary" {
		t.Fatalf("content type = %q", candidate.ContentType)
	}
}

func TestInferGenerationObjectAssetKind(t *testing.T) {
	cases := map[string]string{
		"generations/reference-images/42/2026/03/15/ref.png":               "reference",
		"generations/uid/42/assets/2026/03/15/task-1/model.glb":            "asset",
		"generations/uid/42/videos/2026/03/15/task-1/task-1_thumbnail.jpg": "thumbnail",
		"generations/uid/42/videos/2026/03/15/task-1/task-1_video.mp4":     "video",
		"generations/uid/42/images/2026/03/15/task-1/task-1_0.png":         "image",
	}

	for key, want := range cases {
		if got := inferGenerationObjectAssetKind(key); got != want {
			t.Fatalf("inferGenerationObjectAssetKind(%q) = %q, want %q", key, got, want)
		}
	}
}
