package admin

import (
	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/service"
	"server/utils"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type AdminDashboardApi struct{}

// @Tags Admin Dashboard
// @Summary Get basic statistics (users, orders, revenue)
// @Success 200 {object} response.Response "Basic statistics data"
// @Router /api/admin/dashboard/getBasicStatistics [get]
func (s *AdminDashboardApi) GetBasicStatistics(c *gin.Context) {
	uid := utils.GetUserID(c)
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if user.Role != "manager" {
		response.FailWithMessage("No permission", c)
		return
	}

	countUserToday := int64(0)
	countOrderToday := int64(0)
	countAIChatRecordsToday := int64(0)
	sumAmountToday := float64(0)

	countUserYesterday := int64(0)
	countOrderYesterday := int64(0)
	countAIChatRecordsYesterday := int64(0)
	sumAmountYesterday := float64(0)

	todayStart := time.Now().Format("2006-01-02")
	yesterdayStart := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	// Basic statistics - today
	globals.GraDBs["system"].Model(&model.User{}).Where("created_at >= ?", todayStart).Count(&countUserToday)
	globals.GraDBs["system"].Model(&model.Order{}).Where("pay_time >= ? and status = ?", todayStart, model.STATUS_COMPLETE).Count(&countOrderToday)
	// AI Agent records from usage_record table (consistent with tool statistics)
	globals.GraDBs["system"].Model(&model.UsageRecord{}).Where("created_at >= ? AND feature_type = ?", todayStart, "aiagent").Count(&countAIChatRecordsToday)
	globals.GraDBs["system"].Model(&model.Order{}).Where("pay_time >= ? and status = ?", todayStart, model.STATUS_COMPLETE).Select("COALESCE(sum(amount), 0)").Scan(&sumAmountToday)

	// Basic statistics - yesterday comparison
	globals.GraDBs["system"].Model(&model.User{}).Where("created_at >= ? and created_at < ?", yesterdayStart, todayStart).Count(&countUserYesterday)
	globals.GraDBs["system"].Model(&model.Order{}).Where("pay_time >= ? and pay_time < ? and status = ?", yesterdayStart, todayStart, model.STATUS_COMPLETE).Count(&countOrderYesterday)
	// AI Agent records from usage_record table (consistent with tool statistics)
	globals.GraDBs["system"].Model(&model.UsageRecord{}).Where("created_at >= ? and created_at < ? AND feature_type = ?", yesterdayStart, todayStart, "aiagent").Count(&countAIChatRecordsYesterday)
	globals.GraDBs["system"].Model(&model.Order{}).Where("pay_time >= ? and pay_time < ? and status = ?", yesterdayStart, todayStart, model.STATUS_COMPLETE).Select("COALESCE(sum(amount), 0)").Scan(&sumAmountYesterday)

	statisticData := []map[string]interface{}{
		{
			"key":        "newUsers",
			"label":      "New Users",
			"value":      countUserToday,
			"growShrink": countUserYesterday,
		},
		{
			"key":        "sumAmount",
			"label":      "Revenue",
			"value":      sumAmountToday / 100,
			"growShrink": sumAmountYesterday / 100,
		},
		{
			"key":        "newOrders",
			"label":      "New Orders",
			"value":      countOrderToday,
			"growShrink": countOrderYesterday,
		},
		// AI Agent statistics moved to GetToolsStatistics to avoid duplication
	}

	response.OkWithData(gin.H{
		"statisticData": statisticData,
	}, c)
}

// @Tags Admin Dashboard
// @Summary Get core business statistics (AI detection, tool usage, active users)
// @Success 200 {object} response.Response "Core business statistics data"
// @Router /api/admin/dashboard/getCoreBusinessStats [get]
func (s *AdminDashboardApi) GetCoreBusinessStats(c *gin.Context) {
	uid := utils.GetUserID(c)
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if user.Role != "manager" {
		response.FailWithMessage("No permission", c)
		return
	}

	countActiveUsersToday := int64(0)
	countFeedbackToday := int64(0)

	countActiveUsersYesterday := int64(0)
	countFeedbackYesterday := int64(0)

	todayStart := time.Now().Format("2006-01-02")
	yesterdayStart := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	// Core business statistics - today
	globals.GraDBs["system"].Model(&model.UsageRecord{}).Where("created_at >= ?", todayStart).Select("COUNT(DISTINCT uid)").Scan(&countActiveUsersToday)
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("created_at >= ? AND deleted IS NULL", todayStart).Count(&countFeedbackToday)

	// Core business statistics - yesterday comparison
	globals.GraDBs["system"].Model(&model.UsageRecord{}).Where("created_at >= ? and created_at < ?", yesterdayStart, todayStart).Select("COUNT(DISTINCT uid)").Scan(&countActiveUsersYesterday)
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("created_at >= ? AND created_at < ? AND deleted IS NULL", yesterdayStart, todayStart).Count(&countFeedbackYesterday)

	statisticData := []map[string]interface{}{
		{
			"key":        "activeUsers",
			"label":      "Active Users",
			"value":      countActiveUsersToday,
			"growShrink": countActiveUsersYesterday,
		},
		{
			"key":        "newFeedback",
			"label":      "New Feedback",
			"value":      countFeedbackToday,
			"growShrink": countFeedbackYesterday,
		},
	}

	response.OkWithData(gin.H{
		"statisticData": statisticData,
	}, c)
}

// @Tags Admin Dashboard
// @Summary Get tools statistics (individual tool usage)
// @Success 200 {object} response.Response "Tools statistics data"
// @Router /api/admin/dashboard/getToolsStatistics [get]
func (s *AdminDashboardApi) GetToolsStatistics(c *gin.Context) {
	uid := utils.GetUserID(c)
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if user.Role != "manager" {
		response.FailWithMessage("No permission", c)
		return
	}

	todayStart := time.Now().Format("2006-01-02")
	yesterdayStart := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	// Query usage_record table for today's statistics - much more efficient!
	var todayResults []struct {
		FeatureType string
		Count       int64
	}
	globals.GraDBs["system"].Model(&model.UsageRecord{}).
		Select("feature_type, COUNT(*) as count").
		Where("created_at >= ?", todayStart).
		Group("feature_type").
		Scan(&todayResults)

	// Query usage_record table for yesterday's statistics
	var yesterdayResults []struct {
		FeatureType string
		Count       int64
	}
	globals.GraDBs["system"].Model(&model.UsageRecord{}).
		Select("feature_type, COUNT(*) as count").
		Where("created_at >= ? AND created_at < ?", yesterdayStart, todayStart).
		Group("feature_type").
		Scan(&yesterdayResults)

	// Build maps for easy lookup
	todayMap := make(map[string]int64)
	for _, result := range todayResults {
		todayMap[result.FeatureType] = result.Count
	}

	yesterdayMap := make(map[string]int64)
	for _, result := range yesterdayResults {
		yesterdayMap[result.FeatureType] = result.Count
	}

	// Tool configuration: feature_type -> (key, label)
	//
	// One entry per stable feature_type produced by FeatureTypeForToolID.
	// When adding a new tool, register its mapping in
	// service/tools/usage_record.go AND list it here so the dashboard
	// renders a card.
	toolConfigs := []struct {
		featureType string
		key         string
		label       string
	}{
		// Generic media tools (image / video pipelines).
		{"image_generator", "imageGeneratorRecords", "Image Generator"},
		{model.TOOL_VIDEO_GENERATOR, "videoGeneratorRecords", "Video Generator"},
		{"avatar_studio", "avatarStudioRecords", "Avatar Studio"},
		{"image_upscaler", "imageUpscalerRecords", "Image Upscaler"},
		{"background_remover", "backgroundRemoverRecords", "Background Remover"},
		{"image_vectorizer", "imageVectorizerRecords", "Image Vectorizer"},
		{model.TOOL_LORA_STUDIO, "loraStudioRecords", "LoRA Studio"},
		// Prompt tooling.
		{"prompt_builder", "promptBuilderRecords", "Prompt Builder"},
		{"video_prompt_builder", "videoPromptBuilderRecords", "Video Prompt Builder"},
		{model.FEATURE_TYPE_PROMPT_OPTIMIZE, "promptOptimizeRecords", "Prompt Optimize"},
		// Canvas + agent.
		{"canvas_chat", "canvasChatRecords", "Canvas Chat"},
		{model.FEATURE_TYPE_AI_AGENT, "aiAgentRecords", "AI Agent"},
	}

	// Build statistics data
	statisticData := []map[string]interface{}{}
	for _, config := range toolConfigs {
		statisticData = append(statisticData, map[string]interface{}{
			"key":        config.key,
			"label":      config.label,
			"value":      todayMap[config.featureType],
			"growShrink": yesterdayMap[config.featureType],
		})
	}

	response.OkWithData(gin.H{
		"statisticData": statisticData,
	}, c)
}

// @Tags Admin Dashboard
// @Summary Get user region data
// @Success 200 {object} response.Response "User region data"
// @Router /api/admin/dashboard/getUserRegionData [get]
func (s *AdminDashboardApi) GetUserRegionData(c *gin.Context) {
	uid := utils.GetUserID(c)
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if user.Role != "manager" {
		response.FailWithMessage("No permission", c)
		return
	}

	todayStart := time.Now().Format("2006-01-02")
	userType := c.Query("type") // "registered" (default) or "active"

	userList := []model.User{}

	if userType == "active" {
		// Active users: Users who have usage records today
		var userIds []uint
		globals.GraDBs["system"].Model(&model.UsageRecord{}).
			Where("created_at >= ?", todayStart).
			Distinct("uid").
			Pluck("uid", &userIds)

		if len(userIds) > 0 {
			globals.GraDBs["system"].Where("id IN ?", userIds).Find(&userList)
		}
	} else {
		// Registered users: Users created today
		globals.GraDBs["system"].Where("created_at >= ?", todayStart).Find(&userList)
	}

	userRegionMap := make(map[string]int)
	for _, user := range userList {
		address := user.LoginAddress
		address = strings.Split(address, "|")[0]
		userRegionMap[address]++
	}
	sortedRegions := make([]string, 0, len(userRegionMap))
	for region := range userRegionMap {
		sortedRegions = append(sortedRegions, region)
	}

	// Sort regions by user count in descending order
	sort.Slice(sortedRegions, func(i, j int) bool {
		return userRegionMap[sortedRegions[i]] > userRegionMap[sortedRegions[j]]
	})

	userByRegionData := []map[string]interface{}{}
	for _, region := range sortedRegions {
		value := userRegionMap[region]
		country := globals.CountryMap[region]
		if country == "" {
			country = region
		}
		userByRegionData = append(userByRegionData, map[string]interface{}{
			"name":  country,
			"value": value,
		})
	}

	response.OkWithData(gin.H{
		"userByRegionData": userByRegionData,
	}, c)
}

// @Tags Admin Dashboard
// @Summary Get recent orders data
// @Success 200 {object} response.Response "Recent orders data"
// @Router /api/admin/dashboard/getRecentOrdersData [get]
func (s *AdminDashboardApi) GetRecentOrdersData(c *gin.Context) {
	uid := utils.GetUserID(c)
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if user.Role != "manager" {
		response.FailWithMessage("No permission", c)
		return
	}

	todayStart := time.Now().Format("2006-01-02")

	orderList := []model.Order{}
	globals.GraDBs["system"].Where("created_at >= ?", todayStart).Order("created_at desc").Find(&orderList)
	recentOrdersData := []map[string]interface{}{}
	for _, order := range orderList {
		orderUser, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uint(order.UID))
		if err != nil {
			continue
		}
		address := utils.GetClientAddress(order.IP)
		address = strings.Split(address, "|")[0]
		recentOrdersData = append(recentOrdersData, map[string]interface{}{
			"id":          order.Id,
			"name":        order.Name,
			"amount":      order.Amount,
			"avatar":      orderUser.Avatar,
			"user":        orderUser.Nickname,
			"email":       orderUser.Email,
			"status":      order.Status,
			"createdTime": order.CreatedAt,
			"payTime":     order.PayTime,
			"address":     address,
			"mode":        order.OrderMode,
		})
	}

	response.OkWithData(gin.H{
		"recentOrdersData": recentOrdersData,
	}, c)
}

// @Tags Admin Dashboard
// @Summary Get trend data for statistics
// @Param days query int false "Number of days (7, 14, 30, 90)"
// @Success 200 {object} response.Response "Trend data"
// @Router /api/admin/dashboard/getTrendData [get]
func (s *AdminDashboardApi) GetTrendData(c *gin.Context) {
	uid := utils.GetUserID(c)
	user, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uid)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	if user.Role != "manager" {
		response.FailWithMessage("No permission", c)
		return
	}

	days := 7
	if daysStr := c.Query("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && (d == 7 || d == 14 || d == 30 || d == 90) {
			days = d
		}
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	trendData := buildTrendData(todayStart, days)

	response.OkWithData(gin.H{
		"trendData": trendData,
	}, c)
}

// buildTrendData generates trend data for the specified number of days
func buildTrendData(todayStart time.Time, days int) map[string]interface{} {
	dates := make([]string, days)

	// Build date labels (MM-DD format)
	for i := 0; i < days; i++ {
		dayStart := todayStart.AddDate(0, 0, -(days - 1 - i))
		dates[i] = dayStart.Format("01-02")
	}

	// Initialize core statistics data
	newUsersData := make([]int64, days)
	activeUsersData := make([]int64, days)
	revenueData := make([]float64, days)
	newOrdersData := make([]int64, days)

	// Query core statistics for each day
	for i := 0; i < days; i++ {
		dayStart := todayStart.AddDate(0, 0, -(days - 1 - i))
		dayEnd := dayStart.AddDate(0, 0, 1)

		// New users
		var userCount int64
		globals.GraDBs["system"].Model(&model.User{}).
			Where("created_at >= ? AND created_at < ?", dayStart, dayEnd).
			Count(&userCount)
		newUsersData[i] = userCount

		// Active users
		var activeCount int64
		globals.GraDBs["system"].Model(&model.UsageRecord{}).
			Where("created_at >= ? AND created_at < ?", dayStart, dayEnd).
			Distinct("uid").Count(&activeCount)
		activeUsersData[i] = activeCount

		// Revenue (sum amount)
		var sumAmount float64
		globals.GraDBs["system"].Model(&model.Order{}).
			Where("pay_time >= ? AND pay_time < ? AND status = ?", dayStart, dayEnd, model.STATUS_COMPLETE).
			Select("COALESCE(sum(amount), 0)").Scan(&sumAmount)
		revenueData[i] = sumAmount / 100

		// New orders
		var orderCount int64
		globals.GraDBs["system"].Model(&model.Order{}).
			Where("pay_time >= ? AND pay_time < ? AND status = ?", dayStart, dayEnd, model.STATUS_COMPLETE).
			Count(&orderCount)
		newOrdersData[i] = orderCount
	}

	// Query usage data for each day and feature
	type dailyUsage struct {
		FeatureType string
		Date        time.Time
		Count       int64
	}

	var usageList []dailyUsage
	startDate := todayStart.AddDate(0, 0, -(days - 1))

	globals.GraDBs["system"].Model(&model.UsageRecord{}).
		Select("feature_type, DATE(created_at) as date, COUNT(*) as count").
		Where("created_at >= ?", startDate).
		Group("feature_type, DATE(created_at)").
		Find(&usageList)

	dayIndexMap := make(map[string]int, days)
	for i := 0; i < days; i++ {
		dayStart := todayStart.AddDate(0, 0, -(days - 1 - i))
		dayIndexMap[dayStart.Format("2006-01-02")] = i
	}

	featureTypeData := make(map[string][]int64)

	for _, item := range usageList {
		if item.FeatureType == "" {
			continue
		}

		dayIndex, exists := dayIndexMap[item.Date.Format("2006-01-02")]
		if !exists {
			continue
		}

		if _, exists := featureTypeData[item.FeatureType]; !exists {
			featureTypeData[item.FeatureType] = make([]int64, days)
		}
		featureTypeData[item.FeatureType][dayIndex] += item.Count
	}

	// Build series data - start with core statistics
	series := []map[string]interface{}{
		{"name": "New Users", "data": newUsersData},
		{"name": "Active Users", "data": activeUsersData},
		{"name": "Revenue ($)", "data": revenueData},
		{"name": "New Orders", "data": newOrdersData},
	}

	// Add tool usage trends split by feature_type
	featureTypes := make([]string, 0, len(featureTypeData))
	for featureType := range featureTypeData {
		featureTypes = append(featureTypes, featureType)
	}
	sort.Strings(featureTypes)

	for _, featureType := range featureTypes {
		series = append(series, map[string]interface{}{
			"name": featureType,
			"data": featureTypeData[featureType],
		})
	}

	return map[string]interface{}{
		"dates":  dates,
		"series": series,
	}
}
