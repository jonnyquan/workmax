package admin

import (
	"net/http"
	"server/globals"
	"server/model"
	storageService "server/service/storage"
	toolsService "server/service/tools"
	"server/service/tools/provider"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type AdminGeneratorApi struct{}

// ProviderRequest 提供商请求
type ProviderRequest struct {
	Name            string `json:"name" binding:"required"`
	Type            string `json:"type" binding:"required"`
	MediaType       string `json:"mediaType"`
	Enabled         bool   `json:"enabled"`
	IsDefault       bool   `json:"isDefault"`
	Priority        int    `json:"priority"`
	Endpoint        string `json:"endpoint"`
	APIKey          string `json:"apiKey"`
	Model           string `json:"model"`
	DailyQuota      int    `json:"dailyQuota"`
	MonthlyQuota    int    `json:"monthlyQuota"`
	ConcurrentLimit int    `json:"concurrentLimit"`
	ExtraConfig     string `json:"extraConfig"`
	Description     string `json:"description"`
}

type CleanupGenerationObjectsRequest struct {
	OlderThanHours int `json:"olderThanHours"`
	Limit          int `json:"limit"`
}

type BackfillGenerationObjectsRequest struct {
	AfterRecordID uint `json:"afterRecordId"`
	Limit         int  `json:"limit"`
	DryRun        bool `json:"dryRun"`
}

type AuditGenerationObjectsRequest struct {
	AfterRecordID uint `json:"afterRecordId"`
	Limit         int  `json:"limit"`
}

type GenerationObjectsListItem struct {
	ID          uint   `json:"id"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
	UID         int    `json:"uid"`
	TaskID      string `json:"taskId"`
	RecordID    uint   `json:"recordId"`
	ToolID      string `json:"toolId"`
	Provider    string `json:"provider"`
	Bucket      string `json:"bucket"`
	ObjectKey   string `json:"objectKey"`
	AssetKind   string `json:"assetKind"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	ETag        string `json:"etag"`
	PublicURL   string `json:"publicUrl"`
	SourceURL   string `json:"sourceUrl"`
	Status      int8   `json:"status"`
	StatusLabel string `json:"statusLabel"`
}

type StorageObjectDownloadURLItem struct {
	ID            uint   `json:"id"`
	Provider      string `json:"provider"`
	Bucket        string `json:"bucket"`
	ObjectKey     string `json:"objectKey"`
	URL           string `json:"url"`
	IsPresigned   bool   `json:"isPresigned"`
	ExpiresInSecs int    `json:"expiresInSecs"`
}

type StorageAccessSummary struct {
	Provider      string `json:"provider"`
	Bucket        string `json:"bucket"`
	Mode          string `json:"mode"`
	ExpiresInSecs int    `json:"expiresInSecs"`
}

type StorageUploadSummary struct {
	Mode                    string   `json:"mode"`
	MultipartThresholdBytes int64    `json:"multipartThresholdBytes"`
	MultipartPartSizeBytes  int64    `json:"multipartPartSizeBytes"`
	AllowedRemoteAssetHosts []string `json:"allowedRemoteAssetHosts"`
}

type BackfillGenerationObjectsResponse struct {
	AfterRecordID   uint `json:"afterRecordId"`
	Limit           int  `json:"limit"`
	DryRun          bool `json:"dryRun"`
	ScannedRecords  int  `json:"scannedRecords"`
	MatchedURLs     int  `json:"matchedUrls"`
	CreatedObjects  int  `json:"createdObjects"`
	ExistingObjects int  `json:"existingObjects"`
	SkippedURLs     int  `json:"skippedUrls"`
	LastRecordID    uint `json:"lastRecordId"`
}

type AuditGenerationObjectsResponse struct {
	AfterRecordID  uint                                              `json:"afterRecordId"`
	Limit          int                                               `json:"limit"`
	ScannedRecords int                                               `json:"scannedRecords"`
	LastRecordID   uint                                              `json:"lastRecordId"`
	ManagedURLs    int                                               `json:"managedUrls"`
	RegisteredURLs int                                               `json:"registeredUrls"`
	MissingObjects int                                               `json:"missingObjects"`
	ExternalURLs   int                                               `json:"externalUrls"`
	LocalURLs      int                                               `json:"localUrls"`
	InvalidURLs    int                                               `json:"invalidUrls"`
	SampledIssues  []toolsService.GenerationObjectCoverageAuditIssue `json:"sampledIssues"`
}

// GetProviderList 获取提供商列表
// @Tags 管理员-生成器管理
// @Summary 获取提供商列表
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers [get]
func (a *AdminGeneratorApi) GetProviderList(c *gin.Context) {
	var providers []model.GeneratorProvider
	if err := globals.GraDBs["system"].Order("priority DESC").Find(&providers).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to get providers: " + err.Error(),
		})
		return
	}

	// 隐藏 API Key
	for i := range providers {
		if providers[i].APIKey != "" {
			providers[i].APIKey = "********"
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    providers,
	})
}

// GetProvider 获取单个提供商
// @Tags 管理员-生成器管理
// @Summary 获取单个提供商
// @Param id path int true "提供商ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers/{id} [get]
func (a *AdminGeneratorApi) GetProvider(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid ID",
		})
		return
	}

	var p model.GeneratorProvider
	if err := globals.GraDBs["system"].First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    404,
			"message": "Provider not found",
		})
		return
	}

	// 隐藏 API Key
	if p.APIKey != "" {
		p.APIKey = "********"
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    p,
	})
}

// CreateProvider 创建提供商
// @Tags 管理员-生成器管理
// @Summary 创建提供商
// @Param request body ProviderRequest true "提供商信息"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers [post]
func (a *AdminGeneratorApi) CreateProvider(c *gin.Context) {
	var req ProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid request: " + err.Error(),
		})
		return
	}

	// 检查名称是否已存在
	var count int64
	globals.GraDBs["system"].Model(&model.GeneratorProvider{}).Where("name = ?", req.Name).Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{
			"code":    409,
			"message": "Provider name already exists",
		})
		return
	}

	p := model.GeneratorProvider{
		Name:            req.Name,
		Type:            req.Type,
		MediaType:       req.MediaType,
		Enabled:         req.Enabled,
		IsDefault:       req.IsDefault,
		Priority:        req.Priority,
		Endpoint:        req.Endpoint,
		APIKey:          req.APIKey,
		Model:           req.Model,
		DailyQuota:      req.DailyQuota,
		MonthlyQuota:    req.MonthlyQuota,
		ConcurrentLimit: req.ConcurrentLimit,
		ExtraConfig:     req.ExtraConfig,
		Description:     req.Description,
	}
	if p.MediaType == "" {
		p.MediaType = model.MediaTypeImage
	}

	if err := globals.GraDBs["system"].Create(&p).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to create provider: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    p,
	})
}

// UpdateProvider 更新提供商
// @Tags 管理员-生成器管理
// @Summary 更新提供商
// @Param id path int true "提供商ID"
// @Param request body ProviderRequest true "提供商信息"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers/{id} [put]
func (a *AdminGeneratorApi) UpdateProvider(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid ID",
		})
		return
	}

	var req ProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid request: " + err.Error(),
		})
		return
	}

	var p model.GeneratorProvider
	if err := globals.GraDBs["system"].First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    404,
			"message": "Provider not found",
		})
		return
	}

	// 更新字段
	updates := map[string]interface{}{
		"name":             req.Name,
		"type":             req.Type,
		"media_type":       req.MediaType,
		"enabled":          req.Enabled,
		"is_default":       req.IsDefault,
		"priority":         req.Priority,
		"endpoint":         req.Endpoint,
		"model":            req.Model,
		"daily_quota":      req.DailyQuota,
		"monthly_quota":    req.MonthlyQuota,
		"concurrent_limit": req.ConcurrentLimit,
		"extra_config":     req.ExtraConfig,
		"description":      req.Description,
	}
	if req.MediaType == "" {
		updates["media_type"] = model.MediaTypeImage
	}

	// 只有当 API Key 不是占位符时才更新
	if req.APIKey != "" && req.APIKey != "********" {
		updates["api_key"] = req.APIKey
	}

	if err := globals.GraDBs["system"].Model(&p).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to update provider: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
	})
}

// DeleteProvider 删除提供商
// @Tags 管理员-生成器管理
// @Summary 删除提供商
// @Param id path int true "提供商ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers/{id} [delete]
func (a *AdminGeneratorApi) DeleteProvider(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid ID",
		})
		return
	}

	if err := globals.GraDBs["system"].Delete(&model.GeneratorProvider{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to delete provider: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
	})
}

// ToggleProvider 启用/禁用提供商
// @Tags 管理员-生成器管理
// @Summary 启用/禁用提供商
// @Param id path int true "提供商ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers/{id}/toggle [post]
func (a *AdminGeneratorApi) ToggleProvider(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid ID",
		})
		return
	}

	var p model.GeneratorProvider
	if err := globals.GraDBs["system"].First(&p, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    404,
			"message": "Provider not found",
		})
		return
	}

	// 切换启用状态
	newEnabled := !p.Enabled
	if err := globals.GraDBs["system"].Model(&p).Update("enabled", newEnabled).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to toggle provider: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"enabled": newEnabled,
		},
	})
}

// ReloadProviders 重新加载所有提供商
// @Tags 管理员-生成器管理
// @Summary 重新加载所有提供商
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers/reload [post]
func (a *AdminGeneratorApi) ReloadProviders(c *gin.Context) {
	// Provider 配置不再缓存——每次路由都直接查 DB。这里保留路由是为了兼容
	// 旧前端，调用始终成功，行为等价于无操作。
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "Providers reloaded successfully (no-op: config is read directly from DB)",
	})
}

// GetActiveProviders 获取当前活跃的提供商
// @Tags 管理员-生成器管理
// @Summary 获取当前活跃的提供商
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/providers/active [get]
func (a *AdminGeneratorApi) GetActiveProviders(c *gin.Context) {
	providers := provider.GetAllProviders()

	result := make([]gin.H, 0, len(providers))
	for _, p := range providers {
		result = append(result, gin.H{
			"name":    p.Name(),
			"type":    p.Type(),
			"enabled": p.IsEnabled(),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

// GetStorageSummary 获取生成对象存储摘要
// @Tags 管理员-生成器管理
// @Summary 获取存储对象摘要
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/storage/summary [get]
func (a *AdminGeneratorApi) GetStorageSummary(c *gin.Context) {
	db := globals.GraDBs["system"]
	since := time.Now().Add(-24 * time.Hour)

	statusCounts := map[string]int64{
		"active":  0,
		"deleted": 0,
		"orphan":  0,
	}
	type groupedCount struct {
		Status int8
		Count  int64
	}
	var groupedStatuses []groupedCount
	if err := db.Model(&model.GenerationObject{}).
		Select("status, COUNT(*) AS count").
		Group("status").
		Scan(&groupedStatuses).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to get storage summary: " + err.Error(),
		})
		return
	}
	for _, item := range groupedStatuses {
		statusCounts[generationObjectStatusLabel(item.Status)] = item.Count
	}

	type kindCount struct {
		AssetKind string
		Count     int64
	}
	var kindCounts []kindCount
	if err := db.Model(&model.GenerationObject{}).
		Select("asset_kind, COUNT(*) AS count").
		Group("asset_kind").
		Order("count DESC").
		Scan(&kindCounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to get storage summary: " + err.Error(),
		})
		return
	}

	var totalObjects int64
	var totalBytes int64
	db.Model(&model.GenerationObject{}).Count(&totalObjects)
	db.Model(&model.GenerationObject{}).Select("COALESCE(SUM(size_bytes), 0)").Scan(&totalBytes)

	var recentCreated int64
	var recentOrphan int64
	var recentDeleted int64
	db.Model(&model.GenerationObject{}).Where("created_at >= ?", since).Count(&recentCreated)
	db.Model(&model.GenerationObject{}).Where("updated_at >= ? AND status = ?", since, model.GenerationObjectStatusOrphan).Count(&recentOrphan)
	db.Model(&model.GenerationObject{}).Where("updated_at >= ? AND status = ?", since, model.GenerationObjectStatusDeleted).Count(&recentDeleted)
	accessSummary := resolveStorageAccessSummary()
	uploadSummary := resolveStorageUploadSummary()

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"totals": gin.H{
				"objects":   totalObjects,
				"sizeBytes": totalBytes,
			},
			"recent24h": gin.H{
				"created": recentCreated,
				"orphan":  recentOrphan,
				"deleted": recentDeleted,
			},
			"access":     accessSummary,
			"upload":     uploadSummary,
			"statuses":   statusCounts,
			"assetKinds": kindCounts,
		},
	})
}

// ListStorageObjects 获取生成对象列表
// @Tags 管理员-生成器管理
// @Summary 获取存储对象列表
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/storage/objects [get]
func (a *AdminGeneratorApi) ListStorageObjects(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	status := normalizeGenerationObjectStatusFilter(c.Query("status"))
	assetKindFilter := c.Query("assetKind")
	keyword := strings.TrimSpace(c.Query("keyword"))
	sortBy := strings.ToLower(strings.TrimSpace(c.DefaultQuery("sortBy", "createdAt")))
	sortOrder := strings.ToLower(strings.TrimSpace(c.DefaultQuery("sortOrder", "desc")))

	db := globals.GraDBs["system"].Model(&model.GenerationObject{})
	if status > 0 {
		db = db.Where("status = ?", status)
	}
	if assetKindFilter = normalizeOptionalFilter(assetKindFilter); assetKindFilter != "" {
		db = db.Where("asset_kind = ?", assetKindFilter)
	}
	if keyword != "" {
		likeKeyword := "%" + keyword + "%"
		db = db.Where(
			"task_id LIKE ? OR object_key LIKE ? OR bucket LIKE ? OR public_url LIKE ? OR source_url LIKE ? OR CAST(record_id AS CHAR) = ?",
			likeKeyword,
			likeKeyword,
			likeKeyword,
			likeKeyword,
			likeKeyword,
			keyword,
		)
	}

	orderColumn := "created_at"
	switch sortBy {
	case "size", "sizebytes":
		orderColumn = "size_bytes"
	case "updated", "updatedat":
		orderColumn = "updated_at"
	case "created", "createdat":
		orderColumn = "created_at"
	}
	if sortOrder != "asc" {
		sortOrder = "desc"
	}
	orderExpr := orderColumn + " " + sortOrder

	var total int64
	if err := db.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to list storage objects: " + err.Error(),
		})
		return
	}

	var objects []model.GenerationObject
	if err := db.Order(orderExpr).
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&objects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to list storage objects: " + err.Error(),
		})
		return
	}

	items := make([]GenerationObjectsListItem, 0, len(objects))
	for _, object := range objects {
		items = append(items, GenerationObjectsListItem{
			ID:          uint(object.Id),
			CreatedAt:   object.CreatedAt.Format(time.RFC3339),
			UpdatedAt:   object.UpdatedAt.Format(time.RFC3339),
			UID:         object.UID,
			TaskID:      object.TaskID,
			RecordID:    object.RecordID,
			ToolID:      object.ToolID,
			Provider:    object.Provider,
			Bucket:      object.Bucket,
			ObjectKey:   object.ObjectKey,
			AssetKind:   object.AssetKind,
			ContentType: object.ContentType,
			SizeBytes:   object.SizeBytes,
			ETag:        object.ETag,
			PublicURL:   object.PublicURL,
			SourceURL:   object.SourceURL,
			Status:      object.Status,
			StatusLabel: generationObjectStatusLabel(object.Status),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"items": items,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// CleanupOrphanObjects 清理对象存储中的孤儿对象
// @Tags 管理员-生成器管理
// @Summary 清理孤儿对象
// @Param request body CleanupGenerationObjectsRequest false "清理参数"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/storage/orphan-objects/cleanup [post]
func (a *AdminGeneratorApi) CleanupOrphanObjects(c *gin.Context) {
	var req CleanupGenerationObjectsRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid request: " + err.Error(),
		})
		return
	}

	olderThanHours := req.OlderThanHours
	if olderThanHours <= 0 {
		olderThanHours = 24
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	result, err := (&toolsService.GenerationObjectService{}).CleanupOrphanGenerationObjects(
		c.Request.Context(),
		time.Duration(olderThanHours)*time.Hour,
		limit,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to cleanup orphan objects: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"olderThanHours": olderThanHours,
			"limit":          limit,
			"scanned":        result.Scanned,
			"deleted":        result.Deleted,
			"skipped":        result.Skipped,
		},
	})
}

// BackfillGenerationObjects 回填历史对象台账
// @Tags 管理员-生成器管理
// @Summary 回填历史对象台账
// @Param request body BackfillGenerationObjectsRequest false "回填参数"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/storage/backfill [post]
func (a *AdminGeneratorApi) BackfillGenerationObjects(c *gin.Context) {
	var req BackfillGenerationObjectsRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid request: " + err.Error(),
		})
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	result, err := (&toolsService.GenerationObjectService{}).BackfillGenerationObjects(
		c.Request.Context(),
		req.AfterRecordID,
		limit,
		req.DryRun,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to backfill generation objects: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": BackfillGenerationObjectsResponse{
			AfterRecordID:   req.AfterRecordID,
			Limit:           limit,
			DryRun:          req.DryRun,
			ScannedRecords:  result.ScannedRecords,
			MatchedURLs:     result.MatchedURLs,
			CreatedObjects:  result.CreatedObjects,
			ExistingObjects: result.ExistingObjects,
			SkippedURLs:     result.SkippedURLs,
			LastRecordID:    result.LastRecordID,
		},
	})
}

// AuditGenerationObjects 巡检历史对象覆盖率
// @Tags 管理员-生成器管理
// @Summary 巡检历史对象覆盖率
// @Param request body AuditGenerationObjectsRequest false "巡检参数"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/storage/audit [post]
func (a *AdminGeneratorApi) AuditGenerationObjects(c *gin.Context) {
	var req AuditGenerationObjectsRequest
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid request: " + err.Error(),
		})
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	result, err := (&toolsService.GenerationObjectService{}).AuditGenerationObjectCoverage(
		c.Request.Context(),
		req.AfterRecordID,
		limit,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to audit generation objects: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": AuditGenerationObjectsResponse{
			AfterRecordID:  req.AfterRecordID,
			Limit:          limit,
			ScannedRecords: result.ScannedRecords,
			LastRecordID:   result.LastRecordID,
			ManagedURLs:    result.ManagedURLs,
			RegisteredURLs: result.RegisteredURLs,
			MissingObjects: result.MissingObjects,
			ExternalURLs:   result.ExternalURLs,
			LocalURLs:      result.LocalURLs,
			InvalidURLs:    result.InvalidURLs,
			SampledIssues:  result.SampledIssues,
		},
	})
}

// GetStorageObjectDownloadURL 获取对象下载URL（支持签名URL）
// @Tags 管理员-生成器管理
// @Summary 获取对象下载URL
// @Param id path int true "对象ID"
// @Param expires query int false "过期秒数"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/generator/storage/objects/{id}/download-url [get]
func (a *AdminGeneratorApi) GetStorageObjectDownloadURL(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    400,
			"message": "Invalid ID",
		})
		return
	}

	var object model.GenerationObject
	if err := globals.GraDBs["system"].First(&object, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    404,
			"message": "Storage object not found",
		})
		return
	}

	expiresInSecs, _ := strconv.Atoi(c.DefaultQuery("expires", "3600"))
	if expiresInSecs <= 0 {
		expiresInSecs = 3600
	}
	if expiresInSecs > 86400 {
		expiresInSecs = 86400
	}

	url, err := toolsService.ResolveGenerationObjectDeliveryURL(c.Request.Context(), &object, time.Duration(expiresInSecs)*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": "Failed to resolve download url: " + err.Error(),
		})
		return
	}

	isPresigned := false
	if store, ok, err := storageService.NewObjectStoreForProviderBucket(globals.GraConf.Generator.Storage, object.Provider, object.Bucket); err == nil && ok && store != nil {
		if strings.TrimSpace(url) != strings.TrimSpace(store.PublicURL(object.ObjectKey)) {
			isPresigned = true
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": StorageObjectDownloadURLItem{
			ID:            uint(object.Id),
			Provider:      object.Provider,
			Bucket:        object.Bucket,
			ObjectKey:     object.ObjectKey,
			URL:           url,
			IsPresigned:   isPresigned,
			ExpiresInSecs: expiresInSecs,
		},
	})
}

func generationObjectStatusLabel(status int8) string {
	switch status {
	case model.GenerationObjectStatusDeleted:
		return "deleted"
	case model.GenerationObjectStatusOrphan:
		return "orphan"
	case model.GenerationObjectStatusHidden:
		return "hidden"
	default:
		return "active"
	}
}

func normalizeGenerationObjectStatusFilter(value string) int8 {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "deleted":
		return model.GenerationObjectStatusDeleted
	case "orphan":
		return model.GenerationObjectStatusOrphan
	case "hidden":
		return model.GenerationObjectStatusHidden
	case "active":
		return model.GenerationObjectStatusActive
	default:
		return 0
	}
}

func normalizeOptionalFilter(value string) string {
	return strings.TrimSpace(value)
}

func resolveStorageAccessSummary() StorageAccessSummary {
	cfg := globals.GraConf.Generator.Storage
	switch strings.TrimSpace(cfg.Type) {
	case "r2":
		mode := "public"
		if cfg.R2.UsePresignedURL {
			mode = "presigned"
		}
		ttl := cfg.R2.PresignTTLSeconds
		if ttl <= 0 {
			ttl = 3600
		}
		return StorageAccessSummary{
			Provider:      "r2",
			Bucket:        strings.TrimSpace(cfg.R2.Bucket),
			Mode:          mode,
			ExpiresInSecs: ttl,
		}
	default:
		return StorageAccessSummary{
			Provider:      strings.TrimSpace(cfg.Type),
			Mode:          "public",
			ExpiresInSecs: 0,
		}
	}
}

func resolveStorageUploadSummary() StorageUploadSummary {
	cfg, ok := storageService.NewObjectStoreConfigFromStorage(globals.GraConf.Generator.Storage)
	if !ok {
		return StorageUploadSummary{
			Mode: "local",
		}
	}

	return StorageUploadSummary{
		Mode:                    "multipart",
		MultipartThresholdBytes: storageService.NormalizeMultipartThresholdBytes(cfg.MultipartThresholdBytes),
		MultipartPartSizeBytes:  storageService.NormalizeMultipartPartSizeBytes(cfg.MultipartPartSizeBytes),
		AllowedRemoteAssetHosts: globals.GraConf.Generator.FileUpload.AllowedRemoteAssetHosts,
	}
}
