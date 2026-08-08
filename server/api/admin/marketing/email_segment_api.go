package marketing

import (
	"server/model"
	"server/service"
	"strconv"

	"github.com/gin-gonic/gin"
)

type EmailSegmentApi struct{}

var emailSegmentService = service.GroupServiceApp.MarketingServiceGroup.EmailSegmentService

// GetSegmentList 获取分组列表
func (a *EmailSegmentApi) GetSegmentList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	statusStr := c.Query("status")
	keyword := c.Query("keyword")

	var status *int
	if statusStr != "" {
		s, _ := strconv.Atoi(statusStr)
		status = &s
	}

	segments, total, err := emailSegmentService.GetSegmentList(page, pageSize, status, keyword)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list":     segments,
			"total":    total,
			"page":     page,
			"pageSize": pageSize,
		},
	})
}

// GetSegmentByID 获取分组详情
func (a *EmailSegmentApi) GetSegmentByID(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	segment, err := emailSegmentService.GetSegmentByID(id)
	if err != nil {
		c.JSON(404, gin.H{"code": 404, "message": "Segment not found"})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data":    segment,
	})
}

// CreateSegment 创建分组
func (a *EmailSegmentApi) CreateSegment(c *gin.Context) {
	var segment model.EmailSegment
	if err := c.ShouldBindJSON(&segment); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := emailSegmentService.CreateSegment(&segment); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Segment created successfully",
		"data":    segment,
	})
}

// UpdateSegment 更新分组
func (a *EmailSegmentApi) UpdateSegment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var segment model.EmailSegment
	if err := c.ShouldBindJSON(&segment); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	segment.Id = uint(id)
	if err := emailSegmentService.UpdateSegment(&segment); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Segment updated successfully",
	})
}

// DeleteSegment 删除分组
func (a *EmailSegmentApi) DeleteSegment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	if err := emailSegmentService.DeleteSegment(id); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Segment deleted successfully",
	})
}

// GetSegmentUsers 获取分组用户列表（带分页）
func (a *EmailSegmentApi) GetSegmentUsers(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))

	users, total, err := emailSegmentService.GetSegmentUsersWithPagination(id, page, pageSize)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list":     users,
			"total":    total,
			"page":     page,
			"pageSize": pageSize,
		},
	})
}

// SyncSegmentUsers 同步分组用户数
func (a *EmailSegmentApi) SyncSegmentUsers(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	if err := emailSegmentService.SyncSegmentUserCount(id); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Segment synced successfully",
	})
}
