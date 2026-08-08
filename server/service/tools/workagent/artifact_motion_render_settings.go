package workagent

import (
	"regexp"
	"strconv"
	"strings"
)

type HTMLMotionRenderSettings struct {
	DurationMs int
	FPS        int
	Width      int
	Height     int
}

var (
	motionMetaTagPattern = regexp.MustCompile(`(?is)<meta\s+[^>]*>`)
	motionAttrPattern    = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*["']([^"']*)["']`)
)

func ExtractHTMLMotionRenderSettings(html string) HTMLMotionRenderSettings {
	settings := HTMLMotionRenderSettings{}
	for _, tag := range motionMetaTagPattern.FindAllString(html, -1) {
		attrs := parseHTMLMotionMetaAttrs(tag)
		name := strings.ToLower(strings.TrimSpace(attrs["name"]))
		content := strings.TrimSpace(attrs["content"])
		if name == "" || content == "" {
			continue
		}
		switch name {
		case "workmax:motion-duration-ms":
			settings.DurationMs = clampMotionSettingInt(content, 100, 60000)
		case "workmax:motion-fps":
			settings.FPS = clampMotionSettingInt(content, 1, 120)
		case "workmax:motion-width":
			settings.Width = clampMotionSettingInt(content, 1, 10000)
		case "workmax:motion-height":
			settings.Height = clampMotionSettingInt(content, 1, 10000)
		}
	}
	return settings
}

func parseHTMLMotionMetaAttrs(tag string) map[string]string {
	attrs := map[string]string{}
	for _, match := range motionAttrPattern.FindAllStringSubmatch(tag, -1) {
		if len(match) != 3 {
			continue
		}
		attrs[strings.ToLower(strings.TrimSpace(match[1]))] = strings.TrimSpace(match[2])
	}
	return attrs
}

func clampMotionSettingInt(raw string, min int, max int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || parsed < min || parsed > max {
		return 0
	}
	return parsed
}
