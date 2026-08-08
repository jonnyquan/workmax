package prompt

import (
	"encoding/json"
	"fmt"
	"server/globals"
	"server/model"
	"server/utils"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PromptApi Prompt前台API
type PromptApi struct{}

// escapeLike 转义LIKE查询中的特殊字符
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// parseJSONArray 解析JSON数组字符串
func parseJSONArray(s string) []string {
	if s == "" {
		return []string{}
	}
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return []string{}
	}
	return arr
}

// formatLocalImage 确保本地图片以/开头(http URL不变)。
// 当 statics.cdn_base_url 配置且路径为 /uploads/prompts/* 时，
// 直接返回 CDN 绝对 URL，前端无需经后端代理。
func formatLocalImage(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "http") {
		path = "/" + path
	}
	return globals.GraConf.Statics.PromptCDNURL(path)
}

// promptSummaryPayload returns the canonical PromptSummary shape consumed by
// the web PromptCard. Every list endpoint must return this exact set so the
// frontend never sees `undefined` on numeric stat fields.
func promptSummaryPayload(p model.Prompt) gin.H {
	return gin.H{
		"id":               p.Id,
		"slug":             p.Slug,
		"title":            p.Title,
		"lang":             p.Lang,
		"coverImage":       formatLocalImage(p.CoverImage),
		"thumbImage":       formatLocalImage(p.ThumbImage),
		"localImage":       formatLocalImage(p.LocalImage),
		"aspectRatio":      p.AspectRatio,
		"promptPreview":    p.PromptPreview,
		"medium":           p.Medium,
		"promptType":       p.PromptType,
		"previewAssetType": p.PreviewAssetType,
		"previewVideo":     formatLocalImage(p.PreviewVideo),
		"category":         p.Category,
		"style":            p.Style,
		"tags":             parseJSONArray(p.Tags),
		"supportedModels":  parseJSONArray(p.SupportedModels),
		"primaryModel":     p.PrimaryModel,
		"modelParams":      p.ModelParams,
		"viewCount":        p.ViewCount,
		"copyCount":        p.CopyCount,
		"likeCount":        p.LikeCount,
		"rating":           p.Rating,
		"isFeatured":       p.IsFeatured,
		"isTrending":       p.IsTrending,
		"sourceType":       p.SourceType,
		"sourceID":         p.SourceID,
	}
}

const promptSummarySelectFields = "id, slug, title, lang, cover_image, thumb_image, local_image, aspect_ratio, prompt_preview, medium, prompt_type, preview_asset_type, preview_video, category, style, tags, supported_models, primary_model, model_params, view_count, copy_count, like_count, rating, is_featured, is_trending, source_type, source_id, sort, popular_score"

func applyPromptMediaFilters(query *gorm.DB, medium, promptType string) *gorm.DB {
	if medium != "" {
		query = query.Where("medium = ?", medium)
	}
	if promptType != "" {
		query = query.Where("prompt_type = ?", promptType)
	}
	return query
}

// GetPromptList 获取Prompt列表
// GET /api/prompts?lang=en&model=&category=&tag=&style=&medium=&promptType=&page=1&limit=20&sort=popular
func (a *PromptApi) GetPromptList(c *gin.Context) {
	db := globals.GraDBs["system"]

	lang := c.DefaultQuery("lang", "en")
	modelCode := c.Query("model")
	category := c.Query("category")
	tag := c.Query("tag")
	style := c.Query("style")
	medium := c.Query("medium")
	promptType := c.Query("promptType")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	sortBy := c.DefaultQuery("sort", "popular")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := db.Model(&model.Prompt{}).Where("status = ?", 1)

	if lang != "" {
		query = query.Where("lang = ?", lang)
	}
	if modelCode != "" {
		query = query.Where("primary_model = ? OR supported_models LIKE ?", modelCode, "%"+escapeLike(modelCode)+"%")
	}
	if category != "" {
		query = query.Where("category = ?", category)
	}
	if tag != "" {
		query = query.Where("tags LIKE ?", "%"+escapeLike(tag)+"%")
	}
	if style != "" {
		query = query.Where("style = ?", style)
	}
	query = applyPromptMediaFilters(query, medium, promptType)

	var total int64
	query.Count(&total)

	switch sortBy {
	case "latest":
		query = query.Order("created_at DESC")
	case "rating":
		query = query.Order("rating DESC, popular_score DESC, sort DESC")
	default:
		// `sort` is the editorial weight — gives curated rows priority over
		// auto-imports when popular_score ties at zero.
		query = query.Order("popular_score DESC, sort DESC, created_at DESC")
	}

	var prompts []model.Prompt
	query.Select(promptSummarySelectFields).Offset(offset).Limit(limit).Find(&prompts)

	cache := globals.GraRedis["default"]
	ip := c.ClientIP()

	list := make([]gin.H, len(prompts))
	for i, p := range prompts {
		isLiked := false
		if cache != nil {
			dedupKey := fmt.Sprintf("prompt:like:%d:%s", p.Id, ip)
			if _, err := utils.GetCache(cache, dedupKey); err == nil {
				isLiked = true
			}
		}

		list[i] = gin.H{
			"id":               p.Id,
			"slug":             p.Slug,
			"title":            p.Title,
			"lang":             p.Lang,
			"coverImage":       formatLocalImage(p.CoverImage),
			"thumbImage":       formatLocalImage(p.ThumbImage),
			"localImage":       formatLocalImage(p.LocalImage),
			"aspectRatio":      p.AspectRatio,
			"promptPreview":    p.PromptPreview,
			"medium":           p.Medium,
			"promptType":       p.PromptType,
			"previewAssetType": p.PreviewAssetType,
			"previewVideo":     formatLocalImage(p.PreviewVideo),
			"category":         p.Category,
			"style":            p.Style,
			"tags":             parseJSONArray(p.Tags),
			"supportedModels":  parseJSONArray(p.SupportedModels),
			"primaryModel":     p.PrimaryModel,
			"modelParams":      p.ModelParams,
			"viewCount":        p.ViewCount,
			"copyCount":        p.CopyCount,
			"likeCount":        p.LikeCount,
			"rating":           p.Rating,
			"isFeatured":       p.IsFeatured,
			"isTrending":       p.IsTrending,
			"sourceType":       p.SourceType,
			"sourceID":         p.SourceID,
			"isLiked":          isLiked,
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"list":  list,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// GetPromptDetail 获取Prompt详情
// GET /api/prompts/:slug?lang=en
func (a *PromptApi) GetPromptDetail(c *gin.Context) {
	db := globals.GraDBs["system"]

	slug := c.Param("slug")
	lang := c.DefaultQuery("lang", "")

	var prompt model.Prompt
	query := db.Where("slug = ? AND status = ?", slug, 1)
	if lang != "" {
		query = query.Where("lang = ?", lang)
	}

	if err := query.First(&prompt).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Prompt not found"})
		return
	}

	// Fire-and-forget the view bump — it doesn't block the detail response,
	// and `view_count + 1` runs in SQL so concurrent reads can't race-clobber
	// each other the way `prompt.ViewCount+1` (computed from a stale read) did.
	promptID := prompt.Id
	go func() {
		globals.GraDBs["system"].Model(&model.Prompt{}).
			Where("id = ?", promptID).
			UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	}()

	rootID := prompt.Id
	if prompt.MainPromptID > 0 {
		rootID = prompt.MainPromptID
	}
	var availableLangs []string
	db.Model(&model.Prompt{}).
		Where("id = ? OR main_prompt_id = ?", rootID, rootID).
		Pluck("lang", &availableLangs)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"id":                 prompt.Id,
			"slug":               prompt.Slug,
			"title":              prompt.Title,
			"lang":               prompt.Lang,
			"promptContent":      prompt.PromptContent,
			"negativePrompt":     prompt.NegativePrompt,
			"promptDescription":  prompt.PromptDescription,
			"coverImage":         formatLocalImage(prompt.CoverImage),
			"localImage":         formatLocalImage(prompt.LocalImage),
			"thumbImage":         formatLocalImage(prompt.ThumbImage),
			"imageWidth":         prompt.ImageWidth,
			"imageHeight":        prompt.ImageHeight,
			"aspectRatio":        prompt.AspectRatio,
			"category":           prompt.Category,
			"style":              prompt.Style,
			"tags":               parseJSONArray(prompt.Tags),
			"supportedModels":    parseJSONArray(prompt.SupportedModels),
			"primaryModel":       prompt.PrimaryModel,
			"modelParams":        prompt.ModelParams,
			"medium":             prompt.Medium,
			"promptType":         prompt.PromptType,
			"previewAssetType":   prompt.PreviewAssetType,
			"previewVideo":       formatLocalImage(prompt.PreviewVideo),
			"viewCount":          prompt.ViewCount + 1,
			"copyCount":          prompt.CopyCount,
			"likeCount":          prompt.LikeCount,
			"rating":             prompt.Rating,
			"ratingCount":        prompt.RatingCount,
			"isFeatured":         prompt.IsFeatured,
			"isTrending":         prompt.IsTrending,
			"seoTitle":           prompt.SeoTitle,
			"seoKeyword":         prompt.SeoKeyword,
			"seoDescription":     prompt.SeoDescription,
			"sourceType":         prompt.SourceType,
			"sourceID":           prompt.SourceID,
			"availableLanguages": availableLangs,
		},
	})
}

// GetFeaturedPrompts 获取精选Prompt
// GET /api/prompts/featured?lang=en&medium=&limit=8
func (a *PromptApi) GetFeaturedPrompts(c *gin.Context) {
	db := globals.GraDBs["system"]

	lang := c.DefaultQuery("lang", "en")
	medium := c.Query("medium")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "8"))

	var prompts []model.Prompt
	query := db.Model(&model.Prompt{}).
		Select(promptSummarySelectFields).
		Where("status = ? AND is_featured = ? AND lang = ?", 1, true, lang)
	query = applyPromptMediaFilters(query, medium, "")
	query.Order("sort DESC, popular_score DESC").
		Limit(limit).
		Find(&prompts)

	list := make([]gin.H, len(prompts))
	for i, p := range prompts {
		list[i] = promptSummaryPayload(p)
	}

	c.JSON(200, gin.H{"success": true, "data": list})
}

// GetTrendingPrompts 获取热门Prompt
// GET /api/prompts/trending?lang=en&medium=&limit=6
func (a *PromptApi) GetTrendingPrompts(c *gin.Context) {
	db := globals.GraDBs["system"]

	lang := c.DefaultQuery("lang", "en")
	medium := c.Query("medium")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "6"))

	var prompts []model.Prompt
	query := db.Where("status = ? AND is_trending = ? AND lang = ?", 1, true, lang)
	query = applyPromptMediaFilters(query, medium, "")
	query.Order("popular_score DESC").
		Limit(limit).
		Find(&prompts)

	list := make([]gin.H, len(prompts))
	for i, p := range prompts {
		list[i] = promptSummaryPayload(p)
	}

	c.JSON(200, gin.H{"success": true, "data": list})
}

// SearchPrompts 搜索Prompt
// GET /api/prompts/search?q=&lang=&model=&tag=&medium=&promptType=&page=1&limit=20
func (a *PromptApi) SearchPrompts(c *gin.Context) {
	db := globals.GraDBs["system"]

	q := c.Query("q")
	lang := c.DefaultQuery("lang", "")
	modelCode := c.Query("model")
	tag := c.Query("tag")
	medium := c.Query("medium")
	promptType := c.Query("promptType")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	if q == "" && tag == "" {
		c.JSON(400, gin.H{"success": false, "message": "Search query or tag required"})
		return
	}

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := db.Model(&model.Prompt{}).Where("status = ?", 1)

	if q != "" {
		escapedQ := escapeLike(q)
		query = query.Where(
			"search_vector LIKE ? OR title LIKE ? OR tags LIKE ?",
			"%"+escapedQ+"%", "%"+escapedQ+"%", "%"+escapedQ+"%",
		)
	}

	if tag != "" {
		query = query.Where("tags LIKE ?", "%"+escapeLike(tag)+"%")
	}

	if lang != "" {
		query = query.Where("lang = ?", lang)
	}
	if modelCode != "" {
		query = query.Where("primary_model = ? OR supported_models LIKE ?", modelCode, "%"+escapeLike(modelCode)+"%")
	}
	query = applyPromptMediaFilters(query, medium, promptType)

	var total int64
	query.Count(&total)

	var prompts []model.Prompt
	query.Order("popular_score DESC").Offset(offset).Limit(limit).Find(&prompts)

	list := make([]gin.H, len(prompts))
	for i, p := range prompts {
		list[i] = gin.H{
			"id":               p.Id,
			"slug":             p.Slug,
			"title":            p.Title,
			"lang":             p.Lang,
			"coverImage":       formatLocalImage(p.CoverImage),
			"thumbImage":       formatLocalImage(p.ThumbImage),
			"localImage":       formatLocalImage(p.LocalImage),
			"aspectRatio":      p.AspectRatio,
			"promptPreview":    p.PromptPreview,
			"medium":           p.Medium,
			"promptType":       p.PromptType,
			"previewAssetType": p.PreviewAssetType,
			"previewVideo":     formatLocalImage(p.PreviewVideo),
			"category":         p.Category,
			"style":            p.Style,
			"tags":             parseJSONArray(p.Tags),
			"supportedModels":  parseJSONArray(p.SupportedModels),
			"primaryModel":     p.PrimaryModel,
			"modelParams":      p.ModelParams,
			"viewCount":        p.ViewCount,
			"copyCount":        p.CopyCount,
			"likeCount":        p.LikeCount,
			"rating":           p.Rating,
			"isFeatured":       p.IsFeatured,
			"isTrending":       p.IsTrending,
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"list":  list,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// RecordCopy 记录复制 (IP去重 + 限流)
// POST /api/prompts/interact/:id/copy
func (a *PromptApi) RecordCopy(c *gin.Context) {
	db := globals.GraDBs["system"]
	cache := globals.GraRedis["default"]

	id := c.Param("id")
	ip := c.ClientIP()

	rateKey := fmt.Sprintf("prompt:copy:rate:%s", ip)
	if cache != nil {
		ok, _ := utils.IncrWithLimit(cache, rateKey, 1, 10, 60)
		if !ok {
			c.JSON(429, gin.H{"success": false, "message": "Rate limit exceeded"})
			return
		}
	}

	dedupKey := fmt.Sprintf("prompt:copy:%s:%s", id, ip)
	if cache != nil {
		if _, err := utils.GetCache(cache, dedupKey); err == nil {
			c.JSON(200, gin.H{"success": true, "deduplicated": true})
			return
		}
		utils.SetCache(cache, dedupKey, "1", 86400)
	}

	result := db.Model(&model.Prompt{}).
		Where("id = ?", id).
		UpdateColumn("copy_count", gorm.Expr("copy_count + 1"))

	if result.RowsAffected == 0 {
		c.JSON(404, gin.H{"success": false, "message": "Prompt not found"})
		return
	}

	c.JSON(200, gin.H{"success": true})
}

// RecordLike 记录点赞 (IP去重 + 限流)
// POST /api/prompts/interact/:id/like
func (a *PromptApi) RecordLike(c *gin.Context) {
	db := globals.GraDBs["system"]
	cache := globals.GraRedis["default"]

	id := c.Param("id")
	ip := c.ClientIP()

	rateKey := fmt.Sprintf("prompt:like:rate:%s", ip)
	if cache != nil {
		ok, _ := utils.IncrWithLimit(cache, rateKey, 1, 20, 60)
		if !ok {
			c.JSON(429, gin.H{"success": false, "message": "Rate limit exceeded"})
			return
		}
	}

	dedupKey := fmt.Sprintf("prompt:like:%s:%s", id, ip)
	if cache != nil {
		if _, err := utils.GetCache(cache, dedupKey); err == nil {
			c.JSON(200, gin.H{"success": true, "deduplicated": true, "message": "Already liked"})
			return
		}
		utils.SetCache(cache, dedupKey, "1", 86400)
	}

	result := db.Model(&model.Prompt{}).
		Where("id = ?", id).
		UpdateColumn("like_count", gorm.Expr("like_count + 1"))

	if result.RowsAffected == 0 {
		c.JSON(404, gin.H{"success": false, "message": "Prompt not found"})
		return
	}

	c.JSON(200, gin.H{"success": true})
}

// UnlikePrompt 取消点赞
// DELETE /api/prompts/interact/:id/like
func (a *PromptApi) UnlikePrompt(c *gin.Context) {
	db := globals.GraDBs["system"]
	cache := globals.GraRedis["default"]

	id := c.Param("id")
	ip := c.ClientIP()

	dedupKey := fmt.Sprintf("prompt:like:%s:%s", id, ip)
	if cache != nil {
		if _, err := utils.GetCache(cache, dedupKey); err != nil {
			c.JSON(400, gin.H{"success": false, "message": "Not liked yet"})
			return
		}
		cache.Del(c, dedupKey)
	}

	result := db.Model(&model.Prompt{}).
		Where("id = ? AND like_count > 0", id).
		UpdateColumn("like_count", gorm.Expr("like_count - 1"))

	if result.RowsAffected == 0 {
		c.JSON(404, gin.H{"success": false, "message": "Prompt not found"})
		return
	}

	c.JSON(200, gin.H{"success": true})
}

// CheckLikeStatus 检查点赞状态
// GET /api/prompts/interact/:id/like/status
func (a *PromptApi) CheckLikeStatus(c *gin.Context) {
	cache := globals.GraRedis["default"]

	id := c.Param("id")
	ip := c.ClientIP()

	liked := false
	if cache != nil {
		dedupKey := fmt.Sprintf("prompt:like:%s:%s", id, ip)
		if _, err := utils.GetCache(cache, dedupKey); err == nil {
			liked = true
		}
	}

	c.JSON(200, gin.H{"success": true, "data": gin.H{"liked": liked}})
}

// GetRelatedPrompts 获取相关Prompt
// GET /api/prompts/:slug/related?lang=&limit=6
func (a *PromptApi) GetRelatedPrompts(c *gin.Context) {
	db := globals.GraDBs["system"]

	slug := c.Param("slug")
	lang := c.Query("lang")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "6"))

	var current model.Prompt
	currentQuery := db.Where("slug = ? AND status = 1", slug)
	if lang != "" {
		currentQuery = currentQuery.Where("lang = ?", lang)
	}
	if err := currentQuery.First(&current).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Prompt not found"})
		return
	}

	var prompts []model.Prompt
	query := db.Where("id != ? AND status = 1 AND lang = ? AND medium = ?", current.Id, current.Lang, current.Medium)

	if current.Category != "" && current.Style != "" {
		query = query.Where("(category = ? OR style = ?)", current.Category, current.Style)
	} else if current.Category != "" {
		query = query.Where("category = ?", current.Category)
	} else if current.Style != "" {
		query = query.Where("style = ?", current.Style)
	}

	query.Order("popular_score DESC").Limit(limit).Find(&prompts)

	list := make([]gin.H, len(prompts))
	for i, p := range prompts {
		list[i] = gin.H{
			"id":          p.Id,
			"slug":        p.Slug,
			"title":       p.Title,
			"lang":        p.Lang,
			"coverImage":  formatLocalImage(p.CoverImage),
			"localImage":  formatLocalImage(p.LocalImage),
			"thumbImage":  formatLocalImage(p.ThumbImage),
			"medium":      p.Medium,
			"promptType":  p.PromptType,
			"aspectRatio": p.AspectRatio,
			"category":    p.Category,
			"likeCount":   p.LikeCount,
			"viewCount":   p.ViewCount,
		}
	}

	c.JSON(200, gin.H{"success": true, "data": list})
}

// SubmitRating 提交评分
// POST /api/prompts/interact/:id/rating
func (a *PromptApi) SubmitRating(c *gin.Context) {
	db := globals.GraDBs["system"]
	cache := globals.GraRedis["default"]

	id := c.Param("id")
	ip := c.ClientIP()

	var req struct {
		Rating float32 `json:"rating" binding:"required,min=1,max=5"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"success": false, "message": "Invalid rating (1-5)"})
		return
	}

	ratingKey := fmt.Sprintf("prompt:rating:%s:%s", id, ip)
	if cache != nil {
		if _, err := utils.GetCache(cache, ratingKey); err == nil {
			c.JSON(400, gin.H{"success": false, "message": "Already rated"})
			return
		}
		utils.SetCache(cache, ratingKey, fmt.Sprintf("%.1f", req.Rating), 86400*30)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		var prompt model.Prompt
		if err := tx.Where("id = ?", id).First(&prompt).Error; err != nil {
			return err
		}

		newCount := prompt.RatingCount + 1
		newRating := (prompt.Rating*float32(prompt.RatingCount) + req.Rating) / float32(newCount)

		if err := tx.Model(&prompt).Updates(map[string]interface{}{
			"rating":       newRating,
			"rating_count": newCount,
		}).Error; err != nil {
			return err
		}

		c.JSON(200, gin.H{"success": true, "data": gin.H{"rating": newRating, "count": newCount}})
		return nil
	})

	if err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Prompt not found"})
		return
	}
}

// GetPopularTags 获取热门标签
// GET /api/prompts/tags/popular?lang=en&limit=10
func (a *PromptApi) GetPopularTags(c *gin.Context) {
	db := globals.GraDBs["system"]

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))
	lang := c.DefaultQuery("lang", "en")

	var tags []model.PromptTag
	db.Where("status = ? AND is_popular = ? AND lang = ?", 1, true, lang).
		Order("trend_score DESC, usage_count DESC").
		Limit(limit).
		Find(&tags)

	list := make([]gin.H, len(tags))
	for i, t := range tags {
		list[i] = gin.H{
			"name":  t.Name,
			"slug":  t.Slug,
			"count": t.UsageCount,
		}
	}

	c.JSON(200, gin.H{"success": true, "data": list})
}

// GetCategories 获取分类列表
// GET /api/prompts/categories?lang=en&medium=
func (a *PromptApi) GetCategories(c *gin.Context) {
	db := globals.GraDBs["system"]

	lang := c.DefaultQuery("lang", "en")
	medium := c.Query("medium")

	var categories []model.PromptCategory
	db.Where("status = ? AND lang = ?", 1, lang).
		Order("sort ASC").
		Find(&categories)

	type categoryCountRow struct {
		Category string
		Count    int64
	}

	var countRows []categoryCountRow
	countQuery := db.Model(&model.Prompt{}).
		Select("category, COUNT(*) AS count").
		Where("status = ? AND lang = ? AND category <> ''", 1, lang)
	countQuery = applyPromptMediaFilters(countQuery, medium, "")
	countQuery.Group("category").Scan(&countRows)

	countMap := make(map[string]int, len(countRows))
	for _, row := range countRows {
		countMap[row.Category] = int(row.Count)
	}

	list := make([]gin.H, 0, len(categories))
	for _, cat := range categories {
		count := countMap[cat.Code]
		if medium != "" && count == 0 {
			continue
		}
		if medium == "" && count == 0 {
			count = cat.PromptCount
		}

		list = append(list, gin.H{
			"code":  cat.Code,
			"name":  cat.Name,
			"icon":  cat.Icon,
			"color": cat.Color,
			"count": count,
		})
	}

	c.JSON(200, gin.H{"success": true, "data": list})
}

// GetModels 获取支持的模型列表
// GET /api/prompts/models?medium=image|video
func (a *PromptApi) GetModels(c *gin.Context) {
	medium := c.Query("medium")
	models := []gin.H{
		{"code": model.NANO_BANANA_PRO, "name": "Nano Banana Pro", "nameZh": "Nano Banana Pro", "provider": model.ProviderTypeGemini, "type": "image"},
		{"code": "gpt-4o", "name": "GPT-4o", "nameZh": "GPT-4o", "provider": "openai", "type": "image"},
		{"code": "midjourney", "name": "Midjourney", "nameZh": "Midjourney", "provider": "midjourney", "type": "image"},
		{"code": "stable-diffusion", "name": "Stable Diffusion", "nameZh": "Stable Diffusion", "provider": "stability", "type": "image"},
		{"code": "seedream", "name": "Seedream", "nameZh": "Seedream", "provider": "seedream", "type": "image"},
		{"code": "Grok", "name": "Grok", "nameZh": "Grok", "provider": "xai", "type": "image"},
		{"code": "kling-2.6", "name": "Kling 2.6", "nameZh": "Kling 2.6", "provider": "kling", "type": "video"},
		{"code": "sora-2", "name": "Sora 2", "nameZh": "Sora 2", "provider": "openai", "type": "video"},
		{"code": "veo-3", "name": "Veo 3", "nameZh": "Veo 3", "provider": "google", "type": "video"},
		{"code": "veo-3.1", "name": "Veo 3.1", "nameZh": "Veo 3.1", "provider": "google", "type": "video"},
		{"code": "seedance", "name": "Seedance", "nameZh": "Seedance", "provider": "bytedance", "type": "video"},
		{"code": "seedance-2", "name": "Seedance 2", "nameZh": "Seedance 2", "provider": "bytedance", "type": "video"},
	}

	if medium != "" {
		filtered := make([]gin.H, 0, len(models))
		for _, item := range models {
			if item["type"] == medium {
				filtered = append(filtered, item)
			}
		}
		models = filtered
	}

	c.JSON(200, gin.H{"success": true, "data": models})
}
