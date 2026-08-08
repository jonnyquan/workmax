package admin

import (
	"fmt"
	"net/http"
	"server/globals"
	"server/model"
	"server/scheduler"
	"strconv"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

type AdminPromptApi struct{}

// tagStatsRunning is a per-process lock so two admin clicks (or an
// admin + the 3am cron tick) don't double-burn DB I/O on the same
// recompute. Atomic CAS — never blocks; second click just gets 409.
var tagStatsRunning atomic.Bool

// GetPromptList 获取Prompt列表
func (a *AdminPromptApi) GetPromptList(c *gin.Context) {
	pageIndex, _ := strconv.Atoi(c.DefaultQuery("pageIndex", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	sortOrder := c.DefaultQuery("sort[order]", "desc")
	sortKey := c.DefaultQuery("sort[key]", "created_at")
	query := c.DefaultQuery("query", "")
	filterLang := c.DefaultQuery("filterData[lang]", "")
	filterStatus := c.DefaultQuery("filterData[status]", "")
	filterCategory := c.DefaultQuery("filterData[category]", "")
	filterMedium := c.DefaultQuery("filterData[medium]", "")
	filterPromptType := c.DefaultQuery("filterData[promptType]", "")

	offset := (pageIndex - 1) * pageSize

	db := globals.GraDBs["system"].Model(&model.Prompt{})

	if query != "" {
		db = db.Where("title LIKE ? OR slug LIKE ? OR tags LIKE ?", "%"+query+"%", "%"+query+"%", "%"+query+"%")
	}
	if filterLang != "" && filterLang != "all" {
		db = db.Where("lang = ?", filterLang)
	}
	if filterStatus != "" {
		status, _ := strconv.Atoi(filterStatus)
		db = db.Where("status = ?", status)
	}
	if filterCategory != "" {
		db = db.Where("category = ?", filterCategory)
	}
	if filterMedium != "" {
		db = db.Where("medium = ?", filterMedium)
	}
	if filterPromptType != "" {
		db = db.Where("prompt_type = ?", filterPromptType)
	}

	var total int64
	db.Count(&total)

	if sortKey != "" && sortOrder != "" {
		db = db.Order(sortKey + " " + sortOrder)
	} else {
		db = db.Order("id DESC")
	}

	var prompts []model.Prompt
	if err := db.Limit(pageSize).Offset(offset).Find(&prompts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	data := make([]map[string]interface{}, 0, len(prompts))
	for _, p := range prompts {
		data = append(data, map[string]interface{}{
			"id":                p.Id,
			"slug":              p.Slug,
			"title":             p.Title,
			"lang":              p.Lang,
			"coverImage":        p.CoverImage,
			"thumbImage":        p.ThumbImage,
			"localImage":        p.LocalImage,
			"aspectRatio":       p.AspectRatio,
			"promptPreview":     p.PromptPreview,
			"promptContent":     p.PromptContent,
			"negativePrompt":    p.NegativePrompt,
			"promptDescription": p.PromptDescription,
			"medium":            p.Medium,
			"promptType":        p.PromptType,
			"previewAssetType":  p.PreviewAssetType,
			"previewVideo":      p.PreviewVideo,
			"category":          p.Category,
			"style":             p.Style,
			"tags":              p.Tags,
			"supportedModels":   p.SupportedModels,
			"primaryModel":      p.PrimaryModel,
			"viewCount":         p.ViewCount,
			"copyCount":         p.CopyCount,
			"likeCount":         p.LikeCount,
			"rating":            p.Rating,
			"ratingCount":       p.RatingCount,
			"isFeatured":        p.IsFeatured,
			"isTrending":        p.IsTrending,
			"status":            p.Status,
			"sort":              p.Sort,
			"createdAt":         p.CreatedAt,
			"updatedAt":         p.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  data,
		"total": total,
	})
}

// GetPrompt 获取单个Prompt详情
func (a *AdminPromptApi) GetPrompt(c *gin.Context) {
	id, _ := strconv.Atoi(c.Query("id"))
	if id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	var prompt model.Prompt
	if err := globals.GraDBs["system"].First(&prompt, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "prompt not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"data": prompt})
}

// UpdatePrompt 更新Prompt
func (a *AdminPromptApi) UpdatePrompt(c *gin.Context) {
	type UpdateRequest struct {
		ID         uint    `json:"id"`
		Status     *int    `json:"status"`
		Sort       *int    `json:"sort"`
		IsFeatured *bool   `json:"isFeatured"`
		IsTrending *bool   `json:"isTrending"`
		Title      *string `json:"title"`
		Category   *string `json:"category"`
		Style      *string `json:"style"`
	}

	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	updates := make(map[string]interface{})
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.Sort != nil {
		updates["sort"] = *req.Sort
	}
	if req.IsFeatured != nil {
		updates["is_featured"] = *req.IsFeatured
	}
	if req.IsTrending != nil {
		updates["is_trending"] = *req.IsTrending
	}
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.Category != nil {
		updates["category"] = *req.Category
	}
	if req.Style != nil {
		updates["style"] = *req.Style
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	if err := globals.GraDBs["system"].Model(&model.Prompt{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// DeletePrompt 删除Prompt
func (a *AdminPromptApi) DeletePrompt(c *gin.Context) {
	type DeleteRequest struct {
		ID uint `json:"id"`
	}

	var req DeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	if err := globals.GraDBs["system"].Delete(&model.Prompt{}, req.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// GetPromptCategoryList 获取Prompt分类列表
func (a *AdminPromptApi) GetPromptCategoryList(c *gin.Context) {
	pageIndex, _ := strconv.Atoi(c.DefaultQuery("pageIndex", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	filterLang := c.DefaultQuery("filterData[lang]", "en")
	filterStatus := c.DefaultQuery("filterData[status]", "")

	offset := (pageIndex - 1) * pageSize

	db := globals.GraDBs["system"].Model(&model.PromptCategory{})

	if filterLang != "" && filterLang != "all" {
		db = db.Where("lang = ?", filterLang)
	}
	if filterStatus != "" {
		status, _ := strconv.Atoi(filterStatus)
		db = db.Where("status = ?", status)
	}

	var total int64
	db.Count(&total)

	var categories []model.PromptCategory
	if err := db.Order("sort DESC, id DESC").Limit(pageSize).Offset(offset).Find(&categories).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	data := make([]map[string]interface{}, 0, len(categories))
	for _, cat := range categories {
		data = append(data, map[string]interface{}{
			"id":             cat.Id,
			"code":           cat.Code,
			"name":           cat.Name,
			"lang":           cat.Lang,
			"mainCategoryID": cat.MainCategoryID,
			"icon":           cat.Icon,
			"color":          cat.Color,
			"promptCount":    cat.PromptCount,
			"sort":           cat.Sort,
			"status":         cat.Status,
			"createdAt":      cat.CreatedAt,
			"updatedAt":      cat.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  data,
		"total": total,
	})
}

// UpdatePromptCategory 更新Prompt分类
func (a *AdminPromptApi) UpdatePromptCategory(c *gin.Context) {
	type UpdateRequest struct {
		ID     uint    `json:"id"`
		Code   *string `json:"code"`
		Name   *string `json:"name"`
		Icon   *string `json:"icon"`
		Color  *string `json:"color"`
		Sort   *int    `json:"sort"`
		Status *int    `json:"status"`
	}

	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Icon != nil {
		updates["icon"] = *req.Icon
	}
	if req.Color != nil {
		updates["color"] = *req.Color
	}
	if req.Sort != nil {
		updates["sort"] = *req.Sort
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	if err := globals.GraDBs["system"].Model(&model.PromptCategory{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// GetPromptTagList 获取Prompt标签列表
func (a *AdminPromptApi) GetPromptTagList(c *gin.Context) {
	pageIndex, _ := strconv.Atoi(c.DefaultQuery("pageIndex", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	filterLang := c.DefaultQuery("filterData[lang]", "en")
	filterStatus := c.DefaultQuery("filterData[status]", "")
	filterIsPopular := c.DefaultQuery("filterData[isPopular]", "")

	offset := (pageIndex - 1) * pageSize

	db := globals.GraDBs["system"].Model(&model.PromptTag{})

	if filterLang != "" && filterLang != "all" {
		db = db.Where("lang = ?", filterLang)
	}
	if filterStatus != "" {
		status, _ := strconv.Atoi(filterStatus)
		db = db.Where("status = ?", status)
	}
	if filterIsPopular != "" {
		db = db.Where("is_popular = ?", filterIsPopular == "1" || filterIsPopular == "true")
	}

	var total int64
	db.Count(&total)

	var tags []model.PromptTag
	if err := db.Order("sort DESC, trend_score DESC, id DESC").Limit(pageSize).Offset(offset).Find(&tags).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	data := make([]map[string]interface{}, 0, len(tags))
	for _, t := range tags {
		data = append(data, map[string]interface{}{
			"id":         t.Id,
			"name":       t.Name,
			"slug":       t.Slug,
			"lang":       t.Lang,
			"category":   t.Category,
			"usageCount": t.UsageCount,
			"trendScore": t.TrendScore,
			"isPopular":  t.IsPopular,
			"sort":       t.Sort,
			"status":     t.Status,
			"createdAt":  t.CreatedAt,
			"updatedAt":  t.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"data":  data,
		"total": total,
	})
}

// UpdatePromptTag 更新Prompt标签
func (a *AdminPromptApi) UpdatePromptTag(c *gin.Context) {
	type UpdateRequest struct {
		ID         uint    `json:"id"`
		Name       *string `json:"name"`
		Category   *string `json:"category"`
		IsPopular  *bool   `json:"isPopular"`
		Sort       *int    `json:"sort"`
		Status     *int    `json:"status"`
		TrendScore *int    `json:"trendScore"`
	}

	var req UpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Category != nil {
		updates["category"] = *req.Category
	}
	if req.IsPopular != nil {
		updates["is_popular"] = *req.IsPopular
	}
	if req.Sort != nil {
		updates["sort"] = *req.Sort
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.TrendScore != nil {
		updates["trend_score"] = *req.TrendScore
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
		return
	}

	if err := globals.GraDBs["system"].Model(&model.PromptTag{}).Where("id = ?", req.ID).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// DeletePromptTag 删除Prompt标签
func (a *AdminPromptApi) DeletePromptTag(c *gin.Context) {
	type DeleteRequest struct {
		ID uint `json:"id"`
	}

	var req DeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.ID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id is required"})
		return
	}

	if err := globals.GraDBs["system"].Delete(&model.PromptTag{}, req.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"success": true})
}

// RunPromptTagStats triggers selected TagStatsScheduler phases on
// demand from the admin UI. Async — returns immediately while the job
// runs in a goroutine, since refresh ≈ 60s and search-vector rebuild
// can take many minutes on remote DB. Logs land in logs/workmax.log.
//
// POST /admin/prompt/runTagStats
//
//	{ "refresh": true, "popularity": true, "searchVectors": false }
//
// At least one phase must be true. 409 if a job is already running.
func (a *AdminPromptApi) RunPromptTagStats(c *gin.Context) {
	var req struct {
		Refresh       bool `json:"refresh"`
		Popularity    bool `json:"popularity"`
		SearchVectors bool `json:"searchVectors"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !req.Refresh && !req.Popularity && !req.SearchVectors {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one phase required (refresh / popularity / searchVectors)"})
		return
	}
	if !tagStatsRunning.CompareAndSwap(false, true) {
		c.JSON(http.StatusConflict, gin.H{"error": "tag stats job already running; check logs"})
		return
	}

	phases := make([]string, 0, 3)
	if req.Refresh {
		phases = append(phases, "refresh")
	}
	if req.Popularity {
		phases = append(phases, "popularity")
	}
	if req.SearchVectors {
		phases = append(phases, "searchVectors")
	}

	go func() {
		defer tagStatsRunning.Store(false)
		defer func() {
			if r := recover(); r != nil {
				globals.Error(fmt.Sprintf("Tag stats: admin-triggered job panicked: %v", r))
			}
		}()
		globals.Info(fmt.Sprintf("Tag stats: admin-triggered job phases=%v", phases))
		s := scheduler.NewTagStatsScheduler()
		if req.Refresh {
			s.RunRefresh()
		}
		if req.Popularity {
			s.RunDaily()
		}
		if req.SearchVectors {
			s.RunSearchVectors()
		}
	}()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"phases":  phases,
		"message": "tag stats job started; tail logs/workmax.log for progress",
	})
}
