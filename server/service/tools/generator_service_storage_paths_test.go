package tools

import (
	"context"
	"io"
	"net/url"
	"path/filepath"
	"server/config"
	"server/globals"
	"strings"
	"testing"
	"time"
)

func TestBuildGeneratedObjectKeyUsesTypedFolders(t *testing.T) {
	cfg := config.StorageConfig{
		Type: "r2",
		R2: config.R2Storage{
			PathPrefix: "generations",
		},
	}

	cases := []struct {
		name        string
		assetKind   string
		contentType string
		want        string
	}{
		{name: "image", assetKind: "image", contentType: "image/png", want: "generations/uid/42/images/2026/03/15/task-1/task-1_0.png"},
		{name: "video", assetKind: "video", contentType: "video/mp4", want: "generations/uid/42/videos/2026/03/15/task-1/task-1_video_0.mp4"},
		{name: "thumbnail", assetKind: "thumbnail", contentType: "image/jpeg", want: "generations/uid/42/videos/2026/03/15/task-1/task-1_thumbnail_0.jpg"},
		{name: "asset", assetKind: "asset", contentType: "model/gltf-binary", want: "generations/uid/42/assets/2026/03/15/task-1/task-1_asset_0.glb"},
		{name: "preview", assetKind: "preview", contentType: "image/png", want: "generations/uid/42/images/2026/03/15/task-1/task-1_preview_0.png"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filename := buildStoredAssetFilename("task-1", 0, "", tc.contentType, tc.assetKind)
			if tc.assetKind == "image" {
				filename = "task-1_0.png"
			}
			got := buildGeneratedObjectKey(cfg, 42, tc.assetKind, "2026/03/15", "task-1", filename)
			if got != tc.want {
				t.Fatalf("buildGeneratedObjectKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildReferenceObjectKeyUsesReferenceFolder(t *testing.T) {
	cfg := config.StorageConfig{
		Type: "r2",
		R2: config.R2Storage{
			PathPrefix: "generations",
		},
	}

	got := buildReferenceObjectKey(cfg, "42", "2026/03/15", "ref.png")
	want := "generations/reference-images/42/2026/03/15/ref.png"
	if got != want {
		t.Fatalf("buildReferenceObjectKey() = %q, want %q", got, want)
	}
}

func TestReferenceImageLocalStorageLayout(t *testing.T) {
	localCfg := config.LocalStorage{
		Path:      "./uploads/generations",
		URLPrefix: "/uploads/generations",
	}

	if got := filepath.ToSlash(referenceImageDiskRoot(localCfg)); got != "uploads/references" && got != "./uploads/references" {
		t.Fatalf("referenceImageDiskRoot() = %q", got)
	}
	if got := filepath.ToSlash(referenceImageLegacyDiskRoot(localCfg)); got != "uploads/generations/reference-images" && got != "./uploads/generations/reference-images" {
		t.Fatalf("referenceImageLegacyDiskRoot() = %q", got)
	}
	if got := referenceImageURLPrefix(localCfg); got != "/uploads/references" {
		t.Fatalf("referenceImageURLPrefix() = %q", got)
	}
	if got := referenceImageLegacyURLPrefix(localCfg); got != "/uploads/generations/reference-images" {
		t.Fatalf("referenceImageLegacyURLPrefix() = %q", got)
	}
}

func TestResolveLocalReferenceRelativePathRequiresSegmentBoundary(t *testing.T) {
	if rel, ok := resolveLocalReferenceRelativePath("/uploads/references/example.png", "/uploads/references"); !ok || rel != "example.png" {
		t.Fatalf("resolveLocalReferenceRelativePath(valid path) = (%q, %v)", rel, ok)
	}
	for _, rawURL := range []string{
		"/uploads/references-legacy/example.png",
		"https://example.com/uploads/references-legacy/example.png",
	} {
		if rel, ok := resolveLocalReferenceRelativePath(rawURL, "/uploads/references"); ok {
			t.Fatalf("resolveLocalReferenceRelativePath(%q) unexpectedly accepted %q", rawURL, rel)
		}
	}
}

func TestObjectCacheControlByAssetKind(t *testing.T) {
	if got := objectCacheControl("image"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("objectCacheControl(image) = %q", got)
	}
	if got := objectCacheControl("asset"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("objectCacheControl(asset) = %q", got)
	}
	if got := objectCacheControl("reference"); got != "private, max-age=86400" {
		t.Fatalf("objectCacheControl(reference) = %q", got)
	}
}

func TestBuildObjectMetadataIncludesStableFields(t *testing.T) {
	got := buildObjectMetadata(42, "task-1", "vectorizer", "asset", "https://example.com/source.glb")
	if got["uid"] != "42" {
		t.Fatalf("uid metadata = %q", got["uid"])
	}
	if got["task-id"] != "task-1" {
		t.Fatalf("task-id metadata = %q", got["task-id"])
	}
	if got["tool-id"] != "vectorizer" {
		t.Fatalf("tool-id metadata = %q", got["tool-id"])
	}
	if got["asset-kind"] != "asset" {
		t.Fatalf("asset-kind metadata = %q", got["asset-kind"])
	}
	if got["source-url"] != "https://example.com/source.glb" {
		t.Fatalf("source-url metadata = %q", got["source-url"])
	}
}

func TestGetRemoteAssetConfigDefaultsAndOverrides(t *testing.T) {
	original := globals.GraConf.Generator.FileUpload
	t.Cleanup(func() {
		globals.GraConf.Generator.FileUpload = original
	})

	globals.GraConf.Generator.FileUpload = config.FileUploadConfig{}
	if got := GetMaxRemoteAssetSize(); got != 200*1024*1024 {
		t.Fatalf("GetMaxRemoteAssetSize() = %d", got)
	}
	if got := GetRemoteAssetTimeout(); got != 2*time.Minute {
		t.Fatalf("GetRemoteAssetTimeout() = %s", got)
	}

	globals.GraConf.Generator.FileUpload = config.FileUploadConfig{
		MaxRemoteAssetSize:        32 * 1024 * 1024,
		RemoteAssetTimeoutSeconds: 45,
	}
	if got := GetMaxRemoteAssetSize(); got != 32*1024*1024 {
		t.Fatalf("GetMaxRemoteAssetSize() override = %d", got)
	}
	if got := GetRemoteAssetTimeout(); got != 45*time.Second {
		t.Fatalf("GetRemoteAssetTimeout() override = %s", got)
	}
}

func TestMaxBytesReadCloserRejectsOverflow(t *testing.T) {
	reader := newMaxBytesReadCloser(strings.NewReader("abcdef"), io.NopCloser(strings.NewReader("")), 4)
	data, err := io.ReadAll(reader)
	if err == nil || err.Error() != "remote asset exceeds max allowed size" {
		t.Fatalf("expected overflow error, got %v", err)
	}
	if string(data) != "abcde" {
		t.Fatalf("read data = %q", string(data))
	}
}

func TestExceedsConfiguredRemoteAssetSize(t *testing.T) {
	if !exceedsConfiguredRemoteAssetSize(6, 4) {
		t.Fatal("expected oversized content length to be rejected")
	}
	if exceedsConfiguredRemoteAssetSize(4, 4) {
		t.Fatal("did not expect equal content length to be rejected")
	}
	if exceedsConfiguredRemoteAssetSize(-1, 4) {
		t.Fatal("did not expect unknown content length to be rejected at precheck")
	}
}

func TestValidateRemoteURLHostAllowlist(t *testing.T) {
	allowlist := []string{"example.com", "cdn.test.dev"}
	if !validateRemoteURLHostAllowlist("example.com", allowlist) {
		t.Fatal("expected exact host to match")
	}
	if !validateRemoteURLHostAllowlist("assets.example.com", allowlist) {
		t.Fatal("expected subdomain to match")
	}
	if !validateRemoteURLHostAllowlist("media.cdn.test.dev", allowlist) {
		t.Fatal("expected nested subdomain to match")
	}
	if validateRemoteURLHostAllowlist("evil-example.com", allowlist) {
		t.Fatal("did not expect suffix lookalike to match")
	}
	if validateRemoteURLHostAllowlist("example.org", allowlist) {
		t.Fatal("did not expect unrelated host to match")
	}
}

func TestValidateRemoteAssetURLRespectsAllowlist(t *testing.T) {
	original := globals.GraConf.Generator.FileUpload
	globals.GraConf.Generator.FileUpload = config.FileUploadConfig{
		AllowedRemoteAssetHosts: []string{"allowed.example.com"},
	}
	t.Cleanup(func() {
		globals.GraConf.Generator.FileUpload = original
	})

	blockedURL, _ := url.Parse("https://blocked.example.com/file.mp4")
	if err := (&GeneratorService{}).validateRemoteAssetURL(context.Background(), blockedURL); err == nil {
		t.Fatal("expected blocked host to fail")
	}
}

func TestValidateRemoteReferenceImageURLRejectsPrivateIPs(t *testing.T) {
	parsed, _ := url.Parse("http://127.0.0.1/image.png")
	if err := (&GeneratorService{}).validateRemoteReferenceImageURL(context.Background(), parsed); err == nil {
		t.Fatal("expected private host to fail closed")
	}
}
