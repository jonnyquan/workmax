package tools

import (
	"errors"
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

// CharacterApi handles CRUD for the shared character asset system.
type CharacterApi struct{}

// --- Request/Response DTOs ---

type createCharacterRequest struct {
	ProjectID       *uint64                `json:"projectId"`
	Name            string                 `json:"name" binding:"required"`
	Slug            string                 `json:"slug"`
	AvatarImageURL  string                 `json:"avatarImageUrl"`
	RoleType        string                 `json:"roleType"`
	Gender          string                 `json:"gender"`
	AgeRange        string                 `json:"ageRange"`
	Appearance      string                 `json:"appearance"`
	Personality     string                 `json:"personality"`
	VisualDNA       map[string]interface{} `json:"visualDna"`
	PromptSuffix    string                 `json:"promptSuffix"`
	NegativePrompt  string                 `json:"negativePrompt"`
	IdentityAnchors map[string]interface{} `json:"identityAnchors"`
	NegativeAnchors map[string]interface{} `json:"negativeAnchors"`
	VoicePreset     string                 `json:"voicePreset"`
	SourceKind      string                 `json:"sourceKind"`
}

type updateCharacterRequest struct {
	ProjectID      *uint64                `json:"projectId"`
	Name           *string                `json:"name"`
	Slug           *string                `json:"slug"`
	AvatarImageURL *string                `json:"avatarImageUrl"`
	RoleType       *string                `json:"roleType"`
	Gender         *string                `json:"gender"`
	AgeRange       *string                `json:"ageRange"`
	Appearance     *string                `json:"appearance"`
	Personality    *string                `json:"personality"`
	VisualDNA      map[string]interface{} `json:"visualDna"`
	PromptSuffix   *string                `json:"promptSuffix"`
	NegativePrompt *string                `json:"negativePrompt"`
	// Pointer to map so clients can distinguish "leave anchors alone"
	// (field omitted) from "clear anchors" (explicit empty object).
	IdentityAnchors *map[string]interface{} `json:"identityAnchors"`
	NegativeAnchors *map[string]interface{} `json:"negativeAnchors"`
	// Pointer-to-string so callers can clear the voice (send "")
	// vs leave it unchanged (omit the field).
	VoicePreset *string `json:"voicePreset"`
	LoraModelID *uint64 `json:"loraModelId"`
	Status      *int8   `json:"status"`
}

type addReferenceRequest struct {
	ImageURL      string                 `json:"imageUrl" binding:"required"`
	ReferenceType string                 `json:"referenceType"`
	Label         string                 `json:"label"`
	SortOrder     *int                   `json:"sortOrder"`
	Metadata      map[string]interface{} `json:"metadata"`
}

type reorderReferencesRequest struct {
	Items []struct {
		ID        uint64 `json:"id" binding:"required"`
		SortOrder int    `json:"sortOrder"`
	} `json:"items" binding:"required"`
}

// --- Helpers ---

var allowedRoleTypes = map[string]struct{}{
	model.CharacterRoleProtagonist: {},
	model.CharacterRoleSupporting:  {},
	model.CharacterRoleExtra:       {},
}

var allowedSourceKinds = map[string]struct{}{
	model.CharacterSourceManual:         {},
	model.CharacterSourceComicExtracted: {},
	model.CharacterSourceTemplate:       {},
}

var allowedReferenceTypes = map[string]struct{}{
	model.CharacterReferenceTypeFace:    {},
	model.CharacterReferenceTypeBody:    {},
	model.CharacterReferenceTypeOutfit:  {},
	model.CharacterReferenceTypeExpress: {},
	model.CharacterReferenceTypePose:    {},
}

func normalizeRoleType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if _, ok := allowedRoleTypes[value]; ok {
		return value
	}
	return model.CharacterRoleSupporting
}

func normalizeSourceKind(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if _, ok := allowedSourceKinds[value]; ok {
		return value
	}
	return model.CharacterSourceManual
}

func normalizeReferenceType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if _, ok := allowedReferenceTypes[value]; ok {
		return value
	}
	return model.CharacterReferenceTypeFace
}

func parseIDParam(c *gin.Context, key string) (uint64, bool) {
	raw := strings.TrimSpace(c.Param(key))
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// parsePagination reads ?page & ?limit query params, clamps them to safe
// ranges, and returns (page>=1, 1<=limit<=max). Defaults apply when the
// caller omits a parameter or sends an invalid value.
func parsePagination(c *gin.Context, defaultLimit, maxLimit int) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(defaultLimit)))
	if limit < 1 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	return page, limit
}

// loadCharacter fetches a character that belongs to the current user; returns
// (character, ok). When ok is false, an error response has already been written.
func loadCharacter(c *gin.Context, db *gorm.DB, id uint64, uid int) (*model.Character, bool) {
	var character model.Character
	err := db.Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).First(&character).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.FailWithMessage("Character not found", c)
		return nil, false
	}
	if err != nil {
		respondInternal(c, "Failed to load character", err)
		return nil, false
	}
	return &character, true
}

// --- Handlers ---

func (a *CharacterApi) Create(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	var req createCharacterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		response.FailWithMessage("Character name is required", c)
		return
	}

	character := model.Character{
		UID:            int(uid),
		ProjectID:      req.ProjectID,
		Name:           name,
		Slug:           strings.TrimSpace(req.Slug),
		AvatarImageURL: strings.TrimSpace(req.AvatarImageURL),
		RoleType:       normalizeRoleType(req.RoleType),
		Gender:         strings.TrimSpace(req.Gender),
		AgeRange:       strings.TrimSpace(req.AgeRange),
		Appearance:     req.Appearance,
		Personality:    req.Personality,
		PromptSuffix:   req.PromptSuffix,
		NegativePrompt: req.NegativePrompt,
		VoicePreset:    strings.TrimSpace(req.VoicePreset),
		SourceKind:     normalizeSourceKind(req.SourceKind),
		Status:         model.CharacterStatusActive,
	}
	if req.VisualDNA != nil {
		character.VisualDNAJSON = model.JSONMap(req.VisualDNA)
	}
	if len(req.IdentityAnchors) > 0 {
		character.IdentityAnchors = model.JSONMap(req.IdentityAnchors)
	}
	if len(req.NegativeAnchors) > 0 {
		character.NegativeAnchors = model.JSONMap(req.NegativeAnchors)
	}
	if character.Slug == "" {
		character.Slug = utils.GenerateUUID()
	}

	db := globals.GraDBs["system"]
	if err := db.Create(&character).Error; err != nil {
		respondInternal(c, "Create character failed", err)
		return
	}
	response.OkWithData(character, c)
}

func (a *CharacterApi) List(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	db := globals.GraDBs["system"].Model(&model.Character{}).
		Where("uid = ? AND deleted_at IS NULL", uid)

	if projectParam := strings.TrimSpace(c.Query("projectId")); projectParam != "" {
		if projectParam == "null" || projectParam == "0" {
			db = db.Where("project_id IS NULL")
		} else if pid, err := strconv.ParseUint(projectParam, 10, 64); err == nil && pid > 0 {
			db = db.Where("project_id = ?", pid)
		}
	}
	if roleType := strings.TrimSpace(c.Query("roleType")); roleType != "" {
		db = db.Where("role_type = ?", normalizeRoleType(roleType))
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		if v, err := strconv.Atoi(status); err == nil {
			db = db.Where("status = ?", v)
		}
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		respondInternal(c, "Failed to count characters", err)
		return
	}

	items := make([]model.Character, 0, limit)
	if err := db.Order("id desc").Offset((page - 1) * limit).Limit(limit).Find(&items).Error; err != nil {
		respondInternal(c, "Failed to list characters", err)
		return
	}

	response.OkWithData(gin.H{
		"items": items,
		"total": total,
		"page":  page,
		"limit": limit,
	}, c)
}

func (a *CharacterApi) Get(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	id, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid character id", c)
		return
	}

	db := globals.GraDBs["system"]
	character, ok := loadCharacter(c, db, id, int(uid))
	if !ok {
		return
	}

	references := make([]model.CharacterReference, 0)
	if err := db.Where("character_id = ? AND deleted_at IS NULL", id).
		Order("sort_order asc, id asc").
		Find(&references).Error; err != nil {
		respondInternal(c, "Failed to load references", err)
		return
	}

	response.OkWithData(gin.H{
		"character":  character,
		"references": references,
	}, c)
}

func (a *CharacterApi) Update(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	id, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid character id", c)
		return
	}

	var req updateCharacterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	db := globals.GraDBs["system"]
	character, ok := loadCharacter(c, db, id, int(uid))
	if !ok {
		return
	}

	updates := map[string]interface{}{}
	if req.ProjectID != nil {
		updates["project_id"] = *req.ProjectID
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			response.FailWithMessage("Name cannot be empty", c)
			return
		}
		updates["name"] = name
	}
	if req.Slug != nil {
		updates["slug"] = strings.TrimSpace(*req.Slug)
	}
	if req.AvatarImageURL != nil {
		updates["avatar_image_url"] = strings.TrimSpace(*req.AvatarImageURL)
	}
	if req.RoleType != nil {
		updates["role_type"] = normalizeRoleType(*req.RoleType)
	}
	if req.Gender != nil {
		updates["gender"] = strings.TrimSpace(*req.Gender)
	}
	if req.AgeRange != nil {
		updates["age_range"] = strings.TrimSpace(*req.AgeRange)
	}
	if req.Appearance != nil {
		updates["appearance"] = *req.Appearance
	}
	if req.Personality != nil {
		updates["personality"] = *req.Personality
	}
	if req.VisualDNA != nil {
		updates["visual_dna_json"] = model.JSONMap(req.VisualDNA)
	}
	if req.PromptSuffix != nil {
		updates["prompt_suffix"] = *req.PromptSuffix
	}
	if req.NegativePrompt != nil {
		updates["negative_prompt"] = *req.NegativePrompt
	}
	anchorsTouched := false
	if req.IdentityAnchors != nil {
		// Empty map intentionally clears the anchors; GORM + JSON type
		// persists the zero-length object as `{}`. When the user empties
		// anchors we also stamp calibratedAt to NULL so the UI no longer
		// shows a stale calibration date.
		updates["identity_anchors"] = model.JSONMap(*req.IdentityAnchors)
		if len(*req.IdentityAnchors) == 0 {
			updates["calibrated_at"] = nil
		}
		anchorsTouched = true
	}
	if req.NegativeAnchors != nil {
		updates["negative_anchors"] = model.JSONMap(*req.NegativeAnchors)
		anchorsTouched = true
	}
	// SD-S0.2: anchor edits propagate the version counter so panel-shot
	// snapshots can detect "rendered against an earlier anchor revision".
	if anchorsTouched {
		updates["anchors_version"] = gorm.Expr("anchors_version + 1")
	}
	if req.VoicePreset != nil {
		// Empty string explicitly clears the preset; non-empty stamps
		// a new one. Omitted field leaves the column alone.
		updates["voice_preset"] = strings.TrimSpace(*req.VoicePreset)
	}
	if req.LoraModelID != nil {
		updates["lora_model_id"] = *req.LoraModelID
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		response.OkWithData(character, c)
		return
	}

	if err := db.Model(&model.Character{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, int(uid)).
		Updates(updates).Error; err != nil {
		respondInternal(c, "Update failed", err)
		return
	}

	if reloaded, ok := loadCharacter(c, db, id, int(uid)); ok {
		response.OkWithData(reloaded, c)
	}
}

func (a *CharacterApi) Delete(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	id, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid character id", c)
		return
	}

	db := globals.GraDBs["system"]
	now := time.Now()
	if err := db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Character{}).
			Where("id = ? AND uid = ? AND deleted_at IS NULL", id, int(uid)).
			Update("deleted_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&model.CharacterReference{}).
			Where("character_id = ? AND deleted_at IS NULL", id).
			Update("deleted_at", now).Error
	}); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Character not found", c)
			return
		}
		respondInternal(c, "Delete failed", err)
		return
	}
	response.OkWithMessage("Deleted", c)
}

// --- References ---

func (a *CharacterApi) AddReference(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	characterID, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid character id", c)
		return
	}

	var req addReferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	db := globals.GraDBs["system"]
	if _, ok := loadCharacter(c, db, characterID, int(uid)); !ok {
		return
	}

	reference := model.CharacterReference{
		CharacterID:   characterID,
		UID:           int(uid),
		ImageURL:      strings.TrimSpace(req.ImageURL),
		ReferenceType: normalizeReferenceType(req.ReferenceType),
		Label:         strings.TrimSpace(req.Label),
	}
	if req.SortOrder != nil {
		reference.SortOrder = *req.SortOrder
	}
	if req.Metadata != nil {
		reference.Metadata = model.JSONMap(req.Metadata)
	}

	if err := db.Create(&reference).Error; err != nil {
		respondInternal(c, "Add reference failed", err)
		return
	}
	response.OkWithData(reference, c)
}

func (a *CharacterApi) DeleteReference(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	refID, ok := parseIDParam(c, "refId")
	if !ok {
		response.FailWithMessage("Invalid reference id", c)
		return
	}

	db := globals.GraDBs["system"]
	res := db.Model(&model.CharacterReference{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL", refID, int(uid)).
		Update("deleted_at", time.Now())
	if res.Error != nil {
		response.FailWithMessage("Delete reference failed: "+res.Error.Error(), c)
		return
	}
	if res.RowsAffected == 0 {
		response.FailWithMessage("Reference not found", c)
		return
	}
	response.OkWithMessage("Deleted", c)
}

func (a *CharacterApi) ReorderReferences(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	characterID, ok := parseIDParam(c, "id")
	if !ok {
		response.FailWithMessage("Invalid character id", c)
		return
	}

	var req reorderReferencesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	db := globals.GraDBs["system"]
	if _, ok := loadCharacter(c, db, characterID, int(uid)); !ok {
		return
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, item := range req.Items {
			if err := tx.Model(&model.CharacterReference{}).
				Where("id = ? AND character_id = ? AND uid = ? AND deleted_at IS NULL",
					item.ID, characterID, int(uid)).
				Update("sort_order", item.SortOrder).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		respondInternal(c, "Reorder failed", err)
		return
	}

	response.OkWithMessage("Reordered", c)
}
