package model

import (
	"server/globals"
)

// PromptCategory Prompt分类（多语言）
type PromptCategory struct {
	globals.GraMODEL
	Code           string `json:"code" gorm:"column:code;type:varchar(50);not null;uniqueIndex:uk_code_lang;comment:分类代码"`
	Name           string `json:"name" gorm:"column:name;type:varchar(100);comment:分类名称"`
	Lang           string `json:"lang" gorm:"column:lang;type:varchar(10);not null;default:'en';uniqueIndex:uk_code_lang;comment:语言"`
	MainCategoryID uint   `json:"mainCategoryID" gorm:"column:main_category_id;default:0;comment:主分类ID"`
	Icon           string `json:"icon" gorm:"column:icon;type:varchar(50);comment:图标名称"`
	Color          string `json:"color" gorm:"column:color;type:varchar(50);comment:渐变色类名"`
	PromptCount    int    `json:"promptCount" gorm:"column:prompt_count;default:0;comment:Prompt数量"`
	Sort           int    `json:"sort" gorm:"column:sort;default:0;comment:排序"`
	Status         int    `json:"status" gorm:"column:status;default:1;comment:状态 0禁用 1启用"`
}

func (PromptCategory) TableName() string {
	return "w_prompt_category"
}
