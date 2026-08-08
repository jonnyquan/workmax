package workagent

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	ArtifactHTMLValidationPass  = "pass"
	ArtifactHTMLValidationWarn  = "warn"
	ArtifactHTMLValidationBlock = "block"
)

var (
	htmlOpenTagPattern          = regexp.MustCompile(`(?is)<html\b[^>]*>`)
	htmlHeadTagPattern          = regexp.MustCompile(`(?is)<head\b`)
	htmlTitlePattern            = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	htmlMetaDescriptionPattern  = regexp.MustCompile(`(?is)<meta\b[^>]*\sname\s*=\s*["']description["'][^>]*>|<meta\b[^>]*\scontent\s*=\s*["'][^"']*["'][^>]*\sname\s*=\s*["']description["'][^>]*>`)
	htmlCharsetPattern          = regexp.MustCompile(`(?is)<meta\b[^>]*(?:\scharset\s*=|\scontent\s*=\s*["'][^"']*charset\s*=)`)
	htmlExternalScriptPattern   = regexp.MustCompile(`(?is)<script[^>]+src\s*=`)
	htmlInlineScriptPattern     = regexp.MustCompile(`(?is)<script(?:\s[^>]*)?>`)
	htmlScriptNetworkPattern    = regexp.MustCompile(`(?is)\b(?:fetch|XMLHttpRequest|WebSocket|EventSource)\s*\(|navigator\s*\.\s*sendBeacon\s*\(`)
	htmlScriptNavigationPattern = regexp.MustCompile(`(?is)\b(?:window\s*\.\s*)?open\s*\(|(?:window\s*\.\s*)?location\s*(?:\.href|\.assign|\.replace)?\s*=|(?:window\s*\.\s*)?location\s*\.\s*(?:assign|replace)\s*\(`)
	htmlIframePattern           = regexp.MustCompile(`(?is)<iframe\b`)
	htmlActiveEmbedPattern      = regexp.MustCompile(`(?is)<(?:object|embed)\b`)
	htmlMetaRefreshPattern      = regexp.MustCompile(`(?is)<meta[^>]+http-equiv\s*=\s*["']?refresh\b|<meta[^>]+content\s*=\s*["'][^"']*;\s*url\s*=`)
	htmlBaseElementPattern      = regexp.MustCompile(`(?is)<base\b`)
	htmlFormPattern             = regexp.MustCompile(`(?is)<form\b`)
	htmlFormBlockPattern        = regexp.MustCompile(`(?is)<form\b[^>]*>(.*?)</form>`)
	htmlMainTagPattern          = regexp.MustCompile(`(?is)<main\b`)
	htmlHeadingTagPattern       = regexp.MustCompile(`(?is)<h([1-6])\b[^>]*>`)
	htmlAnchorTagPattern        = regexp.MustCompile(`(?is)<a\b[^>]*>`)
	htmlAnchorHrefPattern       = regexp.MustCompile(`(?is)<a\b[^>]*\shref\s*=\s*["']\s*([^"']+)`)
	htmlImageTagPattern         = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	htmlButtonTagPattern        = regexp.MustCompile(`(?is)<button\b([^>]*)>(.*?)</button>`)
	htmlVideoBlockPattern       = regexp.MustCompile(`(?is)<video\b([^>]*)>(.*?)</video>`)
	htmlAudioBlockPattern       = regexp.MustCompile(`(?is)<audio\b([^>]*)>(.*?)</audio>`)
	htmlCaptionTrackPattern     = regexp.MustCompile(`(?is)<track\b[^>]*\skind\s*=\s*["'](?:captions|subtitles)["']`)
	htmlFormControlTagPattern   = regexp.MustCompile(`(?is)<(?:input|select|textarea)\b[^>]*>`)
	htmlLabelForPattern         = regexp.MustCompile(`(?is)<label\b[^>]*\sfor\s*=\s*["']([^"']+)["']`)
	htmlLabelBlockPattern       = regexp.MustCompile(`(?is)<label\b[^>]*>.*?</label>`)
	htmlExternalAssetPattern    = regexp.MustCompile(`(?is)<(?:img|image|video|audio|source|link|track)[^>]+(?:src|href|poster)\s*=\s*["'](?:https?:)?//`)
	htmlAssetTagPattern         = regexp.MustCompile(`(?is)<(?:img|image|video|audio|source|link|track)\b[^>]*>`)
	htmlSrcSetPattern           = regexp.MustCompile(`(?is)\s(?:srcset|imagesrcset)\s*=\s*["']([^"']+)`)
	htmlCSSRemoteAssetPattern   = regexp.MustCompile(`(?is)url\(\s*["']?(?:https?:)?//`)
	htmlCSSAssetURLPattern      = regexp.MustCompile(`(?is)url\(\s*["']?([^"')]+)`)
	htmlCSSImageSetPattern      = regexp.MustCompile(`(?is)(?:-webkit-)?image-set\(([^)]*)\)`)
	htmlCSSQuotedURLPattern     = regexp.MustCompile(`["']([^"']+)["']`)
	htmlInlineEventPattern      = regexp.MustCompile(`(?is)<[a-z][^>]*\son[a-z]+\s*=`)
	htmlJavascriptURLPattern    = regexp.MustCompile(`(?is)(?:href|src|action|formaction)\s*=\s*["']\s*javascript:`)
	htmlIDAttrPattern           = regexp.MustCompile(`(?is)\sid\s*=\s*["']([^"']+)["']`)
	htmlARIAReferencePattern    = regexp.MustCompile(`(?is)<[a-z][^>]*\saria-(?:labelledby|describedby)\s*=\s*["']([^"']+)["']`)
	htmlExternalStylePattern    = regexp.MustCompile(`(?is)<link[^>]+rel\s*=\s*["'][^"']*stylesheet[^"']*["'][^>]+href\s*=\s*["'](?:https?:)?//|<link[^>]+href\s*=\s*["'](?:https?:)?//[^>]+rel\s*=\s*["'][^"']*stylesheet`)
	htmlCSSImportPattern        = regexp.MustCompile(`(?is)@import\s+(?:url\()?["']?(?:https?:)?//`)
	htmlCSSImportURLPattern     = regexp.MustCompile(`(?is)@import\s+(?:url\()?\s*["']?([^"');\s]+)`)
	htmlExternalFontPattern     = regexp.MustCompile(`(?is)fonts\.(?:googleapis|gstatic)\.com|@font-face[^}]+url\(["']?https?://`)
	htmlFontFaceBlockPattern    = regexp.MustCompile(`(?is)@font-face\s*{[^}]*}`)
	htmlFontFamilyPattern       = regexp.MustCompile(`(?is)font-family\s*:\s*([^;}]+)`)
	htmlVisibleTextStripPattern = regexp.MustCompile(`(?is)<script.*?</script>|<style.*?</style>|<[^>]+>`)
	htmlWhitespacePattern       = regexp.MustCompile(`\s+`)
	htmlWidthHintPattern        = regexp.MustCompile(`(?is)(?:^|[\s;{"])width\s*:\s*(?:\d+(?:\.\d+)?(?:px|rem|vh|vw)|100(?:%|vw))`)
	htmlHeightHintPattern       = regexp.MustCompile(`(?is)(?:^|[\s;{"])height\s*:\s*(?:\d+(?:\.\d+)?(?:px|rem|vh|vw)|100(?:%|vh))`)
	htmlViewportArtboardPattern = regexp.MustCompile(`(?is)(?:width\s*:\s*100(?:vw|%)[^}]+height\s*:\s*100(?:vh|%)|height\s*:\s*100(?:vh|%)[^}]+width\s*:\s*100(?:vw|%))`)
	htmlScrollablePattern       = regexp.MustCompile(`(?is)(?:^|[\s;{"])overflow(?:-[xy])?\s*:\s*(?:auto|scroll)`)
	htmlOutOfBoundsPattern      = regexp.MustCompile(`(?is)(?:^|[\s;{"])(?:left|right|top|bottom)\s*:\s*-\s*(?:\d{2,}(?:\.\d+)?(?:px|rem|vw|vh|%)|[1-9]\d*%)|(?:^|[\s;{"])(?:left|top)\s*:\s*(?:1[1-9]\d|[2-9]\d{2,})(?:%|vw|vh)|translate(?:3d|x|y)?\([^)]*(?:-\s*(?:\d{2,}(?:\.\d+)?(?:px|rem|vw|vh|%)|[1-9]\d*%)|(?:1[1-9]\d|[2-9]\d{2,})(?:%|vw|vh))`)
	htmlMotionPattern           = regexp.MustCompile(`(?is)(?:@keyframes\b|(?:^|[\s;{"])animation(?:-[a-z-]+)?\s*:|(?:^|[\s;{"])transition(?:-[a-z-]+)?\s*:)`)
)

type ArtifactHTMLValidationIssue struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Source   string `json:"source,omitempty"`
}

type ArtifactHTMLAssetReference struct {
	URL    string `json:"url"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Action string `json:"action"`
}

type ArtifactHTMLValidationResult struct {
	Status             string                        `json:"status"`
	IssueCount         int                           `json:"issueCount"`
	Issues             []ArtifactHTMLValidationIssue `json:"issues,omitempty"`
	PreviewDiagnostics []ArtifactHTMLValidationIssue `json:"previewDiagnostics,omitempty"`
	AssetReferences    []ArtifactHTMLAssetReference  `json:"assetReferences,omitempty"`
}

func ValidateHTMLArtifactContent(content string) ArtifactHTMLValidationResult {
	body := strings.TrimSpace(content)
	issues := make([]ArtifactHTMLValidationIssue, 0, 4)
	add := func(code, severity, message string) {
		issues = append(issues, ArtifactHTMLValidationIssue{Code: code, Severity: severity, Message: message})
	}
	if body == "" {
		add("empty_html", "block", "HTML artifact is empty")
		return htmlValidationResult(issues, nil)
	}
	lower := strings.ToLower(body)
	assetReferences := collectHTMLAssetReferences(body)
	if !strings.Contains(lower, "<html") {
		add("missing_html_element", "warn", "HTML artifact should include an html element")
	} else if hasHTMLTagWithoutLang(body) {
		add("html_lang_missing", "warn", "HTML artifact should declare a lang attribute on the html element")
	}
	if !strings.Contains(lower, "<body") {
		add("missing_body_element", "warn", "HTML artifact should include a body element")
	} else if htmlMainTagPattern.FindStringIndex(body) == nil {
		add("main_landmark_missing", "warn", "HTML artifact should wrap primary content in a main landmark")
	}
	if htmlHeadTagPattern.FindStringIndex(body) != nil && hasMissingOrEmptyHTMLTitle(body) {
		add("html_title_missing", "warn", "HTML artifact should include a non-empty title element")
	}
	if hasMultipleHTMLTitles(body) {
		add("html_title_multiple", "warn", "HTML artifact should include only one title element")
	}
	if htmlHeadTagPattern.FindStringIndex(body) != nil && hasMissingOrEmptyMetaDescription(body) {
		add("meta_description_missing", "warn", "HTML artifact should include a non-empty meta description")
	}
	if hasMultipleMetaDescriptions(body) {
		add("meta_description_multiple", "warn", "HTML artifact should include only one meta description")
	}
	if htmlHeadTagPattern.FindStringIndex(body) != nil && htmlCharsetPattern.FindStringIndex(body) == nil {
		add("html_charset_missing", "warn", "HTML artifact should declare a document charset")
	}
	for _, issue := range validateHTMLViewportMetadata(body) {
		add(issue.Code, issue.Severity, issue.Message)
	}
	if htmlExternalScriptPattern.FindStringIndex(body) != nil {
		add("external_script", "block", "HTML artifact loads external script code")
	}
	if htmlInlineScriptPattern.FindStringIndex(body) != nil {
		add("inline_script", "warn", "HTML artifact uses inline script code that may export inconsistently")
	}
	if htmlScriptNetworkPattern.FindStringIndex(body) != nil {
		add("script_network_call", "block", "HTML artifact script performs network calls")
	}
	if htmlScriptNavigationPattern.FindStringIndex(body) != nil {
		add("script_navigation", "block", "HTML artifact script performs navigation")
	}
	if htmlIframePattern.FindStringIndex(body) != nil {
		add("iframe_embed", "block", "HTML artifact embeds an iframe")
	}
	if htmlActiveEmbedPattern.FindStringIndex(body) != nil {
		add("active_embed", "block", "HTML artifact embeds object or embed content")
	}
	if htmlMetaRefreshPattern.FindStringIndex(body) != nil {
		add("meta_refresh", "block", "HTML artifact uses meta refresh navigation")
	}
	if htmlBaseElementPattern.FindStringIndex(body) != nil {
		add("base_element", "warn", "HTML artifact defines a base URL that may change asset resolution during export")
	}
	if htmlFormPattern.FindStringIndex(body) != nil {
		add("form_submission", "warn", "HTML artifact includes a form submission surface")
	}
	if hasFormButtonWithoutType(body) {
		add("form_button_missing_type", "warn", "HTML artifact includes a form button without an explicit type")
	}
	if hasNavigatingAnchor(body) {
		add("anchor_navigation", "warn", "HTML artifact includes link navigation that will not export consistently")
	}
	if hasBlankAnchorWithoutNoopener(body) {
		add("anchor_blank_without_noopener", "warn", "HTML artifact opens a new tab without rel noopener or noreferrer")
	}
	if hasImageWithoutAlt(body) {
		add("image_missing_alt", "warn", "HTML artifact includes an image without alt text")
	}
	if hasImageInputWithoutAlt(body) {
		add("image_input_missing_alt", "warn", "HTML artifact includes an image input without alt text")
	}
	if hasButtonWithoutAccessibleName(body) {
		add("button_missing_accessible_name", "warn", "HTML artifact includes a button without visible text or an accessible label")
	}
	if hasVideoWithoutPoster(body) {
		add("video_missing_poster", "warn", "HTML artifact includes a video without a poster fallback")
	}
	if hasMediaWithoutCaptions(body) {
		add("media_missing_captions", "warn", "HTML artifact includes audio or video without captions/subtitles track")
	}
	if hasFormControlWithoutName(body) {
		add("form_control_missing_name", "warn", "HTML artifact includes a form control without a stable name")
	}
	if hasFormControlWithoutLabel(body) {
		add("form_control_missing_label", "warn", "HTML artifact includes a form control without a visible or accessible label")
	}
	if htmlInlineEventPattern.FindStringIndex(body) != nil {
		add("inline_event_handler", "block", "HTML artifact uses inline event handler JavaScript")
	}
	if htmlJavascriptURLPattern.FindStringIndex(body) != nil {
		add("javascript_url", "block", "HTML artifact uses a javascript: URL")
	}
	if hasDuplicateHTMLElementID(body) {
		add("duplicate_element_id", "warn", "HTML artifact uses duplicate element id attributes")
	}
	if hasBrokenARIAReference(body) {
		add("aria_reference_missing", "warn", "HTML artifact references an aria-labelledby or aria-describedby id that does not exist")
	}
	headingLevels := htmlHeadingLevels(body)
	if len(headingLevels) > 0 && !hasHTMLHeadingLevel(headingLevels, 1) {
		add("heading_missing_h1", "warn", "HTML artifact includes headings but no primary h1 heading")
	} else if hasHTMLHeadingLevelSkip(headingLevels) {
		add("heading_level_skip", "warn", "HTML artifact skips heading levels")
	}
	if htmlHeadingLevelCount(headingLevels, 1) > 1 {
		add("heading_multiple_h1", "warn", "HTML artifact includes multiple h1 headings")
	}
	if htmlExternalAssetPattern.FindStringIndex(body) != nil || hasAssetReference(assetReferences, "remote", "html_attr", "srcset") {
		add("external_asset", "warn", "HTML artifact references remote assets")
	}
	if hasAssetReference(assetReferences, "local", "html_attr", "srcset") {
		add("local_asset_reference", "warn", "HTML artifact references local assets that must be bundled for export")
	}
	if htmlCSSRemoteAssetPattern.FindStringIndex(body) != nil {
		add("external_css_asset", "warn", "HTML artifact references remote assets from CSS")
	}
	if hasAssetReference(assetReferences, "local", "css_url", "css_import", "css_image_set") {
		add("local_css_asset", "warn", "HTML artifact references local CSS assets that must be bundled for export")
	}
	if htmlExternalStylePattern.FindStringIndex(body) != nil || htmlCSSImportPattern.FindStringIndex(body) != nil {
		add("external_stylesheet", "warn", "HTML artifact references external CSS")
	}
	if htmlExternalFontPattern.FindStringIndex(body) != nil {
		add("external_font", "warn", "HTML artifact references external font assets")
	}
	if hasFontFamilyWithoutFallback(body) {
		add("font_family_no_fallback", "warn", "HTML artifact declares a custom font-family without a generic fallback")
	}
	if !strings.Contains(lower, "aspect-ratio") && (!htmlWidthHintPattern.MatchString(body) || !htmlHeightHintPattern.MatchString(body)) {
		add("missing_artboard_constraints", "warn", "HTML artifact should define a stable artboard size or aspect ratio")
	}
	if !strings.Contains(lower, "aspect-ratio") && htmlViewportArtboardPattern.MatchString(body) {
		add("viewport_sized_artboard", "warn", "HTML artifact uses viewport-sized artboard without an explicit aspect ratio")
	}
	if htmlScrollablePattern.MatchString(body) {
		add("scrollable_artboard", "warn", "HTML artifact contains scrollable overflow that may export inconsistently")
	}
	if htmlOutOfBoundsPattern.MatchString(body) {
		add("out_of_bounds_position", "warn", "HTML artifact contains positioning that may place content outside the export artboard")
	}
	if htmlMotionPattern.MatchString(body) && !strings.Contains(lower, "prefers-reduced-motion") {
		add("missing_reduced_motion", "warn", "HTML artifact uses motion without a prefers-reduced-motion fallback")
	}
	for _, issue := range validateHTMLMotionTimelineMetadata(body) {
		add(issue.Code, issue.Severity, issue.Message)
	}
	visible := htmlVisibleTextStripPattern.ReplaceAllString(body, " ")
	visible = strings.TrimSpace(htmlWhitespacePattern.ReplaceAllString(visible, " "))
	if visible == "" {
		add("no_visible_text", "warn", "HTML artifact has no visible text content")
	} else if hasLongUnbreakableText(visible) && !hasTextOverflowWrapHint(lower) {
		add("long_unbreakable_text", "warn", "HTML artifact contains long unbreakable text without overflow wrapping hints")
	}
	return htmlValidationResult(issues, assetReferences)
}

func validateHTMLMotionTimelineMetadata(body string) []ArtifactHTMLValidationIssue {
	issues := make([]ArtifactHTMLValidationIssue, 0, 2)
	hasMotion := htmlMotionPattern.MatchString(body)
	settings := ExtractHTMLMotionRenderSettings(body)
	seenMotionMeta := map[string]bool{}
	invalidMotionMeta := false
	for _, tag := range motionMetaTagPattern.FindAllString(body, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		name := strings.ToLower(strings.TrimSpace(attrs["name"]))
		content := strings.TrimSpace(attrs["content"])
		if !strings.HasPrefix(name, "workmax:motion-") {
			continue
		}
		seenMotionMeta[name] = true
		valid := true
		switch name {
		case "workmax:motion-duration-ms":
			valid = clampMotionSettingInt(content, 100, 60000) > 0
		case "workmax:motion-fps":
			valid = clampMotionSettingInt(content, 1, 120) > 0
		case "workmax:motion-width", "workmax:motion-height":
			valid = clampMotionSettingInt(content, 1, 10000) > 0
		default:
			continue
		}
		if !valid {
			invalidMotionMeta = true
		}
	}
	if invalidMotionMeta {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "invalid_motion_timeline",
			Severity: "warn",
			Message:  "HTML artifact declares invalid workmax:motion-* metadata for MP4/GIF export",
		})
	}
	if hasMotion && (settings.DurationMs == 0 || settings.FPS == 0) && len(seenMotionMeta) == 0 {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "missing_motion_timeline",
			Severity: "warn",
			Message:  "HTML artifact uses motion but does not declare workmax:motion-duration-ms and workmax:motion-fps metadata",
		})
	}
	return issues
}

func hasHTMLTagWithoutLang(body string) bool {
	tag := htmlOpenTagPattern.FindString(body)
	if tag == "" {
		return false
	}
	attrs := parseHTMLMotionMetaAttrs(tag)
	return strings.TrimSpace(attrs["lang"]) == ""
}

func hasMissingOrEmptyHTMLTitle(body string) bool {
	match := htmlTitlePattern.FindStringSubmatch(body)
	if len(match) < 2 {
		return true
	}
	title := htmlVisibleTextStripPattern.ReplaceAllString(match[1], " ")
	title = strings.TrimSpace(htmlWhitespacePattern.ReplaceAllString(title, " "))
	return title == ""
}

func hasMultipleHTMLTitles(body string) bool {
	return len(htmlTitlePattern.FindAllString(body, -1)) > 1
}

func hasMissingOrEmptyMetaDescription(body string) bool {
	tag := htmlMetaDescriptionPattern.FindString(body)
	if tag == "" {
		return true
	}
	attrs := parseHTMLMotionMetaAttrs(tag)
	return strings.TrimSpace(attrs["content"]) == ""
}

func hasMultipleMetaDescriptions(body string) bool {
	return len(htmlMetaDescriptionPattern.FindAllString(body, -1)) > 1
}

func hasDuplicateHTMLElementID(body string) bool {
	seen := map[string]bool{}
	for _, match := range htmlIDAttrPattern.FindAllStringSubmatch(body, -1) {
		if len(match) <= 1 {
			continue
		}
		id := strings.TrimSpace(match[1])
		if id == "" {
			continue
		}
		if seen[id] {
			return true
		}
		seen[id] = true
	}
	return false
}

func hasBrokenARIAReference(body string) bool {
	ids := htmlElementIDs(body)
	for _, match := range htmlARIAReferencePattern.FindAllStringSubmatch(body, -1) {
		if len(match) <= 1 {
			continue
		}
		for _, refID := range strings.Fields(strings.TrimSpace(match[1])) {
			if refID != "" && !ids[refID] {
				return true
			}
		}
	}
	return false
}

func htmlElementIDs(body string) map[string]bool {
	ids := map[string]bool{}
	for _, match := range htmlIDAttrPattern.FindAllStringSubmatch(body, -1) {
		if len(match) <= 1 {
			continue
		}
		id := strings.TrimSpace(match[1])
		if id != "" {
			ids[id] = true
		}
	}
	return ids
}

func htmlHeadingLevels(body string) []int {
	levels := make([]int, 0, 4)
	for _, match := range htmlHeadingTagPattern.FindAllStringSubmatch(body, -1) {
		if len(match) <= 1 {
			continue
		}
		level, err := strconv.Atoi(match[1])
		if err == nil {
			levels = append(levels, level)
		}
	}
	return levels
}

func hasHTMLHeadingLevel(levels []int, expected int) bool {
	for _, level := range levels {
		if level == expected {
			return true
		}
	}
	return false
}

func htmlHeadingLevelCount(levels []int, expected int) int {
	count := 0
	for _, level := range levels {
		if level == expected {
			count++
		}
	}
	return count
}

func hasHTMLHeadingLevelSkip(levels []int) bool {
	if len(levels) == 0 {
		return false
	}
	previous := levels[0]
	for _, level := range levels[1:] {
		if level > previous+1 {
			return true
		}
		previous = level
	}
	return false
}

func validateHTMLViewportMetadata(body string) []ArtifactHTMLValidationIssue {
	issues := make([]ArtifactHTMLValidationIssue, 0, 2)
	viewportContent := ""
	viewportCount := 0
	for _, tag := range motionMetaTagPattern.FindAllString(body, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		if strings.EqualFold(strings.TrimSpace(attrs["name"]), "viewport") {
			viewportCount++
			viewportContent = strings.TrimSpace(attrs["content"])
		}
	}
	if viewportContent == "" {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "missing_viewport",
			Severity: "warn",
			Message:  "HTML artifact should include a responsive viewport meta tag",
		})
		return issues
	}
	if viewportCount > 1 {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "viewport_multiple",
			Severity: "warn",
			Message:  "HTML artifact should include only one viewport meta tag",
		})
	}
	directives := parseViewportContentDirectives(viewportContent)
	if strings.ToLower(strings.TrimSpace(directives["width"])) != "device-width" {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "viewport_width_missing",
			Severity: "warn",
			Message:  "HTML artifact viewport should include width=device-width",
		})
	}
	if strings.TrimSpace(directives["initial-scale"]) == "" {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "viewport_initial_scale_missing",
			Severity: "warn",
			Message:  "HTML artifact viewport should include initial-scale=1",
		})
	} else if !viewportInitialScaleIsOne(directives["initial-scale"]) {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "viewport_initial_scale_invalid",
			Severity: "warn",
			Message:  "HTML artifact viewport initial-scale should be 1",
		})
	}
	if viewportLocksZoom(directives) {
		issues = append(issues, ArtifactHTMLValidationIssue{
			Code:     "viewport_zoom_locked",
			Severity: "warn",
			Message:  "HTML artifact viewport should not lock user zoom",
		})
	}
	return issues
}

func parseViewportContentDirectives(content string) map[string]string {
	directives := map[string]string{}
	for _, part := range strings.Split(content, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		directives[key] = strings.TrimSpace(value)
	}
	return directives
}

func viewportInitialScaleIsOne(raw string) bool {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return err == nil && parsed == 1
}

func viewportLocksZoom(directives map[string]string) bool {
	userScalable := strings.ToLower(strings.TrimSpace(directives["user-scalable"]))
	if userScalable == "no" || userScalable == "0" || userScalable == "false" {
		return true
	}
	maxScale := strings.TrimSpace(directives["maximum-scale"])
	if maxScale == "" {
		return false
	}
	parsed, err := strconv.ParseFloat(maxScale, 64)
	return err == nil && parsed <= 1
}

func hasLongUnbreakableText(visible string) bool {
	for _, token := range strings.Fields(visible) {
		token = strings.Trim(token, " \t\r\n.,;:!?()[]{}<>\"'`")
		if len([]rune(token)) >= 32 {
			return true
		}
	}
	return false
}

func hasTextOverflowWrapHint(lowerHTML string) bool {
	return strings.Contains(lowerHTML, "overflow-wrap") ||
		strings.Contains(lowerHTML, "word-break") ||
		strings.Contains(lowerHTML, "hyphens:")
}

func hasFontFamilyWithoutFallback(content string) bool {
	css := htmlFontFaceBlockPattern.ReplaceAllString(content, " ")
	for _, match := range htmlFontFamilyPattern.FindAllStringSubmatch(css, -1) {
		if len(match) <= 1 {
			continue
		}
		if fontFamilyNeedsFallback(match[1]) {
			return true
		}
	}
	return false
}

func fontFamilyNeedsFallback(raw string) bool {
	value := strings.TrimSpace(strings.TrimSuffix(raw, "!important"))
	if value == "" {
		return false
	}
	if strings.Contains(strings.ToLower(value), "var(") {
		return false
	}
	for _, part := range strings.Split(value, ",") {
		if isGenericFontFamily(part) {
			return false
		}
	}
	first := strings.TrimSpace(strings.Split(value, ",")[0])
	return first != "" && !isGenericFontFamily(first)
}

func isGenericFontFamily(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.Trim(value, `"'`)
	value = strings.TrimSpace(strings.TrimSuffix(value, "!important"))
	switch value {
	case "serif", "sans-serif", "monospace", "cursive", "fantasy", "system-ui",
		"ui-serif", "ui-sans-serif", "ui-monospace", "ui-rounded",
		"emoji", "math", "fangsong":
		return true
	default:
		return false
	}
}

func hasNavigatingAnchor(content string) bool {
	for _, match := range htmlAnchorHrefPattern.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 && isNavigatingHref(match[1]) {
			return true
		}
	}
	return false
}

func isNavigatingHref(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" ||
		strings.HasPrefix(value, "#") ||
		strings.HasPrefix(value, "javascript:") {
		return false
	}
	return true
}

func hasBlankAnchorWithoutNoopener(content string) bool {
	for _, tag := range htmlAnchorTagPattern.FindAllString(content, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		if !strings.EqualFold(strings.TrimSpace(attrs["target"]), "_blank") {
			continue
		}
		rel := strings.ToLower(strings.TrimSpace(attrs["rel"]))
		relValues := strings.Fields(rel)
		if !stringSliceContains(relValues, "noopener") && !stringSliceContains(relValues, "noreferrer") {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func collectHTMLAssetReferences(content string) []ArtifactHTMLAssetReference {
	seen := map[string]bool{}
	refs := make([]ArtifactHTMLAssetReference, 0, 4)
	add := func(raw, source string) {
		url := strings.TrimSpace(raw)
		kind := assetURLKind(url)
		if kind == "" {
			return
		}
		key := kind + "|" + source + "|" + url
		if seen[key] {
			return
		}
		seen[key] = true
		refs = append(refs, ArtifactHTMLAssetReference{
			URL:    url,
			Kind:   kind,
			Source: source,
			Action: assetReferenceAction(kind),
		})
	}
	for _, tag := range htmlAssetTagPattern.FindAllString(content, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		for _, name := range []string{"src", "href", "poster"} {
			if value := attrs[name]; value != "" {
				add(value, "html_attr")
			}
		}
	}
	for _, url := range srcSetURLs(content) {
		add(url, "srcset")
	}
	for _, match := range htmlCSSAssetURLPattern.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			add(match[1], "css_url")
		}
	}
	for _, url := range cssImageSetURLs(content) {
		add(url, "css_image_set")
	}
	for _, match := range htmlCSSImportURLPattern.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			add(match[1], "css_import")
		}
	}
	return refs
}

func hasAssetReference(refs []ArtifactHTMLAssetReference, kind string, sources ...string) bool {
	sourceSet := map[string]bool{}
	for _, source := range sources {
		sourceSet[source] = true
	}
	for _, ref := range refs {
		if ref.Kind == kind && sourceSet[ref.Source] {
			return true
		}
	}
	return false
}

func srcSetURLs(content string) []string {
	urls := make([]string, 0)
	for _, match := range htmlSrcSetPattern.FindAllStringSubmatch(content, -1) {
		if len(match) <= 1 {
			continue
		}
		for _, candidate := range strings.Split(match[1], ",") {
			fields := strings.Fields(strings.TrimSpace(candidate))
			if len(fields) > 0 {
				urls = append(urls, fields[0])
			}
		}
	}
	return urls
}

func cssImageSetURLs(content string) []string {
	urls := make([]string, 0)
	for _, match := range htmlCSSImageSetPattern.FindAllStringSubmatch(content, -1) {
		if len(match) <= 1 {
			continue
		}
		for _, quoted := range htmlCSSQuotedURLPattern.FindAllStringSubmatch(match[1], -1) {
			if len(quoted) > 1 {
				urls = append(urls, strings.TrimSpace(quoted[1]))
			}
		}
	}
	return urls
}

func assetURLKind(raw string) string {
	switch {
	case isRemoteAssetURL(raw):
		return "remote"
	case isLocalAssetURL(raw):
		return "local"
	default:
		return ""
	}
}

func assetReferenceAction(kind string) string {
	switch kind {
	case "local":
		return "bundle"
	case "remote":
		return "inline_or_mirror"
	default:
		return "review"
	}
}

func isRemoteAssetURL(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(value, "http://") ||
		strings.HasPrefix(value, "https://") ||
		strings.HasPrefix(value, "//")
}

func isLocalAssetURL(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "//") ||
		strings.HasPrefix(lower, "data:") ||
		strings.HasPrefix(lower, "blob:") ||
		strings.HasPrefix(lower, "#") ||
		strings.HasPrefix(lower, "mailto:") ||
		strings.HasPrefix(lower, "tel:") ||
		strings.HasPrefix(lower, "var(") {
		return false
	}
	return true
}

func hasImageWithoutAlt(content string) bool {
	for _, tag := range htmlImageTagPattern.FindAllString(content, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		if _, ok := attrs["alt"]; !ok {
			return true
		}
	}
	return false
}

func hasImageInputWithoutAlt(content string) bool {
	for _, tag := range htmlFormControlTagPattern.FindAllString(content, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		if !strings.EqualFold(strings.TrimSpace(attrs["type"]), "image") {
			continue
		}
		if strings.TrimSpace(attrs["alt"]) == "" {
			return true
		}
	}
	return false
}

func hasButtonWithoutAccessibleName(content string) bool {
	for _, match := range htmlButtonTagPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		attrs := parseHTMLMotionMetaAttrs(match[1])
		if strings.TrimSpace(attrs["aria-label"]) != "" || strings.TrimSpace(attrs["title"]) != "" {
			continue
		}
		visible := htmlVisibleTextStripPattern.ReplaceAllString(match[2], " ")
		visible = strings.TrimSpace(htmlWhitespacePattern.ReplaceAllString(visible, " "))
		if visible == "" {
			return true
		}
	}
	return false
}

func hasFormButtonWithoutType(content string) bool {
	for _, formMatch := range htmlFormBlockPattern.FindAllStringSubmatch(content, -1) {
		if len(formMatch) != 2 {
			continue
		}
		for _, buttonMatch := range htmlButtonTagPattern.FindAllStringSubmatch(formMatch[1], -1) {
			if len(buttonMatch) != 3 {
				continue
			}
			attrs := parseHTMLMotionMetaAttrs(buttonMatch[1])
			if strings.TrimSpace(attrs["type"]) == "" {
				return true
			}
		}
	}
	return false
}

func hasVideoWithoutPoster(content string) bool {
	for _, match := range htmlVideoBlockPattern.FindAllStringSubmatch(content, -1) {
		if len(match) < 2 {
			continue
		}
		attrs := parseHTMLMotionMetaAttrs(match[1])
		if isARIAHidden(attrs) {
			continue
		}
		if strings.TrimSpace(attrs["poster"]) == "" {
			return true
		}
	}
	return false
}

func hasMediaWithoutCaptions(content string) bool {
	for _, match := range htmlVideoBlockPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		attrs := parseHTMLMotionMetaAttrs(match[1])
		if isARIAHidden(attrs) {
			continue
		}
		if htmlCaptionTrackPattern.FindStringIndex(match[2]) == nil {
			return true
		}
	}
	for _, match := range htmlAudioBlockPattern.FindAllStringSubmatch(content, -1) {
		if len(match) != 3 {
			continue
		}
		attrs := parseHTMLMotionMetaAttrs(match[1])
		if isARIAHidden(attrs) {
			continue
		}
		if htmlCaptionTrackPattern.FindStringIndex(match[2]) == nil {
			return true
		}
	}
	return false
}

func isARIAHidden(attrs map[string]string) bool {
	return strings.EqualFold(strings.TrimSpace(attrs["aria-hidden"]), "true")
}

func hasFormControlWithoutName(content string) bool {
	for _, tag := range htmlFormControlTagPattern.FindAllString(content, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		if isNonDataFormControl(tag, attrs) {
			continue
		}
		if strings.TrimSpace(attrs["name"]) == "" {
			return true
		}
	}
	return false
}

func hasFormControlWithoutLabel(content string) bool {
	labelForIDs := htmlLabelForIDs(content)
	labelBlocks := htmlLabelBlockPattern.FindAllString(content, -1)
	for _, tag := range htmlFormControlTagPattern.FindAllString(content, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		if isNonLabelledFormControl(tag, attrs) {
			continue
		}
		if strings.TrimSpace(attrs["aria-label"]) != "" ||
			strings.TrimSpace(attrs["aria-labelledby"]) != "" ||
			strings.TrimSpace(attrs["title"]) != "" {
			continue
		}
		id := strings.TrimSpace(attrs["id"])
		if id != "" && labelForIDs[id] {
			continue
		}
		if isFormControlWrappedByLabel(tag, labelBlocks) {
			continue
		}
		return true
	}
	return false
}

func htmlLabelForIDs(content string) map[string]bool {
	ids := map[string]bool{}
	for _, match := range htmlLabelForPattern.FindAllStringSubmatch(content, -1) {
		if len(match) > 1 {
			id := strings.TrimSpace(match[1])
			if id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

func isFormControlWrappedByLabel(tag string, labelBlocks []string) bool {
	for _, block := range labelBlocks {
		if strings.Contains(block, tag) {
			return true
		}
	}
	return false
}

func isNonDataFormControl(tag string, attrs map[string]string) bool {
	if strings.HasPrefix(strings.ToLower(tag), "<input") {
		switch strings.ToLower(strings.TrimSpace(attrs["type"])) {
		case "button", "submit", "reset", "image", "hidden":
			return true
		}
	}
	return false
}

func isNonLabelledFormControl(tag string, attrs map[string]string) bool {
	if strings.HasPrefix(strings.ToLower(tag), "<input") {
		switch strings.ToLower(strings.TrimSpace(attrs["type"])) {
		case "button", "submit", "reset", "image", "hidden":
			return true
		}
	}
	return false
}

func htmlValidationResult(issues []ArtifactHTMLValidationIssue, refs []ArtifactHTMLAssetReference) ArtifactHTMLValidationResult {
	status := ArtifactHTMLValidationPass
	for _, issue := range issues {
		if issue.Severity == "block" {
			status = ArtifactHTMLValidationBlock
			break
		}
		if issue.Severity == "warn" {
			status = ArtifactHTMLValidationWarn
		}
	}
	return ArtifactHTMLValidationResult{
		Status:          status,
		IssueCount:      len(issues),
		Issues:          issues,
		AssetReferences: refs,
	}
}
