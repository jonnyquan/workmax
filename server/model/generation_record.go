package model

import (
	"server/globals"
)

// GenerationRecord AI工具生成记录详细表
type GenerationRecord struct {
	globals.GraMODEL
	UID            int    `json:"uid" gorm:"column:uid;not null;index:idx_uid;comment:用户ID"`
	ToolID         string `json:"toolId" gorm:"column:tool_id;type:varchar(50);not null;index:idx_tool_id;comment:工具ID"`
	Model          string `json:"model" gorm:"column:model;type:varchar(50);default:null;index:idx_model;comment:使用模型"`
	Prompt         string `json:"prompt" gorm:"column:prompt;type:text;default:null;comment:提示词"`
	NegativePrompt string `json:"negativePrompt" gorm:"column:negative_prompt;type:text;default:null;comment:负面提示词"`
	StylePreset    string `json:"stylePreset" gorm:"column:style_preset;type:varchar(50);default:null;index:idx_style_preset;comment:风格预设"`
	AspectRatio    string `json:"aspectRatio" gorm:"column:aspect_ratio;type:varchar(20);default:null;comment:画面比例"`
	Params         string `json:"params" gorm:"column:params;type:json;default:null;comment:其他参数JSON"`
	InputFiles     string `json:"inputFiles" gorm:"column:input_files;type:json;default:null;comment:输入文件信息"`
	ResultImages   string `json:"resultImages" gorm:"column:result_images;type:json;default:null;comment:生成结果图片URLs数组"`
	ResultMetadata string `json:"resultMetadata" gorm:"column:result_metadata;type:json;default:null;comment:结果详细元数据"`
	Status         int8   `json:"status" gorm:"column:status;type:tinyint;default:0;index:idx_status;comment:状态:0=处理中 1=成功 2=失败"`
	DurationMs     int    `json:"durationMs" gorm:"column:duration_ms;type:int;default:0;comment:生成耗时(毫秒)"`
	TokensUsed     int    `json:"tokensUsed" gorm:"column:tokens_used;type:int;default:0;comment:消耗tokens"`
	CreditsUsed    int    `json:"creditsUsed" gorm:"column:credits_used;type:int;default:0;comment:消耗Credits数"`
	Liked          bool   `json:"liked" gorm:"column:liked;type:tinyint(1);default:0;comment:用户是否点赞"`
	Downloaded     bool   `json:"downloaded" gorm:"column:downloaded;type:tinyint(1);default:0;comment:是否下载过"`
	SourcePromptID int    `json:"sourcePromptId" gorm:"column:source_prompt_id;type:int;default:0;comment:来源Prompt库ID"`
	BatchID        string `json:"batchId" gorm:"column:batch_id;type:varchar(50);default:null;index:idx_batch_id;comment:批次ID"`
	// Origin discriminates which product surface produced the record —
	// see §8.8 canvas × video-generator 合流 in canvas-tool-design.md.
	// Empty = legacy rows predating the column; new writes always stamp.
	Origin string `json:"origin" gorm:"column:origin;type:varchar(32);default:null;index:idx_origin;comment:产品来源"`
	// ParentRecordID points at the upstream w_generation_record row that
	// seeded this generation via the video generator's "continue from
	// end frame" flow. NULL for organic submissions. The API validates
	// the parent belongs to the same uid before persisting; no DB FK
	// because soft-delete on the parent shouldn't break the chain.
	ParentRecordID *uint `json:"parentRecordId,omitempty" gorm:"column:parent_record_id;type:bigint unsigned;default:null;index:idx_parent_record_id;comment:续写源记录ID"`
}

func (GenerationRecord) TableName() string {
	return "w_generation_record"
}

// GenerationRecordParams 生成参数结构 (用于序列化到 Params 字段)
type GenerationRecordParams struct {
	// 通用参数
	Seed int `json:"seed,omitempty"`
	// 生成数量/分辨率
	NumberOfImages    int    `json:"numberOfImages,omitempty"`
	Resolution        string `json:"resolution,omitempty"`
	MediaType         string `json:"mediaType,omitempty"`
	Duration          string `json:"duration,omitempty"`
	StartFrame        string `json:"startFrame,omitempty"`
	EndFrame          string `json:"endFrame,omitempty"`
	GenerationMethod  string `json:"generationMethod,omitempty"`
	MotionMode        string `json:"motionMode,omitempty"`
	MotionOrientation string `json:"motionOrientation,omitempty"`

	// Pro 模型专属参数
	Steps     int     `json:"steps,omitempty"`
	CfgScale  float64 `json:"cfgScale,omitempty"`
	Sampler   string  `json:"sampler,omitempty"`
	Lora      string  `json:"lora,omitempty"`
	Upscale   bool    `json:"upscale,omitempty"`
	PromptRef string  `json:"promptRef,omitempty"` // 引用提示词ID

	// Upscaler 工具参数
	Scale       int8 `json:"scale,omitempty"`       // 2x 或 4x
	EnhanceFace bool `json:"enhanceFace,omitempty"` // 是否人脸增强

	// Vectorizer 工具参数
	Mode         string `json:"mode,omitempty"`         // color 或 black-white
	Colors       int    `json:"colors,omitempty"`       // 颜色数量 (2-32)
	DetailLevel  string `json:"detailLevel,omitempty"`  // low / medium / high
	ColorMode    string `json:"colorMode,omitempty"`    // color / monochrome
	HighFidelity bool   `json:"highFidelity,omitempty"` // 是否高保真矢量化

	// Avatar 工具参数
	ReferenceImages []ReferenceImageParam `json:"referenceImages,omitempty"`
}

// ReferenceImageParam 参考图片参数
type ReferenceImageParam struct {
	AssetID  uint    `json:"assetId,omitempty"`
	ID       string  `json:"id"`
	URL      string  `json:"url,omitempty"`
	Weight   float32 `json:"weight,omitempty"`
	FileSize int64   `json:"fileSize,omitempty"`
	FileName string  `json:"fileName,omitempty"`
	MimeType string  `json:"mimeType,omitempty"`
}

// ReferenceMediaParam 视频/音频参考素材参数
type ReferenceMediaParam struct {
	ID       string `json:"id"`
	URL      string `json:"url,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	FileSize int64  `json:"fileSize,omitempty"`
	FileName string `json:"fileName,omitempty"`
	Duration int    `json:"duration,omitempty"`
}

// GenerationResultMetadata 结果元数据结构
type GenerationResultMetadata struct {
	Width        int     `json:"width,omitempty"`
	Height       int     `json:"height,omitempty"`
	Format       string  `json:"format,omitempty"`       // png, jpg, webp
	FileSize     int64   `json:"fileSize,omitempty"`     // 字节
	Quality      float32 `json:"quality,omitempty"`      // 质量分数
	Seed         int     `json:"seed,omitempty"`         // 实际使用的seed
	ModelVersion string  `json:"modelVersion,omitempty"` // 模型版本
	Steps        int     `json:"steps,omitempty"`        // 实际步数
}

// InputFileInfo 输入文件信息
type InputFileInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	URL      string `json:"url,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// Origin 常量 — GenerationRecord.Origin 的取值域。对应 canvas-tool-design.md
// §8.8："canvas × video-generator 合流 … 记录都落 w_generation_record，
// origin 字段区分 canvas / video-gen"。Image-gen 作为第三个在用的产品面保留。
const (
	GENERATION_ORIGIN_CANVAS    = "canvas"
	GENERATION_ORIGIN_VIDEO_GEN = "video-gen"
	GENERATION_ORIGIN_IMAGE_GEN = "image-gen"
)

// ToolID 常量 (与 usage_record.go 保持一致)
const (
	// Image Generation Tools
	TOOL_IMAGE_GENERATOR    = "image_generator"    // 图片生成
	TOOL_VIDEO_GENERATOR    = "video_generator"    // 视频生成
	TOOL_LORA_STUDIO        = "lora_studio"        // Lora工作室（具体模板ID拼接在tool_id后）
	TOOL_AVATAR_STUDIO      = "avatar_studio"      // 头像生成
	TOOL_IMAGE_UPSCALER     = "image_upscaler"     // 图片放大
	TOOL_BACKGROUND_REMOVER = "background_remover" // 背景移除
	TOOL_IMAGE_VECTORIZER   = "image_vectorizer"   // 图片矢量化

	// AI-Assisted Tools
	TOOL_PROMPT_BUILDER       = "prompt_builder"       // Image Prompt Builder AI
	TOOL_VIDEO_PROMPT_BUILDER = "video_prompt_builder" // Video Prompt Builder AI

	// Experimental Tools (Coming Soon)
	TOOL_ANIMATOR      = "animator"      // 动画生成
	TOOL_TYPOGRAPHY    = "typography"    // 排版生成
	TOOL_PATTERN_MAKER = "pattern_maker" // 图案生成
)

// Model ID 常量
const (
	NANO_BANANA     = "nanobanana"
	NANO_BANANA_2   = "nanobanana-2"
	NANO_BANANA_PRO = "nanobanana-pro"
	GPT_IMAGE_2     = "gpt-image-2"

	// Video Models
	MINIMAX_VIDEO   = "video-01"
	VEO_31          = "veo-3.1"
	VEO_31_FAST     = "veo-3.1-fast"
	KLING_2_6       = "kling-2.6"
	SORA_2          = "sora-2"
	SEEDANCE        = "seedance"
	SEEDANCE_2      = "seedance-2"
	SEEDANCE_2_FAST = "seedance-2-fast"
)

// GetToolIDFromModel 根据模型ID获取工具ID
func GetToolIDFromModel(modelID string) string {
	switch modelID {
	case NANO_BANANA:
		return TOOL_IMAGE_GENERATOR
	case NANO_BANANA_2:
		return TOOL_IMAGE_GENERATOR
	case NANO_BANANA_PRO:
		return TOOL_IMAGE_GENERATOR
	case GPT_IMAGE_2:
		return TOOL_IMAGE_GENERATOR
	case MINIMAX_VIDEO, VEO_31, VEO_31_FAST, KLING_2_6, SORA_2, SEEDANCE, SEEDANCE_2, SEEDANCE_2_FAST:
		return TOOL_VIDEO_GENERATOR
	case "image_generator":
		return TOOL_IMAGE_GENERATOR
	case "video_generator", "video-generator", "video":
		return TOOL_VIDEO_GENERATOR
	case "lora_studio":
		return TOOL_LORA_STUDIO
	case "upscaler":
		return TOOL_IMAGE_UPSCALER
	case "image_upscaler":
		return TOOL_IMAGE_UPSCALER
	case "remover":
		return TOOL_BACKGROUND_REMOVER
	case "background_remover":
		return TOOL_BACKGROUND_REMOVER
	case "vectorizer":
		return TOOL_IMAGE_VECTORIZER
	case "image_vectorizer":
		return TOOL_IMAGE_VECTORIZER
	case "avatar_studio", "avatar-studio", "avatars":
		return TOOL_AVATAR_STUDIO
	default:
		return modelID
	}
}
