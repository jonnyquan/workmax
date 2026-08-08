package model

import (
	"server/globals"
)

// PromptTag 热门标签
type PromptTag struct {
	globals.GraMODEL
	Name       string `json:"name" gorm:"column:name;type:varchar(100);not null;comment:标签名称"`
	Slug       string `json:"slug" gorm:"column:slug;type:varchar(100);not null;uniqueIndex:uk_slug_lang;comment:URL标识"`
	Lang       string `json:"lang" gorm:"column:lang;type:varchar(10);not null;default:'en';uniqueIndex:uk_slug_lang;comment:语言代码"`
	Category   string `json:"category" gorm:"column:category;type:varchar(50);comment:分类(style/subject/mood)"`
	UsageCount int    `json:"usageCount" gorm:"column:usage_count;default:0;index;comment:使用次数"`
	TrendScore int    `json:"trendScore" gorm:"column:trend_score;default:0;index;comment:热度趋势分"`
	IsPopular  bool   `json:"isPopular" gorm:"column:is_popular;default:false;index;comment:首页展示"`
	Sort       int    `json:"sort" gorm:"column:sort;default:0;comment:排序"`
	Status     int    `json:"status" gorm:"column:status;default:1;comment:状态 0禁用 1启用"`
}

func (PromptTag) TableName() string {
	return "w_prompt_tag"
}
