package seo

import (
	"server/globals"
	"server/model"
	"strconv"

	"github.com/gin-gonic/gin"
)

type SeoApi struct{}

// GetPromptSlugs returns slugs for sitemap generation
// GET /api/seo/prompt-slugs?page=1&limit=50000&sort=popular&lang=en
func (a *SeoApi) GetPromptSlugs(c *gin.Context) {
	db := globals.GraDBs["system"]

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50000"))
	sortBy := c.DefaultQuery("sort", "latest")
	lang := c.Query("lang")

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 50000 {
		limit = 50000
	}
	offset := (page - 1) * limit

	query := db.Model(&model.Prompt{}).
		Select("slug, lang, updated_at").
		Where("status = ?", 1)

	if lang != "" {
		query = query.Where("lang = ?", lang)
	}

	var total int64
	query.Count(&total)

	switch sortBy {
	case "popular":
		query = query.Order("popular_score DESC, created_at DESC")
	default:
		query = query.Order("created_at DESC")
	}

	var results []struct {
		Slug      string `json:"slug"`
		Lang      string `json:"lang"`
		UpdatedAt string `json:"updatedAt"`
	}

	query.Offset(offset).Limit(limit).Find(&results)

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"list":  results,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// GetAllCategories returns all categories for sitemap
// GET /api/seo/categories?lang=en
func (a *SeoApi) GetAllCategories(c *gin.Context) {
	db := globals.GraDBs["system"]

	lang := c.Query("lang")

	var categories []struct {
		Code      string `json:"code"`
		Lang      string `json:"lang"`
		Count     int    `json:"count"`
		UpdatedAt string `json:"updatedAt"`
	}

	query := db.Model(&model.PromptCategory{}).
		Select("code, lang, prompt_count as count, updated_at").
		Where("status = ?", 1)

	if lang != "" {
		query = query.Where("lang = ?", lang)
	}

	query.Order("prompt_count DESC").Find(&categories)

	c.JSON(200, gin.H{"success": true, "data": categories})
}

// GetAllStyles returns all unique styles for sitemap
// GET /api/seo/styles
func (a *SeoApi) GetAllStyles(c *gin.Context) {
	db := globals.GraDBs["system"]

	var styles []struct {
		Code  string `json:"code"`
		Count int64  `json:"count"`
	}

	db.Model(&model.Prompt{}).
		Select("style as code, COUNT(*) as count").
		Where("status = ? AND style != ''", 1).
		Group("style").
		Order("count DESC").
		Find(&styles)

	c.JSON(200, gin.H{"success": true, "data": styles})
}

// GetAllModelsForSEO returns all unique models for sitemap
// GET /api/seo/models
func (a *SeoApi) GetAllModelsForSEO(c *gin.Context) {
	db := globals.GraDBs["system"]

	var models []struct {
		Code  string `json:"code"`
		Count int64  `json:"count"`
	}

	db.Model(&model.Prompt{}).
		Select("primary_model as code, COUNT(*) as count").
		Where("status = ? AND primary_model != ''", 1).
		Group("primary_model").
		Order("count DESC").
		Find(&models)

	c.JSON(200, gin.H{"success": true, "data": models})
}

// GetPromptStats returns stats for programmatic SEO
// GET /api/seo/stats
func (a *SeoApi) GetPromptStats(c *gin.Context) {
	db := globals.GraDBs["system"]

	var stats struct {
		TotalPrompts    int64 `json:"totalPrompts"`
		TotalCategories int64 `json:"totalCategories"`
		TotalStyles     int64 `json:"totalStyles"`
		TotalModels     int64 `json:"totalModels"`
	}

	db.Model(&model.Prompt{}).Where("status = ?", 1).Count(&stats.TotalPrompts)
	db.Model(&model.PromptCategory{}).Where("status = ?", 1).Count(&stats.TotalCategories)
	db.Model(&model.Prompt{}).Where("status = ? AND style != ''", 1).Distinct("style").Count(&stats.TotalStyles)
	db.Model(&model.Prompt{}).Where("status = ? AND primary_model != ''", 1).Distinct("primary_model").Count(&stats.TotalModels)

	c.JSON(200, gin.H{"success": true, "data": stats})
}

// GetPromptLanguages returns available languages for a specific prompt
// GET /api/seo/prompt-languages/:slug
func (a *SeoApi) GetPromptLanguages(c *gin.Context) {
	db := globals.GraDBs["system"]

	slug := c.Param("slug")

	var prompt model.Prompt
	if err := db.Where("slug = ? AND status = ?", slug, 1).First(&prompt).Error; err != nil {
		c.JSON(404, gin.H{"success": false, "message": "Prompt not found"})
		return
	}

	var languages []string
	mainID := prompt.MainPromptID
	if mainID == 0 {
		mainID = prompt.Id
	}

	db.Model(&model.Prompt{}).
		Where("(main_prompt_id = ? OR id = ?) AND status = ?", mainID, mainID, 1).
		Pluck("lang", &languages)

	hreflang := make(map[string]string)
	for _, lang := range languages {
		localePath := ""
		if lang != "en" {
			localePath = "/" + lang
		}
		hreflang[lang] = localePath + "/prompts/" + slug
	}
	hreflang["x-default"] = "/prompts/" + slug

	c.JSON(200, gin.H{
		"success": true,
		"data": gin.H{
			"slug":         slug,
			"languages":    languages,
			"hreflang":     hreflang,
			"mainPromptId": mainID,
		},
	})
}
