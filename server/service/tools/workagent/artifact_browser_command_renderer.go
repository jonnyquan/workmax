package workagent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultStaticRenderTimeout = 60 * time.Second

type BrowserCommandStaticRenderer struct {
	BrowserPath    string
	Timeout        time.Duration
	ViewportWidth  int
	ViewportHeight int
}

func NewBrowserCommandStaticRenderer(browserPath string) *BrowserCommandStaticRenderer {
	return &BrowserCommandStaticRenderer{BrowserPath: browserPath}
}

func NewDefaultBrowserCommandStaticRenderer() (*BrowserCommandStaticRenderer, error) {
	path, err := resolveBrowserCommandPath("")
	if err != nil {
		return nil, err
	}
	return NewBrowserCommandStaticRenderer(path), nil
}

func (r *BrowserCommandStaticRenderer) RenderStaticHTML(ctx context.Context, input HTMLStaticRenderInput) (HTMLStaticRenderOutput, error) {
	if r == nil {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: nil renderer")
	}
	browserPath, err := resolveBrowserCommandPath(r.BrowserPath)
	if err != nil {
		return HTMLStaticRenderOutput{}, err
	}
	target := strings.ToLower(strings.TrimSpace(input.Target))
	if target != "pdf" && target != "png" {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: unsupported target %s", input.Target)
	}
	if strings.TrimSpace(input.SourcePath) == "" || strings.TrimSpace(input.OutputFileAbs) == "" {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: source and output paths are required")
	}
	if err := os.MkdirAll(filepath.Dir(input.OutputFileAbs), 0o755); err != nil {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: create output dir: %w", err)
	}
	_ = os.Remove(input.OutputFileAbs)

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultStaticRenderTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := r.renderArgs(target, input)
	cmd := exec.CommandContext(runCtx, browserPath, args...)
	cmd.Dir = filepath.Dir(input.SourcePath)
	output, err := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: render timed out after %s", timeout)
	}
	if err != nil {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	content, err := os.ReadFile(input.OutputFileAbs)
	if err != nil {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: read output: %w", err)
	}
	if len(content) == 0 {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: output is empty")
	}
	if !hasStaticRenderOutputSignature(target, content) {
		return HTMLStaticRenderOutput{}, fmt.Errorf("browser command renderer: output does not look like %s", target)
	}
	return HTMLStaticRenderOutput{
		Content:  content,
		MimeType: mimeTypeForStaticRenderTarget(target),
	}, nil
}

func hasStaticRenderOutputSignature(target string, content []byte) bool {
	switch target {
	case "pdf":
		return len(content) >= 4 && string(content[:4]) == "%PDF"
	case "png":
		return len(content) >= 8 &&
			content[0] == 0x89 &&
			content[1] == 'P' &&
			content[2] == 'N' &&
			content[3] == 'G' &&
			content[4] == '\r' &&
			content[5] == '\n' &&
			content[6] == 0x1a &&
			content[7] == '\n'
	default:
		return false
	}
}

func (r *BrowserCommandStaticRenderer) renderArgs(target string, input HTMLStaticRenderInput) []string {
	width := r.ViewportWidth
	if width <= 0 {
		width = 1440
	}
	height := r.ViewportHeight
	if height <= 0 {
		height = 900
	}
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--no-default-browser-check",
		fmt.Sprintf("--window-size=%d,%d", width, height),
	}
	switch target {
	case "pdf":
		args = append(args, "--print-to-pdf="+input.OutputFileAbs)
	case "png":
		args = append(args, "--screenshot="+input.OutputFileAbs)
	}
	sourceURL := strings.TrimSpace(input.SourceURL)
	if sourceURL == "" {
		sourceURL = fileURL(input.SourcePath)
	}
	args = append(args, sourceURL)
	return args
}

func resolveBrowserCommandPath(configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		if filepath.IsAbs(configured) {
			if _, err := os.Stat(configured); err != nil {
				return "", fmt.Errorf("browser command renderer: configured browser not found: %w", err)
			}
			return configured, nil
		}
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("browser command renderer: configured browser not found: %w", err)
		}
		return path, nil
	}
	if env := strings.TrimSpace(os.Getenv("WORKMAX_WORKAGENT_BROWSER_BIN")); env != "" {
		return resolveBrowserCommandPath(env)
	}
	for _, candidate := range []string{
		"chromium",
		"chromium-browser",
		"google-chrome",
		"google-chrome-stable",
		"chrome",
		"msedge",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		path, err := resolveBrowserCommandPath(candidate)
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("browser command renderer: no Chrome/Chromium executable found")
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
