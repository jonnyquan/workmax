package tools

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Team collaboration surface — Phase 2 minimum.
//
//	GET    /api/team                     — list teams the current user belongs to
//	POST   /api/team                     — create team (caller becomes owner)
//	GET    /api/team/:id                 — team detail + member list
//	PUT    /api/team/:id                 — update name/description (owner only)
//	DELETE /api/team/:id                 — soft-delete team (owner only)
//	POST   /api/team/:id/member          — add a member by email (owner only)
//	DELETE /api/team/:id/member/:mid     — remove a member (owner only, or self-leave)
type TeamApi struct{}

// --- DTOs ---

type createTeamRequest struct {
	Name        string `json:"name" binding:"required"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

type updateTeamRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *int8   `json:"status"`
}

// addMemberRequest carries the invite payload. The owner identifies the
// teammate by email — we resolve to the UID server-side so the UI never
// needs to know about internal user IDs. Empty email is rejected.
type addMemberRequest struct {
	Email string `json:"email" binding:"required"`
	Role  string `json:"role"`
}

// memberDTO is the wire shape for /api/team/:id GET. Mirrors model.TeamMember
// but adds the looked-up email + nickname so the UI can render friendly
// identifiers instead of bare UIDs.
type memberDTO struct {
	Id        uint      `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	TeamID    uint64    `json:"teamId"`
	UID       int       `json:"uid"`
	Role      string    `json:"role"`
	Status    int8      `json:"status"`
	Email     string    `json:"email"`
	Nickname  string    `json:"nickname"`
}

const (
	teamNameMax        = 120
	teamDescriptionMax = 500
	teamSlugMax        = 64
	teamMaxPerUser     = 50
	// Defensive cap so a runaway script can't stuff a team with thousands
	// of members. Real teams are small; this leaves headroom for orgs.
	teamMaxMembers = 200
)

var allowedTeamRoles = map[string]struct{}{
	model.TeamMemberRoleOwner:  {},
	model.TeamMemberRoleMember: {},
}

// --- Handlers ---

func (a *TeamApi) List(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	db := globals.GraDBs["system"]

	// Two-step: find active memberships → load those teams. Cheaper than
	// a JOIN when the user is in a handful of teams (the common case).
	var memberships []model.TeamMember
	if err := db.Where(
		"uid = ? AND status = ? AND deleted_at IS NULL",
		int(uid), model.TeamMemberStatusActive,
	).Find(&memberships).Error; err != nil {
		respondInternal(c, "Failed to list memberships", err)
		return
	}
	if len(memberships) == 0 {
		response.OkWithData(gin.H{"items": []model.Team{}, "total": 0}, c)
		return
	}

	teamIDs := make([]uint64, 0, len(memberships))
	for _, m := range memberships {
		teamIDs = append(teamIDs, m.TeamID)
	}

	var teams []model.Team
	if err := db.Where(
		"id IN ? AND deleted_at IS NULL AND status = ?",
		teamIDs, model.TeamStatusActive,
	).Order("id desc").Find(&teams).Error; err != nil {
		respondInternal(c, "Failed to load teams", err)
		return
	}

	response.OkWithData(gin.H{"items": teams, "total": len(teams)}, c)
}

func (a *TeamApi) Create(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	var req createTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	name := truncateRunes(strings.TrimSpace(req.Name), teamNameMax)
	if name == "" {
		response.FailWithMessage("Name is required", c)
		return
	}
	description := truncateRunes(strings.TrimSpace(req.Description), teamDescriptionMax)
	slug := normaliseTeamSlug(req.Slug, name)

	db := globals.GraDBs["system"]

	// Soft cap on teams per user — prevents runaway scripts or accidental
	// runaway workflows from creating thousands of teams.
	var owned int64
	if err := db.Model(&model.Team{}).
		Where("owner_uid = ? AND deleted_at IS NULL", int(uid)).
		Count(&owned).Error; err == nil && owned >= teamMaxPerUser {
		response.FailWithMessage(
			fmt.Sprintf("Max %d teams per user — archive some first.", teamMaxPerUser),
			c,
		)
		return
	}

	var saved model.Team
	err := db.Transaction(func(tx *gorm.DB) error {
		team := model.Team{
			OwnerUID:    int(uid),
			Name:        name,
			Slug:        slug,
			Description: description,
			Status:      model.TeamStatusActive,
		}
		if err := tx.Create(&team).Error; err != nil {
			if isDuplicateTeamSlugError(err) {
				return errors.New("slug already in use — pick another")
			}
			return err
		}
		// Auto-create the owner's membership row so team-membership
		// queries don't need to special-case the owner.
		member := model.TeamMember{
			TeamID: uint64(team.Id),
			UID:    int(uid),
			Role:   model.TeamMemberRoleOwner,
			Status: model.TeamMemberStatusActive,
		}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		saved = team
		return nil
	})
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}

	response.OkWithData(saved, c)
}

func (a *TeamApi) Get(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	id, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid team id", c)
		return
	}

	db := globals.GraDBs["system"]
	team, ok := loadTeamForMember(c, db, id, int(uid))
	if !ok {
		return
	}

	// Cap matches teamMaxMembers — a team over the cap can still fit one
	// page of detail; going beyond that would need proper pagination.
	var members []model.TeamMember
	if err := db.Where(
		"team_id = ? AND deleted_at IS NULL",
		id,
	).Order("id asc").
		Limit(teamMaxMembers).
		Find(&members).Error; err != nil {
		respondInternal(c, "Failed to load members", err)
		return
	}

	// One-shot enrich: pull email + nickname for every UID in the member
	// list so the UI can render real identities instead of "UID 26".
	memberUIDs := make([]int, 0, len(members))
	for _, m := range members {
		memberUIDs = append(memberUIDs, m.UID)
	}
	type userBrief struct {
		Id       uint   `gorm:"column:id"`
		Email    string `gorm:"column:email"`
		Nickname string `gorm:"column:nickname"`
	}
	users := make(map[int]userBrief, len(memberUIDs))
	if len(memberUIDs) > 0 {
		var briefs []userBrief
		if err := db.Table("w_user").
			Select("id, email, nickname").
			Where("id IN ?", memberUIDs).
			Find(&briefs).Error; err == nil {
			for _, u := range briefs {
				users[int(u.Id)] = u
			}
		}
		// Best-effort: missing users just render as empty strings rather
		// than fail the whole detail load.
	}

	dtos := make([]memberDTO, 0, len(members))
	for _, m := range members {
		u := users[m.UID]
		dtos = append(dtos, memberDTO{
			Id:        m.Id,
			CreatedAt: m.CreatedAt,
			UpdatedAt: m.UpdatedAt,
			TeamID:    m.TeamID,
			UID:       m.UID,
			Role:      m.Role,
			Status:    m.Status,
			Email:     u.Email,
			Nickname:  u.Nickname,
		})
	}

	response.OkWithData(gin.H{
		"team":    team,
		"members": dtos,
	}, c)
}

func (a *TeamApi) Update(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	id, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid team id", c)
		return
	}

	var req updateTeamRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	db := globals.GraDBs["system"]
	team, ok := loadTeamForOwner(c, db, id, int(uid))
	if !ok {
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		name := truncateRunes(strings.TrimSpace(*req.Name), teamNameMax)
		if name == "" {
			response.FailWithMessage("Name cannot be empty", c)
			return
		}
		updates["name"] = name
	}
	if req.Description != nil {
		updates["description"] = truncateRunes(strings.TrimSpace(*req.Description), teamDescriptionMax)
	}
	if req.Status != nil {
		if *req.Status != model.TeamStatusActive && *req.Status != model.TeamStatusArchived {
			response.FailWithMessage("Invalid status", c)
			return
		}
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		response.OkWithData(team, c)
		return
	}

	if err := db.Model(&model.Team{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(updates).Error; err != nil {
		respondInternal(c, "Update failed", err)
		return
	}

	if reloaded, ok := loadTeamForMember(c, db, id, int(uid)); ok {
		response.OkWithData(reloaded, c)
	}
}

func (a *TeamApi) Delete(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	id, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid team id", c)
		return
	}

	db := globals.GraDBs["system"]
	if _, ok := loadTeamForOwner(c, db, id, int(uid)); !ok {
		return
	}

	now := time.Now()
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Team{}).
			Where("id = ? AND deleted_at IS NULL", id).
			Update("deleted_at", now).Error; err != nil {
			return err
		}
		// Soft-delete memberships too so the "teams I'm in" list on every
		// member's dashboard drops cleanly.
		return tx.Model(&model.TeamMember{}).
			Where("team_id = ? AND deleted_at IS NULL", id).
			Update("deleted_at", now).Error
	})
	if err != nil {
		respondInternal(c, "Delete failed", err)
		return
	}
	response.OkWithMessage("Deleted", c)
}

func (a *TeamApi) AddMember(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	teamID, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid team id", c)
		return
	}

	var req addMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		response.FailWithMessage("Email is required", c)
		return
	}
	role := strings.ToLower(strings.TrimSpace(req.Role))
	if _, okRole := allowedTeamRoles[role]; !okRole {
		role = model.TeamMemberRoleMember
	}

	db := globals.GraDBs["system"]
	if _, ok := loadTeamForOwner(c, db, teamID, int(uid)); !ok {
		return
	}
	// Prevent demoting the owner via this endpoint.
	if role == model.TeamMemberRoleOwner {
		response.FailWithMessage("Use team-transfer flow to change the owner (not in MVP)", c)
		return
	}

	// Resolve email → UID. Reject deleted/banned accounts so an owner can't
	// resurrect a tombstoned user as a teammate. The deleted_<uid>_<unix>
	// sentinel emails live in the same column with the unique index, but
	// `ban=true` filters them out.
	var invitee model.User
	if err := db.Where("LOWER(email) = ? AND ban = ?", email, false).
		First(&invitee).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("No account found with that email", c)
			return
		}
		respondInternal(c, "Failed to look up user", err)
		return
	}
	inviteeUID := int(invitee.Id)
	if inviteeUID == int(uid) {
		response.FailWithMessage("You're already the owner of this team", c)
		return
	}

	// Guard against runaway scripts stuffing a team. Counts only active
	// rows — soft-deleted members don't contribute to the cap. Resurrecting
	// an existing membership bypasses this because it doesn't grow the
	// active count.
	var activeCount int64
	if err := db.Model(&model.TeamMember{}).
		Where("team_id = ? AND deleted_at IS NULL AND status = ?",
			teamID, model.TeamMemberStatusActive).
		Count(&activeCount).Error; err == nil && activeCount >= teamMaxMembers {
		response.FailWithMessage(
			fmt.Sprintf("Team is at the %d-member cap — remove someone first.", teamMaxMembers),
			c,
		)
		return
	}

	// If the user is already a soft-deleted member, resurrect rather than
	// insert a duplicate (uk_team_uid excludes deleted rows so insert
	// would succeed but we'd have zombie rows cluttering queries).
	var existing model.TeamMember
	err := db.Where(
		"team_id = ? AND uid = ?", teamID, inviteeUID,
	).First(&existing).Error

	if err == nil {
		if existing.DeletedAt == nil && existing.Status == model.TeamMemberStatusActive {
			response.FailWithMessage("User is already a member", c)
			return
		}
		if err := db.Model(&model.TeamMember{}).
			Where("id = ?", existing.Id).
			Updates(map[string]interface{}{
				"deleted_at": nil,
				"status":     model.TeamMemberStatusActive,
				"role":       role,
			}).Error; err != nil {
			respondInternal(c, "Re-add failed", err)
			return
		}
		existing.DeletedAt = nil
		existing.Status = model.TeamMemberStatusActive
		existing.Role = role
		response.OkWithData(existing, c)
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		respondInternal(c, "Failed to check membership", err)
		return
	}

	member := model.TeamMember{
		TeamID: teamID,
		UID:    inviteeUID,
		Role:   role,
		Status: model.TeamMemberStatusActive,
	}
	if err := db.Create(&member).Error; err != nil {
		respondInternal(c, "Add failed", err)
		return
	}
	response.OkWithData(member, c)
}

func (a *TeamApi) RemoveMember(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	teamID, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid team id", c)
		return
	}
	memberID, ok := parseIDParam(c, "mid")
	if !ok {
		response.FailWithMessage("Invalid member id", c)
		return
	}

	db := globals.GraDBs["system"]
	team, ok := loadTeamForMember(c, db, teamID, int(uid))
	if !ok {
		return
	}

	var target model.TeamMember
	err := db.Where(
		"id = ? AND team_id = ? AND deleted_at IS NULL",
		memberID, teamID,
	).First(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.FailWithMessage("Member not found", c)
		return
	}
	if err != nil {
		respondInternal(c, "Failed to load member", err)
		return
	}

	// Authorisation: owner can remove anyone except themselves; members
	// can self-leave. Refuse owner self-removal until a proper transfer
	// flow exists.
	isOwner := int(uid) == team.OwnerUID
	isSelf := target.UID == int(uid)
	if target.UID == team.OwnerUID {
		response.FailWithMessage("The owner cannot be removed without transferring ownership first", c)
		return
	}
	if !isOwner && !isSelf {
		response.FailWithMessage("Only the owner can remove other members", c)
		return
	}

	if err := db.Model(&model.TeamMember{}).
		Where("id = ?", memberID).
		Updates(map[string]interface{}{
			"deleted_at": time.Now(),
			"status":     model.TeamMemberStatusRemoved,
		}).Error; err != nil {
		respondInternal(c, "Remove failed", err)
		return
	}
	response.OkWithMessage("Removed", c)
}

// --- helpers ---

// loadTeamForMember ensures the caller is an active member of the team
// before returning it. Used for read-only access paths.
func loadTeamForMember(c *gin.Context, db *gorm.DB, teamID uint64, uid int) (*model.Team, bool) {
	var team model.Team
	if err := db.Where("id = ? AND deleted_at IS NULL", teamID).First(&team).Error; err != nil {
		response.FailWithMessage("Team not found", c)
		return nil, false
	}
	if !isActiveTeamMember(db, teamID, uid) {
		response.FailWithMessage("Team not found", c) // deliberate: don't leak existence
		return nil, false
	}
	return &team, true
}

// loadTeamForOwner is the stricter version used for mutations. The
// current user must be the team's owner.
func loadTeamForOwner(c *gin.Context, db *gorm.DB, teamID uint64, uid int) (*model.Team, bool) {
	team, ok := loadTeamForMember(c, db, teamID, uid)
	if !ok {
		return nil, false
	}
	if team.OwnerUID != uid {
		response.FailWithMessage("Only the team owner can do that", c)
		return nil, false
	}
	return team, true
}

// isActiveTeamMember — pure membership check, used across surfaces that
// need to authz team-shared resources.
func isActiveTeamMember(db *gorm.DB, teamID uint64, uid int) bool {
	var count int64
	err := db.Model(&model.TeamMember{}).
		Where(
			"team_id = ? AND uid = ? AND status = ? AND deleted_at IS NULL",
			teamID, uid, model.TeamMemberStatusActive,
		).
		Count(&count).Error
	if err != nil {
		return false
	}
	return count > 0
}

// teamIDsForUser returns the set of team IDs the user is an active
// member of. Used to broaden the project-visibility query to include
// team-shared projects.
func teamIDsForUser(db *gorm.DB, uid int) ([]uint64, error) {
	var rows []model.TeamMember
	if err := db.Select("team_id").
		Where(
			"uid = ? AND status = ? AND deleted_at IS NULL",
			uid, model.TeamMemberStatusActive,
		).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.TeamID)
	}
	return ids, nil
}

// normaliseTeamSlug sanitises user input. If empty, derives from name.
// Legal chars: a-z, 0-9, dash. Collapses runs of invalid chars to one
// dash. Returns "" if nothing usable remained; caller should error.
func normaliseTeamSlug(raw, fallbackFromName string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		s = strings.ToLower(fallbackFromName)
	}
	// Filter to letters/digits/dash; collapse other runs to a single dash.
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteRune('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		// Fall back to a uid-less random stamp so insert still succeeds.
		out = "team-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if len(out) > teamSlugMax {
		out = out[:teamSlugMax]
	}
	return out
}

func isDuplicateTeamSlugError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "uk_slug")
}
