package model

import "server/globals"

type UserAssetLedger struct {
	globals.GraMODEL
	UID              int    `json:"uid" gorm:"column:uid;not null;default:0;uniqueIndex:uk_uid_source_source_item,priority:1;index:idx_uid_source_created,priority:1;index:idx_uid_project,priority:1;index:idx_uid_thread,priority:1;index:idx_uid_record,priority:1;index:idx_uid_task,priority:1;index:idx_uid_global_asset,priority:1;comment:用户ID"`
	Source           string `json:"source" gorm:"column:source;type:varchar(64);not null;default:'';uniqueIndex:uk_uid_source_source_item,priority:2;index:idx_uid_source_created,priority:2;index:idx_uid_project,priority:3;index:idx_uid_thread,priority:3;comment:来源类型"`
	SourceID         uint64 `json:"sourceId" gorm:"column:source_id;type:bigint unsigned;not null;default:0;uniqueIndex:uk_uid_source_source_item,priority:3;comment:来源记录ID"`
	GlobalAssetID    uint   `json:"globalAssetId,omitempty" gorm:"column:global_asset_id;type:bigint unsigned;not null;default:0;index:idx_uid_global_asset,priority:2;comment:可选的 w_global_asset.id 桥接"`
	ItemKey          string `json:"itemKey" gorm:"column:item_key;type:varchar(255);not null;default:'';uniqueIndex:uk_uid_source_source_item,priority:4;comment:同一来源记录下的子项键"`
	VisibilityStatus int8   `json:"visibilityStatus" gorm:"column:visibility_status;type:tinyint;not null;default:1;comment:可见性状态 1=visible 2=hidden"`
	ContainerType    string `json:"containerType" gorm:"column:container_type;type:varchar(32);not null;default:'';comment:容器类型"`
	ContainerKey     string `json:"containerKey" gorm:"column:container_key;type:varchar(255);not null;default:'';comment:容器唯一键"`
	ContainerTitle   string `json:"containerTitle" gorm:"column:container_title;type:varchar(255);not null;default:'';comment:容器标题"`
	ContainerUUID    string `json:"containerUUID" gorm:"column:container_uuid;type:varchar(128);not null;default:'';comment:容器UUID"`
	Title            string `json:"title" gorm:"column:title;type:varchar(512);not null;default:'';comment:资源标题"`
	Kind             string `json:"kind" gorm:"column:kind;type:varchar(64);not null;default:'';comment:资源类型"`
	MimeType         string `json:"mimeType" gorm:"column:mime_type;type:varchar(128);not null;default:'';comment:MIME类型"`
	SizeBytes        int64  `json:"sizeBytes" gorm:"column:size_bytes;type:bigint;not null;default:0;comment:文件大小"`
	Width            int    `json:"width" gorm:"column:width;type:int;not null;default:0;comment:宽度"`
	Height           int    `json:"height" gorm:"column:height;type:int;not null;default:0;comment:高度"`
	URL              string `json:"url" gorm:"column:url;type:varchar(2048);not null;default:'';comment:资源访问URL"`
	ThumbURL         string `json:"thumbUrl" gorm:"column:thumb_url;type:varchar(2048);not null;default:'';comment:缩略图URL"`
	PreviewURL       string `json:"previewUrl" gorm:"column:preview_url;type:varchar(2048);not null;default:'';comment:预览图URL"`
	ProjectID        uint   `json:"projectId" gorm:"column:project_id;type:bigint unsigned;not null;default:0;index:idx_uid_project,priority:2;comment:Canvas项目ID"`
	ProjectTitle     string `json:"projectTitle" gorm:"column:project_title;type:varchar(255);not null;default:'';comment:Canvas项目标题"`
	ProjectUUID      string `json:"projectUUID" gorm:"column:project_uuid;type:varchar(64);not null;default:'';comment:Canvas项目UUID"`
	ThreadID         uint   `json:"threadId" gorm:"column:thread_id;type:bigint unsigned;not null;default:0;index:idx_uid_thread,priority:2;comment:线程ID"`
	ThreadName       string `json:"threadName" gorm:"column:thread_name;type:varchar(255);not null;default:'';comment:线程名称"`
	ThreadUUID       string `json:"threadUUID" gorm:"column:thread_uuid;type:varchar(64);not null;default:'';comment:线程UUID"`
	ToolID           string `json:"toolId" gorm:"column:tool_id;type:varchar(64);not null;default:'';comment:工具ID"`
	RecordID         uint   `json:"recordId" gorm:"column:record_id;type:bigint unsigned;not null;default:0;index:idx_uid_record,priority:2;comment:生成记录ID"`
	TaskID           string `json:"taskId" gorm:"column:task_id;type:varchar(64);not null;default:'';index:idx_uid_task,priority:2;comment:生成任务ID"`
	ObjectKey        string `json:"objectKey" gorm:"column:object_key;type:varchar(1024);not null;default:'';comment:对象Key"`
	StoragePath      string `json:"storagePath" gorm:"column:storage_path;type:varchar(2048);not null;default:'';comment:存储路径或URL"`
	IsAttached       bool   `json:"isAttached" gorm:"column:is_attached;not null;default:true;comment:是否绑定容器"`
	HasManagedObject bool   `json:"hasManagedObject" gorm:"column:has_managed_object;not null;default:false;comment:是否有托管对象"`
}

func (UserAssetLedger) TableName() string {
	return "w_user_asset_ledger"
}

const (
	UserAssetLedgerVisibilityVisible int8 = 1
	UserAssetLedgerVisibilityHidden  int8 = 2
)
