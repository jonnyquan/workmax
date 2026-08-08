package workagent

import "testing"

func TestExtractHTMLMotionRenderSettings(t *testing.T) {
	html := `<!doctype html><html><head>
<meta name="workmax:motion-duration-ms" content="3500">
<meta content="24" name="workmax:motion-fps">
<meta name="workmax:motion-width" content="1080">
<meta name="workmax:motion-height" content="1920">
</head><body>motion</body></html>`

	got := ExtractHTMLMotionRenderSettings(html)
	if got.DurationMs != 3500 || got.FPS != 24 || got.Width != 1080 || got.Height != 1920 {
		t.Fatalf("settings = %#v", got)
	}
}

func TestExtractHTMLMotionRenderSettingsRejectsOutOfRangeValues(t *testing.T) {
	html := `<!doctype html><html><head>
<meta name="workmax:motion-duration-ms" content="10">
<meta name="workmax:motion-fps" content="240">
<meta name="workmax:motion-width" content="0">
<meta name="workmax:motion-height" content="bad">
</head><body>motion</body></html>`

	got := ExtractHTMLMotionRenderSettings(html)
	if got.DurationMs != 0 || got.FPS != 0 || got.Width != 0 || got.Height != 0 {
		t.Fatalf("settings = %#v, want empty settings for invalid values", got)
	}
}
