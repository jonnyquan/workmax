package workagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserCommandMotionRenderer_RenderMotionHTMLMP4(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "story.html")
	outputPath := filepath.Join(dir, "story.mp4")
	envPath := filepath.Join(dir, "env.txt")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>motion</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	commandPath := writeFakeMotionRenderCommand(t, dir)
	t.Setenv("WORKMAX_FAKE_MOTION_ENV_FILE", envPath)

	renderer := NewBrowserCommandMotionRenderer(commandPath)
	out, err := renderer.RenderMotionHTML(context.Background(), HTMLMotionRenderInput{
		SourcePath:      sourcePath,
		Target:          "mp4",
		OutputExtension: ".mp4",
		WorkspaceRoot:   dir,
		OutputFileAbs:   outputPath,
		MotionSettings: HTMLMotionRenderSettings{
			DurationMs: 3500,
			FPS:        24,
			Width:      1080,
			Height:     1920,
		},
	})
	if err != nil {
		t.Fatalf("render mp4: %v", err)
	}
	if !bytes.Contains(out.Content, []byte("ftyp")) || out.MimeType != "video/mp4" {
		t.Fatalf("output = %q/%q, want fake-mp4/video/mp4", out.Content, out.MimeType)
	}
	envBytes, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env: %v", err)
	}
	env := string(envBytes)
	for _, want := range []string{
		"target=mp4",
		"source=" + sourcePath,
		"output=" + outputPath,
		"workspace=" + dir,
		"extension=.mp4",
		"source_url=file://",
		"duration_ms=3500",
		"fps=24",
		"width=1080",
		"height=1920",
	} {
		if !strings.Contains(env, want) {
			t.Fatalf("env missing %q in:\n%s", want, env)
		}
	}
}

func TestBrowserCommandMotionRenderer_RenderMotionHTMLGIF(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "banner.html")
	outputPath := filepath.Join(dir, "banner.gif")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>gif</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	commandPath := writeFakeMotionRenderCommand(t, dir)

	renderer := NewBrowserCommandMotionRenderer(commandPath)
	out, err := renderer.RenderMotionHTML(context.Background(), HTMLMotionRenderInput{
		SourcePath:      sourcePath,
		Target:          "gif",
		OutputExtension: ".gif",
		WorkspaceRoot:   dir,
		OutputFileAbs:   outputPath,
	})
	if err != nil {
		t.Fatalf("render gif: %v", err)
	}
	if !bytes.HasPrefix(out.Content, []byte("GIF89a")) || out.MimeType != "image/gif" {
		t.Fatalf("output = %q/%q, want fake-gif/image/gif", out.Content, out.MimeType)
	}
}

func TestBrowserCommandMotionRenderer_RejectsWrongOutputSignature(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "story.html")
	outputPath := filepath.Join(dir, "story.mp4")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>motion</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	commandPath := writeFakeMotionRenderCommand(t, dir)
	t.Setenv("WORKMAX_FAKE_MOTION_BAD_OUTPUT", "1")

	renderer := NewBrowserCommandMotionRenderer(commandPath)
	_, err := renderer.RenderMotionHTML(context.Background(), HTMLMotionRenderInput{
		SourcePath:    sourcePath,
		Target:        "mp4",
		OutputFileAbs: outputPath,
	})
	if err == nil || !strings.Contains(err.Error(), "output does not look like mp4") {
		t.Fatalf("err = %v, want signature failure", err)
	}
}

func TestBrowserCommandMotionRenderer_RequiresConfiguredCommand(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_MOTION_RENDER_BIN", "")

	_, err := NewDefaultBrowserCommandMotionRenderer()
	if err == nil || !strings.Contains(err.Error(), "WORKMAX_WORKAGENT_MOTION_RENDER_BIN is not configured") {
		t.Fatalf("err = %v, want missing env configuration", err)
	}
}

func TestBrowserCommandMotionRenderer_RejectsUnsupportedTarget(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "story.html")
	outputPath := filepath.Join(dir, "story.webm")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>motion</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	commandPath := writeFakeMotionRenderCommand(t, dir)

	renderer := NewBrowserCommandMotionRenderer(commandPath)
	_, err := renderer.RenderMotionHTML(context.Background(), HTMLMotionRenderInput{
		SourcePath:    sourcePath,
		Target:        "webm",
		OutputFileAbs: outputPath,
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported target webm") {
		t.Fatalf("err = %v, want unsupported target", err)
	}
}

func writeFakeMotionRenderCommand(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-motion-render")
	script := `#!/bin/sh
if [ -n "$WORKMAX_FAKE_MOTION_ENV_FILE" ]; then
  {
    printf 'target=%s\n' "$WORKMAX_WORKAGENT_MOTION_TARGET"
    printf 'source=%s\n' "$WORKMAX_WORKAGENT_MOTION_SOURCE"
    printf 'source_url=%s\n' "$WORKMAX_WORKAGENT_MOTION_SOURCE_URL"
    printf 'output=%s\n' "$WORKMAX_WORKAGENT_MOTION_OUTPUT"
    printf 'workspace=%s\n' "$WORKMAX_WORKAGENT_MOTION_WORKSPACE_ROOT"
    printf 'extension=%s\n' "$WORKMAX_WORKAGENT_MOTION_OUTPUT_EXTENSION"
    printf 'duration_ms=%s\n' "$WORKMAX_WORKAGENT_MOTION_DURATION_MS"
    printf 'fps=%s\n' "$WORKMAX_WORKAGENT_MOTION_FPS"
    printf 'width=%s\n' "$WORKMAX_WORKAGENT_MOTION_WIDTH"
    printf 'height=%s\n' "$WORKMAX_WORKAGENT_MOTION_HEIGHT"
  } > "$WORKMAX_FAKE_MOTION_ENV_FILE"
fi
case "$WORKMAX_WORKAGENT_MOTION_TARGET" in
  mp4) if [ -n "$WORKMAX_FAKE_MOTION_BAD_OUTPUT" ]; then printf 'not-an-mp4' > "$WORKMAX_WORKAGENT_MOTION_OUTPUT"; else printf '\000\000\000\030ftypmp42fake-mp4' > "$WORKMAX_WORKAGENT_MOTION_OUTPUT"; fi ;;
  gif) if [ -n "$WORKMAX_FAKE_MOTION_BAD_OUTPUT" ]; then printf 'not-a-gif' > "$WORKMAX_WORKAGENT_MOTION_OUTPUT"; else printf 'GIF89afake-gif' > "$WORKMAX_WORKAGENT_MOTION_OUTPUT"; fi ;;
  *) exit 2 ;;
esac
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake motion render command: %v", err)
	}
	return path
}
