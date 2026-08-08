package model

import "server/globals"

// GlobalModel is the platform-level model catalog. It describes what a model is
// and what it can do; provider credentials/routing remain in w_generator_provider.
type GlobalModel struct {
	globals.GraMODEL
	ModelID       string  `json:"modelId" gorm:"column:model_id;type:varchar(128);not null;uniqueIndex:uk_global_model_media,priority:1;comment:稳定模型ID"`
	MediaType     string  `json:"mediaType" gorm:"column:media_type;type:varchar(20);not null;uniqueIndex:uk_global_model_media,priority:2;index:idx_global_model_media_status,priority:1;comment:image/video/audio"`
	ProviderType  string  `json:"providerType" gorm:"column:provider_type;type:varchar(32);not null;default:'';index:idx_global_model_provider;comment:默认供应商类型"`
	DisplayName   string  `json:"displayName" gorm:"column:display_name;type:varchar(128);not null;default:'';comment:展示名称"`
	Status        int8    `json:"status" gorm:"column:status;type:tinyint;not null;default:1;index:idx_global_model_media_status,priority:2;comment:1启用 0停用"`
	PricingStatus string  `json:"pricingStatus" gorm:"column:pricing_status;type:varchar(20);not null;default:'';comment:official/estimated"`
	SortOrder     int     `json:"sortOrder" gorm:"column:sort_order;not null;default:0;comment:展示排序"`
	Capabilities  JSONMap `json:"capabilities,omitempty" gorm:"column:capabilities;type:json;comment:能力快照"`
	Metadata      JSONMap `json:"metadata,omitempty" gorm:"column:metadata;type:json;comment:扩展元数据"`
}

func (GlobalModel) TableName() string {
	return "w_global_model"
}

const (
	GlobalModelStatusDisabled int8 = 0
	GlobalModelStatusEnabled  int8 = 1
)
