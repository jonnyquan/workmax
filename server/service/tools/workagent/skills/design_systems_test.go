package skills

import (
	"sort"
	"strings"
	"testing"
)

// Sprint-B DS-1: 8 design-system markdown files ship in
// _shared/design-systems/. Tests verify each loads, contains the
// 9-section schema, and resolves correctly from VisualDirection
// ds_link fields.

func TestLoadDesignSystem_AllShippedSystemsExist(t *testing.T) {
	want := []string{
		"modern-minimal",
		"editorial-magazine",
		"vintage-film",
		"bold-editorial",
		"soft-warm-lifestyle",
		"tech-utility",
		"brutalist-experimental",
		"neutral-default",
	}
	for _, name := range want {
		t.Run(name, func(t *testing.T) {
			ds, err := LoadDesignSystem(name)
			if err != nil {
				t.Fatalf("LoadDesignSystem(%q): %v", name, err)
			}
			if ds == nil {
				t.Fatalf("LoadDesignSystem(%q) returned nil — file missing?", name)
			}
			if ds.Body == "" {
				t.Errorf("design system %q has empty body", name)
			}
			if ds.Basename != name {
				t.Errorf("basename mismatch: got %q want %q", ds.Basename, name)
			}
		})
	}
}

func TestLoadDesignSystem_NineSectionSchema(t *testing.T) {
	for _, name := range []string{
		"modern-minimal", "editorial-magazine", "vintage-film",
		"bold-editorial", "soft-warm-lifestyle", "tech-utility",
		"brutalist-experimental", "neutral-default",
	} {
		t.Run(name, func(t *testing.T) {
			ds, _ := LoadDesignSystem(name)
			if ds == nil {
				t.Fatal("nil ds")
			}
			for _, section := range requiredDesignSystemSections {
				if !strings.Contains(ds.Body, section) {
					t.Errorf("system %q missing %q heading", name, section)
				}
			}
		})
	}
}

func TestValidateAllDesignSystems(t *testing.T) {
	if err := ValidateAllDesignSystems(); err != nil {
		t.Fatalf("shipped design systems should validate: %v", err)
	}
}

func TestValidateDesignSystem_RejectsMissingTokens(t *testing.T) {
	body := `# Design System — Bad

## 1. Color
| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(98% 0 0) | #fafafa | bg |

## 2. Typography
- Display: Fancy Display · weight 700 · sizes [48]
- Body:    Fancy Body · weight 400 · sizes [16]
- Mono:    Fancy Mono · weight 400 · size 13

## 3. Spacing
Scale: 8 / 16

## 4. Layout
Container: max-w 1200

## 5. Components
Button

## 6. Motion
- Fast: instant
- Default: 200ms

## 7. Voice
Tone

## 8. Brand
Logo

## 9. Anti-patterns
None`

	err := validateDesignSystem(&DesignSystem{Basename: "bad", Body: body})
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{
		"at least 4 OKLch tokens",
		"Display role lacks a generic fallback",
		"motion Fast token must use ms duration",
		"motion missing Slow token",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateDesignSystem_RejectsInvalidMotionSemantics(t *testing.T) {
	body := `# Design System — Bad Motion

## 1. Color
| Slot | OKLch | Hex | Role |
|---|---|---|---|
| bg | oklch(98% 0 0) | #fafafa | bg |
| fg | oklch(18% 0 0) | #1a1a1a | text |
| accent | oklch(55% 0.2 250) | #3355cc | accent |
| muted | oklch(70% 0 0) | #aaaaaa | muted |

## 2. Typography
- Display: Inter Display, system-ui, sans-serif · weight 700 · sizes [48]
- Body:    Inter, system-ui, sans-serif · weight 400 · sizes [16]
- Mono:    JetBrains Mono, Menlo, monospace · weight 400 · size 13

## 3. Spacing
Scale: 8 / 16 / 24

## 4. Layout
Container: max-w 1200

## 5. Components
Button

## 6. Motion
- Fast: 300ms
- Default: 200ms
- Slow: 180ms

## 7. Voice
Tone

## 8. Brand
Logo

## 9. Anti-patterns
None`

	err := validateDesignSystem(&DesignSystem{Basename: "bad-motion", Body: body})
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{
		"motion durations must increase Fast < Default < Slow",
		"motion section must declare an easing or transition descriptor",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestLoadDesignSystem_DerivedFromAnchorParsed(t *testing.T) {
	// 5 of the 8 systems mirror visual-directions.yaml entries
	// and carry derived_from. The 3 brand-neutral starters
	// (tech-utility, brutalist-experimental, neutral-default)
	// don't have a fallback_5 entry but still declare the
	// annotation for symmetry.
	cases := map[string]string{
		"modern-minimal":      "modern_minimal",
		"editorial-magazine":  "editorial_magazine",
		"vintage-film":        "vintage_film",
		"bold-editorial":      "bold_editorial",
		"soft-warm-lifestyle": "soft_warm_lifestyle",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			ds, _ := LoadDesignSystem(name)
			if ds == nil {
				t.Fatal("nil ds")
			}
			if ds.DerivedFrom != want {
				t.Errorf("derived_from mismatch: got %q want %q", ds.DerivedFrom, want)
			}
		})
	}
}

func TestLoadDesignSystem_MissingReturnsNil(t *testing.T) {
	ds, err := LoadDesignSystem("imaginary-system-name")
	if err != nil {
		t.Errorf("missing system should not error, got %v", err)
	}
	if ds != nil {
		t.Errorf("missing system should return nil")
	}
}

func TestLoadDesignSystem_EmptyBasename(t *testing.T) {
	ds, _ := LoadDesignSystem("")
	if ds != nil {
		t.Error("empty basename should return nil")
	}
}

func TestLoadDesignSystemForDirection_ResolvesViaDsLink(t *testing.T) {
	// Each fallback_5 direction's ds_link should resolve to a
	// shipped design-system file. Locks the cross-link contract
	// — DS-3 yaml + DS-1 markdown agree on filenames.
	vd, _ := LoadVisualDirections()
	for _, d := range vd.Fallback5 {
		dir := d
		t.Run(dir.ID, func(t *testing.T) {
			ds, err := LoadDesignSystemForDirection(&dir)
			if err != nil {
				t.Fatalf("resolve %q (ds_link=%q): %v", dir.ID, dir.DsLink, err)
			}
			if ds == nil {
				t.Errorf("ds_link %q for direction %q didn't resolve to a shipped system", dir.DsLink, dir.ID)
			}
		})
	}
}

func TestLoadDesignSystemForDirection_NilSafe(t *testing.T) {
	ds, err := LoadDesignSystemForDirection(nil)
	if err != nil {
		t.Errorf("nil direction should not error: %v", err)
	}
	if ds != nil {
		t.Error("nil direction should return nil ds")
	}
}

func TestAvailableDesignSystems_All8Listed(t *testing.T) {
	got := AvailableDesignSystems()
	if len(got) < 8 {
		t.Errorf("expected ≥8 design systems shipped, got %d: %v", len(got), got)
	}
	sorted := append([]string{}, got...)
	sort.Strings(sorted)
	for i := range got {
		if got[i] != sorted[i] {
			t.Fatalf("AvailableDesignSystems not sorted: got %v", got)
		}
	}
}

func TestListDesignSystemCatalog_ReturnsStableRows(t *testing.T) {
	items, err := ListDesignSystemCatalog()
	if err != nil {
		t.Fatalf("ListDesignSystemCatalog: %v", err)
	}
	if len(items) < 8 {
		t.Fatalf("expected ≥8 catalog rows, got %d", len(items))
	}
	for _, item := range items {
		if item.Basename == "" {
			t.Errorf("catalog row has empty basename: %#v", item)
		}
		if item.Title == "" || !strings.HasPrefix(item.Title, "Design System") {
			t.Errorf("catalog row title = %q", item.Title)
		}
		if item.Body == "" {
			t.Errorf("catalog row %q has empty body", item.Basename)
		}
	}
}
