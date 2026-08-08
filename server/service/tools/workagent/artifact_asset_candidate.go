package workagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"server/model"
	"sort"
	"strings"
	"time"
	"unicode"

	"server/globals"
	workagentModel "server/model/workagent"
	globalAssetService "server/service/globalasset"
	projectService "server/service/project"
	"server/service/tools/workagent/skills"

	"gorm.io/gorm"
)

type ArtifactAssetCandidateRepository struct {
	db *gorm.DB
}

type ArtifactAssetCandidateInput struct {
	AssetKind string
	Name      string
	Slug      string
	Profile   map[string]interface{}
}

type ProjectDesignSystemHistoryItem struct {
	DesignSystemID uint       `json:"designSystemId,omitempty"`
	ProjectID      uint       `json:"projectId,omitempty"`
	Basename       string     `json:"basename"`
	Title          string     `json:"title"`
	DerivedFrom    string     `json:"derivedFrom,omitempty"`
	Version        string     `json:"version"`
	VersionDiff    string     `json:"versionDiff,omitempty"`
	Status         string     `json:"status"`
	Source         string     `json:"source"`
	ReviewedBy     int        `json:"reviewedBy,omitempty"`
	ReviewedAt     *time.Time `json:"reviewedAt,omitempty"`
	ReviewNote     string     `json:"reviewNote,omitempty"`
	CreatedAt      time.Time  `json:"createdAt,omitempty"`
	UpdatedAt      time.Time  `json:"updatedAt,omitempty"`
}

func NewArtifactAssetCandidateRepository(db *gorm.DB) *ArtifactAssetCandidateRepository {
	if db == nil {
		db = globals.GraDBs["system"]
	}
	return &ArtifactAssetCandidateRepository{db: db}
}

func (r *ArtifactAssetCandidateRepository) UpsertForArtifact(uid int, threadID uint, artifactID uint, input ArtifactAssetCandidateInput) (*workagentModel.ArtifactAssetCandidate, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("upsert artifact asset candidate: nil repository")
	}
	assetKind := strings.TrimSpace(input.AssetKind)
	if !validArtifactAssetKind(assetKind) {
		return nil, fmt.Errorf("invalid asset kind: %s", assetKind)
	}
	profileJSON, err := marshalAssetCandidateProfile(input.Profile)
	if err != nil {
		return nil, err
	}

	var artifact workagentModel.ArtifactRegistry
	if err := r.db.Where("id = ? AND uid = ? AND thread_id = ?", artifactID, uid, threadID).First(&artifact).Error; err != nil {
		return nil, fmt.Errorf("load artifact: %w", err)
	}
	if artifact.Status != workagentModel.ArtifactStatusFinal && artifact.Status != workagentModel.ArtifactStatusExported {
		return nil, fmt.Errorf("artifact must be final or exported before saving an asset candidate")
	}

	var row workagentModel.ArtifactAssetCandidate
	err = r.db.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("artifact_id = ? AND asset_kind = ? AND uid = ? AND thread_id = ?", artifactID, assetKind, uid, threadID).First(&row).Error
		if err == nil {
			updates := map[string]interface{}{
				"thread_file_id": artifact.ThreadFileID,
				"name":           strings.TrimSpace(input.Name),
				"slug":           strings.TrimSpace(input.Slug),
				"profile_json":   profileJSON,
				"status":         workagentModel.ArtifactAssetCandidateStatusDraft,
				"target_kind":    "",
				"target_id":      uint(0),
			}
			if err := tx.Model(&row).Updates(updates).Error; err != nil {
				return fmt.Errorf("update artifact asset candidate: %w", err)
			}
			return tx.First(&row, row.Id).Error
		}
		if err != nil && !isRecordNotFound(err) {
			return fmt.Errorf("find artifact asset candidate: %w", err)
		}
		row = workagentModel.ArtifactAssetCandidate{
			UID:          uid,
			ThreadID:     threadID,
			ArtifactID:   artifact.Id,
			ThreadFileID: artifact.ThreadFileID,
			AssetKind:    assetKind,
			Name:         strings.TrimSpace(input.Name),
			Slug:         strings.TrimSpace(input.Slug),
			ProfileJSON:  profileJSON,
			Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create artifact asset candidate: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *ArtifactAssetCandidateRepository) ListByThread(uid int, threadID uint) ([]workagentModel.ArtifactAssetCandidate, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("list artifact asset candidates: nil repository")
	}
	var rows []workagentModel.ArtifactAssetCandidate
	if err := r.db.
		Where("uid = ? AND thread_id = ?", uid, threadID).
		Order("updated_at DESC, id DESC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list artifact asset candidates: %w", err)
	}
	return rows, nil
}

func (r *ArtifactAssetCandidateRepository) UpdateStatus(uid int, threadID uint, candidateID uint, status string) (*workagentModel.ArtifactAssetCandidate, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("update artifact asset candidate status: nil repository")
	}
	status = strings.TrimSpace(status)
	if !validArtifactAssetCandidateStatus(status) {
		return nil, fmt.Errorf("invalid asset candidate status: %s", status)
	}
	var row workagentModel.ArtifactAssetCandidate
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND uid = ? AND thread_id = ?", candidateID, uid, threadID).First(&row).Error; err != nil {
			return fmt.Errorf("load artifact asset candidate: %w", err)
		}
		updates := map[string]interface{}{"status": status}
		if status == workagentModel.ArtifactAssetCandidateStatusConfirmed && row.AssetKind == workagentModel.ArtifactAssetKindReference {
			targetKind := row.TargetKind
			targetID := row.TargetID
			if targetKind == "" || targetID == 0 {
				var err error
				targetKind, targetID, err = materializeReferenceCandidate(tx, uid, row)
				if err != nil {
					return err
				}
			}
			updates["target_kind"] = targetKind
			updates["target_id"] = targetID
		}
		if status == workagentModel.ArtifactAssetCandidateStatusConfirmed && row.AssetKind == workagentModel.ArtifactAssetKindDesignSystem {
			if err := validateDesignSystemCandidate(row); err != nil {
				return err
			}
			targetKind := row.TargetKind
			targetID := row.TargetID
			if targetKind == "" || targetID == 0 {
				var err error
				targetKind, targetID, err = materializeDesignSystemCandidate(tx, uid, row)
				if err != nil {
					return err
				}
			}
			updates["target_kind"] = targetKind
			updates["target_id"] = targetID
		}
		if status == workagentModel.ArtifactAssetCandidateStatusConfirmed && row.AssetKind == workagentModel.ArtifactAssetKindPromptAsset {
			if err := validatePromptAssetCandidate(row); err != nil {
				return err
			}
			targetKind := row.TargetKind
			targetID := row.TargetID
			if targetKind == "" || targetID == 0 {
				var err error
				targetKind, targetID, err = materializePromptAssetCandidate(tx, uid, row)
				if err != nil {
					return err
				}
			}
			updates["target_kind"] = targetKind
			updates["target_id"] = targetID
		}
		if status == workagentModel.ArtifactAssetCandidateStatusConfirmed && isTypedAssetCandidateKind(row.AssetKind) {
			targetKind := row.TargetKind
			targetID := row.TargetID
			if targetKind == "" || targetID == 0 {
				var err error
				targetKind, targetID, err = materializeTypedAssetCandidate(tx, uid, row)
				if err != nil {
					return err
				}
			}
			updates["target_kind"] = targetKind
			updates["target_id"] = targetID
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return fmt.Errorf("update artifact asset candidate: %w", err)
		}
		return tx.First(&row, row.Id).Error
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func materializeReferenceCandidate(tx *gorm.DB, uid int, row workagentModel.ArtifactAssetCandidate) (string, uint, error) {
	var file workagentModel.ThreadFile
	if err := tx.Where("id = ? AND uid = ? AND thread_id = ?", row.ThreadFileID, uid, row.ThreadID).First(&file).Error; err != nil {
		return "", 0, fmt.Errorf("load candidate thread file: %w", err)
	}
	global, err := globalAssetService.NewRepository(tx).CreateFromThreadFile(&file)
	if err != nil {
		return "", 0, fmt.Errorf("materialize reference candidate: %w", err)
	}
	if global == nil || global.Id == 0 {
		return "", 0, fmt.Errorf("materialize reference candidate: empty global asset")
	}
	return workagentModel.ArtifactAssetCandidateTargetGlobalAsset, global.Id, nil
}

func materializeTypedAssetCandidate(tx *gorm.DB, uid int, row workagentModel.ArtifactAssetCandidate) (string, uint, error) {
	profile, err := parseAssetCandidateProfile(row)
	if err != nil {
		return "", 0, err
	}
	thread, err := loadAssetCandidateThread(tx, uid, row.ThreadID)
	if err != nil {
		return "", 0, err
	}
	name := assetCandidateName(row, profile)
	slug := assetCandidateSlug(row, profile, name)
	projectID := assetCandidateProjectID(thread)
	now := time.Now()
	sourceThreadID := uint64(row.ThreadID)
	switch row.AssetKind {
	case workagentModel.ArtifactAssetKindBrand:
		asset := model.Brand{
			UID:             uid,
			ProjectID:       projectID,
			Lang:            assetCandidateLang(profile),
			Name:            name,
			Slug:            slug,
			Colors:          assetCandidateJSONMap(profile, "colors", "color", "palette"),
			Typography:      assetCandidateJSONMap(profile, "typography", "type"),
			Spacing:         assetCandidateJSONMap(profile, "spacing"),
			Layout:          assetCandidateJSONMap(profile, "layout"),
			Components:      assetCandidateJSONMap(profile, "components"),
			Motion:          assetCandidateJSONMap(profile, "motion"),
			Voice:           assetCandidateJSONMap(profile, "voice"),
			IdentityAnchors: assetCandidateJSONMap(profile, "identityAnchors", "identity_anchors", "anchors"),
			NegativeAnchors: assetCandidateJSONMap(profile, "negativeAnchors", "negative_anchors"),
			PromptSuffix:    assetCandidateString(profile, "promptSuffix", "prompt_suffix"),
			NegativePrompt:  assetCandidateString(profile, "negativePrompt", "negative_prompt"),
			SourceKind:      model.BrandSourceExtracted,
			SourceThreadID:  &sourceThreadID,
			RawSpecMD:       assetCandidateString(profile, "rawSpecMd", "raw_spec_md", "markdown", "body", "designSystemMarkdown"),
			Status:          model.BrandStatusActive,
			Confirmed:       true,
			ConfirmedAt:     &now,
		}
		if err := tx.Create(&asset).Error; err != nil {
			return "", 0, fmt.Errorf("materialize brand candidate: %w", err)
		}
		return workagentModel.ArtifactAssetKindBrand, asset.Id, nil
	case workagentModel.ArtifactAssetKindCharacter:
		asset := model.Character{
			UID:             uid,
			ProjectID:       projectID,
			Lang:            assetCandidateLang(profile),
			Name:            name,
			Slug:            slug,
			AvatarImageURL:  assetCandidateString(profile, "avatarImageUrl", "avatar_image_url", "imageUrl", "image_url"),
			RoleType:        assetCandidateString(profile, "roleType", "role_type", "role"),
			Gender:          assetCandidateString(profile, "gender"),
			AgeRange:        assetCandidateString(profile, "ageRange", "age_range", "age"),
			Appearance:      assetCandidateStringOrJSON(profile, "appearance"),
			Personality:     assetCandidateStringOrJSON(profile, "personality"),
			VisualDNAJSON:   assetCandidateJSONMap(profile, "visualDna", "visual_dna", "visualDNA"),
			PromptSuffix:    assetCandidateString(profile, "promptSuffix", "prompt_suffix"),
			NegativePrompt:  assetCandidateString(profile, "negativePrompt", "negative_prompt"),
			IdentityAnchors: assetCandidateJSONMap(profile, "identityAnchors", "identity_anchors", "anchors"),
			NegativeAnchors: assetCandidateJSONMap(profile, "negativeAnchors", "negative_anchors"),
			AnchorsVersion:  1,
			SourceKind:      "extracted",
			Status:          model.CharacterStatusActive,
			Confirmed:       true,
			ConfirmedAt:     &now,
			SourceThreadID:  &sourceThreadID,
		}
		if asset.RoleType == "" {
			asset.RoleType = model.CharacterRoleSupporting
		}
		if err := tx.Create(&asset).Error; err != nil {
			return "", 0, fmt.Errorf("materialize character candidate: %w", err)
		}
		return workagentModel.ArtifactAssetKindCharacter, asset.Id, nil
	case workagentModel.ArtifactAssetKindProduct:
		asset := model.Product{
			UID:             uid,
			ProjectID:       projectID,
			Lang:            assetCandidateLang(profile),
			Name:            name,
			Slug:            slug,
			SKU:             assetCandidateString(profile, "sku", "SKU"),
			Category:        assetCandidateString(profile, "category"),
			Description:     assetCandidateStringOrJSON(profile, "description", "sellingPoints", "selling_points"),
			Specs:           assetCandidateJSONMap(profile, "specs", "specifications"),
			VisualGuidance:  assetCandidateJSONMap(profile, "visualGuidance", "visual_guidance", "photography"),
			TargetAudience:  assetCandidateJSONMap(profile, "targetAudience", "target_audience", "audience"),
			IdentityAnchors: assetCandidateJSONMap(profile, "identityAnchors", "identity_anchors", "anchors"),
			NegativeAnchors: assetCandidateJSONMap(profile, "negativeAnchors", "negative_anchors"),
			AnchorsVersion:  1,
			PromptSuffix:    assetCandidateString(profile, "promptSuffix", "prompt_suffix"),
			NegativePrompt:  assetCandidateString(profile, "negativePrompt", "negative_prompt"),
			SourceKind:      "extracted",
			SourceThreadID:  &sourceThreadID,
			RawSpecMD:       assetCandidateString(profile, "rawSpecMd", "raw_spec_md", "markdown", "body"),
			Status:          model.ProductStatusActive,
			Confirmed:       true,
			ConfirmedAt:     &now,
		}
		if err := tx.Create(&asset).Error; err != nil {
			return "", 0, fmt.Errorf("materialize product candidate: %w", err)
		}
		return workagentModel.ArtifactAssetKindProduct, asset.Id, nil
	case workagentModel.ArtifactAssetKindDirectorStyle:
		asset := model.DirectorStyle{
			UID:             uid,
			ProjectID:       projectID,
			Lang:            assetCandidateLang(profile),
			Name:            name,
			Slug:            slug,
			Era:             assetCandidateString(profile, "era"),
			Genre:           assetCandidateString(profile, "genre"),
			Composition:     assetCandidateJSONMap(profile, "composition"),
			Color:           assetCandidateJSONMap(profile, "color", "colors", "palette"),
			Lighting:        assetCandidateJSONMap(profile, "lighting"),
			Motion:          assetCandidateJSONMap(profile, "motion"),
			Texture:         assetCandidateJSONMap(profile, "texture"),
			IdentityAnchors: assetCandidateJSONMap(profile, "identityAnchors", "identity_anchors", "anchors"),
			NegativeAnchors: assetCandidateJSONMap(profile, "negativeAnchors", "negative_anchors"),
			AnchorsVersion:  1,
			PromptSuffix:    assetCandidateString(profile, "promptSuffix", "prompt_suffix", "promptFragment", "prompt_fragment"),
			NegativePrompt:  assetCandidateString(profile, "negativePrompt", "negative_prompt"),
			SourceKind:      model.DirectorStyleSourceExtracted,
			SourceThreadID:  &sourceThreadID,
			RawSpecMD:       assetCandidateString(profile, "rawSpecMd", "raw_spec_md", "markdown", "body"),
			Status:          model.DirectorStyleStatusActive,
			Confirmed:       true,
			ConfirmedAt:     &now,
		}
		if err := tx.Create(&asset).Error; err != nil {
			return "", 0, fmt.Errorf("materialize director style candidate: %w", err)
		}
		return workagentModel.ArtifactAssetKindDirectorStyle, asset.Id, nil
	default:
		return "", 0, fmt.Errorf("unsupported typed asset candidate kind: %s", row.AssetKind)
	}
}

func validateDesignSystemCandidate(row workagentModel.ArtifactAssetCandidate) error {
	body, ok := designSystemCandidateMarkdown(row)
	if !ok {
		return fmt.Errorf("confirm design system candidate: profile must include designSystemMarkdown")
	}
	basename := strings.TrimSpace(row.Slug)
	if basename == "" {
		basename = fmt.Sprintf("project-candidate-%d", row.Id)
	}
	if err := skills.ValidateDesignSystemMarkdown(basename, body); err != nil {
		return fmt.Errorf("confirm design system candidate: %w", err)
	}
	return nil
}

func materializeDesignSystemCandidate(tx *gorm.DB, uid int, row workagentModel.ArtifactAssetCandidate) (string, uint, error) {
	body, ok := designSystemCandidateMarkdown(row)
	if !ok {
		return "", 0, fmt.Errorf("materialize design system candidate: profile must include designSystemMarkdown")
	}
	profile, err := parseAssetCandidateProfile(row)
	if err != nil {
		return "", 0, err
	}
	thread, err := loadAssetCandidateThread(tx, uid, row.ThreadID)
	if err != nil {
		return "", 0, err
	}
	title := assetCandidateName(row, profile)
	slug := assetCandidateSlug(row, profile, title)
	designSystem := workagentModel.ProjectDesignSystem{
		UID:         uid,
		ProjectID:   thread.ProjectID,
		ThreadID:    row.ThreadID,
		ArtifactID:  row.ArtifactID,
		CandidateID: row.Id,
		Name:        strings.TrimSpace(row.Name),
		Slug:        strings.TrimSpace(row.Slug),
		Basename:    designSystemCandidateBasename(row.Id, slug),
		Title:       title,
		DerivedFrom: fmt.Sprintf("artifact-%d", row.ArtifactID),
		Version:     1,
		Body:        body,
		Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := tx.Where("candidate_id = ? AND uid = ?", row.Id, uid).FirstOrCreate(&designSystem).Error; err != nil {
		return "", 0, fmt.Errorf("materialize design system candidate: %w", err)
	}
	return workagentModel.ArtifactAssetCandidateTargetDesignSystem, designSystem.Id, nil
}

func validatePromptAssetCandidate(row workagentModel.ArtifactAssetCandidate) error {
	profile, err := parseAssetCandidateProfile(row)
	if err != nil {
		return err
	}
	if assetCandidateString(profile, "prompt", "promptContent", "prompt_content", "body") == "" {
		return fmt.Errorf("confirm prompt asset candidate: profile must include prompt or promptContent")
	}
	return nil
}

func materializePromptAssetCandidate(tx *gorm.DB, uid int, row workagentModel.ArtifactAssetCandidate) (string, uint, error) {
	profile, err := parseAssetCandidateProfile(row)
	if err != nil {
		return "", 0, err
	}
	prompt := assetCandidateString(profile, "prompt", "promptContent", "prompt_content", "body")
	if prompt == "" {
		return "", 0, fmt.Errorf("materialize prompt asset candidate: profile must include prompt or promptContent")
	}
	thread, err := loadAssetCandidateThread(tx, uid, row.ThreadID)
	if err != nil {
		return "", 0, err
	}
	name := assetCandidateName(row, profile)
	slug := assetCandidateSlug(row, profile, name)
	promptAsset := workagentModel.PromptAsset{
		UID:            uid,
		ProjectID:      thread.ProjectID,
		ThreadID:       row.ThreadID,
		ArtifactID:     row.ArtifactID,
		CandidateID:    row.Id,
		Name:           name,
		Slug:           slug,
		Prompt:         prompt,
		NegativePrompt: assetCandidateString(profile, "negativePrompt", "negative_prompt"),
		ProfileJSON:    row.ProfileJSON,
		Status:         workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := tx.Where("candidate_id = ? AND uid = ?", row.Id, uid).FirstOrCreate(&promptAsset).Error; err != nil {
		return "", 0, fmt.Errorf("materialize prompt asset candidate: %w", err)
	}
	return workagentModel.ArtifactAssetCandidateTargetPromptAsset, promptAsset.Id, nil
}

func (r *ArtifactAssetCandidateRepository) ListConfirmedDesignSystemsForProject(uid int, projectID uint) ([]skills.DesignSystemCatalogItem, error) {
	return r.ListDesignSystemsForProject(uid, projectID, false)
}

func (r *ArtifactAssetCandidateRepository) ListDesignSystemsForProject(uid int, projectID uint, includePending bool) ([]skills.DesignSystemCatalogItem, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("list project design systems: nil repository")
	}
	if uid == 0 || projectID == 0 {
		return nil, nil
	}
	access, accessKnown, err := r.resolveProjectAccess(uid, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project design systems: load project: %w", err)
	}
	if accessKnown && !access.CanView() {
		return nil, nil
	}
	role := model.GlobalProjectRoleOwner
	if accessKnown {
		role = access.Role
	}
	var projectRows []workagentModel.ProjectDesignSystem
	projectQuery := r.db.Where("project_id = ?", projectID)
	if !accessKnown {
		projectQuery = projectQuery.Where("uid = ?", uid)
	}
	if err := projectQuery.Order("updated_at DESC, id DESC").Find(&projectRows).Error; err != nil {
		return nil, fmt.Errorf("list project design systems: %w", err)
	}
	items := make([]skills.DesignSystemCatalogItem, 0, len(projectRows))
	materializedCandidateIDs := map[uint]bool{}
	for _, row := range projectRows {
		if row.CandidateID != 0 {
			materializedCandidateIDs[row.CandidateID] = true
		}
		status := strings.TrimSpace(row.Status)
		if status == workagentModel.ProjectDesignSystemStatusArchived {
			continue
		}
		if !includePending && status != workagentModel.ArtifactAssetCandidateStatusConfirmed {
			continue
		}
		items = append(items, designSystemCatalogItemFromProjectRowForRole(row, role))
	}
	table := workagentModel.ArtifactAssetCandidate{}.TableName()
	var rows []workagentModel.ArtifactAssetCandidate
	candidateQuery := r.db.
		Model(&workagentModel.ArtifactAssetCandidate{}).
		Joins("JOIN w_workagent_thread AS t ON t.id = "+table+".thread_id").
		Where("t.project_id = ?", projectID).
		Where(table+".asset_kind = ? AND "+table+".status = ?", workagentModel.ArtifactAssetKindDesignSystem, workagentModel.ArtifactAssetCandidateStatusConfirmed).
		Order(table + ".updated_at DESC, " + table + ".id DESC")
	if !accessKnown {
		candidateQuery = candidateQuery.Where(table+".uid = ? AND t.uid = ?", uid, uid)
	}
	if err := candidateQuery.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list project design systems: %w", err)
	}
	for _, row := range rows {
		if materializedCandidateIDs[row.Id] {
			continue
		}
		items = append(items, designSystemCatalogItemFromCandidate(row))
	}
	return items, nil
}

func (r *ArtifactAssetCandidateRepository) resolveProjectAccess(uid int, projectID uint) (*projectService.ProjectAccess, bool, error) {
	access, err := projectService.NewRepository(r.db).ResolveAccess(projectID, uint(uid))
	if err == nil {
		return access, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var project model.CanvasProject
		if projectErr := r.db.Where("id = ? AND deleted_at IS NULL", projectID).First(&project).Error; projectErr == nil {
			return &projectService.ProjectAccess{Project: &project, Role: ""}, true, nil
		} else if projectErr != nil && !errors.Is(projectErr, gorm.ErrRecordNotFound) {
			return nil, false, projectErr
		}
		return nil, false, nil
	}
	return nil, false, err
}

func (r *ArtifactAssetCandidateRepository) UpdateProjectDesignSystemStatus(uid int, projectID uint, designSystemID uint, status string, reviewNote string) (*workagentModel.ProjectDesignSystem, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("update project design system status: nil repository")
	}
	status = strings.TrimSpace(status)
	if status != workagentModel.ArtifactAssetCandidateStatusConfirmed &&
		status != workagentModel.ProjectDesignSystemStatusArchived &&
		status != workagentModel.ProjectDesignSystemStatusRejected {
		return nil, fmt.Errorf("invalid project design system status: %s", status)
	}
	if uid == 0 || projectID == 0 || designSystemID == 0 {
		return nil, fmt.Errorf("update project design system status: invalid identity")
	}
	access, accessKnown, err := r.resolveProjectAccess(uid, projectID)
	if err != nil {
		return nil, fmt.Errorf("load project design system: %w", err)
	}
	if accessKnown && !access.CanManage() {
		return nil, fmt.Errorf("load project design system: %w", gorm.ErrRecordNotFound)
	}
	var row workagentModel.ProjectDesignSystem
	rowQuery := r.db.Where("id = ? AND project_id = ?", designSystemID, projectID)
	if !accessKnown {
		rowQuery = rowQuery.Where("uid = ?", uid)
	}
	if err := rowQuery.First(&row).Error; err != nil {
		return nil, fmt.Errorf("load project design system: %w", err)
	}
	if status == workagentModel.ArtifactAssetCandidateStatusConfirmed {
		if err := skills.ValidateDesignSystemMarkdown(strings.TrimSpace(row.Basename), row.Body); err != nil {
			return nil, fmt.Errorf("validate project design system before confirm: %w", err)
		}
	}
	now := time.Now().UTC()
	updates := map[string]interface{}{
		"status":      status,
		"reviewed_by": uid,
		"reviewed_at": now,
		"review_note": truncateProjectDesignSystemReviewNote(reviewNote),
	}
	if err := r.db.Model(&row).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update project design system: %w", err)
	}
	if err := r.db.First(&row, row.Id).Error; err != nil {
		return nil, fmt.Errorf("reload project design system: %w", err)
	}
	return &row, nil
}

func (r *ArtifactAssetCandidateRepository) ListProjectDesignSystemHistory(uid int, projectID uint, designSystemID uint) ([]ProjectDesignSystemHistoryItem, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("list project design system history: nil repository")
	}
	if uid == 0 || projectID == 0 || designSystemID == 0 {
		return nil, fmt.Errorf("list project design system history: invalid identity")
	}
	access, accessKnown, err := r.resolveProjectAccess(uid, projectID)
	if err != nil {
		return nil, fmt.Errorf("load project design system: %w", err)
	}
	if accessKnown && !access.CanView() {
		return nil, fmt.Errorf("load project design system: %w", gorm.ErrRecordNotFound)
	}
	var current workagentModel.ProjectDesignSystem
	currentQuery := r.db.Where("id = ? AND project_id = ?", designSystemID, projectID)
	if !accessKnown {
		currentQuery = currentQuery.Where("uid = ?", uid)
	}
	if err := currentQuery.First(&current).Error; err != nil {
		return nil, fmt.Errorf("load project design system: %w", err)
	}
	var rows []workagentModel.ProjectDesignSystem
	rowsQuery := r.db.Where("project_id = ?", projectID)
	if !accessKnown {
		rowsQuery = rowsQuery.Where("uid = ?", uid)
	}
	if err := rowsQuery.Order("version ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list project design system history: %w", err)
	}
	byBasename := map[string][]workagentModel.ProjectDesignSystem{}
	for _, row := range rows {
		basename := strings.TrimSpace(row.Basename)
		if basename == "" {
			continue
		}
		byBasename[basename] = append(byBasename[basename], row)
	}

	related := map[uint]workagentModel.ProjectDesignSystem{current.Id: current}
	knownBasenames := map[string]bool{}
	if basename := strings.TrimSpace(current.Basename); basename != "" {
		knownBasenames[basename] = true
	}
	for cursor := strings.TrimSpace(current.DerivedFrom); cursor != ""; {
		parents := byBasename[cursor]
		if len(parents) == 0 {
			knownBasenames[cursor] = true
			break
		}
		parent := parents[len(parents)-1]
		related[parent.Id] = parent
		knownBasenames[strings.TrimSpace(parent.Basename)] = true
		next := strings.TrimSpace(parent.DerivedFrom)
		if next == "" || knownBasenames[next] {
			if next != "" {
				knownBasenames[next] = true
			}
			break
		}
		cursor = next
	}
	changed := true
	for changed {
		changed = false
		for _, row := range rows {
			if row.Id == 0 {
				continue
			}
			if _, ok := related[row.Id]; ok {
				continue
			}
			if knownBasenames[strings.TrimSpace(row.DerivedFrom)] {
				related[row.Id] = row
				if basename := strings.TrimSpace(row.Basename); basename != "" && !knownBasenames[basename] {
					knownBasenames[basename] = true
					changed = true
				}
			}
		}
	}

	items := make([]ProjectDesignSystemHistoryItem, 0, len(related)+1)
	if official := officialDesignSystemHistoryRoot(knownBasenames); official != nil {
		items = append(items, *official)
	}
	for _, row := range rows {
		if _, ok := related[row.Id]; !ok {
			continue
		}
		parentBody := projectDesignSystemParentBody(row, byBasename)
		item := projectDesignSystemHistoryItemFromRow(row)
		item.VersionDiff = summarizeProjectDesignSystemDiff(parentBody, row.Body)
		items = append(items, item)
	}
	return items, nil
}

func (r *ArtifactAssetCandidateRepository) ForkProjectDesignSystem(uid int, projectID uint, designSystemID uint, name string, slug string) (*workagentModel.ProjectDesignSystem, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fork project design system: nil repository")
	}
	if uid == 0 || projectID == 0 || designSystemID == 0 {
		return nil, fmt.Errorf("fork project design system: invalid identity")
	}
	var forked workagentModel.ProjectDesignSystem
	err := r.db.Transaction(func(tx *gorm.DB) error {
		txRepo := NewArtifactAssetCandidateRepository(tx)
		access, accessKnown, err := txRepo.resolveProjectAccess(uid, projectID)
		if err != nil {
			return fmt.Errorf("load project design system: %w", err)
		}
		if accessKnown && !access.CanEdit() {
			return fmt.Errorf("load project design system: %w", gorm.ErrRecordNotFound)
		}
		var source workagentModel.ProjectDesignSystem
		sourceQuery := tx.Where("id = ? AND project_id = ? AND status = ?", designSystemID, projectID, workagentModel.ArtifactAssetCandidateStatusConfirmed)
		if !accessKnown {
			sourceQuery = sourceQuery.Where("uid = ?", uid)
		}
		if err := sourceQuery.First(&source).Error; err != nil {
			return fmt.Errorf("load project design system: %w", err)
		}
		forkName := strings.TrimSpace(name)
		if forkName == "" {
			forkName = strings.TrimSpace(source.Title)
		}
		if forkName == "" {
			forkName = strings.TrimSpace(source.Name)
		}
		if forkName == "" {
			forkName = "Forked Design System"
		}
		forkSlug := simpleAssetCandidateSlug(strings.TrimSpace(slug))
		if forkSlug == "" {
			forkSlug = simpleAssetCandidateSlug(forkName)
		}
		if forkSlug == "" {
			forkSlug = fmt.Sprintf("fork-%d", source.Id)
		}
		forkSlug = fmt.Sprintf("%s-fork-%d", forkSlug, time.Now().UnixNano())
		profileJSON, err := json.Marshal(map[string]interface{}{
			"designSystemMarkdown": source.Body,
			"forkedFrom":           source.Basename,
			"forkedFromId":         source.Id,
		})
		if err != nil {
			return fmt.Errorf("fork project design system: marshal profile: %w", err)
		}
		candidate := workagentModel.ArtifactAssetCandidate{
			UID:         uid,
			ThreadID:    source.ThreadID,
			ArtifactID:  source.ArtifactID,
			AssetKind:   workagentModel.ArtifactAssetKindDesignSystem,
			Name:        forkName,
			Slug:        forkSlug,
			ProfileJSON: string(profileJSON),
			Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
			TargetKind:  workagentModel.ArtifactAssetCandidateTargetDesignSystem,
		}
		if err := tx.Create(&candidate).Error; err != nil {
			return fmt.Errorf("fork project design system: create candidate: %w", err)
		}
		forked = workagentModel.ProjectDesignSystem{
			UID:         uid,
			ProjectID:   source.ProjectID,
			ThreadID:    source.ThreadID,
			ArtifactID:  source.ArtifactID,
			CandidateID: candidate.Id,
			Name:        forkName,
			Slug:        forkSlug,
			Basename:    designSystemCandidateBasename(candidate.Id, forkSlug),
			Title:       forkName,
			DerivedFrom: strings.TrimSpace(source.Basename),
			Version:     nextProjectDesignSystemVersion(source.Version),
			Body:        source.Body,
			Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
		}
		if err := tx.Create(&forked).Error; err != nil {
			return fmt.Errorf("fork project design system: create fork: %w", err)
		}
		if err := tx.Model(&candidate).Updates(map[string]interface{}{
			"target_kind": workagentModel.ArtifactAssetCandidateTargetDesignSystem,
			"target_id":   forked.Id,
		}).Error; err != nil {
			return fmt.Errorf("fork project design system: update candidate target: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &forked, nil
}

func (r *ArtifactAssetCandidateRepository) ForkOfficialDesignSystem(uid int, projectID uint, basename string, name string, slug string) (*workagentModel.ProjectDesignSystem, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("fork official design system: nil repository")
	}
	if uid == 0 || projectID == 0 {
		return nil, fmt.Errorf("fork official design system: invalid identity")
	}
	basename = strings.TrimSpace(basename)
	if basename == "" {
		return nil, fmt.Errorf("fork official design system: basename is required")
	}
	source, err := skills.LoadDesignSystem(basename)
	if err != nil {
		return nil, fmt.Errorf("fork official design system: load official system: %w", err)
	}
	if source == nil {
		return nil, fmt.Errorf("fork official design system: official system not found")
	}
	var forked workagentModel.ProjectDesignSystem
	err = r.db.Transaction(func(tx *gorm.DB) error {
		canEdit, err := projectService.NewRepository(tx).CanEditProject(projectID, uint(uid))
		if err != nil {
			return fmt.Errorf("fork official design system: load project: %w", err)
		}
		if !canEdit {
			return fmt.Errorf("fork official design system: load project: %w", gorm.ErrRecordNotFound)
		}
		forkName := strings.TrimSpace(name)
		if forkName == "" {
			forkName = skills.DesignSystemTitle(source)
		}
		if forkName == "" {
			forkName = strings.TrimSpace(source.Basename)
		}
		if forkName == "" {
			forkName = "Forked Design System"
		}
		forkSlug := simpleAssetCandidateSlug(strings.TrimSpace(slug))
		if forkSlug == "" {
			forkSlug = simpleAssetCandidateSlug(forkName)
		}
		if forkSlug == "" {
			forkSlug = fmt.Sprintf("official-%s", source.Basename)
		}
		forkSlug = fmt.Sprintf("%s-fork-%d", forkSlug, time.Now().UnixNano())
		profileJSON, err := json.Marshal(map[string]interface{}{
			"designSystemMarkdown": source.Body,
			"forkedFrom":           source.Basename,
			"forkedFromSource":     "official",
		})
		if err != nil {
			return fmt.Errorf("fork official design system: marshal profile: %w", err)
		}
		candidate := workagentModel.ArtifactAssetCandidate{
			UID:         uid,
			AssetKind:   workagentModel.ArtifactAssetKindDesignSystem,
			Name:        forkName,
			Slug:        forkSlug,
			ProfileJSON: string(profileJSON),
			Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
			TargetKind:  workagentModel.ArtifactAssetCandidateTargetDesignSystem,
		}
		if err := tx.Create(&candidate).Error; err != nil {
			return fmt.Errorf("fork official design system: create candidate: %w", err)
		}
		forked = workagentModel.ProjectDesignSystem{
			UID:         uid,
			ProjectID:   projectID,
			CandidateID: candidate.Id,
			Name:        forkName,
			Slug:        forkSlug,
			Basename:    designSystemCandidateBasename(candidate.Id, forkSlug),
			Title:       forkName,
			DerivedFrom: strings.TrimSpace(source.Basename),
			Version:     1,
			Body:        source.Body,
			Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
		}
		if err := tx.Create(&forked).Error; err != nil {
			return fmt.Errorf("fork official design system: create fork: %w", err)
		}
		if err := tx.Model(&candidate).Updates(map[string]interface{}{
			"target_kind": workagentModel.ArtifactAssetCandidateTargetDesignSystem,
			"target_id":   forked.Id,
		}).Error; err != nil {
			return fmt.Errorf("fork official design system: update candidate target: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &forked, nil
}

func designSystemCatalogItemFromProjectRow(row workagentModel.ProjectDesignSystem) skills.DesignSystemCatalogItem {
	return designSystemCatalogItemFromProjectRowForRole(row, model.GlobalProjectRoleOwner)
}

func designSystemCatalogItemFromProjectRowForRole(row workagentModel.ProjectDesignSystem, role string) skills.DesignSystemCatalogItem {
	status := strings.TrimSpace(row.Status)
	permissions := projectDesignSystemPermissions(status, role)
	return skills.DesignSystemCatalogItem{
		Basename:       strings.TrimSpace(row.Basename),
		Title:          strings.TrimSpace(row.Title),
		DerivedFrom:    strings.TrimSpace(row.DerivedFrom),
		Body:           row.Body,
		Source:         "project",
		ProjectID:      row.ProjectID,
		DesignSystemID: row.Id,
		ThreadID:       row.ThreadID,
		ArtifactID:     row.ArtifactID,
		CandidateID:    row.CandidateID,
		Status:         status,
		Version:        formatProjectDesignSystemVersion(row.Version),
		ReadOnly:       false,
		Permissions:    permissions,
		ReviewedBy:     row.ReviewedBy,
		ReviewedAt:     row.ReviewedAt,
		ReviewNote:     strings.TrimSpace(row.ReviewNote),
	}
}

func projectDesignSystemPermissions(status string, role string) []string {
	role = strings.TrimSpace(role)
	if role == "" {
		role = model.GlobalProjectRoleOwner
	}
	if status == workagentModel.ArtifactAssetCandidateStatusDraft {
		if role == model.GlobalProjectRoleOwner {
			return []string{"confirm", "reject", "archive"}
		}
		return nil
	}
	if status == workagentModel.ProjectDesignSystemStatusRejected {
		if role == model.GlobalProjectRoleOwner {
			return []string{"archive"}
		}
		return nil
	}
	if status != workagentModel.ArtifactAssetCandidateStatusConfirmed {
		return nil
	}
	switch role {
	case model.GlobalProjectRoleOwner:
		return []string{"use", "fork", "archive"}
	case model.GlobalProjectRoleEditor:
		return []string{"use", "fork"}
	case model.GlobalProjectRoleViewer, model.GlobalProjectRoleCommenter:
		return []string{"use"}
	default:
		return nil
	}
}

func projectDesignSystemHistoryItemFromRow(row workagentModel.ProjectDesignSystem) ProjectDesignSystemHistoryItem {
	return ProjectDesignSystemHistoryItem{
		DesignSystemID: row.Id,
		ProjectID:      row.ProjectID,
		Basename:       strings.TrimSpace(row.Basename),
		Title:          strings.TrimSpace(row.Title),
		DerivedFrom:    strings.TrimSpace(row.DerivedFrom),
		Version:        formatProjectDesignSystemVersion(row.Version),
		Status:         strings.TrimSpace(row.Status),
		Source:         "project",
		ReviewedBy:     row.ReviewedBy,
		ReviewedAt:     row.ReviewedAt,
		ReviewNote:     strings.TrimSpace(row.ReviewNote),
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func officialDesignSystemHistoryRoot(knownBasenames map[string]bool) *ProjectDesignSystemHistoryItem {
	for basename := range knownBasenames {
		if strings.TrimSpace(basename) == "" {
			continue
		}
		ds, err := skills.LoadDesignSystem(basename)
		if err != nil || ds == nil {
			continue
		}
		return &ProjectDesignSystemHistoryItem{
			Basename: strings.TrimSpace(ds.Basename),
			Title:    skills.DesignSystemTitle(ds),
			Version:  "shipped",
			Status:   workagentModel.ArtifactAssetCandidateStatusConfirmed,
			Source:   "official",
		}
	}
	return nil
}

func projectDesignSystemParentBody(row workagentModel.ProjectDesignSystem, byBasename map[string][]workagentModel.ProjectDesignSystem) string {
	derivedFrom := strings.TrimSpace(row.DerivedFrom)
	if derivedFrom == "" {
		return ""
	}
	if parents := byBasename[derivedFrom]; len(parents) > 0 {
		for i := len(parents) - 1; i >= 0; i-- {
			if parents[i].Id != row.Id {
				return parents[i].Body
			}
		}
	}
	if ds, err := skills.LoadDesignSystem(derivedFrom); err == nil && ds != nil {
		return ds.Body
	}
	return ""
}

func summarizeProjectDesignSystemDiff(previous string, next string) string {
	previousSections := designSystemMarkdownSections(previous)
	nextSections := designSystemMarkdownSections(next)
	if len(previousSections) == 0 || len(nextSections) == 0 {
		return ""
	}
	changed := make([]string, 0)
	added := make([]string, 0)
	removed := make([]string, 0)
	for name, nextBody := range nextSections {
		previousBody, ok := previousSections[name]
		if !ok {
			added = append(added, name)
			continue
		}
		if normalizeDesignSystemSectionBody(previousBody) != normalizeDesignSystemSectionBody(nextBody) {
			changed = append(changed, name)
		}
	}
	for name := range previousSections {
		if _, ok := nextSections[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	parts := make([]string, 0, 3)
	if len(changed) > 0 {
		parts = append(parts, "changed: "+strings.Join(changed, ", "))
	}
	if len(added) > 0 {
		parts = append(parts, "added: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed: "+strings.Join(removed, ", "))
	}
	if len(parts) == 0 {
		return "no section changes"
	}
	return strings.Join(parts, "; ")
}

func designSystemMarkdownSections(markdown string) map[string]string {
	sections := map[string]string{}
	current := ""
	var body []string
	flush := func() {
		if current == "" {
			return
		}
		sections[current] = strings.Join(body, "\n")
		body = nil
	}
	for _, line := range strings.Split(markdown, "\n") {
		if name, ok := designSystemSectionHeading(line); ok {
			flush()
			current = name
			continue
		}
		if current != "" {
			body = append(body, line)
		}
	}
	flush()
	return sections
}

func designSystemSectionHeading(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "## ") {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
	name = strings.TrimLeftFunc(name, func(r rune) bool {
		return unicode.IsDigit(r) || r == '.' || unicode.IsSpace(r)
	})
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	return name, true
}

func normalizeDesignSystemSectionBody(body string) string {
	lines := make([]string, 0)
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.Join(strings.Fields(line), " ")
		if trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return strings.Join(lines, "\n")
}

func nextProjectDesignSystemVersion(sourceVersion int) int {
	if sourceVersion < 1 {
		return 2
	}
	return sourceVersion + 1
}

func formatProjectDesignSystemVersion(version int) string {
	if version < 1 {
		version = 1
	}
	return fmt.Sprintf("v%d", version)
}

func truncateProjectDesignSystemReviewNote(note string) string {
	note = strings.TrimSpace(note)
	const maxRunes = 1000
	runes := []rune(note)
	if len(runes) <= maxRunes {
		return note
	}
	return string(runes[:maxRunes])
}

func designSystemCatalogItemFromCandidate(row workagentModel.ArtifactAssetCandidate) skills.DesignSystemCatalogItem {
	title := strings.TrimSpace(row.Name)
	if title == "" {
		title = strings.TrimSpace(row.Slug)
	}
	if title == "" {
		title = fmt.Sprintf("Design System Candidate %d", row.Id)
	}
	slug := strings.TrimSpace(row.Slug)
	if slug == "" {
		slug = fmt.Sprintf("candidate-%d", row.Id)
	}
	basename := designSystemCandidateBasename(row.Id, slug)
	derivedFrom := fmt.Sprintf("artifact-%d", row.ArtifactID)
	return skills.DesignSystemCatalogItem{
		Basename:    basename,
		Title:       title,
		DerivedFrom: derivedFrom,
		Body:        renderDesignSystemCandidateBody(row, title, derivedFrom),
		Source:      "candidate",
		ThreadID:    row.ThreadID,
		ArtifactID:  row.ArtifactID,
		CandidateID: row.Id,
		Status:      strings.TrimSpace(row.Status),
		Version:     "candidate",
		ReadOnly:    false,
		Permissions: []string{"use"},
	}
}

func renderDesignSystemCandidateBody(row workagentModel.ArtifactAssetCandidate, title string, derivedFrom string) string {
	if body, ok := designSystemCandidateMarkdown(row); ok {
		return body
	}
	profile := strings.TrimSpace(row.ProfileJSON)
	if profile == "" {
		profile = "{}"
	}
	var parsed interface{}
	if err := json.Unmarshal([]byte(profile), &parsed); err == nil {
		if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			profile = string(pretty)
		}
	}
	return strings.Join([]string{
		"# " + title,
		"",
		"`derived_from: " + derivedFrom + "`",
		"",
		"## Source",
		fmt.Sprintf("- candidate_id: %d", row.Id),
		fmt.Sprintf("- artifact_id: artifact-%d", row.ArtifactID),
		fmt.Sprintf("- thread_id: %d", row.ThreadID),
		"",
		"## Extracted Design System Profile",
		"",
		"```json",
		profile,
		"```",
	}, "\n")
}

func designSystemCandidateMarkdown(row workagentModel.ArtifactAssetCandidate) (string, bool) {
	var profile map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(row.ProfileJSON)), &profile); err != nil {
		return "", false
	}
	for _, key := range []string{
		"designSystemMarkdown",
		"design_system_markdown",
		"designSystemBody",
		"design_system_body",
		"markdown",
		"body",
	} {
		if value, ok := profile[key].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return value, true
			}
		}
	}
	return "", false
}

func designSystemCandidateBasename(candidateID uint, slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		slug = fmt.Sprintf("candidate-%d", candidateID)
	}
	return fmt.Sprintf("project-candidate-%d-%s", candidateID, slug)
}

func parseAssetCandidateProfile(row workagentModel.ArtifactAssetCandidate) (map[string]interface{}, error) {
	profile := map[string]interface{}{}
	raw := strings.TrimSpace(row.ProfileJSON)
	if raw == "" {
		return profile, nil
	}
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return nil, fmt.Errorf("parse asset candidate profile: %w", err)
	}
	return profile, nil
}

func loadAssetCandidateThread(tx *gorm.DB, uid int, threadID uint) (*workagentModel.ChatThread, error) {
	var thread workagentModel.ChatThread
	if err := tx.Where("id = ? AND uid = ?", threadID, uid).First(&thread).Error; err != nil {
		return nil, fmt.Errorf("load asset candidate thread: %w", err)
	}
	return &thread, nil
}

func assetCandidateProjectID(thread *workagentModel.ChatThread) *uint64 {
	if thread == nil || thread.ProjectID == 0 {
		return nil
	}
	value := uint64(thread.ProjectID)
	return &value
}

func assetCandidateName(row workagentModel.ArtifactAssetCandidate, profile map[string]interface{}) string {
	for _, candidate := range []string{
		strings.TrimSpace(row.Name),
		assetCandidateString(profile, "name", "title", "displayName", "display_name"),
		strings.TrimSpace(row.Slug),
	} {
		if candidate != "" {
			return candidate
		}
	}
	return fmt.Sprintf("%s candidate %d", row.AssetKind, row.Id)
}

func assetCandidateSlug(row workagentModel.ArtifactAssetCandidate, profile map[string]interface{}, name string) string {
	for _, candidate := range []string{
		strings.TrimSpace(row.Slug),
		assetCandidateString(profile, "slug", "id", "key"),
		name,
	} {
		if slug := simpleAssetCandidateSlug(candidate); slug != "" {
			return slug
		}
	}
	return fmt.Sprintf("%s-%d", strings.ReplaceAll(row.AssetKind, "_", "-"), row.Id)
}

func simpleAssetCandidateSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func assetCandidateLang(profile map[string]interface{}) string {
	lang := assetCandidateString(profile, "lang", "locale", "language")
	if lang == "" {
		return "en"
	}
	return lang
}

func assetCandidateString(profile map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := profile[key]
		if !ok {
			continue
		}
		if str, ok := value.(string); ok {
			if str = strings.TrimSpace(str); str != "" {
				return str
			}
		}
	}
	return ""
}

func assetCandidateStringOrJSON(profile map[string]interface{}, keys ...string) string {
	if str := assetCandidateString(profile, keys...); str != "" {
		return str
	}
	for _, key := range keys {
		value, ok := profile[key]
		if !ok || value == nil {
			continue
		}
		if raw, err := json.Marshal(value); err == nil && string(raw) != "null" {
			return string(raw)
		}
	}
	return ""
}

func assetCandidateJSONMap(profile map[string]interface{}, keys ...string) model.JSONMap {
	for _, key := range keys {
		value, ok := profile[key]
		if !ok || value == nil {
			continue
		}
		if typed, ok := value.(map[string]interface{}); ok && len(typed) > 0 {
			return model.JSONMap(typed)
		}
	}
	return nil
}

func isTypedAssetCandidateKind(kind string) bool {
	switch kind {
	case workagentModel.ArtifactAssetKindBrand,
		workagentModel.ArtifactAssetKindCharacter,
		workagentModel.ArtifactAssetKindProduct,
		workagentModel.ArtifactAssetKindDirectorStyle:
		return true
	default:
		return false
	}
}

func marshalAssetCandidateProfile(profile map[string]interface{}) (string, error) {
	if len(profile) == 0 {
		return "{}", nil
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return "", fmt.Errorf("marshal asset candidate profile: %w", err)
	}
	return string(raw), nil
}

func validArtifactAssetCandidateStatus(status string) bool {
	switch status {
	case workagentModel.ArtifactAssetCandidateStatusDraft,
		workagentModel.ArtifactAssetCandidateStatusConfirmed,
		workagentModel.ArtifactAssetCandidateStatusRejected:
		return true
	default:
		return false
	}
}

func validArtifactAssetKind(kind string) bool {
	switch kind {
	case workagentModel.ArtifactAssetKindBrand,
		workagentModel.ArtifactAssetKindCharacter,
		workagentModel.ArtifactAssetKindProduct,
		workagentModel.ArtifactAssetKindDirectorStyle,
		workagentModel.ArtifactAssetKindPromptAsset,
		workagentModel.ArtifactAssetKindDesignSystem,
		workagentModel.ArtifactAssetKindReference:
		return true
	default:
		return false
	}
}
