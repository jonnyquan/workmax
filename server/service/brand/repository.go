// Package brand owns the platform-level w_brand + w_brand_reference
// CRUD layer (Sprint-E 3/N). The workagent module previously kept
// its own w_workagent_brand_asset table + repo; promoting brand to
// a first-class platform asset means canvas tools, video-ad surfaces,
// and any future generator can reach brand identity without going
// through the workagent module.
//
// Surface mirrors the existing workagent BrandAssetRepository (and
// the sibling character / director-style repos): every uid'd read
// puts uid in the WHERE clause via a *ForOwner method, so a
// forgotten ownership check at a call site can't serve another
// user's brand.
//
// Lifecycle reconciliation: workagent's Status enum (draft /
// confirmed / archived) maps onto the platform's flat Status int8
// (1=active) + Confirmed bool + DeletedAt soft-delete. The four
// methods that operate on lifecycle transitions (MarkConfirmed,
// SoftDelete, Restore, FindLatestActiveForOwner) bake the mapping
// in so callers don't have to think about it.
package brand

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// Repository wraps the w_brand DB ops. Stateless apart from the
// *gorm.DB binding.
type Repository struct {
	db *gorm.DB
}

// NewRepository binds the repo to a *gorm.DB. Production callers
// prefer Default(); tests inject their own DB via NewTestDB.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Default returns a repo bound to the system DB. Convenience for
// handler code that doesn't otherwise plumb a *gorm.DB.
func Default() *Repository {
	return NewRepository(globals.GraDBs["system"])
}

// Create inserts a new brand row. Required fields (UID + Name) are
// enforced server-side; partial M4 extractions (no typography yet,
// etc.) ride through with the empty JSON sections defaulted at the
// DB layer.
func (r *Repository) Create(b *model.Brand) error {
	if b == nil {
		return errors.New("nil brand")
	}
	if b.UID == 0 {
		return errors.New("brand must have non-zero uid")
	}
	if b.Name == "" {
		return errors.New("brand must have a name")
	}
	if b.SourceKind == "" {
		b.SourceKind = model.BrandSourceManual
	}
	if b.Status == 0 {
		b.Status = model.BrandStatusActive
	}
	if b.Lang == "" {
		b.Lang = "en"
	}
	// Select("*") forces GORM to write zero-value bool fields
	// (notably Confirmed=false for workagent draft extractions).
	// Without this, the DDL DEFAULT 1 on the confirmed column
	// would override an explicit `false`, producing rows that
	// pass the FindLatestActiveForOwner filter when they shouldn't.
	if err := r.db.Select("*").Create(b).Error; err != nil {
		return fmt.Errorf("create brand: %w", err)
	}
	return nil
}

// LoadByIDForOwner reads one brand by id only when uid matches.
// Returns gorm.ErrRecordNotFound for both "row missing" and "row
// exists under a different uid" — single error sentinel keeps the
// IDOR contract simple for upstream tests.
func (r *Repository) LoadByIDForOwner(id, uid uint) (*model.Brand, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	b := &model.Brand{}
	q := r.db.Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid)
	if err := q.First(b).Error; err != nil {
		return nil, err
	}
	return b, nil
}

// FindLatestActiveForOwner returns the user's most recently
// confirmed + non-deleted brand. "Active" here means
// Confirmed=true AND DeletedAt IS NULL — i.e. fully vetted, ready
// to inject into preflight. Drafts are excluded; the watermark
// path uses FindLatestForOwner instead.
//
// Walks updated_at DESC so a re-confirmed brand floats above an
// older first-confirmed one — matches the user mental model "the
// brand I just touched is the active one."
func (r *Repository) FindLatestActiveForOwner(uid uint) (*model.Brand, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	b := &model.Brand{}
	err := r.db.
		Where("uid = ? AND confirmed = ? AND status = ? AND deleted_at IS NULL",
			uid, true, model.BrandStatusActive).
		Order("updated_at DESC").
		First(b).Error
	if err != nil {
		return nil, err
	}
	return b, nil
}

// FindLatestForOwner returns the latest brand for uid in ANY
// confirmation state (draft + confirmed), excluding soft-deleted.
// Backs the M4 watermark path: even an unconfirmed extraction
// beats no brand at all when riding with a [待品牌方确认]
// marker.
func (r *Repository) FindLatestForOwner(uid uint) (*model.Brand, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	b := &model.Brand{}
	err := r.db.
		Where("uid = ? AND status = ? AND deleted_at IS NULL", uid, model.BrandStatusActive).
		Order("updated_at DESC").
		First(b).Error
	if err != nil {
		return nil, err
	}
	return b, nil
}

// SearchByOwner returns brands whose name or slug LIKE-matches the
// query, scoped to uid, newest first. Used by the agent-facing
// `lookup_asset` tool — when the user says "use my coffee brand"
// the agent calls this with query="coffee" and picks from the
// top hits instead of demanding an exact id / slug.
//
// Empty query returns nothing (falsy-input → empty-result is
// safer than "match everything" — that would inadvertently
// behave like ListForOwner via a stale caller).
//
// The LIKE pattern wraps with %query% so partial matches in
// either direction land — "acme" matches both "acme-corp" and
// "acme-corp-2024". Case sensitivity follows MySQL's collation
// (utf8mb4_general_ci is case-insensitive by default).
func (r *Repository) SearchByOwner(uid uint, query string, limit int) ([]model.Brand, error) {
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
	var rows []model.Brand
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ?)", uid, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search brands: %w", err)
	}
	return rows, nil
}

// SearchByOwnerProject is SearchByOwner scoped to a project. Same
// LIKE-pattern + newest-first / limit-clamp posture; adds a
// project_id filter so the agent can scope "find my coffee brand"
// to one project's library instead of the whole user-wide pile.
//
// projectID semantics:
//   - >0: include only rows whose project_id matches
//   - 0:  fall through to SearchByOwner (no project scope)
//
// Pass 0 explicitly when "any project / no project" is what the
// caller wants; this method exists for the explicitly-scoped
// case. Phase A3 will plumb projectID through the
// production-tool input so the agent decides per-call.
func (r *Repository) SearchByOwnerProject(uid, projectID uint, query string, limit int) ([]model.Brand, error) {
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
	var rows []model.Brand
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ?)",
			uid, projectID, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search brands by project: %w", err)
	}
	return rows, nil
}

// ListByOwnerProject is ListForOwner scoped to a project. Same
// posture as SearchByOwnerProject: projectID=0 falls through to
// the uid-only ListForOwner.
func (r *Repository) ListByOwnerProject(uid, projectID uint, limit, offset int) ([]model.Brand, error) {
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
	var rows []model.Brand
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL", uid, projectID).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list brands by project: %w", err)
	}
	return rows, nil
}

// ListForOwner returns the user's brand library, newest first.
// Soft-deleted rows are excluded; callers that need an "include
// archived" view can either join via deleted_at IS NOT NULL or
// add a separate ListArchivedForOwner method later.
func (r *Repository) ListForOwner(uid uint, limit, offset int) ([]model.Brand, error) {
	if uid == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.Brand
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list brands: %w", err)
	}
	return rows, nil
}

// MarkConfirmed flips Confirmed=false → true and stamps ConfirmedAt
// (M4 Vocalize step). Idempotent: already-confirmed rows return
// gorm.ErrRecordNotFound to disambiguate "no-op" from "row missing"
// — callers that want to surface idempotent success do a LoadByID
// after, matching the pattern in api/.../asset_library_api.go's
// handleConfirmAsset.
func (r *Repository) MarkConfirmed(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now()
	tx := r.db.Model(&model.Brand{}).
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

// Restore reverses a SoftDelete: clears deleted_at and resets
// Confirmed=false so the user re-vocalizes via MarkConfirmed if
// they want the brand active in preflight again. Mirrors the
// workagent archived → draft transition.
func (r *Repository) Restore(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	tx := r.db.Model(&model.Brand{}).
		Unscoped().
		Where("id = ? AND uid = ? AND deleted_at IS NOT NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at":   nil,
			"confirmed":    false,
			"confirmed_at": nil,
		})
	if tx.Error != nil {
		return fmt.Errorf("restore brand: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete sets deleted_at = now. Library reads filter on
// deleted_at IS NULL; the row is preserved for audit / undo.
func (r *Repository) SoftDelete(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now()
	tx := r.db.Model(&model.Brand{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at": &now,
		})
	if tx.Error != nil {
		return fmt.Errorf("soft delete brand: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
