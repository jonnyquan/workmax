package workagent

import (
	"encoding/json"
	"fmt"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

type ArtifactExportJobRepository struct {
	db *gorm.DB
}

type ArtifactExportJobInput struct {
	UID          int
	ThreadID     uint
	ArtifactID   uint
	ThreadFileID uint
	Plan         ArtifactHTMLExportJobPlan
}

func NewArtifactExportJobRepository(db *gorm.DB) *ArtifactExportJobRepository {
	if db == nil {
		db = globals.GraDBs["system"]
	}
	return &ArtifactExportJobRepository{db: db}
}

func (r *ArtifactExportJobRepository) CreateFromHTMLJobPlan(input ArtifactExportJobInput) (*workagentModel.ArtifactExportJob, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("create artifact export job: nil repository")
	}
	if input.UID == 0 || input.ThreadID == 0 || input.ArtifactID == 0 || input.ThreadFileID == 0 {
		return nil, fmt.Errorf("create artifact export job: missing owner or artifact identity")
	}
	target := strings.TrimSpace(strings.ToLower(input.Plan.Target))
	if target == "" || !IsSupportedHTMLExportTarget(target) {
		return nil, fmt.Errorf("unsupported HTML export target: %s", target)
	}
	if target == "html" || target == "zip" {
		return nil, fmt.Errorf("target %s does not require an async export job", target)
	}
	status, reason := exportJobStatusFromPlan(input.Plan)
	prerequisites, _ := json.Marshal(input.Plan.Prerequisites)
	planJSON, _ := json.Marshal(input.Plan)
	row := workagentModel.ArtifactExportJob{
		UID:               input.UID,
		ThreadID:          input.ThreadID,
		ArtifactID:        input.ArtifactID,
		ThreadFileID:      input.ThreadFileID,
		Target:            target,
		Kind:              input.Plan.Kind,
		Worker:            input.Plan.Worker,
		Status:            status,
		Reason:            reason,
		OutputExtension:   input.Plan.OutputExtension,
		PrerequisitesJSON: string(prerequisites),
		PlanJSON:          string(planJSON),
	}
	if err := r.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("create artifact export job: %w", err)
	}
	emitArtifactExportJobMetric(row, "created")
	return &row, nil
}

func (r *ArtifactExportJobRepository) LoadForOwner(uid int, threadID uint, jobID uint) (*workagentModel.ArtifactExportJob, error) {
	if r == nil || r.db == nil || uid == 0 || threadID == 0 || jobID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var row workagentModel.ArtifactExportJob
	if err := r.db.Where("id = ? AND uid = ? AND thread_id = ?", jobID, uid, threadID).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *ArtifactExportJobRepository) ClaimNext(worker string) (*workagentModel.ArtifactExportJob, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("claim artifact export job: nil repository")
	}
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return nil, fmt.Errorf("claim artifact export job: worker is required")
	}
	var row workagentModel.ArtifactExportJob
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("worker = ? AND status = ?", worker, workagentModel.ArtifactExportJobStatusQueued).
			Order("id ASC").
			First(&row).Error; err != nil {
			return err
		}
		if err := tx.Model(&workagentModel.ArtifactExportJob{}).
			Where("id = ? AND status = ?", row.Id, workagentModel.ArtifactExportJobStatusQueued).
			Updates(map[string]interface{}{
				"status": workagentModel.ArtifactExportJobStatusRunning,
				"reason": "",
			}).Error; err != nil {
			return err
		}
		return tx.First(&row, row.Id).Error
	})
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claim artifact export job: %w", err)
	}
	emitArtifactExportJobMetric(row, "claimed")
	return &row, nil
}

func (r *ArtifactExportJobRepository) MarkSucceeded(jobID uint, outputFileID uint, outputPath string) (*workagentModel.ArtifactExportJob, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("mark artifact export job succeeded: nil repository")
	}
	if jobID == 0 {
		return nil, fmt.Errorf("mark artifact export job succeeded: job id is required")
	}
	outputPath = strings.TrimSpace(outputPath)
	var row workagentModel.ArtifactExportJob
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&row, jobID).Error; err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]interface{}{
			"status":         workagentModel.ArtifactExportJobStatusSucceeded,
			"reason":         "",
			"output_file_id": outputFileID,
			"output_path":    outputPath,
			"error_message":  "",
		}).Error; err != nil {
			return err
		}
		return tx.First(&row, row.Id).Error
	})
	if err != nil {
		return nil, fmt.Errorf("mark artifact export job succeeded: %w", err)
	}
	emitArtifactExportJobMetric(row, "succeeded")
	return &row, nil
}

func (r *ArtifactExportJobRepository) MarkFailed(jobID uint, reason string, message string) (*workagentModel.ArtifactExportJob, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("mark artifact export job failed: nil repository")
	}
	if jobID == 0 {
		return nil, fmt.Errorf("mark artifact export job failed: job id is required")
	}
	reason = strings.TrimSpace(reason)
	message = strings.TrimSpace(message)
	if reason == "" {
		reason = "missing_failure_reason"
	}
	var row workagentModel.ArtifactExportJob
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&row, jobID).Error; err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]interface{}{
			"status":        workagentModel.ArtifactExportJobStatusFailed,
			"reason":        reason,
			"error_message": message,
		}).Error; err != nil {
			return err
		}
		return tx.First(&row, row.Id).Error
	})
	if err != nil {
		return nil, fmt.Errorf("mark artifact export job failed: %w", err)
	}
	emitArtifactExportJobMetric(row, "failed")
	return &row, nil
}

func exportJobStatusFromPlan(plan ArtifactHTMLExportJobPlan) (string, string) {
	switch plan.Status {
	case ArtifactHTMLExportJobReady, ArtifactHTMLExportJobWorkerPending:
		return workagentModel.ArtifactExportJobStatusQueued, plan.Reason
	case ArtifactHTMLExportJobBlocked, ArtifactHTMLExportJobUnsupported:
		reason := plan.Reason
		if reason == "" {
			reason = plan.Status
		}
		return workagentModel.ArtifactExportJobStatusBlocked, reason
	default:
		reason := plan.Reason
		if reason == "" {
			reason = "invalid_job_plan"
		}
		return workagentModel.ArtifactExportJobStatusBlocked, reason
	}
}

func emitArtifactExportJobMetric(row workagentModel.ArtifactExportJob, eventStatus string) {
	EmitMetric("wa_artifact_export_job", map[string]any{
		"uid":            row.UID,
		"thread_id":      row.ThreadID,
		"artifact_id":    row.ArtifactID,
		"thread_file_id": row.ThreadFileID,
		"job_id":         row.Id,
		"target":         row.Target,
		"worker":         row.Worker,
		"kind":           row.Kind,
		"status":         row.Status,
		"event_status":   eventStatus,
		"reason":         row.Reason,
	})
}
