package workagent

import (
	"strings"
	"testing"
)

func TestBuildPreflightAdditions_AlwaysOnForSkillWithChecklist(t *testing.T) {
	got := BuildPreflightAdditions(42, "ppt")
	if got == "" {
		t.Fatal("expected non-empty additions for ppt")
	}
	if !strings.Contains(got, "<skill-checklist>") {
		t.Errorf("expected checklist XML wrapper, got %q", got[:min(200, len(got))])
	}
	if !strings.Contains(got, "honest_data") {
		t.Errorf("expected ppt's honest_data P0 in digest")
	}
}

func TestBuildPreflightAdditions_EmptySkillName(t *testing.T) {
	got := BuildPreflightAdditions(42, "")
	if got != "" {
		t.Errorf("empty skill name should produce empty additions, got %q", got)
	}
}

func TestBuildPreflightAdditions_UnknownSkill(t *testing.T) {
	// No checklist file exists → empty digest → empty additions
	// (gate degrades to "no rules apply" rather than crashing).
	got := BuildPreflightAdditions(42, "imaginary")
	if got != "" {
		t.Errorf("unknown skill should produce empty additions, got %q", got)
	}
}

func TestBuildPreflightAdditions_AllFiveShippedSkills(t *testing.T) {
	allFive := []string{"ppt", "character", "productShot", "marketingPoster", "flashCard"}
	for _, skill := range allFive {
		t.Run(skill, func(t *testing.T) {
			got := BuildPreflightAdditions(42, skill)
			if got == "" {
				t.Errorf("expected non-empty additions for %q", skill)
			}
			if !strings.Contains(got, "<skill-checklist>") {
				t.Errorf("expected checklist wrapper for %q", skill)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─────────────────────────────────────────────────────────────────
// PR-6b: discovery context resolver / formatter
// ─────────────────────────────────────────────────────────────────

func TestLoadDiscoveryContext_EmptyThread(t *testing.T) {
	// threadID=0 short-circuits in BuildPreflightAdditionsForThread,
	// so we don't touch DefaultMessageRepository here. With no thread
	// scope the function returns "".
	if got := loadDiscoveryContext(0, ""); got != "" {
		t.Errorf("threadID=0 should return empty, got %q", got)
	}
}

func TestBuildPreflightAdditionsForThread_NoDiscovery(t *testing.T) {
	got := BuildPreflightAdditionsForThread(42, "ppt", 0)
	if !strings.Contains(got, "<skill-checklist>") {
		t.Errorf("always-on preflight should include checklist, got %q", got)
	}
}

func TestPersistInferredDiscoveryAnswers_GuardsBadInputs(t *testing.T) {
	// Should silently skip when uid/threadID/answers missing —
	// tests the defensive guards. We don't invoke the repo here,
	// which would require a DB.
	// G10 (2026-05-17) signature gained a skillName argument. Pass
	// "" to exercise the legacy form_id="discovery" fallback —
	// these are guard-only tests; the guards reject before form_id
	// resolution matters.
	PersistInferredDiscoveryAnswers(0, 1, "", map[string]string{"a": "b"}, "")
	PersistInferredDiscoveryAnswers(1, 0, "", map[string]string{"a": "b"}, "")
	PersistInferredDiscoveryAnswers(1, 1, "", map[string]string{}, "")
	PersistInferredDiscoveryAnswers(1, 1, "", nil, "")
	// No assertion — survival without panic = pass
}

// ─────────────────────────────────────────────────────────────────
// DS-3 SystemAdditions wiring (selected direction)
// ─────────────────────────────────────────────────────────────────

func TestPersistSelectedDirection_GuardsBadInputs(t *testing.T) {
	// Defensive guards mirror the discovery counterparts.
	if PersistSelectedDirection(0, 1, "modern_minimal", "user_picker") {
		t.Error("bad uid should return false")
	}
	if PersistSelectedDirection(1, 0, "modern_minimal", "user_picker") {
		t.Error("bad thread should return false")
	}
	if PersistSelectedDirection(1, 1, "", "user_picker") {
		t.Error("empty direction id should return false")
	}
	if PersistSelectedDirection(1, 1, "imaginary_direction_id", "user_picker") {
		t.Error("unknown direction id should return false")
	}
	// Survival without panic = pass; nothing reaches the repo
	// because the guards reject.
}

func TestLoadSelectedDirection_NoThread(t *testing.T) {
	// threadID=0 short-circuits before any DB lookup; keeps the
	// test runnable without a DB fixture.
	if got := loadSelectedDirection(0); got != "" {
		t.Errorf("expected empty for threadID=0, got %q", got)
	}
}
