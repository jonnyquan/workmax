// Package director_style owns the platform-level w_director_style +
// w_director_style_reference CRUD layer (Sprint-E 3c/8). Sibling of
// service/brand and service/character; same surface shape with one
// director-specific extra: FilterByGenreForOwner backs the genre-
// filter chip row in the asset library UI.
package director_style

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// Repository wraps w_director_style DB ops.
type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Default returns a repo bound to the system DB.
func Default() *Repository {
	return NewRepository(globals.GraDBs["system"])
}

// Create inserts a new director_style row. Required fields (UID +
// Name) are enforced server-side. Select("*") forces explicit
// Confirmed=false through the GORM zero-value-skip (matching the
// brand repo's pattern; see model.Brand.Confirmed comment for
// the rationale).
func (r *Repository) Create(d *model.DirectorStyle) error {
	if d == nil {
		return errors.New("nil director_style")
	}
	if d.UID == 0 {
		return errors.New("director_style must have non-zero uid")
	}
	if d.Name == "" {
		return errors.New("director_style must have a name")
	}
	if d.SourceKind == "" {
		d.SourceKind = model.DirectorStyleSourceManual
	}
	if d.Status == 0 {
		d.Status = model.DirectorStyleStatusActive
	}
	if d.Lang == "" {
		d.Lang = "en"
	}
	if err := r.db.Select("*").Create(d).Error; err != nil {
		return fmt.Errorf("create director_style: %w", err)
	}
	return nil
}

// LoadByIDForOwner — uid-scoped read. Returns ErrRecordNotFound for
// both missing and cross-tenant.
func (r *Repository) LoadByIDForOwner(id, uid uint) (*model.DirectorStyle, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	d := &model.DirectorStyle{}
	q := r.db.Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid)
	if err := q.First(d).Error; err != nil {
		return nil, err
	}
	return d, nil
}

// FindLatestActiveForOwner — confirmed + not-deleted, latest first.
// "Active" here means Confirmed=true && DeletedAt IS NULL.
func (r *Repository) FindLatestActiveForOwner(uid uint) (*model.DirectorStyle, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	d := &model.DirectorStyle{}
	err := r.db.
		Where("uid = ? AND confirmed = ? AND status = ? AND deleted_at IS NULL",
			uid, true, model.DirectorStyleStatusActive).
		Order("updated_at DESC").
		First(d).Error
	if err != nil {
		return nil, err
	}
	return d, nil
}

// FindLatestForOwner — any-confirmation-state, not-deleted, latest
// first. Backs the M4 watermark path.
func (r *Repository) FindLatestForOwner(uid uint) (*model.DirectorStyle, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	d := &model.DirectorStyle{}
	err := r.db.
		Where("uid = ? AND status = ? AND deleted_at IS NULL", uid, model.DirectorStyleStatusActive).
		Order("updated_at DESC").
		First(d).Error
	if err != nil {
		return nil, err
	}
	return d, nil
}

// SearchByOwner returns director styles whose name or slug
// LIKE-matches the query, scoped to uid, newest first. Used by
// the agent-facing `lookup_asset` tool — see
// brand.Repository.SearchByOwner for the shared contract notes.
func (r *Repository) SearchByOwner(uid uint, query string, limit int) ([]model.DirectorStyle, error) {
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
	var rows []model.DirectorStyle
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ?)", uid, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search director_styles: %w", err)
	}
	return rows, nil
}

// SearchByOwnerProject is SearchByOwner scoped to a project.
// See brand.Repository.SearchByOwnerProject for the contract
// (projectID=0 falls through to uid-only Search).
func (r *Repository) SearchByOwnerProject(uid, projectID uint, query string, limit int) ([]model.DirectorStyle, error) {
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
	var rows []model.DirectorStyle
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ?)",
			uid, projectID, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search director_styles by project: %w", err)
	}
	return rows, nil
}

// ListByOwnerProject is ListForOwner scoped to a project. See
// brand.Repository.ListByOwnerProject for the contract.
func (r *Repository) ListByOwnerProject(uid, projectID uint, limit, offset int) ([]model.DirectorStyle, error) {
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
	var rows []model.DirectorStyle
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL", uid, projectID).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list director_styles by project: %w", err)
	}
	return rows, nil
}

// ListForOwner — paginated newest-first list. Soft-deleted excluded.
func (r *Repository) ListForOwner(uid uint, limit, offset int) ([]model.DirectorStyle, error) {
	if uid == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.DirectorStyle
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list director_styles: %w", err)
	}
	return rows, nil
}

// HasReferencesByID returns the subset of the given director-style
// IDs that have at least one non-deleted row in
// w_global_director_style_reference. Single query keyed on
// director_style_id IN (...) — avoids N+1 when the descriptor's
// Summarise method needs to populate the `hasReferences` hint for
// a list of rows.
//
// uid scope: the reference table has its own uid column for IDOR
// defence; we filter on it so cross-tenant references can't leak
// the hasReferences signal back through a list response.
//
// Returns a set (map[uint]struct{}) rather than a count map because
// the consumer only cares about presence — exposing counts would
// invite UI bloat with no current use case.
func (r *Repository) HasReferencesByID(ids []uint, uid uint) (map[uint]struct{}, error) {
	if uid == 0 || len(ids) == 0 {
		return nil, nil
	}
	type row struct {
		DirectorStyleID uint
	}
	var rows []row
	err := r.db.
		Table("w_global_director_style_reference").
		Select("DISTINCT director_style_id").
		Where("uid = ? AND deleted_at IS NULL AND director_style_id IN ?", uid, ids).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("has-references lookup: %w", err)
	}
	out := make(map[uint]struct{}, len(rows))
	for _, r := range rows {
		out[r.DirectorStyleID] = struct{}{}
	}
	return out, nil
}

// FilterByGenreForOwner hits the (genre) index for genre-scoped
// reads (e.g. "show me my noir styles"). No offset — typical genre
// subset is small; clients page client-side or fall back to
// ListForOwner if a genre has 100+ rows.
func (r *Repository) FilterByGenreForOwner(uid uint, genre string, limit int) ([]model.DirectorStyle, error) {
	if uid == 0 || genre == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var rows []model.DirectorStyle
	err := r.db.
		Where("uid = ? AND genre = ? AND deleted_at IS NULL", uid, genre).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("filter director_styles by genre: %w", err)
	}
	return rows, nil
}

// MarkConfirmed flips Confirmed=false → true and stamps ConfirmedAt.
// Idempotent: already-confirmed rows return ErrRecordNotFound (no-op
// disambiguator at the handler layer).
func (r *Repository) MarkConfirmed(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now()
	tx := r.db.Model(&model.DirectorStyle{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL AND confirmed = ?", id, uid, false).
		Updates(map[string]interface{}{
			"confirmed":    true,
			"confirmed_at": &now,
		})
	if tx.Error != nil {
		return fmt.Errorf("mark director_style confirmed: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Restore reverses SoftDelete: clears deleted_at and resets
// Confirmed=false. User re-vocalizes via MarkConfirmed.
func (r *Repository) Restore(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	tx := r.db.Model(&model.DirectorStyle{}).
		Unscoped().
		Where("id = ? AND uid = ? AND deleted_at IS NOT NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at":   nil,
			"confirmed":    false,
			"confirmed_at": nil,
		})
	if tx.Error != nil {
		return fmt.Errorf("restore director_style: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete sets deleted_at = now.
func (r *Repository) SoftDelete(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now()
	tx := r.db.Model(&model.DirectorStyle{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at": &now,
		})
	if tx.Error != nil {
		return fmt.Errorf("soft delete director_style: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
