package tools

import "strings"

// Shared LLM-prompt text helpers. These were originally defined in
// short_drama_outline_api.go because that's where the first caller
// landed, but every solution that builds a prompt with structured
// user input — short-drama, comic-video, ecommerce, ad-script,
// product-analyze, video-generator — reaches into the same toolkit.
// Living here makes the dependency explicit instead of relying on
// "they happen to share a package".
//
// Keep this file pure: no globals, no DB, no Gin. Anything that needs
// state belongs in the caller.

// sanitizeBriefLine collapses newlines and strips angle brackets so
// user input can't close a `<project_brief>` / `<comic_brief>` /
// similar tag or forge a header. Capped at 500 chars so a single
// pasted blob doesn't blow past the model's context window.
func sanitizeBriefLine(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "<", "")
	s = strings.ReplaceAll(s, ">", "")
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

// sanitizeBriefBlock keeps paragraph breaks (synopsis / reference
// works can be multi-sentence) but still strips tag-closing chars.
// Cap is 2000 chars — anything longer is almost certainly a prompt-
// injection payload masquerading as backstory.
func sanitizeBriefBlock(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "<", "")
	s = strings.ReplaceAll(s, ">", "")
	if len(s) > 2000 {
		s = s[:2000]
	}
	return s
}

// extractString safely pulls a string out of an `interface{}` (the
// shape we get back from json.Unmarshal). Returns "" for nil / wrong
// type so callers can chain TrimSpace / firstNonEmpty without nil
// checks.
func extractString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// extractInt narrows an `interface{}` (json.Unmarshal lands floats
// for all numeric types) into an int. Second return is false when
// the value isn't numeric — caller decides whether to default.
func extractInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// truncateRunes caps by rune count so CJK / emoji content doesn't
// get sliced mid-codepoint. Returns "" when max <= 0.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
