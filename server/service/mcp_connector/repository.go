// Package mcp_connector owns the per-user MCP connector
// registry (Phase B1 of the MCP-connector quarter). Each row is
// one external MCP server the agent should expose alongside the
// platform's in-process production-tools server.
//
// Phase B1 ships the CRUD foundation: Create / LoadByID /
// ListForOwner / Update / SoftDelete / SetEnabled. Phase B2 will
// build BuildExternalMcpServers(uid) on top of List + Transport
// mapping. Phase B3 wraps env / headers with at-rest encryption.
// Phase B4 ships the user-facing UI for connector management.
//
// IDOR posture matches the asset repos: cross-tenant fetches
// return gorm.ErrRecordNotFound (single sentinel). uid=0 and
// id=0 short-circuit before the DB so a system-path caller that
// lost its context can't surface a row.
package mcp_connector

import (
	"errors"
	"fmt"
	"strings"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
)

// ErrValidation is the sentinel every repo-side validation error
// wraps. Handler code distinguishes "this is a 4xx-shaped user
// input problem" from "this is a 5xx-shaped DB / system failure"
// via errors.Is(err, mcp_connector.ErrValidation). The previous
// shape did string-prefix matching against a hand-maintained list
// of known message prefixes — fragile across rephrases and easy
// to forget when a new validation lands. Wrapping every
// errors.New here under this sentinel makes the discriminator
// type-safe and the list-of-prefixes go away.
//
// The wrapped Error() string preserves the original message
// verbatim — existing tests that compare against
// "transport is required" / "stdio transport requires command"
// stay unchanged. errors.Is uses the unexported validationError
// type's Is method, not message comparison.
var ErrValidation = errors.New("mcp_connector: validation")

// validationError is the concrete type returned by every repo
// validation. Its Error() returns the human-readable reason
// (preserves the pre-sentinel message contract); its Is method
// matches ErrValidation so handlers can branch via errors.Is
// without enumerating individual reasons.
//
// Defined unexported because callers should match on ErrValidation
// (the umbrella) rather than picking apart per-reason types — the
// repo owns the validation taxonomy and adding a new reason
// shouldn't ripple to every handler.
type validationError struct{ reason string }

func (e *validationError) Error() string                 { return e.reason }
func (e *validationError) Is(target error) bool          { return target == ErrValidation }
func newValidationError(reason string) *validationError { return &validationError{reason: reason} }

// Repository wraps the w_global_mcp_connector table. Stateless
// apart from the *gorm.DB binding so tests can inject NewTestDB.
type Repository struct {
	db *gorm.DB
}

// NewRepository binds the repo to a *gorm.DB.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Default returns a repo bound to the system DB.
func Default() *Repository {
	return NewRepository(globals.GraDBs["system"])
}

// Create inserts a new connector row. Required fields:
//   - UID (must be > 0)
//   - Name (non-empty after trim)
//   - Transport (one of MCPTransportStdio/SSE/HTTP)
//
// For Transport == "stdio", Command must be non-empty.
// For Transport in {"sse", "http"}, URL must be non-empty.
//
// Enabled defaults to true if zero-valued (Go bool zero is
// false; we explicitly set true so a user that omits the flag
// gets the connector active). Tests that need a draft / disabled
// row set Enabled=false before Create.
func (r *Repository) Create(c *model.MCPConnector) error {
	if c == nil {
		return errors.New("nil connector")
	}
	if c.UID == 0 {
		return newValidationError("connector must have non-zero uid")
	}
	if strings.TrimSpace(c.Name) == "" {
		return newValidationError("connector must have a name")
	}
	if err := validateTransport(c); err != nil {
		return err
	}
	// Select("*") forces GORM to write the Enabled=false case
	// despite Go's bool zero-value skip — same pattern the
	// brand repo uses for Confirmed.
	if err := r.db.Select("*").Create(c).Error; err != nil {
		return fmt.Errorf("create mcp connector: %w", err)
	}
	return nil
}

// LoadByIDForOwner reads one connector by id, scoped to uid.
// Cross-tenant / missing / soft-deleted all return
// gorm.ErrRecordNotFound.
func (r *Repository) LoadByIDForOwner(id, uid uint) (*model.MCPConnector, error) {
	if uid == 0 || id == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	c := &model.MCPConnector{}
	err := r.db.
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		First(c).Error
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ListForOwner returns the user's connectors newest-first.
// Soft-deleted excluded. Caller filters by Enabled for the SDK
// hot path (see Phase B2's ListEnabledForOwner).
func (r *Repository) ListForOwner(uid uint, limit, offset int) ([]model.MCPConnector, error) {
	if uid == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.MCPConnector
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list mcp connectors: %w", err)
	}
	return rows, nil
}

// ListEnabledForOwner is the Phase B2 hot-path read. Returns
// every enabled, non-deleted connector for uid, newest-first.
// No limit / offset — the agent loads ALL enabled connectors on
// every turn; cardinality is bounded by what the user opted
// into (typically < 20).
func (r *Repository) ListEnabledForOwner(uid uint) ([]model.MCPConnector, error) {
	if uid == 0 {
		return nil, nil
	}
	var rows []model.MCPConnector
	err := r.db.
		Where("uid = ? AND enabled = ? AND deleted_at IS NULL", uid, true).
		Order("updated_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list enabled mcp connectors: %w", err)
	}
	return rows, nil
}

// Update applies field updates to a connector. Only non-nil
// fields on the patch are written. transport / command / url
// re-trigger validation so an update can't put the row into an
// inconsistent state.
type UpdatePatch struct {
	Name      *string
	Transport *string
	Command   *string
	Args      *model.JSONMap
	Env       *model.EncryptedJSONMap
	URL       *string
	Headers   *model.EncryptedJSONMap
	Enabled   *bool
}

func (r *Repository) Update(id, uid uint, patch UpdatePatch) error {
	if uid == 0 || id == 0 {
		return gorm.ErrRecordNotFound
	}
	// Load the current row so we can re-run validateTransport
	// against the merged shape, not the partial patch.
	current, err := r.LoadByIDForOwner(id, uid)
	if err != nil {
		return err
	}

	updates := map[string]interface{}{}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return newValidationError("connector name cannot be empty")
		}
		updates["name"] = name
		current.Name = name
	}
	if patch.Transport != nil {
		updates["transport"] = *patch.Transport
		current.Transport = *patch.Transport
	}
	if patch.Command != nil {
		updates["command"] = *patch.Command
		current.Command = *patch.Command
	}
	if patch.Args != nil {
		updates["args"] = *patch.Args
		current.Args = *patch.Args
	}
	if patch.Env != nil {
		updates["env"] = *patch.Env
		current.Env = *patch.Env
	}
	if patch.URL != nil {
		updates["url"] = *patch.URL
		current.URL = *patch.URL
	}
	if patch.Headers != nil {
		updates["headers"] = *patch.Headers
		current.Headers = *patch.Headers
	}
	if patch.Enabled != nil {
		updates["enabled"] = *patch.Enabled
		current.Enabled = *patch.Enabled
	}

	// Re-validate after applying the patch so transport / url /
	// command stay consistent. Skip when nothing material changed
	// (avoids re-rejecting a name-only edit on a pre-existing
	// invalid row).
	if patch.Transport != nil || patch.Command != nil || patch.URL != nil {
		if err := validateTransport(current); err != nil {
			return err
		}
	}

	if len(updates) == 0 {
		return nil
	}
	return r.db.
		Model(&model.MCPConnector{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Updates(updates).Error
}

// SetEnabled is the one-bit toggle for the per-uid kill switch.
// Cheaper than a full Update for the common "disable / re-enable"
// flow.
func (r *Repository) SetEnabled(id, uid uint, enabled bool) error {
	if uid == 0 || id == 0 {
		return gorm.ErrRecordNotFound
	}
	res := r.db.
		Model(&model.MCPConnector{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Update("enabled", enabled)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SoftDelete marks the row deleted. IDOR-scoped — cross-tenant
// or missing row returns gorm.ErrRecordNotFound rather than
// 200-no-op so the caller can surface a real 404 to the user.
func (r *Repository) SoftDelete(id, uid uint) error {
	if uid == 0 || id == 0 {
		return gorm.ErrRecordNotFound
	}
	res := r.db.
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Delete(&model.MCPConnector{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// validateTransport enforces the per-transport required fields.
// Called by Create + Update so the same shape holds across both
// write paths.
func validateTransport(c *model.MCPConnector) error {
	switch c.Transport {
	case model.MCPTransportStdio:
		if strings.TrimSpace(c.Command) == "" {
			return newValidationError("stdio transport requires command")
		}
	case model.MCPTransportSSE, model.MCPTransportHTTP:
		if strings.TrimSpace(c.URL) == "" {
			return newValidationError(c.Transport + " transport requires url")
		}
	case "":
		return newValidationError("transport is required")
	default:
		return newValidationError("unknown transport: " + c.Transport)
	}
	return nil
}
