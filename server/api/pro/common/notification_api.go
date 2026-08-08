package common

import (
	"net/http"
	"server/service"
	"server/utils"
	"strconv"

	"github.com/gin-gonic/gin"
)

type NotificationApi struct {
}

type notificationListItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Content       string `json:"content"`
	Date          string `json:"date"`
	Image         string `json:"image"`
	Type          int    `json:"type"`
	Location      string `json:"location"`
	LocationLabel string `json:"locationLabel"`
	Status        string `json:"status"`
	Readed        bool   `json:"readed"`
}

const (
	notificationListDateFormat = "2006-01-02 15:04"
	notificationStatusRead     = "read"
	notificationStatusUnread   = "unread"
)

// GetNotificationCount returns the unread notification count for the current user.
// GET /api/common/notification/count
func (n *NotificationApi) GetNotificationCount(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": 0, "msg": "unauthorized"})
		return
	}

	count, err := service.GroupServiceApp.CommonServiceGroup.NotificationService.UnreadCountByUser(uid)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 500, "data": 0, "msg": "failed to load unread count"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": count,
		"msg":  "获取通知数量成功",
	})
}

// GetNotificationList returns the most recent notifications for the current user.
// GET /api/common/notification/list?limit=50
func (n *NotificationApi) GetNotificationList(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": []notificationListItem{}, "msg": "unauthorized"})
		return
	}

	limit := 0
	if raw := c.Query("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	rows, err := service.GroupServiceApp.CommonServiceGroup.NotificationService.ListByUser(uid, limit)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 500, "data": []notificationListItem{}, "msg": "failed to load notifications"})
		return
	}

	items := make([]notificationListItem, 0, len(rows))
	for _, row := range rows {
		status := notificationStatusUnread
		if row.Readed {
			status = notificationStatusRead
		}
		items = append(items, notificationListItem{
			ID:            strconv.FormatUint(uint64(row.Id), 10),
			Title:         row.Title,
			Content:       row.Content,
			Date:          row.CreatedAt.Format(notificationListDateFormat),
			Image:         row.Image,
			Type:          row.Type,
			Location:      row.Location,
			LocationLabel: row.LocationLabel,
			Status:        status,
			Readed:        row.Readed,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": items,
		"msg":  "获取通知列表成功",
	})
}

// GetNotificationDetail returns a single notification and marks it read.
// GET /api/common/notification/detail?id=123
func (n *NotificationApi) GetNotificationDetail(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusOK, gin.H{"code": 401, "data": nil, "msg": "unauthorized"})
		return
	}

	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusOK, gin.H{"code": 400, "data": nil, "msg": "invalid notification id"})
		return
	}

	svc := service.GroupServiceApp.CommonServiceGroup.NotificationService
	row, err := svc.GetDetail(uid, uint(id))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 500, "data": nil, "msg": "failed to load notification"})
		return
	}
	if row == nil {
		c.JSON(http.StatusOK, gin.H{"code": 404, "data": nil, "msg": "notification not found"})
		return
	}

	if !row.Readed {
		if err := svc.MarkRead(uid, row.Id); err == nil {
			row.Readed = true
		}
	}

	status := notificationStatusUnread
	if row.Readed {
		status = notificationStatusRead
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 200,
		"data": notificationListItem{
			ID:            strconv.FormatUint(uint64(row.Id), 10),
			Title:         row.Title,
			Content:       row.Content,
			Date:          row.CreatedAt.Format(notificationListDateFormat),
			Image:         row.Image,
			Type:          row.Type,
			Location:      row.Location,
			LocationLabel: row.LocationLabel,
			Status:        status,
			Readed:        row.Readed,
		},
		"msg": "获取通知详情成功",
	})
}

// MarkNotificationRead marks a single notification as read.
// POST /api/common/notification/read { "id": "123" }
func (n *NotificationApi) MarkNotificationRead(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusOK, gin.H{"code": 401, "data": nil, "msg": "unauthorized"})
		return
	}

	var body struct {
		ID string `json:"id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 400, "data": nil, "msg": "invalid request"})
		return
	}
	id, err := strconv.ParseUint(body.ID, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusOK, gin.H{"code": 400, "data": nil, "msg": "invalid notification id"})
		return
	}

	if err := service.GroupServiceApp.CommonServiceGroup.NotificationService.MarkRead(uid, uint(id)); err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 500, "data": nil, "msg": "failed to mark as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 200, "data": nil, "msg": "标记已读成功"})
}

// MarkAllNotificationsRead marks every unread notification for the current user as read.
// POST /api/common/notification/read-all
func (n *NotificationApi) MarkAllNotificationsRead(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusOK, gin.H{"code": 401, "data": nil, "msg": "unauthorized"})
		return
	}

	affected, err := service.GroupServiceApp.CommonServiceGroup.NotificationService.MarkAllRead(uid)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"code": 500, "data": nil, "msg": "failed to mark all as read"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 200, "data": affected, "msg": "全部标记已读成功"})
}
