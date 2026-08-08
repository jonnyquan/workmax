package model

import (
	"server/globals"
)

// VideoPromptConfig stores user-saved video prompt builder configurations.
// Indexes:
//   - idx_vp_uid: single-user list (ListConfigs, quota count, save tx lock)
type VideoPromptConfig struct {
	globals.GraMODEL
	UID        uint   `json:"uid" gorm:"column:uid;not null;index:idx_vp_uid;comment:用户ID"`
	Name       string `json:"name" gorm:"column:name;type:varchar(100);not null;comment:配置名称"`
	Adapter    string `json:"adapter" gorm:"column:adapter;type:varchar(30);not null;comment:目标模型适配器"`
	IRJSON     string `json:"irJSON" gorm:"column:ir_json;type:mediumtext;not null;comment:VideoPromptIR的JSON数据"`
	Thumbnail  string `json:"thumbnail" gorm:"column:thumbnail;type:varchar(500);default:null;comment:预览摘要"`
	IsFavorite bool   `json:"isFavorite" gorm:"column:is_favorite;type:tinyint(1);default:0;comment:是否收藏"`
}

func (VideoPromptConfig) TableName() string {
	return "w_prompt_config_video"
}
