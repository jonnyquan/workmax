package workagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type BrowserCommandHTMLValidator struct {
	BrowserPath string
	MinBytes    int
}

func NewBrowserCommandHTMLValidator(browserPath string) *BrowserCommandHTMLValidator {
	return &BrowserCommandHTMLValidator{BrowserPath: browserPath}
}

func NewDefaultBrowserCommandHTMLValidator() (*BrowserCommandHTMLValidator, error) {
	path, err := resolveBrowserCommandPath("")
	if err != nil {
		return nil, err
	}
	return NewBrowserCommandHTMLValidator(path), nil
}

func (v *BrowserCommandHTMLValidator) ValidateHTMLInBrowser(ctx context.Context, input HTMLBrowserValidationInput) ([]ArtifactHTMLValidationIssue, error) {
	if v == nil {
		return nil, fmt.Errorf("browser command validator: nil validator")
	}
	if strings.TrimSpace(input.SourcePath) == "" {
		return nil, fmt.Errorf("browser command validator: source path is required")
	}
	minBytes := v.MinBytes
	if minBytes <= 0 {
		minBytes = 256
	}
	viewports := input.Plan.Viewports
	if len(viewports) == 0 {
		viewports = defaultHTMLBrowserValidationViewports()
	}
	diagnostics := make([]ArtifactHTMLValidationIssue, 0, len(viewports))
	diagnostics = append(diagnostics, browserLayoutDiagnosticsFromStaticResult(input.StaticResult)...)
	diagnostics = append(diagnostics, validateLocalBrowserResources(input)...)
	screenshots := make([]browserScreenshotProbe, 0, len(viewports))
	for _, viewport := range viewports {
		probe, err := v.probeBrowserLayout(ctx, input, viewport)
		if err != nil {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_layout_probe_failed",
				Severity: "warn",
				Message:  fmt.Sprintf("%s viewport layout probe failed: %v", viewport.Name, err),
				Source:   "browser_validation",
			})
		} else {
			diagnostics = appendBrowserLayoutProbeDiagnostics(diagnostics, viewport.Name, probe)
		}
		outputPath := filepath.Join(os.TempDir(), fmt.Sprintf("workmax-html-browser-validation-%d-%s.png", input.Artifact.Id, normalizeDiagnosticToken(viewport.Name, "viewport")))
		renderer := &BrowserCommandStaticRenderer{
			BrowserPath:    v.BrowserPath,
			ViewportWidth:  viewport.Width,
			ViewportHeight: viewport.Height,
		}
		out, err := renderer.RenderStaticHTML(ctx, HTMLStaticRenderInput{
			Artifact:       input.Artifact,
			SourceFile:     input.SourceFile,
			SourcePath:     input.SourcePath,
			SourceHTML:     input.SourceHTML,
			Target:         "png",
			WorkspaceRoot:  input.WorkspaceRoot,
			OutputFileName: filepath.Base(outputPath),
			OutputFileAbs:  outputPath,
		})
		_ = os.Remove(outputPath)
		if err != nil {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_screenshot_failed",
				Severity: "error",
				Message:  fmt.Sprintf("%s viewport screenshot failed: %v", viewport.Name, err),
				Source:   "browser_validation",
			})
			continue
		}
		if len(out.Content) < minBytes {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_screenshot_blank",
				Severity: "error",
				Message:  fmt.Sprintf("%s viewport screenshot is too small (%d bytes)", viewport.Name, len(out.Content)),
				Source:   "browser_validation",
			})
			continue
		}
		pixelStats := analyzeBrowserScreenshotPixels(out.Content)
		if screenshotSizeMismatch(pixelStats, viewport) {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_screenshot_size_mismatch",
				Severity: "warn",
				Message:  fmt.Sprintf("%s viewport screenshot size is %dx%d, expected about %dx%d", viewport.Name, pixelStats.Width, pixelStats.Height, viewport.Width, viewport.Height),
				Source:   "browser_validation",
			})
		}
		if pixelStats.Decoded && pixelStats.FlatColor {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_screenshot_flat_color",
				Severity: "warn",
				Message:  fmt.Sprintf("%s viewport screenshot is visually flat (%.1f%% pixels match the first pixel)", viewport.Name, pixelStats.FirstPixelRatio*100),
				Source:   "browser_validation",
			})
		}
		if pixelStats.Decoded && pixelStats.LowDetail && !pixelStats.FlatColor {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_screenshot_low_detail",
				Severity: "warn",
				Message:  fmt.Sprintf("%s viewport screenshot has low visual detail (%d unique colors, %.1f%% dominant color)", viewport.Name, pixelStats.UniqueColors, pixelStats.DominantColorRatio*100),
				Source:   "browser_validation",
			})
		}
		if pixelStats.Decoded && pixelStats.EdgeContent {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_screenshot_edge_content",
				Severity: "warn",
				Message:  fmt.Sprintf("%s viewport screenshot has %.1f%% non-background pixels on the outer edge; inspect for clipped or crowded content before export", viewport.Name, pixelStats.EdgeContentRatio*100),
				Source:   "browser_validation",
			})
		}
		screenshots = append(screenshots, browserScreenshotProbe{
			ViewportName:      viewport.Name,
			Width:             viewport.Width,
			Height:            viewport.Height,
			Size:              len(out.Content),
			Hash:              fmt.Sprintf("%x", sha256.Sum256(out.Content)),
			PixelFingerprint:  pixelStats.Fingerprint,
			PixelVector:       pixelStats.VisualVector,
			StructureVector:   pixelStats.StructureVector,
			StructureMeasured: pixelStats.StructureMeasured,
			TextEdgeVector:    pixelStats.TextEdgeVector,
			TextEdgeMeasured:  pixelStats.TextEdgeMeasured,
			PixelDiffVector:   pixelStats.PixelDiffVector,
			PixelDiffMeasured: pixelStats.PixelDiffMeasured,
			PerceptualHash:    pixelStats.PerceptualHash,
			PerceptualHashed:  pixelStats.PerceptualHashed,
			PixelStatsDecoded: pixelStats.Decoded,
		})
	}
	diagnostics = appendBrowserScreenshotDiffDiagnostics(diagnostics, screenshots)
	return diagnostics, nil
}

type browserScreenshotProbe struct {
	ViewportName      string
	Width             int
	Height            int
	Size              int
	Hash              string
	PixelFingerprint  string
	PixelVector       []uint8
	StructureVector   []uint8
	StructureMeasured bool
	TextEdgeVector    []uint8
	TextEdgeMeasured  bool
	PixelDiffVector   []uint8
	PixelDiffMeasured bool
	PerceptualHash    uint64
	PerceptualHashed  bool
	PixelStatsDecoded bool
}

type browserScreenshotPixelStats struct {
	Decoded            bool
	FlatColor          bool
	LowDetail          bool
	EdgeContent        bool
	FirstPixelRatio    float64
	DominantColorRatio float64
	EdgeContentRatio   float64
	UniqueColors       int
	Fingerprint        string
	VisualVector       []uint8
	StructureVector    []uint8
	StructureMeasured  bool
	TextEdgeVector     []uint8
	TextEdgeMeasured   bool
	PixelDiffVector    []uint8
	PixelDiffMeasured  bool
	PerceptualHash     uint64
	PerceptualHashed   bool
	Width              int
	Height             int
}

type browserLayoutProbeResult struct {
	TextOverflow      bool     `json:"textOverflow"`
	ScrollBounds      bool     `json:"scrollBounds"`
	FontLoadRisk      bool     `json:"fontLoadRisk"`
	ResourceLoadError bool     `json:"resourceLoadError"`
	ResourceErrors    []string `json:"resourceErrors"`
	ConsoleErrors     []string `json:"consoleErrors"`
	ConsoleWarnings   []string `json:"consoleWarnings"`
	RuntimeErrors     []string `json:"runtimeErrors"`
	ViewportWidth     int      `json:"viewportWidth"`
	ViewportHeight    int      `json:"viewportHeight"`
	DocumentWidth     int      `json:"documentWidth"`
	DocumentHeight    int      `json:"documentHeight"`
	ContentLeft       int      `json:"contentLeft"`
	ContentTop        int      `json:"contentTop"`
	ContentRight      int      `json:"contentRight"`
	ContentBottom     int      `json:"contentBottom"`
}

func (v *BrowserCommandHTMLValidator) probeBrowserLayout(ctx context.Context, input HTMLBrowserValidationInput, viewport ArtifactHTMLBrowserValidationViewport) (browserLayoutProbeResult, error) {
	if strings.TrimSpace(input.SourcePath) == "" {
		return browserLayoutProbeResult{}, fmt.Errorf("source path is required")
	}
	browserPath, err := resolveBrowserCommandPath(v.BrowserPath)
	if err != nil {
		return browserLayoutProbeResult{}, err
	}
	probeHTML := injectBrowserLayoutProbe(input.SourceHTML)
	if strings.TrimSpace(probeHTML) == "" {
		bytes, err := os.ReadFile(input.SourcePath)
		if err != nil {
			return browserLayoutProbeResult{}, err
		}
		probeHTML = injectBrowserLayoutProbe(string(bytes))
	}
	probeFile, err := os.CreateTemp(filepath.Dir(input.SourcePath), ".workmax-layout-probe-*.html")
	if err != nil {
		return browserLayoutProbeResult{}, err
	}
	probePath := probeFile.Name()
	defer os.Remove(probePath)
	if _, err := probeFile.WriteString(probeHTML); err != nil {
		_ = probeFile.Close()
		return browserLayoutProbeResult{}, err
	}
	if err := probeFile.Close(); err != nil {
		return browserLayoutProbeResult{}, err
	}
	width := viewport.Width
	if width <= 0 {
		width = 1440
	}
	height := viewport.Height
	if height <= 0 {
		height = 900
	}
	runCtx, cancel := context.WithTimeout(ctx, defaultStaticRenderTimeout)
	defer cancel()
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--no-default-browser-check",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--dump-dom",
		fileURL(probePath),
	}
	cmd := exec.CommandContext(runCtx, browserPath, args...)
	cmd.Dir = filepath.Dir(input.SourcePath)
	output, err := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return browserLayoutProbeResult{}, fmt.Errorf("layout probe timed out")
	}
	if err != nil {
		return browserLayoutProbeResult{}, fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseBrowserLayoutProbeOutput(string(output))
}

func injectBrowserLayoutProbe(source string) string {
	body := strings.TrimSpace(source)
	if body == "" {
		return ""
	}
	bootstrap := `<script>
(function(){
  if (window.__workmaxRuntimeDiagnostics) return;
  var diagnostics = {consoleErrors:[],consoleWarnings:[],runtimeErrors:[],resourceErrors:[]};
  window.__workmaxRuntimeDiagnostics = diagnostics;
  function textFromArgs(args){
    return Array.prototype.map.call(args, function(arg){
      if (arg instanceof Error) return arg.message || String(arg);
      if (typeof arg === 'string') return arg;
      try { return JSON.stringify(arg); } catch (_) { return String(arg); }
    }).join(' ');
  }
  function push(list, message){
    message = String(message || '').trim();
    if (!message || list.length >= 5) return;
    list.push(message.slice(0, 240));
  }
  function resourceURL(target){
    if (!target || target === window) return '';
    return target.currentSrc || target.src || target.href || target.getAttribute && (target.getAttribute('src') || target.getAttribute('href') || target.getAttribute('srcset')) || '';
  }
  var originalError = console.error;
  console.error = function(){
    push(diagnostics.consoleErrors, textFromArgs(arguments));
    if (originalError && originalError.apply) return originalError.apply(console, arguments);
  };
  var originalWarn = console.warn;
  console.warn = function(){
    push(diagnostics.consoleWarnings, textFromArgs(arguments));
    if (originalWarn && originalWarn.apply) return originalWarn.apply(console, arguments);
  };
  window.addEventListener('error', function(event){
    var url = resourceURL(event && event.target);
    if (url) {
      push(diagnostics.resourceErrors, url);
      return;
    }
    push(diagnostics.runtimeErrors, event && (event.message || (event.error && event.error.message) || 'runtime error'));
  }, true);
  window.addEventListener('unhandledrejection', function(event){
    var reason = event && event.reason;
    push(diagnostics.runtimeErrors, reason && (reason.message || String(reason)) || 'unhandled rejection');
  }, true);
})();
</script>`
	probe := `<script>
(function(){
  function visible(el){
    var style = window.getComputedStyle(el);
    if (!style || style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity) === 0) return false;
    var rect = el.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  }
  var textOverflow = false;
  var scrollBounds = document.documentElement.scrollWidth > window.innerWidth + 1 || document.documentElement.scrollHeight > window.innerHeight + 1 || document.body.scrollWidth > window.innerWidth + 1 || document.body.scrollHeight > window.innerHeight + 1;
  var fontLoadRisk = false;
  if (document.fonts) {
    if (document.fonts.status && document.fonts.status !== 'loaded') fontLoadRisk = true;
    document.fonts.forEach(function(face){
      if (face && (face.status === 'error' || face.status === 'unloaded' || face.status === 'loading')) fontLoadRisk = true;
    });
  }
  var resourceErrors = [];
  Array.prototype.forEach.call(document.images || [], function(img){
    var src = img.currentSrc || img.src || img.getAttribute('src') || '';
    if (!src) return;
    if (img.complete && img.naturalWidth === 0 && img.naturalHeight === 0) resourceErrors.push(src);
  });
  Array.prototype.forEach.call(document.querySelectorAll ? document.querySelectorAll('source[srcset]') : [], function(source){
    var srcset = source.getAttribute('srcset') || '';
    if (srcset && source.parentElement && source.parentElement.tagName && source.parentElement.tagName.toLowerCase() === 'picture') {
      var img = source.parentElement.querySelector('img');
      if (img && img.complete && img.naturalWidth === 0 && img.naturalHeight === 0) resourceErrors.push(srcset);
    }
  });
  Array.prototype.forEach.call(document.body ? document.body.querySelectorAll('*') : [], function(el){
    if (!visible(el)) return;
    if ((el.scrollWidth > el.clientWidth + 1 || el.scrollHeight > el.clientHeight + 1) && (el.textContent || '').trim().length > 0) textOverflow = true;
    var rect = el.getBoundingClientRect();
    if (rect.left < -1 || rect.top < -1 || rect.right > window.innerWidth + 1 || rect.bottom > window.innerHeight + 1) scrollBounds = true;
  });
  var viewportWidth = Math.ceil(window.innerWidth || 0);
  var viewportHeight = Math.ceil(window.innerHeight || 0);
  var minLeft = Infinity;
  var minTop = Infinity;
  var maxRight = 0;
  var maxBottom = 0;
  var visibleCount = 0;
  Array.prototype.forEach.call(document.body ? document.body.querySelectorAll('*') : [], function(el){
    if (!visible(el)) return;
    var rect = el.getBoundingClientRect();
    visibleCount++;
    minLeft = Math.min(minLeft, rect.left);
    minTop = Math.min(minTop, rect.top);
    maxRight = Math.max(maxRight, rect.right);
    maxBottom = Math.max(maxBottom, rect.bottom);
  });
  var contentLeft = visibleCount ? Math.floor(minLeft) : 0;
  var contentTop = visibleCount ? Math.floor(minTop) : 0;
  var contentRight = visibleCount ? Math.ceil(maxRight) : 0;
  var contentBottom = visibleCount ? Math.ceil(maxBottom) : 0;
  var documentWidth = Math.ceil(Math.max(document.documentElement ? document.documentElement.scrollWidth || 0 : 0, document.body ? document.body.scrollWidth || 0 : 0, contentRight));
  var documentHeight = Math.ceil(Math.max(document.documentElement ? document.documentElement.scrollHeight || 0 : 0, document.body ? document.body.scrollHeight || 0 : 0, contentBottom));
  var runtimeDiagnostics = window.__workmaxRuntimeDiagnostics || {};
  var allResourceErrors = resourceErrors.concat(runtimeDiagnostics.resourceErrors || []);
  document.body.textContent = 'WORKMAX_LAYOUT_PROBE_START' + JSON.stringify({textOverflow:textOverflow,scrollBounds:scrollBounds,fontLoadRisk:fontLoadRisk,resourceLoadError:allResourceErrors.length>0,resourceErrors:allResourceErrors.slice(0,5),consoleErrors:(runtimeDiagnostics.consoleErrors||[]).slice(0,5),consoleWarnings:(runtimeDiagnostics.consoleWarnings||[]).slice(0,5),runtimeErrors:(runtimeDiagnostics.runtimeErrors||[]).slice(0,5),viewportWidth:viewportWidth,viewportHeight:viewportHeight,documentWidth:documentWidth,documentHeight:documentHeight,contentLeft:contentLeft,contentTop:contentTop,contentRight:contentRight,contentBottom:contentBottom}) + 'WORKMAX_LAYOUT_PROBE_END';
})();
</script>`
	lower := strings.ToLower(body)
	if idx := strings.Index(lower, "<head"); idx >= 0 {
		if endIdx := strings.Index(body[idx:], ">"); endIdx >= 0 {
			insertAt := idx + endIdx + 1
			body = body[:insertAt] + bootstrap + body[insertAt:]
			lower = strings.ToLower(body)
		}
	} else if idx := strings.Index(lower, "<html"); idx >= 0 {
		if endIdx := strings.Index(body[idx:], ">"); endIdx >= 0 {
			insertAt := idx + endIdx + 1
			body = body[:insertAt] + bootstrap + body[insertAt:]
			lower = strings.ToLower(body)
		}
	} else if idx := strings.Index(lower, "<body"); idx >= 0 {
		body = body[:idx] + bootstrap + body[idx:]
		lower = strings.ToLower(body)
	} else {
		body = bootstrap + body
		lower = strings.ToLower(body)
	}
	if idx := strings.LastIndex(lower, "</body>"); idx >= 0 {
		return body[:idx] + probe + body[idx:]
	}
	return body + probe
}

func parseBrowserLayoutProbeOutput(output string) (browserLayoutProbeResult, error) {
	const start = "WORKMAX_LAYOUT_PROBE_START"
	const end = "WORKMAX_LAYOUT_PROBE_END"
	startIdx := strings.Index(output, start)
	if startIdx < 0 {
		return browserLayoutProbeResult{}, fmt.Errorf("probe marker missing")
	}
	startIdx += len(start)
	endIdx := strings.Index(output[startIdx:], end)
	if endIdx < 0 {
		return browserLayoutProbeResult{}, fmt.Errorf("probe marker end missing")
	}
	raw := strings.TrimSpace(output[startIdx : startIdx+endIdx])
	var result browserLayoutProbeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return browserLayoutProbeResult{}, err
	}
	return result, nil
}

func appendBrowserLayoutProbeDiagnostics(diagnostics []ArtifactHTMLValidationIssue, viewportName string, result browserLayoutProbeResult) []ArtifactHTMLValidationIssue {
	if result.TextOverflow && !hasHTMLValidationDiagnosticCode(diagnostics, "text_overflow") {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "text_overflow",
			Severity: "warn",
			Message:  fmt.Sprintf("%s viewport has text content whose rendered box overflows", viewportName),
			Source:   "browser_validation",
		})
	}
	if result.ScrollBounds && !hasHTMLValidationDiagnosticCode(diagnostics, "scroll_bounds") {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "scroll_bounds",
			Severity: "warn",
			Message:  fmt.Sprintf("%s viewport has content outside the export viewport or document scroll bounds", viewportName),
			Source:   "browser_validation",
		})
	}
	if browserLayoutProbeHasDOMBoundsRisk(result) && !hasHTMLValidationDiagnosticCode(diagnostics, "browser_dom_bounds") {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "browser_dom_bounds",
			Severity: "warn",
			Message:  fmt.Sprintf("%s viewport measured DOM bounds document=%dx%d content=(%d,%d)-(%d,%d) viewport=%dx%d; inspect for crop/overflow before export", viewportName, result.DocumentWidth, result.DocumentHeight, result.ContentLeft, result.ContentTop, result.ContentRight, result.ContentBottom, result.ViewportWidth, result.ViewportHeight),
			Source:   "browser_validation",
		})
	}
	if result.FontLoadRisk && !hasHTMLValidationDiagnosticCode(diagnostics, "font_load_risk") {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "font_load_risk",
			Severity: "warn",
			Message:  fmt.Sprintf("%s viewport has web fonts that are not fully loaded or failed during browser validation", viewportName),
			Source:   "browser_validation",
		})
	}
	if result.ResourceLoadError && !hasHTMLValidationDiagnosticCode(diagnostics, "browser_resource_missing") {
		resource := "one or more image resources"
		if len(result.ResourceErrors) > 0 && strings.TrimSpace(result.ResourceErrors[0]) != "" {
			resource = strings.TrimSpace(result.ResourceErrors[0])
		}
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "browser_resource_missing",
			Severity: "error",
			Message:  fmt.Sprintf("%s viewport failed to load %s during browser validation", viewportName, resource),
			Source:   "browser_validation",
		})
	}
	if len(result.RuntimeErrors) > 0 && !hasHTMLValidationDiagnosticCode(diagnostics, "browser_runtime_error") {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "browser_runtime_error",
			Severity: "error",
			Message:  fmt.Sprintf("%s viewport hit a runtime error during browser validation: %s", viewportName, firstBrowserProbeMessage(result.RuntimeErrors)),
			Source:   "browser_validation",
		})
	}
	if len(result.ConsoleErrors) > 0 && !hasHTMLValidationDiagnosticCode(diagnostics, "browser_console_error") {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "browser_console_error",
			Severity: "error",
			Message:  fmt.Sprintf("%s viewport emitted console.error during browser validation: %s", viewportName, firstBrowserProbeMessage(result.ConsoleErrors)),
			Source:   "browser_validation",
		})
	}
	if len(result.ConsoleWarnings) > 0 && !hasHTMLValidationDiagnosticCode(diagnostics, "browser_console_warn") {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "browser_console_warn",
			Severity: "warn",
			Message:  fmt.Sprintf("%s viewport emitted console.warn during browser validation: %s", viewportName, firstBrowserProbeMessage(result.ConsoleWarnings)),
			Source:   "browser_validation",
		})
	}
	return diagnostics
}

func browserLayoutProbeHasDOMBoundsRisk(result browserLayoutProbeResult) bool {
	if result.ViewportWidth <= 0 || result.ViewportHeight <= 0 {
		return false
	}
	allowedWidth := result.ViewportWidth + maxInt(8, result.ViewportWidth/20)
	allowedHeight := result.ViewportHeight + maxInt(8, result.ViewportHeight/20)
	if result.DocumentWidth > allowedWidth || result.DocumentHeight > allowedHeight {
		return true
	}
	if result.ContentLeft < -8 || result.ContentTop < -8 {
		return true
	}
	if result.ContentRight > allowedWidth || result.ContentBottom > allowedHeight {
		return true
	}
	return false
}

func firstBrowserProbeMessage(messages []string) string {
	for _, message := range messages {
		if trimmed := strings.TrimSpace(message); trimmed != "" {
			return trimmed
		}
	}
	return "unknown"
}

func appendBrowserScreenshotDiffDiagnostics(diagnostics []ArtifactHTMLValidationIssue, screenshots []browserScreenshotProbe) []ArtifactHTMLValidationIssue {
	if len(screenshots) < 2 || hasHTMLValidationDiagnosticCode(diagnostics, "viewport_screenshot_identical") {
		return diagnostics
	}
	distinctViewportSizes := map[string]bool{}
	signatures := map[string][]browserScreenshotProbe{}
	fingerprints := map[string][]browserScreenshotProbe{}
	for _, screenshot := range screenshots {
		if screenshot.Width <= 0 || screenshot.Height <= 0 || screenshot.Hash == "" {
			continue
		}
		distinctViewportSizes[fmt.Sprintf("%dx%d", screenshot.Width, screenshot.Height)] = true
		signature := fmt.Sprintf("%d:%s", screenshot.Size, screenshot.Hash)
		signatures[signature] = append(signatures[signature], screenshot)
		if screenshot.PixelStatsDecoded && screenshot.PixelFingerprint != "" {
			fingerprints[screenshot.PixelFingerprint] = append(fingerprints[screenshot.PixelFingerprint], screenshot)
		}
	}
	if len(distinctViewportSizes) < 2 {
		return diagnostics
	}
	for _, group := range signatures {
		if len(group) < 2 {
			continue
		}
		if !hasDistinctScreenshotViewportSizes(group) {
			continue
		}
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "viewport_screenshot_identical",
			Severity: "warn",
			Message:  fmt.Sprintf("Browser validation produced identical screenshots for different viewport sizes (%s); inspect responsive layout before export", formatScreenshotViewportNames(group)),
			Source:   "browser_validation",
		})
		return diagnostics
	}
	if hasHTMLValidationDiagnosticCode(diagnostics, "viewport_screenshot_near_identical") {
		return diagnostics
	}
	for _, group := range fingerprints {
		if len(group) < 2 {
			continue
		}
		if !hasDistinctScreenshotViewportSizes(group) || allScreenshotHashesEqual(group) {
			continue
		}
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "viewport_screenshot_near_identical",
			Severity: "warn",
			Message:  fmt.Sprintf("Browser validation produced visually similar screenshots for different viewport sizes (%s); inspect responsive layout before export", formatScreenshotViewportNames(group)),
			Source:   "browser_validation",
		})
		return diagnostics
	}
	if hasHTMLValidationDiagnosticCode(diagnostics, "viewport_screenshot_low_visual_delta") {
		return diagnostics
	}
	if group, delta, ok := findLowVisualDeltaScreenshotGroup(screenshots); ok {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "viewport_screenshot_low_visual_delta",
			Severity: "warn",
			Message:  fmt.Sprintf("Browser validation produced low visual delta across different viewport sizes (%s, %.2f%% average color delta); inspect responsive layout before export", formatScreenshotViewportNames(group), delta*100),
			Source:   "browser_validation",
		})
		return diagnostics
	}
	if group, delta, ok := findLowStructureDeltaScreenshotGroup(screenshots); ok {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "viewport_screenshot_low_structure_delta",
			Severity: "warn",
			Message:  fmt.Sprintf("Browser validation produced low structural delta across different viewport sizes (%s, %.2f%% content occupancy delta); inspect responsive composition before export", formatScreenshotViewportNames(group), delta*100),
			Source:   "browser_validation",
		})
		return diagnostics
	}
	if group, delta, ok := findLowTextEdgeDeltaScreenshotGroup(screenshots); ok {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "viewport_screenshot_low_text_edge_delta",
			Severity: "warn",
			Message:  fmt.Sprintf("Browser validation produced low text/glyph edge delta across different viewport sizes (%s, %.2f%% edge-density delta); inspect font rendering and responsive text scale before export", formatScreenshotViewportNames(group), delta*100),
			Source:   "browser_validation",
		})
		return diagnostics
	}
	if group, delta, ok := findLowPixelDiffScreenshotGroup(screenshots); ok {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "viewport_screenshot_low_pixel_delta",
			Severity: "warn",
			Message:  fmt.Sprintf("Browser validation produced low normalized pixel delta across different viewport sizes (%s, %.2f%% luminance delta); inspect responsive layout and perceptual differences before export", formatScreenshotViewportNames(group), delta*100),
			Source:   "browser_validation",
		})
		return diagnostics
	}
	if group, distance, ok := findLowPerceptualDeltaScreenshotGroup(screenshots); ok {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "viewport_screenshot_low_perceptual_delta",
			Severity: "warn",
			Message:  fmt.Sprintf("Browser validation produced low perceptual delta across different viewport sizes (%s, dHash distance %d); inspect responsive structure before export", formatScreenshotViewportNames(group), distance),
			Source:   "browser_validation",
		})
		return diagnostics
	}
	return diagnostics
}

func analyzeBrowserScreenshotPixels(content []byte) browserScreenshotPixelStats {
	img, _, err := image.Decode(bytes.NewReader(content))
	if err != nil || img == nil {
		return browserScreenshotPixelStats{}
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	total := width * height
	if width <= 0 || height <= 0 || total <= 0 {
		return browserScreenshotPixelStats{Decoded: true, Width: width, Height: height}
	}
	first := colorKey(img.At(bounds.Min.X, bounds.Min.Y))
	matching := 0
	colorCounts := map[uint64]int{}
	dominant := 0
	var dominantKey uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			key := colorKey(img.At(x, y))
			if key == first {
				matching++
			}
			colorCounts[key]++
			if colorCounts[key] > dominant {
				dominant = colorCounts[key]
				dominantKey = key
			}
		}
	}
	ratio := float64(matching) / float64(total)
	dominantRatio := float64(dominant) / float64(total)
	edgeRatio := browserScreenshotEdgeContentRatio(img, bounds, dominantKey)
	structureVector, structureMeasured := browserScreenshotStructureVector(img, bounds, dominantKey, dominantRatio)
	textEdgeVector, textEdgeMeasured := browserScreenshotTextEdgeVector(img, bounds)
	return browserScreenshotPixelStats{
		Decoded:            true,
		FlatColor:          ratio >= 0.995,
		LowDetail:          len(colorCounts) <= 8 && dominantRatio >= 0.98,
		EdgeContent:        edgeRatio >= 0.15,
		FirstPixelRatio:    ratio,
		DominantColorRatio: dominantRatio,
		EdgeContentRatio:   edgeRatio,
		UniqueColors:       len(colorCounts),
		Fingerprint:        browserScreenshotFingerprint(img, bounds),
		VisualVector:       browserScreenshotVisualVector(img, bounds),
		StructureVector:    structureVector,
		StructureMeasured:  structureMeasured,
		TextEdgeVector:     textEdgeVector,
		TextEdgeMeasured:   textEdgeMeasured,
		PixelDiffVector:    browserScreenshotLuminanceVector(img, bounds, 32, 32),
		PixelDiffMeasured:  true,
		PerceptualHash:     browserScreenshotDHash(img, bounds),
		PerceptualHashed:   true,
		Width:              width,
		Height:             height,
	}
}

func findLowPerceptualDeltaScreenshotGroup(screenshots []browserScreenshotProbe) ([]browserScreenshotProbe, int, bool) {
	const threshold = 4
	var bestGroup []browserScreenshotProbe
	bestDistance := 65
	for i := 0; i < len(screenshots); i++ {
		left := screenshots[i]
		if !left.PerceptualHashed {
			continue
		}
		for j := i + 1; j < len(screenshots); j++ {
			right := screenshots[j]
			if !right.PerceptualHashed {
				continue
			}
			group := []browserScreenshotProbe{left, right}
			if !hasDistinctScreenshotViewportSizes(group) || allScreenshotHashesEqual(group) {
				continue
			}
			distance := hammingDistance64(left.PerceptualHash, right.PerceptualHash)
			if distance < bestDistance {
				bestDistance = distance
				bestGroup = group
			}
		}
	}
	return bestGroup, bestDistance, len(bestGroup) == 2 && bestDistance <= threshold
}

func findLowStructureDeltaScreenshotGroup(screenshots []browserScreenshotProbe) ([]browserScreenshotProbe, float64, bool) {
	const threshold = 0.03
	var bestGroup []browserScreenshotProbe
	bestDelta := 1.0
	for i := 0; i < len(screenshots); i++ {
		left := screenshots[i]
		if !left.StructureMeasured || len(left.StructureVector) == 0 {
			continue
		}
		for j := i + 1; j < len(screenshots); j++ {
			right := screenshots[j]
			if !right.StructureMeasured || len(right.StructureVector) == 0 {
				continue
			}
			group := []browserScreenshotProbe{left, right}
			if !hasDistinctScreenshotViewportSizes(group) || allScreenshotHashesEqual(group) {
				continue
			}
			delta := browserScreenshotVisualDelta(left.StructureVector, right.StructureVector)
			if delta < bestDelta {
				bestDelta = delta
				bestGroup = group
			}
		}
	}
	return bestGroup, bestDelta, len(bestGroup) == 2 && bestDelta <= threshold
}

func findLowTextEdgeDeltaScreenshotGroup(screenshots []browserScreenshotProbe) ([]browserScreenshotProbe, float64, bool) {
	const threshold = 0.02
	var bestGroup []browserScreenshotProbe
	bestDelta := 1.0
	for i := 0; i < len(screenshots); i++ {
		left := screenshots[i]
		if !left.TextEdgeMeasured || len(left.TextEdgeVector) == 0 {
			continue
		}
		for j := i + 1; j < len(screenshots); j++ {
			right := screenshots[j]
			if !right.TextEdgeMeasured || len(right.TextEdgeVector) == 0 {
				continue
			}
			group := []browserScreenshotProbe{left, right}
			if !hasDistinctScreenshotViewportSizes(group) || allScreenshotHashesEqual(group) {
				continue
			}
			delta := browserScreenshotVisualDelta(left.TextEdgeVector, right.TextEdgeVector)
			if delta < bestDelta {
				bestDelta = delta
				bestGroup = group
			}
		}
	}
	return bestGroup, bestDelta, len(bestGroup) == 2 && bestDelta <= threshold
}

func findLowPixelDiffScreenshotGroup(screenshots []browserScreenshotProbe) ([]browserScreenshotProbe, float64, bool) {
	const threshold = 0.025
	var bestGroup []browserScreenshotProbe
	bestDelta := 1.0
	for i := 0; i < len(screenshots); i++ {
		left := screenshots[i]
		if !left.PixelDiffMeasured || len(left.PixelDiffVector) == 0 {
			continue
		}
		for j := i + 1; j < len(screenshots); j++ {
			right := screenshots[j]
			if !right.PixelDiffMeasured || len(right.PixelDiffVector) == 0 {
				continue
			}
			group := []browserScreenshotProbe{left, right}
			if !hasDistinctScreenshotViewportSizes(group) || allScreenshotHashesEqual(group) {
				continue
			}
			delta := browserScreenshotVisualDelta(left.PixelDiffVector, right.PixelDiffVector)
			if delta < bestDelta {
				bestDelta = delta
				bestGroup = group
			}
		}
	}
	return bestGroup, bestDelta, len(bestGroup) == 2 && bestDelta <= threshold
}

func findLowVisualDeltaScreenshotGroup(screenshots []browserScreenshotProbe) ([]browserScreenshotProbe, float64, bool) {
	const threshold = 0.035
	var bestGroup []browserScreenshotProbe
	bestDelta := 1.0
	for i := 0; i < len(screenshots); i++ {
		left := screenshots[i]
		if !left.PixelStatsDecoded || len(left.PixelVector) == 0 {
			continue
		}
		for j := i + 1; j < len(screenshots); j++ {
			right := screenshots[j]
			if !right.PixelStatsDecoded || len(right.PixelVector) == 0 {
				continue
			}
			group := []browserScreenshotProbe{left, right}
			if !hasDistinctScreenshotViewportSizes(group) || allScreenshotHashesEqual(group) {
				continue
			}
			delta := browserScreenshotVisualDelta(left.PixelVector, right.PixelVector)
			if delta < bestDelta {
				bestDelta = delta
				bestGroup = group
			}
		}
	}
	return bestGroup, bestDelta, len(bestGroup) == 2 && bestDelta <= threshold
}

func browserScreenshotEdgeContentRatio(img image.Image, bounds image.Rectangle, backgroundKey uint64) float64 {
	width := bounds.Dx()
	height := bounds.Dy()
	if img == nil || width <= 0 || height <= 0 || backgroundKey == 0 {
		return 0
	}
	band := minInt(width, height) / 50
	if band < 1 {
		band = 1
	}
	edgeTotal := 0
	edgeContent := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if x >= bounds.Min.X+band && x < bounds.Max.X-band && y >= bounds.Min.Y+band && y < bounds.Max.Y-band {
				continue
			}
			edgeTotal++
			if colorKey(img.At(x, y)) != backgroundKey {
				edgeContent++
			}
		}
	}
	if edgeTotal == 0 {
		return 0
	}
	return float64(edgeContent) / float64(edgeTotal)
}

func browserScreenshotFingerprint(img image.Image, bounds image.Rectangle) string {
	const grid = 4
	width := bounds.Dx()
	height := bounds.Dy()
	if img == nil || width <= 0 || height <= 0 {
		return ""
	}
	parts := make([]string, 0, grid*grid)
	for gy := 0; gy < grid; gy++ {
		for gx := 0; gx < grid; gx++ {
			minX := bounds.Min.X + gx*width/grid
			maxX := bounds.Min.X + (gx+1)*width/grid
			minY := bounds.Min.Y + gy*height/grid
			maxY := bounds.Min.Y + (gy+1)*height/grid
			if maxX <= minX {
				maxX = minX + 1
			}
			if maxY <= minY {
				maxY = minY + 1
			}
			var sumR, sumG, sumB, sumA uint64
			var count uint64
			for y := minY; y < maxY && y < bounds.Max.Y; y++ {
				for x := minX; x < maxX && x < bounds.Max.X; x++ {
					r, g, b, a := img.At(x, y).RGBA()
					sumR += uint64(r >> 8)
					sumG += uint64(g >> 8)
					sumB += uint64(b >> 8)
					sumA += uint64(a >> 8)
					count++
				}
			}
			if count == 0 {
				parts = append(parts, "0000")
				continue
			}
			parts = append(parts, fmt.Sprintf("%02x%02x%02x%02x", quantize8(sumR/count), quantize8(sumG/count), quantize8(sumB/count), quantize8(sumA/count)))
		}
	}
	return strings.Join(parts, ".")
}

func browserScreenshotVisualVector(img image.Image, bounds image.Rectangle) []uint8 {
	const grid = 8
	width := bounds.Dx()
	height := bounds.Dy()
	if img == nil || width <= 0 || height <= 0 {
		return nil
	}
	vector := make([]uint8, 0, grid*grid*3)
	for gy := 0; gy < grid; gy++ {
		for gx := 0; gx < grid; gx++ {
			minX := bounds.Min.X + gx*width/grid
			maxX := bounds.Min.X + (gx+1)*width/grid
			minY := bounds.Min.Y + gy*height/grid
			maxY := bounds.Min.Y + (gy+1)*height/grid
			if maxX <= minX {
				maxX = minX + 1
			}
			if maxY <= minY {
				maxY = minY + 1
			}
			var sumR, sumG, sumB uint64
			var count uint64
			for y := minY; y < maxY && y < bounds.Max.Y; y++ {
				for x := minX; x < maxX && x < bounds.Max.X; x++ {
					r, g, b, _ := img.At(x, y).RGBA()
					sumR += uint64(r >> 8)
					sumG += uint64(g >> 8)
					sumB += uint64(b >> 8)
					count++
				}
			}
			if count == 0 {
				vector = append(vector, 0, 0, 0)
				continue
			}
			vector = append(vector, uint8(sumR/count), uint8(sumG/count), uint8(sumB/count))
		}
	}
	return vector
}

func browserScreenshotStructureVector(img image.Image, bounds image.Rectangle, backgroundKey uint64, dominantRatio float64) ([]uint8, bool) {
	const grid = 8
	width := bounds.Dx()
	height := bounds.Dy()
	if img == nil || width <= 0 || height <= 0 || backgroundKey == 0 || dominantRatio < 0.35 {
		return nil, false
	}
	vector := make([]uint8, 0, grid*grid)
	for gy := 0; gy < grid; gy++ {
		for gx := 0; gx < grid; gx++ {
			minX := bounds.Min.X + gx*width/grid
			maxX := bounds.Min.X + (gx+1)*width/grid
			minY := bounds.Min.Y + gy*height/grid
			maxY := bounds.Min.Y + (gy+1)*height/grid
			if maxX <= minX {
				maxX = minX + 1
			}
			if maxY <= minY {
				maxY = minY + 1
			}
			var count, content uint64
			for y := minY; y < maxY && y < bounds.Max.Y; y++ {
				for x := minX; x < maxX && x < bounds.Max.X; x++ {
					count++
					if colorKey(img.At(x, y)) != backgroundKey {
						content++
					}
				}
			}
			if count == 0 {
				vector = append(vector, 0)
				continue
			}
			vector = append(vector, uint8((content*255)/count))
		}
	}
	return vector, true
}

func browserScreenshotTextEdgeVector(img image.Image, bounds image.Rectangle) ([]uint8, bool) {
	const grid = 8
	width := bounds.Dx()
	height := bounds.Dy()
	if img == nil || width < 4 || height < 4 {
		return nil, false
	}
	vector := make([]uint8, 0, grid*grid)
	measuredCells := 0
	for gy := 0; gy < grid; gy++ {
		for gx := 0; gx < grid; gx++ {
			minX := bounds.Min.X + gx*width/grid
			maxX := bounds.Min.X + (gx+1)*width/grid
			minY := bounds.Min.Y + gy*height/grid
			maxY := bounds.Min.Y + (gy+1)*height/grid
			if maxX-minX < 2 || maxY-minY < 2 {
				vector = append(vector, 0)
				continue
			}
			var edges, count uint64
			for y := minY; y < maxY-1 && y+1 < bounds.Max.Y; y++ {
				for x := minX; x < maxX-1 && x+1 < bounds.Max.X; x++ {
					current := browserScreenshotLuminance(img.At(x, y))
					right := browserScreenshotLuminance(img.At(x+1, y))
					down := browserScreenshotLuminance(img.At(x, y+1))
					if absInt(current-right) >= 28 || absInt(current-down) >= 28 {
						edges++
					}
					count++
				}
			}
			if count == 0 {
				vector = append(vector, 0)
				continue
			}
			measuredCells++
			vector = append(vector, uint8((edges*255)/count))
		}
	}
	return vector, measuredCells > 0
}

func browserScreenshotLuminanceVector(img image.Image, bounds image.Rectangle, sampleWidth int, sampleHeight int) []uint8 {
	if img == nil || bounds.Dx() <= 0 || bounds.Dy() <= 0 || sampleWidth <= 0 || sampleHeight <= 0 {
		return nil
	}
	vector := make([]uint8, 0, sampleWidth*sampleHeight)
	for y := 0; y < sampleHeight; y++ {
		for x := 0; x < sampleWidth; x++ {
			vector = append(vector, browserScreenshotLuminanceAt(img, bounds, x, y, sampleWidth, sampleHeight))
		}
	}
	return vector
}

func browserScreenshotVisualDelta(left []uint8, right []uint8) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 1
	}
	total := 0
	for i := range left {
		total += absInt(int(left[i]) - int(right[i]))
	}
	return float64(total) / float64(len(left)*255)
}

func browserScreenshotDHash(img image.Image, bounds image.Rectangle) uint64 {
	const sampleWidth = 9
	const sampleHeight = 8
	width := bounds.Dx()
	height := bounds.Dy()
	if img == nil || width <= 0 || height <= 0 {
		return 0
	}
	var hash uint64
	bit := 0
	for y := 0; y < sampleHeight; y++ {
		for x := 0; x < sampleWidth-1; x++ {
			left := browserScreenshotLuminanceAt(img, bounds, x, y, sampleWidth, sampleHeight)
			right := browserScreenshotLuminanceAt(img, bounds, x+1, y, sampleWidth, sampleHeight)
			if left > right {
				hash |= uint64(1) << bit
			}
			bit++
		}
	}
	return hash
}

func browserScreenshotLuminanceAt(img image.Image, bounds image.Rectangle, gridX int, gridY int, gridWidth int, gridHeight int) uint8 {
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 || gridWidth <= 0 || gridHeight <= 0 {
		return 0
	}
	x := bounds.Min.X + gridX*width/(gridWidth-1)
	y := bounds.Min.Y + gridY*height/(gridHeight-1)
	if x >= bounds.Max.X {
		x = bounds.Max.X - 1
	}
	if y >= bounds.Max.Y {
		y = bounds.Max.Y - 1
	}
	return uint8(browserScreenshotLuminance(img.At(x, y)))
}

func browserScreenshotLuminance(c color.Color) int {
	r, g, b, _ := c.RGBA()
	rr := uint32(r >> 8)
	gg := uint32(g >> 8)
	bb := uint32(b >> 8)
	return int((299*rr + 587*gg + 114*bb) / 1000)
}

func hammingDistance64(left uint64, right uint64) int {
	value := left ^ right
	count := 0
	for value != 0 {
		count++
		value &= value - 1
	}
	return count
}

func quantize8(value uint64) uint64 {
	return (value / 32) * 32
}

func colorKey(c color.Color) uint64 {
	r, g, b, a := c.RGBA()
	return uint64(r)<<48 | uint64(g)<<32 | uint64(b)<<16 | uint64(a)
}

func screenshotSizeMismatch(stats browserScreenshotPixelStats, viewport ArtifactHTMLBrowserValidationViewport) bool {
	if !stats.Decoded || viewport.Width <= 0 || viewport.Height <= 0 {
		return false
	}
	return absInt(stats.Width-viewport.Width) > 2 || absInt(stats.Height-viewport.Height) > 2
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func hasDistinctScreenshotViewportSizes(screenshots []browserScreenshotProbe) bool {
	seen := map[string]bool{}
	for _, screenshot := range screenshots {
		key := fmt.Sprintf("%dx%d", screenshot.Width, screenshot.Height)
		seen[key] = true
		if len(seen) > 1 {
			return true
		}
	}
	return false
}

func allScreenshotHashesEqual(screenshots []browserScreenshotProbe) bool {
	first := ""
	for _, screenshot := range screenshots {
		if screenshot.Hash == "" {
			continue
		}
		if first == "" {
			first = screenshot.Hash
			continue
		}
		if screenshot.Hash != first {
			return false
		}
	}
	return first != ""
}

func formatScreenshotViewportNames(screenshots []browserScreenshotProbe) string {
	names := make([]string, 0, len(screenshots))
	for _, screenshot := range screenshots {
		name := strings.TrimSpace(screenshot.ViewportName)
		if name == "" {
			name = fmt.Sprintf("%dx%d", screenshot.Width, screenshot.Height)
		}
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

func hasHTMLValidationDiagnosticCode(diagnostics []ArtifactHTMLValidationIssue, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func browserLayoutDiagnosticsFromStaticResult(result ArtifactHTMLValidationResult) []ArtifactHTMLValidationIssue {
	if len(result.Issues) == 0 {
		return nil
	}
	diagnostics := make([]ArtifactHTMLValidationIssue, 0, 2)
	seen := map[string]bool{}
	add := func(code, message string) {
		if seen[code] {
			return
		}
		seen[code] = true
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     code,
			Severity: "warn",
			Message:  message,
			Source:   "browser_validation",
		})
	}
	for _, issue := range result.Issues {
		switch issue.Code {
		case "long_unbreakable_text":
			add("text_overflow", "Static HTML validation found long unbreakable text; browser validation should inspect text overflow across target viewports")
		case "scrollable_artboard", "out_of_bounds_position":
			add("scroll_bounds", "Static HTML validation found scrollable or out-of-bounds artboard content; browser validation should inspect export crop bounds")
		}
	}
	return diagnostics
}

func validateLocalBrowserResources(input HTMLBrowserValidationInput) []ArtifactHTMLValidationIssue {
	refs := collectHTMLAssetReferences(input.SourceHTML)
	if len(refs) == 0 {
		return nil
	}
	diagnostics := make([]ArtifactHTMLValidationIssue, 0)
	seen := map[string]bool{}
	seenCSS := map[string]bool{}
	for _, ref := range refs {
		if ref.Kind != "local" {
			continue
		}
		if seen[ref.URL] {
			continue
		}
		seen[ref.URL] = true
		resolvedPath := resolvedLocalBrowserResourcePath(input, ref.URL)
		if resolvedPath == "" {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_resource_missing",
				Severity: "error",
				Message:  fmt.Sprintf("Local HTML resource is missing during browser validation: %s", ref.URL),
				Source:   "browser_validation",
			})
			continue
		}
		if shouldInspectLocalCSSResource(ref, resolvedPath) {
			diagnostics = append(diagnostics, validateLocalBrowserCSSResources(input, resolvedPath, ref.URL, seen, seenCSS)...)
		}
	}
	return diagnostics
}

func validateLocalBrowserCSSResources(input HTMLBrowserValidationInput, cssPath string, cssURL string, seen map[string]bool, seenCSS map[string]bool) []ArtifactHTMLValidationIssue {
	cssPath = strings.TrimSpace(cssPath)
	if cssPath == "" || seenCSS[cssPath] {
		return nil
	}
	seenCSS[cssPath] = true
	content, err := os.ReadFile(cssPath)
	if err != nil {
		return []ArtifactHTMLValidationIssue{{
			Code:     "browser_resource_missing",
			Severity: "error",
			Message:  fmt.Sprintf("Local CSS resource could not be read during browser validation: %s", cssURL),
			Source:   "browser_validation",
		}}
	}
	refs := collectHTMLAssetReferences(string(content))
	if len(refs) == 0 {
		return nil
	}
	cssInput := input
	cssInput.SourcePath = cssPath
	diagnostics := make([]ArtifactHTMLValidationIssue, 0)
	for _, ref := range refs {
		if ref.Kind != "local" {
			continue
		}
		key := cssPath + "|" + ref.URL
		if seen[key] {
			continue
		}
		seen[key] = true
		resolvedPath := resolvedLocalBrowserResourcePath(cssInput, ref.URL)
		if resolvedPath == "" {
			diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
				Code:     "browser_resource_missing",
				Severity: "error",
				Message:  fmt.Sprintf("Local CSS dependency is missing during browser validation: %s -> %s", cssURL, ref.URL),
				Source:   "browser_validation",
			})
			continue
		}
		if shouldInspectLocalCSSResource(ref, resolvedPath) {
			diagnostics = append(diagnostics, validateLocalBrowserCSSResources(cssInput, resolvedPath, ref.URL, seen, seenCSS)...)
		}
	}
	return diagnostics
}

func shouldInspectLocalCSSResource(ref ArtifactHTMLAssetReference, resolvedPath string) bool {
	if strings.EqualFold(filepath.Ext(stripHTMLAssetURLDecorations(ref.URL)), ".css") {
		return true
	}
	if strings.EqualFold(filepath.Ext(resolvedPath), ".css") {
		return true
	}
	return ref.Source == "css_import"
}

func resolvedLocalBrowserResourcePath(input HTMLBrowserValidationInput, rawURL string) string {
	cleanURL := stripHTMLAssetURLDecorations(rawURL)
	if cleanURL == "" {
		return ""
	}
	candidates := make([]string, 0, 2)
	if strings.HasPrefix(cleanURL, "/") {
		if strings.TrimSpace(input.WorkspaceRoot) != "" {
			candidates = append(candidates, filepath.Join(input.WorkspaceRoot, strings.TrimPrefix(cleanURL, "/")))
		}
	} else if strings.TrimSpace(input.SourcePath) != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(input.SourcePath), filepath.FromSlash(cleanURL)))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func stripHTMLAssetURLDecorations(rawURL string) string {
	value := strings.TrimSpace(rawURL)
	if hash := strings.Index(value, "#"); hash >= 0 {
		value = value[:hash]
	}
	if query := strings.Index(value, "?"); query >= 0 {
		value = value[:query]
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "\x00") {
		return ""
	}
	return value
}
