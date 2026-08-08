package workagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserCommandStaticRenderer_RenderStaticHTMLPDF(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	outputPath := filepath.Join(dir, "page.pdf")
	argsPath := filepath.Join(dir, "args.txt")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>hello</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	t.Setenv("WORKMAX_FAKE_BROWSER_ARGS_FILE", argsPath)

	renderer := &BrowserCommandStaticRenderer{
		BrowserPath:    browserPath,
		ViewportWidth:  1200,
		ViewportHeight: 800,
	}
	out, err := renderer.RenderStaticHTML(context.Background(), HTMLStaticRenderInput{
		SourcePath:    sourcePath,
		Target:        "pdf",
		OutputFileAbs: outputPath,
	})
	if err != nil {
		t.Fatalf("render pdf: %v", err)
	}
	if !bytes.HasPrefix(out.Content, []byte("%PDF")) || out.MimeType != "application/pdf" {
		t.Fatalf("output = %q/%q", out.Content, out.MimeType)
	}
	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	args := string(argsBytes)
	if !strings.Contains(args, "--print-to-pdf="+outputPath) {
		t.Fatalf("args missing print-to-pdf output: %s", args)
	}
	if !strings.Contains(args, "--window-size=1200,800") {
		t.Fatalf("args missing window size: %s", args)
	}
	if !strings.Contains(args, "file://") || !strings.Contains(args, "page.html") {
		t.Fatalf("args missing file URL: %s", args)
	}
}

func TestBrowserCommandStaticRenderer_RenderStaticHTMLPNG(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	outputPath := filepath.Join(dir, "page.png")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>hello</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)

	renderer := &BrowserCommandStaticRenderer{BrowserPath: browserPath}
	out, err := renderer.RenderStaticHTML(context.Background(), HTMLStaticRenderInput{
		SourcePath:    sourcePath,
		Target:        "png",
		OutputFileAbs: outputPath,
	})
	if err != nil {
		t.Fatalf("render png: %v", err)
	}
	if !bytes.HasPrefix(out.Content, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) || out.MimeType != "image/png" {
		t.Fatalf("output = %q/%q", out.Content, out.MimeType)
	}
}

func TestBrowserCommandStaticRenderer_RejectsWrongOutputSignature(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "page.html")
	outputPath := filepath.Join(dir, "page.pdf")
	if err := os.WriteFile(sourcePath, []byte("<!doctype html><html><body>hello</body></html>"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	browserPath := writeFakeBrowserCommand(t, dir)
	t.Setenv("WORKMAX_FAKE_BROWSER_BAD_OUTPUT", "1")

	renderer := &BrowserCommandStaticRenderer{BrowserPath: browserPath}
	_, err := renderer.RenderStaticHTML(context.Background(), HTMLStaticRenderInput{
		SourcePath:    sourcePath,
		Target:        "pdf",
		OutputFileAbs: outputPath,
	})
	if err == nil || !strings.Contains(err.Error(), "output does not look like pdf") {
		t.Fatalf("render err = %v, want signature failure", err)
	}
}

func writeFakeBrowserCommand(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-browser")
	script := `#!/bin/sh
out=""
dump_dom=""
window_size=""
for arg in "$@"; do
  if [ -n "$WORKMAX_FAKE_BROWSER_ARGS_FILE" ]; then
    printf '%s\n' "$arg" >> "$WORKMAX_FAKE_BROWSER_ARGS_FILE"
  fi
  case "$arg" in
    --print-to-pdf=*) out="${arg#--print-to-pdf=}"; if [ -n "$WORKMAX_FAKE_BROWSER_BAD_OUTPUT" ]; then printf 'not-a-pdf' > "$out"; else printf '%%PDF-1.4\nfake-pdf' > "$out"; fi ;;
    --screenshot=*) out="${arg#--screenshot=}"; if [ -n "$WORKMAX_FAKE_BROWSER_BAD_OUTPUT" ]; then printf 'not-a-png' > "$out"; elif [ -n "$WORKMAX_FAKE_BROWSER_SCREENSHOT_BY_SIZE" ]; then printf '\211PNG\r\n\032\nfake-png-%s' "$window_size" > "$out"; else printf '\211PNG\r\n\032\nfake-png' > "$out"; fi ;;
    --window-size=*) window_size="${arg#--window-size=}" ;;
    --dump-dom) dump_dom="1" ;;
  esac
done
if [ -n "$dump_dom" ]; then
  if [ -n "$WORKMAX_FAKE_BROWSER_LAYOUT_PROBE" ]; then
    printf '%s\n' "$WORKMAX_FAKE_BROWSER_LAYOUT_PROBE"
  else
    printf '%s\n' 'WORKMAX_LAYOUT_PROBE_START{"textOverflow":false,"scrollBounds":false}WORKMAX_LAYOUT_PROBE_END'
  fi
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake browser: %v", err)
	}
	return path
}
