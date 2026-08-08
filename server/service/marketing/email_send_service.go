package marketing

import (
	"fmt"
	"server/globals"
	"server/model"
	"server/utils"
	"strings"
	"time"
)

type EmailSendService struct{}

// SendEmail 发送单封邮件
func (s *EmailSendService) SendEmail(templateID int, uid int, variables map[string]string) error {
	// 获取用户信息
	var user model.User
	if err := globals.GraDBs["system"].Where("id = ?", uid).First(&user).Error; err != nil {
		return fmt.Errorf("user not found: %w", err)
	}

	// 检查是否退订
	if s.IsUnsubscribed(uid) {
		return fmt.Errorf("user has unsubscribed")
	}

	// 获取模板
	templateService := &EmailTemplateService{}
	template, err := templateService.GetTemplateByID(templateID)
	if err != nil {
		return fmt.Errorf("template not found: %w", err)
	}

	// 合并默认变量和自定义变量
	allVariables := s.MergeDefaultVariables(user, variables)

	// 渲染模板
	subject := s.RenderTemplate(template.Subject, allVariables)
	content := s.RenderTemplate(template.Content, allVariables)

	// 创建发送记录
	record := &model.EmailSendRecord{
		TemplateID: templateID,
		UID:        uid,
		Email:      user.Email,
		Subject:    subject,
		Status:     model.EMAIL_SEND_STATUS_PENDING,
	}
	if err := globals.GraDBs["system"].Create(record).Error; err != nil {
		return err
	}

	// 发送邮件
	err = utils.SendEmail(user.Email, subject, content)
	if err != nil {
		// 更新记录为失败
		globals.GraDBs["system"].Model(record).Updates(map[string]interface{}{
			"status":    model.EMAIL_SEND_STATUS_FAILED,
			"error_msg": err.Error(),
			"sent_at":   time.Now(),
		})
		return err
	}

	// 更新记录为已发送
	globals.GraDBs["system"].Model(record).Updates(map[string]interface{}{
		"status":  model.EMAIL_SEND_STATUS_SENT,
		"sent_at": time.Now(),
	})

	return nil
}

// SendCampaignEmails 批量发送活动邮件
func (s *EmailSendService) SendCampaignEmails(campaignID int) error {
	campaignService := &EmailCampaignService{}
	campaign, err := campaignService.GetCampaignByID(campaignID)
	if err != nil {
		return err
	}

	// 获取目标用户
	var users []model.User
	if campaign.SegmentID > 0 {
		segmentService := &EmailSegmentService{}
		users, err = segmentService.GetSegmentUsers(campaign.SegmentID)
		if err != nil {
			return err
		}
	} else {
		// 发送给所有活跃用户
		globals.GraDBs["system"].Where("ban = ? AND email != ?", false, "").Find(&users)
	}

	// 更新活动状态
	campaignService.UpdateCampaignStatus(campaignID, model.EMAIL_CAMPAIGN_STATUS_RUNNING)

	// 更新总接收人数
	globals.GraDBs["system"].Model(&model.EmailCampaign{}).Where("id = ?", campaignID).Update("total_recipients", len(users))

	// 获取模板 (只获取一次，避免N+1)
	templateService := &EmailTemplateService{}
	template, err := templateService.GetTemplateByID(campaign.TemplateID)
	if err != nil {
		return err
	}

	// 提前获取所有退订用户 (避免N+1)
	var userIDs []int
	for _, u := range users {
		userIDs = append(userIDs, int(u.Id))
	}
	var unsubscribed []model.EmailUnsubscribe
	unsubMap := make(map[int]bool)
	if len(userIDs) > 0 {
		globals.GraDBs["system"].Where("uid IN ?", userIDs).Find(&unsubscribed)
		for _, u := range unsubscribed {
			unsubMap[u.UID] = true
		}
	}

	// 批量发送
	successCount := 0
	failedCount := 0

	for _, user := range users {
		// 检查是否退订
		if unsubMap[int(user.Id)] {
			continue
		}

		err := s.sendEmailWithCampaignData(&user, template, campaignID, nil)
		if err != nil {
			failedCount++
			campaignService.IncrementCampaignStats(campaignID, "failed_count")
		} else {
			successCount++
			campaignService.IncrementCampaignStats(campaignID, "sent_count")
		}

		// 限流：每秒发送不超过10封
		time.Sleep(100 * time.Millisecond)
	}

	// 更新活动状态为已完成
	campaignService.UpdateCampaignStatus(campaignID, model.EMAIL_CAMPAIGN_STATUS_COMPLETED)

	return nil
}

// SendEmailWithCampaign 发送带活动ID的邮件 (保留原始接口以兼容旧代码)
func (s *EmailSendService) SendEmailWithCampaign(templateID int, uid int, campaignID int, variables map[string]string) error {
	var user model.User
	if err := globals.GraDBs["system"].Where("id = ?", uid).First(&user).Error; err != nil {
		return fmt.Errorf("user not found: %w", err)
	}
	templateService := &EmailTemplateService{}
	template, err := templateService.GetTemplateByID(templateID)
	if err != nil {
		return fmt.Errorf("template not found: %w", err)
	}
	return s.sendEmailWithCampaignData(&user, template, campaignID, variables)
}

// sendEmailWithCampaignData 内部实现，接受用户实体和模板，避免重复查库
func (s *EmailSendService) sendEmailWithCampaignData(user *model.User, template *model.EmailTemplate, campaignID int, variables map[string]string) error {
	// 合并默认变量和自定义变量
	allVariables := s.MergeDefaultVariables(*user, variables)

	// 渲染模板
	subject := s.RenderTemplate(template.Subject, allVariables)
	content := s.RenderTemplate(template.Content, allVariables)

	// 添加追踪和退订链接
	content = s.AddTrackingPixel(content, int(user.Id), campaignID)
	content = s.AddUnsubscribeLink(content, int(user.Id))

	// 创建发送记录
	record := &model.EmailSendRecord{
		CampaignID: &campaignID,
		TemplateID: int(template.Id),
		UID:        int(user.Id),
		Email:      user.Email,
		Subject:    subject,
		Status:     model.EMAIL_SEND_STATUS_PENDING,
	}
	if err := globals.GraDBs["system"].Create(record).Error; err != nil {
		return err
	}

	// 发送邮件
	err := utils.SendEmail(user.Email, subject, content)
	if err != nil {
		// 更新记录为失败
		globals.GraDBs["system"].Model(record).Updates(map[string]interface{}{
			"status":    model.EMAIL_SEND_STATUS_FAILED,
			"error_msg": err.Error(),
			"sent_at":   time.Now(),
		})
		return err
	}

	// 更新记录为已发送
	globals.GraDBs["system"].Model(record).Updates(map[string]interface{}{
		"status":  model.EMAIL_SEND_STATUS_SENT,
		"sent_at": time.Now(),
	})

	return nil
}

// MergeDefaultVariables 合并默认变量和自定义变量
func (s *EmailSendService) MergeDefaultVariables(user model.User, customVariables map[string]string) map[string]string {
	// 准备 Dashboard URL
	dashboardURL := globals.GraConf.System.FrontendURL
	if dashboardURL == "" {
		dashboardURL = "https://workmax.app"
	}
	if !strings.HasSuffix(dashboardURL, "/") {
		dashboardURL = dashboardURL + "/"
	}
	dashboardURL = dashboardURL + "dashboard"

	// 默认变量
	defaultVars := map[string]string{
		"nickname":     user.Nickname,
		"email":        user.Email,
		"dashboardUrl": dashboardURL,
	}

	// 如果没有自定义变量，直接返回默认变量
	if customVariables == nil {
		return defaultVars
	}

	// 合并变量（自定义变量优先级更高）
	result := make(map[string]string)
	for k, v := range defaultVars {
		result[k] = v
	}
	for k, v := range customVariables {
		result[k] = v
	}

	return result
}

// RenderTemplate 渲染模板变量
func (s *EmailSendService) RenderTemplate(template string, variables map[string]string) string {
	result := template
	for key, value := range variables {
		placeholder := "{{" + key + "}}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

// AddTrackingPixel 添加打开追踪像素
func (s *EmailSendService) AddTrackingPixel(content string, uid int, campaignID int) string {
	backendURL := globals.GraConf.System.BackendURL
	trackingURL := fmt.Sprintf("%s/api/email/track/open?uid=%d&cid=%d", backendURL, uid, campaignID)
	pixel := fmt.Sprintf(`<img src="%s" width="1" height="1" alt="" style="display:none" />`, trackingURL)
	return content + pixel
}

// AddUnsubscribeLink 添加退订链接
func (s *EmailSendService) AddUnsubscribeLink(content string, uid int) string {
	backendURL := globals.GraConf.System.BackendURL
	unsubscribeURL := fmt.Sprintf("%s/api/email/unsubscribe?uid=%d", backendURL, uid)
	link := fmt.Sprintf(`<div style="text-align:center;margin-top:20px;font-size:12px;color:#999;">
		<a href="%s" style="color:#999;">Unsubscribe from these emails</a>
	</div>`, unsubscribeURL)
	return content + link
}

// GetSendRecordList 获取发送记录列表
func (s *EmailSendService) GetSendRecordList(page, pageSize int, campaignID *int, status string, keyword string) ([]model.EmailSendRecord, int64, error) {
	var records []model.EmailSendRecord
	var total int64

	db := globals.GraDBs["system"].Model(&model.EmailSendRecord{})

	// 筛选条件
	if campaignID != nil {
		db = db.Where("campaign_id = ?", *campaignID)
	}
	if status != "" {
		db = db.Where("status = ?", status)
	}
	if keyword != "" {
		db = db.Where("email LIKE ? OR subject LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	offset := (page - 1) * pageSize
	if err := db.Order("id DESC").Offset(offset).Limit(pageSize).Find(&records).Error; err != nil {
		return nil, 0, err
	}

	return records, total, nil
}

// RecordEmailOpen 记录邮件打开
func (s *EmailSendService) RecordEmailOpen(uid int, campaignID int, ip string, userAgent string) error {
	// 查找发送记录
	var record model.EmailSendRecord
	query := globals.GraDBs["system"].Where("uid = ?", uid)
	if campaignID > 0 {
		query = query.Where("campaign_id = ?", campaignID)
	}
	if err := query.Order("id DESC").First(&record).Error; err != nil {
		return err
	}

	// 更新打开记录
	updates := map[string]interface{}{
		"status":     model.EMAIL_SEND_STATUS_OPENED,
		"open_count": globals.GraDBs["system"].Raw("open_count + ?", 1),
		"ip":         ip,
		"user_agent": userAgent,
	}

	// 首次打开记录时间
	if record.OpenedAt.IsZero() {
		updates["opened_at"] = time.Now()
	}

	if err := globals.GraDBs["system"].Model(&record).Updates(updates).Error; err != nil {
		return err
	}

	// 更新活动统计
	if campaignID > 0 {
		campaignService := &EmailCampaignService{}
		campaignService.IncrementCampaignStats(campaignID, "open_count")
	}

	return nil
}

// RecordEmailClick 记录邮件点击
func (s *EmailSendService) RecordEmailClick(uid int, campaignID int, ip string, userAgent string) error {
	// 查找发送记录
	var record model.EmailSendRecord
	query := globals.GraDBs["system"].Where("uid = ?", uid)
	if campaignID > 0 {
		query = query.Where("campaign_id = ?", campaignID)
	}
	if err := query.Order("id DESC").First(&record).Error; err != nil {
		return err
	}

	// 更新点击记录
	updates := map[string]interface{}{
		"status":      model.EMAIL_SEND_STATUS_CLICKED,
		"click_count": globals.GraDBs["system"].Raw("click_count + ?", 1),
		"ip":          ip,
		"user_agent":  userAgent,
	}

	// 首次点击记录时间
	if record.ClickedAt.IsZero() {
		updates["clicked_at"] = time.Now()
	}

	if err := globals.GraDBs["system"].Model(&record).Updates(updates).Error; err != nil {
		return err
	}

	// 更新活动统计
	if campaignID > 0 {
		campaignService := &EmailCampaignService{}
		campaignService.IncrementCampaignStats(campaignID, "click_count")
	}

	return nil
}

// IsUnsubscribed 检查用户是否已退订
func (s *EmailSendService) IsUnsubscribed(uid int) bool {
	var count int64
	globals.GraDBs["system"].Model(&model.EmailUnsubscribe{}).Where("uid = ?", uid).Count(&count)
	return count > 0
}

// Unsubscribe 退订
func (s *EmailSendService) Unsubscribe(uid int, reason string, campaignID int, ip string) error {
	// 获取用户信息
	var user model.User
	if err := globals.GraDBs["system"].Where("id = ?", uid).First(&user).Error; err != nil {
		return err
	}

	// 创建退订记录
	var campaignIDPtr *int
	if campaignID > 0 {
		campaignIDPtr = &campaignID
	}
	unsubscribe := &model.EmailUnsubscribe{
		UID:        uid,
		Email:      user.Email,
		Reason:     reason,
		CampaignID: campaignIDPtr,
		IP:         ip,
	}

	if err := globals.GraDBs["system"].Create(unsubscribe).Error; err != nil {
		return err
	}

	// 更新活动退订统计
	if campaignID > 0 {
		campaignService := &EmailCampaignService{}
		campaignService.IncrementCampaignStats(campaignID, "unsubscribe_count")
	}

	return nil
}
