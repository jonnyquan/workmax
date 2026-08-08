package workagent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type smokeStaticRenderer struct {
	output  HTMLStaticRenderOutput
	outputs map[string]HTMLStaticRenderOutput
	inputs  []HTMLStaticRenderInput
}

func (r *smokeStaticRenderer) RenderStaticHTML(_ context.Context, input HTMLStaticRenderInput) (HTMLStaticRenderOutput, error) {
	r.inputs = append(r.inputs, input)
	if r.output.MimeType == "__error__" {
		return HTMLStaticRenderOutput{}, fmt.Errorf("%s", string(r.output.Content))
	}
	if r.outputs != nil {
		if output, ok := r.outputs[input.Target]; ok {
			if output.MimeType == "__error__" {
				return HTMLStaticRenderOutput{}, fmt.Errorf("%s", string(output.Content))
			}
			return output, nil
		}
	}
	return r.output, nil
}

type smokeMotionRenderer struct {
	output  HTMLMotionRenderOutput
	outputs map[string]HTMLMotionRenderOutput
	inputs  []HTMLMotionRenderInput
}

func (r *smokeMotionRenderer) RenderMotionHTML(_ context.Context, input HTMLMotionRenderInput) (HTMLMotionRenderOutput, error) {
	r.inputs = append(r.inputs, input)
	if r.output.MimeType == "__error__" {
		return HTMLMotionRenderOutput{}, fmt.Errorf("%s", string(r.output.Content))
	}
	if r.outputs != nil {
		if output, ok := r.outputs[input.Target]; ok {
			if output.MimeType == "__error__" {
				return HTMLMotionRenderOutput{}, fmt.Errorf("%s", string(output.Content))
			}
			return output, nil
		}
	}
	return r.output, nil
}

func TestRunArtifactRenderSmokePassesCanonicalOutputExtensions(t *testing.T) {
	staticRenderer := &smokeStaticRenderer{outputs: map[string]HTMLStaticRenderOutput{
		"pdf": {Content: []byte("%PDF fake"), MimeType: "application/pdf"},
		"png": {Content: []byte("\x89PNG\r\n\x1a\nfake"), MimeType: "image/png"},
	}}
	motionRenderer := &smokeMotionRenderer{outputs: map[string]HTMLMotionRenderOutput{
		"mp4": {Content: []byte("\x00\x00\x00\x18ftypfake"), MimeType: "video/mp4"},
		"gif": {Content: []byte("GIF89afake"), MimeType: "image/gif"},
	}}

	got := RunArtifactRenderSmoke(context.Background(), staticRenderer, motionRenderer)

	if len(got.Static) != 2 || len(got.Motion) != 2 {
		t.Fatalf("smoke results = %+v", got)
	}
	if got.Status != "passed" || got.Total != 4 || got.Passed != 4 || got.Failed != 0 || got.Unavailable != 0 {
		t.Fatalf("smoke summary = %+v, want all targets passed", got)
	}
	if staticRenderer.inputs[0].OutputExtension != ".pdf" || staticRenderer.inputs[1].OutputExtension != ".png" {
		t.Fatalf("static output extensions = %#v, want canonical dotted extensions", staticRenderer.inputs)
	}
	if motionRenderer.inputs[0].OutputExtension != ".mp4" || motionRenderer.inputs[1].OutputExtension != ".gif" {
		t.Fatalf("motion output extensions = %#v, want canonical dotted extensions", motionRenderer.inputs)
	}
	for _, input := range motionRenderer.inputs {
		if input.MotionSettings.DurationMs != 2400 || input.MotionSettings.FPS != 24 ||
			input.MotionSettings.Width != 1200 || input.MotionSettings.Height != 630 {
			t.Fatalf("motion smoke settings = %+v, want stable render smoke timeline", input.MotionSettings)
		}
	}
}

func TestRunArtifactRenderSmokeFailsMismatchedMimeType(t *testing.T) {
	staticRenderer := &smokeStaticRenderer{output: HTMLStaticRenderOutput{Content: []byte("not really png"), MimeType: "text/plain"}}
	motionRenderer := &smokeMotionRenderer{output: HTMLMotionRenderOutput{Content: []byte("not really gif"), MimeType: "text/plain"}}

	got := RunArtifactRenderSmoke(context.Background(), staticRenderer, motionRenderer)

	if got.Status != "failed" || got.Total != 4 || got.Failed != 4 {
		t.Fatalf("smoke summary = %+v, want failed count for all targets", got)
	}
	assertSmokeFailedWith(t, got.Static, "pdf", "mime_type_mismatch")
	assertSmokeFailedWith(t, got.Static, "png", "mime_type_mismatch")
	assertSmokeFailedWith(t, got.Motion, "mp4", "mime_type_mismatch")
	assertSmokeFailedWith(t, got.Motion, "gif", "mime_type_mismatch")
}

func TestRunArtifactRenderSmokeFailsMismatchedOutputSignature(t *testing.T) {
	staticRenderer := &smokeStaticRenderer{outputs: map[string]HTMLStaticRenderOutput{
		"pdf": {Content: []byte("not really pdf"), MimeType: "application/pdf"},
		"png": {Content: []byte("not really png"), MimeType: "image/png"},
	}}
	motionRenderer := &smokeMotionRenderer{outputs: map[string]HTMLMotionRenderOutput{
		"mp4": {Content: []byte("not really mp4"), MimeType: "video/mp4"},
		"gif": {Content: []byte("not really gif"), MimeType: "image/gif"},
	}}

	got := RunArtifactRenderSmoke(context.Background(), staticRenderer, motionRenderer)

	if got.Status != "failed" || got.Total != 4 || got.Failed != 4 {
		t.Fatalf("smoke summary = %+v, want failed count for all targets", got)
	}
	assertSmokeFailedWith(t, got.Static, "pdf", "render_output_signature_mismatch")
	assertSmokeFailedWith(t, got.Static, "png", "render_output_signature_mismatch")
	assertSmokeFailedWith(t, got.Motion, "mp4", "render_output_signature_mismatch")
	assertSmokeFailedWith(t, got.Motion, "gif", "render_output_signature_mismatch")
}

func TestRunArtifactRenderSmokeAddsFailureReasonsAndHints(t *testing.T) {
	staticRenderer := &smokeStaticRenderer{output: HTMLStaticRenderOutput{
		Content:  []byte("browser command renderer: command failed: exit status 1: missing library"),
		MimeType: "__error__",
	}}
	motionRenderer := &smokeMotionRenderer{output: HTMLMotionRenderOutput{
		Content:  []byte("browser command motion renderer: WORKMAX_WORKAGENT_MOTION_RENDER_BIN is not configured"),
		MimeType: "__error__",
	}}

	got := RunArtifactRenderSmoke(context.Background(), staticRenderer, motionRenderer)

	if got.Status != "failed" || got.Failed != 4 {
		t.Fatalf("smoke summary = %+v, want failed count for all targets", got)
	}
	assertSmokeReasonAndHint(t, got.Static, "pdf", "render_command_failed", "browser command")
	assertSmokeReasonAndHint(t, got.Static, "png", "render_command_failed", "browser command")
	assertSmokeReasonAndHint(t, got.Motion, "mp4", "motion_renderer_not_configured", "WORKMAX_WORKAGENT_MOTION_RENDER_BIN")
	assertSmokeReasonAndHint(t, got.Motion, "gif", "motion_renderer_not_configured", "WORKMAX_WORKAGENT_MOTION_RENDER_BIN")
}

func TestRunArtifactRenderSmokeClassifiesCommandSignatureMismatch(t *testing.T) {
	staticRenderer := &smokeStaticRenderer{output: HTMLStaticRenderOutput{
		Content:  []byte("browser command renderer: output does not look like png"),
		MimeType: "__error__",
	}}
	motionRenderer := &smokeMotionRenderer{outputs: map[string]HTMLMotionRenderOutput{
		"mp4": {Content: []byte("\x00\x00\x00\x18ftypfake"), MimeType: "video/mp4"},
		"gif": {Content: []byte("GIF89afake"), MimeType: "image/gif"},
	}}

	got := RunArtifactRenderSmoke(context.Background(), staticRenderer, motionRenderer)

	if got.Status != "failed" || got.Failed != 2 || got.Passed != 2 {
		t.Fatalf("smoke summary = %+v, want failed static targets and passed motion targets", got)
	}
	assertSmokeReasonAndHint(t, got.Static, "pdf", "render_output_signature_mismatch", "requested target format")
	assertSmokeReasonAndHint(t, got.Static, "png", "render_output_signature_mismatch", "requested target format")
}

func TestRunArtifactRenderSmokeFailsEmptyOutput(t *testing.T) {
	staticRenderer := &smokeStaticRenderer{output: HTMLStaticRenderOutput{MimeType: "application/pdf"}}
	motionRenderer := &smokeMotionRenderer{output: HTMLMotionRenderOutput{MimeType: "video/mp4"}}

	got := RunArtifactRenderSmoke(context.Background(), staticRenderer, motionRenderer)

	if got.Status != "failed" || got.Total != 4 || got.Failed != 4 {
		t.Fatalf("smoke summary = %+v, want failed count for all targets", got)
	}
	assertSmokeFailedWith(t, got.Static, "pdf", "empty pdf output")
	assertSmokeFailedWith(t, got.Static, "png", "empty png output")
	assertSmokeFailedWith(t, got.Motion, "mp4", "empty mp4 output")
	assertSmokeFailedWith(t, got.Motion, "gif", "empty gif output")
}

func TestRunArtifactRenderSmokeSummarizesUnavailableTargets(t *testing.T) {
	got := newArtifactRenderSmokeSnapshot(
		unavailableSmokeResults(ArtifactStaticRenderWorkerName, []string{"pdf", "png"}, "missing browser"),
		[]ArtifactRenderSmokeResult{
			{Worker: ArtifactMotionRenderWorkerName, Target: "mp4", Status: "passed", MimeType: "video/mp4", Bytes: 24},
			{Worker: ArtifactMotionRenderWorkerName, Target: "gif", Status: "passed", MimeType: "image/gif", Bytes: 12},
		},
	)

	if got.Status != "unavailable" || got.Total != 4 || got.Passed != 2 || got.Unavailable != 2 || got.Failed != 0 {
		t.Fatalf("smoke summary = %+v, want unavailable static targets and passed motion targets", got)
	}
	assertSmokeReasonAndHint(t, got.Static, "pdf", "static_renderer_failed", "")
}

func assertSmokeFailedWith(t *testing.T, results []ArtifactRenderSmokeResult, target string, messagePart string) {
	t.Helper()
	for _, result := range results {
		if result.Target != target {
			continue
		}
		if result.Status != "failed" || !strings.Contains(result.Error, messagePart) {
			t.Fatalf("smoke %s = %+v, want failed containing %q", target, result, messagePart)
		}
		return
	}
	t.Fatalf("missing smoke result for %s in %+v", target, results)
}

func assertSmokeReasonAndHint(t *testing.T, results []ArtifactRenderSmokeResult, target string, reason string, hintPart string) {
	t.Helper()
	for _, result := range results {
		if result.Target != target {
			continue
		}
		if result.Reason != reason {
			t.Fatalf("smoke %s reason = %q, want %q in %+v", target, result.Reason, reason, result)
		}
		if hintPart != "" && !strings.Contains(result.Hint, hintPart) {
			t.Fatalf("smoke %s hint = %q, want containing %q", target, result.Hint, hintPart)
		}
		return
	}
	t.Fatalf("missing smoke result for %s in %+v", target, results)
}
