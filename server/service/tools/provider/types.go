package provider

import (
	"context"
	"server/model"
	"strings"
)

// ImageProvider 图片生成提供商接口
type ImageProvider interface {
	// Generate 生成图片，返回生成结果
	Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error)
	// Name 返回提供商名称
	Name() string
	// Type 返回提供商类型
	Type() string
	// IsEnabled 是否启用
	IsEnabled() bool
}

// BackgroundRemoverProvider 背景移除提供商接口
type BackgroundRemoverProvider interface {
	// RemoveBackground 移除背景，返回结果图
	RemoveBackground(ctx context.Context, req *BackgroundRemovalRequest) (*BackgroundRemovalResult, error)
	// Name 返回提供商名称
	Name() string
	// Type 返回提供商类型
	Type() string
	// IsEnabled 是否启用
	IsEnabled() bool
}

// UpscalerProvider 图片放大提供商接口
type UpscalerProvider interface {
	// Upscale 放大图片，返回结果图
	Upscale(ctx context.Context, req *UpscaleRequest) (*UpscaleResult, error)
	// Name 返回提供商名称
	Name() string
	// Type 返回提供商类型
	Type() string
	// IsEnabled 是否启用
	IsEnabled() bool
}

// CancelableProvider 支持取消远端任务的提供商接口
type CancelableProvider interface {
	Cancel(ctx context.Context, jobID string) error
}

// ProviderConfig 从数据库加载的提供商配置
type ProviderConfig struct {
	ProviderID      uint
	Name            string
	Type            string
	MediaType       string
	Enabled         bool
	Endpoint        string
	APIKey          string
	Model           string
	DailyQuota      int
	MonthlyQuota    int
	ConcurrentLimit int
	ExtraConfig     *model.GeneratorProviderExtraConfig
}

type ReferenceImageData struct {
	ID       string  `json:"id,omitempty"`
	MimeType string  `json:"mimeType"`
	Data     string  `json:"data"`
	Weight   float32 `json:"weight,omitempty"`
	// URL is the original public source. Most providers ignore this and
	// consume Data (base64). Providers backed by APIs that accept image
	// URLs in JSON (e.g. gpt-image-2 via api.gptsapi.net's predictions
	// API) read URL directly so we don't redundantly download + re-upload
	// the image just to get a public URL back. Empty when the source URL
	// is not public-reachable (e.g. data: URLs).
	URL string `json:"url,omitempty"`
}

func IsFrameReferenceImageID(id string) bool {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "start-frame", "video-start-frame", "end-frame", "video-end-frame":
		return true
	default:
		return false
	}
}

// ReferenceMediaData 视频/音频参考素材
// URL 必填；Provider 按 URL 下发给外部 API（不走 base64）。
type ReferenceMediaData struct {
	URL      string `json:"url"`
	MimeType string `json:"mimeType,omitempty"`
}

// BackgroundRemovalRequest 背景移除请求
type BackgroundRemovalRequest struct {
	Model           string                                          `json:"model"`
	Prompt          string                                          `json:"prompt"`
	NegativePrompt  string                                          `json:"negativePrompt,omitempty"`
	AspectRatio     string                                          `json:"aspectRatio,omitempty"`
	Width           int                                             `json:"width"`
	Height          int                                             `json:"height"`
	SourceImageURL  string                                          `json:"sourceImageUrl,omitempty"`
	ReferenceImages []ReferenceImageData                            `json:"referenceImages,omitempty"`
	TaskID          string                                          `json:"-"`
	OnProgress      func(progress int, meta map[string]interface{}) `json:"-"`
	OnProviderJob   func(jobID string, meta map[string]interface{}) `json:"-"`
}

// UpscaleRequest 图片放大请求
type UpscaleRequest struct {
	Model           string                                          `json:"model"`
	Prompt          string                                          `json:"prompt"`
	NegativePrompt  string                                          `json:"negativePrompt,omitempty"`
	AspectRatio     string                                          `json:"aspectRatio,omitempty"`
	Width           int                                             `json:"width"`
	Height          int                                             `json:"height"`
	Scale           int                                             `json:"scale"`
	EnhanceFace     bool                                            `json:"enhanceFace,omitempty"`
	SourceImageURL  string                                          `json:"sourceImageUrl,omitempty"`
	ReferenceImages []ReferenceImageData                            `json:"referenceImages,omitempty"`
	TaskID          string                                          `json:"-"`
	OnProgress      func(progress int, meta map[string]interface{}) `json:"-"`
	OnProviderJob   func(jobID string, meta map[string]interface{}) `json:"-"`
}

// GenerationRequest 生成请求
type GenerationRequest struct {
	Model           string               `json:"model"`
	Prompt          string               `json:"prompt"`
	NegativePrompt  string               `json:"negativePrompt,omitempty"`
	Resolution      string               `json:"resolution,omitempty"`
	Width           int                  `json:"width"`
	Height          int                  `json:"height"`
	Steps           int                  `json:"steps,omitempty"`
	CFGScale        float64              `json:"cfgScale,omitempty"`
	Seed            int64                `json:"seed,omitempty"`
	StylePreset     string               `json:"stylePreset,omitempty"`
	Sampler         string               `json:"sampler,omitempty"`
	NumImages       int                  `json:"numImages,omitempty"`
	ReferenceImages []ReferenceImageData `json:"referenceImages,omitempty"`
	ReferenceVideos []ReferenceMediaData `json:"referenceVideos,omitempty"`
	ReferenceAudios []ReferenceMediaData `json:"referenceAudios,omitempty"`
	// Video generation specific fields
	Duration          string `json:"duration,omitempty"`          // e.g. "5s", "10s"
	StartFrame        string `json:"startFrame,omitempty"`        // URL to start frame
	EndFrame          string `json:"endFrame,omitempty"`          // URL to end frame
	GenerationMethod  string `json:"generationMethod,omitempty"`  // "frames" or "motion"
	MotionMode        string `json:"motionMode,omitempty"`        // "std" or "pro"
	MotionOrientation string `json:"motionOrientation,omitempty"` // "image" or "video"
	AspectRatio       string `json:"aspectRatio,omitempty"`       // requested aspect ratio hint (e.g. "16:9")
	GenerateAudio     *bool  `json:"generateAudio,omitempty"`     // opt-in/opt-out for providers that support native audio
	// GPT Image 2 specific knobs. Other providers ignore them; gpt_image_2.go
	// forwards them to OpenAI's Images API as quality / output_format /
	// output_compression respectively.
	Quality           string                                          `json:"quality,omitempty"`           // low / medium / high / auto
	OutputFormat      string                                          `json:"outputFormat,omitempty"`      // png / jpeg / webp
	OutputCompression int                                             `json:"outputCompression,omitempty"` // 0-100, only meaningful with jpeg/webp
	TaskID            string                                          `json:"-"`
	OnProgress        func(progress int, meta map[string]interface{}) `json:"-"`
	OnProviderJob     func(jobID string, meta map[string]interface{}) `json:"-"`
}

// GenerationResult 生成结果
type GenerationResult struct {
	Success            bool     `json:"success"`
	ImageURLs          []string `json:"imageUrls,omitempty"`
	VideoURLs          []string `json:"videoUrls,omitempty"`
	VideoContentTypes  []string `json:"videoContentTypes,omitempty"`
	ThumbnailURL       string   `json:"thumbnailUrl,omitempty"`
	ProviderJobID      string   `json:"providerJobId,omitempty"`
	DownloadVideos     bool     `json:"downloadVideos,omitempty"`
	DownloadThumbnails bool     `json:"downloadThumbnails,omitempty"`
	ImageData          [][]byte `json:"imageData,omitempty"`
	VideoData          [][]byte `json:"videoData,omitempty"`
	Seed               int64    `json:"seed,omitempty"`
	Error              string   `json:"error,omitempty"`
	Duration           int64    `json:"duration,omitempty"`
	Provider           string   `json:"provider"`
}

// BackgroundRemovalResult 背景移除结果
type BackgroundRemovalResult struct {
	Success       bool     `json:"success"`
	ImageURLs     []string `json:"imageUrls,omitempty"`
	ImageData     [][]byte `json:"imageData,omitempty"`
	ProviderJobID string   `json:"providerJobId,omitempty"`
	Error         string   `json:"error,omitempty"`
	Duration      int64    `json:"duration,omitempty"`
	Provider      string   `json:"provider"`
}

// UpscaleResult 图片放大结果
type UpscaleResult struct {
	Success       bool     `json:"success"`
	ImageURLs     []string `json:"imageUrls,omitempty"`
	ImageData     [][]byte `json:"imageData,omitempty"`
	ProviderJobID string   `json:"providerJobId,omitempty"`
	Error         string   `json:"error,omitempty"`
	Duration      int64    `json:"duration,omitempty"`
	Provider      string   `json:"provider"`
}

// AspectRatioToDimensions 将宽高比转换为实际尺寸
func AspectRatioToDimensions(aspectRatio string) (width, height int) {
	switch aspectRatio {
	case "auto":
		return 1024, 1024
	case "1:1":
		return 1024, 1024
	case "16:9":
		return 1344, 768
	case "9:16":
		return 768, 1344
	case "3:2":
		return 1152, 768
	case "2:3":
		return 768, 1152
	case "4:5":
		return 1024, 1280
	case "5:4":
		return 1280, 1024
	case "4:3":
		return 1152, 896
	case "3:4":
		return 896, 1152
	case "21:9":
		return 1536, 640
	case "9:21":
		return 640, 1536
	default:
		return 1024, 1024
	}
}

// StylePresetToPromptSuffix 将风格预设转换为提示词后缀
func StylePresetToPromptSuffix(stylePreset string) string {
	switch stylePreset {
	case "anime":
		return ", anime style, japanese animation, vibrant colors"
	case "realistic":
		return ", photorealistic, highly detailed, 8k uhd"
	case "artistic":
		return ", artistic style, expressive, creative interpretation"
	case "photo":
		return ", professional photography, high quality photo"
	case "cinematic":
		return ", cinematic lighting, movie scene, dramatic"
	case "fantasy":
		return ", fantasy art, magical, ethereal atmosphere"
	case "cyberpunk":
		return ", cyberpunk style, neon lights, futuristic city"
	case "watercolor":
		return ", watercolor painting, soft colors, artistic"
	case "oil-painting":
		return ", oil painting style, classical art, textured brushstrokes"
	case "3d-render":
		return ", 3d render, octane render, highly detailed cgi"
	default:
		return ""
	}
}
