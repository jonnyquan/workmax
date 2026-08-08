package tools

import "time"

// Request/response DTOs and validation tables for the prompt-builder API.
// Pure data — no business logic. The api file owns handlers + helpers; the
// validate file owns normalization/sanitization that references these tables.

// --- Request bodies ---

type saveConfigRequest struct {
	Name       string  `json:"name" binding:"required"`
	Mode       string  `json:"mode" binding:"required"`
	BlocksJSON string  `json:"blocksJSON" binding:"required"`
	Thumbnail  *string `json:"thumbnail"`
	IsFavorite *bool   `json:"isFavorite"`
}

type enhanceRequest struct {
	Prompt   string `json:"prompt" binding:"required"`
	Negative string `json:"negative"`
	Mode     string `json:"mode"`
	Style    string `json:"style"`
}

type translatePromptStreamRequest struct {
	Prompt         string `json:"prompt" binding:"required"`
	TargetLanguage string `json:"targetLanguage,omitempty"`
}

type parseRequest struct {
	Text string `json:"text" binding:"required"`
}

type suggestRequest struct {
	BlockType        string                 `json:"blockType" binding:"required"`
	Field            string                 `json:"field" binding:"required"`
	Context          map[string]interface{} `json:"context"`
	Existing         []string               `json:"existing"`
	AllBlocksSummary string                 `json:"allBlocksSummary"`
}

// --- Response bodies ---

// promptBuilderConfigSummary omits the potentially large blocks_json column so
// list responses stay small. Callers that need the blocks call GetConfig.
type promptBuilderConfigSummary struct {
	ID         uint      `json:"id"`
	UID        uint      `json:"uid"`
	Name       string    `json:"name"`
	Mode       string    `json:"mode"`
	Thumbnail  string    `json:"thumbnail"`
	IsFavorite bool      `json:"isFavorite"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// --- Quotas and limits ---

const (
	maxConfigsPerUser                              = 50
	maxPromptBuilderImageSize                int64 = 10 * 1024 * 1024
	maxPromptBuilderMultipartOverheadBytes   int64 = 1 * 1024 * 1024
	maxPromptBuilderJSONBodyBytes            int64 = 512 * 1024
	maxPromptBuilderBlocksJSONSize                 = 200 * 1024
	maxPromptBuilderNameLength                     = 100
	maxPromptBuilderThumbnailLength                = 500
	maxPromptBuilderPromptTextBytes                = 20 * 1024
	maxPromptBuilderMetaTextBytes                  = 200
	maxPromptBuilderSuggestContextJSONBytes        = 12 * 1024
	maxPromptBuilderSuggestExistingTextBytes       = 256
	maxPromptBuilderAllBlocksSummaryBytes          = 2000
	promptBuilderSchemaVersion                     = 2
	promptBuilderListDefaultPageSize               = 50
	promptBuilderListMaxPageSize                   = 100
)

// --- Allowed-values tables ---

var promptBuilderAllowedImageTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/webp": {},
	"image/gif":  {},
}

var promptBuilderAllowedModes = map[string]struct{}{
	"portrait":    {},
	"product":     {},
	"grid":        {},
	"scene":       {},
	"instruct":    {},
	"infographic": {},
	"freeform":    {},
}

var promptBuilderAllowedBlockTypes = map[string]struct{}{
	"subject":        {},
	"attire":         {},
	"interaction":    {},
	"environment":    {},
	"lighting":       {},
	"camera":         {},
	"style":          {},
	"details":        {},
	"negative":       {},
	"meta":           {},
	"sensory":        {},
	"reference":      {},
	"instructions":   {},
	"constraints":    {},
	"text_overlay":   {},
	"grid_cell":      {},
	"product_module": {},
	"custom":         {},
}

var promptBuilderBlockFields = map[string][]string{
	"subject":        {"characters", "single_subject"},
	"attire":         {"clothing", "texture", "accessories", "makeup"},
	"interaction":    {"interactions", "description"},
	"environment":    {"setting_type", "setting", "atmosphere", "color_mood", "color_scheme", "psychological_colors"},
	"lighting":       {"type", "quality", "direction", "color_tone"},
	"camera":         {"shot_type", "lens", "aperture", "focus", "film_stock"},
	"style":          {"art_style", "realism_level", "detail_level", "mood", "emotion", "style_fusion", "style_fusion_blend", "artist_inspiration", "inspiration_mode"},
	"details":        {"micro_details", "textures", "physics", "invisible_verbs"},
	"negative":       {"avoid_tags", "custom_negative"},
	"meta":           {"aspect_ratio", "resolution", "model_type", "single_subject", "strict_adherence"},
	"sensory":        {"intensity", "tactile", "temperature", "motion", "sound", "olfactory"},
	"reference":      {"uploaded_images", "reference_images", "preservation_rules", "transformation", "source_material"},
	"instructions":   {"role", "task", "steps", "constraints"},
	"constraints":    {"forbidden", "consistency_rules", "strict_adherence"},
	"text_overlay":   {"font", "color", "position", "content"},
	"grid_cell":      {"cell_name", "shot_type", "action", "setting", "dialogue_or_caption"},
	"product_module": {"product", "orientation", "effects", "background"},
	"custom":         {"custom_name", "raw_text"},
}

type promptBuilderFieldKind string

const (
	promptBuilderFieldKindString      promptBuilderFieldKind = "string"
	promptBuilderFieldKindStringArray promptBuilderFieldKind = "string_array"
	promptBuilderFieldKindBoolean     promptBuilderFieldKind = "boolean"
	promptBuilderFieldKindNumber      promptBuilderFieldKind = "number"
	promptBuilderFieldKindObject      promptBuilderFieldKind = "object"
	promptBuilderFieldKindObjectArray promptBuilderFieldKind = "object_array"
)

var promptBuilderFieldKinds = map[string]promptBuilderFieldKind{
	"characters":           promptBuilderFieldKindObjectArray,
	"single_subject":       promptBuilderFieldKindBoolean,
	"clothing":             promptBuilderFieldKindStringArray,
	"texture":              promptBuilderFieldKindStringArray,
	"accessories":          promptBuilderFieldKindStringArray,
	"makeup":               promptBuilderFieldKindStringArray,
	"interactions":         promptBuilderFieldKindStringArray,
	"description":          promptBuilderFieldKindString,
	"setting_type":         promptBuilderFieldKindString,
	"setting":              promptBuilderFieldKindString,
	"atmosphere":           promptBuilderFieldKindStringArray,
	"color_mood":           promptBuilderFieldKindString,
	"color_scheme":         promptBuilderFieldKindStringArray,
	"psychological_colors": promptBuilderFieldKindStringArray,
	"type":                 promptBuilderFieldKindString,
	"quality":              promptBuilderFieldKindString,
	"direction":            promptBuilderFieldKindString,
	"color_tone":           promptBuilderFieldKindString,
	"shot_type":            promptBuilderFieldKindString,
	"lens":                 promptBuilderFieldKindString,
	"aperture":             promptBuilderFieldKindString,
	"focus":                promptBuilderFieldKindString,
	"film_stock":           promptBuilderFieldKindString,
	"art_style":            promptBuilderFieldKindStringArray,
	"realism_level":        promptBuilderFieldKindNumber,
	"detail_level":         promptBuilderFieldKindNumber,
	"mood":                 promptBuilderFieldKindString,
	"emotion":              promptBuilderFieldKindStringArray,
	"style_fusion":         promptBuilderFieldKindString,
	"style_fusion_blend":   promptBuilderFieldKindObject,
	"artist_inspiration":   promptBuilderFieldKindString,
	"inspiration_mode":     promptBuilderFieldKindString,
	"micro_details":        promptBuilderFieldKindStringArray,
	"textures":             promptBuilderFieldKindStringArray,
	"physics":              promptBuilderFieldKindStringArray,
	"invisible_verbs":      promptBuilderFieldKindObjectArray,
	"avoid_tags":           promptBuilderFieldKindStringArray,
	"custom_negative":      promptBuilderFieldKindString,
	"aspect_ratio":         promptBuilderFieldKindString,
	"resolution":           promptBuilderFieldKindString,
	"model_type":           promptBuilderFieldKindString,
	"strict_adherence":     promptBuilderFieldKindBoolean,
	"intensity":            promptBuilderFieldKindNumber,
	"tactile":              promptBuilderFieldKindStringArray,
	"temperature":          promptBuilderFieldKindStringArray,
	"motion":               promptBuilderFieldKindStringArray,
	"sound":                promptBuilderFieldKindStringArray,
	"olfactory":            promptBuilderFieldKindStringArray,
	"uploaded_images":      promptBuilderFieldKindObjectArray,
	"reference_images":     promptBuilderFieldKindStringArray,
	"preservation_rules":   promptBuilderFieldKindStringArray,
	"transformation":       promptBuilderFieldKindString,
	"source_material":      promptBuilderFieldKindString,
	"role":                 promptBuilderFieldKindString,
	"task":                 promptBuilderFieldKindString,
	"steps":                promptBuilderFieldKindStringArray,
	"constraints":          promptBuilderFieldKindStringArray,
	"forbidden":            promptBuilderFieldKindStringArray,
	"consistency_rules":    promptBuilderFieldKindStringArray,
	"font":                 promptBuilderFieldKindString,
	"color":                promptBuilderFieldKindString,
	"position":             promptBuilderFieldKindString,
	"content":              promptBuilderFieldKindString,
	"cell_name":            promptBuilderFieldKindString,
	"action":               promptBuilderFieldKindString,
	"dialogue_or_caption":  promptBuilderFieldKindString,
	"product":              promptBuilderFieldKindString,
	"orientation":          promptBuilderFieldKindString,
	"effects":              promptBuilderFieldKindString,
	"background":           promptBuilderFieldKindString,
	"custom_name":          promptBuilderFieldKindString,
	"raw_text":             promptBuilderFieldKindString,
	"weight":               promptBuilderFieldKindNumber,
}

var promptBuilderFieldAliases = map[string][]string{
	"setting_type":         {"settingType"},
	"color_mood":           {"colorMood"},
	"color_scheme":         {"colorScheme"},
	"psychological_colors": {"psychologicalColors"},
	"color_tone":           {"colorTone"},
	"shot_type":            {"shotType"},
	"film_stock":           {"filmStock"},
	"art_style":            {"artStyle"},
	"realism_level":        {"realismLevel"},
	"detail_level":         {"detailLevel"},
	"style_fusion":         {"styleFusion"},
	"style_fusion_blend":   {"styleFusionBlend"},
	"artist_inspiration":   {"artistInspiration"},
	"inspiration_mode":     {"inspirationMode"},
	"micro_details":        {"microDetails"},
	"avoid_tags":           {"avoidTags"},
	"custom_negative":      {"customNegative"},
	"single_subject":       {"singleSubject"},
	"strict_adherence":     {"strictAdherence"},
	"uploaded_images":      {"uploadedImages"},
	"dialogue_or_caption":  {"dialogueOrCaption"},
	"custom_name":          {"customName"},
	"raw_text":             {"rawText"},
}

var promptBuilderCharacterArrayFields = []string{
	"features",
	"skin",
	"eyes",
	"hair",
	"expression",
	"demographics",
	"clothing",
	"texture",
	"accessories",
	"makeup",
	"attire",
}
