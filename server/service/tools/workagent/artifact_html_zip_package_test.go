package workagent

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildHTMLZipPackage_RewritesHTMLAndIncludesAssets(t *testing.T) {
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./hero.png?v=1", BundlePath: "assets/hero.png", Status: "pending"},
			{SourceURL: "/styles/theme.css", BundlePath: "assets/theme.css", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackage(
		`<html><img src="./hero.png?v=1"><link href="/styles/theme.css"></html>`,
		manifest,
		func(sourceURL string) ([]byte, error) {
			return []byte("asset:" + sourceURL), nil
		},
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["index.html"]) != `<html><img src="assets/hero.png"><link href="assets/theme.css"></html>` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	if string(files["assets/hero.png"]) != "asset:./hero.png?v=1" {
		t.Fatalf("hero asset = %q", string(files["assets/hero.png"]))
	}
	if string(files["assets/theme.css"]) != "asset:/styles/theme.css" {
		t.Fatalf("theme asset = %q", string(files["assets/theme.css"]))
	}
}

func TestBuildHTMLZipPackage_RewritesSrcSetCandidates(t *testing.T) {
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./hero-small.png", BundlePath: "assets/hero-small.png", Status: "pending"},
			{SourceURL: "./hero-large.png", BundlePath: "assets/hero-large.png", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackage(
		`<img srcset="./hero-small.png 1x, ./hero-large.png 2x" src="./hero-small.png">`,
		manifest,
		func(sourceURL string) ([]byte, error) {
			return []byte("asset:" + sourceURL), nil
		},
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["index.html"]) != `<img srcset="assets/hero-small.png 1x, assets/hero-large.png 2x" src="assets/hero-small.png">` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	if string(files["assets/hero-small.png"]) != "asset:./hero-small.png" {
		t.Fatalf("small asset = %q", string(files["assets/hero-small.png"]))
	}
	if string(files["assets/hero-large.png"]) != "asset:./hero-large.png" {
		t.Fatalf("large asset = %q", string(files["assets/hero-large.png"]))
	}
}

func TestBuildHTMLZipPackage_RewritesImageSrcSetPreloadCandidates(t *testing.T) {
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./hero-small.png", BundlePath: "assets/hero-small.png", Status: "pending"},
			{SourceURL: "./hero-large.png", BundlePath: "assets/hero-large.png", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackage(
		`<link rel="preload" as="image" href="./hero-small.png" imagesrcset="./hero-small.png 1x, ./hero-large.png 2x"><img src="./hero-small.png">`,
		manifest,
		func(sourceURL string) ([]byte, error) {
			return []byte("asset:" + sourceURL), nil
		},
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["index.html"]) != `<link rel="preload" as="image" href="assets/hero-small.png" imagesrcset="assets/hero-small.png 1x, assets/hero-large.png 2x"><img src="assets/hero-small.png">` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	if string(files["assets/hero-large.png"]) != "asset:./hero-large.png" {
		t.Fatalf("large asset = %q", string(files["assets/hero-large.png"]))
	}
}

func TestBuildHTMLZipPackage_RewritesVideoPosterAndTrackAssets(t *testing.T) {
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./demo.mp4", BundlePath: "assets/demo.mp4", Status: "pending"},
			{SourceURL: "./demo-poster.jpg", BundlePath: "assets/demo-poster.jpg", Status: "pending"},
			{SourceURL: "./captions.vtt", BundlePath: "assets/captions.vtt", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackage(
		`<video src="./demo.mp4" poster="./demo-poster.jpg" controls><track kind="captions" src="./captions.vtt"></video>`,
		manifest,
		func(sourceURL string) ([]byte, error) {
			return []byte("asset:" + sourceURL), nil
		},
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["index.html"]) != `<video src="assets/demo.mp4" poster="assets/demo-poster.jpg" controls><track kind="captions" src="assets/captions.vtt"></video>` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	if string(files["assets/demo-poster.jpg"]) != "asset:./demo-poster.jpg" {
		t.Fatalf("poster asset = %q", string(files["assets/demo-poster.jpg"]))
	}
	if string(files["assets/captions.vtt"]) != "asset:./captions.vtt" {
		t.Fatalf("captions asset = %q", string(files["assets/captions.vtt"]))
	}
}

func TestBuildHTMLZipPackage_RejectsBlockedManifest(t *testing.T) {
	_, err := BuildHTMLZipPackage("html", &ArtifactHTMLAssetBundleManifest{Blocked: true}, func(string) ([]byte, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected blocked manifest error")
	}
}

func TestBuildHTMLZipPackage_ReturnsAssetLoaderErrors(t *testing.T) {
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./missing.png", BundlePath: "assets/missing.png", Status: "pending"},
		},
	}
	_, err := BuildHTMLZipPackage("html", manifest, func(string) ([]byte, error) {
		return nil, errors.New("not found")
	})
	if err == nil {
		t.Fatal("expected asset loader error")
	}
}

func TestBuildHTMLZipPackage_ClassifiesMissingLocalAssets(t *testing.T) {
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./missing.png", BundlePath: "assets/missing.png", Status: "pending"},
		},
	}
	_, err := BuildHTMLZipPackage("html", manifest, func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	})
	var packageErr *HTMLZipPackageError
	if !errors.As(err, &packageErr) {
		t.Fatalf("error = %T %[1]v, want HTMLZipPackageError", err)
	}
	if packageErr.Code != "missing_local_asset" || packageErr.SourceURL != "./missing.png" {
		t.Fatalf("package error = %#v", packageErr)
	}
}

func TestBuildHTMLZipPackage_RejectsUnsafeBundlePaths(t *testing.T) {
	for _, bundlePath := range []string{"../evil.png", "/tmp/evil.png", `assets\evil.png`, "assets//evil.png"} {
		t.Run(bundlePath, func(t *testing.T) {
			manifest := &ArtifactHTMLAssetBundleManifest{
				Entries: []ArtifactHTMLAssetBundleEntry{
					{SourceURL: "./hero.png", BundlePath: bundlePath, Status: "pending"},
				},
			}
			_, err := BuildHTMLZipPackage("html", manifest, func(string) ([]byte, error) {
				return []byte("asset"), nil
			})
			if err == nil {
				t.Fatal("expected unsafe bundle path error")
			}
		})
	}
}

func TestBuildHTMLZipPackage_RejectsDuplicateBundlePaths(t *testing.T) {
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./hero.png", BundlePath: "assets/hero.png", Status: "pending"},
			{SourceURL: "./mobile-hero.png", BundlePath: "assets/hero.png", Status: "pending"},
		},
	}
	_, err := BuildHTMLZipPackage("html", manifest, func(string) ([]byte, error) {
		return []byte("asset"), nil
	})
	if err == nil {
		t.Fatal("expected duplicate bundle path error")
	}
}

func TestBuildHTMLZipPackageForFile_LoadsAssetsFromArtifactDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hero.png"), []byte("hero-bytes"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "styles"), 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "styles", "theme.css"), []byte("css-bytes"), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	htmlPath := filepath.Join(dir, "index.html")
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./hero.png?cache=1", BundlePath: "assets/hero.png", Status: "pending"},
			{SourceURL: "/styles/theme.css", BundlePath: "assets/theme.css", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackageForFile(
		`<img src="./hero.png?cache=1"><link href="/styles/theme.css">`,
		htmlPath,
		manifest,
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["assets/hero.png"]) != "hero-bytes" {
		t.Fatalf("hero asset = %q", string(files["assets/hero.png"]))
	}
	if string(files["assets/theme.css"]) != "css-bytes" {
		t.Fatalf("theme asset = %q", string(files["assets/theme.css"]))
	}
	if string(files["index.html"]) != `<img src="assets/hero.png"><link href="assets/theme.css">` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
}

func TestBuildHTMLZipPackageForFile_RewritesNestedCSSAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "styles"), 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatalf("mkdir fonts: %v", err)
	}
	css := `.hero{background-image:url("../images/bg.png")}@font-face{font-family:Acme;src:url("../fonts/acme.woff2")}`
	if err := os.WriteFile(filepath.Join(dir, "styles", "theme.css"), []byte(css), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "bg.png"), []byte("bg-bytes"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fonts", "acme.woff2"), []byte("font-bytes"), 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./styles/theme.css", BundlePath: "assets/theme.css", Source: "html_attr", Status: "pending"},
			{SourceURL: "../images/bg.png", BundlePath: "assets/bg.png", Source: "css_url", Status: "pending"},
			{SourceURL: "../fonts/acme.woff2", BundlePath: "assets/acme.woff2", Source: "css_url", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackageForFile(
		`<html><head><link rel="stylesheet" href="./styles/theme.css"></head><body><main class="hero">Poster</main></body></html>`,
		filepath.Join(dir, "index.html"),
		manifest,
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["index.html"]) != `<html><head><link rel="stylesheet" href="assets/theme.css"></head><body><main class="hero">Poster</main></body></html>` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	wantCSS := `.hero{background-image:url("bg.png")}@font-face{font-family:Acme;src:url("acme.woff2")}`
	if string(files["assets/theme.css"]) != wantCSS {
		t.Fatalf("theme.css = %q, want %q", string(files["assets/theme.css"]), wantCSS)
	}
	if string(files["assets/bg.png"]) != "bg-bytes" {
		t.Fatalf("bg asset = %q", string(files["assets/bg.png"]))
	}
	if string(files["assets/acme.woff2"]) != "font-bytes" {
		t.Fatalf("font asset = %q", string(files["assets/acme.woff2"]))
	}
}

func TestBuildHTMLZipPackageForFile_RewritesCSSImageSetAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "styles"), 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	css := `.hero{background-image:image-set("../images/poster.png" 1x, "../images/poster@2x.png" 2x)}`
	if err := os.WriteFile(filepath.Join(dir, "styles", "theme.css"), []byte(css), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "poster.png"), []byte("poster-bytes"), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "poster@2x.png"), []byte("poster-2x-bytes"), 0o644); err != nil {
		t.Fatalf("write poster 2x: %v", err)
	}
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./styles/theme.css", BundlePath: "assets/theme.css", Source: "html_attr", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackageForFile(
		`<html><head><link rel="stylesheet" href="./styles/theme.css"></head><body><main class="hero">Poster</main></body></html>`,
		filepath.Join(dir, "index.html"),
		manifest,
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	wantCSS := `.hero{background-image:image-set("poster.png" 1x, "poster@2x.png" 2x)}`
	if string(files["assets/theme.css"]) != wantCSS {
		t.Fatalf("theme.css = %q, want %q", string(files["assets/theme.css"]), wantCSS)
	}
	if string(files["assets/poster.png"]) != "poster-bytes" {
		t.Fatalf("poster asset = %q", string(files["assets/poster.png"]))
	}
	if string(files["assets/poster@2x.png"]) != "poster-2x-bytes" {
		t.Fatalf("poster 2x asset = %q", string(files["assets/poster@2x.png"]))
	}
}

func TestBuildHTMLZipPackageForFile_RecursivelyBundlesImportedCSSAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "styles", "components"), 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatalf("mkdir fonts: %v", err)
	}
	themeCSS := `@import "./components/card.css";.page{background:url("../images/page.png")}`
	cardCSS := `@import "../tokens.css";.card{background:url("../../images/card.png")}@font-face{font-family:Acme;src:url("../../fonts/acme.woff2")}`
	tokensCSS := `:root{--accent:#123456}`
	if err := os.WriteFile(filepath.Join(dir, "styles", "theme.css"), []byte(themeCSS), 0o644); err != nil {
		t.Fatalf("write theme css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "styles", "components", "card.css"), []byte(cardCSS), 0o644); err != nil {
		t.Fatalf("write card css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "styles", "tokens.css"), []byte(tokensCSS), 0o644); err != nil {
		t.Fatalf("write tokens css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "page.png"), []byte("page-bytes"), 0o644); err != nil {
		t.Fatalf("write page image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "card.png"), []byte("card-bytes"), 0o644); err != nil {
		t.Fatalf("write card image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fonts", "acme.woff2"), []byte("font-bytes"), 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./styles/theme.css", BundlePath: "assets/theme.css", Source: "html_attr", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackageForFile(
		`<html><head><link rel="stylesheet" href="./styles/theme.css"></head><body><main class="page">Poster</main></body></html>`,
		filepath.Join(dir, "index.html"),
		manifest,
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["index.html"]) != `<html><head><link rel="stylesheet" href="assets/theme.css"></head><body><main class="page">Poster</main></body></html>` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	if string(files["assets/theme.css"]) != `@import "card.css";.page{background:url("page.png")}` {
		t.Fatalf("theme.css = %q", string(files["assets/theme.css"]))
	}
	if string(files["assets/card.css"]) != `@import "tokens.css";.card{background:url("card.png")}@font-face{font-family:Acme;src:url("acme.woff2")}` {
		t.Fatalf("card.css = %q", string(files["assets/card.css"]))
	}
	if string(files["assets/tokens.css"]) != tokensCSS {
		t.Fatalf("tokens.css = %q", string(files["assets/tokens.css"]))
	}
	for name, want := range map[string]string{
		"assets/page.png":   "page-bytes",
		"assets/card.png":   "card-bytes",
		"assets/acme.woff2": "font-bytes",
	} {
		if string(files[name]) != want {
			t.Fatalf("%s = %q, want %q", name, string(files[name]), want)
		}
	}
}

func TestBuildHTMLZipPackageForFile_RecursivelyBundlesCacheBustedCSSAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "styles", "components"), 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatalf("mkdir fonts: %v", err)
	}
	themeCSS := `@import "./components/card.css?v=2#screen";.page{background:url("../images/page.png?cache=7#hero")}`
	cardCSS := `.card{background:image-set("../../images/card.png?v=3" 1x, "../../images/card@2x.png#retina" 2x)}@font-face{font-family:Acme;src:url("../../fonts/acme.woff2?v=1")}`
	if err := os.WriteFile(filepath.Join(dir, "styles", "theme.css"), []byte(themeCSS), 0o644); err != nil {
		t.Fatalf("write theme css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "styles", "components", "card.css"), []byte(cardCSS), 0o644); err != nil {
		t.Fatalf("write card css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "page.png"), []byte("page-bytes"), 0o644); err != nil {
		t.Fatalf("write page image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "card.png"), []byte("card-bytes"), 0o644); err != nil {
		t.Fatalf("write card image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "card@2x.png"), []byte("card-2x-bytes"), 0o644); err != nil {
		t.Fatalf("write card retina image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fonts", "acme.woff2"), []byte("font-bytes"), 0o644); err != nil {
		t.Fatalf("write font: %v", err)
	}
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./styles/theme.css?v=1#main", BundlePath: "assets/theme.css", Source: "html_attr", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackageForFile(
		`<html><head><link rel="stylesheet" href="./styles/theme.css?v=1#main"></head><body><main class="page">Poster</main></body></html>`,
		filepath.Join(dir, "index.html"),
		manifest,
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	if string(files["index.html"]) != `<html><head><link rel="stylesheet" href="assets/theme.css"></head><body><main class="page">Poster</main></body></html>` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	if string(files["assets/theme.css"]) != `@import "card.css";.page{background:url("page.png")}` {
		t.Fatalf("theme.css = %q", string(files["assets/theme.css"]))
	}
	if string(files["assets/card.css"]) != `.card{background:image-set("card.png" 1x, "card@2x.png" 2x)}@font-face{font-family:Acme;src:url("acme.woff2")}` {
		t.Fatalf("card.css = %q", string(files["assets/card.css"]))
	}
	for name, want := range map[string]string{
		"assets/page.png":    "page-bytes",
		"assets/card.png":    "card-bytes",
		"assets/card@2x.png": "card-2x-bytes",
		"assets/acme.woff2":  "font-bytes",
	} {
		if string(files[name]) != want {
			t.Fatalf("%s = %q, want %q", name, string(files[name]), want)
		}
	}
}

func TestBuildHTMLZipPackageForFile_RewritesDuplicateDecoratedCSSAssetAliases(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "styles"), 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	css := `.plain{background:url("../images/bg.png")}.hero{background:url("../images/bg.png?v=1#hero")}.card{background:url('../images/bg.png?v=2')}.logo{background-image:image-set("../images/bg.png#one" 1x, "../images/bg.png?density=2" 2x)}.inline{background:url("data:image/svg+xml,%3Csvg%3E%3C/svg%3E")}`
	if err := os.WriteFile(filepath.Join(dir, "styles", "theme.css"), []byte(css), 0o644); err != nil {
		t.Fatalf("write theme css: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "bg.png"), []byte("bg-bytes"), 0o644); err != nil {
		t.Fatalf("write bg image: %v", err)
	}
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "./styles/theme.css", BundlePath: "assets/theme.css", Source: "html_attr", Status: "pending"},
		},
	}
	got, err := BuildHTMLZipPackageForFile(
		`<html><head><link rel="stylesheet" href="./styles/theme.css"></head><body><main class="hero">Poster</main></body></html>`,
		filepath.Join(dir, "index.html"),
		manifest,
	)
	if err != nil {
		t.Fatalf("build zip: %v", err)
	}
	files := unzipHTMLPackage(t, got)
	wantCSS := `.plain{background:url("bg.png")}.hero{background:url("bg.png")}.card{background:url('bg.png')}.logo{background-image:image-set("bg.png" 1x, "bg.png" 2x)}.inline{background:url("data:image/svg+xml,%3Csvg%3E%3C/svg%3E")}`
	if string(files["assets/theme.css"]) != wantCSS {
		t.Fatalf("theme.css = %q, want %q", string(files["assets/theme.css"]), wantCSS)
	}
	if string(files["assets/bg.png"]) != "bg-bytes" {
		t.Fatalf("bg asset = %q", string(files["assets/bg.png"]))
	}
}

func TestBuildHTMLZipPackageForFile_RejectsEscapingAssets(t *testing.T) {
	dir := t.TempDir()
	manifest := &ArtifactHTMLAssetBundleManifest{
		Entries: []ArtifactHTMLAssetBundleEntry{
			{SourceURL: "../secret.png", BundlePath: "assets/secret.png", Status: "pending"},
		},
	}
	_, err := BuildHTMLZipPackageForFile("html", filepath.Join(dir, "index.html"), manifest)
	if err == nil {
		t.Fatal("expected escaping asset error")
	}
}

func unzipHTMLPackage(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := map[string][]byte{}
	for _, file := range zr.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			_ = rc.Close()
			t.Fatalf("read %s: %v", file.Name, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("close %s: %v", file.Name, err)
		}
		out[file.Name] = buf.Bytes()
	}
	return out
}
