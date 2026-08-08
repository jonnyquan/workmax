package workagent

import (
	"fmt"
	"strings"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

type SkillAccessRequestRepository struct {
	db *gorm.DB
}

func NewSkillAccessRequestRepository(db *gorm.DB) *SkillAccessRequestRepository {
	if db == nil {
		db = globals.GraDBs["system"]
	}
	return &SkillAccessRequestRepository{db: db}
}

func (r *SkillAccessRequestRepository) UpsertPending(uid int, agentMode string, source string, reason string) (*workagentModel.SkillAccessRequest, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("skill access request: nil repository")
	}
	agentMode = strings.TrimSpace(agentMode)
	if uid == 0 || agentMode == "" {
		return nil, fmt.Errorf("skill access request: invalid identity")
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "official"
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 512 {
		reason = reason[:512]
	}
	var row workagentModel.SkillAccessRequest
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("uid = ? AND agent_mode = ?", uid, agentMode).First(&row).Error; err != nil {
			if err != gorm.ErrRecordNotFound {
				return fmt.Errorf("load skill access request: %w", err)
			}
			row = workagentModel.SkillAccessRequest{
				UID:       uid,
				AgentMode: agentMode,
				Source:    source,
				Status:    workagentModel.SkillAccessRequestStatusPending,
				Reason:    reason,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("create skill access request: %w", err)
			}
			return nil
		}
		updates := map[string]interface{}{
			"source": source,
			"reason": reason,
		}
		if row.Status == "" || row.Status == workagentModel.SkillAccessRequestStatusRejected {
			updates["status"] = workagentModel.SkillAccessRequestStatusPending
		}
		if err := tx.Model(&row).Updates(updates).Error; err != nil {
			return fmt.Errorf("update skill access request: %w", err)
		}
		return tx.First(&row, row.Id).Error
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *SkillAccessRequestRepository) ListForUser(uid int) (map[string]workagentModel.SkillAccessRequest, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("skill access request: nil repository")
	}
	if uid == 0 {
		return map[string]workagentModel.SkillAccessRequest{}, nil
	}
	var rows []workagentModel.SkillAccessRequest
	if err := r.db.Where("uid = ?", uid).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list skill access requests: %w", err)
	}
	out := make(map[string]workagentModel.SkillAccessRequest, len(rows))
	for _, row := range rows {
		if mode := strings.TrimSpace(row.AgentMode); mode != "" {
			out[mode] = row
		}
	}
	return out, nil
}

func (r *SkillAccessRequestRepository) List(status string, limit int) ([]workagentModel.SkillAccessRequest, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("skill access request: nil repository")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := r.db.Model(&workagentModel.SkillAccessRequest{})
	if status = strings.TrimSpace(status); status != "" {
		query = query.Where("status = ?", status)
	}
	var rows []workagentModel.SkillAccessRequest
	if err := query.Order("updated_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list skill access requests: %w", err)
	}
	return rows, nil
}

func (r *SkillAccessRequestRepository) UpdateStatus(requestID uint, status string, reviewedBy int, reviewNote string) (*workagentModel.SkillAccessRequest, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("skill access request: nil repository")
	}
	if requestID == 0 {
		return nil, fmt.Errorf("skill access request: invalid request id")
	}
	status = strings.TrimSpace(status)
	if status != workagentModel.SkillAccessRequestStatusPending &&
		status != workagentModel.SkillAccessRequestStatusApproved &&
		status != workagentModel.SkillAccessRequestStatusRejected {
		return nil, fmt.Errorf("skill access request: invalid status %s", status)
	}
	var row workagentModel.SkillAccessRequest
	note := truncateSkillAccessReviewNote(reviewNote)
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&row, requestID).Error; err != nil {
			return fmt.Errorf("load skill access request: %w", err)
		}
		now := time.Now().UTC()
		if err := tx.Model(&row).Updates(map[string]interface{}{
			"status":      status,
			"reviewed_by": reviewedBy,
			"reviewed_at": now,
			"review_note": note,
		}).Error; err != nil {
			return fmt.Errorf("update skill access request: %w", err)
		}
		return tx.First(&row, requestID).Error
	})
	if err != nil {
		return nil, err
	}
	if err := r.db.First(&row, requestID).Error; err != nil {
		return nil, fmt.Errorf("reload skill access request: %w", err)
	}
	return &row, nil
}

func truncateSkillAccessReviewNote(note string) string {
	note = strings.TrimSpace(note)
	const maxRunes = 512
	runes := []rune(note)
	if len(runes) <= maxRunes {
		return note
	}
	return string(runes[:maxRunes])
}
