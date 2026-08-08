package workagent

// director_style_asset_descriptor.go — Sprint-D 2/7 (descriptor
// abstraction) → Sprint-E 3c/8 (retargeted at platform table).
// Adapts the platform-level model.DirectorStyle into the
// asset_library.Descriptor contract; reads/writes go through
// service/director_style.Default(). Sibling of brand_asset_descriptor
// + character_asset_descriptor.

import (
	"encoding/json"
	"time"

	"server/model"
	"server/service/asset_library"
	"server/service/director_style"
)

// directorStyleLibraryAsset wraps *model.DirectorStyle to satisfy
// LibraryAsset.
type directorStyleLibraryAsset struct {
	*model.DirectorStyle
}

// MarshalJSON projects raw Status int8 onto the asset_library.Status
// enum string on the wire — same fix as productLibraryAsset
// (see that file for the longer rationale).
func (d directorStyleLibraryAsset) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(d.DirectorStyle)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	m["status"] = d.GetStatus()
	return json.Marshal(m)
}

func (d directorStyleLibraryAsset) GetID() uint     { return d.DirectorStyle.Id }
func (d directorStyleLibraryAsset) GetUID() int     { return d.DirectorStyle.UID }
func (d directorStyleLibraryAsset) GetName() string { return d.DirectorStyle.Name }
func (d directorStyleLibraryAsset) GetSlug() string { return d.DirectorStyle.Slug }

// GetStatus projects (Confirmed + DeletedAt) onto the
// asset_library.Status string enum.
func (d directorStyleLibraryAsset) GetStatus() asset_library.Status {
	if d.DirectorStyle.DeletedAt != nil {
		return asset_library.StatusArchived
	}
	if !d.DirectorStyle.Confirmed {
		return asset_library.StatusDraft
	}
	return asset_library.StatusConfirmed
}

func (d directorStyleLibraryAsset) GetConfirmed() bool         { return d.DirectorStyle.Confirmed }
func (d directorStyleLibraryAsset) GetConfirmedAt() *time.Time { return d.DirectorStyle.ConfirmedAt }
func (d directorStyleLibraryAsset) GetDeletedAt() *time.Time   { return d.DirectorStyle.DeletedAt }
func (d directorStyleLibraryAsset) GetUpdatedAt() time.Time    { return d.DirectorStyle.UpdatedAt }
func (d directorStyleLibraryAsset) TableName() string          { return d.DirectorStyle.TableName() }

// WrapDirectorStyle exposes the adapter for callers (e.g. the API
// genre-filter list path) that already have the row in hand.
func WrapDirectorStyle(d *model.DirectorStyle) asset_library.LibraryAsset {
	if d == nil {
		return nil
	}
	return directorStyleLibraryAsset{d}
}

// directorStyleDescriptor implements asset_library.Descriptor for
// the director-style library. Stateless.
type directorStyleDescriptor struct{}

func (directorStyleDescriptor) Kind() asset_library.AssetKind {
	return asset_library.AssetKindDirectorStyle
}
func (directorStyleDescriptor) IndexLabel() string { return "director_styles" }
func (directorStyleDescriptor) URLPrefix() string  { return "director-style-assets" }

func (directorStyleDescriptor) NewEmpty() asset_library.LibraryAsset {
	return directorStyleLibraryAsset{&model.DirectorStyle{}}
}

func (directorStyleDescriptor) LoadByID(id, uid uint) (asset_library.LibraryAsset, error) {
	d, err := director_style.Default().LoadByIDForOwner(id, uid)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	return directorStyleLibraryAsset{d}, nil
}

func (directorStyleDescriptor) LoadLatestActive(uid uint) (asset_library.LibraryAsset, error) {
	d, err := director_style.Default().FindLatestActiveForOwner(uid)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	return directorStyleLibraryAsset{d}, nil
}

func (directorStyleDescriptor) LoadLatest(uid uint) (asset_library.LibraryAsset, error) {
	d, err := director_style.Default().FindLatestForOwner(uid)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	return directorStyleLibraryAsset{d}, nil
}

func (directorStyleDescriptor) List(uid uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := director_style.Default().ListForOwner(uid, limit, offset)
	if err != nil {
		return nil, err
	}
	EnrichDirectorStyleReferenceFlags(rows, uid)
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, directorStyleLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (directorStyleDescriptor) Search(uid uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := director_style.Default().SearchByOwner(uid, query, limit)
	if err != nil {
		return nil, err
	}
	EnrichDirectorStyleReferenceFlags(rows, uid)
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, directorStyleLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (directorStyleDescriptor) SearchByProject(uid, projectID uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := director_style.Default().SearchByOwnerProject(uid, projectID, query, limit)
	if err != nil {
		return nil, err
	}
	EnrichDirectorStyleReferenceFlags(rows, uid)
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, directorStyleLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (directorStyleDescriptor) ListByProject(uid, projectID uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := director_style.Default().ListByOwnerProject(uid, projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	EnrichDirectorStyleReferenceFlags(rows, uid)
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, directorStyleLibraryAsset{&rows[i]})
	}
	return out, nil
}

// EnrichDirectorStyleReferenceFlags populates HasReferencesCached
// on each row in a single bulk lookup against
// w_global_director_style_reference. Called from List paths so
// Summarise can emit hasReferences without an N+1 per-row query.
//
// Best-effort: a lookup error logs nothing and leaves the cache
// flags at their zero value (false). The wire-shape `hasReferences`
// stays accurate-or-false; never sets a spurious true on a query
// failure.
func EnrichDirectorStyleReferenceFlags(rows []model.DirectorStyle, uid uint) {
	if len(rows) == 0 {
		return
	}
	ids := make([]uint, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].Id)
	}
	withRefs, err := director_style.Default().HasReferencesByID(ids, uid)
	if err != nil || withRefs == nil {
		return
	}
	for i := range rows {
		if _, ok := withRefs[rows[i].Id]; ok {
			rows[i].HasReferencesCached = true
		}
	}
}

func (directorStyleDescriptor) MarkConfirmed(id, uid uint) error {
	return director_style.Default().MarkConfirmed(id, uid)
}

func (directorStyleDescriptor) SoftDelete(id, uid uint) error {
	return director_style.Default().SoftDelete(id, uid)
}

func (directorStyleDescriptor) Restore(id, uid uint) error {
	return director_style.Default().Restore(id, uid)
}

// FormatXML — type-asserts back to the concrete platform model and
// delegates to formatDirectorStyleXML in preflight.go.
func (directorStyleDescriptor) FormatXML(asset asset_library.LibraryAsset) string {
	if asset == nil {
		return ""
	}
	wrapped, ok := asset.(directorStyleLibraryAsset)
	if !ok {
		return ""
	}
	return formatDirectorStyleXML(wrapped.DirectorStyle)
}

// Summarise projects model.DirectorStyle into the kind-agnostic
// Summary wire shape. Director-specific hints (era / genre /
// has-references) ride in Extras.
//
// hasReferences reads from HasReferencesCached, populated by
// EnrichDirectorStyleReferenceFlags in the List paths. A caller
// that doesn't go through List (e.g. one-off LoadByID + Summarise)
// gets hasReferences=false; that's fine for the current list-
// rendering call sites — the detail-view path loads the references
// collection separately.
func (directorStyleDescriptor) Summarise(asset asset_library.LibraryAsset) asset_library.Summary {
	if asset == nil {
		return asset_library.Summary{}
	}
	wrapped, ok := asset.(directorStyleLibraryAsset)
	if !ok {
		return asset_library.Summary{}
	}
	d := wrapped.DirectorStyle
	return asset_library.Summary{
		ID:         d.Id,
		UID:        d.UID,
		Name:       d.Name,
		Slug:       d.Slug,
		Status:     wrapped.GetStatus(),
		SourceKind: asset_library.SourceKind(d.SourceKind),
		Confirmed:  d.Confirmed,
		CreatedAt:  d.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  d.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Extras: map[string]interface{}{
			"era":           d.Era,
			"genre":         d.Genre,
			"hasReferences": d.HasReferencesCached,
		},
	}
}

func init() {
	asset_library.Default().Register(directorStyleDescriptor{})
}
