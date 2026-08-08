package asset_library

import "time"

// Status is the shared lifecycle vocabulary across every asset type.
// The three concrete asset models (BrandAssetStatus,
// CharacterAssetStatus, DirectorStyleAssetStatus) all use the same
// underlying string values — this enum codifies that contract.
type Status string

const (
	StatusDraft     Status = "draft"
	StatusConfirmed Status = "confirmed"
	StatusArchived  Status = "archived"
)

// SourceKind matches the BrandAssetSourceKind / CharacterAssetSourceKind
// / DirectorStyleAssetSourceKind enums on the concrete models. Each
// shares the same four canonical values; abstracting here lets the
// registry / library-UI badge logic stay type-agnostic.
type SourceKind string

const (
	SourceExtracted SourceKind = "extracted"
	SourceUploaded  SourceKind = "uploaded"
	SourceManual    SourceKind = "manual"
	SourceImported  SourceKind = "imported"
)

// LibraryAsset is the row contract every asset type implements. The
// registry / generic repo / preflight loaders only see this surface;
// type-specific fields (brand's 7 JSON sections, character's avatar
// path, director-style's 5 cinematic axes) stay on the concrete
// model and are reached via the descriptor's type-aware helpers
// (FormatXML, Summarise — the only places that need to know which
// concrete shape they're rendering).
//
// The interface is intentionally narrow:
//   - Identity (id, uid, name, slug)
//   - Lifecycle (status, confirmed, deleted_at, updated_at)
//   - Persistence (table name)
//
// No setters — mutations go through the repo (which is the only
// caller that should rewrite a row), not through the interface.
//
// Concrete models add this interface in Phase 2 of the Sprint-D
// refactor; for Phase 1 the interface is the type-level contract
// without implementations yet.
type LibraryAsset interface {
	// Identity
	GetID() uint
	GetUID() int
	GetName() string
	GetSlug() string

	// Lifecycle
	GetStatus() Status
	GetConfirmed() bool
	GetConfirmedAt() *time.Time
	GetDeletedAt() *time.Time
	GetUpdatedAt() time.Time

	// Persistence — used by generic repo helpers that need the
	// table name without reflecting the concrete struct. GORM
	// already requires every model to define this; we just lift
	// the requirement into the interface so the registry can
	// satisfy "give me a table name for this kind."
	TableName() string
}

// IsActive captures the strict "fully vetted, restorable" predicate
// the existing per-type IsActive() methods all share. Confirmed
// status with no soft-delete. Lifted here so callers iterating
// the registry don't have to type-switch.
func IsActive(a LibraryAsset) bool {
	if a == nil {
		return false
	}
	return a.GetStatus() == StatusConfirmed && a.GetDeletedAt() == nil
}
