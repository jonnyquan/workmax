package model

import (
	"server/globals"
)

type Identity struct {
	globals.GraMODEL
	Name           string `json:"name" gorm:"column:name;type:varchar(50);not null;comment:身份名称"`
	Code           string `json:"code" gorm:"column:code;type:varchar(50);not null;comment:身份编码"`
	Status         int    `json:"status" gorm:"column:status;not null;default:1;comment:0:禁用,1:正常"`
	Lang           string `json:"lang" gorm:"column:lang;type:varchar(10);not null;default:'en';comment:语言"`
	Sort           int    `json:"sort" gorm:"column:sort;not null;default:0;comment:排序"`
	MainIdentityID int    `json:"main_identity_id" gorm:"column:main_identity_id;not null;default:0;comment:主身份ID"`
}

func (Identity) TableName() string {
	return "w_identity"
}
