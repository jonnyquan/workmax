package account

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"server/globals"
	"server/model"
	"server/model/common/response"
	accountsvc "server/service/account"
	canvasService "server/service/tools/canvas"
	"server/utils"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AccountApi struct{}

// userQuotaResponse 是 /account/quota 返回给前端的 DTO。
// PR4 起次数配额退役，aiAgent*/proTools* 字段仍在但永远为 0，形状兼容旧前端；
// 实际计费都走 credits（CreditsTotal / CreditsUsed 由 w_credits_pack 现算）。
type userQuotaResponse struct {
	UID                int       `json:"uid"`
	ResetType          string    `json:"resetType"`
	ResetTime          time.Time `json:"resetTime"`
	AIAgentTotalTimes  int       `json:"aiAgentTotalTimes"`
	AIAgentUsedTimes   int       `json:"aiAgentUsedTimes"`
	ProToolsTotalTimes int       `json:"proToolsTotalTimes"`
	ProToolsUsedTimes  int       `json:"proToolsUsedTimes"`
	CreditsTotal       int       `json:"creditsTotal"`
	CreditsUsed        int       `json:"creditsUsed"`
	CreditsAvailable   int       `json:"creditsAvailable"`
	CreditsReserved    int       `json:"creditsReservedPending"`
	CreditsFinalized   int       `json:"creditsFinalizedUsed"`
}

// GetUserQuota 获取用户配额信息
// @Tags Account
// @Summary 获取用户配额信息
// @Produce  application/json
// @Security ApiKeyAuth
// @Success 200 {string} string "{"code":200,"data":{},"msg":"获取成功"}"
// @Router /account/quota [get]
func (a *AccountApi) GetUserQuota(c *gin.Context) {
	uid := utils.GetUserID(c)

	// 如果uid为0，则返回未授权
	if uid == 0 {
		c.JSON(200, gin.H{
			"code": 300,
			"data": map[string]interface{}{"userQuota": nil, "memberStatus": "unauthorized"},
		})
		return
	}

	user := model.User{}
	err := globals.GraDBs["system"].Where("id = ?", uid).First(&user).Error
	if err != nil {
		response.FailWithMessage("获取用户失败", c)
		return
	}

	// memberStatus 只有三个取值：active / expired / free。
	//
	// 统一 member 枚举之前这里用 `Member > 0` 判"有会员"，于是领过免费计划的
	// 用户（member=1）会被报成 active，进而在下面的 loadActiveCreditsPacks 里
	// 把订阅来源的 credits pack 计进展示余额——而真正扣费那条链路
	// （CreditsPackService.isSubscriptionCreditsActiveTx 用 `member <= FREE`）
	// 从来就不认这些 pack。展示与可花额度对不上。现在两边同一套判定。
	memberStatus := "free"
	if user.Member > model.MEMBER_SUBSCRIPTION_FREE {
		if model.IsActivePaidMember(user.Member, user.MemberEndTime, time.Now()) {
			memberStatus = "active"
		} else {
			memberStatus = "expired"
		}
	}

	// Credits 现场汇总（w_credits_pack 是唯一真源）。这里用一次 pack
	// 明细查询同时计算余额和 breakdown，避免刷新页面时对 w_credits_pack
	// 重复 SUM + detail 扫描。
	creditsTotal, creditsUsed, creditsAvailable, creditsReservedPending, creditsFinalizedUsed := 0, 0, 0, 0, 0
	packs, packErr := loadActiveCreditsPacks(&user, memberStatus)
	if packErr == nil {
		for _, pack := range packs {
			creditsTotal += pack.CreditsTotal
			creditsUsed += pack.CreditsUsed
		}
		creditsAvailable = creditsTotal - creditsUsed
		if creditsAvailable < 0 {
			creditsAvailable = 0
		}
	}
	packService := accountsvc.NewCreditsPackService()
	if pending, pendingErr := packService.GetReservedPending(int(uid)); pendingErr == nil {
		creditsReservedPending = pending
	}
	creditsFinalizedUsed = creditsUsed - creditsReservedPending
	if creditsFinalizedUsed < 0 {
		creditsFinalizedUsed = 0
	}

	// PR4: 次数配额已退役，aiAgent*/proTools* 字段保留 0 以保持响应形状与旧前端兼容。
	// 前端 `value || 0` 的写法会自动把 0 视为"无次数条"，UI 独立 PR 再清理。
	userQuota := userQuotaResponse{
		UID:              int(user.Id),
		ResetType:        model.RESET_TYPE_MONTHLY,
		ResetTime:        time.Time{},
		CreditsTotal:     creditsTotal,
		CreditsUsed:      creditsUsed,
		CreditsAvailable: creditsAvailable,
		CreditsReserved:  creditsReservedPending,
		CreditsFinalized: creditsFinalizedUsed,
	}

	creditsBreakdown := buildCreditsBreakdown(packs, creditsTotal, creditsUsed, creditsAvailable, creditsReservedPending)

	c.JSON(200, gin.H{
		"code": 200,
		"data": map[string]interface{}{
			"userQuota":    userQuota,
			"memberStatus": memberStatus,
			"credits":      creditsBreakdown,
		},
	})
}

type creditsPackDetail struct {
	SourceType string     `json:"sourceType"`
	Total      int        `json:"total"`
	Used       int        `json:"used"`
	Remaining  int        `json:"remaining"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
}

func loadActiveCreditsPacks(user *model.User, memberStatus string) ([]model.CreditsPack, error) {
	now := time.Now()
	var packs []model.CreditsPack
	packQuery := globals.GraDBs["system"].
		Where("uid = ? AND source_type <> ? AND (expires_at IS NULL OR expires_at >= ?)", user.Id, model.CreditsSourceLegacy, now)
	if memberStatus != "active" {
		packQuery = packQuery.Where("source_type <> ?", model.CreditsSourceSubscription)
	}
	return packs, packQuery.Find(&packs).Error
}

// buildCreditsBreakdown 从已加载的 w_credits_pack 明细聚合用户积分。PR3b 起
// w_user_quota 的遗留 fallback 分支已移除——所有字段都基于 pack 直接计算。
func buildCreditsBreakdown(packs []model.CreditsPack, total, used, remaining, reservedPending int) map[string]interface{} {
	memberTotal, memberUsed := 0, 0
	packTotal, packUsed := 0, 0
	detailsMap := map[string]*creditsPackDetail{}
	for _, pack := range packs {
		detail, ok := detailsMap[pack.SourceType]
		if !ok {
			detail = &creditsPackDetail{SourceType: pack.SourceType}
			detailsMap[pack.SourceType] = detail
		}
		detail.Total += pack.CreditsTotal
		detail.Used += pack.CreditsUsed
		if pack.ExpiresAt != nil {
			if detail.ExpiresAt == nil || pack.ExpiresAt.After(*detail.ExpiresAt) {
				detail.ExpiresAt = pack.ExpiresAt
			}
		}

		if pack.SourceType == model.CreditsSourceSubscription {
			memberTotal += pack.CreditsTotal
			memberUsed += pack.CreditsUsed
		} else {
			packTotal += pack.CreditsTotal
			packUsed += pack.CreditsUsed
		}
	}

	finalizedUsed := used - reservedPending
	if finalizedUsed < 0 {
		finalizedUsed = 0
	}
	if remaining < 0 {
		remaining = 0
	}
	memberRemaining := memberTotal - memberUsed
	if memberRemaining < 0 {
		memberRemaining = 0
	}
	packRemaining := packTotal - packUsed
	if packRemaining < 0 {
		packRemaining = 0
	}

	details := make([]creditsPackDetail, 0, len(detailsMap))
	for _, detail := range detailsMap {
		detail.Remaining = detail.Total - detail.Used
		if detail.Remaining < 0 {
			detail.Remaining = 0
		}
		details = append(details, *detail)
	}
	order := map[string]int{
		model.CreditsSourceSubscription: 0,
		model.CreditsSourcePurchase:     1,
		model.CreditsSourceCheckin:      2,
		model.CreditsSourceInvite:       3,
		model.CreditsSourceBonus:        4,
		model.CreditsSourceAdmin:        5,
	}
	sort.Slice(details, func(i, j int) bool {
		ai, ok := order[details[i].SourceType]
		if !ok {
			ai = 100
		}
		aj, ok := order[details[j].SourceType]
		if !ok {
			aj = 100
		}
		if ai == aj {
			return details[i].SourceType < details[j].SourceType
		}
		return ai < aj
	})

	return map[string]interface{}{
		"total":           total,
		"used":            used,
		"available":       remaining,
		"reservedPending": reservedPending,
		"finalizedUsed":   finalizedUsed,
		"remaining":       remaining,
		"memberTotal":     memberTotal,
		"memberUsed":      memberUsed,
		"memberRemaining": memberRemaining,
		"packTotal":       packTotal,
		"packUsed":        packUsed,
		"packRemaining":   packRemaining,
		"packDetails":     details,
	}
}

// GetInviteStats 获取邀请统计
// @Tags Account
// @Summary 获取邀请统计信息
// @Produce  application/json
// @Security ApiKeyAuth
// @Success 200 {string} string "{"code":200,"data":{},"msg":"获取成功"}"
// @Router /account/invite/stats [get]
func (a *AccountApi) GetInviteStats(c *gin.Context) {
	uid := utils.GetUserID(c)

	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	// 获取用户邀请码
	user := model.User{}
	if err := globals.GraDBs["system"].Where("id = ?", uid).First(&user).Error; err != nil {
		response.FailWithMessage("获取用户失败", c)
		return
	}

	// 如果用户没有邀请码，自动生成一个
	if user.InviteCode == "" {
		user.InviteCode = utils.GenerateInviteCode()
		globals.GraDBs["system"].Model(&user).Update("invite_code", user.InviteCode)
	}

	// Merge the two aggregates (invite count + credits earned) into a
	// single round-trip via correlated subqueries. They hit different
	// tables (w_user vs w_user_rewards) so they can't share a FROM
	// clause, but MySQL still plans both subqueries inside one
	// statement and saves the network hop.
	var stats struct {
		TotalInvites       int64 `gorm:"column:total_invites"`
		TotalCreditsEarned int64 `gorm:"column:total_credits_earned"`
	}
	if err := globals.GraDBs["system"].Raw(`
		SELECT
			(SELECT COUNT(*) FROM w_user WHERE invite_uid = ?) AS total_invites,
			(SELECT COALESCE(SUM(reward_num), 0) FROM w_user_rewards WHERE uid = ? AND source_type = ?) AS total_credits_earned
	`, uid, uid, model.UserRewardsSourceTypeInvite).Scan(&stats).Error; err != nil {
		response.FailWithMessage("获取邀请统计失败", c)
		return
	}

	c.JSON(200, gin.H{
		"code": 200,
		"data": map[string]interface{}{
			"totalInvites":       stats.TotalInvites,
			"totalCreditsEarned": stats.TotalCreditsEarned,
			"inviteCode":         user.InviteCode,
		},
	})
}

// GetUsageRecords 获取用户使用记录
// @Tags Account
// @Summary 获取用户使用记录
// @Produce application/json
// @Security ApiKeyAuth
// @Param page query int false "页码" default(1)
// @Param limit query int false "每页数量" default(10)
// @Param featureType query string false "功能类型筛选 (e.g. image_generator)"
// @Param startDate query string false "开始日期 (YYYY-MM-DD)"
// @Param endDate query string false "结束日期 (YYYY-MM-DD)"
// @Success 200 {object} response.Response
// @Router /account/usage/records [get]
func (a *AccountApi) GetUsageRecords(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	offset := (page - 1) * limit

	featureFilter := strings.TrimSpace(c.Query("featureType"))
	startDateFilter := strings.TrimSpace(c.Query("startDate"))
	endDateFilter := strings.TrimSpace(c.Query("endDate"))

	baseDB := globals.GraDBs["system"].Model(&model.UsageRecord{}).Where("uid = ?", uid)
	if featureFilter != "" && strings.ToLower(featureFilter) != "all" {
		baseDB = baseDB.Where("feature_type = ?", featureFilter)
	}
	if startAt, ok := parseDateQuery(startDateFilter, true); ok {
		baseDB = baseDB.Where("created_at >= ?", startAt)
	}
	if endAt, ok := parseDateQuery(endDateFilter, false); ok {
		baseDB = baseDB.Where("created_at <= ?", endAt)
	}

	// Single-query pagination: COUNT(*) OVER() bolts the filter-wide
	// total onto every returned row, so the happy path (user has
	// records on this page) costs ONE round trip instead of two.
	// The previous code Count()ed then Find()ed in separate sessions
	// — correct, but doubled DB latency for a hot dashboard surface.
	type usageRow struct {
		model.UsageRecord
		Total int64 `gorm:"column:total"`
	}
	var rows []usageRow
	if err := baseDB.Session(&gorm.Session{}).
		Select("w_usage_record.*, COUNT(*) OVER() AS total").
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Scan(&rows).Error; err != nil {
		response.FailWithMessage("获取使用记录失败", c)
		return
	}

	records := make([]model.UsageRecord, 0, len(rows))
	var total int64
	if len(rows) > 0 {
		total = rows[0].Total
		for i := range rows {
			records = append(records, rows[i].UsageRecord)
		}
	} else {
		// Empty result page (cold start OR out-of-range page).
		// Window aggregate emits no rows when there are no matches,
		// so fall back to an explicit Count to keep totalPages
		// accurate in the response — the UI uses it to render
		// pagination controls + "no results" copy distinctly from
		// "page out of range".
		if err := baseDB.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			response.FailWithMessage("获取使用记录失败", c)
			return
		}
	}

	totalPages := int((total + int64(limit) - 1) / int64(limit))

	c.JSON(200, gin.H{
		"code": 200,
		"data": map[string]interface{}{
			"records":    records,
			"total":      total,
			"page":       page,
			"limit":      limit,
			"totalPages": totalPages,
		},
	})
}

// GetUsageSummary 获取用户使用情况汇总（用于 Dashboard 顶部统计卡片）
// @Tags Account
// @Summary 获取用户使用情况汇总
// @Produce application/json
// @Security ApiKeyAuth
// @Success 200 {object} response.Response
// @Router /account/usage/summary [get]
//
// Window semantics: month is bucketed by `time.Local` (server TZ), to
// match the rest of this file. Returns:
//   - currentMonthCredits / currentMonthRecords
//   - previousMonthCredits (for the delta indicator)
//   - topFeature this month (feature_type + creditsUsed) — empty if no
//     activity
//   - availableFeatures: distinct feature_types this user has ever used
//     (drives the records-list filter dropdown without a second roundtrip)
func (a *AccountApi) GetUsageSummary(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	startOfPrevMonth := startOfMonth.AddDate(0, -1, 0)

	db := globals.GraDBs["system"]

	// Combined current-month + previous-month aggregate in one pass.
	// Both buckets read the same 60-day-wide row set (uid + created_at
	// >= startOfPrevMonth) and split via CASE WHEN. Saves one round
	// trip vs. the previous two-query form, with no shape change.
	var monthBuckets struct {
		CurrentCredits  int64 `gorm:"column:current_credits"`
		CurrentRecords  int64 `gorm:"column:current_records"`
		PreviousCredits int64 `gorm:"column:previous_credits"`
	}
	if err := db.Model(&model.UsageRecord{}).
		Select(`
			COALESCE(SUM(CASE WHEN created_at >= ? THEN credits_used ELSE 0 END), 0) AS current_credits,
			SUM(CASE WHEN created_at >= ? THEN 1 ELSE 0 END) AS current_records,
			COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? THEN credits_used ELSE 0 END), 0) AS previous_credits
		`, startOfMonth, startOfMonth, startOfPrevMonth, startOfMonth).
		Where("uid = ? AND created_at >= ?", uid, startOfPrevMonth).
		Scan(&monthBuckets).Error; err != nil {
		response.FailWithMessage("获取使用汇总失败", c)
		return
	}

	var topFeature struct {
		FeatureType string `gorm:"column:feature_type"`
		Credits     int64  `gorm:"column:credits"`
	}
	if err := db.Model(&model.UsageRecord{}).
		Select("feature_type, COALESCE(SUM(credits_used), 0) AS credits").
		Where("uid = ? AND created_at >= ? AND credits_used > 0", uid, startOfMonth).
		Group("feature_type").
		Order("credits DESC").
		Limit(1).
		Scan(&topFeature).Error; err != nil {
		response.FailWithMessage("获取使用汇总失败", c)
		return
	}

	var availableFeatures []string
	if err := db.Model(&model.UsageRecord{}).
		Where("uid = ? AND feature_type <> ''", uid).
		Distinct("feature_type").
		Order("feature_type ASC").
		Pluck("feature_type", &availableFeatures).Error; err != nil {
		response.FailWithMessage("获取使用汇总失败", c)
		return
	}

	topFeaturePayload := gin.H{
		"featureType": "",
		"creditsUsed": int64(0),
	}
	if topFeature.Credits > 0 && topFeature.FeatureType != "" {
		topFeaturePayload = gin.H{
			"featureType": topFeature.FeatureType,
			"creditsUsed": topFeature.Credits,
		}
	}

	c.JSON(200, gin.H{
		"code": 200,
		"data": gin.H{
			"currentMonthCredits":  monthBuckets.CurrentCredits,
			"currentMonthRecords":  monthBuckets.CurrentRecords,
			"previousMonthCredits": monthBuckets.PreviousCredits,
			"topFeature":           topFeaturePayload,
			"availableFeatures":    availableFeatures,
		},
	})
}

// GetGenerationRecords 获取用户生成记录 (My Gallery)
// @Tags Account
// @Summary 获取用户生成记录
// @Produce application/json
// @Security ApiKeyAuth
// @Param page query int false "页码" default(1)
// @Param limit query int false "每页数量" default(20)
// @Param query query string false "关键词（匹配提示词、工具和模型）"
// @Param tool query string false "工具筛选"
// @Param model query string false "模型筛选（兼容旧参数）"
// @Param status query string false "状态筛选(all/pending/processing/completed/failed/cancelled)" default(completed)
// @Param startDate query string false "开始日期 (YYYY-MM-DD)"
// @Param endDate query string false "结束日期 (YYYY-MM-DD)"
// @Router /account/generation/records [get]
func (a *AccountApi) GetGenerationRecords(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	offset := (page - 1) * limit

	var records []model.GenerationRecord
	var availableTools []string
	var rawToolIDs []string
	var total int64

	searchQuery := strings.TrimSpace(c.Query("query"))
	toolFilter := strings.TrimSpace(c.Query("tool"))
	modelFilter := strings.TrimSpace(c.Query("model"))
	statusFilter := strings.ToLower(strings.TrimSpace(c.DefaultQuery("status", "completed")))
	startDateFilter := strings.TrimSpace(c.Query("startDate"))
	endDateFilter := strings.TrimSpace(c.Query("endDate"))

	baseDB := globals.GraDBs["system"].Model(&model.GenerationRecord{}).Where("uid = ?", uid)

	if statusFilter != "" && statusFilter != "all" {
		if status, ok := parseGenerationRecordStatus(statusFilter); ok {
			if status < 0 {
				baseDB = baseDB.Where("1 = 0")
			} else {
				baseDB = baseDB.Where("status = ?", status)
			}
		} else {
			baseDB = baseDB.Where("status = ?", model.STATUS_SUCCESS)
		}
	}

	if startAt, ok := parseDateQuery(startDateFilter, true); ok {
		baseDB = baseDB.Where("created_at >= ?", startAt)
	}

	if endAt, ok := parseDateQuery(endDateFilter, false); ok {
		baseDB = baseDB.Where("created_at <= ?", endAt)
	}

	toolListDB := baseDB.Session(&gorm.Session{})
	if err := toolListDB.
		Where("tool_id <> ''").
		Distinct("tool_id").
		Order("tool_id ASC").
		Pluck("tool_id", &rawToolIDs).Error; err != nil {
		response.FailWithMessage("获取工具筛选失败", c)
		return
	}

	toolSet := make(map[string]struct{}, len(rawToolIDs))
	matchedRawToolIDs := make([]string, 0, len(rawToolIDs))
	normalizedToolFilter := model.GetToolIDFromModel(toolFilter)
	for _, rawToolID := range rawToolIDs {
		normalizedToolID := model.GetToolIDFromModel(strings.TrimSpace(rawToolID))
		if normalizedToolID == "" {
			continue
		}
		if _, exists := toolSet[normalizedToolID]; !exists {
			toolSet[normalizedToolID] = struct{}{}
			availableTools = append(availableTools, normalizedToolID)
		}
		if normalizedToolFilter != "" && strings.EqualFold(normalizedToolID, normalizedToolFilter) {
			matchedRawToolIDs = append(matchedRawToolIDs, rawToolID)
		}
	}
	sort.Strings(availableTools)

	db := baseDB.Session(&gorm.Session{})
	if toolFilter != "" && strings.ToLower(toolFilter) != "all" {
		if len(matchedRawToolIDs) == 0 {
			response.OkWithData(gin.H{
				"items":      []any{},
				"total":      0,
				"page":       page,
				"limit":      limit,
				"totalPages": 0,
				"tools":      availableTools,
			}, c)
			return
		}
		db = db.Where("tool_id IN ?", matchedRawToolIDs)
	}

	if modelFilter != "" && strings.ToLower(modelFilter) != "all" {
		db = db.Where("model = ?", modelFilter)
	}

	if searchQuery != "" {
		likeQuery := "%" + searchQuery + "%"
		db = db.Where("(prompt LIKE ? OR negative_prompt LIKE ? OR model LIKE ? OR tool_id LIKE ?)", likeQuery, likeQuery, likeQuery, likeQuery)
	}

	if err := db.Count(&total).Error; err != nil {
		response.FailWithMessage("获取生成记录失败", c)
		return
	}

	orderedIDs := make([]uint, 0, limit)
	if err := db.
		Session(&gorm.Session{}).
		Order("created_at DESC, id DESC").
		Offset(offset).
		Limit(limit).
		Pluck("id", &orderedIDs).Error; err != nil {
		response.FailWithMessage("获取生成记录失败", c)
		return
	}
	if len(orderedIDs) > 0 {
		if err := globals.GraDBs["system"].
			Model(&model.GenerationRecord{}).
			Where("id IN ?", orderedIDs).
			Find(&records).Error; err != nil {
			response.FailWithMessage("获取生成记录失败", c)
			return
		}

		recordsByID := make(map[uint]model.GenerationRecord, len(records))
		for _, record := range records {
			recordsByID[uint(record.Id)] = record
		}

		orderedRecords := make([]model.GenerationRecord, 0, len(orderedIDs))
		for _, id := range orderedIDs {
			if record, ok := recordsByID[id]; ok {
				orderedRecords = append(orderedRecords, record)
			}
		}
		records = orderedRecords
	}

	// 转换为前端需要的格式
	type GalleryItem struct {
		ID             uint      `json:"id"`
		Prompt         string    `json:"prompt"`
		NegativePrompt string    `json:"negativePrompt,omitempty"`
		ToolID         string    `json:"toolId,omitempty"`
		Model          string    `json:"model"`
		MediaType      string    `json:"mediaType,omitempty"`
		ImageUrl       string    `json:"imageUrl"`
		ImageUrls      []string  `json:"imageUrls,omitempty"`
		VideoUrl       string    `json:"videoUrl,omitempty"`
		VideoUrls      []string  `json:"videoUrls,omitempty"`
		ThumbnailUrl   string    `json:"thumbnailUrl,omitempty"`
		AspectRatio    string    `json:"aspectRatio,omitempty"`
		StylePreset    string    `json:"stylePreset,omitempty"`
		Steps          int       `json:"steps,omitempty"`
		Width          int       `json:"width,omitempty"`
		Height         int       `json:"height,omitempty"`
		Status         string    `json:"status"`
		CreditsUsed    int       `json:"creditsUsed"`
		DurationMs     int       `json:"durationMs"`
		CreatedAt      time.Time `json:"createdAt"`
	}

	items := make([]GalleryItem, 0, len(records))
	for _, record := range records {
		params := parseJSONStringMap(record.Params)
		metadata := parseJSONStringMap(record.ResultMetadata)
		imageUrls, videoUrls := splitGeneratedAssetURLs(parseJSONStringSlice(record.ResultImages))
		videoUrls = appendUniqueStrings(videoUrls, extractStringSliceFromMap(metadata, "videoUrls")...)
		if videoURL := extractStringFromMap(metadata, "videoUrl"); videoURL != "" {
			videoUrls = appendUniqueStrings(videoUrls, videoURL)
		}
		if nestedVideo := extractMapFromMap(metadata, "video"); nestedVideo != nil {
			videoUrls = appendUniqueStrings(videoUrls, extractStringSliceFromMap(nestedVideo, "videoUrls")...)
			if videoURL := extractStringFromMap(nestedVideo, "videoUrl"); videoURL != "" {
				videoUrls = appendUniqueStrings(videoUrls, videoURL)
			}
		}

		thumbnailURL := extractStringFromMap(metadata, "thumbnailUrl")
		if thumbnailURL == "" {
			if nestedVideo := extractMapFromMap(metadata, "video"); nestedVideo != nil {
				thumbnailURL = extractStringFromMap(nestedVideo, "thumbnailUrl")
			}
		}

		imageUrl := ""
		if len(imageUrls) > 0 {
			imageUrl = imageUrls[0]
		}
		videoUrl := ""
		if len(videoUrls) > 0 {
			videoUrl = videoUrls[0]
		}

		mediaType := strings.TrimSpace(extractStringFromMap(metadata, "mediaType"))
		if mediaType == "" {
			switch {
			case len(videoUrls) > 0:
				mediaType = model.MediaTypeVideo
			case len(imageUrls) > 0:
				mediaType = model.MediaTypeImage
			}
		}

		items = append(items, GalleryItem{
			ID:             record.Id,
			Prompt:         record.Prompt,
			NegativePrompt: record.NegativePrompt,
			ToolID:         model.GetToolIDFromModel(record.ToolID),
			Model:          record.Model,
			MediaType:      mediaType,
			ImageUrl:       imageUrl,
			ImageUrls:      imageUrls,
			VideoUrl:       videoUrl,
			VideoUrls:      videoUrls,
			ThumbnailUrl:   thumbnailURL,
			AspectRatio:    record.AspectRatio,
			StylePreset:    record.StylePreset,
			Steps:          getIntFromJSONMap(params, "steps", metadata),
			Width:          getIntFromJSONMap(metadata, "width"),
			Height:         getIntFromJSONMap(metadata, "height"),
			Status:         formatGenerationRecordStatus(record.Status),
			CreditsUsed:    record.CreditsUsed,
			DurationMs:     record.DurationMs,
			CreatedAt:      record.CreatedAt,
		})
	}

	totalPages := int(total) / limit
	if int(total)%limit > 0 {
		totalPages++
	}

	c.JSON(200, gin.H{
		"code": 200,
		"data": gin.H{
			"items":      items,
			"total":      total,
			"page":       page,
			"limit":      limit,
			"totalPages": totalPages,
			"tools":      availableTools,
		},
	})
}

func parseJSONStringMap(raw string) map[string]interface{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]interface{}{}
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil && parsed != nil {
		return parsed
	}

	var wrapped string
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil {
		wrapped = strings.TrimSpace(wrapped)
		if wrapped != "" {
			if err := json.Unmarshal([]byte(wrapped), &parsed); err == nil && parsed != nil {
				return parsed
			}
		}
	}

	return map[string]interface{}{}
}

func parseJSONStringSlice(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var parsed []string
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		return parsed
	}

	var wrapped string
	if err := json.Unmarshal([]byte(trimmed), &wrapped); err == nil {
		wrapped = strings.TrimSpace(wrapped)
		if wrapped != "" {
			if err := json.Unmarshal([]byte(wrapped), &parsed); err == nil {
				return parsed
			}
		}
	}

	return nil
}

func splitGeneratedAssetURLs(urls []string) ([]string, []string) {
	images := make([]string, 0, len(urls))
	videos := make([]string, 0, len(urls))
	for _, rawURL := range urls {
		trimmed := strings.TrimSpace(rawURL)
		if trimmed == "" {
			continue
		}
		switch detectGeneratedAssetType(trimmed) {
		case "video":
			videos = appendUniqueStrings(videos, trimmed)
		default:
			images = appendUniqueStrings(images, trimmed)
		}
	}
	return images, videos
}

func detectGeneratedAssetType(rawURL string) string {
	cleanURL := strings.ToLower(strings.TrimSpace(strings.Split(rawURL, "?")[0]))
	switch path.Ext(cleanURL) {
	case ".mp4", ".webm", ".mov", ".m4v", ".avi", ".mkv":
		return "video"
	default:
		return "image"
	}
}

func appendUniqueStrings(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		seen[trimmed] = struct{}{}
	}
	for _, item := range values {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		existing = append(existing, trimmed)
	}
	return existing
}

func extractStringFromMap(m map[string]interface{}, key string) string {
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return ""
}

func extractMapFromMap(m map[string]interface{}, key string) map[string]interface{} {
	value, ok := m[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case map[string]interface{}:
		return typed
	case model.JSONMap:
		return map[string]interface{}(typed)
	default:
		return nil
	}
}

func extractStringSliceFromMap(m map[string]interface{}, key string) []string {
	value, ok := m[key]
	if !ok || value == nil {
		return nil
	}

	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				items = append(items, strings.TrimSpace(text))
			}
		}
		return items
	default:
		return nil
	}
}

func getIntFromJSONMap(m map[string]interface{}, key string, fallbacks ...map[string]interface{}) int {
	if value, ok := m[key]; ok {
		switch typed := value.(type) {
		case int:
			return typed
		case int8:
			return int(typed)
		case int16:
			return int(typed)
		case int32:
			return int(typed)
		case int64:
			return int(typed)
		case uint:
			return int(typed)
		case uint8:
			return int(typed)
		case uint16:
			return int(typed)
		case uint32:
			return int(typed)
		case uint64:
			return int(typed)
		case float32:
			return int(typed)
		case float64:
			return int(typed)
		}
	}

	for _, fallback := range fallbacks {
		if result := getIntFromJSONMap(fallback, key); result > 0 {
			return result
		}
	}

	return 0
}

func formatGenerationRecordStatus(status int8) string {
	switch status {
	case model.STATUS_SUCCESS:
		return "completed"
	case model.STATUS_FAILED:
		return "failed"
	default:
		return "processing"
	}
}

func parseGenerationRecordStatus(raw string) (int8, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "completed":
		return model.STATUS_SUCCESS, true
	case "failed":
		return model.STATUS_FAILED, true
	case "pending", "processing":
		return model.STATUS_PROCESSING, true
	case "cancelled":
		return -1, true
	default:
		return model.STATUS_SUCCESS, false
	}
}

func parseDateQuery(raw string, isStart bool) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}

	layouts := []string{
		"2006-01-02",
		time.RFC3339,
		"2006-01-02 15:04:05",
	}

	for _, layout := range layouts {
		parsed, err := time.ParseInLocation(layout, trimmed, time.Local)
		if err != nil {
			continue
		}

		// When a date-only filter is passed, normalize to start/end of that day.
		if layout == "2006-01-02" && !isStart {
			parsed = parsed.Add(24*time.Hour - time.Nanosecond)
		}
		return parsed, true
	}

	return time.Time{}, false
}

// GetOrderList 获取用户订单列表
// @Tags Account
// @Summary 获取用户订单列表
// @Produce application/json
// @Security ApiKeyAuth
// @Param page query int false "页码" default(1)
// @Param limit query int false "每页数量" default(10)
// @Param status query string false "订单状态"
// @Param startDate query string false "开始日期 (YYYY-MM-DD)"
// @Param endDate query string false "结束日期 (YYYY-MM-DD)"
// @Router /account/subscription/billing [get]
func (a *AccountApi) GetOrderList(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	status := strings.TrimSpace(c.Query("status"))
	startDate := strings.TrimSpace(c.Query("startDate"))
	endDate := strings.TrimSpace(c.Query("endDate"))

	db := globals.GraDBs["system"].Model(&model.Order{}).Where("uid = ?", uid)
	if status != "" && status != "all" {
		if status == "CANCELED" {
			status = model.STATUS_CANCEL
		}
		db = db.Where("status = ?", status)
	}
	// parseDateQuery normalizes "2026-05-08" to start-of-day for startAt
	// and end-of-day (23:59:59.999...) for endAt — without this, the old
	// `<= '2026-05-08'` comparison silently dropped same-day rows
	// because MySQL coerced it to 2026-05-08 00:00:00.
	if startAt, ok := parseDateQuery(startDate, true); ok {
		db = db.Where("created_at >= ?", startAt)
	}
	if endAt, ok := parseDateQuery(endDate, false); ok {
		db = db.Where("created_at <= ?", endAt)
	}

	var total int64
	if err := db.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		response.FailWithMessage("获取订单失败", c)
		return
	}

	var orders []model.Order
	offset := (page - 1) * limit
	if err := db.Session(&gorm.Session{}).
		Order("pay_time desc, id desc").
		Limit(limit).
		Offset(offset).
		Find(&orders).Error; err != nil {
		response.FailWithMessage("获取订单失败", c)
		return
	}

	totalPages := int((total + int64(limit) - 1) / int64(limit))

	c.JSON(200, gin.H{
		"code": 200,
		"data": map[string]interface{}{
			"items":      orders,
			"total":      total,
			"page":       page,
			"limit":      limit,
			"totalPages": totalPages,
		},
	})
}

// GetRewardsRecords 获取用户奖励记录
// @Tags Account
// @Summary 获取用户奖励记录
// @Produce application/json
// @Security ApiKeyAuth
// @Param page query int false "页码" default(1)
// @Param limit query int false "每页数量" default(10)
// @Success 200 {object} response.Response
// @Router /account/rewards/records [get]
func (a *AccountApi) GetRewardsRecords(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	if page <= 0 {
		page = 1
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	offset := (page - 1) * limit

	var records []model.UserRewards
	var total int64

	baseDB := globals.GraDBs["system"].Model(&model.UserRewards{}).Where("uid = ?", uid)
	if err := baseDB.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		response.FailWithMessage("获取奖励记录失败", c)
		return
	}

	if err := baseDB.Session(&gorm.Session{}).
		Order("created_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error; err != nil {
		response.FailWithMessage("获取奖励记录失败", c)
		return
	}

	totalPages := int((total + int64(limit) - 1) / int64(limit))

	c.JSON(200, gin.H{
		"code": 200,
		"data": map[string]interface{}{
			"records":    records,
			"total":      total,
			"page":       page,
			"limit":      limit,
			"totalPages": totalPages,
		},
	})
}

// deleteAccountRequest is the body for self-service account deletion.
//
// Password is required for users who signed up with email+password (i.e. the
// stored password hash is non-empty). Pure OAuth users (empty password column)
// can delete with confirmation only — they have no credential to verify.
//
// Confirmation must echo back the user's current email address. This forces
// an out-of-band step beyond just clicking a button, mirroring the
// GitHub / Stripe / Vercel pattern.
type deleteAccountRequest struct {
	Password     string `json:"password"`
	Confirmation string `json:"confirmation"`
}

const accountDeletionPendingNote = "account deletion pending"

var (
	errAccountDeletionConfirmation = errors.New("email confirmation does not match")
	errAccountDeletionPassword     = errors.New("account deletion password is invalid")
	errAccountDeletionBilling      = errors.New("account has unsettled billing state")
	errAccountDeletionUnavailable  = errors.New("account is not eligible for deletion")
)

type accountDeletionIntent struct {
	User            model.User
	NewlyStaged     bool
	PreviousBanNote string
}

func stageAccountDeletionIntent(
	db *gorm.DB,
	uid uint,
	req deleteAccountRequest,
	now time.Time,
) (intent accountDeletionIntent, err error) {
	if db == nil || uid == 0 {
		return accountDeletionIntent{}, errAccountDeletionUnavailable
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent.User, "id = ?", uid).Error; err != nil {
			return err
		}
		if strings.TrimSpace(strings.ToLower(req.Confirmation)) != strings.ToLower(intent.User.Email) {
			return errAccountDeletionConfirmation
		}
		if intent.User.Password != "" {
			if req.Password == "" || !utils.Md5Check(req.Password, intent.User.Password) {
				return errAccountDeletionPassword
			}
		}
		alreadyPending := intent.User.Ban && intent.User.BanNote == accountDeletionPendingNote
		if intent.User.Ban && !alreadyPending {
			return errAccountDeletionUnavailable
		}
		if providerSubscriptionMayStillBill(intent.User, now) || hasActivePaidMembership(intent.User, now) {
			return errAccountDeletionBilling
		}
		var unpaidStripeOrders int64
		if err := tx.Model(&model.Order{}).
			Where("uid = ? AND status = ? AND pay_method = ?", uid, model.STATUS_UNPAID, "stripe").
			Count(&unpaidStripeOrders).Error; err != nil {
			return err
		}
		if unpaidStripeOrders != 0 {
			return errAccountDeletionBilling
		}
		if alreadyPending {
			return nil
		}
		intent.PreviousBanNote = intent.User.BanNote
		result := tx.Model(&model.User{}).
			Where("id = ? AND ban = ?", uid, false).
			Updates(map[string]interface{}{
				"ban": true, "ban_note": accountDeletionPendingNote, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("account deletion owner changed")
		}
		intent.NewlyStaged = true
		intent.User.Ban = true
		intent.User.BanNote = accountDeletionPendingNote
		return nil
	})
	return intent, err
}

func releaseAccountDeletionIntent(db *gorm.DB, uid uint, previousBanNote string, now time.Time) error {
	result := db.Model(&model.User{}).
		Where("id = ? AND ban = ? AND ban_note = ?", uid, true, accountDeletionPendingNote).
		Updates(map[string]interface{}{"ban": false, "ban_note": previousBanNote, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("account deletion intent owner changed")
	}
	return nil
}

// DeleteAccount soft-deletes the current user. Refuses while an active Stripe
// subscription is still attached so we don't strand a billing relationship.
// On success the row is anonymized: email is rewritten to a sentinel that
// frees the original address for re-registration, PII fields are cleared,
// the API key is revoked, and `ban` is flipped on so any lingering JWT can
// no longer transact against this row.
//
// Hard delete is intentionally avoided — orders, generation records, usage
// rows, and team memberships all carry this UID as a foreign key.
//
// @Tags Account
// @Summary 删除账户
// @Produce application/json
// @Security ApiKeyAuth
// @Param data body deleteAccountRequest true "删除账户请求"
// @Success 200 {object} response.Response
// @Router /account/delete [post]
func (a *AccountApi) DeleteAccount(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	var req deleteAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}

	db := globals.GraDBs["system"]
	intent, err := stageAccountDeletionIntent(db, uid, req, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, errAccountDeletionConfirmation):
			response.FailWithMessage("Email confirmation does not match", c)
		case errors.Is(err, errAccountDeletionPassword):
			response.FailWithMessage("Incorrect password or password is required", c)
		case errors.Is(err, errAccountDeletionBilling):
			response.FailWithMessage("Please settle or cancel active billing before deleting your account", c)
		case errors.Is(err, errAccountDeletionUnavailable):
			response.FailWithMessage("Account is not eligible for deletion", c)
		default:
			globals.Error(fmt.Sprintf("DeleteAccount: failed to stage deletion for uid=%d: %v", uid, err))
			response.FailWithMessage("Failed to prepare account deletion", c)
		}
		return
	}
	user := intent.User

	// C-10: cascade-delete Canvas data BEFORE anonymizing the user
	// row. Order matters — if the canvas purge errors, the user row
	// stays untouched so a retry resumes cleanly from a re-runnable
	// state. Anonymize-first would force ops to manually re-fetch
	// the original email for any follow-up reconciliation.
	//
	// PurgeUserCanvasData is idempotent: a retry after partial
	// completion skips the rows already cleaned in the prior pass.
	//
	// Future per-tool cleanups (workagent, video generator, etc.)
	// chain in here in the same shape — call → check err → return on
	// failure. The chain stays linear and explicit; we deliberately
	// don't introduce a hook-registry indirection.
	purgeReport, err := canvasService.PurgeUserCanvasData(c.Request.Context(), db, int(uid))
	if err != nil {
		globals.Error(fmt.Sprintf("DeleteAccount: canvas purge failed for uid=%d (partial=%+v): %v", uid, purgeReport, err))
		if intent.NewlyStaged {
			if releaseErr := releaseAccountDeletionIntent(db, uid, intent.PreviousBanNote, time.Now()); releaseErr != nil {
				globals.Error(fmt.Sprintf("DeleteAccount: failed to release deletion intent for uid=%d: %v", uid, releaseErr))
			}
		}
		response.FailWithMessage("Failed to delete account data, please retry", c)
		return
	}
	globals.Info(fmt.Sprintf("DeleteAccount: uid=%d canvas purge complete %+v", uid, purgeReport))

	// Anonymize: rewrite email to a sentinel that satisfies the unique
	// index while freeing the original address for re-registration; clear
	// PII / credentials; ban so any lingering JWT is dead on arrival.
	deletedEmail := fmt.Sprintf("deleted_%d_%d@deleted.local", uid, time.Now().Unix())
	updates := map[string]interface{}{
		"email":               deletedEmail,
		"phone":               "",
		"nickname":            "",
		"avatar":              "",
		"password":            "",
		"api_key":             "",
		"ban":                 true,
		"ban_note":            "user requested deletion",
		"identity_code":       "",
		"member_subscription": "",
		"updated_at":          time.Now(),
	}
	result := db.Model(&model.User{}).
		Where("id = ? AND ban = ? AND ban_note = ? AND member_subscription = ?", uid, true, accountDeletionPendingNote, user.MemberSubscription).
		Updates(updates)
	if result.Error != nil {
		err = result.Error
	} else if result.RowsAffected != 1 {
		err = fmt.Errorf("account deletion owner changed")
	}
	if err != nil {
		globals.Error(fmt.Sprintf("DeleteAccount: failed to anonymize user %d: %v", uid, err))
		response.FailWithMessage("Failed to delete account", c)
		return
	}

	globals.Info(fmt.Sprintf("DeleteAccount: user %d deleted (email rewritten to %s)", uid, deletedEmail))
	response.OkWithMessage("Account deleted", c)
}

// verifyEmailRequest is the body for the email-confirmation step. The code
// is the 6-digit value the user typed; we look it up against the cache key
// keyed on the *current* user's email (not anything in the body) so a
// stolen JWT can't verify someone else's address.
type verifyEmailRequest struct {
	Code string `json:"code" binding:"required"`
}

// verifyEmailMaxAttempts is the per-email cap before the cached code is
// invalidated. Five attempts at 1-in-1,000,000 odds is ~5e-6 cumulative
// success — well below any reasonable concern. The user can ask for a
// resend if they exhaust attempts.
const verifyEmailMaxAttempts = 5

// resendVerifyEmailCooldown is the per-email gap between resends. Stops
// the resend button from being a free email-bombing primitive while still
// letting a user retry promptly if the first email was filtered.
const resendVerifyEmailCooldown = 60 * time.Second

func verifyAttemptsCacheKey(email string) string {
	return fmt.Sprintf("verifyAttempts-%s", strings.ToLower(strings.TrimSpace(email)))
}

func resendCooldownCacheKey(email string) string {
	return fmt.Sprintf("resendCooldown-%s", strings.ToLower(strings.TrimSpace(email)))
}

// VerifyEmail consumes the 6-digit code the user received and flips
// `auth_email = 1`. The code lives in the in-process cache under
// `verifyCode-{email}` (5-minute TTL, written by SendVerificationEmail).
//
// Brute-force defense: per-email attempt counter caps at 5 before the
// cached code is deleted; the user must request a fresh code. This is
// orthogonal to the per-IP route-level RateLimit middleware.
//
// @Tags Account
// @Summary 验证邮箱
// @Produce application/json
// @Security ApiKeyAuth
// @Param data body verifyEmailRequest true "验证码"
// @Success 200 {object} response.Response
// @Router /account/verify-email [post]
func (a *AccountApi) VerifyEmail(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	var req verifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		response.FailWithMessage("Verification code is required", c)
		return
	}

	var user model.User
	if err := globals.GraDBs["system"].First(&user, "id = ?", uid).Error; err != nil {
		response.FailWithMessage("Account not found", c)
		return
	}
	if user.AuthEmail == 1 {
		// Already verified — return success so the UI can move on without
		// a confusing error if the user clicks twice.
		response.OkWithMessage("Email already verified", c)
		return
	}

	cached, ok := globals.GraCache.Get(utils.VerifyCodeCacheKey(user.Email))
	if !ok {
		response.FailWithMessage("Code expired or not found, please request a new one", c)
		return
	}
	expected, _ := cached.(string)

	if code != expected {
		// Bump attempts; on the cap, drop the cached code so the next
		// attempt can't grind through the keyspace.
		attemptsKey := verifyAttemptsCacheKey(user.Email)
		attempts := 1
		if v, found := globals.GraCache.Get(attemptsKey); found {
			if n, okN := v.(int); okN {
				attempts = n + 1
			}
		}
		if attempts >= verifyEmailMaxAttempts {
			globals.GraCache.Delete(utils.VerifyCodeCacheKey(user.Email))
			globals.GraCache.Delete(attemptsKey)
			response.FailWithMessage("Too many incorrect attempts, please request a new code", c)
			return
		}
		// Mirror the code's TTL so the counter resets together with the code.
		globals.GraCache.Set(attemptsKey, attempts, 5*time.Minute)
		response.FailWithMessage("Incorrect verification code", c)
		return
	}

	// Code matches — flip the verification flag and burn the cached code so
	// it can't be replayed.
	if err := globals.GraDBs["system"].Model(&model.User{}).
		Where("id = ?", uid).
		Update("auth_email", 1).Error; err != nil {
		globals.Error(fmt.Sprintf("VerifyEmail: failed to set auth_email for user %d: %v", uid, err))
		response.FailWithMessage("Failed to verify email", c)
		return
	}
	globals.GraCache.Delete(utils.VerifyCodeCacheKey(user.Email))
	globals.GraCache.Delete(verifyAttemptsCacheKey(user.Email))

	response.OkWithMessage("Email verified", c)
}

// ResendVerifyEmail re-issues the 6-digit code with a per-email cooldown
// (resendVerifyEmailCooldown). Cheap no-op when the user is already
// verified — returns success so the UI doesn't get stuck.
//
// @Tags Account
// @Summary 重新发送验证邮件
// @Produce application/json
// @Security ApiKeyAuth
// @Success 200 {object} response.Response
// @Router /account/resend-verify-email [post]
func (a *AccountApi) ResendVerifyEmail(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("未授权", c)
		return
	}

	var user model.User
	if err := globals.GraDBs["system"].First(&user, "id = ?", uid).Error; err != nil {
		response.FailWithMessage("Account not found", c)
		return
	}
	if user.AuthEmail == 1 {
		response.OkWithMessage("Email already verified", c)
		return
	}

	cooldownKey := resendCooldownCacheKey(user.Email)
	if _, onCooldown := globals.GraCache.Get(cooldownKey); onCooldown {
		response.FailWithMessage("Please wait a minute before requesting another code", c)
		return
	}

	if err := utils.SendVerificationEmail(user.Email); err != nil {
		globals.Error(fmt.Sprintf("ResendVerifyEmail: send failed for user %d: %v", uid, err))
		response.FailWithMessage("Failed to send verification email", c)
		return
	}

	globals.GraCache.Set(cooldownKey, true, resendVerifyEmailCooldown)
	// Reset the attempts counter — the previous code is gone, the new one
	// deserves its own 5 tries.
	globals.GraCache.Delete(verifyAttemptsCacheKey(user.Email))

	response.OkWithMessage("Verification email sent", c)
}
