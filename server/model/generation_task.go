package model

import (
	"database/sql/driver"
	"encoding/json"
	"server/globals"
	"strings"
	"time"
)

// GenerationTask 异步生成任务表
type GenerationTask struct {
	globals.GraMODEL
	TaskID      string     `json:"taskId" gorm:"column:task_id;type:varchar(64);not null;uniqueIndex:idx_task_id;comment:任务ID (UUID)"`
	UID         int        `json:"uid" gorm:"column:uid;not null;index:idx_uid;comment:用户ID"`
	ToolID      string     `json:"toolId" gorm:"column:tool_id;type:varchar(50);not null;index:idx_tool_id;comment:工具ID"`
	Model       string     `json:"model" gorm:"column:model;type:varchar(50);not null;index:idx_model;comment:使用模型"`
	Status      TaskStatus `json:"status" gorm:"column:status;type:tinyint;not null;default:0;index:idx_status;comment:任务状态"`
	Progress    int        `json:"progress" gorm:"column:progress;type:int;default:0;comment:进度 0-100"`
	RequestData JSONMap    `json:"requestData" gorm:"column:request_data;type:json;comment:请求数据JSON"`
	ResultData  JSONMap    `json:"resultData" gorm:"column:result_data;type:json;comment:结果数据JSON"`
	ErrorMsg    string     `json:"errorMsg" gorm:"column:error_msg;type:text;default:null;comment:错误信息"`
	CreditsUsed int        `json:"creditsUsed" gorm:"column:credits_used;type:int;default:0;comment:消耗的Credits"`
	DurationMs  int        `json:"durationMs" gorm:"column:duration_ms;type:int;default:0;comment:处理耗时(毫秒)"`
	ThreadID    uint       `json:"threadId,omitempty" gorm:"column:thread_id;type:bigint unsigned;not null;default:0;index:idx_generation_task_uid_tool_thread_id,priority:3;comment:来源线程ID(WorkAgent render_shot等)"`
	MessageID   uint       `json:"messageId,omitempty" gorm:"column:message_id;type:bigint unsigned;not null;default:0;comment:来源消息ID(WorkAgent render_shot等)"`
	StartedAt   *time.Time `json:"startedAt" gorm:"column:started_at;type:datetime;default:null;comment:开始处理时间"`
	CompletedAt *time.Time `json:"completedAt" gorm:"column:completed_at;type:datetime;default:null;comment:完成时间"`
	RecordID    uint       `json:"recordId" gorm:"column:record_id;type:int;default:0;index:idx_record_id;comment:关联的生成记录ID"`
	// HeartbeatAt is the last time the worker stamped "I'm still alive"
	// (SD-S1.3). The dead-worker sweeper finds rows in pending/processing
	// state whose heartbeat is older than the threshold and marks them
	// failed. The composite (status, heartbeat_at) index that supports
	// the sweeper's WHERE clause is declared in the migration; the model
	// just owns the column.
	HeartbeatAt *time.Time `json:"heartbeatAt" gorm:"column:heartbeat_at;type:datetime;default:null;comment:worker心跳;sweeper用于卡死任务回收"`
}

// TableName 指定表名
func (GenerationTask) TableName() string {
	return "w_generation_task"
}

// TaskStatus 任务状态类型
type TaskStatus int8

const (
	TaskStatusPending    TaskStatus = 0 // 等待处理
	TaskStatusProcessing TaskStatus = 1 // 处理中
	TaskStatusCompleted  TaskStatus = 2 // 已完成
	TaskStatusFailed     TaskStatus = 3 // 失败
	TaskStatusCancelled  TaskStatus = 4 // 已取消
)

// String 返回状态的字符串表示
func (s TaskStatus) String() string {
	switch s {
	case TaskStatusPending:
		return "pending"
	case TaskStatusProcessing:
		return "processing"
	case TaskStatusCompleted:
		return "completed"
	case TaskStatusFailed:
		return "failed"
	case TaskStatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// JSONMap 用于存储 JSON 数据的 map 类型
type JSONMap map[string]interface{}

// GormDataType tells GORM's schema parser the column type when JSONMap
// appears as a field on an ad-hoc struct (e.g. anonymous Find target)
// without an explicit `gorm:"type:..."` tag. Without this, GORM logs
// "unsupported data type" for every such query even though Scan still works.
func (JSONMap) GormDataType() string { return "json" }

// Value 实现 driver.Valuer 接口，用于写入数据库
func (j JSONMap) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Scan 实现 sql.Scanner 接口，用于从数据库读取
func (j *JSONMap) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return nil
	}
	if len(bytes) == 0 {
		*j = nil
		return nil
	}
	if err := json.Unmarshal(bytes, j); err == nil {
		return nil
	}

	var wrapped string
	if err := json.Unmarshal(bytes, &wrapped); err != nil {
		*j = nil
		return nil
	}
	if strings.TrimSpace(wrapped) == "" {
		*j = nil
		return nil
	}
	if err := json.Unmarshal([]byte(wrapped), j); err != nil {
		*j = nil
		return nil
	}
	return nil
}

// TaskRequestData 任务请求数据结构
type TaskRequestData struct {
	Model             string                `json:"model"`
	Prompt            string                `json:"prompt"`
	NegativePrompt    string                `json:"negativePrompt,omitempty"`
	AspectRatio       string                `json:"aspectRatio,omitempty"`
	StylePreset       string                `json:"stylePreset,omitempty"`
	Resolution        string                `json:"resolution,omitempty"`
	MediaType         string                `json:"mediaType,omitempty"`
	Duration          string                `json:"duration,omitempty"`
	NumberOfImages    int                   `json:"numberOfImages,omitempty"`
	Steps             int                   `json:"steps,omitempty"`
	CFGScale          float64               `json:"cfgScale,omitempty"`
	Sampler           string                `json:"sampler,omitempty"`
	Seed              int64                 `json:"seed,omitempty"`
	Upscale           bool                  `json:"upscale,omitempty"`
	StartFrame        string                `json:"startFrame,omitempty"`
	EndFrame          string                `json:"endFrame,omitempty"`
	GenerationMethod  string                `json:"generationMethod,omitempty"`
	MotionMode        string                `json:"motionMode,omitempty"`
	MotionOrientation string                `json:"motionOrientation,omitempty"`
	ReferenceImages   []ReferenceImageParam `json:"referenceImages,omitempty"`
	ReferenceVideos   []ReferenceMediaParam `json:"referenceVideos,omitempty"`
	ReferenceAudios   []ReferenceMediaParam `json:"referenceAudios,omitempty"`
	SkipRecord        bool                  `json:"skipRecord,omitempty"`
	SplitBatchID      string                `json:"splitBatchId,omitempty"`
	SplitBatchSize    int                   `json:"splitBatchSize,omitempty"`
	SplitBatchIndex   int                   `json:"splitBatchIndex,omitempty"`
	// LineageParentRecordID is the upstream w_generation_record.id this
	// submission is continuing from (video generator's "continue from
	// end frame" flow). NOT the same as canvas's internal
	// parentRecordId aggregation field — separate key, separate scope.
	LineageParentRecordID *uint `json:"lineageParentRecordId,omitempty"`
	// AssetBindings carries canvas §8.4 character/brand/product IDs
	// through the task queue so the generator can merge prompt suffixes
	// and reference images before calling the provider. Nil for
	// non-canvas callers.
	AssetBindings *AssetBinding `json:"assetBindings,omitempty"`
	// Origin discriminates which product surface submitted this task —
	// survives the queue round-trip so the finalized GenerationRecord
	// can be stamped correctly. See canvas-tool-design.md §8.8.
	Origin string                 `json:"origin,omitempty"`
	Extra  map[string]interface{} `json:"extra,omitempty"`
}

// TaskResultData 任务结果数据结构
type TaskResultData struct {
	Success          bool     `json:"success"`
	ImageURLs        []string `json:"imageUrls,omitempty"`
	VideoURLs        []string `json:"videoUrls,omitempty"`
	ThumbnailURL     string   `json:"thumbnailUrl,omitempty"`
	ProviderJobID    string   `json:"providerJobId,omitempty"`
	TaskID           string   `json:"taskId,omitempty"`
	CreditsUsed      int      `json:"creditsUsed,omitempty"`
	CreditsRemaining int      `json:"creditsRemaining,omitempty"`
	Duration         int64    `json:"duration,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// IsFinal 是否是最终状态（已完成、失败或取消）
func (t *GenerationTask) IsFinal() bool {
	return t.Status == TaskStatusCompleted || t.Status == TaskStatusFailed || t.Status == TaskStatusCancelled
}

// ShouldRetry 是否应该重试
func (t *GenerationTask) ShouldRetry() bool {
	// 只重试失败状态的任务，不包括取消的任务
	return t.Status == TaskStatusFailed
}

// ElapsedTime 返回已用时间（毫秒）
func (t *GenerationTask) ElapsedTime() int64 {
	if t.StartedAt == nil {
		return 0
	}
	end := t.CompletedAt
	if end == nil {
		end = &[]time.Time{time.Now()}[0]
	}
	return end.Sub(*t.StartedAt).Milliseconds()
}
