package config

import (
	"fmt"
	"strings"
)

// Generator 图片生成器配置
type Generator struct {
	Models     map[string]ModelConfig `mapstructure:"models" json:"models" yaml:"models"`
	Storage    StorageConfig          `mapstructure:"storage" json:"storage" yaml:"storage"`
	RateLimit  RateLimitConfig        `mapstructure:"rate_limit" json:"rateLimit" yaml:"rate_limit"`
	TaskQueue  TaskQueueConfig        `mapstructure:"task_queue" json:"taskQueue" yaml:"task_queue"`
	FileUpload FileUploadConfig       `mapstructure:"file_upload" json:"fileUpload" yaml:"file_upload"`
}

// RateLimitConfig 速率限制配置
type RateLimitConfig struct {
	PerMinute         int `mapstructure:"per_minute" json:"perMinute" yaml:"per_minute"`                           // 每分钟请求数
	PerUserConcurrent int `mapstructure:"per_user_concurrent" json:"perUserConcurrent" yaml:"per_user_concurrent"` // 每用户并发数
	GlobalConcurrent  int `mapstructure:"global_concurrent" json:"globalConcurrent" yaml:"global_concurrent"`      // 全局并发数
	ActiveTaskLimit   int `mapstructure:"active_task_limit" json:"activeTaskLimit" yaml:"active_task_limit"`       // 最大活跃任务数
}

// TaskQueueConfig 任务队列配置
type TaskQueueConfig struct {
	Workers          int `mapstructure:"workers" json:"workers" yaml:"workers"`                              // 工作协程数
	MaxQueueSize     int `mapstructure:"max_queue_size" json:"maxQueueSize" yaml:"max_queue_size"`           // 最大队列长度
	TaskTimeout      int `mapstructure:"task_timeout" json:"taskTimeout" yaml:"task_timeout"`                // 任务超时时间(秒)
	MaxRetries       int `mapstructure:"max_retries" json:"maxRetries" yaml:"max_retries"`                   // 最大重试次数
	RecoverThreshold int `mapstructure:"recover_threshold" json:"recoverThreshold" yaml:"recover_threshold"` // 恢复任务时间窗口(分钟)
	RecoverLimit     int `mapstructure:"recover_limit" json:"recoverLimit" yaml:"recover_limit"`             // 恢复任务数量限制
	ExpireHours      int `mapstructure:"expire_hours" json:"expireHours" yaml:"expire_hours"`                // 任务过期时间(小时)
}

// FileUploadConfig 文件上传配置
type FileUploadConfig struct {
	MaxReferenceImageSize     int64    `mapstructure:"max_reference_image_size" json:"maxReferenceImageSize" yaml:"max_reference_image_size"`             // 参考图片最大大小(字节)
	MaxRemoteAssetSize        int64    `mapstructure:"max_remote_asset_size" json:"maxRemoteAssetSize" yaml:"max_remote_asset_size"`                      // 远程资产最大大小(字节)
	RemoteAssetTimeoutSeconds int      `mapstructure:"remote_asset_timeout_seconds" json:"remoteAssetTimeoutSeconds" yaml:"remote_asset_timeout_seconds"` // 远程资产下载超时(秒)
	AllowedRemoteAssetHosts   []string `mapstructure:"allowed_remote_asset_hosts" json:"allowedRemoteAssetHosts" yaml:"allowed_remote_asset_hosts"`       // 允许远程资产转存的主机名列表，空表示不限制
}

// ModelConfig 模型配置 (Provider 配置从数据库加载)
type ModelConfig struct {
	CreditCost int `mapstructure:"credit_cost" json:"creditCost" yaml:"credit_cost"`
}

// StorageConfig 存储配置
type StorageConfig struct {
	Type  string       `mapstructure:"type" json:"type" yaml:"type"` // local, r2
	Local LocalStorage `mapstructure:"local" json:"local" yaml:"local"`
	R2    R2Storage    `mapstructure:"r2" json:"r2" yaml:"r2"`
}

// LocalStorage 本地存储配置
type LocalStorage struct {
	Path      string `mapstructure:"path" json:"path" yaml:"path"`
	URLPrefix string `mapstructure:"url_prefix" json:"urlPrefix" yaml:"url_prefix"`
}

// R2Storage Cloudflare R2 存储配置（通过兼容对象存储 API 接入）
type R2Storage struct {
	Endpoint             string `mapstructure:"endpoint" json:"endpoint" yaml:"endpoint"`
	Region               string `mapstructure:"region" json:"region" yaml:"region"`
	Bucket               string `mapstructure:"bucket" json:"bucket" yaml:"bucket"`
	AccessKeyID          string `mapstructure:"access_key_id" json:"accessKeyId" yaml:"access_key_id"`
	SecretAccessKey      string `mapstructure:"secret_access_key" json:"secretAccessKey" yaml:"secret_access_key"`
	CDNUrl               string `mapstructure:"cdn_url" json:"cdnUrl" yaml:"cdn_url"`
	PublicURL            string `mapstructure:"public_url" json:"publicUrl" yaml:"public_url"`
	PathPrefix           string `mapstructure:"path_prefix" json:"pathPrefix" yaml:"path_prefix"`
	ForcePathStyle       bool   `mapstructure:"force_path_style" json:"forcePathStyle" yaml:"force_path_style"`
	UsePresignedURL      bool   `mapstructure:"use_presigned_url" json:"usePresignedUrl" yaml:"use_presigned_url"`
	PresignTTLSeconds    int    `mapstructure:"presign_ttl_seconds" json:"presignTtlSeconds" yaml:"presign_ttl_seconds"`
	MultipartThresholdMB int    `mapstructure:"multipart_threshold_mb" json:"multipartThresholdMb" yaml:"multipart_threshold_mb"`
	MultipartPartSizeMB  int    `mapstructure:"multipart_part_size_mb" json:"multipartPartSizeMb" yaml:"multipart_part_size_mb"`
}

func (g Generator) Validate() error {
	return g.Storage.Validate()
}

func (c StorageConfig) Validate() error {
	switch strings.TrimSpace(c.Type) {
	case "local":
		return nil
	case "r2":
		return c.R2.Validate()
	default:
		return fmt.Errorf("generator.storage.type must be one of: local, r2")
	}
}

func (c R2Storage) Validate() error {
	var missing []string
	if strings.TrimSpace(c.Endpoint) == "" {
		missing = append(missing, "generator.storage.r2.endpoint")
	}
	if strings.TrimSpace(c.Bucket) == "" {
		missing = append(missing, "generator.storage.r2.bucket")
	}
	if strings.TrimSpace(c.AccessKeyID) == "" {
		missing = append(missing, "generator.storage.r2.access_key_id")
	}
	if strings.TrimSpace(c.SecretAccessKey) == "" {
		missing = append(missing, "generator.storage.r2.secret_access_key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required r2 config: %s", strings.Join(missing, ", "))
	}
	if c.UsePresignedURL && c.PresignTTLSeconds <= 0 {
		return fmt.Errorf("generator.storage.r2.presign_ttl_seconds must be > 0 when use_presigned_url is enabled")
	}
	if c.MultipartThresholdMB > 0 && c.MultipartThresholdMB < 5 {
		return fmt.Errorf("generator.storage.r2.multipart_threshold_mb must be >= 5")
	}
	if c.MultipartPartSizeMB > 0 && c.MultipartPartSizeMB < 5 {
		return fmt.Errorf("generator.storage.r2.multipart_part_size_mb must be >= 5")
	}
	return nil
}
