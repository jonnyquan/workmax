package workagent

// preflight_layers_test.go — DS-5. Covers the layerTraceSummary
// helper and the always-on prompt-layer policy wired into
// BuildPreflightAdditions.

import (
	"strings"
	"testing"

	"server/service/tools/workagent/prompts"
)

func TestResolvePromptLayerDisableSet_AlwaysOn(t *testing.T) {
	if got := resolvePromptLayerDisableSet(); got != nil {
		t.Errorf("expected nil disable set in always-on mode, got %v", got)
	}
}

func TestLayerTraceSummary_EmptyReturnsNone(t *testing.T) {
	if got := layerTraceSummary(nil); got != "none" {
		t.Errorf("expected 'none' for empty trace, got %q", got)
	}
}

func TestLayerTraceSummary_FormatsBracketedPairs(t *testing.T) {
	// (F2 2026-05-17) anti-slop / brand-protocol layer entries
	// removed from the composer — replaced with field markers that
	// the post-F2 composer actually emits (design-system / brand-spec
	// / discovery).
	trace := []prompts.LayerContribution{
		{Layer: prompts.LayerDesignSystem, Fields: []string{"design-system"}},
		{Layer: prompts.LayerProjectMetadata, Fields: []string{"brand-spec"}},
		{Layer: prompts.LayerDiscovery, Fields: []string{"discovery"}},
	}
	got := layerTraceSummary(trace)
	if !strings.Contains(got, "[design-system:design-system]") {
		t.Errorf("missing design-system entry: %q", got)
	}
	if !strings.Contains(got, "[project-metadata:brand-spec]") {
		t.Errorf("missing project-metadata entry: %q", got)
	}
	if !strings.Contains(got, "[discovery:discovery]") {
		t.Errorf("missing discovery entry: %q", got)
	}
}

func TestLayerTraceSummary_HandlesEmptyFields(t *testing.T) {
	trace := []prompts.LayerContribution{
		{Layer: prompts.LayerSkill, Fields: nil},
	}
	got := layerTraceSummary(trace)
	if got != "[skill]" {
		t.Errorf("expected '[skill]' for empty-fields contribution, got %q", got)
	}
}
