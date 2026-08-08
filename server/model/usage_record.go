package model

import (
	"server/globals"
)

// UsageRecord Credits消耗记录表（轻量级）
// 职责：记录用户使用工具消耗的Credits，用于财务对账和限额控制
// 详细信息请查询 w_generation_record 表
type UsageRecord struct {
	globals.GraMODEL
	UID             int    `json:"uid" gorm:"column:uid;not null;comment:用户ID"`
	FeatureType     string `json:"featureType" gorm:"column:feature_type;type:varchar(50);not null;comment:功能类型(用于统计分类)"`
	RecordID        uint   `json:"recordId" gorm:"column:record_id;type:bigint unsigned;default:0;comment:关联生成记录ID"`
	TokensUsed      int    `json:"tokensUsed" gorm:"column:tokens_used;default:0;comment:使用的tokens"`
	CreditsUsed     int    `json:"creditsUsed" gorm:"column:credits_used;default:0;comment:消耗的Credits数"`
	DurationSeconds int    `json:"durationSeconds" gorm:"column:duration_seconds;default:0;comment:使用时长(秒)"`
	IP              string `json:"ip" gorm:"column:ip;type:varchar(50);default:null;comment:用户IP(风控)"`
	DeviceInfo      string `json:"deviceInfo" gorm:"column:device_info;type:varchar(255);default:null;comment:设备信息"`
	Status          int8   `json:"status" gorm:"column:status;type:tinyint;default:0;comment:状态:0=处理中 1=成功 2=失败"`
}

func (UsageRecord) TableName() string {
	return "w_usage_record"
}

// WorkMax Feature Types
const (
	// AI Image Generation Features - Core competency
	FEATURE_TYPE_IMAGE_GENERATION = "image_generation" // AI图片生成
	FEATURE_TYPE_IMAGE_EDIT       = "image_edit"       // 图片编辑
	FEATURE_TYPE_IMAGE_UPSCALE    = "image_upscale"    // 图片放大

	// Prompt Management Features
	FEATURE_TYPE_PROMPT_LIBRARY  = "prompt_library"  // Prompt库
	FEATURE_TYPE_PROMPT_OPTIMIZE = "prompt_optimize" // Prompt优化
	FEATURE_TYPE_PROMPT_TEMPLATE = "prompt_template" // Prompt模板

	// AI Agent & Assistance Features
	FEATURE_TYPE_AI_AGENT = "aiagent" // AI对话助手

	// Community & Sharing Features
	FEATURE_TYPE_COMMUNITY_SHARE   = "community_share"   // 社区分享
	FEATURE_TYPE_RESOURCE_DOWNLOAD = "resource_download" // 资源下载

	// Analytics & Insights
	FEATURE_TYPE_USAGE_ANALYTICS = "usage_analytics" // 使用分析
	FEATURE_TYPE_TREND_INSIGHTS  = "trend_insights"  // 趋势洞察
	FEATURE_TYPE_QUALITY_METRICS = "quality_metrics" // 质量指标
)

// Note: Tool ID constants are defined in generation_record.go to avoid duplication
// This includes TOOL_IMAGE_GENERATOR, TOOL_AVATAR_STUDIO,
// TOOL_IMAGE_UPSCALER, TOOL_BACKGROUND_REMOVER, TOOL_IMAGE_VECTORIZER

// Generation Status
const (
	STATUS_PROCESSING = 0 // 处理中
	STATUS_SUCCESS    = 1 // 成功
	STATUS_FAILED     = 2 // 失败
)
