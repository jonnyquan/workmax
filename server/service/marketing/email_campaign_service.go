package marketing

import (
	"server/globals"
	"server/model"
	"time"
)

type EmailCampaignService struct{}

// GetCampaignList 获取活动列表
func (s *EmailCampaignService) GetCampaignList(page, pageSize int, status string, keyword string) ([]model.EmailCampaign, int64, error) {
	var campaigns []model.EmailCampaign
	var total int64

	db := globals.GraDBs["system"].Model(&model.EmailCampaign{})

	// 筛选条件
	if status != "" {
		db = db.Where("status = ?", status)
	}
	if keyword != "" {
		db = db.Where("name LIKE ?", "%"+keyword+"%")
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	offset := (page - 1) * pageSize
	if err := db.Order("id DESC").Offset(offset).Limit(pageSize).Find(&campaigns).Error; err != nil {
		return nil, 0, err
	}

	return campaigns, total, nil
}

// GetCampaignByID 根据ID获取活动
func (s *EmailCampaignService) GetCampaignByID(id int) (*model.EmailCampaign, error) {
	var campaign model.EmailCampaign
	if err := globals.GraDBs["system"].Where("id = ?", id).First(&campaign).Error; err != nil {
		return nil, err
	}
	return &campaign, nil
}

// CreateCampaign 创建活动
func (s *EmailCampaignService) CreateCampaign(campaign *model.EmailCampaign) error {
	return globals.GraDBs["system"].Create(campaign).Error
}

// UpdateCampaign 更新活动
func (s *EmailCampaignService) UpdateCampaign(campaign *model.EmailCampaign) error {
	return globals.GraDBs["system"].Model(&model.EmailCampaign{}).Where("id = ?", campaign.Id).Updates(campaign).Error
}

// DeleteCampaign 删除活动
func (s *EmailCampaignService) DeleteCampaign(id int) error {
	return globals.GraDBs["system"].Where("id = ?", id).Delete(&model.EmailCampaign{}).Error
}

// UpdateCampaignStatus 更新活动状态
func (s *EmailCampaignService) UpdateCampaignStatus(id int, status string) error {
	updates := map[string]interface{}{
		"status": status,
	}

	if status == model.EMAIL_CAMPAIGN_STATUS_RUNNING {
		updates["started_at"] = time.Now()
	} else if status == model.EMAIL_CAMPAIGN_STATUS_COMPLETED {
		updates["completed_at"] = time.Now()
	}

	return globals.GraDBs["system"].Model(&model.EmailCampaign{}).Where("id = ?", id).Updates(updates).Error
}

// IncrementCampaignStats 增加活动统计数据
func (s *EmailCampaignService) IncrementCampaignStats(campaignID int, field string) error {
	return globals.GraDBs["system"].Model(&model.EmailCampaign{}).
		Where("id = ?", campaignID).
		UpdateColumn(field, globals.GraDBs["system"].Raw(field+" + ?", 1)).Error
}

// GetScheduledCampaigns 获取待执行的定时活动
func (s *EmailCampaignService) GetScheduledCampaigns() ([]model.EmailCampaign, error) {
	var campaigns []model.EmailCampaign
	now := time.Now()

	err := globals.GraDBs["system"].Where("status = ? AND schedule_type = ? AND schedule_time <= ?",
		model.EMAIL_CAMPAIGN_STATUS_SCHEDULED,
		model.EMAIL_CAMPAIGN_SCHEDULE_SCHEDULED,
		now,
	).Find(&campaigns).Error

	return campaigns, err
}

// GetCampaignStats 获取活动统计数据
func (s *EmailCampaignService) GetCampaignStats(campaignID int) (map[string]interface{}, error) {
	campaign, err := s.GetCampaignByID(campaignID)
	if err != nil {
		return nil, err
	}

	stats := map[string]interface{}{
		"totalRecipients":  campaign.TotalRecipients,
		"sentCount":        campaign.SentCount,
		"deliveredCount":   campaign.DeliveredCount,
		"failedCount":      campaign.FailedCount,
		"openCount":        campaign.OpenCount,
		"clickCount":       campaign.ClickCount,
		"unsubscribeCount": campaign.UnsubscribeCount,
	}

	// 计算百分比
	if campaign.SentCount > 0 {
		stats["deliveryRate"] = float64(campaign.DeliveredCount) / float64(campaign.SentCount) * 100
		stats["openRate"] = float64(campaign.OpenCount) / float64(campaign.SentCount) * 100
		stats["clickRate"] = float64(campaign.ClickCount) / float64(campaign.SentCount) * 100
		stats["unsubscribeRate"] = float64(campaign.UnsubscribeCount) / float64(campaign.SentCount) * 100
	}

	return stats, nil
}
