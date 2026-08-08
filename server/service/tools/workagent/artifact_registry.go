package workagent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

type ArtifactRegistryRepository struct {
	db *gorm.DB
}

func NewArtifactRegistryRepository(db *gorm.DB) *ArtifactRegistryRepository {
	if db == nil {
		db = globals.GraDBs["system"]
	}
	return &ArtifactRegistryRepository{db: db}
}

func (r *ArtifactRegistryRepository) UpsertFromThreadFile(file *workagentModel.ThreadFile) (*workagentModel.ArtifactRegistry, error) {
	if file == nil || file.Id == 0 {
		return nil, fmt.Errorf("upsert artifact: empty thread file")
	}
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("upsert artifact: nil repository")
	}

	view := ArtifactViewFromThreadFile(*file)
	key := artifactVersionKey(view)
	targets, _ := json.Marshal(view.ExportTargets)
	designSystem := selectedDesignSystemRef(r.db, file.ThreadID)

	var existing workagentModel.ArtifactRegistry
	err := r.db.Where("thread_file_id = ?", file.Id).First(&existing).Error
	if err == nil {
		updates := map[string]interface{}{
			"artifact_key":   key,
			"name":           view.Name,
			"display_name":   view.DisplayName,
			"artifact_type":  view.ArtifactType,
			"output_type":    view.OutputType,
			"preview_type":   view.PreviewType,
			"export_targets": string(targets),
			"source":         view.Source,
			"file_hash":      view.FileHash,
		}
		if designSystem.Basename != "" && existing.DesignSystemBasename == "" {
			updates["design_system_basename"] = designSystem.Basename
			updates["design_system_title"] = designSystem.Title
			updates["design_system_derived_from"] = designSystem.DerivedFrom
		}
		if err := r.db.Model(&existing).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("update artifact registry: %w", err)
		}
		if err := r.db.First(&existing, existing.Id).Error; err != nil {
			return nil, fmt.Errorf("reload artifact registry: %w", err)
		}
		return &existing, nil
	}
	if err != nil && !isRecordNotFound(err) {
		return nil, fmt.Errorf("find artifact registry: %w", err)
	}

	version, parentID, err := r.nextVersion(file.UID, file.ThreadID, key)
	if err != nil {
		return nil, err
	}
	row := workagentModel.ArtifactRegistry{
		UID:                  file.UID,
		ThreadID:             file.ThreadID,
		ThreadFileID:         file.Id,
		ArtifactKey:          key,
		Name:                 view.Name,
		DisplayName:          view.DisplayName,
		ArtifactType:         view.ArtifactType,
		OutputType:           view.OutputType,
		PreviewType:          view.PreviewType,
		ExportTargets:        string(targets),
		Version:              version,
		Status:               view.Status,
		ReviewState:          view.ReviewState,
		DesignSystemBasename: designSystem.Basename,
		DesignSystemTitle:    designSystem.Title,
		DesignSystemDerived:  designSystem.DerivedFrom,
		Source:               view.Source,
		ParentArtifactID:     parentID,
		ArtifactRelation:     defaultArtifactRelation(parentID),
		FileHash:             view.FileHash,
	}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create artifact registry: %w", err)
	}
	emitArtifactCreatedMetric(row, isGormTransactionDB(r.db))
	return &row, nil
}

func emitArtifactCreatedMetric(row workagentModel.ArtifactRegistry, asyncPersistent bool) {
	EmitMetric("wa_artifact_created", map[string]any{
		"uid":                 row.UID,
		"thread_id":           row.ThreadID,
		"artifact_id":         row.Id,
		"thread_file_id":      row.ThreadFileID,
		"artifact_type":       row.ArtifactType,
		"output_type":         row.OutputType,
		"preview_type":        row.PreviewType,
		"source":              row.Source,
		"version":             row.Version,
		"status":              row.Status,
		"export_target_count": len(parseArtifactExportTargets(row.ExportTargets, nil)),
		"async_persistent":    asyncPersistent,
	})
}

func isGormTransactionDB(db *gorm.DB) bool {
	if db == nil || db.Statement == nil || db.Statement.ConnPool == nil {
		return false
	}
	_, ok := db.Statement.ConnPool.(*sql.Tx)
	return ok
}

func (r *ArtifactRegistryRepository) ListByThreadFileIDs(uid int, threadID uint, fileIDs []uint) (map[uint]workagentModel.ArtifactRegistry, error) {
	out := make(map[uint]workagentModel.ArtifactRegistry, len(fileIDs))
	if r == nil || r.db == nil || uid == 0 || threadID == 0 || len(fileIDs) == 0 {
		return out, nil
	}
	var rows []workagentModel.ArtifactRegistry
	if err := r.db.Where("uid = ? AND thread_id = ? AND thread_file_id IN ?", uid, threadID, fileIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list artifact registry: %w", err)
	}
	for _, row := range rows {
		out[row.ThreadFileID] = row
	}
	return out, nil
}

func (r *ArtifactRegistryRepository) LatestArtifactIDForThreadFiles(uid int, threadID uint, fileIDs []uint) (uint, error) {
	if r == nil || r.db == nil || uid == 0 || threadID == 0 || len(fileIDs) == 0 {
		return 0, nil
	}
	var row workagentModel.ArtifactRegistry
	err := r.db.
		Where("uid = ? AND thread_id = ? AND thread_file_id IN ?", uid, threadID, fileIDs).
		Order("id DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find latest artifact for thread files: %w", err)
	}
	return row.Id, nil
}

func (r *ArtifactRegistryRepository) LoadForOwnerWithThreadFile(uid int, threadID uint, artifactID uint) (*workagentModel.ArtifactRegistry, *workagentModel.ThreadFile, error) {
	if r == nil || r.db == nil || uid == 0 || threadID == 0 || artifactID == 0 {
		return nil, nil, gorm.ErrRecordNotFound
	}
	var row workagentModel.ArtifactRegistry
	if err := r.db.Where("id = ? AND uid = ? AND thread_id = ?", artifactID, uid, threadID).First(&row).Error; err != nil {
		return nil, nil, err
	}
	var file workagentModel.ThreadFile
	if err := r.db.Where("id = ? AND uid = ? AND thread_id = ?", row.ThreadFileID, uid, threadID).First(&file).Error; err != nil {
		return nil, nil, err
	}
	return &row, &file, nil
}

func (r *ArtifactRegistryRepository) UpdateLifecycle(uid int, threadID uint, artifactID uint, status string, reviewState string) (*workagentModel.ArtifactRegistry, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("update artifact lifecycle: nil repository")
	}
	status = strings.TrimSpace(status)
	reviewState = strings.TrimSpace(reviewState)
	if !validArtifactStatus(status) {
		return nil, fmt.Errorf("invalid artifact status: %s", status)
	}
	if reviewState != "" && !validArtifactReviewState(reviewState) {
		return nil, fmt.Errorf("invalid artifact review state: %s", reviewState)
	}
	updates := map[string]interface{}{"status": status}
	if reviewState != "" {
		updates["review_state"] = reviewState
	}
	var row workagentModel.ArtifactRegistry
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND uid = ? AND thread_id = ?", artifactID, uid, threadID).First(&row).Error; err != nil {
			return fmt.Errorf("load artifact: %w", err)
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return fmt.Errorf("update artifact: %w", err)
		}
		if err := tx.First(&row, row.Id).Error; err != nil {
			return fmt.Errorf("reload artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *ArtifactRegistryRepository) UpdateLifecycleForThreadFiles(uid int, threadID uint, fileIDs []uint, status string, reviewState string) (int, error) {
	return r.UpdateLifecycleForThreadFilesWithReview(uid, threadID, fileIDs, status, reviewState, "", "")
}

func (r *ArtifactRegistryRepository) LinkThreadFilesToParent(uid int, threadID uint, fileIDs []uint, parentArtifactID uint) (int, error) {
	return r.LinkThreadFilesToParentWithRelation(uid, threadID, fileIDs, parentArtifactID, workagentModel.ArtifactRelationRevision)
}

func (r *ArtifactRegistryRepository) LinkThreadFilesToParentWithRelation(uid int, threadID uint, fileIDs []uint, parentArtifactID uint, relation string) (int, error) {
	if r == nil || r.db == nil || len(fileIDs) == 0 || parentArtifactID == 0 {
		return 0, nil
	}
	relation = strings.TrimSpace(relation)
	if relation != "" && !validArtifactRelation(relation) {
		return 0, fmt.Errorf("invalid artifact relation: %s", relation)
	}
	var parent workagentModel.ArtifactRegistry
	if err := r.db.Where("id = ? AND uid = ? AND thread_id = ?", parentArtifactID, uid, threadID).First(&parent).Error; err != nil {
		return 0, fmt.Errorf("load parent artifact: %w", err)
	}
	tx := r.db.Model(&workagentModel.ArtifactRegistry{}).
		Where("uid = ? AND thread_id = ? AND thread_file_id IN ?", uid, threadID, fileIDs).
		Updates(map[string]interface{}{
			"parent_artifact_id": parentArtifactID,
			"artifact_relation":  relation,
		})
	if tx.Error != nil {
		return 0, fmt.Errorf("link artifact parent: %w", tx.Error)
	}
	return int(tx.RowsAffected), nil
}

func (r *ArtifactRegistryRepository) UpdateComparison(uid int, threadID uint, artifactID uint, source string, summary string, decision string) (*workagentModel.ArtifactRegistry, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("update artifact comparison: nil repository")
	}
	source = strings.TrimSpace(source)
	summary = strings.TrimSpace(summary)
	decision = strings.TrimSpace(strings.ToLower(decision))
	if source == "" {
		return nil, fmt.Errorf("comparison source is required")
	}
	if summary == "" {
		return nil, fmt.Errorf("comparison summary is required")
	}
	if decision != "" && !validArtifactComparisonDecision(decision) {
		return nil, fmt.Errorf("invalid comparison decision: %s", decision)
	}
	updates := map[string]interface{}{
		"comparison_source":  source,
		"comparison_summary": summary,
	}
	if decision != "" {
		updates["comparison_decision"] = decision
	}
	var row workagentModel.ArtifactRegistry
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND uid = ? AND thread_id = ?", artifactID, uid, threadID).First(&row).Error; err != nil {
			return fmt.Errorf("load artifact: %w", err)
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return fmt.Errorf("update artifact comparison: %w", err)
		}
		if err := tx.First(&row, row.Id).Error; err != nil {
			return fmt.Errorf("reload artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *ArtifactRegistryRepository) UpdateHTMLPreviewDiagnostics(uid int, threadID uint, artifactID uint, diagnostics []ArtifactHTMLValidationIssue) (*workagentModel.ArtifactRegistry, error) {
	return r.updateHTMLPreviewDiagnostics(uid, threadID, artifactID, diagnostics, true)
}

func (r *ArtifactRegistryRepository) ReplaceHTMLPreviewDiagnostics(uid int, threadID uint, artifactID uint, diagnostics []ArtifactHTMLValidationIssue) (*workagentModel.ArtifactRegistry, error) {
	return r.updateHTMLPreviewDiagnostics(uid, threadID, artifactID, diagnostics, false)
}

func (r *ArtifactRegistryRepository) updateHTMLPreviewDiagnostics(uid int, threadID uint, artifactID uint, diagnostics []ArtifactHTMLValidationIssue, mergeExisting bool) (*workagentModel.ArtifactRegistry, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("update html preview diagnostics: nil repository")
	}
	var row workagentModel.ArtifactRegistry
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND uid = ? AND thread_id = ?", artifactID, uid, threadID).First(&row).Error; err != nil {
			return fmt.Errorf("load artifact: %w", err)
		}
		normalized := normalizeHTMLPreviewDiagnostics(diagnostics)
		if mergeExisting {
			normalized = mergeHTMLPreviewDiagnostics(parseHTMLPreviewDiagnostics(row.HTMLPreviewDiagnostics), diagnostics)
		}
		raw, err := json.Marshal(normalized)
		if err != nil {
			return fmt.Errorf("encode html preview diagnostics: %w", err)
		}
		if err := tx.Model(&row).Update("html_preview_diagnostics", string(raw)).Error; err != nil {
			return fmt.Errorf("update html preview diagnostics: %w", err)
		}
		if err := tx.First(&row, row.Id).Error; err != nil {
			return fmt.Errorf("reload artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func mergeHTMLPreviewDiagnostics(existing []ArtifactHTMLValidationIssue, incoming []ArtifactHTMLValidationIssue) []ArtifactHTMLValidationIssue {
	merged := make([]ArtifactHTMLValidationIssue, 0, len(existing)+len(incoming))
	merged = append(merged, existing...)
	merged = append(merged, incoming...)
	return normalizeHTMLPreviewDiagnostics(merged)
}

func normalizeHTMLPreviewDiagnostics(in []ArtifactHTMLValidationIssue) []ArtifactHTMLValidationIssue {
	const maxPreviewDiagnostics = 50
	out := make([]ArtifactHTMLValidationIssue, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		code := normalizeDiagnosticToken(item.Code, "preview_diagnostic")
		severity := normalizeHTMLPreviewSeverity(item.Severity)
		message := strings.TrimSpace(item.Message)
		if message == "" {
			continue
		}
		if len(message) > 500 {
			message = message[:500]
		}
		key := code + "\x00" + severity + "\x00" + message
		if seen[key] {
			continue
		}
		seen[key] = true
		source := strings.TrimSpace(item.Source)
		if source == "" {
			source = "preview_runtime"
		}
		out = append(out, ArtifactHTMLValidationIssue{
			Code:     code,
			Severity: severity,
			Message:  message,
			Source:   source,
		})
		if len(out) >= maxPreviewDiagnostics {
			break
		}
	}
	return out
}

func normalizeDiagnosticToken(value string, fallback string) string {
	clean := strings.TrimSpace(strings.ToLower(value))
	clean = strings.ReplaceAll(clean, "-", "_")
	clean = strings.ReplaceAll(clean, " ", "_")
	if clean == "" {
		return fallback
	}
	var b strings.Builder
	for _, r := range clean {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

func normalizeHTMLPreviewSeverity(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "error", "block":
		return "error"
	case "warn", "warning":
		return "warn"
	default:
		return "info"
	}
}

func (r *ArtifactRegistryRepository) ApplyComparisonDecision(uid int, threadID uint, artifactID uint, decision string) (*workagentModel.ArtifactRegistry, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("apply comparison decision: nil repository")
	}
	decision = strings.TrimSpace(strings.ToLower(decision))
	if !validArtifactComparisonDecision(decision) {
		return nil, fmt.Errorf("invalid comparison decision: %s", decision)
	}
	var row workagentModel.ArtifactRegistry
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND uid = ? AND thread_id = ?", artifactID, uid, threadID).First(&row).Error; err != nil {
			return fmt.Errorf("load artifact: %w", err)
		}
		updates := map[string]interface{}{
			"comparison_decision": decision,
		}
		switch decision {
		case "keep":
			updates["status"] = workagentModel.ArtifactStatusFinal
			updates["review_state"] = workagentModel.ArtifactReviewApproved
		case "revise":
			updates["status"] = workagentModel.ArtifactStatusChangesRequested
			updates["review_state"] = workagentModel.ArtifactReviewChangesRequested
		case "rollback":
			updates["status"] = workagentModel.ArtifactStatusChangesRequested
			updates["review_state"] = workagentModel.ArtifactReviewChangesRequested
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return fmt.Errorf("update artifact decision: %w", err)
		}
		if decision == "rollback" && row.ParentArtifactID != 0 {
			if err := tx.Model(&workagentModel.ArtifactRegistry{}).
				Where("id = ? AND uid = ? AND thread_id = ?", row.ParentArtifactID, uid, threadID).
				Updates(map[string]interface{}{
					"status":       workagentModel.ArtifactStatusFinal,
					"review_state": workagentModel.ArtifactReviewApproved,
				}).Error; err != nil {
				return fmt.Errorf("approve rollback parent: %w", err)
			}
		}
		if err := tx.First(&row, row.Id).Error; err != nil {
			return fmt.Errorf("reload artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	emitArtifactComparisonDecisionMetric(row, decision)
	return &row, nil
}

func emitArtifactComparisonDecisionMetric(row workagentModel.ArtifactRegistry, decision string) {
	adoptedArtifactID := uint(0)
	switch decision {
	case "keep":
		adoptedArtifactID = row.Id
	case "rollback":
		adoptedArtifactID = row.ParentArtifactID
	}
	EmitMetric("wa_artifact_comparison_decision", map[string]any{
		"uid":                 row.UID,
		"thread_id":           row.ThreadID,
		"artifact_id":         row.Id,
		"parent_artifact_id":  row.ParentArtifactID,
		"adopted_artifact_id": adoptedArtifactID,
		"thread_file_id":      row.ThreadFileID,
		"artifact_type":       row.ArtifactType,
		"output_type":         row.OutputType,
		"preview_type":        row.PreviewType,
		"source":              row.ComparisonSource,
		"decision":            decision,
		"status":              row.Status,
		"review_state":        row.ReviewState,
	})
}

func (r *ArtifactRegistryRepository) UpdateLifecycleForThreadFilesWithReview(uid int, threadID uint, fileIDs []uint, status string, reviewState string, reviewSource string, reviewSummary string) (int, error) {
	return r.UpdateLifecycleForThreadFilesWithReviewDetails(uid, threadID, fileIDs, status, reviewState, reviewSource, reviewSummary, "")
}

func (r *ArtifactRegistryRepository) UpdateLifecycleForThreadFilesWithReviewDetails(uid int, threadID uint, fileIDs []uint, status string, reviewState string, reviewSource string, reviewSummary string, reviewDetailsJSON string) (int, error) {
	if r == nil || r.db == nil || len(fileIDs) == 0 {
		return 0, nil
	}
	status = strings.TrimSpace(status)
	reviewState = strings.TrimSpace(reviewState)
	reviewSource = strings.TrimSpace(reviewSource)
	reviewSummary = strings.TrimSpace(reviewSummary)
	reviewDetailsJSON = strings.TrimSpace(reviewDetailsJSON)
	if !validArtifactStatus(status) {
		return 0, fmt.Errorf("invalid artifact status: %s", status)
	}
	if reviewState != "" && !validArtifactReviewState(reviewState) {
		return 0, fmt.Errorf("invalid artifact review state: %s", reviewState)
	}
	if reviewDetailsJSON != "" && !json.Valid([]byte(reviewDetailsJSON)) {
		return 0, fmt.Errorf("invalid review details JSON")
	}
	updates := map[string]interface{}{"status": status}
	if reviewState != "" {
		updates["review_state"] = reviewState
	}
	if reviewSource != "" {
		updates["review_source"] = reviewSource
	}
	if reviewSummary != "" {
		updates["review_summary"] = reviewSummary
	}
	if reviewDetailsJSON != "" {
		updates["review_details_json"] = reviewDetailsJSON
	}
	tx := r.db.Model(&workagentModel.ArtifactRegistry{}).
		Where("uid = ? AND thread_id = ? AND thread_file_id IN ?", uid, threadID, fileIDs).
		Updates(updates)
	if tx.Error != nil {
		return 0, fmt.Errorf("update artifact lifecycle for files: %w", tx.Error)
	}
	count := int(tx.RowsAffected)
	if count > 0 && reviewSource != "" {
		emitArtifactReviewDecisionMetric(uid, threadID, fileIDs, status, reviewState, reviewSource, count)
	}
	if count > 0 && reviewDetailsJSON != "" {
		r.emitArtifactRedoReviewDeltaMetrics(uid, threadID, fileIDs, reviewSource)
	}
	return count, nil
}

func emitArtifactReviewDecisionMetric(uid int, threadID uint, fileIDs []uint, status string, reviewState string, reviewSource string, count int) {
	EmitMetric("wa_artifact_review_decision", map[string]any{
		"uid":                  uid,
		"thread_id":            threadID,
		"source":               reviewSource,
		"status":               status,
		"review_state":         reviewState,
		"artifact_count":       count,
		"thread_file_id_count": len(fileIDs),
	})
}

func (r *ArtifactRegistryRepository) emitArtifactRedoReviewDeltaMetrics(uid int, threadID uint, fileIDs []uint, reviewSource string) {
	if r == nil || r.db == nil || len(fileIDs) == 0 {
		return
	}
	var children []workagentModel.ArtifactRegistry
	if err := r.db.
		Where("uid = ? AND thread_id = ? AND thread_file_id IN ? AND parent_artifact_id <> 0", uid, threadID, fileIDs).
		Find(&children).Error; err != nil {
		return
	}
	for _, child := range children {
		childDetails := parseArtifactReviewDetails(child.ReviewDetailsJSON)
		if childDetails == nil {
			continue
		}
		var parent workagentModel.ArtifactRegistry
		if err := r.db.Where("id = ? AND uid = ? AND thread_id = ?", child.ParentArtifactID, uid, threadID).First(&parent).Error; err != nil {
			continue
		}
		parentDetails := parseArtifactReviewDetails(parent.ReviewDetailsJSON)
		if parentDetails == nil {
			continue
		}
		improved, regressed := countReviewDimensionDeltas(parentDetails, childDetails)
		EmitMetric("wa_artifact_redo_review_delta", map[string]any{
			"uid":                  uid,
			"thread_id":            threadID,
			"source":               reviewSource,
			"artifact_id":          child.Id,
			"parent_artifact_id":   child.ParentArtifactID,
			"thread_file_id":       child.ThreadFileID,
			"artifact_type":        child.ArtifactType,
			"output_type":          child.OutputType,
			"status":               child.Status,
			"review_state":         child.ReviewState,
			"overall_score_before": parentDetails.OverallScore,
			"overall_score_after":  childDetails.OverallScore,
			"overall_score_delta":  childDetails.OverallScore - parentDetails.OverallScore,
			"improved_dimensions":  improved,
			"regressed_dimensions": regressed,
		})
	}
}

func countReviewDimensionDeltas(parent *ArtifactReviewDetails, child *ArtifactReviewDetails) (int, int) {
	if parent == nil || child == nil {
		return 0, 0
	}
	improved := 0
	regressed := 0
	for name, childDimension := range child.Dimensions {
		parentDimension, ok := parent.Dimensions[name]
		if !ok {
			continue
		}
		switch {
		case childDimension.Score > parentDimension.Score:
			improved++
		case childDimension.Score < parentDimension.Score:
			regressed++
		}
	}
	return improved, regressed
}

func (r *ArtifactRegistryRepository) nextVersion(uid int, threadID uint, key string) (int, uint, error) {
	var latest workagentModel.ArtifactRegistry
	err := r.db.
		Where("uid = ? AND thread_id = ? AND artifact_key = ?", uid, threadID, key).
		Order("version DESC, id DESC").
		First(&latest).Error
	if err == nil {
		return latest.Version + 1, latest.Id, nil
	}
	if isRecordNotFound(err) {
		return 1, 0, nil
	}
	return 0, 0, fmt.Errorf("find latest artifact version: %w", err)
}

func ArtifactViewFromRegistryAndThreadFile(row workagentModel.ArtifactRegistry, file workagentModel.ThreadFile) ArtifactView {
	view := ArtifactViewFromThreadFile(file)
	view.ID = "artifact-" + strconv.FormatUint(uint64(row.Id), 10)
	view.ArtifactType = nonEmptyString(row.ArtifactType, view.ArtifactType)
	view.OutputType = nonEmptyString(row.OutputType, view.OutputType)
	view.PreviewType = nonEmptyString(row.PreviewType, view.PreviewType)
	view.Preview = inferPreviewCapability(view.PreviewType)
	view.ExportTargets = parseArtifactExportTargets(row.ExportTargets, view.ExportTargets)
	view.Version = row.Version
	view.Status = nonEmptyString(row.Status, view.Status)
	view.ReviewState = nonEmptyString(row.ReviewState, view.ReviewState)
	view.ReviewSource = row.ReviewSource
	view.ReviewSummary = row.ReviewSummary
	view.ReviewDetails = parseArtifactReviewDetails(row.ReviewDetailsJSON)
	view.ComparisonSource = row.ComparisonSource
	view.ComparisonSummary = row.ComparisonSummary
	view.ComparisonDecision = row.ComparisonDecision
	view.DesignSystemBasename = row.DesignSystemBasename
	view.DesignSystemTitle = row.DesignSystemTitle
	view.DesignSystemDerivedFrom = row.DesignSystemDerived
	if diagnostics := parseHTMLPreviewDiagnostics(row.HTMLPreviewDiagnostics); len(diagnostics) > 0 {
		if view.HTMLValidation == nil {
			view.HTMLValidation = &ArtifactHTMLValidationResult{Status: ArtifactHTMLValidationPass}
		}
		view.HTMLValidation.PreviewDiagnostics = diagnostics
		view.HTMLValidation.Status = mergeHTMLValidationStatusWithPreviewDiagnostics(view.HTMLValidation.Status, diagnostics)
	}
	view.Source = row.Source
	view.FileHash = nonEmptyString(row.FileHash, view.FileHash)
	if row.ParentArtifactID != 0 {
		view.ParentArtifactID = "artifact-" + strconv.FormatUint(uint64(row.ParentArtifactID), 10)
	}
	view.ArtifactRelation = row.ArtifactRelation
	return view
}

func mergeHTMLValidationStatusWithPreviewDiagnostics(status string, diagnostics []ArtifactHTMLValidationIssue) string {
	if status == "" {
		status = ArtifactHTMLValidationPass
	}
	if status == ArtifactHTMLValidationBlock {
		return status
	}
	hasWarn := status == ArtifactHTMLValidationWarn
	for _, diagnostic := range diagnostics {
		switch diagnostic.Severity {
		case "block", "error":
			return ArtifactHTMLValidationBlock
		case "warn":
			hasWarn = true
		}
	}
	if hasWarn {
		return ArtifactHTMLValidationWarn
	}
	return status
}

func parseArtifactReviewDetails(raw string) *ArtifactReviewDetails {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var details ArtifactReviewDetails
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		return nil
	}
	return &details
}

func parseHTMLPreviewDiagnostics(raw string) []ArtifactHTMLValidationIssue {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var diagnostics []ArtifactHTMLValidationIssue
	if err := json.Unmarshal([]byte(raw), &diagnostics); err != nil {
		return nil
	}
	return normalizeHTMLPreviewDiagnostics(diagnostics)
}

func parseArtifactExportTargets(raw string, fallback []string) []string {
	var targets []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &targets); err == nil && len(targets) > 0 {
		return targets
	}
	return fallback
}

func nonEmptyString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultArtifactRelation(parentID uint) string {
	if parentID == 0 {
		return ""
	}
	return workagentModel.ArtifactRelationRevision
}

func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func validArtifactStatus(status string) bool {
	switch status {
	case workagentModel.ArtifactStatusDraft,
		workagentModel.ArtifactStatusReference,
		workagentModel.ArtifactStatusSystem,
		workagentModel.ArtifactStatusNeedsReview,
		workagentModel.ArtifactStatusApproved,
		workagentModel.ArtifactStatusFinal,
		workagentModel.ArtifactStatusChangesRequested,
		workagentModel.ArtifactStatusExported:
		return true
	default:
		return false
	}
}

func validArtifactReviewState(state string) bool {
	switch state {
	case workagentModel.ArtifactReviewNone,
		workagentModel.ArtifactReviewNeedsReview,
		workagentModel.ArtifactReviewApproved,
		workagentModel.ArtifactReviewChangesRequested:
		return true
	default:
		return false
	}
}

func validArtifactComparisonDecision(decision string) bool {
	switch decision {
	case "keep", "revise", "rollback":
		return true
	default:
		return false
	}
}

func validArtifactRelation(relation string) bool {
	switch relation {
	case workagentModel.ArtifactRelationRevision,
		workagentModel.ArtifactRelationComparisonReport:
		return true
	default:
		return false
	}
}
