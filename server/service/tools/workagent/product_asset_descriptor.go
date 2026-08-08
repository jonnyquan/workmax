package workagent

// product_asset_descriptor.go — P1 #5 slice 2. Adapts the
// platform-level model.Product into the asset_library.Descriptor
// contract. Mirrors brand_asset_descriptor.go's structure so the
// asset-library registry's "walk every kind uniformly" contract
// holds.
//
// Lifecycle reconciliation (same as brand):
//   DeletedAt != nil  → StatusArchived
//   Confirmed = false → StatusDraft
//   Confirmed = true  → StatusConfirmed

import (
	"encoding/json"
	"time"

	"server/model"
	"server/service/asset_library"
	"server/service/product"
)

// productLibraryAsset wraps *model.Product to satisfy LibraryAsset.
type productLibraryAsset struct {
	*model.Product
}

// MarshalJSON projects the raw `Status int8` (1/0) onto the
// asset_library.Status enum string ("draft" / "confirmed" /
// "archived") on the WIRE so the frontend AssetDetailDrawer's
// `detail.status === 'draft'` gate matches reality. Without this
// override, GET /api/product/:id ships `"status": 1` and the
// drawer's Confirm button never appears for draft rows because
// 1 !== 'draft'.
//
// The list path (Summary.MarshalJSON in service/asset_library)
// already projects status correctly; this fixes the parallel
// Detail path, which marshals the raw wrapper. Same fix lives on
// brand / character / director_style wrappers.
//
// Implementation: marshal the embedded *model.Product (NOT p
// itself — that'd recurse), unmarshal back into a map, override
// `status` with GetStatus(), re-marshal. The two-step is the
// idiomatic Go pattern for "marshal an embedded struct + tweak
// one field" without writing every other field by hand.
func (p productLibraryAsset) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(p.Product)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	m["status"] = p.GetStatus()
	return json.Marshal(m)
}

func (p productLibraryAsset) GetID() uint     { return p.Product.Id }
func (p productLibraryAsset) GetUID() int     { return p.Product.UID }
func (p productLibraryAsset) GetName() string { return p.Product.Name }
func (p productLibraryAsset) GetSlug() string { return p.Product.Slug }

func (p productLibraryAsset) GetStatus() asset_library.Status {
	if p.Product.DeletedAt != nil {
		return asset_library.StatusArchived
	}
	if !p.Product.Confirmed {
		return asset_library.StatusDraft
	}
	return asset_library.StatusConfirmed
}

func (p productLibraryAsset) GetConfirmed() bool         { return p.Product.Confirmed }
func (p productLibraryAsset) GetConfirmedAt() *time.Time { return p.Product.ConfirmedAt }
func (p productLibraryAsset) GetDeletedAt() *time.Time   { return p.Product.DeletedAt }
func (p productLibraryAsset) GetUpdatedAt() time.Time    { return p.Product.UpdatedAt }
func (p productLibraryAsset) TableName() string          { return p.Product.TableName() }

// WrapProduct exposes the adapter for callers (e.g. the
// api-package list path) that already have a *model.Product in
// hand. Mirrors WrapBrand / WrapDirectorStyleAsset.
func WrapProduct(p *model.Product) asset_library.LibraryAsset {
	if p == nil {
		return nil
	}
	return productLibraryAsset{p}
}

// productDescriptor is the Descriptor for the product asset
// library. Stateless — every call resolves the singleton repo
// afresh so a hot-reload of GraDBs picks up immediately.
type productDescriptor struct{}

func (productDescriptor) Kind() asset_library.AssetKind { return asset_library.AssetKindProduct }
func (productDescriptor) IndexLabel() string            { return "products" }
func (productDescriptor) URLPrefix() string             { return "product-assets" }

func (productDescriptor) NewEmpty() asset_library.LibraryAsset {
	return productLibraryAsset{&model.Product{}}
}

func (productDescriptor) LoadByID(id, uid uint) (asset_library.LibraryAsset, error) {
	p, err := product.Default().LoadByIDForOwner(id, uid)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	return productLibraryAsset{p}, nil
}

func (productDescriptor) LoadLatestActive(uid uint) (asset_library.LibraryAsset, error) {
	p, err := product.Default().FindLatestActiveForOwner(uid)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	return productLibraryAsset{p}, nil
}

func (productDescriptor) LoadLatest(uid uint) (asset_library.LibraryAsset, error) {
	p, err := product.Default().FindLatestForOwner(uid)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	return productLibraryAsset{p}, nil
}

func (productDescriptor) List(uid uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := product.Default().ListForOwner(uid, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, productLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (productDescriptor) Search(uid uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := product.Default().SearchByOwner(uid, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, productLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (productDescriptor) SearchByProject(uid, projectID uint, query string, limit int) ([]asset_library.LibraryAsset, error) {
	rows, err := product.Default().SearchByOwnerProject(uid, projectID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, productLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (productDescriptor) ListByProject(uid, projectID uint, limit, offset int) ([]asset_library.LibraryAsset, error) {
	rows, err := product.Default().ListByOwnerProject(uid, projectID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]asset_library.LibraryAsset, 0, len(rows))
	for i := range rows {
		out = append(out, productLibraryAsset{&rows[i]})
	}
	return out, nil
}

func (productDescriptor) MarkConfirmed(id, uid uint) error {
	return product.Default().MarkConfirmed(id, uid)
}

func (productDescriptor) SoftDelete(id, uid uint) error {
	return product.Default().SoftDelete(id, uid)
}

func (productDescriptor) Restore(id, uid uint) error {
	return product.Default().Restore(id, uid)
}

// FormatXML delegates to formatProductSpecXML in preflight.go.
// Type-asserts back to the concrete model; returns "" on nil or
// type-assert failure (matches brand/character descriptors).
func (productDescriptor) FormatXML(asset asset_library.LibraryAsset) string {
	if asset == nil {
		return ""
	}
	wrapped, ok := asset.(productLibraryAsset)
	if !ok {
		return ""
	}
	return formatProductSpecXML(wrapped.Product)
}

// Summarise projects model.Product into the kind-agnostic
// Summary wire shape. Product-specific hints (has-sku /
// has-category / has-specs) ride in Extras.
func (productDescriptor) Summarise(asset asset_library.LibraryAsset) asset_library.Summary {
	if asset == nil {
		return asset_library.Summary{}
	}
	wrapped, ok := asset.(productLibraryAsset)
	if !ok {
		return asset_library.Summary{}
	}
	p := wrapped.Product
	hasJSON := func(m model.JSONMap) bool { return len(m) > 0 }
	return asset_library.Summary{
		ID:         p.Id,
		UID:        p.UID,
		Name:       p.Name,
		Slug:       p.Slug,
		Status:     wrapped.GetStatus(),
		SourceKind: asset_library.SourceKind(p.SourceKind),
		Confirmed:  p.Confirmed,
		CreatedAt:  p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  p.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
		Extras: map[string]interface{}{
			"sku":               p.SKU,
			"category":          p.Category,
			"hasSpecs":          hasJSON(p.Specs),
			"hasVisualGuidance": hasJSON(p.VisualGuidance),
		},
	}
}

// _ keeps the json import alive against future Summarise changes
// (e.g. when we want to surface a parsed-specs preview); the
// brand descriptor uses json.Marshal in its Summarise so the
// pattern stays parallel.
var _ = json.Marshal

func init() {
	asset_library.Default().Register(productDescriptor{})
}
