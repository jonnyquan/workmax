package asset_library

import (
	"encoding/json"
)

// Summary is the trimmed wire shape for list responses, lifted
// from the per-type brandAssetSummary / characterAssetSummary /
// directorStyleAssetSummary structs. The shared fields are
// what the asset library card needs to render WITHOUT loading
// the full row's JSON sections.
//
// Type-specific projections (palette swatches for brand, avatar
// for character, has-references for director-style) ride in
// the Extras map so the wire shape stays uniform but each kind
// can attach its own hints. Frontend reads Extras[<key>] when
// rendering the card; missing keys gracefully degrade.
//
// Wire shape — MarshalJSON flattens Extras up to the parent
// object so the JSON output matches the legacy per-type summary
// shapes byte-for-byte (e.g. brand emits paletteSwatches at the
// top level, not under .extras.paletteSwatches). This keeps
// Sprint-D backend refactor backward-compatible with the
// frontend in flight; Phase 5 + 6 may renest later.
type Summary struct {
	ID         uint                   `json:"id"`
	UID        int                    `json:"uid"`
	Name       string                 `json:"name"`
	Slug       string                 `json:"slug"`
	Status     Status                 `json:"status"`
	SourceKind SourceKind             `json:"sourceKind"`
	Confirmed  bool                   `json:"confirmed"`
	CreatedAt  string                 `json:"createdAt"`
	UpdatedAt  string                 `json:"updatedAt"`

	// Extras carries kind-specific hint signals. Populated by the
	// descriptor's Summarise method. Frontend reads:
	//
	//   brand          → "hasColors" / "hasFonts" / "paletteSwatches"
	//   character      → "role" / "gender" / "ageRange" /
	//                    "hasReference" / "canonicalImagePath"
	//   director-style → "era" / "genre" / "hasReferences"
	//
	// Keys are stable strings, not enums, so the descriptor can
	// extend without touching the framework. The MarshalJSON method
	// flattens this up to the parent JSON object — the wire shape
	// presents these as top-level fields, matching the legacy
	// per-type summary structs.
	Extras map[string]interface{} `json:"-"`
}

// MarshalJSON emits Summary with Extras flattened into the parent
// JSON object. Wire-shape preservation: legacy per-type summaries
// (brandAssetSummary etc.) had hint fields like paletteSwatches as
// top-level JSON keys; Sprint-D 4/7's generic handler must produce
// the same byte stream so the frontend (and existing tests) keep
// working unchanged.
//
// Conflict resolution — typed Summary fields win over Extras keys
// of the same JSON name. Descriptor implementations should never
// stuff a typed-field name into Extras; this method protects
// against accidents.
func (s Summary) MarshalJSON() ([]byte, error) {
	type aliasSummary Summary
	base, err := json.Marshal(aliasSummary(s))
	if err != nil {
		return nil, err
	}
	if len(s.Extras) == 0 {
		return base, nil
	}

	// Round-trip: unmarshal the typed-field JSON into a map, merge
	// Extras under it (typed wins), re-marshal. One allocation pass
	// — the cardinality is tiny (≤ ~12 keys) so this is well
	// within the chat hot-path budget.
	var merged map[string]interface{}
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range s.Extras {
		if _, taken := merged[k]; taken {
			continue
		}
		merged[k] = v
	}
	return json.Marshal(merged)
}

// Descriptor is the per-kind plug-in that the generic registry,
// preflight, and REST handlers consume. Each implementation owns:
//   - identity (Kind() string)
//   - storage (NewEmpty for find targets, repo bindings)
//   - rendering (FormatXML for prompt injection, Summarise for list)
//
// Implementations live next to the concrete model and repo in the
// workagent package (e.g. brand_descriptor.go) so type-specific
// formatting code stays close to the type-specific field shapes
// it reads. The descriptor itself is small — most behavior lives
// in the repo it wraps.
//
// Phase 1 (this commit) defines the interface; Phase 2 lands
// concrete descriptors and Phase 3 onwards rewires preflight /
// REST / frontend to consume Descriptor instead of branching
// per asset type.
type Descriptor interface {
	// Kind returns the canonical enum value this descriptor
	// handles. Used by the registry as the lookup key.
	Kind() AssetKind

	// NewEmpty returns a zero-value of the concrete model so
	// generic GORM helpers (`db.First(asset, id)`) have a target
	// to populate. The returned LibraryAsset is also the
	// canonical instance for `db.Model(...)` operations.
	NewEmpty() LibraryAsset

	// LoadLatestActive returns the user's latest CONFIRMED asset
	// of this kind, or nil + nil when none exists (the empty
	// case is NOT an error — it's the most-common state for
	// fresh users).
	//
	// Mirrors the existing per-type FindLatestActiveForOwner
	// methods. Used by preflight to inject the active brand /
	// character / director-style into the system prompt.
	LoadLatestActive(uid uint) (LibraryAsset, error)

	// LoadLatest returns the latest non-archived asset (draft OR
	// confirmed) for uid. Used by the M4 watermark path where
	// drafts ride into preflight with [待品牌方确认] until the
	// user vocalizes.
	LoadLatest(uid uint) (LibraryAsset, error)

	// List paginates the user's library. limit / offset are
	// clamped at the repo layer (≤100 / ≥0).
	List(uid uint, limit, offset int) ([]LibraryAsset, error)

	// Search returns assets whose name or slug LIKE-matches
	// `query`, scoped to uid, newest first. Used by the agent-
	// facing `lookup_asset` production tool so the user can say
	// "use my coffee brand" and let the agent SQL-search instead
	// of injecting the whole library into preflight.
	//
	// Empty query returns nil (falsy-input → empty-result is
	// safer than "match everything", which would be List in
	// disguise). limit is clamped at the repo layer (≤50).
	Search(uid uint, query string, limit int) ([]LibraryAsset, error)

	// SearchByProject is Search scoped to a project. projectID=0
	// falls through to the unscoped Search (uid-only) so callers
	// can pass projectID unconditionally without a per-call
	// branch. Plan-A Phase A2 — exposes the project_id column
	// already on the asset tables through the descriptor seam.
	SearchByProject(uid, projectID uint, query string, limit int) ([]LibraryAsset, error)

	// ListByProject is List scoped to a project. Same contract
	// as SearchByProject: projectID=0 falls through to the
	// uid-only List.
	ListByProject(uid, projectID uint, limit, offset int) ([]LibraryAsset, error)

	// FormatXML renders the asset as the system-prompt block
	// that the SystemAdditionsComposer consumes (e.g.
	// <brand-spec>name: ...\ncolors: {...}\n</brand-spec>).
	// Returns "" when the asset is nil or has no surfaceable
	// content — matches the existing per-type formatters'
	// "drop empty cleanly" contract.
	FormatXML(asset LibraryAsset) string

	// Summarise projects the row into the wire shape the
	// frontend list consumes. Common identity fields land in
	// the typed slots; type-specific hints (palette swatches,
	// avatar path, has-references signal) ride in Extras.
	Summarise(asset LibraryAsset) Summary

	// IndexLabel returns the section header used in the
	// <asset-library-index> preflight injection — "brands",
	// "characters", "director_styles". Plural snake_case so the
	// agent's prompt stays grep-friendly. Distinct from Kind() (which
	// is the URL-/JSON-safe singular hyphenated form) so the prompt
	// label stays stable even if the routing kind ever evolves.
	IndexLabel() string

	// URLPrefix returns the path segment under /api/work-agent for
	// this kind's REST surface — "brand-assets", "character-assets",
	// "director-style-assets". Plural hyphenated form, matching
	// existing routes. Routers walk Default().All() and bind
	// generic handlers under "{base}/{URLPrefix()}".
	URLPrefix() string

	// LoadByID is the GetXxx handler's read path — load one row by
	// id, scoped to uid (cross-tenant returns ErrRecordNotFound).
	// Used by GET /:id and as the post-write disambiguator on
	// idempotent confirm.
	LoadByID(id, uid uint) (LibraryAsset, error)

	// MarkConfirmed flips a draft to confirmed atomically. The
	// existing per-type repos document idempotency: a row that's
	// already confirmed returns ErrRecordNotFound (caller
	// disambiguates via LoadByID after).
	MarkConfirmed(id, uid uint) error

	// SoftDelete archives the row (status=archived + deleted_at).
	// Cross-tenant or already-deleted both return ErrRecordNotFound,
	// which the handler surfaces as 404 (no existence oracle).
	SoftDelete(id, uid uint) error

	// Restore reverses a soft-delete: clears deleted_at + flips
	// status archived → draft. The user re-confirms via
	// MarkConfirmed if they want the asset active in preflight again.
	// Returns ErrRecordNotFound when the row isn't currently
	// soft-deleted (already active OR cross-tenant).
	Restore(id, uid uint) error
}
