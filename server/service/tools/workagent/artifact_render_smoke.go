package workagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ArtifactRenderSmokeResult struct {
	Worker     string `json:"worker"`
	Target     string `json:"target"`
	Status     string `json:"status"`
	MimeType   string `json:"mimeType,omitempty"`
	Bytes      int    `json:"bytes,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Hint       string `json:"hint,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
}

type ArtifactRenderSmokeSnapshot struct {
	Status      string                      `json:"status"`
	Total       int                         `json:"total"`
	Passed      int                         `json:"passed"`
	Failed      int                         `json:"failed"`
	Unavailable int                         `json:"unavailable"`
	Static      []ArtifactRenderSmokeResult `json:"static"`
	Motion      []ArtifactRenderSmokeResult `json:"motion"`
}

func RunDefaultArtifactRenderSmoke(ctx context.Context) ArtifactRenderSmokeSnapshot {
	return RunArtifactRenderSmoke(ctx, nil, nil)
}

func RunArtifactRenderSmoke(ctx context.Context, staticRenderer HTMLStaticRenderer, motionRenderer HTMLMotionRenderer) ArtifactRenderSmokeSnapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	root, err := os.MkdirTemp("", "workmax-workagent-render-smoke-*")
	if err != nil {
		msg := fmt.Sprintf("create smoke workspace: %v", err)
		return newArtifactRenderSmokeSnapshot(
			unavailableSmokeResults(ArtifactStaticRenderWorkerName, []string{"pdf", "png"}, msg),
			unavailableSmokeResults(ArtifactMotionRenderWorkerName, []string{"mp4", "gif"}, msg),
		)
	}
	defer func() { _ = os.RemoveAll(root) }()

	sourcePath := filepath.Join(root, "render-smoke.html")
	if err := os.WriteFile(sourcePath, []byte(renderSmokeHTML), 0o644); err != nil {
		msg := fmt.Sprintf("write smoke html: %v", err)
		return newArtifactRenderSmokeSnapshot(
			unavailableSmokeResults(ArtifactStaticRenderWorkerName, []string{"pdf", "png"}, msg),
			unavailableSmokeResults(ArtifactMotionRenderWorkerName, []string{"mp4", "gif"}, msg),
		)
	}

	staticResults := make([]ArtifactRenderSmokeResult, 0, 2)
	if staticRenderer == nil {
		renderer, err := NewDefaultBrowserCommandStaticRenderer()
		if err != nil {
			staticResults = unavailableSmokeResults(ArtifactStaticRenderWorkerName, []string{"pdf", "png"}, err.Error())
		} else {
			staticRenderer = renderer
		}
	}
	if staticRenderer != nil {
		for _, target := range []string{"pdf", "png"} {
			staticResults = append(staticResults, runStaticRenderSmokeTarget(ctx, staticRenderer, root, sourcePath, target))
		}
	}

	motionResults := make([]ArtifactRenderSmokeResult, 0, 2)
	if motionRenderer == nil {
		renderer, err := NewDefaultBrowserCommandMotionRenderer()
		if err != nil {
			motionResults = unavailableSmokeResults(ArtifactMotionRenderWorkerName, []string{"mp4", "gif"}, err.Error())
		} else {
			motionRenderer = renderer
		}
	}
	if motionRenderer != nil {
		for _, target := range []string{"mp4", "gif"} {
			motionResults = append(motionResults, runMotionRenderSmokeTarget(ctx, motionRenderer, root, sourcePath, target))
		}
	}

	return newArtifactRenderSmokeSnapshot(staticResults, motionResults)
}

func newArtifactRenderSmokeSnapshot(staticResults, motionResults []ArtifactRenderSmokeResult) ArtifactRenderSmokeSnapshot {
	snapshot := ArtifactRenderSmokeSnapshot{
		Status: "unknown",
		Static: staticResults,
		Motion: motionResults,
	}
	for _, result := range append(append([]ArtifactRenderSmokeResult{}, staticResults...), motionResults...) {
		snapshot.Total++
		switch result.Status {
		case "passed":
			snapshot.Passed++
		case "unavailable":
			snapshot.Unavailable++
		default:
			snapshot.Failed++
		}
	}
	switch {
	case snapshot.Failed > 0:
		snapshot.Status = "failed"
	case snapshot.Unavailable > 0:
		snapshot.Status = "unavailable"
	case snapshot.Total > 0 && snapshot.Passed == snapshot.Total:
		snapshot.Status = "passed"
	}
	return snapshot
}

func unavailableSmokeResults(worker string, targets []string, err string) []ArtifactRenderSmokeResult {
	out := make([]ArtifactRenderSmokeResult, 0, len(targets))
	for _, target := range targets {
		out = append(out, ArtifactRenderSmokeResult{
			Worker: worker,
			Target: target,
			Status: "unavailable",
			Reason: classifyRenderSmokeReason(worker, err),
			Hint:   renderSmokeHint(worker, classifyRenderSmokeReason(worker, err)),
			Error:  err,
		})
	}
	return out
}

func runStaticRenderSmokeTarget(ctx context.Context, renderer HTMLStaticRenderer, root, sourcePath, target string) ArtifactRenderSmokeResult {
	start := time.Now()
	result := ArtifactRenderSmokeResult{Worker: ArtifactStaticRenderWorkerName, Target: target}
	outputAbs := filepath.Join(root, "render-smoke."+target)
	outputExt := "." + target
	output, err := renderer.RenderStaticHTML(ctx, HTMLStaticRenderInput{
		SourcePath:      sourcePath,
		SourceHTML:      renderSmokeHTML,
		Target:          target,
		OutputExtension: outputExt,
		WorkspaceRoot:   root,
		OutputFileName:  "render-smoke." + target,
		OutputFilePath:  "render-smoke." + target,
		OutputFileAbs:   outputAbs,
	})
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Reason = classifyRenderSmokeReason(result.Worker, result.Error)
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		return result
	}
	if len(output.Content) == 0 {
		result.Status = "failed"
		result.Error = fmt.Sprintf("render smoke: renderer returned empty %s output", target)
		result.Reason = "empty_render_output"
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		return result
	}
	if err := validateRenderMimeType(target, output.MimeType); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Reason = "mime_type_mismatch"
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		result.MimeType = output.MimeType
		result.Bytes = len(output.Content)
		return result
	}
	if err := validateRenderOutputSignature(target, output.Content); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Reason = "render_output_signature_mismatch"
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		result.MimeType = output.MimeType
		result.Bytes = len(output.Content)
		return result
	}
	result.Status = "passed"
	result.MimeType = output.MimeType
	result.Bytes = len(output.Content)
	return result
}

func runMotionRenderSmokeTarget(ctx context.Context, renderer HTMLMotionRenderer, root, sourcePath, target string) ArtifactRenderSmokeResult {
	start := time.Now()
	result := ArtifactRenderSmokeResult{Worker: ArtifactMotionRenderWorkerName, Target: target}
	outputAbs := filepath.Join(root, "render-smoke."+target)
	outputExt := "." + target
	output, err := renderer.RenderMotionHTML(ctx, HTMLMotionRenderInput{
		SourcePath:      sourcePath,
		SourceHTML:      renderSmokeHTML,
		Target:          target,
		OutputExtension: outputExt,
		WorkspaceRoot:   root,
		OutputFileName:  "render-smoke." + target,
		OutputFilePath:  "render-smoke." + target,
		OutputFileAbs:   outputAbs,
		MotionSettings:  ExtractHTMLMotionRenderSettings(renderSmokeHTML),
	})
	result.DurationMs = time.Since(start).Milliseconds()
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Reason = classifyRenderSmokeReason(result.Worker, result.Error)
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		return result
	}
	if len(output.Content) == 0 {
		result.Status = "failed"
		result.Error = fmt.Sprintf("render smoke: renderer returned empty %s output", target)
		result.Reason = "empty_render_output"
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		return result
	}
	if err := validateRenderMimeType(target, output.MimeType); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Reason = "mime_type_mismatch"
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		result.MimeType = output.MimeType
		result.Bytes = len(output.Content)
		return result
	}
	if err := validateRenderOutputSignature(target, output.Content); err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		result.Reason = "render_output_signature_mismatch"
		result.Hint = renderSmokeHint(result.Worker, result.Reason)
		result.MimeType = output.MimeType
		result.Bytes = len(output.Content)
		return result
	}
	result.Status = "passed"
	result.MimeType = output.MimeType
	result.Bytes = len(output.Content)
	return result
}

func classifyRenderSmokeReason(worker string, message string) string {
	msg := strings.ToLower(strings.TrimSpace(message))
	switch {
	case msg == "":
		return ""
	case strings.Contains(msg, "no chrome/chromium executable found"):
		return "browser_not_found"
	case strings.Contains(msg, "configured browser not found"):
		return "browser_not_found"
	case strings.Contains(msg, "workmax_workagent_motion_render_bin is not configured"):
		return "motion_renderer_not_configured"
	case strings.Contains(msg, "configured motion renderer not found"):
		return "motion_renderer_not_found"
	case strings.Contains(msg, "timed out"):
		return "render_timeout"
	case strings.Contains(msg, "command failed"):
		return "render_command_failed"
	case strings.Contains(msg, "read output"):
		return "render_output_read_failed"
	case strings.Contains(msg, "empty"):
		return "empty_render_output"
	case strings.Contains(msg, "mime_type_mismatch"):
		return "mime_type_mismatch"
	case strings.Contains(msg, "render_output_signature_mismatch"):
		return "render_output_signature_mismatch"
	case strings.Contains(msg, "output does not look like"):
		return "render_output_signature_mismatch"
	}
	if worker == ArtifactMotionRenderWorkerName {
		return "motion_renderer_failed"
	}
	if worker == ArtifactStaticRenderWorkerName {
		return "static_renderer_failed"
	}
	return "render_smoke_failed"
}

func renderSmokeHint(worker string, reason string) string {
	switch reason {
	case "browser_not_found":
		return "Install Chrome/Chromium on the server or set WORKMAX_WORKAGENT_BROWSER_BIN to an executable path."
	case "motion_renderer_not_configured":
		return "Set WORKMAX_WORKAGENT_MOTION_RENDER_BIN to the motion renderer command used for MP4/GIF exports."
	case "motion_renderer_not_found":
		return "Verify WORKMAX_WORKAGENT_MOTION_RENDER_BIN points to an executable file available to the API process."
	case "render_timeout":
		return "Check renderer process health and increase timeout only after confirming the command is not hanging."
	case "render_command_failed":
		if worker == ArtifactMotionRenderWorkerName {
			return "Run the configured motion renderer command with the smoke environment variables and inspect stderr."
		}
		return "Run the configured browser command headlessly on the server and inspect stderr."
	case "mime_type_mismatch", "render_output_signature_mismatch":
		return "Verify the renderer writes the requested target format, not an error page or a different media type."
	case "empty_render_output", "render_output_read_failed":
		return "Verify the renderer creates a non-empty output file at the path passed by the smoke job."
	default:
		return ""
	}
}

func validateRenderOutputSignature(target string, content []byte) error {
	target = strings.ToLower(strings.TrimSpace(target))
	switch target {
	case "pdf", "png":
		if hasStaticRenderOutputSignature(target, content) {
			return nil
		}
	case "mp4", "gif":
		if hasMotionRenderOutputSignature(target, content) {
			return nil
		}
	default:
		return fmt.Errorf("render_output_signature_mismatch: export target %s has no signature check", target)
	}
	return fmt.Errorf("render_output_signature_mismatch: target %s output does not match expected file signature", target)
}

const renderSmokeHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="workmax:motion-duration-ms" content="2400">
  <meta name="workmax:motion-fps" content="24">
  <meta name="workmax:motion-width" content="1200">
  <meta name="workmax:motion-height" content="630">
  <style>
    html, body { margin: 0; width: 100%; height: 100%; }
    main {
      width: 1200px;
      height: 630px;
      display: grid;
      place-items: center;
      color: #111827;
      background: linear-gradient(135deg, #f8fafc, #dbeafe);
      font: 700 72px/1.1 system-ui, sans-serif;
    }
  </style>
</head>
<body><main>WorkMax render smoke</main></body>
</html>`
