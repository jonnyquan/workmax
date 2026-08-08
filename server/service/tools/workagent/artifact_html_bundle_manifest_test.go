package workagent

import "testing"

func TestBuildHTMLAssetBundleManifest_MapsLocalBundleReferences(t *testing.T) {
	view := ArtifactView{
		OutputType:  "html",
		PreviewType: "html",
		HTMLValidation: &ArtifactHTMLValidationResult{
			AssetReferences: []ArtifactHTMLAssetReference{
				{URL: "./hero.png?v=1", Kind: "local", Source: "html_attr", Action: "bundle"},
				{URL: "/styles/theme.css", Kind: "local", Source: "css_import", Action: "bundle"},
				{URL: "https://cdn.example.com/bg.png", Kind: "remote", Source: "css_url", Action: "inline_or_mirror"},
			},
		},
	}
	got := BuildHTMLAssetBundleManifest(view)
	if got == nil {
		t.Fatal("expected manifest")
	}
	if got.Blocked {
		t.Fatalf("manifest should not be blocked: %#v", got)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2: %#v", len(got.Entries), got.Entries)
	}
	if got.Entries[0].SourceURL != "./hero.png?v=1" || got.Entries[0].BundlePath != "assets/hero.png" || got.Entries[0].Status != "pending" {
		t.Fatalf("first entry = %#v", got.Entries[0])
	}
	if got.Entries[1].SourceURL != "/styles/theme.css" || got.Entries[1].BundlePath != "assets/theme.css" {
		t.Fatalf("second entry = %#v", got.Entries[1])
	}
}

func TestBuildHTMLAssetBundleManifest_DisambiguatesNamesAndBlocksTraversal(t *testing.T) {
	view := ArtifactView{
		OutputType:  "html",
		PreviewType: "html",
		HTMLValidation: &ArtifactHTMLValidationResult{
			AssetReferences: []ArtifactHTMLAssetReference{
				{URL: "./img/hero.png", Kind: "local", Source: "html_attr", Action: "bundle"},
				{URL: "./mobile/hero.png", Kind: "local", Source: "srcset", Action: "bundle"},
				{URL: "../secret.png", Kind: "local", Source: "css_url", Action: "bundle"},
			},
		},
	}
	got := BuildHTMLAssetBundleManifest(view)
	if got == nil || !got.Blocked {
		t.Fatalf("expected blocked manifest, got %#v", got)
	}
	if got.Entries[0].BundlePath != "assets/hero.png" {
		t.Fatalf("first path = %q", got.Entries[0].BundlePath)
	}
	if got.Entries[1].BundlePath != "assets/hero-2.png" {
		t.Fatalf("second path = %q", got.Entries[1].BundlePath)
	}
	if got.Entries[2].Status != "blocked" || got.Entries[2].Reason != "path_traversal" {
		t.Fatalf("third entry = %#v", got.Entries[2])
	}
}

func TestBuildHTMLAssetBundleManifest_IncludesVideoPosterAndTrackAssets(t *testing.T) {
	validation := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><video src="./demo.mp4" poster="./demo-poster.jpg" controls><track kind="captions" src="./captions.vtt"></video>Product demo</main></body></html>`)
	view := ArtifactView{
		OutputType:     "html",
		PreviewType:    "html",
		HTMLValidation: &validation,
	}
	got := BuildHTMLAssetBundleManifest(view)
	if got == nil {
		t.Fatal("expected manifest")
	}
	if got.Blocked {
		t.Fatalf("manifest should not be blocked: %#v", got)
	}
	want := map[string]bool{
		"./demo.mp4":        false,
		"./demo-poster.jpg": false,
		"./captions.vtt":    false,
	}
	for _, entry := range got.Entries {
		if _, ok := want[entry.SourceURL]; ok {
			want[entry.SourceURL] = true
		}
	}
	for sourceURL, seen := range want {
		if !seen {
			t.Fatalf("expected manifest to include %s, got %#v", sourceURL, got.Entries)
		}
	}
}

func TestBuildHTMLAssetBundleManifest_IncludesImageSrcSetPreloadCandidates(t *testing.T) {
	validation := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<link rel="preload" as="image" href="./hero-small.png" imagesrcset="./hero-small.png 1x, ./hero-large.png 2x">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main><img src="./hero-small.png" alt="">Poster</main></body></html>`)
	view := ArtifactView{
		OutputType:     "html",
		PreviewType:    "html",
		HTMLValidation: &validation,
	}
	got := BuildHTMLAssetBundleManifest(view)
	if got == nil {
		t.Fatal("expected manifest")
	}
	want := map[string]bool{
		"./hero-small.png": false,
		"./hero-large.png": false,
	}
	for _, entry := range got.Entries {
		if _, ok := want[entry.SourceURL]; ok {
			want[entry.SourceURL] = true
		}
	}
	for sourceURL, seen := range want {
		if !seen {
			t.Fatalf("expected manifest to include %s, got %#v", sourceURL, got.Entries)
		}
	}
}

func TestBuildHTMLAssetBundleManifest_IncludesCSSImageSetCandidates(t *testing.T) {
	validation := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; background-image: image-set("./poster.png" 1x, "./poster@2x.png" 2x); }</style>
</head><body><main>Poster</main></body></html>`)
	view := ArtifactView{
		OutputType:     "html",
		PreviewType:    "html",
		HTMLValidation: &validation,
	}
	got := BuildHTMLAssetBundleManifest(view)
	if got == nil {
		t.Fatal("expected manifest")
	}
	want := map[string]bool{
		"./poster.png":    false,
		"./poster@2x.png": false,
	}
	for _, entry := range got.Entries {
		if _, ok := want[entry.SourceURL]; ok {
			want[entry.SourceURL] = true
		}
	}
	for sourceURL, seen := range want {
		if !seen {
			t.Fatalf("expected manifest to include %s, got %#v", sourceURL, got.Entries)
		}
	}
}
