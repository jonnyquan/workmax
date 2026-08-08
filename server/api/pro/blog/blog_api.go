package blog

import (
	"server/globals"
	"server/model"
	"server/model/common/response"
	"strconv"

	"github.com/gin-gonic/gin"
)

type BlogApi struct{}

// GetCategories 获取分类
func (b *BlogApi) GetCategories(c *gin.Context) {
	lang := c.Query("lang")
	if lang == "zh_cn" {
		lang = "zh"
	}
	blog_category := []model.BlogCategory{}
	err := globals.GraDBs["system"].Where("status = ? and lang = ?", 1, lang).Find(&blog_category).Error
	if err != nil {
		response.FailWithMessage("获取分类失败", c)
		return
	}
	c.JSON(200, gin.H{"dataList": blog_category})
}

// GetCategoriesCount 获取带文章数的分类列表
func (b *BlogApi) GetCategoriesCount(c *gin.Context) {
	lang := c.Query("lang")
	if lang == "zh_cn" {
		lang = "zh"
	}

	// 使用单个查询获取分类和文章数量，避免N+1查询问题
	// 修改：使用category_code而不是category_id进行关联
	type CategoryCount struct {
		ID    uint   `json:"id"`
		Title string `json:"title"`
		Slug  string `json:"slug"`
		Count int64  `json:"count"`
	}

	var categoryList []CategoryCount
	err := globals.GraDBs["system"].Raw(`
		SELECT
			bc.id,
			bc.title,
			bc.slug,
			COALESCE(blog_counts.count, 0) as count
		FROM w_blog_category bc
		LEFT JOIN (
			SELECT
				category_code COLLATE utf8mb4_unicode_ci as category_code,
				COUNT(*) as count
			FROM w_blog
			WHERE status = ? AND lang = ? AND category_code != ''
			GROUP BY category_code
		) blog_counts ON bc.slug = blog_counts.category_code
		WHERE bc.status = ? AND bc.lang = ?
		ORDER BY bc.sort ASC
	`, 1, lang, 1, lang).Scan(&categoryList).Error

	if err != nil {
		globals.Warn("Failed to get categories count: " + err.Error())
		response.FailWithMessage("获取分类数量失败", c)
		return
	}

	// 转换为响应格式
	category_response := make([]map[string]interface{}, len(categoryList))
	for i, category := range categoryList {
		category_response[i] = map[string]interface{}{
			"name":  category.Title,
			"slug":  category.Slug,
			"count": category.Count,
		}
	}

	c.JSON(200, gin.H{"dataList": category_response})
}

// GetBlogList 获取博客列表
func (b *BlogApi) GetBlogList(c *gin.Context) {
	slug := c.Query("slug") // 这个是category的slug，不是blog的slug
	lang := c.Query("lang")
	pageNo := c.Query("pageNo")
	pageNoInt, err := strconv.Atoi(pageNo)
	if err != nil {
		pageNoInt = 1
	}

	pageSize := c.Query("pageSize")
	pageSizeInt, err := strconv.Atoi(pageSize)
	if err != nil {
		pageSizeInt = 36
	}
	if lang == "zh_cn" {
		lang = "zh"
	}
	data := map[string]interface{}{}
	categorySlug := "" // 用于存储分类的slug

	if slug != "" {
		blog_category := model.BlogCategory{}
		err := globals.GraDBs["system"].Where("slug = ? and lang = ?", slug, lang).First(&blog_category).Error
		if err != nil {
			globals.Warn(err.Error())
		} else {
			data["categoryTitle"] = blog_category.Title
			data["categorySeoTitle"] = blog_category.SeoTitle
			data["categorySeoKeyword"] = blog_category.SeoKeyword
			data["categorySeoDescription"] = blog_category.SeoDescription
			categorySlug = blog_category.Slug // 使用slug而不是id
		}
	}

	blog_list := []model.Blog{}
	if categorySlug != "" {
		// 修改：使用category_code进行查询
		err = globals.GraDBs["system"].Where("status = ? and lang = ?", 1, lang).Where("category_code = ?", categorySlug).Order("updated_at DESC").Limit(pageSizeInt).Offset((pageNoInt - 1) * pageSizeInt).Find(&blog_list).Error
		if err != nil {
			globals.Warn(err.Error())
		}
	} else {
		err = globals.GraDBs["system"].Where("status = ? and lang = ?", 1, lang).Order("updated_at DESC").Limit(pageSizeInt).Offset((pageNoInt - 1) * pageSizeInt).Find(&blog_list).Error
		if err != nil {
			globals.Warn(err.Error())
		}
	}

	// Get category information for mapping using category_code
	categoryMap := make(map[string]model.BlogCategory)
	if len(blog_list) > 0 {
		categoryCodes := []string{}
		for _, blog := range blog_list {
			if blog.CategoryCode != "" {
				categoryCodes = append(categoryCodes, blog.CategoryCode)
			}
		}
		if len(categoryCodes) > 0 {
			categories := []model.BlogCategory{}
			globals.GraDBs["system"].Where("slug COLLATE utf8mb4_unicode_ci IN ? AND lang = ?", categoryCodes, lang).Find(&categories)
			for _, cat := range categories {
				categoryMap[cat.Slug] = cat
			}
		}
	}

	records := []map[string]interface{}{}
	for _, blog := range blog_list {
		// Get category info from map using category_code
		categoryTitle := ""
		categorySlug := ""
		if cat, exists := categoryMap[blog.CategoryCode]; exists {
			categoryTitle = cat.Title
			categorySlug = cat.Slug
		}

		// Canonical chain key — EN rows have main_blog_id=0 so fall back to id;
		// translations point at the canonical via main_blog_id. Sitemap uses
		// this to group hreflang alternates across locales.
		canonicalID := uint(blog.Id)
		if blog.MainBlogID != 0 {
			canonicalID = uint(blog.MainBlogID)
		}

		records = append(records, map[string]interface{}{
			"id":              blog.Id,
			"canonicalId":     canonicalID,
			"created_at":      blog.CreatedAt,
			"updated_at":      blog.UpdatedAt,
			"title":           blog.Title,
			"cover":           blog.Cover,
			"seoTitle":        blog.SeoTitle,
			"seoKeyword":      blog.SeoKeyword,
			"seoDescription":  blog.SeoDescription,
			"summary":         blog.Summary,
			"slug":            blog.Slug,
			"detailContent":   blog.DetailContent,
			"category":        categoryTitle,
			"categorySlug":    categorySlug,
			"categoryDisplay": categoryTitle,
		})
	}

	total := int64(0)
	if categorySlug != "" {
		// 修改：使用category_code进行统计
		err = globals.GraDBs["system"].Model(&model.Blog{}).Where("status = ? and lang = ?", 1, lang).Where("category_code = ?", categorySlug).Count(&total).Error
	} else {
		err = globals.GraDBs["system"].Model(&model.Blog{}).Where("status = ? and lang = ?", 1, lang).Count(&total).Error
	}
	if err != nil {
		globals.Warn(err.Error())
	}
	data["current"] = pageNoInt
	data["pageTotal"] = int(total/int64(pageSizeInt)) + 1
	data["total"] = total
	data["records"] = records
	c.JSON(200, gin.H{"data": data})
}

// GetBlogDetail 获取博客详情
func (b *BlogApi) GetBlogDetail(c *gin.Context) {
	slug := c.Param("slug") // blog的slug
	lang := c.Query("lang")
	if lang == "zh_cn" {
		lang = "zh"
	}
	blog := model.Blog{}
	err := globals.GraDBs["system"].Where("slug = ? and lang = ?", slug, lang).First(&blog).Error
	if err != nil {
		globals.Warn(err.Error())
	}

	// 修改：使用category_code查询分类
	blog_category := model.BlogCategory{}
	err = globals.GraDBs["system"].Where("slug COLLATE utf8mb4_unicode_ci = ? AND lang = ?", blog.CategoryCode, lang).First(&blog_category).Error
	if err != nil {
		globals.Warn(err.Error())
	}

	// 获取推荐博客列表
	// 修改：使用category_code查询推荐文章
	recommend_blog_list := []model.Blog{}
	err = globals.GraDBs["system"].Where("status = ? and lang = ?", 1, lang).Where("category_code = ?", blog.CategoryCode).Order("updated_at DESC").Limit(3).Find(&recommend_blog_list).Error
	if err != nil {
		globals.Warn(err.Error())
	}

	// Convert recommend blogs to response format with category info
	recommendRecords := []map[string]interface{}{}
	for _, recBlog := range recommend_blog_list {
		recommendRecords = append(recommendRecords, map[string]interface{}{
			"id":              recBlog.Id,
			"title":           recBlog.Title,
			"slug":            recBlog.Slug,
			"summary":         recBlog.Summary,
			"created_at":      recBlog.CreatedAt,
			"updated_at":      recBlog.UpdatedAt,
			"cover":           recBlog.Cover,
			"category":        blog_category.Title,
			"categorySlug":    blog_category.Slug,
			"categoryDisplay": blog_category.Title,
		})
	}

	// Resolve canonical blog ID — if current row is a translation, its
	// MainBlogID points at the canonical; otherwise it IS the canonical.
	canonicalID := uint(blog.Id)
	if blog.MainBlogID != 0 {
		canonicalID = uint(blog.MainBlogID)
	}

	// Collect (lang, slug) for every online row in the canonical chain so
	// the frontend can emit hreflang only for real translations, and use
	// the correct per-locale slug for each URL.
	translations := []map[string]string{}
	if canonicalID != 0 {
		rows := []struct {
			Lang string
			Slug string
		}{}
		globals.GraDBs["system"].
			Table("w_blog").
			Select("lang, slug").
			Where("status = ? AND (id = ? OR main_blog_id = ?)", 1, canonicalID, canonicalID).
			Scan(&rows)
		for _, r := range rows {
			if r.Lang == "" || r.Slug == "" {
				continue
			}
			translations = append(translations, map[string]string{
				"lang": r.Lang,
				"slug": r.Slug,
			})
		}
	}

	// Convert main blog to response format
	blogData := map[string]interface{}{
		"id":                blog.Id,
		"title":             blog.Title,
		"slug":              blog.Slug,
		"summary":           blog.Summary,
		"detailContent":     blog.DetailContent,
		"seoTitle":          blog.SeoTitle,
		"seoKeyword":        blog.SeoKeyword,
		"seoDescription":    blog.SeoDescription,
		"created_at":        blog.CreatedAt,
		"updated_at":        blog.UpdatedAt,
		"cover":             blog.Cover,
		"category":        blog_category.Title,
		"categorySlug":    blog_category.Slug,
		"categoryDisplay": blog_category.Title,
		"translations":    translations,
	}

	c.JSON(200, gin.H{"data": blogData, "categoryTitle": blog_category.Title, "categorySlug": blog_category.Slug, "recommendBlogList": recommendRecords})
}

// SearchPosts 搜索博客
func (b *BlogApi) SearchPosts(c *gin.Context) {
	query := c.Query("query")
	lang := c.Query("lang")
	page := c.DefaultQuery("page", "1")
	limit := c.DefaultQuery("limit", "10")

	if lang == "zh_cn" {
		lang = "zh"
	}

	// Convert page and limit to integers
	pageInt, err := strconv.Atoi(page)
	if err != nil {
		pageInt = 1
	}
	limitInt, err := strconv.Atoi(limit)
	if err != nil {
		limitInt = 10
	}

	// Calculate offset
	offset := (pageInt - 1) * limitInt

	blog_list := []model.Blog{}
	// Enhanced search: search in title, summary, and content
	searchPattern := "%" + query + "%"
	err = globals.GraDBs["system"].Where("status = ? and lang = ?", 1, lang).
		Where("title LIKE ? OR summary LIKE ? OR detail_content LIKE ?", searchPattern, searchPattern, searchPattern).
		Order("updated_at DESC").
		Limit(limitInt).
		Offset(offset).
		Find(&blog_list).Error

	if err != nil {
		globals.Warn(err.Error())
		response.FailWithMessage("Search failed", c)
		return
	}

	// Get total count for pagination
	var total int64
	err = globals.GraDBs["system"].Model(&model.Blog{}).
		Where("status = ? and lang = ?", 1, lang).
		Where("title LIKE ? OR summary LIKE ? OR detail_content LIKE ?", searchPattern, searchPattern, searchPattern).
		Count(&total).Error

	if err != nil {
		globals.Warn(err.Error())
	}

	// Build enhanced response with category information
	records := []map[string]interface{}{}
	for _, blog := range blog_list {
		// Get category information using category_code
		blog_category := model.BlogCategory{}
		categoryDisplay := "Blog" // default fallback
		categorySlug := "blog"    // default fallback

		// 修改：使用category_code查询分类
		err = globals.GraDBs["system"].Where("slug COLLATE utf8mb4_unicode_ci = ? AND lang = ?", blog.CategoryCode, lang).First(&blog_category).Error
		if err == nil {
			categoryDisplay = blog_category.Title
			categorySlug = blog_category.Slug
		}

		records = append(records, map[string]interface{}{
			"id":              blog.Id,
			"title":           blog.Title,
			"slug":            blog.Slug,
			"summary":         blog.Summary,
			"created_at":      blog.CreatedAt,
			"updated_at":      blog.UpdatedAt,
			"cover":           blog.Cover,
			"category":        categoryDisplay,
			"categorySlug":    categorySlug,
			"categoryDisplay": categoryDisplay,
		})
	}

	c.JSON(200, gin.H{
		"data": records,
		"meta": map[string]interface{}{
			"total":   total,
			"page":    pageInt,
			"limit":   limitInt,
			"query":   query,
			"hasMore": int64(pageInt*limitInt) < total,
		},
	})
}

// GetPopularPosts 获取热门博客
func (b *BlogApi) GetPopularPosts(c *gin.Context) {
	lang := c.Query("lang")
	if lang == "zh_cn" {
		lang = "zh"
	}
	blog_list := []model.Blog{}
	err := globals.GraDBs["system"].Where("status = ? and lang = ?", 1, lang).Order("updated_at DESC").Limit(10).Find(&blog_list).Error
	if err != nil {
		globals.Warn(err.Error())
	}

	// Get category information for mapping using category_code
	categoryMap := make(map[string]model.BlogCategory)
	if len(blog_list) > 0 {
		categoryCodes := []string{}
		for _, blog := range blog_list {
			if blog.CategoryCode != "" {
				categoryCodes = append(categoryCodes, blog.CategoryCode)
			}
		}
		if len(categoryCodes) > 0 {
			categories := []model.BlogCategory{}
			globals.GraDBs["system"].Where("slug COLLATE utf8mb4_unicode_ci IN ? AND lang = ?", categoryCodes, lang).Find(&categories)
			for _, cat := range categories {
				categoryMap[cat.Slug] = cat
			}
		}
	}

	// Convert to response format with category info
	records := []map[string]interface{}{}
	for _, blog := range blog_list {
		categoryTitle := ""
		categorySlug := ""
		if cat, exists := categoryMap[blog.CategoryCode]; exists {
			categoryTitle = cat.Title
			categorySlug = cat.Slug
		}

		records = append(records, map[string]interface{}{
			"id":              blog.Id,
			"title":           blog.Title,
			"slug":            blog.Slug,
			"summary":         blog.Summary,
			"created_at":      blog.CreatedAt,
			"updated_at":      blog.UpdatedAt,
			"cover":           blog.Cover,
			"category":        categoryTitle,
			"categorySlug":    categorySlug,
			"categoryDisplay": categoryTitle,
		})
	}

	c.JSON(200, gin.H{"data": records})
}
