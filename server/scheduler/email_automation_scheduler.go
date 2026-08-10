package scheduler

import (
	"fmt"
	"server/globals"
	"server/model"
	"server/service"
	"time"

	"go.uber.org/zap"
)

// StartEmailAutomationScheduler 启动邮件自动化定时任务
func StartEmailAutomationScheduler() {
	globals.GraLog.Info("Starting email automation scheduler")

	// 启动用户引导邮件检查（每天凌晨1点）
	go checkOnboardingDay3()
	go checkOnboardingDay7()

	// 启动订阅到期检查（每天凌晨2点）
	go checkSubscriptionExpiry()

	// 启动不活跃用户检查（每天凌晨3点）
	go checkInactiveUsers()

	globals.GraLog.Info("Email automation scheduler started")
}

// checkSubscriptionExpiry 检查即将到期的订阅
func checkSubscriptionExpiry() {
	defer func() {
		if r := recover(); r != nil {
			globals.GraLog.Error("Subscription expiry scheduler panic recovered", zap.Any("panic", r))
			time.Sleep(5 * time.Second)
			go checkSubscriptionExpiry()
		}
	}()

	// 首次延迟到凌晨2点
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 2, 0, 0, 0, now.Location())
	time.Sleep(time.Until(next))

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// 立即执行一次
	runSubscriptionExpiryCheck()

	// 然后每24小时执行一次
	for range ticker.C {
		runSubscriptionExpiryCheck()
	}
}

func runSubscriptionExpiryCheck() {
	globals.GraLog.Info("Running subscription expiry check")

	now := time.Now()

	// 查询7天内到期的付费会员用户
	// member > MEMBER_SUBSCRIPTION_FREE: 是付费会员
	// （NONE=0 / FREE=1 都不是付费；PRO=2、ENTERPRISE=3 才是，见 model/user.go）
	// member_end_time BETWEEN now AND 7days: 7天内到期
	var expiringUsers []model.User
	sevenDaysLater := now.AddDate(0, 0, 7)

	err := globals.GraDBs["system"].
		Where("member > ?", model.MEMBER_SUBSCRIPTION_FREE).           // 付费会员（member > 1）
		Where("member_end_time BETWEEN ? AND ?", now, sevenDaysLater). // 7天内到期
		Where("ban = 0").                                              // 未封禁
		Limit(200).                                                    // 限制每次处理200个用户
		Find(&expiringUsers).Error

	if err != nil {
		globals.GraLog.Error("Failed to query expiring subscriptions", zap.Error(err))
		return
	}

	if len(expiringUsers) == 0 {
		globals.GraLog.Info("No expiring subscriptions found")
		return
	}

	automationService := service.GroupServiceApp.MarketingServiceGroup.EmailAutomationService

	// 分别处理7天和3天的提醒
	for _, user := range expiringUsers {
		daysUntilExpiry := int(time.Until(user.MemberEndTime).Hours() / 24)
		expiryDate := user.MemberEndTime.Format("2006-01-02")

		// 根据剩余天数触发不同优先级的规则
		go func(uid int, email, expiryDate string, daysLeft int) {
			if err := automationService.TriggerSubscriptionExpire(int(uid), expiryDate); err != nil {
				globals.GraLog.Error("Failed to trigger subscription expire",
					zap.Int("uid", uid),
					zap.String("email", email),
					zap.Int("daysLeft", daysLeft),
					zap.Error(err))
			} else {
				globals.GraLog.Info("Triggered subscription expiry reminder",
					zap.Int("uid", uid),
					zap.String("email", email),
					zap.Int("daysLeft", daysLeft))
			}
		}(int(user.Id), user.Email, expiryDate, daysUntilExpiry)

		// 避免瞬间发送太多邮件，每个用户之间间隔100ms
		time.Sleep(100 * time.Millisecond)
	}

	globals.GraLog.Info("Subscription expiry check completed",
		zap.Int("count", len(expiringUsers)))
}

// checkInactiveUsers 检查不活跃用户
func checkInactiveUsers() {
	defer func() {
		if r := recover(); r != nil {
			globals.GraLog.Error("Inactive users scheduler panic recovered", zap.Any("panic", r))
			time.Sleep(5 * time.Second)
			go checkInactiveUsers()
		}
	}()

	// 首次延迟到凌晨3点
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 3, 0, 0, 0, now.Location())
	time.Sleep(time.Until(next))

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// 立即执行一次
	runInactiveUsersCheck()

	// 然后每24小时执行一次
	for range ticker.C {
		runInactiveUsersCheck()
	}
}

func runInactiveUsersCheck() {
	globals.GraLog.Info("Running inactive users check")

	// 查询30天未登录的用户
	var users []model.User
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)

	err := globals.GraDBs["system"].
		Where("login_time < ?", thirtyDaysAgo).
		Where("ban = 0"). // 未封禁
		Limit(100).       // 限制每次处理100个用户
		Find(&users).Error

	if err != nil {
		globals.GraLog.Error("Failed to query inactive users", zap.Error(err))
		return
	}

	if len(users) == 0 {
		globals.GraLog.Info("No inactive users found")
		return
	}

	automationService := service.GroupServiceApp.MarketingServiceGroup.EmailAutomationService
	for _, user := range users {
		lastLoginDays := int(time.Since(user.LoginTime).Hours() / 24)
		data := map[string]string{
			"nickname":      user.Nickname,
			"email":         user.Email,
			"lastLoginDays": fmt.Sprintf("%d", lastLoginDays),
		}

		go func(uid int, triggerData map[string]string) {
			if err := automationService.TriggerAutomationByType(model.EMAIL_AUTOMATION_TRIGGER_INACTIVE_USER, uid, triggerData); err != nil {
				globals.GraLog.Error("Failed to trigger inactive user automation",
					zap.Int("uid", uid),
					zap.Error(err))
			}
		}(int(user.Id), data)

		// 避免瞬间发送太多邮件，每个用户之间间隔100ms
		time.Sleep(100 * time.Millisecond)
	}

	globals.GraLog.Info("Inactive users check completed", zap.Int("count", len(users)))
}

// checkOnboardingDay3 检查注册后第3天的用户
func checkOnboardingDay3() {
	defer func() {
		if r := recover(); r != nil {
			globals.GraLog.Error("Onboarding day 3 scheduler panic recovered", zap.Any("panic", r))
			time.Sleep(5 * time.Second)
			go checkOnboardingDay3()
		}
	}()

	// 首次延迟到凌晨1点
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 1, 0, 0, 0, now.Location())
	if now.Hour() >= 1 {
		// 如果当前时间已经超过凌晨1点，则延迟到明天凌晨1点
		next = next.AddDate(0, 0, 1)
	}
	time.Sleep(time.Until(next))

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// 立即执行一次
	runOnboardingDay3Check()

	// 然后每24小时执行一次
	for range ticker.C {
		runOnboardingDay3Check()
	}
}

func runOnboardingDay3Check() {
	globals.GraLog.Info("Running onboarding day 3 check")

	db := globals.GraDBs["system"]
	now := time.Now()
	threeDaysAgo := now.AddDate(0, 0, -3)

	// 查询3天前注册的用户（未封禁）
	var users []model.User
	err := db.Where("DATE(created_at) = ?", threeDaysAgo.Format("2006-01-02")).
		Where("ban = 0").
		Limit(200). // 限制每次处理200个用户
		Find(&users).Error

	if err != nil {
		globals.GraLog.Error("Failed to query onboarding day 3 users", zap.Error(err))
		return
	}

	if len(users) == 0 {
		globals.GraLog.Info("No onboarding day 3 users found")
		return
	}

	globals.GraLog.Info("Found onboarding day 3 users", zap.Int("count", len(users)))

	automationService := service.GroupServiceApp.MarketingServiceGroup.EmailAutomationService

	// 触发onboarding day 3规则
	for _, user := range users {
		// 检查是否已发送过（避免重复）
		var record model.EmailSendRecord
		checkErr := db.Where("uid = ? AND template_id IN (SELECT id FROM w_email_template WHERE name LIKE ?)",
			user.Id, "%Onboarding%Day 3%").
			Where("sent_at > ?", threeDaysAgo.Add(-24*time.Hour)). // 检查最近24小时内是否发送过
			First(&record).Error

		if checkErr == nil {
			// 已发送过，跳过
			globals.GraLog.Debug("Onboarding day 3 email already sent, skipping",
				zap.Uint("uid", user.Id),
				zap.String("email", user.Email))
			continue
		}

		data := map[string]string{
			"nickname": user.Nickname,
			"email":    user.Email,
			"day":      "3",
		}

		go func(uid int, triggerData map[string]string, email string) {
			if err := automationService.TriggerAutomationByType(model.EMAIL_AUTOMATION_TRIGGER_ONBOARDING_DAY_3, uid, triggerData); err != nil {
				globals.GraLog.Error("Failed to trigger onboarding day 3",
					zap.Int("uid", uid),
					zap.String("email", email),
					zap.Error(err))
			} else {
				globals.GraLog.Info("Triggered onboarding day 3 email",
					zap.Int("uid", uid),
					zap.String("email", email))
			}
		}(int(user.Id), data, user.Email)

		// 避免瞬间发送太多邮件，每个用户之间间隔100ms
		time.Sleep(100 * time.Millisecond)
	}

	globals.GraLog.Info("Onboarding day 3 check completed", zap.Int("count", len(users)))
}

// checkOnboardingDay7 检查注册后第7天的用户
func checkOnboardingDay7() {
	defer func() {
		if r := recover(); r != nil {
			globals.GraLog.Error("Onboarding day 7 scheduler panic recovered", zap.Any("panic", r))
			time.Sleep(5 * time.Second)
			go checkOnboardingDay7()
		}
	}()

	// 首次延迟到凌晨1点10分（避免与day3同时执行）
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 1, 10, 0, 0, now.Location())
	if now.Hour() >= 1 && now.Minute() >= 10 {
		// 如果当前时间已经超过凌晨1点10分，则延迟到明天凌晨1点10分
		next = next.AddDate(0, 0, 1)
	}
	time.Sleep(time.Until(next))

	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	// 立即执行一次
	runOnboardingDay7Check()

	// 然后每24小时执行一次
	for range ticker.C {
		runOnboardingDay7Check()
	}
}

func runOnboardingDay7Check() {
	globals.GraLog.Info("Running onboarding day 7 check")

	db := globals.GraDBs["system"]
	now := time.Now()
	sevenDaysAgo := now.AddDate(0, 0, -7)

	// 查询7天前注册的用户（未封禁）
	var users []model.User
	err := db.Where("DATE(created_at) = ?", sevenDaysAgo.Format("2006-01-02")).
		Where("ban = 0").
		Limit(200). // 限制每次处理200个用户
		Find(&users).Error

	if err != nil {
		globals.GraLog.Error("Failed to query onboarding day 7 users", zap.Error(err))
		return
	}

	if len(users) == 0 {
		globals.GraLog.Info("No onboarding day 7 users found")
		return
	}

	globals.GraLog.Info("Found onboarding day 7 users", zap.Int("count", len(users)))

	automationService := service.GroupServiceApp.MarketingServiceGroup.EmailAutomationService

	// 触发onboarding day 7规则
	for _, user := range users {
		// 检查是否已发送过（避免重复）
		var record model.EmailSendRecord
		checkErr := db.Where("uid = ? AND template_id IN (SELECT id FROM w_email_template WHERE name LIKE ?)",
			user.Id, "%Onboarding%Day 7%").
			Where("sent_at > ?", sevenDaysAgo.Add(-24*time.Hour)). // 检查最近24小时内是否发送过
			First(&record).Error

		if checkErr == nil {
			// 已发送过，跳过
			globals.GraLog.Debug("Onboarding day 7 email already sent, skipping",
				zap.Uint("uid", user.Id),
				zap.String("email", user.Email))
			continue
		}

		data := map[string]string{
			"nickname": user.Nickname,
			"email":    user.Email,
			"day":      "7",
		}

		go func(uid int, triggerData map[string]string, email string) {
			if err := automationService.TriggerAutomationByType(model.EMAIL_AUTOMATION_TRIGGER_ONBOARDING_DAY_7, uid, triggerData); err != nil {
				globals.GraLog.Error("Failed to trigger onboarding day 7",
					zap.Int("uid", uid),
					zap.String("email", email),
					zap.Error(err))
			} else {
				globals.GraLog.Info("Triggered onboarding day 7 email",
					zap.Int("uid", uid),
					zap.String("email", email))
			}
		}(int(user.Id), data, user.Email)

		// 避免瞬间发送太多邮件，每个用户之间间隔100ms
		time.Sleep(100 * time.Millisecond)
	}

	globals.GraLog.Info("Onboarding day 7 check completed", zap.Int("count", len(users)))
}
