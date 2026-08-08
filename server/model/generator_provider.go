package model

import (
	"server/globals"
	"time"
)

// GeneratorProvider 生成提供商配置表
type GeneratorProvider struct {
	globals.GraMODEL
	Name      string `json:"name" gorm:"column:name;type:varchar(50);not null;uniqueIndex:idx_name;comment:提供商名称"`
	Type      string `json:"type" gorm:"column:type;type:varchar(30);not null;index:idx_type;comment:提供商类型:gemini,vertex,openai,kling,sora,seedance,minimax,replicate,stability,custom"`
	MediaType string `json:"mediaType" gorm:"column:media_type;type:varchar(20);not null;default:'image';index:idx_media_type;comment:媒体类型:image,video,audio,3d"`
	Enabled   bool   `json:"enabled" gorm:"column:enabled;type:tinyint(1);default:0;comment:是否启用"`
	IsDefault bool   `json:"isDefault" gorm:"column:is_default;type:tinyint(1);default:0;comment:是否默认提供商"`
	Priority  int    `json:"priority" gorm:"column:priority;type:int;default:0;comment:优先级(越大越优先)"`

	// 通用配置
	Endpoint string `json:"endpoint" gorm:"column:endpoint;type:varchar(255);default:null;comment:API端点"`
	APIKey   string `json:"apiKey" gorm:"column:api_key;type:varchar(500);default:null;comment:API密钥(加密存储)"`
	Model    string `json:"model" gorm:"column:model;type:varchar(100);default:null;comment:默认模型"`

	// 配额配置
	DailyQuota      int `json:"dailyQuota" gorm:"column:daily_quota;type:int;default:0;comment:每日配额(0=无限制)"`
	MonthlyQuota    int `json:"monthlyQuota" gorm:"column:monthly_quota;type:int;default:0;comment:每月配额(0=无限制)"`
	ConcurrentLimit int `json:"concurrentLimit" gorm:"column:concurrent_limit;type:int;default:5;comment:并发限制"`

	// 统计字段
	TotalRequests   int64      `json:"totalRequests" gorm:"column:total_requests;type:bigint;default:0;comment:总请求数"`
	SuccessRequests int64      `json:"successRequests" gorm:"column:success_requests;type:bigint;default:0;comment:成功请求数"`
	FailedRequests  int64      `json:"failedRequests" gorm:"column:failed_requests;type:bigint;default:0;comment:失败请求数"`
	LastUsedAt      *time.Time `json:"lastUsedAt" gorm:"column:last_used_at;type:datetime;default:null;comment:最后使用时间"`
	LastError       string     `json:"lastError" gorm:"column:last_error;type:varchar(500);default:null;comment:最后错误信息"`
	LastErrorAt     *time.Time `json:"lastErrorAt" gorm:"column:last_error_at;type:datetime;default:null;comment:最后错误时间"`

	// 今日/本月统计（用于配额检查）
	DailyUsed   int        `json:"dailyUsed" gorm:"column:daily_used;type:int;default:0;comment:今日已用"`
	MonthlyUsed int        `json:"monthlyUsed" gorm:"column:monthly_used;type:int;default:0;comment:本月已用"`
	ResetDate   *time.Time `json:"resetDate" gorm:"column:reset_date;type:date;default:null;comment:配额重置日期"`

	// 扩展配置 (JSON)
	ExtraConfig string `json:"extraConfig" gorm:"column:extra_config;type:json;default:null;comment:额外配置JSON"`

	// 描述
	Description string `json:"description" gorm:"column:description;type:varchar(500);default:null;comment:描述"`
}

func (GeneratorProvider) TableName() string {
	return "w_generator_provider"
}

// ProviderType 提供商类型常量
const (
	ProviderTypeCustom    = "custom"
	ProviderTypeGemini    = "gemini"
	ProviderTypeVertex    = "vertex"
	ProviderTypeReplicate = "replicate"
	ProviderTypeOpenAI    = "openai"
	ProviderTypeStability = "stability"
	ProviderTypeKling     = "kling"
	ProviderTypeSora      = "sora"
	ProviderTypeSeedance  = "seedance"
	ProviderTypeMinimax   = "minimax"
)

// MediaType 媒体类型常量
const (
	MediaTypeImage = "image"
	MediaTypeVideo = "video"
	MediaTypeAudio = "audio"
)

// GeneratorProviderExtraConfig 扩展配置结构
type GeneratorProviderExtraConfig struct {
	// 请求配置
	Timeout    int `json:"timeout,omitempty"`    // 超时时间(秒)
	RetryCount int `json:"retryCount,omitempty"` // 重试次数

	// Replicate 配置
	WebhookURL string `json:"webhookUrl,omitempty"` // Webhook回调URL

	// 视频特有
	MaxDuration               int               `json:"maxDuration,omitempty"`               // 最大时长(秒)
	MaxFPS                    int               `json:"maxFps,omitempty"`                    // 最大帧率
	PollInterval              int               `json:"pollInterval,omitempty"`              // 轮询间隔(秒)
	MaxWaitSeconds            int               `json:"maxWaitSeconds,omitempty"`            // 最大等待时长(秒)
	SubmitPath                string            `json:"submitPath,omitempty"`                // 提交任务路径
	SubmitPayloadTemplate     string            `json:"submitPayloadTemplate,omitempty"`     // 提交请求体模板(JSON)
	StatusPath                string            `json:"statusPath,omitempty"`                // 查询状态路径，支持 {jobId}
	CancelPath                string            `json:"cancelPath,omitempty"`                // 取消任务路径，支持 {jobId}
	APIKeyHeader              string            `json:"apiKeyHeader,omitempty"`              // API Key header 名称，默认 Authorization
	APIKeyPrefix              string            `json:"apiKeyPrefix,omitempty"`              // API Key 前缀，默认 Bearer
	DownloadVideos            *bool             `json:"downloadVideos,omitempty"`            // 是否下载视频后再转存
	DownloadThumbnails        *bool             `json:"downloadThumbnails,omitempty"`        // 是否下载缩略图后再转存
	ResponseJobIDField        string            `json:"responseJobIdField,omitempty"`        // 任务ID字段路径
	ResponseStatusField       string            `json:"responseStatusField,omitempty"`       // 状态字段路径
	ResponseProgressField     string            `json:"responseProgressField,omitempty"`     // 进度字段路径
	ResponseErrorField        string            `json:"responseErrorField,omitempty"`        // 错误字段路径
	ResponseVideoURLsField    string            `json:"responseVideoUrlsField,omitempty"`    // 视频URL字段路径
	ResponseThumbnailField    string            `json:"responseThumbnailField,omitempty"`    // 缩略图字段路径
	ResponseVideoURLTemplate  string            `json:"responseVideoUrlTemplate,omitempty"`  // 视频结果值转URL模板，支持 {value}/{valueEscaped}
	ResponseThumbnailTemplate string            `json:"responseThumbnailTemplate,omitempty"` // 缩略图结果值转URL模板，支持 {value}/{valueEscaped}
	Headers                   map[string]string `json:"headers,omitempty"`                   // 额外请求头

	// Vertex AI 配置。优先使用 vertexCredentialsJson；为空时使用
	// GOOGLE_APPLICATION_CREDENTIALS/ADC；Gemini image 也支持 APIKey。
	VertexProjectID       string `json:"vertexProjectId,omitempty"`
	VertexLocation        string `json:"vertexLocation,omitempty"`
	VertexCredentialsJSON string `json:"vertexCredentialsJson,omitempty"`
	VertexStorageURI      string `json:"vertexStorageUri,omitempty"`
	OutputMIMEType        string `json:"outputMimeType,omitempty"`
	PersonGeneration      string `json:"personGeneration,omitempty"`
	SafetySetting         string `json:"safetySetting,omitempty"`
	AddWatermark          *bool  `json:"addWatermark,omitempty"`
	SampleCount           int    `json:"sampleCount,omitempty"`

	// 音频特有
	MaxAudioLength int    `json:"maxAudioLength,omitempty"` // 最大音频时长(秒)
	AudioFormat    string `json:"audioFormat,omitempty"`    // 音频格式

	// 费用配置
	CostPerRequest float64 `json:"costPerRequest,omitempty"` // 每次请求费用
	CostPerToken   float64 `json:"costPerToken,omitempty"`   // 每Token费用
}
