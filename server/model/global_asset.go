package model

import (
	"server/globals"
	"time"
)

// GlobalAsset is the platform-level media asset index shared by Canvas,
// generators, prompt builders, and WorkAgent. Source tables remain the
// execution/storage facts during migration; this row owns reuse, permission,
// and product-level metadata.
type GlobalAsset struct {
	globals.GraMODEL
	DeletedAt     *time.Time `json:"-" gorm:"column:deleted_at;index"`
	UID           int        `json:"uid" gorm:"column:uid;not null;default:0;index:idx_global_asset_uid_updated,priority:1;comment:所属用户"`
	TeamID        *uint64    `json:"teamId,omitempty" gorm:"column:team_id;type:bigint unsigned;default:null;index:idx_global_asset_team;comment:团队ID(预留)"`
	ProjectID     *uint      `json:"projectId,omitempty" gorm:"column:project_id;type:bigint unsigned;default:null;index:idx_global_asset_project_updated,priority:1;comment:项目ID"`
	UUID          string     `json:"uuid" gorm:"column:uuid;type:varchar(36);not null;uniqueIndex:uk_global_asset_uuid;comment:公开安全UUID"`
	Kind          string     `json:"kind" gorm:"column:kind;type:varchar(32);not null;default:'image';index:idx_global_asset_kind_status_updated,priority:1;comment:类型 image/video/audio/document/archive"`
	Source        string     `json:"source" gorm:"column:source;type:varchar(32);not null;default:'upload';comment:来源 upload/generation/agent/import/shared_copy"`
	SourceTable   string     `json:"sourceTable" gorm:"column:source_table;type:varchar(64);not null;default:'';uniqueIndex:uk_global_asset_source,priority:1;comment:来源事实表"`
	SourceID      uint64     `json:"sourceId" gorm:"column:source_id;type:bigint unsigned;not null;default:0;uniqueIndex:uk_global_asset_source,priority:2;comment:来源事实表ID"`
	SourceItemKey string     `json:"sourceItemKey" gorm:"column:source_item_key;type:varchar(128);not null;default:'';uniqueIndex:uk_global_asset_source,priority:3;comment:来源子项"`
	URL           string     `json:"url" gorm:"column:url;type:varchar(2048);not null;default:'';comment:规范访问URL"`
	ThumbURL      string     `json:"thumbUrl" gorm:"column:thumb_url;type:varchar(2048);not null;default:'';comment:缩略图URL"`
	MimeType      string     `json:"mimeType" gorm:"column:mime_type;type:varchar(128);not null;default:'';comment:MIME类型"`
	SizeBytes     int64      `json:"sizeBytes" gorm:"column:size_bytes;type:bigint;not null;default:0;comment:文件大小"`
	Width         int        `json:"width" gorm:"column:width;type:int;not null;default:0;comment:宽度"`
	Height        int        `json:"height" gorm:"column:height;type:int;not null;default:0;comment:高度"`
	DurationMs    int        `json:"durationMs" gorm:"column:duration_ms;type:int;not null;default:0;comment:时长毫秒"`
	ContentHash   string     `json:"contentHash" gorm:"column:content_hash;type:char(64);not null;default:'';index:idx_global_asset_uid_hash,priority:2;comment:内容哈希"`
	Status        int8       `json:"status" gorm:"column:status;type:tinyint;not null;default:1;index:idx_global_asset_kind_status_updated,priority:2;comment:状态"`
	Visibility    int8       `json:"visibility" gorm:"column:visibility;type:tinyint;not null;default:1;comment:可见性"`
	ParentAssetID uint       `json:"parentAssetId,omitempty" gorm:"column:parent_asset_id;type:bigint unsigned;not null;default:0;index:idx_global_asset_parent_variant,priority:1;comment:父资产ID"`
	VariantType   string     `json:"variantType" gorm:"column:variant_type;type:varchar(32);not null;default:'original';index:idx_global_asset_parent_variant,priority:2;comment:变体类型"`
	Metadata      JSONMap    `json:"metadata" gorm:"column:metadata;type:json;default:null;comment:附加元数据"`
}

func (GlobalAsset) TableName() string {
	return "w_global_asset"
}

const (
	GlobalAssetKindImage    = "image"
	GlobalAssetKindVideo    = "video"
	GlobalAssetKindAudio    = "audio"
	GlobalAssetKindDocument = "document"
	GlobalAssetKindArchive  = "archive"

	GlobalAssetSourceUpload     = "upload"
	GlobalAssetSourceGeneration = "generation"
	GlobalAssetSourceAgent      = "agent"
	GlobalAssetSourceImport     = "import"
	GlobalAssetSourceSharedCopy = "shared_copy"

	GlobalAssetVariantOriginal = "original"
	GlobalAssetVariantThumb    = "thumb"
	GlobalAssetVariantPreview  = "preview"
	GlobalAssetVariantPoster   = "poster"
	GlobalAssetVariantKeyframe = "keyframe"
	GlobalAssetVariantSource   = "source"

	GlobalAssetStatusActive     int8 = 1
	GlobalAssetStatusProcessing int8 = 2
	GlobalAssetStatusFailed     int8 = 3
	GlobalAssetStatusDeleted    int8 = 4

	GlobalAssetVisibilityPrivate  int8 = 1
	GlobalAssetVisibilityProject  int8 = 2
	GlobalAssetVisibilityUnlisted int8 = 3
	GlobalAssetVisibilityPublic   int8 = 4
)
