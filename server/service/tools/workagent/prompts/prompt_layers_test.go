package prompts

// prompt_layers_test.go — DS-5. Pins the layer enum + the
// ComposeWithTrace surface so future changes don't silently
// shift which layer a field belongs to or break the byte-for-byte
// parity with legacy Compose().

import (
	"strings"
	"testing"
)

func TestPromptLayer_NameStable(t *testing.T) {
	cases := map[PromptLayer]string{
		LayerDiscovery:       "discovery",
		LayerIdentity:        "identity",
		LayerProtocols:       "protocols",
		LayerDesignSystem:    "design-system",
		LayerSkill:           "skill",
		LayerSkillSideFiles:  "skill-side-files",
		LayerProjectMetadata: "project-metadata",
		LayerUnknown:         "unknown",
		PromptLayer(99):      "unknown",
	}
	for l, want := range cases {
		if got := l.Name(); got != want {
			t.Errorf("Layer(%d).Name() = %q, want %q", l, got, want)
		}
	}
}

func TestAllLayers_HasSeven_InCanonicalOrder(t *testing.T) {
	got := AllLayers()
	if len(got) != 7 {
		t.Fatalf("expected 7 layers, got %d", len(got))
	}
	want := []PromptLayer{
		LayerDiscovery, LayerIdentity, LayerProtocols, LayerDesignSystem,
		LayerSkill, LayerSkillSideFiles, LayerProjectMetadata,
	}
	for i, l := range got {
		if l != want[i] {
			t.Errorf("AllLayers()[%d] = %v, want %v", i, l, want[i])
		}
		if l.Canonical() != i+1 {
			t.Errorf("Canonical for %v = %d, want %d", l, l.Canonical(), i+1)
		}
	}
}

func TestLayerDisableSet_NilSafe(t *testing.T) {
	var s LayerDisableSet
	if s.IsDisabled(LayerDiscovery) {
		t.Error("nil set should report all layers enabled")
	}
}

func TestLayerDisableSet_HitMissCorrectly(t *testing.T) {
	s := LayerDisableSet{LayerDiscovery: true}
	if !s.IsDisabled(LayerDiscovery) {
		t.Error("expected discovery disabled")
	}
	if s.IsDisabled(LayerIdentity) {
		t.Error("identity should not be disabled")
	}
}

func TestComposeWithTrace_LegacyOrderPreserved(t *testing.T) {
	// Populate every field with a marker string so the legacy
	// order is observable in the output.
	// (F2 2026-05-17) anti-slop / brand-protocol fields removed
	// from the composer — see system_additions.go field block.
	c := SystemAdditionsComposer{
		DesignSystem:      "DESIGN",
		BrandSpec:         "<brand-spec/>",
		SelectedDirection: "<direction/>", // ignored when BrandSpec present
		AssetLibraryIndex: "<library-index/>",
		DirectorStyle:     "<director/>",
		CharacterContext:  "<character/>",
		SkillSideFiles:    "<side-files/>",
		DiscoveryContext:  "<discovery/>",
		ChecklistDigest:   "<checklist/>",
		PassModeProtocol:  "<pass-mode/>",
	}
	out, trace := c.ComposeWithTrace()

	wantOrder := []string{
		"DESIGN",
		"<brand-spec/>",
		"<library-index/>",
		"<director/>",
		"<character/>",
		"<side-files/>",
		"<discovery/>",
		"<checklist/>",
		"<pass-mode/>",
	}
	lastIdx := -1
	for _, want := range wantOrder {
		idx := strings.Index(out, want)
		if idx < 0 {
			t.Errorf("output missing %q", want)
			continue
		}
		if idx <= lastIdx {
			t.Errorf("ordering broken: %q at %d, previous at %d", want, idx, lastIdx)
		}
		lastIdx = idx
	}

	// Trace should align with the same order.
	if len(trace) != len(wantOrder) {
		t.Fatalf("trace length = %d, want %d", len(trace), len(wantOrder))
	}
	// SelectedDirection was suppressed by BrandSpec winning; verify
	// the trace doesn't carry a "direction" field.
	for _, c := range trace {
		for _, f := range c.Fields {
			if f == "direction" {
				t.Error("trace should not contain 'direction' when BrandSpec wins")
			}
		}
	}
}

func TestComposeWithTrace_BrandFallsBackToDirection(t *testing.T) {
	c := SystemAdditionsComposer{SelectedDirection: "<direction/>"}
	out, trace := c.ComposeWithTrace()
	if !strings.Contains(out, "<direction/>") {
		t.Error("direction should appear when BrandSpec empty")
	}
	if len(trace) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(trace))
	}
	if trace[0].Layer != LayerProjectMetadata {
		t.Errorf("direction should be in project-metadata layer, got %v", trace[0].Layer)
	}
	if len(trace[0].Fields) != 1 || trace[0].Fields[0] != "direction" {
		t.Errorf("trace fields = %v, want [direction]", trace[0].Fields)
	}
}

func TestComposeWithTrace_LayerToFieldMapping(t *testing.T) {
	// (F2 2026-05-17) anti-slop / brand-protocol fields removed.
	c := SystemAdditionsComposer{
		DesignSystem:      "DS",
		BrandSpec:         "BS",
		AssetLibraryIndex: "LI",
		DirectorStyle:     "DR",
		CharacterContext:  "CR",
		SkillSideFiles:    "SF",
		DiscoveryContext:  "DC",
		ChecklistDigest:   "CL",
		PassModeProtocol:  "PM",
	}
	_, trace := c.ComposeWithTrace()

	wantLayers := map[string]PromptLayer{
		"design-system":     LayerDesignSystem,
		"brand-spec":        LayerProjectMetadata,
		"library-index":     LayerProjectMetadata,
		"director-style":    LayerProjectMetadata,
		"character-context": LayerProjectMetadata,
		"side-files":        LayerSkillSideFiles,
		"discovery":         LayerDiscovery,
		"checklist":         LayerSkillSideFiles,
		"pass-mode":         LayerDiscovery,
	}
	got := map[string]PromptLayer{}
	for _, contrib := range trace {
		for _, f := range contrib.Fields {
			got[f] = contrib.Layer
		}
	}
	for field, wantLayer := range wantLayers {
		if got[field] != wantLayer {
			t.Errorf("field %q maps to layer %v, want %v", field, got[field], wantLayer)
		}
	}
}

func TestComposeWithTrace_DisabledLayer_VanishesEverywhere(t *testing.T) {
	// (F2 2026-05-17) replaced AntiSlopProtocol (LayerIdentity)
	// with DesignSystem (LayerDesignSystem) — same shape of test,
	// different non-disabled-layer marker.
	c := SystemAdditionsComposer{
		DesignSystem:     "<design-system/>",
		DiscoveryContext: "<discovery/>",
		LayerDisable:     LayerDisableSet{LayerDiscovery: true},
	}
	out, trace := c.ComposeWithTrace()
	if strings.Contains(out, "<discovery/>") {
		t.Errorf("disabled layer's text leaked into output: %s", out)
	}
	if !strings.Contains(out, "<design-system/>") {
		t.Error("non-disabled design-system layer was wrongly suppressed")
	}
	for _, contrib := range trace {
		if contrib.Layer == LayerDiscovery {
			t.Errorf("disabled layer should not appear in trace: %+v", contrib)
		}
	}
}

func TestComposeWithTrace_AllLayersDisabled_EmptyOutput(t *testing.T) {
	// (F2 2026-05-17) AntiSlopProtocol was the only field in
	// LayerIdentity; with it removed, disabling LayerIdentity has
	// no observable effect. Test refactored to disable the
	// LayerDesignSystem layer that DesignSystem lives in.
	c := SystemAdditionsComposer{
		DesignSystem: "DS",
		LayerDisable: LayerDisableSet{LayerDesignSystem: true},
	}
	out, trace := c.ComposeWithTrace()
	if out != "" {
		t.Errorf("expected empty output, got %q", out)
	}
	if len(trace) != 0 {
		t.Errorf("expected empty trace, got %v", trace)
	}
}

func TestCompose_ByteParityWithComposeWithTrace(t *testing.T) {
	// Compose() must remain a thin shim — same string as
	// ComposeWithTrace, byte-for-byte. Regression guard for any
	// future refactor that accidentally diverges them.
	c := SystemAdditionsComposer{
		DesignSystem:     "DS",
		BrandSpec:        "<brand/>",
		DiscoveryContext: "<discovery/>",
	}
	cheap := c.Compose()
	full, _ := c.ComposeWithTrace()
	if cheap != full {
		t.Errorf("Compose() and ComposeWithTrace() text drifted:\n  Compose:  %q\n  Compose+T: %q", cheap, full)
	}
}

func TestCompose_EmptyComposer_StaysEmpty(t *testing.T) {
	var c SystemAdditionsComposer
	if c.Compose() != "" {
		t.Error("empty composer should produce empty output")
	}
	if out, trace := c.ComposeWithTrace(); out != "" || trace != nil {
		t.Errorf("empty composer trace should be empty: %q %v", out, trace)
	}
}
