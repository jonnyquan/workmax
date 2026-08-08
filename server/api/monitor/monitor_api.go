package monitor

import (
	"net/http"
	"server/globals"
	"server/model"
	"server/service"
	"server/utils"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type MonitorApi struct{}

type monitorMetric struct {
	Key             string   `json:"key"`
	Label           string   `json:"label"`
	Value           float64  `json:"value"`
	Unit            string   `json:"unit,omitempty"`
	ComparisonValue *float64 `json:"comparisonValue,omitempty"`
	ComparisonLabel string   `json:"comparisonLabel,omitempty"`
}

type monitorTrendSeries struct {
	Name string      `json:"name"`
	Data interface{} `json:"data"`
}

type monitorRegionPoint struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

type monitorOrderItem struct {
	ID          uint      `json:"id"`
	Name        string    `json:"name"`
	Amount      float64   `json:"amount"`
	User        string    `json:"user"`
	Email       string    `json:"email"`
	Status      string    `json:"status"`
	CreatedTime time.Time `json:"createdTime"`
	PayTime     time.Time `json:"payTime"`
	Address     string    `json:"address"`
	Mode        string    `json:"mode"`
}

func (a *MonitorApi) GetSummary(c *gin.Context) {
	todayStart := time.Now().Format("2006-01-02")
	yesterdayStart := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	countUserToday := int64(0)
	countOrderToday := int64(0)
	countActiveUsersToday := int64(0)
	sumAmountToday := float64(0)

	countUserYesterday := int64(0)
	countOrderYesterday := int64(0)
	countActiveUsersYesterday := int64(0)
	sumAmountYesterday := float64(0)

	db := globals.GraDBs["system"]

	db.Model(&model.User{}).Where("created_at >= ?", todayStart).Count(&countUserToday)
	db.Model(&model.Order{}).Where("pay_time >= ? and status = ?", todayStart, model.STATUS_COMPLETE).Count(&countOrderToday)
	db.Model(&model.UsageRecord{}).Where("created_at >= ?", todayStart).Select("COUNT(DISTINCT uid)").Scan(&countActiveUsersToday)
	db.Model(&model.Order{}).Where("pay_time >= ? and status = ?", todayStart, model.STATUS_COMPLETE).Select("COALESCE(sum(amount), 0)").Scan(&sumAmountToday)

	db.Model(&model.User{}).Where("created_at >= ? and created_at < ?", yesterdayStart, todayStart).Count(&countUserYesterday)
	db.Model(&model.Order{}).Where("pay_time >= ? and pay_time < ? and status = ?", yesterdayStart, todayStart, model.STATUS_COMPLETE).Count(&countOrderYesterday)
	db.Model(&model.UsageRecord{}).Where("created_at >= ? and created_at < ?", yesterdayStart, todayStart).Select("COUNT(DISTINCT uid)").Scan(&countActiveUsersYesterday)
	db.Model(&model.Order{}).Where("pay_time >= ? and pay_time < ? and status = ?", yesterdayStart, todayStart, model.STATUS_COMPLETE).Select("COALESCE(sum(amount), 0)").Scan(&sumAmountYesterday)

	newUsersToday := float64(countUserToday)
	newUsersYesterday := float64(countUserYesterday)
	newOrdersToday := float64(countOrderToday)
	newOrdersYesterday := float64(countOrderYesterday)
	activeUsersToday := float64(countActiveUsersToday)
	activeUsersYesterday := float64(countActiveUsersYesterday)
	revenueToday := sumAmountToday / 100
	revenueYesterday := sumAmountYesterday / 100

	metrics := []monitorMetric{
		{
			Key:             "newUsers",
			Label:           "New Users",
			Value:           newUsersToday,
			ComparisonValue: &newUsersYesterday,
			ComparisonLabel: "昨日",
		},
		{
			Key:             "sumAmount",
			Label:           "Revenue",
			Value:           revenueToday,
			Unit:            "currency",
			ComparisonValue: &revenueYesterday,
			ComparisonLabel: "昨日",
		},
		{
			Key:             "newOrders",
			Label:           "New Orders",
			Value:           newOrdersToday,
			ComparisonValue: &newOrdersYesterday,
			ComparisonLabel: "昨日",
		},
		{
			Key:             "activeUsers",
			Label:           "Active Users",
			Value:           activeUsersToday,
			ComparisonValue: &activeUsersYesterday,
			ComparisonLabel: "昨日",
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"updatedAt": time.Now().Format(time.RFC3339),
		"note":      "今日经营数据，附昨日对比",
		"metrics":   metrics,
	})
}

func (a *MonitorApi) GetTrends(c *gin.Context) {
	days := 7
	if daysStr := c.Query("days"); daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil && (d == 7 || d == 14 || d == 30 || d == 90) {
			days = d
		}
	}

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	trendData := buildMonitorTrendData(todayStart, days)

	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"updatedAt": time.Now().Format(time.RFC3339),
		"trendData": trendData,
	})
}

func (a *MonitorApi) GetDrilldown(c *gin.Context) {
	metricKey := c.Query("metricKey")

	switch metricKey {
	case "newOrders":
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"updatedAt": time.Now().Format(time.RFC3339),
			"detail": gin.H{
				"kind":      "orders",
				"metricKey": metricKey,
				"title":     "New Orders",
				"items":     buildMonitorRecentOrdersData(),
			},
		})
	case "newUsers":
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"updatedAt": time.Now().Format(time.RFC3339),
			"detail": gin.H{
				"kind":      "countries",
				"metricKey": metricKey,
				"title":     "New Users by Country",
				"items":     buildMonitorUserRegionData(false),
			},
		})
	case "activeUsers":
		c.JSON(http.StatusOK, gin.H{
			"status":    "ok",
			"updatedAt": time.Now().Format(time.RFC3339),
			"detail": gin.H{
				"kind":      "countries",
				"metricKey": metricKey,
				"title":     "Active Users by Country",
				"items":     buildMonitorUserRegionData(true),
			},
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "offline",
			"message": "unsupported metricKey",
		})
	}
}

func buildMonitorUserRegionData(activeOnly bool) []monitorRegionPoint {
	todayStart := time.Now().Format("2006-01-02")
	db := globals.GraDBs["system"]

	userList := []model.User{}
	if activeOnly {
		var userIDs []uint
		db.Model(&model.UsageRecord{}).
			Where("created_at >= ?", todayStart).
			Distinct("uid").
			Pluck("uid", &userIDs)

		if len(userIDs) > 0 {
			db.Where("id IN ?", userIDs).Find(&userList)
		}
	} else {
		db.Where("created_at >= ?", todayStart).Find(&userList)
	}

	userRegionMap := make(map[string]int)
	for _, user := range userList {
		address := strings.Split(user.LoginAddress, "|")[0]
		if address == "" {
			address = "Unknown"
		}
		userRegionMap[address]++
	}

	sortedRegions := make([]string, 0, len(userRegionMap))
	for region := range userRegionMap {
		sortedRegions = append(sortedRegions, region)
	}
	sort.Slice(sortedRegions, func(i, j int) bool {
		return userRegionMap[sortedRegions[i]] > userRegionMap[sortedRegions[j]]
	})

	points := make([]monitorRegionPoint, 0, len(sortedRegions))
	for _, region := range sortedRegions {
		country := globals.CountryMap[region]
		if country == "" {
			country = region
		}

		points = append(points, monitorRegionPoint{
			Name:  country,
			Value: userRegionMap[region],
		})
	}

	return points
}

func buildMonitorRecentOrdersData() []monitorOrderItem {
	todayStart := time.Now().Format("2006-01-02")
	db := globals.GraDBs["system"]

	orderList := []model.Order{}
	db.Where("created_at >= ?", todayStart).Order("created_at desc").Limit(50).Find(&orderList)

	items := make([]monitorOrderItem, 0, len(orderList))
	for _, order := range orderList {
		orderUser, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uint(order.UID))
		if err != nil {
			continue
		}

		address := utils.GetClientAddress(order.IP)
		address = strings.Split(address, "|")[0]

		items = append(items, monitorOrderItem{
			ID:          order.Id,
			Name:        order.Name,
			Amount:      float64(order.Amount) / 100,
			User:        orderUser.Nickname,
			Email:       orderUser.Email,
			Status:      order.Status,
			CreatedTime: order.CreatedAt,
			PayTime:     order.PayTime,
			Address:     address,
			Mode:        order.OrderMode,
		})
	}

	return items
}

func buildMonitorTrendData(todayStart time.Time, days int) map[string]interface{} {
	dates := make([]string, days)
	for i := 0; i < days; i++ {
		dayStart := todayStart.AddDate(0, 0, -(days - 1 - i))
		dates[i] = dayStart.Format("01-02")
	}

	newUsersData := make([]int64, days)
	activeUsersData := make([]int64, days)
	revenueData := make([]float64, days)
	newOrdersData := make([]int64, days)

	db := globals.GraDBs["system"]
	for i := 0; i < days; i++ {
		dayStart := todayStart.AddDate(0, 0, -(days - 1 - i))
		dayEnd := dayStart.AddDate(0, 0, 1)

		var userCount int64
		db.Model(&model.User{}).
			Where("created_at >= ? AND created_at < ?", dayStart, dayEnd).
			Count(&userCount)
		newUsersData[i] = userCount

		var activeCount int64
		db.Model(&model.UsageRecord{}).
			Where("created_at >= ? AND created_at < ?", dayStart, dayEnd).
			Distinct("uid").Count(&activeCount)
		activeUsersData[i] = activeCount

		var sumAmount float64
		db.Model(&model.Order{}).
			Where("pay_time >= ? AND pay_time < ? AND status = ?", dayStart, dayEnd, model.STATUS_COMPLETE).
			Select("COALESCE(sum(amount), 0)").Scan(&sumAmount)
		revenueData[i] = sumAmount / 100

		var orderCount int64
		db.Model(&model.Order{}).
			Where("pay_time >= ? AND pay_time < ? AND status = ?", dayStart, dayEnd, model.STATUS_COMPLETE).
			Count(&orderCount)
		newOrdersData[i] = orderCount
	}

	type dailyUsage struct {
		FeatureType string
		Date        time.Time
		Count       int64
	}

	var usageList []dailyUsage
	startDate := todayStart.AddDate(0, 0, -(days - 1))
	db.Model(&model.UsageRecord{}).
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

	series := []monitorTrendSeries{
		{Name: "New Users", Data: newUsersData},
		{Name: "Active Users", Data: activeUsersData},
		{Name: "Revenue ($)", Data: revenueData},
		{Name: "New Orders", Data: newOrdersData},
	}

	featureTypes := make([]string, 0, len(featureTypeData))
	for featureType := range featureTypeData {
		featureTypes = append(featureTypes, featureType)
	}
	sort.Strings(featureTypes)

	for _, featureType := range featureTypes {
		series = append(series, monitorTrendSeries{
			Name: featureType,
			Data: featureTypeData[featureType],
		})
	}

	return map[string]interface{}{
		"dates":  dates,
		"series": series,
	}
}
