package marketing

import (
	"fmt"
	"server/globals"
	"server/model"
	"server/utils"
	"time"
)

type EmailAutomationService struct{}

// GetAutomationRuleList 获取自动化规则列表
func (s *EmailAutomationService) GetAutomationRuleList(page, pageSize int, status *int, triggerType string) ([]model.EmailAutomationRule, int64, error) {
	var rules []model.EmailAutomationRule
	var total int64

	db := globals.GraDBs["system"].Model(&model.EmailAutomationRule{})

	// 筛选条件
	if status != nil {
		db = db.Where("status = ?", *status)
	}
	if triggerType != "" {
		db = db.Where("trigger_type = ?", triggerType)
	}

	// 获取总数
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 分页查询
	offset := (page - 1) * pageSize
	if err := db.Order("priority DESC, id DESC").Offset(offset).Limit(pageSize).Find(&rules).Error; err != nil {
		return nil, 0, err
	}

	return rules, total, nil
}

// GetAutomationRuleByID 根据ID获取规则
func (s *EmailAutomationService) GetAutomationRuleByID(id int) (*model.EmailAutomationRule, error) {
	var rule model.EmailAutomationRule
	if err := globals.GraDBs["system"].Where("id = ?", id).First(&rule).Error; err != nil {
		return nil, err
	}
	return &rule, nil
}

// CreateAutomationRule 创建规则
func (s *EmailAutomationService) CreateAutomationRule(rule *model.EmailAutomationRule) error {
	return globals.GraDBs["system"].Create(rule).Error
}

// UpdateAutomationRule 更新规则
func (s *EmailAutomationService) UpdateAutomationRule(rule *model.EmailAutomationRule) error {
	return globals.GraDBs["system"].Model(&model.EmailAutomationRule{}).Where("id = ?", rule.Id).Updates(rule).Error
}

// DeleteAutomationRule 删除规则
func (s *EmailAutomationService) DeleteAutomationRule(id int) error {
	return globals.GraDBs["system"].Where("id = ?", id).Delete(&model.EmailAutomationRule{}).Error
}

// UpdateAutomationRuleStatus 更新规则状态
func (s *EmailAutomationService) UpdateAutomationRuleStatus(id int, status int) error {
	return globals.GraDBs["system"].Model(&model.EmailAutomationRule{}).Where("id = ?", id).Update("status", status).Error
}

// TriggerAutomationByType 根据类型触发自动化
func (s *EmailAutomationService) TriggerAutomationByType(triggerType string, uid int, data map[string]string) error {
	// 获取启用的规则
	var rules []model.EmailAutomationRule
	if err := globals.GraDBs["system"].Where("trigger_type = ? AND status = ?", triggerType, model.EMAIL_AUTOMATION_STATUS_ENABLED).
		Order("priority DESC").Find(&rules).Error; err != nil {
		return err
	}

	sendService := &EmailSendService{}

	for _, rule := range rules {
		// 如果有延迟，需要使用调度器
		if rule.DelayMinutes > 0 {
			// TODO: 集成调度器
			continue
		}

		// 发送邮件
		err := sendService.SendEmail(rule.TemplateID, uid, data)
		if err != nil {
			continue
		}

		// 更新触发计数
		globals.GraDBs["system"].Model(&rule).Updates(map[string]interface{}{
			"trigger_count":     globals.GraDBs["system"].Raw("trigger_count + ?", 1),
			"last_trigger_time": time.Now(),
		})
	}

	return nil
}

// TriggerUserRegister 触发用户注册自动化
func (s *EmailAutomationService) TriggerUserRegister(uid int, nickname string, email string) error {
	data := map[string]string{
		"nickname": nickname,
		"email":    email,
	}
	return s.TriggerAutomationByType(model.EMAIL_AUTOMATION_TRIGGER_USER_REGISTER, uid, data)
}

// TriggerSubscriptionExpire 触发订阅即将过期自动化
func (s *EmailAutomationService) TriggerSubscriptionExpire(uid int, expiryDate string) error {
	data := map[string]string{
		"expiryDate": expiryDate,
	}
	return s.TriggerAutomationByType(model.EMAIL_AUTOMATION_TRIGGER_SUBSCRIPTION_EXPIRE, uid, data)
}

// TriggerUsageLimit 触发使用限制自动化
func (s *EmailAutomationService) TriggerUsageLimit(uid int, quotaType string, usagePercent string) error {
	data := map[string]string{
		"quotaType":    quotaType,
		"usagePercent": usagePercent,
	}
	return s.TriggerAutomationByType(model.EMAIL_AUTOMATION_TRIGGER_USAGE_LIMIT, uid, data)
}

// TriggerInactivity 触发不活跃用户召回
func (s *EmailAutomationService) TriggerInactivity(uid int, nickname string, lastLoginDate string, inactiveDays int) error {
	data := map[string]string{
		"nickname":      nickname,
		"lastLoginDate": lastLoginDate,
		"inactiveDays":  string(rune(inactiveDays)),
	}
	return s.TriggerAutomationByType(model.EMAIL_AUTOMATION_TRIGGER_INACTIVITY, uid, data)
}

// CheckAndTriggerInactiveUsers 检测并触发不活跃用户召回
// inactiveDays: 不活跃天数阈值（例如：7、30、90）
func (s *EmailAutomationService) CheckAndTriggerInactiveUsers(inactiveDays int) (int, error) {
	// 获取启用的规则 (一次性获取，避免N+1)
	var rules []model.EmailAutomationRule
	if err := globals.GraDBs["system"].Where("trigger_type = ? AND status = ?", model.EMAIL_AUTOMATION_TRIGGER_INACTIVITY, model.EMAIL_AUTOMATION_STATUS_ENABLED).
		Order("priority DESC").Find(&rules).Error; err != nil {
		return 0, err
	}

	if len(rules) == 0 {
		return 0, nil // 没有启用的规则
	}

	// 预先获取相关模板
	templateService := &EmailTemplateService{}
	templateMap := make(map[int]*model.EmailTemplate)
	for _, r := range rules {
		if r.DelayMinutes > 0 {
			continue // 暂时跳过延迟任务
		}
		if _, ok := templateMap[r.TemplateID]; !ok {
			template, err := templateService.GetTemplateByID(r.TemplateID)
			if err == nil {
				templateMap[r.TemplateID] = template
			}
		}
	}

	// 计算不活跃日期阈值
	thresholdDate := time.Now().AddDate(0, 0, -inactiveDays)

	// 查询不活跃用户
	var users []model.User
	err := globals.GraDBs["system"].
		Where("login_time < ? AND login_time IS NOT NULL", thresholdDate).
		Where("auth_email = ?", 1). // 只召回已验证邮箱的用户
		Find(&users).Error

	if err != nil || len(users) == 0 {
		return 0, err
	}

	// 批量获取退订和最近发送记录，防止N+1
	var userIDs []int
	for _, u := range users {
		userIDs = append(userIDs, int(u.Id))
	}

	unsubMap := make(map[int]bool)
	if len(userIDs) > 0 {
		var unsubscribed []model.EmailUnsubscribe
		globals.GraDBs["system"].Where("uid IN ?", userIDs).Find(&unsubscribed)
		for _, u := range unsubscribed {
			unsubMap[u.UID] = true
		}
	}

	recentCheckDate := time.Now().AddDate(0, 0, -7) // 7天内不重复发送
	var recentRecords []model.EmailSendRecord
	recordMap := make(map[int]bool)
	if len(userIDs) > 0 {
		globals.GraDBs["system"].
			Where("uid IN ? AND created_at > ?", userIDs, recentCheckDate).
			Where("subject LIKE ?", "%comeback%").
			Find(&recentRecords)
		for _, r := range recentRecords {
			recordMap[r.UID] = true
		}
	}

	sendService := &EmailSendService{}
	successCount := 0

	for _, user := range users {
		// 检查用户是否退订
		if unsubMap[int(user.Id)] {
			continue
		}

		// 检查最近是否已经发送过召回邮件（避免频繁骚扰）
		if recordMap[int(user.Id)] {
			continue
		}

		data := map[string]string{
			"nickname":      user.Nickname,
			"lastLoginDate": user.LoginTime.Format("2006-01-02"),
			"inactiveDays":  fmt.Sprint(inactiveDays),
		}

		userHandled := false
		for i, rule := range rules {
			if rule.DelayMinutes > 0 {
				continue
			}
			template, ok := templateMap[rule.TemplateID]
			if !ok {
				continue
			}

			// 直接发送，避免 SendEmail 内部的 N+1 (User, Template, IsUnsubscribed)
			allVariables := sendService.MergeDefaultVariables(user, data)
			subject := sendService.RenderTemplate(template.Subject, allVariables)
			content := sendService.RenderTemplate(template.Content, allVariables)

			record := &model.EmailSendRecord{
				TemplateID: rule.TemplateID,
				UID:        int(user.Id),
				Email:      user.Email,
				Subject:    subject,
				Status:     model.EMAIL_SEND_STATUS_PENDING,
			}
			globals.GraDBs["system"].Create(record)

			sendErr := utils.SendEmail(user.Email, subject, content)
			if sendErr != nil {
				globals.GraDBs["system"].Model(record).Updates(map[string]interface{}{
					"status":    model.EMAIL_SEND_STATUS_FAILED,
					"error_msg": sendErr.Error(),
					"sent_at":   time.Now(),
				})
				continue
			}

			globals.GraDBs["system"].Model(record).Updates(map[string]interface{}{
				"status":  model.EMAIL_SEND_STATUS_SENT,
				"sent_at": time.Now(),
			})

			// 更新触发计数
			globals.GraDBs["system"].Model(&rules[i]).Updates(map[string]interface{}{
				"trigger_count":     globals.GraDBs["system"].Raw("trigger_count + ?", 1),
				"last_trigger_time": time.Now(),
			})
			userHandled = true
		}

		if userHandled {
			successCount++
		}
	}

	return successCount, nil
}

// BatchCheckInactiveUsers 批量检测不同级别的不活跃用户
// 支持多个不活跃天数阈值（例如：7天、30天、90天）
func (s *EmailAutomationService) BatchCheckInactiveUsers(inactiveDaysList []int) (map[int]int, error) {
	results := make(map[int]int)

	for _, days := range inactiveDaysList {
		count, err := s.CheckAndTriggerInactiveUsers(days)
		if err != nil {
			return results, err
		}
		results[days] = count
	}

	return results, nil
}
