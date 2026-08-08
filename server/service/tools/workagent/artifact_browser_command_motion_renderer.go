package workagent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const defaultMotionRenderTimeout = 3 * time.Minute

type BrowserCommandMotionRenderer struct {
	CommandPath string
	Timeout     time.Duration
}

func NewBrowserCommandMotionRenderer(commandPath string) *BrowserCommandMotionRenderer {
	return &BrowserCommandMotionRenderer{CommandPath: commandPath}
}

func NewDefaultBrowserCommandMotionRenderer() (*BrowserCommandMotionRenderer, error) {
	path, err := resolveMotionRenderCommandPath("")
	if err != nil {
		return nil, err
	}
	return NewBrowserCommandMotionRenderer(path), nil
}

func (r *BrowserCommandMotionRenderer) RenderMotionHTML(ctx context.Context, input HTMLMotionRenderInput) (HTMLMotionRenderOutput, error) {
	if r == nil {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: nil renderer")
	}
	commandPath, err := resolveMotionRenderCommandPath(r.CommandPath)
	if err != nil {
		return HTMLMotionRenderOutput{}, err
	}
	target := strings.ToLower(strings.TrimSpace(input.Target))
	if target != "mp4" && target != "gif" {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: unsupported target %s", input.Target)
	}
	if strings.TrimSpace(input.SourcePath) == "" || strings.TrimSpace(input.OutputFileAbs) == "" {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: source and output paths are required")
	}
	if err := os.MkdirAll(filepath.Dir(input.OutputFileAbs), 0o755); err != nil {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: create output dir: %w", err)
	}
	_ = os.Remove(input.OutputFileAbs)

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultMotionRenderTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, commandPath)
	cmd.Dir = filepath.Dir(input.SourcePath)
	cmd.Env = appendMotionRenderSettingsEnv(append(os.Environ(),
		"WORKMAX_WORKAGENT_MOTION_SOURCE="+input.SourcePath,
		"WORKMAX_WORKAGENT_MOTION_SOURCE_URL="+fileURL(input.SourcePath),
		"WORKMAX_WORKAGENT_MOTION_OUTPUT="+input.OutputFileAbs,
		"WORKMAX_WORKAGENT_MOTION_TARGET="+target,
		"WORKMAX_WORKAGENT_MOTION_WORKSPACE_ROOT="+input.WorkspaceRoot,
		"WORKMAX_WORKAGENT_MOTION_OUTPUT_EXTENSION="+input.OutputExtension,
	), input.MotionSettings)
	output, err := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: render timed out after %s", timeout)
	}
	if err != nil {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	content, err := os.ReadFile(input.OutputFileAbs)
	if err != nil {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: read output: %w", err)
	}
	if len(content) == 0 {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: output is empty")
	}
	if !hasMotionRenderOutputSignature(target, content) {
		return HTMLMotionRenderOutput{}, fmt.Errorf("browser command motion renderer: output does not look like %s", target)
	}
	return HTMLMotionRenderOutput{
		Content:  content,
		MimeType: mimeTypeForMotionRenderTarget(target),
	}, nil
}

func appendMotionRenderSettingsEnv(env []string, settings HTMLMotionRenderSettings) []string {
	if settings.DurationMs > 0 {
		env = append(env, fmt.Sprintf("WORKMAX_WORKAGENT_MOTION_DURATION_MS=%d", settings.DurationMs))
	}
	if settings.FPS > 0 {
		env = append(env, fmt.Sprintf("WORKMAX_WORKAGENT_MOTION_FPS=%d", settings.FPS))
	}
	if settings.Width > 0 {
		env = append(env, fmt.Sprintf("WORKMAX_WORKAGENT_MOTION_WIDTH=%d", settings.Width))
	}
	if settings.Height > 0 {
		env = append(env, fmt.Sprintf("WORKMAX_WORKAGENT_MOTION_HEIGHT=%d", settings.Height))
	}
	return env
}

func hasMotionRenderOutputSignature(target string, content []byte) bool {
	switch target {
	case "mp4":
		return len(content) >= 12 && string(content[4:8]) == "ftyp"
	case "gif":
		return len(content) >= 6 && (string(content[:6]) == "GIF87a" || string(content[:6]) == "GIF89a")
	default:
		return false
	}
}

func resolveMotionRenderCommandPath(configured string) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		if filepath.IsAbs(configured) {
			if _, err := os.Stat(configured); err != nil {
				return "", fmt.Errorf("browser command motion renderer: configured command not found: %w", err)
			}
			return configured, nil
		}
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("browser command motion renderer: configured command not found: %w", err)
		}
		return path, nil
	}
	if env := strings.TrimSpace(os.Getenv("WORKMAX_WORKAGENT_MOTION_RENDER_BIN")); env != "" {
		return resolveMotionRenderCommandPath(env)
	}
	return "", fmt.Errorf("browser command motion renderer: WORKMAX_WORKAGENT_MOTION_RENDER_BIN is not configured")
}
