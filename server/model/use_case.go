package model

import (
	"server/globals"
	"time"
)

type UseCase struct {
	globals.GraMODEL
	Slug        string    `json:"slug" gorm:"column:slug;type:varchar(255);not null;uniqueIndex:uk_slug_lang;comment:slug标识"`
	Title       string    `json:"title" gorm:"column:title;type:varchar(255);comment:标题"`
	Summary     string    `json:"summary" gorm:"column:summary;type:varchar(500);comment:摘要"`
	Description string    `json:"description" gorm:"column:description;type:text;comment:SEO描述"`
	Cover       string    `json:"cover" gorm:"column:cover;type:varchar(255);comment:封面"`
	Gallery     string    `json:"gallery" gorm:"column:gallery;type:text;comment:结果图集(JSON)"`
	AppSlug     string    `json:"appSlug" gorm:"column:app_slug;type:varchar(100);index;comment:关联App的slug"`
	PromptUsed  string    `json:"promptUsed" gorm:"column:prompt_used;type:text;comment:使用的Prompt"`
	Steps       string    `json:"steps" gorm:"column:steps;type:text;comment:使用步骤(JSON)"`
	Tags        string    `json:"tags" gorm:"column:tags;type:text;comment:标签(JSON)"`
	Difficulty  string    `json:"difficulty" gorm:"column:difficulty;type:varchar(20);default:Beginner;comment:难度"`
	Content     string    `json:"content" gorm:"column:content;type:longtext;comment:详细内容"`
	Author      string    `json:"author" gorm:"column:author;type:text;comment:作者信息(JSON)"`
	Seo         string    `json:"seo" gorm:"column:seo;type:text;comment:SEO信息(JSON)"`
	PublishedAt time.Time `json:"publishedAt" gorm:"column:published_at;comment:发布时间"`
	Status      int       `json:"status" gorm:"column:status;default:1;comment:状态 1:发布 2:草稿"`
	Lang        string    `json:"lang" gorm:"column:lang;type:varchar(10);default:en;uniqueIndex:uk_slug_lang;comment:语言"`
}

func (UseCase) TableName() string {
	return "w_use_case"
}
