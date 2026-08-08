package workagent

// brand_palette.go — Sprint-D 2/7 lift. Pure-logic helpers for
// extracting hex color tokens out of a brand_asset's colors JSON.
// Originally lived in api/pro/tools/workagent/brand_asset_api.go;
// hoisted here so the asset_library descriptor (this package) can
// call it without an upward import. The API file now delegates.
//
// Schema-flexible: the M4 protocol doesn't pin a single shape, so
// we walk the JSON looking for any "#xxx" / "#xxxxxx" string values
// regardless of the surrounding key structure. Conventional slots
// (primary / accent / muted / bg / fg) lead the result; other hex
// values fill remaining slots in deterministic alphabetical order.

import (
	"encoding/json"
	"strings"
)

// ExtractPaletteSwatches pulls up to 4 hex color tokens out of a
// brand's colors JSON for library-card swatch rendering. Returns
// nil when no recognisable hex value exists (caller falls back to
// the "colors" text chip).
//
// Public so callers across packages (api / asset_library descriptor)
// share one implementation.
func ExtractPaletteSwatches(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	s := string(raw)
	if s == "null" || s == "{}" {
		return nil
	}
	var data map[string]interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}

	out := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	add := func(v interface{}) {
		hex, ok := v.(string)
		if !ok || len(out) >= 4 {
			return
		}
		hex = strings.TrimSpace(hex)
		if !looksLikeHexColor(hex) {
			return
		}
		key := strings.ToLower(hex)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, hex)
	}

	// Conventional slots first — matches M4's primary→accent→
	// muted→bg→fg canonical ordering.
	for _, k := range []string{"primary", "accent", "muted", "bg", "fg"} {
		if v, ok := data[k]; ok {
			add(v)
		}
	}
	// Then any remaining hex values, walking the tree
	// alphabetically so the swatch order is deterministic across
	// turns (prompt-cache stability when the swatch list rides
	// into a system-prompt log line).
	walkForHexes(data, add)
	return out
}

func looksLikeHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func walkForHexes(v interface{}, add func(interface{})) {
	switch t := v.(type) {
	case string:
		add(t)
	case []interface{}:
		for _, item := range t {
			walkForHexes(item, add)
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortStringsAsc(keys)
		for _, k := range keys {
			walkForHexes(t[k], add)
		}
	}
}

func sortStringsAsc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
