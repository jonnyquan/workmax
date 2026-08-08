// Package project owns the platform-level Project read path —
// the underlying table is `w_global_project` (renamed from
// w_canvas_project in Plan-A Phase A4). Canvas surface still
// owns the document / snapshot / share write CRUD; this package
// gives workagent + future surfaces a clean, scoped read API so
// they can:
//
//   - Resolve a project_id → name / uuid / visibility
//   - List a user's projects for the agent rail / picker
//   - Verify ownership before binding an asset to a project_id
//
// Why not reach into service/tools/canvas: that package owns
// writes + the full document patch surface; workagent reading
// brand.project_id doesn't need (or want) that surface. The thin
// repo here lets the two services evolve independently.
//
// Reads stay scoped to (id, uid) — same IDOR posture the asset
// repos use. Cross-tenant fetches return gorm.ErrRecordNotFound
// (single sentinel, no existence-oracle leak).
package project

import (
	"errors"
	"fmt"

	"server/globals"
	"server/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Repository wraps the w_global_project table for non-canvas
// callers. Stateless apart from the *gorm.DB binding so tests
// can inject NewTestDB.
type Repository struct {
	db *gorm.DB
}

// NewRepository binds the repo to a *gorm.DB. Production callers
// prefer Default(); tests inject their own DB via NewTestDB.
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// Default returns a repo bound to the system DB. Convenience for
// handler / agent code that doesn't otherwise plumb a *gorm.DB.
func Default() *Repository {
	return NewRepository(globals.GraDBs["system"])
}

// LoadByIDForOwner reads one project by id, scoped to uid. Used
// when an asset's project_id resolves to a name / uuid for
// preflight injection or the agent's project-picker tool.
//
// Returns gorm.ErrRecordNotFound for both "row missing" and "row
// exists under a different uid" — matches the asset repos'
// single-sentinel IDOR contract. uid=0 short-circuits the same
// way so a system-path caller that loses its uid context can't
// accidentally surface a random project.
func (r *Repository) LoadByIDForOwner(id, uid uint) (*model.CanvasProject, error) {
	if uid == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if id == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	p := &model.CanvasProject{}
	err := r.db.
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		First(p).Error
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ProjectAccess describes the effective access a user has to a project.
// During the membership migration owner access is recognized from either
// w_global_project.uid or w_global_project_member(role=owner).
type ProjectAccess struct {
	Project *model.CanvasProject
	Role    string
}

func (a ProjectAccess) CanView() bool {
	switch a.Role {
	case model.GlobalProjectRoleOwner,
		model.GlobalProjectRoleEditor,
		model.GlobalProjectRoleViewer,
		model.GlobalProjectRoleCommenter:
		return true
	default:
		return false
	}
}

func (a ProjectAccess) CanEdit() bool {
	return a.Role == model.GlobalProjectRoleOwner || a.Role == model.GlobalProjectRoleEditor
}

func (a ProjectAccess) CanManage() bool {
	return a.Role == model.GlobalProjectRoleOwner
}

func (r *Repository) ResolveAccess(projectID, uid uint) (*ProjectAccess, error) {
	if uid == 0 || projectID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var project model.CanvasProject
	if err := r.db.
		Where("id = ? AND deleted_at IS NULL", projectID).
		First(&project).Error; err != nil {
		return nil, err
	}
	if project.UID == int(uid) {
		return &ProjectAccess{Project: &project, Role: model.GlobalProjectRoleOwner}, nil
	}

	var member model.GlobalProjectMember
	if err := r.db.
		Where("project_id = ? AND uid = ? AND deleted_at IS NULL", projectID, uid).
		First(&member).Error; err != nil {
		return nil, err
	}
	return &ProjectAccess{Project: &project, Role: normalizeProjectMemberRole(member.Role)}, nil
}

func (r *Repository) CanViewProject(projectID, uid uint) (bool, error) {
	access, err := r.ResolveAccess(projectID, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return access.CanView(), nil
}

func (r *Repository) CanEditProject(projectID, uid uint) (bool, error) {
	access, err := r.ResolveAccess(projectID, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return access.CanEdit(), nil
}

func (r *Repository) CanManageProject(projectID, uid uint) (bool, error) {
	access, err := r.ResolveAccess(projectID, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return access.CanManage(), nil
}

func (r *Repository) UpsertOwnerMember(projectID uint, uid int) error {
	if projectID == 0 || uid == 0 {
		return gorm.ErrRecordNotFound
	}
	row := model.GlobalProjectMember{
		ProjectID: projectID,
		UID:       uid,
		Role:      model.GlobalProjectRoleOwner,
		Source:    model.GlobalProjectMemberSourceOwner,
		CreatedBy: uid,
	}
	return r.db.
		Where("project_id = ? AND uid = ?", projectID, uid).
		Assign(map[string]interface{}{
			"role":       row.Role,
			"source":     row.Source,
			"created_by": row.CreatedBy,
			"deleted_at": nil,
		}).
		FirstOrCreate(&row).Error
}

func normalizeProjectMemberRole(role string) string {
	switch role {
	case model.GlobalProjectRoleOwner,
		model.GlobalProjectRoleEditor,
		model.GlobalProjectRoleViewer,
		model.GlobalProjectRoleCommenter:
		return role
	default:
		return ""
	}
}

// LoadByUUIDForOwner is the UUID-keyed sibling. Used by the
// workagent thread bootstrap when a request carries a project
// UUID (URL shape: ?project=<uuid>) and needs to verify
// ownership before persisting the project_id onto the thread.
func (r *Repository) LoadByUUIDForOwner(uuid string, uid uint) (*model.CanvasProject, error) {
	if uid == 0 || uuid == "" {
		return nil, gorm.ErrRecordNotFound
	}
	p := &model.CanvasProject{}
	err := r.db.
		Where("uuid = ? AND uid = ? AND deleted_at IS NULL", uuid, uid).
		First(p).Error
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListForOwner returns the user's projects newest-first, paginated.
// Mirrors the brand / character / director_style ListForOwner
// surface so the workagent project-picker can call into all
// four kinds uniformly. Soft-deleted rows are excluded.
func (r *Repository) ListForOwner(uid uint, limit, offset int) ([]model.CanvasProject, error) {
	if uid == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var rows []model.CanvasProject
	err := r.db.
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("updated_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return rows, nil
}

// ErrBudgetExceeded is the sentinel AddBudgetSpent returns when
// the conditional UPDATE would push the project past its cap.
// Handler code branches on errors.Is — matches the
// mcp_connector.ErrValidation / workagent.ErrInvalidRating
// pattern so the codebase has one idiom, not three.
var ErrBudgetExceeded = errors.New("project: budget cap exceeded")

// ErrBudgetRefundInvariant means a reservation tried to refund more project
// budget than is still charged to the project. Treating that case as a no-op
// would let the matching CreditsPack refund commit while the project ledger
// stays unchanged, so reservation settlement must remain retryable instead.
var ErrBudgetRefundInvariant = errors.New("project: budget refund invariant failed")

// BudgetStatus is the projection the budget API returns. Cap is
// *int (nil = no cap). Remaining is -1 when uncapped (caller
// renders "unlimited" rather than a number). Exceeded is true
// when Used > Cap — only meaningful with a non-nil Cap.
type BudgetStatus struct {
	Cap       *int
	Used      int
	Remaining int
	Exceeded  bool
}

// GetBudgetStatusForOwner reads the cap + used pair, scoped to
// uid. Used by the budget-detail endpoint + the reservation
// pre-flight check. Cross-tenant collapses to ErrRecordNotFound.
func (r *Repository) GetBudgetStatusForOwner(id, uid uint) (*BudgetStatus, error) {
	p, err := r.LoadByIDForOwner(id, uid)
	if err != nil {
		return nil, err
	}
	status := &BudgetStatus{
		Cap:       p.BudgetCreditsCap,
		Used:      p.BudgetCreditsUsed,
		Remaining: -1, // uncapped sentinel
	}
	if p.BudgetCreditsCap != nil {
		status.Remaining = *p.BudgetCreditsCap - p.BudgetCreditsUsed
		status.Exceeded = p.BudgetCreditsUsed > *p.BudgetCreditsCap
	}
	return status, nil
}

// SetBudgetCapForOwner writes the cap. Pass cap=nil to clear
// (unlimited). Owner-scoped via uid in WHERE. Negative caps are
// rejected — they're nonsensical and likely indicate a frontend
// bug.
//
// Doesn't touch BudgetCreditsUsed: changing the cap mid-flight
// is a policy adjustment, not a settlement. If the new cap is
// below current Used, the project's status becomes Exceeded and
// subsequent reservation attempts reject — same defence the cap
// enforces all the time, just expressed via the cap rather than
// the spend side.
func (r *Repository) SetBudgetCapForOwner(id, uid uint, cap *int) error {
	if uid == 0 || id == 0 {
		return gorm.ErrRecordNotFound
	}
	if cap != nil && *cap < 0 {
		return fmt.Errorf("project budget cap must be >= 0 (got %d)", *cap)
	}
	// GORM's Update("col", typedNilPointer) is unreliable across
	// engines — sometimes it writes NULL, sometimes it skips the
	// field. Explicit map with a true `nil` value or a real int
	// makes the SQL deterministic: "budget_credits_cap" = NULL
	// vs "budget_credits_cap" = ?.
	var capValue interface{} // nil ⇒ SQL NULL
	if cap != nil {
		capValue = *cap
	}
	res := r.db.Model(&model.CanvasProject{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Updates(map[string]interface{}{"budget_credits_cap": capValue})
	if res.Error != nil {
		return fmt.Errorf("set budget cap: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// AddBudgetSpent atomically increments BudgetCreditsUsed,
// gated on the cap. Caller passes the credits being charged
// (positive int); the conditional UPDATE only fires when adding
// `credits` would not exceed the cap. Returns ErrBudgetExceeded
// when the gate blocks — caller surfaces that as a 402 / refuse
// payment to the reservation service.
//
// Atomicity: the WHERE includes the cap check so two parallel
// settles can't both pass a Go-side `used + credits <= cap`
// check then both increment past the cap. RowsAffected==0 + a
// follow-up read distinguishes "cap exceeded" from "row
// missing" — the post-check is one extra round-trip but the
// caller already knows the project exists (bind-time check
// upstream).
//
// uid scope: same uid in WHERE as the other writers. Cross-
// tenant attempts collapse to ErrRecordNotFound.
//
// Negative credits ARE allowed — slice 2 uses negative values
// to decrement on reservation release. The cap check still
// holds (decrementing always succeeds because used - n <= cap
// when used <= cap). Capped to >= 0 to avoid negative-used
// drift if a release double-fires.
func (r *Repository) AddBudgetSpent(id, uid uint, credits int) error {
	if uid == 0 || id == 0 {
		return gorm.ErrRecordNotFound
	}
	if credits == 0 {
		return nil // nothing to do
	}

	// Negative credits (refund) — simple atomic decrement with a
	// floor at 0 so a double-release can't drive used negative.
	if credits < 0 {
		res := r.db.Model(&model.CanvasProject{}).
			Where("id = ? AND uid = ? AND deleted_at IS NULL AND budget_credits_used + ? >= 0",
				id, uid, credits).
			UpdateColumn("budget_credits_used",
				gorm.Expr("budget_credits_used + ?", credits))
		if res.Error != nil {
			return fmt.Errorf("decrement budget: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// Either the row is gone OR the decrement would drive
			// used below 0. Both cases collapse — the caller doesn't
			// need to distinguish; double-refunds are idempotent
			// no-ops.
			return nil
		}
		return nil
	}

	// Positive credits (charge) — conditional on cap.
	// NULL cap (uncapped) always allows the charge; a numeric
	// cap requires used + credits <= cap. SQL handles both via
	// the IS NULL / + check in the WHERE.
	res := r.db.Model(&model.CanvasProject{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL AND (budget_credits_cap IS NULL OR budget_credits_used + ? <= budget_credits_cap)",
			id, uid, credits).
		UpdateColumn("budget_credits_used",
			gorm.Expr("budget_credits_used + ?", credits))
	if res.Error != nil {
		return fmt.Errorf("increment budget: %w", res.Error)
	}
	if res.RowsAffected == 1 {
		return nil
	}
	// RowsAffected = 0 means either:
	//   (a) the row vanished / cross-tenant (ErrRecordNotFound)
	//   (b) the cap check failed (ErrBudgetExceeded)
	// Disambiguate via a follow-up read. Two queries on the
	// failure path is fine because this is the slow-path "user
	// hit their budget" case, not the every-turn happy path.
	var exists int64
	if err := r.db.Model(&model.CanvasProject{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Count(&exists).Error; err != nil {
		return fmt.Errorf("budget disambiguate: %w", err)
	}
	if exists == 0 {
		return gorm.ErrRecordNotFound
	}
	return ErrBudgetExceeded
}

// RefundBudgetSpentExact returns a reservation-owned charge to a project.
// Unlike the legacy negative AddBudgetSpent path, it fails closed when the
// project is missing or its running total is smaller than the requested
// refund. The project row is locked before CreditsPack rows are touched so all
// reservation settlement paths share the canonical Project -> Pack order.
func (r *Repository) RefundBudgetSpentExact(id, uid uint, credits int) error {
	if id == 0 || uid == 0 {
		return gorm.ErrRecordNotFound
	}
	if credits < 0 {
		return fmt.Errorf("refund credits must be non-negative")
	}
	if credits == 0 {
		return nil
	}

	var locked model.CanvasProject
	if err := r.db.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id, uid, budget_credits_used").
		Where("id = ? AND uid = ?", id, uid).
		First(&locked).Error; err != nil {
		return err
	}
	if locked.BudgetCreditsUsed < credits {
		return ErrBudgetRefundInvariant
	}

	res := r.db.Unscoped().Model(&model.CanvasProject{}).
		Where("id = ? AND uid = ? AND budget_credits_used >= ?", id, uid, credits).
		UpdateColumn("budget_credits_used", gorm.Expr("budget_credits_used - ?", credits))
	if res.Error != nil {
		return fmt.Errorf("refund project budget: %w", res.Error)
	}
	if res.RowsAffected != 1 {
		return ErrBudgetRefundInvariant
	}
	return nil
}

// ExistsForOwner is a cheap predicate for the bind-time validation
// path: workagent verifies the user owns the project before
// stamping project_id onto a thread / asset, without needing the
// full row. Implemented via Pluck so we only fetch one int —
// less wire than a full row read.
//
// Cross-tenant / nonexistent / soft-deleted all return false. The
// caller surfaces that as 404 / "project not found" rather than
// 403, matching the IDOR posture.
func (r *Repository) ExistsForOwner(id, uid uint) (bool, error) {
	if id == 0 || uid == 0 {
		return false, nil
	}
	var count int64
	err := r.db.
		Model(&model.CanvasProject{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		Count(&count).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("project exists check: %w", err)
	}
	return count > 0, nil
}
