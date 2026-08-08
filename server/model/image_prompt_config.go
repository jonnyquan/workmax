package model

import (
	"server/globals"
)

// ImagePromptConfig stores user-saved image prompt builder configurations.
// Indexes:
//   - idx_ip_uid: single-user list (ListConfigs, quota count, save tx lock)
type ImagePromptConfig struct {
	globals.GraMODEL
	UID        uint   `json:"uid" gorm:"column:uid;not null;index:idx_ip_uid;comment:用户ID"`
	Name       string `json:"name" gorm:"column:name;type:varchar(100);not null;comment:配置名称"`
	Mode       string `json:"mode" gorm:"column:mode;type:varchar(20);not null;comment:模式(portrait|product|grid|scene|instruct|infographic|freeform)"`
	BlocksJSON string `json:"blocksJSON" gorm:"column:blocks_json;type:text;not null;comment:区块JSON数据"`
	Thumbnail  string `json:"thumbnail" gorm:"column:thumbnail;type:varchar(500);default:null;comment:预览摘要"`
	IsFavorite bool   `json:"isFavorite" gorm:"column:is_favorite;type:tinyint(1);default:0;comment:是否收藏"`
}

func (ImagePromptConfig) TableName() string {
	return "w_prompt_config_image"
}
