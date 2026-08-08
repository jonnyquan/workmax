// Package character owns the platform-level w_global_character +
// w_global_character_reference CRUD layer (Sprint-E 3b/8). Until now,
// model.Character was consumed via raw GORM in api/.../character_api.go,
// canvas/mention_resolver, asset_injector, and tts/voices. With
// the workagent character descriptor folding into the platform
// table, a shared repo gives all consumers one place to keep IDOR
// scoping + lifecycle filters consistent.
//
// Surface mirrors service/brand and service/director_style. Lifecycle
// reconciliation (workagent's draft/confirmed/archived enum →
// platform's flat Status int8 + Confirmed bool + DeletedAt) is
// baked into the four lifecycle methods so callers don't have to
// think about the mapping.
package character

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// Repository wraps w_global_character DB ops.
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

// Create inserts a new character row. Required fields (UID + Name)
// are enforced server-side; partial M4 extractions ride through
// with empty optional fields. Select("*") forces explicit
// Confirmed=false from workagent draft INSERTs to survive the DDL
// DEFAULT 1 (zero-value-skip workaround).
func (r *Repository) Create(c *model.Character) error {
	if c == nil {
		return errors.New("nil character")
	}
	if c.UID == 0 {
		return errors.New("character must have non-zero uid")
	}
	if c.Name == "" {
		return errors.New("character must have a name")
	}
	if c.SourceKind == "" {
		c.SourceKind = model.CharacterSourceManual
	}
	if c.Status == 0 {
		c.Status = model.CharacterStatusActive
	}
	if c.RoleType == "" {
		c.RoleType = model.CharacterRoleSupporting
	}
	if c.Lang == "" {
		c.Lang = "en"
	}
	if err := r.db.Select("*").Create(c).Error; err != nil {
		return fmt.Errorf("create character: %w", err)
	}
	return nil
}

// LoadByIDForOwner — uid-scoped read. Returns ErrRecordNotFound for
// missing OR cross-tenant.
func (r *Repository) LoadByIDForOwner(id, uid uint) (*model.Character, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	c := &model.Character{}
	q := r.db.Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid)
	if err := q.First(c).Error; err != nil {
		return nil, err
	}
	return c, nil
}

// FindLatestActiveForOwner — confirmed + not-deleted, latest first.
// "Active" means Confirmed=true && Status=Active && DeletedAt IS NULL.
func (r *Repository) FindLatestActiveForOwner(uid uint) (*model.Character, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	c := &model.Character{}
	err := r.db.
		Where("uid = ? AND confirmed = ? AND status = ? AND deleted_at IS NULL",
			uid, true, model.CharacterStatusActive).
		Order("updated_at DESC").
		First(c).Error
	if err != nil {
		return nil, err
	}
	return c, nil
}

// FindLatestForOwner — any-confirmation-state, not-deleted, latest
// first. Backs the M4 watermark path.
func (r *Repository) FindLatestForOwner(uid uint) (*model.Character, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	c := &model.Character{}
	err := r.db.
		Where("uid = ? AND status = ? AND deleted_at IS NULL", uid, model.CharacterStatusActive).
		Order("updated_at DESC").
		First(c).Error
	if err != nil {
		return nil, err
	}
	return c, nil
}

// SearchByOwner returns characters whose name or slug LIKE-matches
// the query, scoped to uid, newest first. Used by the agent-facing
// `lookup_asset` tool — see brand.Repository.SearchByOwner for the
// shared contract notes (empty query → empty result, %wrapping%,
// case-insensitive via MySQL collation).
func (r *Repository) SearchByOwner(uid uint, query string, limit int) ([]model.Character, error) {
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
	var rows []model.Character
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ?)", uid, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search characters: %w", err)
	}
	return rows, nil
}

// SearchByOwnerProject is SearchByOwner scoped to a project.
// See brand.Repository.SearchByOwnerProject for the contract
// (projectID=0 falls through to uid-only Search).
func (r *Repository) SearchByOwnerProject(uid, projectID uint, query string, limit int) ([]model.Character, error) {
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
	var rows []model.Character
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL AND (name LIKE ? OR slug LIKE ?)",
			uid, projectID, pattern, pattern).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("search characters by project: %w", err)
	}
	return rows, nil
}

// ListByOwnerProject is ListForOwner scoped to a project. See
// brand.Repository.ListByOwnerProject for the contract.
func (r *Repository) ListByOwnerProject(uid, projectID uint, limit, offset int) ([]model.Character, error) {
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
	var rows []model.Character
	err := r.db.
		Where("uid = ? AND project_id = ? AND deleted_at IS NULL", uid, projectID).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list characters by project: %w", err)
	}
	return rows, nil
}

// ListForOwner — paginated newest-first list. Soft-deleted excluded.
func (r *Repository) ListForOwner(uid uint, limit, offset int) ([]model.Character, error) {
	if uid == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.Character
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list characters: %w", err)
	}
	return rows, nil
}

// MarkConfirmed flips Confirmed=false → true and stamps ConfirmedAt.
// Idempotent — already-confirmed rows return ErrRecordNotFound.
func (r *Repository) MarkConfirmed(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	now := time.Now()
	tx := r.db.Model(&model.Character{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL AND confirmed = ?", id, uid, false).
		Updates(map[string]interface{}{
			"confirmed":    true,
			"confirmed_at": &now,
		})
	if tx.Error != nil {
		return fmt.Errorf("mark character confirmed: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// Restore reverses SoftDelete: clears deleted_at and resets
// Confirmed=false so the user re-vocalizes via MarkConfirmed.
func (r *Repository) Restore(id, uid uint) error {
	if uid == 0 {
		return gorm.ErrRecordNotFound
	}
	tx := r.db.Model(&model.Character{}).
		Unscoped().
		Where("id = ? AND uid = ? AND deleted_at IS NOT NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at":   nil,
			"confirmed":    false,
			"confirmed_at": nil,
		})
	if tx.Error != nil {
		return fmt.Errorf("restore character: %w", tx.Error)
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
	tx := r.db.Model(&model.Character{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Updates(map[string]interface{}{
			"deleted_at": &now,
		})
	if tx.Error != nil {
		return fmt.Errorf("soft delete character: %w", tx.Error)
	}
	if tx.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
