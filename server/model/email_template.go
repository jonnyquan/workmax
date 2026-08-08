package model

import (
	"server/globals"
)

type EmailTemplate struct {
	globals.GraMODEL
	Name        string `json:"name" gorm:"column:name;type:varchar(255);not null;comment:模板名称"`
	Code        string `json:"code" gorm:"column:code;type:varchar(100);not null;comment:模板代码（唯一标识）"`
	Subject     string `json:"subject" gorm:"column:subject;type:varchar(500);not null;comment:邮件主题"`
	Content     string `json:"content" gorm:"column:content;type:text;not null;comment:HTML内容"`
	Category    string `json:"category" gorm:"column:category;type:varchar(50);default:marketing;comment:分类"`
	Variables   string `json:"variables" gorm:"column:variables;type:text;comment:JSON格式的变量说明"`
	PreviewText string `json:"previewText" gorm:"column:preview_text;type:varchar(255);comment:预览文本"`
	Description string `json:"description" gorm:"column:description;type:varchar(500);comment:模板描述"`
	Status      int    `json:"status" gorm:"column:status;type:int;default:1;comment:0:禁用,1:启用"`
}

func (EmailTemplate) TableName() string {
	return "w_email_template"
}

const (
	EMAIL_TEMPLATE_CATEGORY_TRANSACTIONAL = "transactional"
	EMAIL_TEMPLATE_CATEGORY_MARKETING     = "marketing"
	EMAIL_TEMPLATE_CATEGORY_NOTIFICATION  = "notification"
)

const (
	EMAIL_TEMPLATE_STATUS_DISABLED = 0
	EMAIL_TEMPLATE_STATUS_ENABLED  = 1
)
