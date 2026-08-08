package model

import (
	"encoding/json"
	"server/globals"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Prompt AI视频/图像生成Prompt (支持多语言 + 多模型)
type Prompt struct {
	globals.GraMODEL

	// 基础信息
	SourceType string `json:"sourceType" gorm:"column:source_type;type:varchar(50);not null;index;comment:来源类型(local/import/github/opennana/civitai)"`
	SourceID   string `json:"sourceID" gorm:"column:source_id;type:varchar(100);index;comment:原始ID或slug"`
	Slug       string `json:"slug" gorm:"column:slug;type:varchar(256);not null;uniqueIndex:uk_slug_lang;comment:URL标识"`

	// 多语言
	Title        string `json:"title" gorm:"column:title;type:varchar(500);comment:标题"`
	Lang         string `json:"lang" gorm:"column:lang;type:varchar(10);not null;default:'en';uniqueIndex:uk_slug_lang;index;comment:语言"`
	MainPromptID uint   `json:"mainPromptID" gorm:"column:main_prompt_id;default:0;index;comment:主Prompt ID(多语言关联)"`

	// Prompt内容
	PromptContent     string `json:"promptContent" gorm:"column:prompt_content;type:text;comment:完整prompt"`
	PromptPreview     string `json:"promptPreview" gorm:"column:prompt_preview;type:varchar(200);comment:前端预览截断"`
	NegativePrompt    string `json:"negativePrompt" gorm:"column:negative_prompt;type:text;comment:负面prompt"`
	PromptDescription string `json:"promptDescription" gorm:"column:prompt_description;type:text;comment:prompt说明"`
	Medium            string `json:"medium" gorm:"column:medium;type:varchar(32);not null;default:'image';index;comment:媒体类型(image/video)"`
	PromptType        string `json:"promptType" gorm:"column:prompt_type;type:varchar(64);not null;default:'text_to_image';index;comment:prompt类型(text_to_image/text_to_video/image_to_video/image_edit)"`

	// 资源
	CoverImage       string `json:"coverImage" gorm:"column:cover_image;type:varchar(500);comment:原始封面URL"`
	LocalImage       string `json:"localImage" gorm:"column:local_image;type:varchar(500);comment:本地/OSS路径"`
	ThumbImage       string `json:"thumbImage" gorm:"column:thumb_image;type:varchar(500);comment:缩略图路径"`
	ImageWidth       int    `json:"imageWidth" gorm:"column:image_width;comment:图片宽度"`
	ImageHeight      int    `json:"imageHeight" gorm:"column:image_height;comment:图片高度"`
	AspectRatio      string `json:"aspectRatio" gorm:"column:aspect_ratio;type:varchar(20);index;comment:tall/wide/square"`
	ImageGradient    string `json:"imageGradient" gorm:"column:image_gradient;type:varchar(100);comment:主色调渐变(备用)"`
	PreviewAssetType string `json:"previewAssetType" gorm:"column:preview_asset_type;type:varchar(32);not null;default:'image';comment:预览资源类型(image/video)"`
	PreviewVideo     string `json:"previewVideo" gorm:"column:preview_video;type:varchar(500);comment:视频预览URL"`

	// 模型支持
	SupportedModels string `json:"supportedModels" gorm:"column:supported_models;type:varchar(500);comment:支持的模型JSON数组"`
	PrimaryModel    string `json:"primaryModel" gorm:"column:primary_model;type:varchar(100);index;comment:主推荐模型"`
	ModelParams     string `json:"modelParams" gorm:"column:model_params;type:text;comment:模型参数JSON"`

	// 分类标签
	Category   string `json:"category" gorm:"column:category;type:varchar(100);index;comment:主分类"`
	Tags       string `json:"tags" gorm:"column:tags;type:varchar(1000);comment:标签JSON数组"`
	Style      string `json:"style" gorm:"column:style;type:varchar(100);index;comment:风格"`
	TagsWeight string `json:"tagsWeight" gorm:"column:tags_weight;type:text;comment:标签权重JSON用于搜索排序"`

	// SEO
	SeoTitle       string `json:"seoTitle" gorm:"column:seo_title;type:varchar(255);comment:SEO标题"`
	SeoKeyword     string `json:"seoKeyword" gorm:"column:seo_keyword;type:varchar(255);comment:SEO关键词"`
	SeoDescription string `json:"seoDescription" gorm:"column:seo_description;type:text;comment:SEO描述"`

	// 状态与排序
	Status     int  `json:"status" gorm:"column:status;not null;default:0;index;comment:0待审核 1已发布 2已拒绝"`
	Sort       int  `json:"sort" gorm:"column:sort;default:0;comment:排序权重"`
	IsFeatured bool `json:"isFeatured" gorm:"column:is_featured;default:false;index;comment:首页精选"`
	IsTrending bool `json:"isTrending" gorm:"column:is_trending;default:false;index;comment:热门标记"`

	// 互动统计
	ViewCount   int     `json:"viewCount" gorm:"column:view_count;default:0;comment:浏览次数"`
	CopyCount   int     `json:"copyCount" gorm:"column:copy_count;default:0;comment:复制次数"`
	LikeCount   int     `json:"likeCount" gorm:"column:like_count;default:0;comment:点赞数"`
	Rating      float32 `json:"rating" gorm:"column:rating;default:0;comment:评分(1-5)"`
	RatingCount int     `json:"ratingCount" gorm:"column:rating_count;default:0;comment:评分人数"`

	// 搜索优化
	SearchVector string `json:"searchVector" gorm:"column:search_vector;type:text;comment:全文搜索向量"`
	PopularScore int    `json:"popularScore" gorm:"column:popular_score;default:0;index;comment:热度分数"`

	// 导入追踪
	ContentHash string    `json:"contentHash" gorm:"column:content_hash;type:varchar(64);index;comment:内容hash"`
	CollectedAt time.Time `json:"collectedAt" gorm:"column:collected_at;comment:导入时间"`
	LastSyncAt  time.Time `json:"lastSyncAt" gorm:"column:last_sync_at;comment:最后更新时间"`
}

func (Prompt) TableName() string {
	return "w_prompt"
}

// BuildSearchVector 构建用于模糊搜索的向量字符串
func (p *Prompt) BuildSearchVector() string {
	var parts []string

	if p.Title != "" {
		parts = append(parts, p.Title, p.Title, p.Title)
	}
	if p.Medium != "" {
		parts = append(parts, p.Medium)
	}
	if p.PromptType != "" {
		parts = append(parts, p.PromptType)
	}
	if p.Category != "" {
		parts = append(parts, p.Category, p.Category)
	}
	if p.Style != "" {
		parts = append(parts, p.Style, p.Style)
	}

	if p.Tags != "" {
		var tags []string
		if err := json.Unmarshal([]byte(p.Tags), &tags); err == nil {
			for _, tag := range tags {
				parts = append(parts, tag, tag)
			}
		}
	}

	if p.PrimaryModel != "" {
		parts = append(parts, p.PrimaryModel)
	}

	if p.SupportedModels != "" {
		var models []string
		if err := json.Unmarshal([]byte(p.SupportedModels), &models); err == nil {
			parts = append(parts, models...)
		}
	}

	if p.PromptContent != "" {
		content := p.PromptContent
		if len(content) > 500 {
			content = content[:500]
		}
		parts = append(parts, content)
	}

	if p.PromptDescription != "" {
		desc := p.PromptDescription
		if len(desc) > 300 {
			desc = desc[:300]
		}
		parts = append(parts, desc)
	}

	if p.SeoKeyword != "" {
		parts = append(parts, p.SeoKeyword)
	}

	return strings.ToLower(strings.Join(parts, " "))
}

// UpdateSearchVector 更新搜索向量字段
func (p *Prompt) UpdateSearchVector() {
	p.SearchVector = p.BuildSearchVector()
}

// RebuildAllSearchVectors 批量重建所有已发布Prompt的搜索向量
func RebuildAllSearchVectors(db *gorm.DB) (total int64, updated int64) {
	batchSize := 200

	db.Model(&Prompt{}).Where("status = 1").Count(&total)

	for offset := 0; ; offset += batchSize {
		var prompts []Prompt
		db.Where("status = 1").Order("id ASC").Offset(offset).Limit(batchSize).Find(&prompts)

		if len(prompts) == 0 {
			break
		}

		tx := db.Begin()
		for i := range prompts {
			prompts[i].UpdateSearchVector()
			if err := tx.Model(&Prompt{}).Where("id = ?", prompts[i].Id).
				UpdateColumn("search_vector", prompts[i].SearchVector).Error; err != nil {
				tx.Rollback()
				break
			}
			updated++
		}
		tx.Commit()
	}

	return total, updated
}
