package model

import (
	"server/globals"
)

type Blog struct {
	globals.GraMODEL
	CategoryCode   string `json:"categoryCode" gorm:"column:category_code;type:varchar(50);not null;default:'';comment:分类代码"`
	Title          string `json:"title" gorm:"column:title;type:varchar(255);comment:标题"`
	Cover          string `json:"cover" gorm:"column:cover;type:varchar(248);default:'';comment:封面"`
	SeoTitle       string `json:"seoTitle" gorm:"column:seo_title;type:varchar(255);comment:seo标题"`
	SeoKeyword     string `json:"seoKeyword" gorm:"column:seo_keyword;type:varchar(248);comment:seo关键词"`
	SeoDescription string `json:"seoDescription" gorm:"column:seo_description;type:text;comment:seo详细描述"`
	Summary        string `json:"summary" gorm:"column:summary;type:varchar(500);comment:博客摘要"`
	Slug           string `json:"slug" gorm:"column:slug;type:varchar(256);not null;comment:slug标识"`
	DetailContent  string `json:"detailContent" gorm:"column:detail_content;type:text;comment:详情描述"`
	Sort           int    `json:"sort" gorm:"column:sort;comment:排序"`
	Status         int    `json:"status" gorm:"column:status;not null;default:1;comment:0:禁用,1:正常"`
	Lang           string `json:"lang" gorm:"column:lang;type:varchar(10);not null;default:en;comment:语言"`
	MainBlogID     int    `json:"mainBlogID" gorm:"column:main_blog_id;comment:主BlogID"`
	AIRecordID     int    `json:"aiRecordID" gorm:"column:ai_record_id;comment:AI记录ID"`
}

func (Blog) TableName() string {
	return "w_blog"
}
