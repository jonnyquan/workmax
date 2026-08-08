package skills

import (
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// VisualDirection mirrors a single entry under fallback_5 in
// _shared/visual-directions.yaml. Sprint-B DS-3 introduces the
// OKLch + hex palette + font_stack determinism so the M2 picker
// (Sprint C) and the future DS-1 design-systems library can agree
// on a single source of truth.
//
// Naming and structure follow §0.7 命名中性 — no platform brand
// names, just visual-tone identifiers (editorial_magazine,
// modern_minimal, etc.).
type VisualDirection struct {
	ID             string             `yaml:"id"`
	LabelKey       string             `yaml:"label_key"`
	DescriptionKey string             `yaml:"description_key,omitempty"`

	Palette        DirectionPalette   `yaml:"palette"`
	FontStack      DirectionFontStack `yaml:"font_stack"`
	LayoutPosture  string             `yaml:"layout_posture,omitempty"`
	AudioStyle     string             `yaml:"audio_style,omitempty"`
	Transition     string             `yaml:"transition,omitempty"`
	PromptFragment string             `yaml:"prompt_fragment,omitempty"`

	// SuitableFor is the suggested skill list this direction
	// pairs well with. Used by the picker UI to dim or boost
	// directions per-skill (e.g. show editorial_magazine first
	// when user is in `character` mode).
	SuitableFor []string `yaml:"suitable_for,omitempty"`

	// DsLink is the basename of the design-system markdown that
	// expands this direction into a full 9-section spec. Sprint-C
	// DS-1 ships these files; until then the link is a forward
	// reference (lookups skip cleanly when the .md is absent).
	DsLink string `yaml:"ds_link,omitempty"`

	// SampleVideoURL — optional preview clip played muted on hover
	// in the DirectionsPicker (frontend reads it as
	// direction.sample_video_url). 6-12s loop at 720p is the target
	// shape; empty disables the hover preview cleanly. URLs paste
	// in the yaml as assets become available — Sprint-C M2
	// deliverable, currently empty across the 5 fallback rows.
	SampleVideoURL string `yaml:"sample_video_url,omitempty"`

	// ThumbnailURL — optional static poster the picker can render
	// before / instead of the video (e.g. mobile where autoplay is
	// gated, or first-render before hover). Treated as a fall-back
	// visual when SampleVideoURL is empty too — the picker still
	// shows the palette card without a thumbnail; the field is
	// purely additive.
	ThumbnailURL string `yaml:"thumbnail_url,omitempty"`
}

// DirectionPalette carries 4 named slots in OKLch + hex twin-form.
// hex is committed (not regenerated at runtime) so determinism
// holds across deploys; jitter would defeat the SDK prompt cache.
type DirectionPalette struct {
	Bg     ColorValue `yaml:"bg"`
	Fg     ColorValue `yaml:"fg"`
	Accent ColorValue `yaml:"accent"`
	Muted  ColorValue `yaml:"muted"`
}

// ColorValue twins OKLch and hex. Older Safari / canvas APIs that
// can't parse OKLch literals consume the hex; modern CSS uses
// OKLch for perceptually uniform manipulation.
type ColorValue struct {
	OKLch string `yaml:"oklch"`
	Hex   string `yaml:"hex"`
}

// DirectionFontStack bundles display / body / mono CSS font-family
// values. Concrete font names rather than generic categories so the
// model writes verbatim into CSS; if a font isn't web-loadable the
// stack's last fallback (system-ui / sans-serif / monospace) takes
// over.
type DirectionFontStack struct {
	Display string `yaml:"display"`
	Body    string `yaml:"body"`
	Mono    string `yaml:"mono,omitempty"`
}

// VisualDirections is the parsed root of visual-directions.yaml.
type VisualDirections struct {
	Fallback5 []VisualDirection `yaml:"fallback_5"`
}

// LoadVisualDirections parses the embedded visual-directions.yaml
// and returns the resulting struct. Cached across Build calls
// (embed.FS is immutable per process). Errors propagate so a
// malformed yaml fails fast at first use instead of silently
// returning empty data.
func LoadVisualDirections() (*VisualDirections, error) {
	visualDirectionsCacheMu.Lock()
	defer visualDirectionsCacheMu.Unlock()
	if visualDirectionsCache != nil {
		return visualDirectionsCache, nil
	}
	data, err := skillsFS.ReadFile("_shared/visual-directions.yaml")
	if err != nil {
		return nil, fmt.Errorf("read visual-directions.yaml: %w", err)
	}
	var vd VisualDirections
	if err := yaml.Unmarshal(data, &vd); err != nil {
		return nil, fmt.Errorf("parse visual-directions.yaml: %w", err)
	}
	if err := validateVisualDirections(&vd); err != nil {
		return nil, fmt.Errorf("visual-directions.yaml: %w", err)
	}
	visualDirectionsCache = &vd
	return &vd, nil
}

// FindDirection looks up a single direction by id. Returns nil when
// the id doesn't exist — caller should treat as "no direction
// selected" rather than an error (the picker UI controls valid ids).
func FindDirection(id string) *VisualDirection {
	vd, err := LoadVisualDirections()
	if err != nil {
		return nil
	}
	for i := range vd.Fallback5 {
		if vd.Fallback5[i].ID == id {
			return &vd.Fallback5[i]
		}
	}
	return nil
}

// FormatDirectionXML renders a VisualDirection as the
// <visual-direction>...</visual-direction> block that
// SystemAdditionsComposer.SelectedDirection consumes. Mirrors the
// schema described in v2.4 §0.6.2 DS-3 — palette + font_stack +
// layout_posture + prompt_fragment surface verbatim so the model
// can quote them when emitting CSS / generation prompts.
//
// nil input → empty string. Composer skips empty fields cleanly.
func FormatDirectionXML(d *VisualDirection) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("<visual-direction id=\"")
	b.WriteString(d.ID)
	b.WriteString("\">\n")

	// Palette: list 4 named slots with both OKLch + hex so the
	// model can write whichever syntax the target surface needs.
	//
	// Iterate in a FIXED order — bg, fg, accent, muted. The previous
	// shape walked a map literal which is Go-randomized iteration;
	// two consecutive renders of the SAME direction produced
	// different byte sequences in palette key order. That breaks
	// system-prompt cache hits on every turn AND surfaces a confusing
	// diff in any prompt audit log. Fixed-order list = deterministic
	// output, no behavior change for the model (the slots are
	// semantically a map; order is purely a serialization detail).
	b.WriteString("palette:\n")
	paletteSlots := [...]struct {
		name string
		c    ColorValue
	}{
		{"bg", d.Palette.Bg},
		{"fg", d.Palette.Fg},
		{"accent", d.Palette.Accent},
		{"muted", d.Palette.Muted},
	}
	for _, slot := range paletteSlots {
		b.WriteString("  ")
		b.WriteString(slot.name)
		b.WriteString(": ")
		b.WriteString(slot.c.OKLch)
		b.WriteString("  /  ")
		b.WriteString(slot.c.Hex)
		b.WriteString("\n")
	}

	b.WriteString("font_stack:\n")
	if d.FontStack.Display != "" {
		b.WriteString("  display: ")
		b.WriteString(d.FontStack.Display)
		b.WriteString("\n")
	}
	if d.FontStack.Body != "" {
		b.WriteString("  body: ")
		b.WriteString(d.FontStack.Body)
		b.WriteString("\n")
	}
	if d.FontStack.Mono != "" {
		b.WriteString("  mono: ")
		b.WriteString(d.FontStack.Mono)
		b.WriteString("\n")
	}

	if d.LayoutPosture != "" {
		b.WriteString("layout: ")
		b.WriteString(d.LayoutPosture)
		b.WriteString("\n")
	}
	if d.PromptFragment != "" {
		b.WriteString("prompt_anchor: ")
		// PromptFragment can carry newlines from yaml block
		// scalars; collapse to inline form so the XML stays
		// flat and the model reads it as one cue.
		b.WriteString(strings.TrimSpace(strings.ReplaceAll(d.PromptFragment, "\n", " ")))
		b.WriteString("\n")
	}

	b.WriteString("</visual-direction>")
	return b.String()
}

// validateVisualDirections enforces shape contracts at parse time
// so a malformed yaml lands a clear error rather than producing
// half-broken directions that the picker silently skips.
func validateVisualDirections(vd *VisualDirections) error {
	if len(vd.Fallback5) == 0 {
		return fmt.Errorf("fallback_5 must not be empty")
	}
	if len(vd.Fallback5) < 3 {
		return fmt.Errorf("fallback_5 must carry ≥3 directions, got %d", len(vd.Fallback5))
	}
	seen := make(map[string]struct{}, len(vd.Fallback5))
	for i, d := range vd.Fallback5 {
		if d.ID == "" {
			return fmt.Errorf("fallback_5[%d] missing id", i)
		}
		if _, dup := seen[d.ID]; dup {
			return fmt.Errorf("fallback_5: duplicate id %q", d.ID)
		}
		seen[d.ID] = struct{}{}
		if d.LabelKey == "" {
			return fmt.Errorf("direction %q missing label_key", d.ID)
		}
		if d.Palette.Bg.Hex == "" || d.Palette.Fg.Hex == "" {
			return fmt.Errorf("direction %q missing required palette slots (bg/fg)", d.ID)
		}
		if d.Palette.Bg.OKLch == "" || d.Palette.Fg.OKLch == "" {
			return fmt.Errorf("direction %q missing OKLch values (bg/fg)", d.ID)
		}
		if d.FontStack.Display == "" || d.FontStack.Body == "" {
			return fmt.Errorf("direction %q missing font_stack (display/body)", d.ID)
		}
	}
	return nil
}

var (
	visualDirectionsCache   *VisualDirections
	visualDirectionsCacheMu sync.Mutex
)
