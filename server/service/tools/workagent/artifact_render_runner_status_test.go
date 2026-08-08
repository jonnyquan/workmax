package workagent

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultArtifactStaticRenderRunnerStatusRecordsDisabledReason(t *testing.T) {
	ShutdownDefaultArtifactStaticRenderRunner()
	t.Setenv("WORKMAX_WORKAGENT_BROWSER_BIN", filepath.Join(t.TempDir(), "missing-chrome"))

	err := StartDefaultArtifactStaticRenderRunner()
	if err == nil {
		t.Fatal("expected static runner startup to fail with missing browser")
	}
	status := DefaultArtifactStaticRenderRunnerStatus()
	if status.Worker != ArtifactStaticRenderWorkerName {
		t.Fatalf("worker = %q, want %q", status.Worker, ArtifactStaticRenderWorkerName)
	}
	if status.Running {
		t.Fatalf("status = %#v, want disabled", status)
	}
	if !strings.Contains(status.DisabledReason, "configured browser not found") || status.LastError != status.DisabledReason {
		t.Fatalf("status = %#v, want configured browser failure reason", status)
	}
	if status.UpdatedAt.IsZero() {
		t.Fatalf("status missing UpdatedAt: %#v", status)
	}
}

func TestDefaultArtifactMotionRenderRunnerStatusRecordsDisabledReason(t *testing.T) {
	ShutdownDefaultArtifactMotionRenderRunner()
	t.Setenv("WORKMAX_WORKAGENT_MOTION_RENDER_BIN", "")

	err := StartDefaultArtifactMotionRenderRunner()
	if err == nil {
		t.Fatal("expected motion runner startup to fail without command")
	}
	status := DefaultArtifactMotionRenderRunnerStatus()
	if status.Worker != ArtifactMotionRenderWorkerName {
		t.Fatalf("worker = %q, want %q", status.Worker, ArtifactMotionRenderWorkerName)
	}
	if status.Running {
		t.Fatalf("status = %#v, want disabled", status)
	}
	if !strings.Contains(status.DisabledReason, "WORKMAX_WORKAGENT_MOTION_RENDER_BIN is not configured") || status.LastError != status.DisabledReason {
		t.Fatalf("status = %#v, want missing motion renderer reason", status)
	}
	if status.UpdatedAt.IsZero() {
		t.Fatalf("status missing UpdatedAt: %#v", status)
	}
}
