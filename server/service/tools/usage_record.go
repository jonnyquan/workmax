package tools

import (
	"server/model"
	"strings"

	"gorm.io/gorm"
)

type UsageRecordMeta struct {
	IP             string
	DeviceInfo     string
	ToolParams     interface{}
	InputFiles     interface{}
	ErrorMessage   string
	ResultMetadata interface{}
}

// FeatureTypeForToolID maps a raw tool identifier to the stable
// feature_type written into w_usage_record.
//
// Cases below collapse multiple historical aliases (e.g.
// "image-upscaler" / "upscaler" / TOOL_IMAGE_UPSCALER) into a single
// canonical feature_type. Unknown tool IDs pass through unchanged so
// new tools can ship without touching this map.
func FeatureTypeForToolID(toolID string) string {
	if strings.HasPrefix(toolID, model.TOOL_LORA_STUDIO+":") {
		return model.TOOL_LORA_STUDIO
	}

	switch toolID {
	case model.TOOL_AGENT, "canvas_agent":
		return model.FEATURE_TYPE_AI_AGENT
	case model.TOOL_IMAGE_GENERATOR, model.NANO_BANANA, model.NANO_BANANA_2, model.NANO_BANANA_PRO:
		return "image_generator"
	case model.TOOL_VIDEO_GENERATOR:
		return model.TOOL_VIDEO_GENERATOR
	case model.TOOL_LORA_STUDIO:
		return model.TOOL_LORA_STUDIO
	case model.TOOL_AVATAR_STUDIO, "avatar-studio", "avatars":
		return "avatar_studio"
	case model.TOOL_IMAGE_UPSCALER, "image-upscaler", "upscaler":
		return "image_upscaler"
	case model.TOOL_BACKGROUND_REMOVER, "background-remover", "remover":
		return "background_remover"
	case model.TOOL_IMAGE_VECTORIZER, "image-vectorizer", "vectorizer":
		return "image_vectorizer"
	case model.TOOL_PROMPT_BUILDER, "prompt-builder":
		return "prompt_builder"
	case model.TOOL_VIDEO_PROMPT_BUILDER, "video-prompt-builder":
		return "video_prompt_builder"
	case "canvas_chat":
		return "canvas_chat"
	case "canvas_optimize_prompt":
		return model.FEATURE_TYPE_PROMPT_OPTIMIZE
	default:
		return toolID
	}
}

func CreateUsageRecordTx(tx *gorm.DB, uid int, toolID string, recordID uint, creditsUsed int, status int8, durationSeconds int, meta *UsageRecordMeta) error {
	// usage_record 只记录成功消耗，失败/处理中不落库
	if status != model.STATUS_SUCCESS {
		return nil
	}

	record := &model.UsageRecord{
		UID:             uid,
		FeatureType:     FeatureTypeForToolID(toolID),
		RecordID:        recordID,
		CreditsUsed:     creditsUsed,
		DurationSeconds: durationSeconds,
		Status:          status,
	}
	if meta != nil {
		record.IP = meta.IP
		record.DeviceInfo = meta.DeviceInfo
	}
	return tx.Create(record).Error
}
