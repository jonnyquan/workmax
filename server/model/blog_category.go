package model

import (
	"server/globals"
)

type BlogCategory struct {
	globals.GraMODEL
	Title          string `json:"title" gorm:"column:title;type:varchar(255);comment:标题"`
	SeoTitle       string `json:"seoTitle" gorm:"column:seo_title;type:varchar(255);comment:seo标题"`
	SeoKeyword     string `json:"seoKeyword" gorm:"column:seo_keyword;type:varchar(248);comment:seo关键词"`
	SeoDescription string `json:"seoDescription" gorm:"column:seo_description;type:text;comment:seo详细描述"`
	Slug           string `json:"slug" gorm:"column:slug;type:varchar(50);not null;comment:slug标识"`
	Sort           int    `json:"sort" gorm:"column:sort;comment:排序"`
	Status         int    `json:"status" gorm:"column:status;not null;default:1;comment:0:未启用,1:已启用"`
	Lang           string `json:"lang" gorm:"column:lang;type:varchar(10);not null;default:en;comment:语言"`
	MainCategoryID int    `json:"mainCategoryID" gorm:"column:main_category_id;comment:主分类ID"`
}

func (BlogCategory) TableName() string {
	return "w_blog_category"
}
