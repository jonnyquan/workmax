package workagent

// brand_asset_descriptor.go — Sprint-D 2/7 (descriptor abstraction)
// → Sprint-E 3/N (retargeted at platform table). Adapts the
// platform-level model.Brand into the asset_library.Descriptor
// contract; reads/writes go through service/brand.Default().
//
// Lifecycle reconciliation (Sprint-E design Q&A): the platform
// w_brand table has flat Status int8 (1=active) + Confirmed bool +
// DeletedAt soft-delete. The asset_library.Status string enum
// (draft / confirmed / archived) projects from this tuple at the
// adapter boundary:
//   DeletedAt != nil  → StatusArchived
//   Confirmed = false → StatusDraft
//   Confirmed = true  → StatusConfirmed
//
// Why the adapter wrapper still exists: the model package can't
// gain methods without an upward import on asset_library; the
// pointer-wrap costs one indirection per accessor on the chat-turn
// path, immeasurable.

import (
	"encoding/json"
	"time"

	"server/model"
	"server/service/asset_library"
	"server/service/brand"
)

// brandLibraryAsset wraps *model.Brand to satisfy LibraryAsset.
type brandLibraryAsset struct {
	*model.Brand
}

// MarshalJSON projects raw Status int8 onto the asset_library.Status
// enum string on the wire — same fix as productLibraryAsset
// (see that file for the longer rationale). Closes the Detail-
// path drift where `GET /api/brand/:id` shipped `"status": 1` and
// the drawer's `detail.status === 'draft'` gate always failed.
func (b brandLibraryAsset) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(b.Brand)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	m["status"] = b.GetStatus()
	return json.Marshal(m)
}

func (b brandLibraryAsset) GetID() uint     { return b.Brand.Id }
func (b brandLibraryAsset) GetUID() int     { return b.Brand.UID }
func (b brandLibraryAsset) GetName() string { return b.Brand.Name }
func (b brandLibraryAsset) GetSlug() string { return b.Brand.Slug }

// GetStatus projects the platform's (Status int8 + Confirmed +
// DeletedAt) tuple onto the asset_library.Status string enum so
// the workagent UI's draft/confirmed/archived semantics keep
// flowing unchanged.
func (b brandLibraryAsset) GetStatus() asset_library.Status {
	if b.Brand.DeletedAt != nil {
		return asset_library.StatusArchived
	}
	if !b.Brand.Confirmed {
		return asset_library.StatusDraft
	}
	return asset_library.StatusConfirmed
}

func (b brandLibraryAsset) GetConfirmed() bool         { return b.Brand.Confirmed }
func (b brandLibraryAsset) GetConfirmedAt() *time.Time { return b.Brand.ConfirmedAt }
func (b brandLibraryAsset) GetDeletedAt() *time.Time   { return b.Brand.DeletedAt }
func (b brandLibraryAsset) GetUpdatedAt() time.Time    { return b.Brand.UpdatedAt }
func (b brandLibraryAsset) TableName() string          { return b.Brand.TableName() }

// WrapBrand exposes the adapter for callers (e.g. the api-package
// list path) that already have a *model.Brand in hand and don't
// want to re-hit the DB. Mirrors WrapDirectorStyleAsset.
func WrapBrand(b *model.Brand) asset_library.LibraryAsset {
	if b == nil {
		return nil
	}
	return brandLibraryAsset{b}
}

// brandDescriptor is the Descriptor for the brand asset library.
// Stateless — every call resolves the singleton repo afresh so a
// hot-reload of GraDBs picks up immediately.
type brandDescriptor struct{}

func (brandDescriptor) Kind() asset_library.AssetKind { return asset_library.AssetKindBrand }
func (brandDescriptor) IndexLabel() string            { return "brands" }
func (brandDescriptor) URLPrefix() string             { return "brand-assets" }

func (brandDescriptor) NewEmpty() asset_library.LibraryAsset {
	return brandLibraryAsset{&model.Brand{}}
}

func (brandDescriptor) LoadByID(id, uid uint) (asset_library.LibraryAsset, error) {
	b, err := brand.Default().LoadByIDForOwner(id, uid)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, nil
	}
	return brandLibraryAsset{b}, nil
}

func (brandDescriptor) LoadLatestActive(uid uint) (asset_library.LibraryAsset, error) {
	b, err := brand.Default().FindLatestActiveForOwner(uid)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, nil
	}
	return brandLibraryAsset{b}, nil
}

func (brandDescriptor) LoadLatest(uid uint) (asset_library.LibraryAsset, error) {
	b, err := brand.Default().FindLatestForOwner(uid)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, nil
	}
	return brandLibraryAsset{b}, nil
}

func (brandDescriptor) List(uid uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := brand.Default().ListForOwner(uid, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, brandLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (brandDescriptor) Search(uid uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := brand.Default().SearchByOwner(uid, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, brandLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (brandDescriptor) SearchByProject(uid, projectID uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := brand.Default().SearchByOwnerProject(uid, projectID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, brandLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (brandDescriptor) ListByProject(uid, projectID uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := brand.Default().ListByOwnerProject(uid, projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, brandLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (brandDescriptor) MarkConfirmed(id, uid uint) error {
	return brand.Default().MarkConfirmed(id, uid)
}

func (brandDescriptor) SoftDelete(id, uid uint) error {
	return brand.Default().SoftDelete(id, uid)
}

func (brandDescriptor) Restore(id, uid uint) error {
	return brand.Default().Restore(id, uid)
}

// FormatXML delegates to formatBrandSpecXML in preflight.go.
// Type-asserts back to the concrete model — boundary where erasure
// unwinds. Returns "" on nil or type-assert failure.
func (brandDescriptor) FormatXML(asset asset_library.LibraryAsset) string {
	if asset == nil {
		return ""
	}
	wrapped, ok := asset.(brandLibraryAsset)
	if !ok {
		return ""
	}
	return formatBrandSpecXML(wrapped.Brand)
}

// Summarise projects model.Brand into the kind-agnostic Summary
// wire shape. Type-specific hints (palette swatches, has-colors /
// has-fonts) ride in Extras.
//
// Sprint-E mapping note: the JSONMap sections (Colors / Typography)
// marshal to []byte for the existing ExtractPaletteSwatches +
// hasJSON helpers. Cost: one alloc per Summarise; Summarise runs
// at list-rendering time (≤50 rows) so it's well within the chat-
// path budget.
func (brandDescriptor) Summarise(asset asset_library.LibraryAsset) asset_library.Summary {
	if asset == nil {
		return asset_library.Summary{}
	}
	wrapped, ok := asset.(brandLibraryAsset)
	if !ok {
		return asset_library.Summary{}
	}
	b := wrapped.Brand
	hasJSON := func(m model.JSONMap) bool { return len(m) > 0 }
	colorsBytes, _ := json.Marshal(b.Colors)
	return asset_library.Summary{
		ID:         b.Id,
		UID:        b.UID,
		Name:       b.Name,
		Slug:       b.Slug,
		Status:     wrapped.GetStatus(),
		SourceKind: asset_library.SourceKind(b.SourceKind),
		Confirmed:  b.Confirmed,
		CreatedAt:  b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  b.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Extras: map[string]interface{}{
			"hasColors":       hasJSON(b.Colors),
			"hasFonts":        hasJSON(b.Typography),
			"paletteSwatches": ExtractPaletteSwatches(colorsBytes),
		},
	}
}

func init() {
	asset_library.Default().Register(brandDescriptor{})
}
