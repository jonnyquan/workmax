package skills

import (
	"strings"
	"testing"
)

// Sprint-B DS-3: validates the visual-directions.yaml ships with
// the expected 5 directions, each carrying both OKLch and hex twin
// values + non-empty font stacks.

func TestLoadVisualDirections_Smoke(t *testing.T) {
	vd, err := LoadVisualDirections()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(vd.Fallback5) < 5 {
		t.Errorf("expected ≥5 fallback directions, got %d", len(vd.Fallback5))
	}
}

func TestLoadVisualDirections_AllExpectedIDs(t *testing.T) {
	vd, _ := LoadVisualDirections()
	want := []string{
		"editorial_magazine",
		"modern_minimal",
		"vintage_film",
		"bold_editorial",
		"soft_warm_lifestyle",
	}
	got := map[string]bool{}
	for _, d := range vd.Fallback5 {
		got[d.ID] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("expected direction %q in fallback_5", id)
		}
	}
}

func TestLoadVisualDirections_ColorTokensTwinned(t *testing.T) {
	// Sprint-B DS-3 contract: every palette slot has BOTH OKLch
	// and hex. OKLch for modern CSS / canvas, hex for Safari /
	// older surfaces. Mismatched twin-form would defeat
	// determinism (jitter under runtime conversion).
	vd, _ := LoadVisualDirections()
	for _, d := range vd.Fallback5 {
		t.Run(d.ID, func(t *testing.T) {
			for slot, color := range map[string]ColorValue{
				"bg":     d.Palette.Bg,
				"fg":     d.Palette.Fg,
				"accent": d.Palette.Accent,
				"muted":  d.Palette.Muted,
			} {
				if color.Hex == "" {
					t.Errorf("slot %s missing hex", slot)
				}
				if color.OKLch == "" {
					t.Errorf("slot %s missing OKLch", slot)
				}
				if color.Hex != "" && !strings.HasPrefix(color.Hex, "#") {
					t.Errorf("slot %s hex must start with #, got %q", slot, color.Hex)
				}
				if color.OKLch != "" && !strings.HasPrefix(color.OKLch, "oklch(") {
					t.Errorf("slot %s OKLch must use oklch(...) form, got %q", slot, color.OKLch)
				}
			}
		})
	}
}

func TestLoadVisualDirections_FontStacks(t *testing.T) {
	vd, _ := LoadVisualDirections()
	for _, d := range vd.Fallback5 {
		if d.FontStack.Display == "" {
			t.Errorf("%s: missing display font stack", d.ID)
		}
		if d.FontStack.Body == "" {
			t.Errorf("%s: missing body font stack", d.ID)
		}
		// Each stack should include a fallback (commas separating
		// the chain). A bare single name is brittle if the font
		// fails to load.
		if !strings.Contains(d.FontStack.Display, ",") {
			t.Errorf("%s: display font_stack lacks fallback chain: %q", d.ID, d.FontStack.Display)
		}
	}
}

func TestLoadVisualDirections_PromptFragmentsPopulated(t *testing.T) {
	vd, _ := LoadVisualDirections()
	for _, d := range vd.Fallback5 {
		if d.PromptFragment == "" {
			t.Errorf("%s: missing prompt_fragment (model gets nothing to bind)", d.ID)
		}
		if len(d.PromptFragment) < 30 {
			t.Errorf("%s: prompt_fragment too short (%d chars)", d.ID, len(d.PromptFragment))
		}
	}
}

func TestLoadVisualDirections_DsLinkPresent(t *testing.T) {
	// Forward references to DS-1 design-system files. The .md
	// files don't exist yet; this test just verifies the link
	// is populated so DS-1 can resolve it later.
	vd, _ := LoadVisualDirections()
	for _, d := range vd.Fallback5 {
		if d.DsLink == "" {
			t.Errorf("%s: missing ds_link", d.ID)
		}
		if !strings.HasSuffix(d.DsLink, ".md") {
			t.Errorf("%s: ds_link should be a .md filename, got %q", d.ID, d.DsLink)
		}
	}
}

func TestLoadVisualDirections_SuitableForPopulated(t *testing.T) {
	vd, _ := LoadVisualDirections()
	for _, d := range vd.Fallback5 {
		if len(d.SuitableFor) == 0 {
			t.Errorf("%s: missing suitable_for hints (picker can't tier directions per skill)", d.ID)
		}
	}
}

func TestFindDirection_HitAndMiss(t *testing.T) {
	if got := FindDirection("editorial_magazine"); got == nil || got.ID != "editorial_magazine" {
		t.Error("FindDirection editorial_magazine should resolve")
	}
	if got := FindDirection("nonexistent_direction"); got != nil {
		t.Error("FindDirection nonexistent should return nil")
	}
}

// ─────────────────────────────────────────────────────────────────
// FormatDirectionXML — Sprint-B DS-3 SystemAdditions wiring
// ─────────────────────────────────────────────────────────────────

func TestFormatDirectionXML_NilSafe(t *testing.T) {
	if got := FormatDirectionXML(nil); got != "" {
		t.Errorf("nil direction must produce empty XML, got %q", got)
	}
}

func TestFormatDirectionXML_PaletteSlotsPresent(t *testing.T) {
	d := FindDirection("modern_minimal")
	if d == nil {
		t.Fatal("modern_minimal should exist")
	}
	got := FormatDirectionXML(d)
	for _, marker := range []string{
		`<visual-direction id="modern_minimal">`,
		"palette:",
		"bg:",
		"fg:",
		"accent:",
		"muted:",
		"font_stack:",
		"display:",
		"body:",
		"</visual-direction>",
	} {
		if !strings.Contains(got, marker) {
			t.Errorf("missing marker %q in:\n%s", marker, got)
		}
	}
}

func TestFormatDirectionXML_OKLchAndHexBothEmitted(t *testing.T) {
	d := FindDirection("editorial_magazine")
	got := FormatDirectionXML(d)
	// Each color slot should emit BOTH OKLch + hex twin so the
	// model can pick whichever syntax the target surface accepts.
	if !strings.Contains(got, "oklch(") {
		t.Errorf("XML should carry OKLch syntax; got:\n%s", got)
	}
	if !strings.Contains(got, "#") {
		t.Errorf("XML should carry hex twin; got:\n%s", got)
	}
}

func TestFormatDirectionXML_PromptFragmentInlined(t *testing.T) {
	d := FindDirection("vintage_film")
	got := FormatDirectionXML(d)
	// Yaml block-scalar prompt fragments carry newlines; the
	// formatter must collapse them so the XML stays flat.
	if strings.Contains(strings.TrimPrefix(got, "<visual-direction"), "\n\n") {
		// We allow single \n between fields. Two consecutive
		// newlines = block-scalar leakage.
		t.Errorf("prompt_fragment leaked block-scalar newlines:\n%s", got)
	}
	if !strings.Contains(got, "prompt_anchor:") {
		t.Errorf("missing prompt_anchor line; got:\n%s", got)
	}
}
