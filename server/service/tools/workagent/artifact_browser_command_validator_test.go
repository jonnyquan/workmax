package workagent

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserPassesNonBlankScreenshots(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>hello</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v, want none", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsBlankScreenshot(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>hello</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 1024}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "mobile", Width: 390, Height: 844}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one blank screenshot issue", diagnostics)
	}
	if diagnostics[0].Code != "browser_screenshot_blank" || diagnostics[0].Source != "browser_validation" {
		t.Fatalf("diagnostic = %#v", diagnostics[0])
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsMissingLocalResources(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main><img src="./missing.png">hello</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath:    sourcePath,
		SourceHTML:    html,
		WorkspaceRoot: dir,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_resource_missing") {
		t.Fatalf("diagnostics = %#v, want missing resource", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsMissingCSSDependencies(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	stylesDir := filepath.Join(dir, "styles")
	if err := os.Mkdir(stylesDir, 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stylesDir, "poster.css"), []byte(`.hero{background-image:url("../images/missing-bg.png")}`), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	html := `<!doctype html><html><head><link rel="stylesheet" href="./styles/poster.css"></head><body><main class="hero">hello</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath:    sourcePath,
		SourceHTML:    html,
		WorkspaceRoot: dir,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_resource_missing") {
		t.Fatalf("diagnostics = %#v, want missing nested css resource", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsMissingExpandedAssetReferences(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	stylesDir := filepath.Join(dir, "styles")
	if err := os.Mkdir(stylesDir, 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stylesDir, "banner.css"), []byte(`.hero{background-image:image-set("../images/bg.png" 1x, "../images/bg@2x.png" 2x)}`), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	html := `<!doctype html><html><head><link rel="stylesheet" href="./styles/banner.css"><link rel="preload" as="image" imagesrcset="./preload-1x.png 1x, ./preload-2x.png 2x"></head><body><main class="hero"><video src="./demo.mp4" poster="./demo-poster.jpg" controls><track kind="captions" src="./captions.vtt"></video>hello</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath:    sourcePath,
		SourceHTML:    html,
		WorkspaceRoot: dir,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if countBrowserCommandDiagnostics(diagnostics, "browser_resource_missing") < 5 {
		t.Fatalf("diagnostics = %#v, want missing imagesrcset, poster/track, and css image-set resources", diagnostics)
	}
	for _, want := range []string{"./preload-1x.png", "./demo-poster.jpg", "./captions.vtt", "../images/bg.png"} {
		if !browserCommandDiagnosticsContain(diagnostics, want) {
			t.Fatalf("diagnostics = %#v, want resource detail %q", diagnostics, want)
		}
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserMapsStaticLayoutWarnings(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>SUMMERLAUNCHPROMOTIONWITHNOSPACES2026</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		StaticResult: ArtifactHTMLValidationResult{
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "long_unbreakable_text", Severity: "warn", Message: "long token"},
				{Code: "scrollable_artboard", Severity: "warn", Message: "scrollable"},
				{Code: "out_of_bounds_position", Severity: "warn", Message: "bounds"},
			},
		},
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "text_overflow") {
		t.Fatalf("diagnostics = %#v, want text_overflow", diagnostics)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "scroll_bounds") {
		t.Fatalf("diagnostics = %#v, want scroll_bounds", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsLayoutProbeDiagnostics(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main style="width:200px;overflow:hidden"><span>headline</span></main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_LAYOUT_PROBE", `WORKMAX_LAYOUT_PROBE_START{"textOverflow":true,"scrollBounds":true}WORKMAX_LAYOUT_PROBE_END`)
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "mobile", Width: 390, Height: 844}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "text_overflow") {
		t.Fatalf("diagnostics = %#v, want text_overflow from layout probe", diagnostics)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "scroll_bounds") {
		t.Fatalf("diagnostics = %#v, want scroll_bounds from layout probe", diagnostics)
	}
	if hasBrowserCommandDiagnostic(diagnostics, "browser_layout_probe_failed") {
		t.Fatalf("diagnostics = %#v, did not expect probe failure", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsMeasuredDOMBounds(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main style="width:1800px;height:1200px">oversized artboard</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_LAYOUT_PROBE", `WORKMAX_LAYOUT_PROBE_START{"textOverflow":false,"scrollBounds":true,"viewportWidth":390,"viewportHeight":844,"documentWidth":1800,"documentHeight":1200,"contentLeft":0,"contentTop":0,"contentRight":1800,"contentBottom":1200}WORKMAX_LAYOUT_PROBE_END`)
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "mobile", Width: 390, Height: 844}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_dom_bounds") {
		t.Fatalf("diagnostics = %#v, want browser_dom_bounds from measured DOM bounds", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsFontLoadRisk(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><head><style>@font-face{font-family:BrandDisplay;src:url("./brand.woff2")}body{font-family:BrandDisplay,Arial,sans-serif}</style></head><body><main>headline</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_LAYOUT_PROBE", `WORKMAX_LAYOUT_PROBE_START{"textOverflow":false,"scrollBounds":false,"fontLoadRisk":true}WORKMAX_LAYOUT_PROBE_END`)
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "font_load_risk") {
		t.Fatalf("diagnostics = %#v, want font_load_risk from layout probe", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsRuntimeImageLoadFailure(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main><img src="https://cdn.example.invalid/hero.png">headline</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_LAYOUT_PROBE", `WORKMAX_LAYOUT_PROBE_START{"textOverflow":false,"scrollBounds":false,"resourceLoadError":true,"resourceErrors":["https://cdn.example.invalid/hero.png"]}WORKMAX_LAYOUT_PROBE_END`)
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_resource_missing") {
		t.Fatalf("diagnostics = %#v, want browser_resource_missing from runtime image probe", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsConsoleAndRuntimeErrors(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><head><script>console.error("missing asset config");throw new Error("hero crash")</script></head><body><main>headline</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_LAYOUT_PROBE", `WORKMAX_LAYOUT_PROBE_START{"textOverflow":false,"scrollBounds":false,"consoleErrors":["missing asset config"],"consoleWarnings":["fallback font"],"runtimeErrors":["hero crash"]}WORKMAX_LAYOUT_PROBE_END`)
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_console_error") {
		t.Fatalf("diagnostics = %#v, want browser_console_error", diagnostics)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_runtime_error") {
		t.Fatalf("diagnostics = %#v, want browser_runtime_error", diagnostics)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_console_warn") {
		t.Fatalf("diagnostics = %#v, want browser_console_warn", diagnostics)
	}
}

func TestInjectBrowserLayoutProbeKeepsDoctypeFirstWithoutHead(t *testing.T) {
	html := `<!doctype html><html><body><main>headline</main></body></html>`
	injected := injectBrowserLayoutProbe(html)
	if !strings.HasPrefix(strings.ToLower(injected), "<!doctype html>") {
		snippet := injected
		if len(snippet) > 64 {
			snippet = snippet[:64]
		}
		t.Fatalf("injected html should keep doctype first, got %q", snippet)
	}
	if !strings.Contains(injected, "window.__workmaxRuntimeDiagnostics") {
		t.Fatalf("injected html missing runtime diagnostics hook: %s", injected)
	}
}

func TestInjectBrowserLayoutProbeCapturesRuntimeResourceErrors(t *testing.T) {
	html := `<!doctype html><html><head><script src="./missing.js"></script><link rel="stylesheet" href="./missing.css"></head><body><main>headline</main></body></html>`
	injected := injectBrowserLayoutProbe(html)
	for _, want := range []string{
		"resourceErrors:[]",
		"function resourceURL",
		"diagnostics.resourceErrors",
		"target.currentSrc || target.src || target.href",
		"allResourceErrors",
	} {
		if !strings.Contains(injected, want) {
			t.Fatalf("injected html missing %q: %s", want, injected)
		}
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsIdenticalViewportScreenshots(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>responsive poster</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{
				{Name: "desktop", Width: 1200, Height: 800},
				{Name: "mobile", Width: 390, Height: 844},
			},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_identical") {
		t.Fatalf("diagnostics = %#v, want viewport_screenshot_identical", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserAllowsDistinctViewportScreenshots(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>responsive poster</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_SCREENSHOT_BY_SIZE", "1")
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{
				{Name: "desktop", Width: 1200, Height: 800},
				{Name: "mobile", Width: 390, Height: 844},
			},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_identical") {
		t.Fatalf("diagnostics = %#v, did not expect identical viewport screenshots", diagnostics)
	}
}

func TestAppendBrowserScreenshotDiffDiagnosticsReportsNearIdenticalDecodedScreenshots(t *testing.T) {
	desktop := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 24, 24, func(x int, y int) color.RGBA {
		if x < 12 {
			return color.RGBA{R: 240, G: 240, B: 240, A: 255}
		}
		return color.RGBA{R: 40, G: 120, B: 220, A: 255}
	}))
	mobile := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 12, 12, func(x int, y int) color.RGBA {
		if x < 6 {
			return color.RGBA{R: 242, G: 242, B: 242, A: 255}
		}
		return color.RGBA{R: 42, G: 122, B: 222, A: 255}
	}))
	if desktop.Fingerprint == "" || desktop.Fingerprint != mobile.Fingerprint {
		t.Fatalf("fingerprints = %q/%q, want matching visual fingerprint", desktop.Fingerprint, mobile.Fingerprint)
	}
	diagnostics := appendBrowserScreenshotDiffDiagnostics(nil, []browserScreenshotProbe{
		{ViewportName: "desktop", Width: 1200, Height: 800, Size: 1000, Hash: "desktop-bytes", PixelStatsDecoded: true, PixelFingerprint: desktop.Fingerprint},
		{ViewportName: "mobile", Width: 390, Height: 844, Size: 800, Hash: "mobile-bytes", PixelStatsDecoded: true, PixelFingerprint: mobile.Fingerprint},
	})
	if !hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_near_identical") {
		t.Fatalf("diagnostics = %#v, want viewport_screenshot_near_identical", diagnostics)
	}
}

func TestAppendBrowserScreenshotDiffDiagnosticsReportsLowVisualDeltaDecodedScreenshots(t *testing.T) {
	desktop := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 24, 24, func(_ int, _ int) color.RGBA {
		return color.RGBA{R: 31, G: 120, B: 200, A: 255}
	}))
	mobile := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 12, 12, func(_ int, _ int) color.RGBA {
		return color.RGBA{R: 33, G: 122, B: 202, A: 255}
	}))
	if desktop.Fingerprint == "" || desktop.Fingerprint == mobile.Fingerprint {
		t.Fatalf("fingerprints = %q/%q, want different coarse fingerprints", desktop.Fingerprint, mobile.Fingerprint)
	}
	if delta := browserScreenshotVisualDelta(desktop.VisualVector, mobile.VisualVector); delta > 0.035 {
		t.Fatalf("visual delta = %f, want low delta", delta)
	}
	diagnostics := appendBrowserScreenshotDiffDiagnostics(nil, []browserScreenshotProbe{
		{ViewportName: "desktop", Width: 1200, Height: 800, Size: 1000, Hash: "desktop-bytes", PixelStatsDecoded: true, PixelFingerprint: desktop.Fingerprint, PixelVector: desktop.VisualVector},
		{ViewportName: "mobile", Width: 390, Height: 844, Size: 800, Hash: "mobile-bytes", PixelStatsDecoded: true, PixelFingerprint: mobile.Fingerprint, PixelVector: mobile.VisualVector},
	})
	if !hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_low_visual_delta") {
		t.Fatalf("diagnostics = %#v, want viewport_screenshot_low_visual_delta", diagnostics)
	}
}

func TestAppendBrowserScreenshotDiffDiagnosticsReportsLowStructureDeltaDecodedScreenshots(t *testing.T) {
	desktop := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 32, 32, func(x int, y int) color.RGBA {
		if x >= 8 && x < 24 && y >= 8 && y < 24 {
			return color.RGBA{R: 220, G: 40, B: 40, A: 255}
		}
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	}))
	mobile := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 16, 16, func(x int, y int) color.RGBA {
		if x >= 4 && x < 12 && y >= 4 && y < 12 {
			return color.RGBA{R: 40, G: 70, B: 220, A: 255}
		}
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	}))
	if delta := browserScreenshotVisualDelta(desktop.VisualVector, mobile.VisualVector); delta <= 0.035 {
		t.Fatalf("visual delta = %f, want structural path instead of low visual delta", delta)
	}
	structureDelta := browserScreenshotVisualDelta(desktop.StructureVector, mobile.StructureVector)
	if !desktop.StructureMeasured || !mobile.StructureMeasured || structureDelta > 0.03 {
		t.Fatalf("structure measured=%v/%v delta=%f, want low structure delta", desktop.StructureMeasured, mobile.StructureMeasured, structureDelta)
	}
	diagnostics := appendBrowserScreenshotDiffDiagnostics(nil, []browserScreenshotProbe{
		{ViewportName: "desktop", Width: 1200, Height: 800, Size: 1000, Hash: "desktop-bytes", PixelStatsDecoded: true, PixelFingerprint: desktop.Fingerprint, PixelVector: desktop.VisualVector, StructureVector: desktop.StructureVector, StructureMeasured: desktop.StructureMeasured, PerceptualHash: desktop.PerceptualHash, PerceptualHashed: desktop.PerceptualHashed},
		{ViewportName: "mobile", Width: 390, Height: 844, Size: 800, Hash: "mobile-bytes", PixelStatsDecoded: true, PixelFingerprint: mobile.Fingerprint, PixelVector: mobile.VisualVector, StructureVector: mobile.StructureVector, StructureMeasured: mobile.StructureMeasured, PerceptualHash: mobile.PerceptualHash, PerceptualHashed: mobile.PerceptualHashed},
	})
	if !hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_low_structure_delta") {
		t.Fatalf("diagnostics = %#v, want viewport_screenshot_low_structure_delta", diagnostics)
	}
}

func TestAppendBrowserScreenshotDiffDiagnosticsReportsLowTextEdgeDelta(t *testing.T) {
	desktopEdges := []uint8{0, 12, 48, 12, 0, 0, 24, 0}
	mobileEdges := []uint8{0, 13, 47, 13, 0, 0, 24, 0}
	if delta := browserScreenshotVisualDelta(desktopEdges, mobileEdges); delta > 0.02 {
		t.Fatalf("text edge delta = %f, want low edge delta", delta)
	}
	diagnostics := appendBrowserScreenshotDiffDiagnostics(nil, []browserScreenshotProbe{
		{
			ViewportName:      "desktop",
			Width:             1200,
			Height:            800,
			Size:              1000,
			Hash:              "desktop-bytes",
			PixelStatsDecoded: true,
			PixelVector:       []uint8{20, 40, 220},
			TextEdgeVector:    desktopEdges,
			TextEdgeMeasured:  true,
		},
		{
			ViewportName:      "mobile",
			Width:             390,
			Height:            844,
			Size:              800,
			Hash:              "mobile-bytes",
			PixelStatsDecoded: true,
			PixelVector:       []uint8{220, 40, 20},
			TextEdgeVector:    mobileEdges,
			TextEdgeMeasured:  true,
		},
	})
	if !hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_low_text_edge_delta") {
		t.Fatalf("diagnostics = %#v, want viewport_screenshot_low_text_edge_delta", diagnostics)
	}
}

func TestAppendBrowserScreenshotDiffDiagnosticsReportsLowNormalizedPixelDelta(t *testing.T) {
	desktopPixels := []uint8{20, 40, 80, 120, 160, 200}
	mobilePixels := []uint8{21, 41, 80, 119, 161, 199}
	if delta := browserScreenshotVisualDelta(desktopPixels, mobilePixels); delta > 0.025 {
		t.Fatalf("pixel delta = %f, want low normalized pixel delta", delta)
	}
	diagnostics := appendBrowserScreenshotDiffDiagnostics(nil, []browserScreenshotProbe{
		{
			ViewportName:      "desktop",
			Width:             1200,
			Height:            800,
			Size:              1000,
			Hash:              "desktop-bytes",
			PixelStatsDecoded: true,
			PixelVector:       []uint8{20, 40, 220},
			TextEdgeVector:    []uint8{0, 220},
			TextEdgeMeasured:  true,
			PixelDiffVector:   desktopPixels,
			PixelDiffMeasured: true,
		},
		{
			ViewportName:      "mobile",
			Width:             390,
			Height:            844,
			Size:              800,
			Hash:              "mobile-bytes",
			PixelStatsDecoded: true,
			PixelVector:       []uint8{220, 40, 20},
			TextEdgeVector:    []uint8{220, 0},
			TextEdgeMeasured:  true,
			PixelDiffVector:   mobilePixels,
			PixelDiffMeasured: true,
		},
	})
	if !hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_low_pixel_delta") {
		t.Fatalf("diagnostics = %#v, want viewport_screenshot_low_pixel_delta", diagnostics)
	}
}

func TestAppendBrowserScreenshotDiffDiagnosticsReportsLowPerceptualDeltaDecodedScreenshots(t *testing.T) {
	desktop := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 32, 24, func(x int, _ int) color.RGBA {
		v := uint8(x * 255 / 31)
		return color.RGBA{R: v, G: v, B: v, A: 255}
	}))
	mobile := analyzeBrowserScreenshotPixels(encodeTestPNG(t, 16, 24, func(x int, _ int) color.RGBA {
		v := uint8(x * 255 / 15)
		return color.RGBA{R: v, G: 0, B: 0, A: 255}
	}))
	if desktop.Fingerprint == "" || desktop.Fingerprint == mobile.Fingerprint {
		t.Fatalf("fingerprints = %q/%q, want different coarse fingerprints", desktop.Fingerprint, mobile.Fingerprint)
	}
	if delta := browserScreenshotVisualDelta(desktop.VisualVector, mobile.VisualVector); delta <= 0.035 {
		t.Fatalf("visual delta = %f, want perceptual hash path instead of low visual delta", delta)
	}
	if distance := hammingDistance64(desktop.PerceptualHash, mobile.PerceptualHash); distance > 4 {
		t.Fatalf("dHash distance = %d, want low perceptual distance", distance)
	}
	diagnostics := appendBrowserScreenshotDiffDiagnostics(nil, []browserScreenshotProbe{
		{ViewportName: "desktop", Width: 1200, Height: 800, Size: 1000, Hash: "desktop-bytes", PixelStatsDecoded: true, PixelFingerprint: desktop.Fingerprint, PixelVector: desktop.VisualVector, PerceptualHash: desktop.PerceptualHash, PerceptualHashed: desktop.PerceptualHashed},
		{ViewportName: "mobile", Width: 390, Height: 844, Size: 800, Hash: "mobile-bytes", PixelStatsDecoded: true, PixelFingerprint: mobile.Fingerprint, PixelVector: mobile.VisualVector, PerceptualHash: mobile.PerceptualHash, PerceptualHashed: mobile.PerceptualHashed},
	})
	if !hasBrowserCommandDiagnostic(diagnostics, "viewport_screenshot_low_perceptual_delta") {
		t.Fatalf("diagnostics = %#v, want viewport_screenshot_low_perceptual_delta", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsFlatColorScreenshot(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>blank export risk</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	flatPNGPath := filepath.Join(dir, "flat.png")
	if err := os.WriteFile(flatPNGPath, encodeTestPNG(t, 16, 16, func(_ int, _ int) color.RGBA {
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	}), 0o644); err != nil {
		t.Fatalf("write flat png: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_SCREENSHOT_FILE", flatPNGPath)
	browserPath := writeFakeScreenshotFileBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_screenshot_flat_color") {
		t.Fatalf("diagnostics = %#v, want flat color screenshot warning", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsLowDetailScreenshot(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>low detail export risk</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	lowDetailPNGPath := filepath.Join(dir, "low-detail.png")
	if err := os.WriteFile(lowDetailPNGPath, encodeTestPNG(t, 20, 20, func(x int, y int) color.RGBA {
		if x < 2 && y < 2 {
			return color.RGBA{R: 40, G: 40, B: 40, A: 255}
		}
		return color.RGBA{R: 250, G: 250, B: 250, A: 255}
	}), 0o644); err != nil {
		t.Fatalf("write low-detail png: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_SCREENSHOT_FILE", lowDetailPNGPath)
	browserPath := writeFakeScreenshotFileBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_screenshot_low_detail") {
		t.Fatalf("diagnostics = %#v, want low detail screenshot warning", diagnostics)
	}
	if hasBrowserCommandDiagnostic(diagnostics, "browser_screenshot_flat_color") {
		t.Fatalf("diagnostics = %#v, did not expect flat color warning", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsScreenshotSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>wrong viewport screenshot size</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	mismatchPNGPath := filepath.Join(dir, "mismatch.png")
	if err := os.WriteFile(mismatchPNGPath, encodeTestPNG(t, 320, 240, func(x int, y int) color.RGBA {
		return color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 140, A: 255}
	}), 0o644); err != nil {
		t.Fatalf("write mismatch png: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_SCREENSHOT_FILE", mismatchPNGPath)
	browserPath := writeFakeScreenshotFileBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_screenshot_size_mismatch") {
		t.Fatalf("diagnostics = %#v, want screenshot size mismatch warning", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserReportsEdgeContentScreenshot(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>edge content export risk</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	edgePNGPath := filepath.Join(dir, "edge.png")
	if err := os.WriteFile(edgePNGPath, encodeTestPNG(t, 100, 100, func(x int, y int) color.RGBA {
		if x < 8 || x >= 92 || y < 8 || y >= 92 {
			return color.RGBA{R: 15, G: 15, B: 15, A: 255}
		}
		return color.RGBA{R: 250, G: 250, B: 250, A: 255}
	}), 0o644); err != nil {
		t.Fatalf("write edge png: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_SCREENSHOT_FILE", edgePNGPath)
	browserPath := writeFakeScreenshotFileBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 100, Height: 100}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !hasBrowserCommandDiagnostic(diagnostics, "browser_screenshot_edge_content") {
		t.Fatalf("diagnostics = %#v, want edge content warning", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserKeepsViewportScreenshotQualityDiagnostics(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	html := `<!doctype html><html><body><main>same bad screenshot in multiple viewports</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	flatPNGPath := filepath.Join(dir, "flat.png")
	if err := os.WriteFile(flatPNGPath, encodeTestPNG(t, 320, 240, func(_ int, _ int) color.RGBA {
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	}), 0o644); err != nil {
		t.Fatalf("write flat png: %v", err)
	}
	t.Setenv("WORKMAX_FAKE_BROWSER_SCREENSHOT_FILE", flatPNGPath)
	browserPath := writeFakeScreenshotFileBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath: sourcePath,
		SourceHTML: html,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{
				{Name: "desktop", Width: 1200, Height: 800},
				{Name: "mobile", Width: 390, Height: 844},
			},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if countBrowserCommandDiagnostics(diagnostics, "browser_screenshot_size_mismatch") != 2 {
		t.Fatalf("diagnostics = %#v, want one size mismatch per viewport", diagnostics)
	}
	if countBrowserCommandDiagnostics(diagnostics, "browser_screenshot_flat_color") != 2 {
		t.Fatalf("diagnostics = %#v, want one flat color warning per viewport", diagnostics)
	}
}

func TestAnalyzeBrowserScreenshotPixelsFlagsFlatPNG(t *testing.T) {
	content := encodeTestPNG(t, 16, 16, func(_ int, _ int) color.RGBA {
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	})
	stats := analyzeBrowserScreenshotPixels(content)
	if !stats.Decoded || !stats.FlatColor {
		t.Fatalf("stats = %#v, want decoded flat screenshot", stats)
	}
	if stats.FirstPixelRatio != 1 {
		t.Fatalf("first pixel ratio = %f, want 1", stats.FirstPixelRatio)
	}
}

func TestAnalyzeBrowserScreenshotPixelsFlagsLowDetailPNG(t *testing.T) {
	content := encodeTestPNG(t, 20, 20, func(x int, y int) color.RGBA {
		if x < 2 && y < 2 {
			return color.RGBA{R: 30, G: 30, B: 30, A: 255}
		}
		return color.RGBA{R: 245, G: 245, B: 245, A: 255}
	})
	stats := analyzeBrowserScreenshotPixels(content)
	if !stats.Decoded || !stats.LowDetail {
		t.Fatalf("stats = %#v, want decoded low-detail screenshot", stats)
	}
	if stats.FlatColor {
		t.Fatalf("stats = %#v, did not expect flat screenshot", stats)
	}
	if stats.UniqueColors != 2 {
		t.Fatalf("unique colors = %d, want 2", stats.UniqueColors)
	}
	if stats.DominantColorRatio < 0.98 {
		t.Fatalf("dominant ratio = %f, want >= 0.98", stats.DominantColorRatio)
	}
}

func TestAnalyzeBrowserScreenshotPixelsAllowsVariedPNG(t *testing.T) {
	content := encodeTestPNG(t, 16, 16, func(x int, y int) color.RGBA {
		return color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 120, A: 255}
	})
	stats := analyzeBrowserScreenshotPixels(content)
	if !stats.Decoded {
		t.Fatalf("stats = %#v, want decoded screenshot", stats)
	}
	if stats.FlatColor {
		t.Fatalf("stats = %#v, did not expect flat screenshot", stats)
	}
}

func TestAnalyzeBrowserScreenshotPixelsFlagsEdgeContentPNG(t *testing.T) {
	content := encodeTestPNG(t, 100, 100, func(x int, y int) color.RGBA {
		if x < 8 || x >= 92 || y < 8 || y >= 92 {
			return color.RGBA{R: 20, G: 20, B: 20, A: 255}
		}
		return color.RGBA{R: 245, G: 245, B: 245, A: 255}
	})
	stats := analyzeBrowserScreenshotPixels(content)
	if !stats.Decoded || !stats.EdgeContent {
		t.Fatalf("stats = %#v, want decoded edge-content screenshot", stats)
	}
	if stats.EdgeContentRatio < 0.15 {
		t.Fatalf("edge ratio = %f, want >= 0.15", stats.EdgeContentRatio)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserAllowsExistingLocalResources(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	if err := os.WriteFile(filepath.Join(dir, "hero.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	html := `<!doctype html><html><body><main><img src="./hero.png?cache=1">hello</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath:    sourcePath,
		SourceHTML:    html,
		WorkspaceRoot: dir,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if hasBrowserCommandDiagnostic(diagnostics, "browser_resource_missing") {
		t.Fatalf("diagnostics = %#v, did not expect missing resource", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserAllowsExistingCSSDependencies(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	stylesDir := filepath.Join(dir, "styles")
	imagesDir := filepath.Join(dir, "images")
	if err := os.Mkdir(stylesDir, 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.Mkdir(imagesDir, 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "poster-bg.png"), []byte("png"), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stylesDir, "poster.css"), []byte(`.hero{background-image:url("../images/poster-bg.png")}`), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	html := `<!doctype html><html><head><link rel="stylesheet" href="./styles/poster.css"></head><body><main class="hero">hello</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath:    sourcePath,
		SourceHTML:    html,
		WorkspaceRoot: dir,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if hasBrowserCommandDiagnostic(diagnostics, "browser_resource_missing") {
		t.Fatalf("diagnostics = %#v, did not expect missing nested css resource", diagnostics)
	}
}

func TestBrowserCommandHTMLValidator_ValidateHTMLInBrowserAllowsExistingExpandedAssetReferences(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	stylesDir := filepath.Join(dir, "styles")
	imagesDir := filepath.Join(dir, "images")
	if err := os.Mkdir(stylesDir, 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.Mkdir(imagesDir, 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	for rel, body := range map[string]string{
		"preload-1x.png":   "preload1",
		"preload-2x.png":   "preload2",
		"demo.mp4":         "mp4",
		"demo-poster.jpg":  "poster",
		"captions.vtt":     "WEBVTT\n",
		"images/bg.png":    "bg1",
		"images/bg@2x.png": "bg2",
	} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := os.WriteFile(filepath.Join(stylesDir, "banner.css"), []byte(`.hero{background-image:image-set("../images/bg.png" 1x, "../images/bg@2x.png" 2x)}`), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	html := `<!doctype html><html><head><link rel="stylesheet" href="./styles/banner.css"><link rel="preload" as="image" imagesrcset="./preload-1x.png 1x, ./preload-2x.png 2x"></head><body><main class="hero"><video src="./demo.mp4" poster="./demo-poster.jpg" controls><track kind="captions" src="./captions.vtt"></video>hello</main></body></html>`
	if err := os.WriteFile(sourcePath, []byte(html), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	validator := &BrowserCommandHTMLValidator{BrowserPath: browserPath, MinBytes: 4}
	diagnostics, err := validator.ValidateHTMLInBrowser(context.Background(), HTMLBrowserValidationInput{
		SourcePath:    sourcePath,
		SourceHTML:    html,
		WorkspaceRoot: dir,
		Plan: ArtifactHTMLBrowserValidationPlan{
			Viewports: []ArtifactHTMLBrowserValidationViewport{{Name: "desktop", Width: 1200, Height: 800}},
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if hasBrowserCommandDiagnostic(diagnostics, "browser_resource_missing") {
		t.Fatalf("diagnostics = %#v, did not expect missing expanded asset resource", diagnostics)
	}
}

func encodeTestPNG(t *testing.T, width int, height int, pixel func(x int, y int) color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetRGBA(x, y, pixel(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func writeFakeScreenshotFileBrowserCommand(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-browser-file-screenshot")
	script := `#!/bin/sh
out=""
dump_dom=""
for arg in "$@"; do
  case "$arg" in
    --screenshot=*) out="${arg#--screenshot=}" ;;
    --dump-dom) dump_dom="1" ;;
  esac
done
if [ -n "$out" ]; then
  cp "$WORKMAX_FAKE_BROWSER_SCREENSHOT_FILE" "$out"
fi
if [ -n "$dump_dom" ]; then
  printf '%s\n' 'WORKMAX_LAYOUT_PROBE_START{"textOverflow":false,"scrollBounds":false}WORKMAX_LAYOUT_PROBE_END'
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake screenshot browser: %v", err)
	}
	return path
}

func hasBrowserCommandDiagnostic(diagnostics []ArtifactHTMLValidationIssue, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func countBrowserCommandDiagnostics(diagnostics []ArtifactHTMLValidationIssue, code string) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			count++
		}
	}
	return count
}

func browserCommandDiagnosticsContain(diagnostics []ArtifactHTMLValidationIssue, needle string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, needle) {
			return true
		}
	}
	return false
}
