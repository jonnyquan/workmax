package workagent

import "testing"

func TestValidateHTMLArtifactContent_Pass(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationPass {
		t.Fatalf("status = %q, want pass; issues=%#v", got.Status, got.Issues)
	}
	if got.IssueCount != 0 {
		t.Fatalf("issue count = %d, want 0", got.IssueCount)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingHTMLLang(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html>
<head>
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "html_lang_missing") {
		t.Fatalf("expected html_lang_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingHTMLTitle(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "html_title_missing") {
		t.Fatalf("expected html_title_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMultipleHTMLTitles(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<title>Campaign Poster</title>
	<title>Duplicate Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "html_title_multiple") {
		t.Fatalf("expected html_title_multiple, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingMetaDescription(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<title>Campaign Poster</title>
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "meta_description_missing") {
		t.Fatalf("expected meta_description_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMultipleMetaDescriptions(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta content="Duplicate summary" name="description">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "meta_description_multiple") {
		t.Fatalf("expected meta_description_multiple, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingHTMLCharset(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "html_charset_missing") {
		t.Fatalf("expected html_charset_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsContentTypeCharset(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<meta http-equiv="content-type" content="text/html; charset=utf-8">
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if hasHTMLIssue(got, "html_charset_missing") {
		t.Fatalf("did not expect html_charset_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingMainLandmark(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>.artboard { width: 1080px; height: 1080px; }</style>
</head>
<body><section class="artboard"><h1>Campaign Poster</h1></section></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "main_landmark_missing") {
		t.Fatalf("expected main_landmark_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingViewportAndRemoteAsset(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><body><main style="aspect-ratio: 1 / 1"><img src="https://example.com/a.png">Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "missing_viewport") || !hasHTMLIssue(got, "external_asset") {
		t.Fatalf("expected viewport and remote asset warnings, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForViewportWithoutDeviceWidth(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "viewport_width_missing") || hasHTMLIssue(got, "missing_viewport") {
		t.Fatalf("expected viewport_width_missing without missing_viewport, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForViewportWithoutInitialScale(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "viewport_initial_scale_missing") || hasHTMLIssue(got, "missing_viewport") {
		t.Fatalf("expected viewport_initial_scale_missing without missing_viewport, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForInvalidViewportInitialScale(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width, initial-scale=0.5">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "viewport_initial_scale_invalid") {
		t.Fatalf("expected viewport_initial_scale_invalid, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsViewportInitialScaleFloatOne(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<meta charset="utf-8">
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1></main></body>
</html>`)
	if hasHTMLIssue(got, "viewport_initial_scale_invalid") {
		t.Fatalf("did not expect viewport_initial_scale_invalid, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMultipleViewportTags(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<meta name="viewport" content="width=1024">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "viewport_multiple") {
		t.Fatalf("expected viewport_multiple, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForViewportLockedZoom(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "viewport_zoom_locked") {
		t.Fatalf("expected viewport_zoom_locked, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForEmptyViewportContent(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "missing_viewport") || hasHTMLIssue(got, "viewport_width_missing") {
		t.Fatalf("expected empty content to be treated as missing viewport only, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForDuplicateElementIDs(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main><section id="hero">Hero</section><section id="hero">Duplicate</section></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "duplicate_element_id") {
		t.Fatalf("expected duplicate_element_id, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingARIAReference(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main><button aria-labelledby="download-label missing-label">Download</button><span id="download-label">Download file</span></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "aria_reference_missing") {
		t.Fatalf("expected aria_reference_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsValidARIAReferences(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main><button aria-labelledby="download-label" aria-describedby="download-hint">Download</button><span id="download-label">Download file</span><span id="download-hint">PDF format</span></main></body></html>`)
	if hasHTMLIssue(got, "aria_reference_missing") {
		t.Fatalf("did not expect aria_reference_missing, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForHeadingMissingH1(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h2>Campaign Poster</h2><p>Launch offer</p></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "heading_missing_h1") {
		t.Fatalf("expected heading_missing_h1, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForHeadingLevelSkip(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1><h3>Offer Details</h3></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "heading_level_skip") {
		t.Fatalf("expected heading_level_skip, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMultipleH1Headings(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1><h1>Limited Offer</h1></main></body>
</html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "heading_multiple_h1") {
		t.Fatalf("expected heading_multiple_h1, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsOrderedHeadings(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<!doctype html>
<html lang="en">
<head>
	<title>Campaign Poster</title>
	<meta name="description" content="Campaign poster preview">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>main { width: 1080px; height: 1080px; }</style>
</head>
<body><main><h1>Campaign Poster</h1><h2>Offer Details</h2><h3>Fine Print</h3></main></body>
</html>`)
	if hasHTMLIssue(got, "heading_missing_h1") || hasHTMLIssue(got, "heading_level_skip") {
		t.Fatalf("did not expect heading issues, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_BlocksExternalScript(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head><meta name="viewport" content="width=device-width"></head><body><script src="https://cdn.example/app.js"></script></body></html>`)
	if got.Status != ArtifactHTMLValidationBlock {
		t.Fatalf("status = %q, want block; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "external_script") || !hasHTMLIssue(got, "inline_script") {
		t.Fatalf("expected external_script and inline_script issues, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForInlineScript(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
</head><body><main style="width: 1200px; height: 628px">Poster</main><script>document.body.dataset.ready = "1"</script></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "inline_script") {
		t.Fatalf("expected inline_script issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_BlocksScriptNetworkCalls(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
</head><body><main style="width: 1200px; height: 628px">Poster</main><script>fetch("https://api.example.com/track")</script></body></html>`)
	if got.Status != ArtifactHTMLValidationBlock {
		t.Fatalf("status = %q, want block; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "inline_script") || !hasHTMLIssue(got, "script_network_call") {
		t.Fatalf("expected inline_script and script_network_call issues, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_BlocksScriptNavigation(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
</head><body><main style="width: 1200px; height: 628px">Poster</main><script>window.location.href = "https://example.com"</script></body></html>`)
	if got.Status != ArtifactHTMLValidationBlock {
		t.Fatalf("status = %q, want block; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "inline_script") || !hasHTMLIssue(got, "script_navigation") {
		t.Fatalf("expected inline_script and script_navigation issues, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_BlocksInlineJavaScript(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 1200px; height: 628px"><a href="javascript:alert(1)" onclick="track()">Open</a></main></body></html>`)
	if got.Status != ArtifactHTMLValidationBlock {
		t.Fatalf("status = %q, want block; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "inline_event_handler") || !hasHTMLIssue(got, "javascript_url") {
		t.Fatalf("expected inline event and javascript URL issues, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_BlocksActiveEmbedsAndMetaRefresh(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<meta http-equiv="refresh" content="0; url=https://example.com">
</head><body><main style="width: 1200px; height: 628px"><object data="movie.swf"></object><embed src="plugin.swf">Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationBlock {
		t.Fatalf("status = %q, want block; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "active_embed") || !hasHTMLIssue(got, "meta_refresh") {
		t.Fatalf("expected active embed and meta refresh issues, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForBaseElement(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<base href="https://cdn.example.com/campaign/">
</head><body><main style="width: 1200px; height: 628px">Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "base_element") {
		t.Fatalf("expected base_element issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForExternalStylesAndFonts(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<link rel="stylesheet" href="https://cdn.example.com/base.css">
	<style>@import url("https://fonts.googleapis.com/css2?family=Inter"); main { aspect-ratio: 16 / 9; }</style>
</head><body><main>Launch banner</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "external_stylesheet") || !hasHTMLIssue(got, "external_font") {
		t.Fatalf("expected external style and font warnings, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForFontFamilyWithoutFallback(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>
		@font-face { font-family: "Acme Sans"; src: url("./acme.woff2"); }
		main { width: 1080px; height: 1080px; font-family: "Acme Sans"; }
	</style>
</head><body><main>Launch banner</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "font_family_no_fallback") {
		t.Fatalf("expected font_family_no_fallback warning, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsFontFamilyWithFallback(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; font-family: "Acme Sans", Arial, sans-serif; }</style>
</head><body><main>Launch banner</main></body></html>`)
	if hasHTMLIssue(got, "font_family_no_fallback") {
		t.Fatalf("did not expect font_family_no_fallback warning, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForRemoteCSSAssets(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; background-image: url("https://cdn.example.com/poster-bg.png"); }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "external_css_asset") {
		t.Fatalf("expected external_css_asset issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForProtocolRelativeRemoteAssets(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<link rel="stylesheet" href="//cdn.example.com/base.css">
	<style>@import "//cdn.example.com/theme.css"; main { width: 1080px; height: 1080px; background-image: url("//cdn.example.com/bg.png"); }</style>
</head><body><main><img src="//cdn.example.com/hero.png" alt="">Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "external_asset") || !hasHTMLIssue(got, "external_css_asset") || !hasHTMLIssue(got, "external_stylesheet") {
		t.Fatalf("expected protocol-relative remote asset warnings, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForSVGImageAssetReferences(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main><svg viewBox="0 0 100 100"><image href="./texture.png" width="100" height="100" /></svg>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "local_asset_reference") {
		t.Fatalf("expected local_asset_reference issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForSrcSetAssetReferences(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main><img srcset="./hero-small.png 1x, https://cdn.example.com/hero-large.png 2x" alt="">Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "local_asset_reference") || !hasHTMLIssue(got, "external_asset") {
		t.Fatalf("expected srcset local and external asset issues, got %#v", got.Issues)
	}
	if !hasHTMLAssetReference(got, "./hero-small.png", "local", "srcset") ||
		!hasHTMLAssetReference(got, "https://cdn.example.com/hero-large.png", "remote", "srcset") {
		t.Fatalf("expected structured srcset asset references, got %#v", got.AssetReferences)
	}
}

func TestValidateHTMLArtifactContent_WarnsForImageSrcSetPreloadAssetReferences(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<link rel="preload" as="image" href="./hero-small.png" imagesrcset="./hero-small.png 1x, ./hero-large.png 2x">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main><img src="./hero-small.png" alt="">Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "local_asset_reference") {
		t.Fatalf("expected local_asset_reference issue, got %#v", got.Issues)
	}
	if !hasHTMLAssetReference(got, "./hero-small.png", "local", "html_attr") ||
		!hasHTMLAssetReference(got, "./hero-large.png", "local", "srcset") {
		t.Fatalf("expected preload href and imagesrcset references, got %#v", got.AssetReferences)
	}
}

func TestValidateHTMLArtifactContent_WarnsForVideoPosterAndTrackAssetReferences(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><video src="./demo.mp4" poster="./demo-poster.jpg" controls><track kind="captions" src="./captions.vtt" srclang="en" label="English"></video>Product demo</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "local_asset_reference") {
		t.Fatalf("expected local_asset_reference issue, got %#v", got.Issues)
	}
	if !hasHTMLAssetReference(got, "./demo.mp4", "local", "html_attr") ||
		!hasHTMLAssetReference(got, "./demo-poster.jpg", "local", "html_attr") ||
		!hasHTMLAssetReference(got, "./captions.vtt", "local", "html_attr") {
		t.Fatalf("expected video src, poster, and track src asset references, got %#v", got.AssetReferences)
	}
}

func TestValidateHTMLArtifactContent_WarnsForLocalAssetsThatNeedBundling(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; background-image: url("/assets/poster-bg.png"); }</style>
</head><body><main><img src="./hero.png" alt="">Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "local_asset_reference") || !hasHTMLIssue(got, "local_css_asset") {
		t.Fatalf("expected local asset bundling warnings, got %#v", got.Issues)
	}
	if !hasHTMLAssetReference(got, "./hero.png", "local", "html_attr") ||
		!hasHTMLAssetReference(got, "/assets/poster-bg.png", "local", "css_url") {
		t.Fatalf("expected structured local asset references, got %#v", got.AssetReferences)
	}
}

func TestValidateHTMLArtifactContent_WarnsForCSSImageSetAssetsThatNeedBundling(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; background-image: image-set("./poster.png" 1x, "./poster@2x.png" 2x); }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "local_css_asset") {
		t.Fatalf("expected local_css_asset issue, got %#v", got.Issues)
	}
	if !hasHTMLAssetReference(got, "./poster.png", "local", "css_image_set") ||
		!hasHTMLAssetReference(got, "./poster@2x.png", "local", "css_image_set") {
		t.Fatalf("expected structured CSS image-set references, got %#v", got.AssetReferences)
	}
}

func TestValidateHTMLArtifactContent_WarnsForLocalCSSImportsThatNeedBundling(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>@import "./theme.css"; main { width: 1080px; height: 1080px; }</style>
</head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "local_css_asset") {
		t.Fatalf("expected local_css_asset issue, got %#v", got.Issues)
	}
	if !hasHTMLAssetReference(got, "./theme.css", "local", "css_import") {
		t.Fatalf("expected structured css import asset reference, got %#v", got.AssetReferences)
	}
}

func TestValidateHTMLArtifactContent_WarnsForFormSubmission(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 1200px; height: 628px"><form action="/submit"><input name="email"></form>Lead form</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "form_submission") {
		t.Fatalf("expected form_submission issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForAnchorNavigation(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 1200px; height: 628px"><a href="/checkout">Shop now</a></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "anchor_navigation") {
		t.Fatalf("expected anchor_navigation issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsHashAnchorsWithoutNavigationWarning(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 1200px; height: 628px"><a href="#details">Details</a></main></body></html>`)
	if hasHTMLIssue(got, "anchor_navigation") {
		t.Fatalf("did not expect anchor_navigation issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForBlankAnchorWithoutNoopener(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
</head><body><main style="width: 1200px; height: 628px"><a href="https://example.com" target="_blank">Open</a></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "anchor_blank_without_noopener") {
		t.Fatalf("expected anchor_blank_without_noopener, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsBlankAnchorWithNoopener(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
</head><body><main style="width: 1200px; height: 628px"><a href="https://example.com" target="_blank" rel="nofollow noopener">Open</a></main></body></html>`)
	if hasHTMLIssue(got, "anchor_blank_without_noopener") {
		t.Fatalf("did not expect anchor_blank_without_noopener, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForImageMissingAlt(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><img src="data:image/png;base64,AAAA">Hero product</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "image_missing_alt") {
		t.Fatalf("expected image_missing_alt issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsImageWithEmptyDecorativeAlt(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><img src="data:image/png;base64,AAAA" alt="">Decorative texture</main></body></html>`)
	if hasHTMLIssue(got, "image_missing_alt") {
		t.Fatalf("did not expect image_missing_alt issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForImageInputMissingAlt(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><form><input type="image" name="submit" src="./submit.png"></form></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "image_input_missing_alt") {
		t.Fatalf("expected image_input_missing_alt issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsImageInputWithAlt(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><form><input type="image" name="submit" src="./submit.png" alt="Submit form"></form></main></body></html>`)
	if hasHTMLIssue(got, "image_input_missing_alt") {
		t.Fatalf("did not expect image_input_missing_alt issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForButtonMissingAccessibleName(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main>Toolbar <button type="button"><svg viewBox="0 0 16 16"></svg></button></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "button_missing_accessible_name") {
		t.Fatalf("expected button_missing_accessible_name issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsButtonWithAccessibleName(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main>Toolbar <button type="button" aria-label="Download"><svg viewBox="0 0 16 16"></svg></button></main></body></html>`)
	if hasHTMLIssue(got, "button_missing_accessible_name") {
		t.Fatalf("did not expect button_missing_accessible_name issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForFormButtonWithoutType(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><form><input name="email" aria-label="Email"><button>Preview</button></form></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "form_button_missing_type") {
		t.Fatalf("expected form_button_missing_type issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsFormButtonWithType(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><form><input name="email" aria-label="Email"><button type="button">Preview</button></form></main></body></html>`)
	if hasHTMLIssue(got, "form_button_missing_type") {
		t.Fatalf("did not expect form_button_missing_type issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForVideoMissingPosterAndCaptions(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><video src="./demo.mp4" controls></video>Product demo</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "video_missing_poster") || !hasHTMLIssue(got, "media_missing_captions") {
		t.Fatalf("expected video poster and captions warnings, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForAudioMissingCaptions(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><audio src="./voiceover.mp3" controls></audio>Voiceover preview</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "media_missing_captions") {
		t.Fatalf("expected media_missing_captions issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsCaptionedVideoWithPoster(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><video src="./demo.mp4" poster="./demo.jpg" controls><track kind="captions" src="./demo.vtt" srclang="en" label="English"></video>Product demo</main></body></html>`)
	if hasHTMLIssue(got, "video_missing_poster") || hasHTMLIssue(got, "media_missing_captions") {
		t.Fatalf("did not expect media accessibility warnings, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsDecorativeHiddenVideoWithoutPosterOrCaptions(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><video src="./texture.mp4" aria-hidden="true" muted autoplay loop></video>Ambient hero</main></body></html>`)
	if hasHTMLIssue(got, "video_missing_poster") || hasHTMLIssue(got, "media_missing_captions") {
		t.Fatalf("did not expect decorative media warnings, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForFormControlMissingName(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><form><label for="email">Email</label><input id="email" type="email"></form>Signup</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "form_control_missing_name") {
		t.Fatalf("expected form_control_missing_name issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForFormControlMissingLabel(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><form><input name="email" type="email"></form>Signup</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "form_control_missing_label") {
		t.Fatalf("expected form_control_missing_label issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsLabelledNamedFormControls(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1200px; height: 628px; }</style>
</head><body><main><form>
	<label for="email">Email</label><input id="email" name="email" type="email">
	<label>Plan <select name="plan"><option>Pro</option></select></label>
	<textarea name="notes" aria-label="Notes"></textarea>
</form>Signup</main></body></html>`)
	if hasHTMLIssue(got, "form_control_missing_name") || hasHTMLIssue(got, "form_control_missing_label") {
		t.Fatalf("did not expect form control issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMissingArtboardConstraints(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head><meta name="viewport" content="width=device-width"></head><body><main>Poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "missing_artboard_constraints") {
		t.Fatalf("expected missing_artboard_constraints issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForViewportSizedArtboard(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 100vw; height: 100vh; }</style>
</head><body><main>Landing page</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "viewport_sized_artboard") {
		t.Fatalf("expected viewport_sized_artboard issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForScrollableArtboard(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; overflow-y: scroll; }</style>
</head><body><main>Long poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "scrollable_artboard") {
		t.Fatalf("expected scrollable_artboard issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForOutOfBoundsPositioning(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>
		main { width: 1080px; height: 1080px; position: relative; overflow: hidden; }
		.badge { position: absolute; left: 140%; top: -80px; transform: translateX(120%); }
	</style>
</head><body><main><span class="badge">Launch</span></main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "out_of_bounds_position") {
		t.Fatalf("expected out_of_bounds_position issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForMotionWithoutReducedMotion(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; animation: fade-in 600ms ease both; } @keyframes fade-in { from { opacity: 0; } to { opacity: 1; } }</style>
</head><body><main>Motion poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "missing_reduced_motion") {
		t.Fatalf("expected missing_reduced_motion issue, got %#v", got.Issues)
	}
	if !hasHTMLIssue(got, "missing_motion_timeline") {
		t.Fatalf("expected missing_motion_timeline issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsMotionWithReducedMotion(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<meta name="workmax:motion-duration-ms" content="3500">
	<meta name="workmax:motion-fps" content="24">
	<style>
		main { width: 1080px; height: 1080px; transition: opacity 300ms ease; }
		@media (prefers-reduced-motion: reduce) { * { animation: none !important; transition: none !important; } }
	</style>
</head><body><main>Motion poster</main></body></html>`)
	if hasHTMLIssue(got, "missing_reduced_motion") {
		t.Fatalf("did not expect missing_reduced_motion issue, got %#v", got.Issues)
	}
	if hasHTMLIssue(got, "missing_motion_timeline") || hasHTMLIssue(got, "invalid_motion_timeline") {
		t.Fatalf("did not expect motion timeline issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForInvalidMotionTimelineMetadata(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<meta name="workmax:motion-duration-ms" content="10">
	<meta name="workmax:motion-fps" content="240">
	<style>
		main { width: 1080px; height: 1080px; transition: opacity 300ms ease; }
		@media (prefers-reduced-motion: reduce) { * { animation: none !important; transition: none !important; } }
	</style>
</head><body><main>Motion poster</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "invalid_motion_timeline") {
		t.Fatalf("expected invalid_motion_timeline issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_WarnsForLongUnbreakableText(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; }</style>
</head><body><main>SUMMERLAUNCHPROMOTIONWITHNOSPACES2026</main></body></html>`)
	if got.Status != ArtifactHTMLValidationWarn {
		t.Fatalf("status = %q, want warn; issues=%#v", got.Status, got.Issues)
	}
	if !hasHTMLIssue(got, "long_unbreakable_text") {
		t.Fatalf("expected long_unbreakable_text issue, got %#v", got.Issues)
	}
}

func TestValidateHTMLArtifactContent_AllowsLongTextWithWrapHint(t *testing.T) {
	got := ValidateHTMLArtifactContent(`<html><head>
	<meta name="viewport" content="width=device-width">
	<style>main { width: 1080px; height: 1080px; overflow-wrap: anywhere; }</style>
</head><body><main>SUMMERLAUNCHPROMOTIONWITHNOSPACES2026</main></body></html>`)
	if hasHTMLIssue(got, "long_unbreakable_text") {
		t.Fatalf("did not expect long_unbreakable_text issue, got %#v", got.Issues)
	}
}

func hasHTMLIssue(result ArtifactHTMLValidationResult, code string) bool {
	for _, issue := range result.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func hasHTMLAssetReference(result ArtifactHTMLValidationResult, url, kind, source string) bool {
	for _, ref := range result.AssetReferences {
		if ref.URL == url && ref.Kind == kind && ref.Source == source && ref.Action != "" {
			return true
		}
	}
	return false
}
