// Package product owns the platform-level w_global_product +
// w_global_product_reference CRUD layer (P1 #5). Mirrors
// service/brand's surface exactly — the asset_library Descriptor
// adapter projects this repo through the registry alongside
// Brand / Character / DirectorStyle.
//
// Why mirror brand instead of generalising: the asset_library
// descriptor is the abstraction layer (one interface, four
// concrete repos behind it). Generalising the repo layer would
// either lose type information (returning []interface{}) or
// require generics gymnastics that adds friction at every call
// site. Four ~350-LOC repos with parallel shape is a clear win
// over a generic repo with type assertions on every read.
//
// Surface conventions mirror service/brand:
//   - *ForOwner methods bake uid into the WHERE clause; cross-
//     tenant collapses to gorm.ErrRecordNotFound (single sentinel
//     for the IDOR contract)
//   - SearchByOwnerProject / ListByOwnerProject — projectID=0
//     falls through to the uid-only variant
//   - Lifecycle reconciliation: workagent's draft/confirmed/
//     archived enum maps onto Status int8 + Confirmed bool +
//     DeletedAt soft-delete
package product

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// Repository wraps the w_global_product DB ops. Stateless apart
// from the *gorm.DB binding so tests can inject NewTestDB.
type Repository struct {
	db *gorm.DB
}

// NewRepository binds the repo to a *gorm.DB.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Default returns a repo bound to the system DB. Convenience
// for handler code that doesn't otherwise plumb a *gorm.DB.
func Default() *Repository {
	return NewRepository(globals.GraDBs["system"])
}

// Create inserts a new product row. UID + Name required;
// partial extractions (no specs yet, no visual_guidance) ride
// through with empty JSON sections defaulted at the DB layer.
//
// Select("*") forces GORM to write zero-value bool fields
// (Confirmed=false on workagent draft extractions). Without
// this, the DDL DEFAULT 1 would override an explicit false.
func (r *Repository) Create(p *model.Product) error {
	if p == nil {
		return errors.New("nil product")
	}
	if p.UID == 0 {
		return errors.New("product must have non-zero uid")
	}
	if p.Name == "" {
		return errors.New("product must have a name")
	}
	if p.SourceKind == "" {
		p.SourceKind = model.ProductSourceManual
	}
	if p.Status == 0 {
		p.Status = model.ProductStatusActive
	}
	if p.Lang == "" {
		p.Lang = "en"
	}
	if err := r.db.Select("*").Create(p).Error; err != nil {
		return fmt.Errorf("create product: %w", err)
	}
	return nil
}

// LoadByIDForOwner reads one product by id when uid matches.
// Cross-tenant + missing both collapse to ErrRecordNotFound —
// the single sentinel keeps the IDOR contract simple.
func (r *Repository) LoadByIDForOwner(id, uid uint) (*model.Product, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	p := &model.Product{}
	q := r.db.Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid)
	if err := q.First(p).Error; err != nil {
		return nil, err
	}
	return p, nil
}

// FindLatestActiveForOwner returns the user's most recently
// confirmed + non-deleted product. "Active" here means
// Confirmed=true AND DeletedAt IS NULL — i.e. fully vetted,
// ready to inject into preflight. Drafts excluded; the
// watermark path uses FindLatestForOwner.
func (r *Repository) FindLatestActiveForOwner(uid uint) (*model.Product, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	p := &model.Product{}
	err := r.db.
		Where("uid = ? AND confirmed = ? AND status = ? AND deleted_at IS NULL",
			uid, true, model.ProductStatusActive).
		Order("updated_at DESC").
		First(p).Error
	if err != nil {
		return nil, err
	}
	return p, nil
}

// FindLatestForOwner returns the latest product in ANY
// confirmation state (draft + confirmed), excluding soft-deleted.
// Backs the watermark path.
func (r *Repository) FindLatestForOwner(uid uint) (*model.Product, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	p := &model.Product{}
	err := r.db.
		Where("uid = ? AND status = ? AND deleted_at IS NULL", uid, model.ProductStatusActive).
		Order("updated_at DESC").
		First(p).Error
	if err != nil {
		return nil, err
	}
	return p, nil
}

// SearchByOwner returns products whose name OR slug OR sku
// LIKE-matches the query, scoped to uid, newest first. Used
// by the agent-facing `lookup_asset` tool. Empty query → nil
// (safer than "match everything"). SKU is added to the search
// columns vs Brand's name+slug because the merchant-side
// identifier is the most common "find my product" handle.
func (r *Repository) SearchByOwner(uid uint, query string, limit int) ([]model.Product, error) {
	if uid == 0 {
		return nil, nil
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	pattern := "%" + trimmed + "%"
	var rows []model.Product
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ? OR sku LIKE ?)",
			uid, pattern, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search products: %w", err)
	}
	return rows, nil
}

// SearchByOwnerProject is SearchByOwner scoped to a project.
// projectID=0 falls through to the uid-only SearchByOwner.
func (r *Repository) SearchByOwnerProject(uid, projectID uint, query string, limit int) ([]model.Product, error) {
	if projectID == 0 {
		return r.SearchByOwner(uid, query, limit)
	}
	if uid == 0 {
		return nil, nil
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	pattern := "%" + trimmed + "%"
	var rows []model.Product
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ? OR sku LIKE ?)",
			uid, projectID, pattern, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search products by project: %w", err)
	}
	return rows, nil
}

// ListByOwnerProject is ListForOwner scoped to a project.
// projectID=0 falls through to the uid-only variant.
func (r *Repository) ListByOwnerProject(uid, projectID uint, limit, offset int) ([]model.Product, error) {
	if projectID == 0 {
		return r.ListForOwner(uid, limit, offset)
	}
	if uid == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.Product
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL", uid, projectID).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list products by project: %w", err)
	}
	return rows, nil
}

// ListForOwner returns the user's product library, newest first.
// Soft-deleted rows excluded.
func (r *Repository) ListForOwner(uid uint, limit, offset int) ([]model.Product, error) {
	if uid == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.Product
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	return rows, nil
}

// MarkConfirmed flips Confirmed=false → true and stamps
// ConfirmedAt. Idempotent: already-confirmed rows return
// ErrRecordNotFound so callers can disambiguate "no-op" from
// "row missing" via a LoadByID after the call.
func (r *Repository) MarkConfirmed(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now()
	tx := r.db.Model(&model.Product{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL AND confirmed = ?", id, uid, false).
		Updates(map[string]interface{}{
			"confirmed":    true,
			"confirmed_at": &now,
		})
	if tx.Error != nil {
		return fmt.Errorf("mark confirmed: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Restore reverses SoftDelete: clears deleted_at + resets
// Confirmed=false (re-vocalize required to come back active).
func (r *Repository) Restore(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	tx := r.db.Model(&model.Product{}).
		Unscoped().
		Where("id = ? AND uid = ? AND deleted_at IS NOT NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at":   nil,
			"confirmed":    false,
			"confirmed_at": nil,
		})
	if tx.Error != nil {
		return fmt.Errorf("restore product: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete stamps deleted_at = now. Library reads filter on
// deleted_at IS NULL.
func (r *Repository) SoftDelete(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now()
	tx := r.db.Model(&model.Product{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at": &now,
		})
	if tx.Error != nil {
		return fmt.Errorf("soft delete product: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
