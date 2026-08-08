// Package asset_library provides the parametric abstraction over
// the platform's user-facing asset types (brand, character,
// director-style — and any future kind). Each kind ships as a
// single Descriptor registration; the registry's consumers
// (preflight injectors, REST handlers, frontend list components)
// iterate it instead of branching per-type.
//
// History — Sprint-D (2026-05-10) introduced the abstraction inside
// the workagent module to collapse three near-identical asset
// implementations (~1300 LOC each). Sprint-E (2026-05-11) promoted
// the package to platform level so character / brand / director-
// style become first-class platform assets backed by w_character
// (already platform-level) + the new w_global_brand / w_global_director_style
// tables — accessible from canvas @-mentions, TTS voice presets,
// and any future tool, not just workagent preflight.
//
// This file owns AssetKind — the canonical enum that callers pass
// to identify which library they mean. Strings match the existing
// frontend's AssetLibraryTab union ("brand", "character",
// "director-style") so URL params and routing keys flow unchanged
// across the boundary.
package asset_library

// AssetKind identifies one of the three asset libraries the work-agent
// module exposes. Adding a fourth library is now a one-line constant
// + a descriptor registration, not a per-layer copy-paste.
//
// Values are the canonical wire identities consumed by the bundled Desktop
// renderer, so URL/search parameters feed Kind lookups without translation.
type AssetKind string

const (
	AssetKindBrand         AssetKind = "brand"
	AssetKindCharacter     AssetKind = "character"
	AssetKindDirectorStyle AssetKind = "director-style"
	// AssetKindProduct — P1 #5. Promoted from "deferred" status
	// the platform design doc carried into a first-class fourth
	// kind. Reachable from canvas @-mentions, lookup_asset, REST
	// surface, and the workagent preflight injectors — same as
	// the other three.
	AssetKindProduct AssetKind = "product"
)

// String satisfies fmt.Stringer for log lines + error messages.
// Values are the same JSON / URL-safe strings the constants carry,
// so wrapping a kind in error context produces stable diagnostics.
func (k AssetKind) String() string {
	return string(k)
}

// IsValid reports whether the kind is one of the registered types.
// Used by handlers that take a kind from URL / request body — a
// non-matching value short-circuits to a 400 instead of reaching
// the registry lookup. Defensive: registry.Get already errors on
// unknown kinds, this is the cheap pre-check.
func (k AssetKind) IsValid() bool {
	switch k {
	case AssetKindBrand, AssetKindCharacter, AssetKindDirectorStyle, AssetKindProduct:
		return true
	default:
		return false
	}
}

// AllKinds returns the canonical enumeration order for cross-kind
// surfaces (the AssetLibraryIndex preflight injection, the library
// shell tab order, etc.). Brand → Character → Director-style
// matches the abstract → concrete progression the SystemAdditions
// composer's layer order documents (brand identity → who appears →
// how shots look).
func AllKinds() []AssetKind {
	// Product lands AFTER director-style so the canonical
	// abstract → concrete reading order holds: brand sets
	// identity, character is who appears, director-style
	// is how shots look, product is what's being sold /
	// showcased in those shots.
	return []AssetKind{
		AssetKindBrand,
		AssetKindCharacter,
		AssetKindDirectorStyle,
		AssetKindProduct,
	}
}
