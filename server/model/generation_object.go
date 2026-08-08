package model

import "server/globals"

// GenerationObject stores object-level metadata for generated assets persisted to object storage.
type GenerationObject struct {
	globals.GraMODEL
	UID           int    `json:"uid" gorm:"column:uid;not null;default:0;index:idx_uid_kind;comment:用户ID"`
	TaskID        string `json:"taskId" gorm:"column:task_id;type:varchar(64);not null;default:'';index:idx_task_id;comment:生成任务ID"`
	RecordID      uint   `json:"recordId" gorm:"column:record_id;type:bigint unsigned;not null;default:0;index:idx_record_id;comment:生成记录ID"`
	ToolID        string `json:"toolId" gorm:"column:tool_id;type:varchar(50);not null;default:'';index:idx_tool_id;comment:工具ID"`
	Provider      string `json:"provider" gorm:"column:provider;type:varchar(32);not null;default:'r2';comment:存储提供方"`
	Bucket        string `json:"bucket" gorm:"column:bucket;type:varchar(255);not null;default:'';uniqueIndex:uk_bucket_object_key;comment:Bucket名称"`
	ObjectKey     string `json:"objectKey" gorm:"column:object_key;type:varchar(1024);not null;default:'';uniqueIndex:uk_bucket_object_key;comment:对象Key"`
	AssetKind     string `json:"assetKind" gorm:"column:asset_kind;type:varchar(32);not null;default:'';index:idx_uid_kind;comment:资源类型:image/video/thumbnail/asset/reference"`
	ContentType   string `json:"contentType" gorm:"column:content_type;type:varchar(128);not null;default:'';comment:MIME类型"`
	SizeBytes     int64  `json:"sizeBytes" gorm:"column:size_bytes;type:bigint;not null;default:0;comment:文件大小(字节)"`
	ETag          string `json:"etag" gorm:"column:etag;type:varchar(128);not null;default:'';comment:对象ETag"`
	PublicURL     string `json:"publicUrl" gorm:"column:public_url;type:varchar(2048);not null;default:'';comment:当前公开访问URL"`
	SourceURL     string `json:"sourceUrl" gorm:"column:source_url;type:varchar(2048);not null;default:'';comment:原始远程URL"`
	Status        int8   `json:"status" gorm:"column:status;type:tinyint;not null;default:1;comment:1=active 2=deleted 3=orphan 4=hidden"`
	GlobalAssetID uint   `json:"globalAssetId,omitempty" gorm:"column:global_asset_id;type:bigint unsigned;not null;default:0;index:idx_generation_object_global_asset;comment:可选的 w_global_asset.id 桥接"`
}

func (GenerationObject) TableName() string {
	return "w_generation_object"
}

const (
	GenerationObjectStatusActive  int8 = 1
	GenerationObjectStatusDeleted int8 = 2
	GenerationObjectStatusOrphan  int8 = 3
	GenerationObjectStatusHidden  int8 = 4
)
