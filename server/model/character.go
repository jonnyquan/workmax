package model

import (
	"server/globals"
	"time"
)

// Character is the cross-cutting character asset consumed by the canvas
// @-mention pipeline.
// Originally created for short-drama; the verticals were retired in
// 2026-05-07 and the table was renamed from w_drama_character (legacy) to
// w_global_character (Sprint-E 20260611) as the actual scope.
//
// i18n 模型: 系统行 (uid=0 + project_id IS NULL) 按
// (slug, lang) 多行 fan-out;
// 用户行/项目行 lang 仅作授权时的 active locale 元数据,不参与可见性
// 过滤.
type Character struct {
	globals.GraMODEL
	DeletedAt      *time.Time `json:"-" gorm:"column:deleted_at;index"`
	UID            int        `json:"uid" gorm:"column:uid;not null;default:0;index:idx_uid_status,priority:1;comment:所属用户"`
	ProjectID      *uint64    `json:"projectId" gorm:"column:project_id;type:bigint unsigned;default:null;index:idx_project;comment:项目归属,NULL表示全局角色"`
	Name           string     `json:"name" gorm:"column:name;type:varchar(120);not null;default:'';comment:角色名"`
	Slug           string     `json:"slug" gorm:"column:slug;type:varchar(160);not null;default:'';comment:短链"`
	Lang           string     `json:"lang" gorm:"column:lang;type:varchar(16);not null;default:'en';index:idx_lang_slug,priority:1;comment:语言代码"`
	AvatarImageURL string     `json:"avatarImageUrl" gorm:"column:avatar_image_url;type:varchar(2048);not null;default:'';comment:头像URL"`
	RoleType       string     `json:"roleType" gorm:"column:role_type;type:varchar(32);not null;default:'supporting';comment:角色定位 protagonist/supporting/extra"`
	Gender         string     `json:"gender" gorm:"column:gender;type:varchar(32);not null;default:'';comment:性别"`
	AgeRange       string     `json:"ageRange" gorm:"column:age_range;type:varchar(32);not null;default:'';comment:年龄段"`
	Appearance     string     `json:"appearance" gorm:"column:appearance;type:text;comment:外观描述"`
	Personality    string     `json:"personality" gorm:"column:personality;type:text;comment:性格描述"`
	VisualDNAJSON  JSONMap    `json:"visualDna" gorm:"column:visual_dna_json;type:json;comment:结构化视觉特征"`
	PromptSuffix   string     `json:"promptSuffix" gorm:"column:prompt_suffix;type:text;comment:生成时自动附加的prompt"`
	NegativePrompt string     `json:"negativePrompt" gorm:"column:negative_prompt;type:text;comment:默认负向prompt (自由文本)"`
	// IdentityAnchors is the 6-layer structured character consistency
	// anchor (bone structure / features / unique marks / color / skin /
	// hair). Populated by the AI calibrator from script analysis.
	// Consumed by image+video prompt assembly to hold cross-shot visual
	// consistency.
	IdentityAnchors JSONMap `json:"identityAnchors" gorm:"column:identity_anchors;type:json;comment:6层身份锚点"`
	// NegativeAnchors parallels the above: structured { avoid[],
	// styleExclusions[] } from the calibrator, injected as negative
	// prompts on every generation for this character.
	NegativeAnchors JSONMap `json:"negativeAnchors" gorm:"column:negative_anchors;type:json;comment:结构化负面提示词"`
	// AnchorsVersion is the monotonic counter that lets downstream systems
	// detect stale renders. Bumped by BumpCharacterAnchorsVersion whenever
	// identity_anchors / negative_anchors / appearance changes. Defaults to
	// 1 so legacy rows behave like a freshly-calibrated character.
	AnchorsVersion int `json:"anchorsVersion" gorm:"column:anchors_version;type:int;not null;default:1;comment:锚点版本号"`
	// AppearanceHash is T4's facet-aware staleness signal: a 16-hex-
	// char digest over the visually-affecting subset (Appearance +
	// IdentityAnchors + NegativeAnchors). Identity-only edits (e.g.
	// Personality, RoleType) leave the hash unchanged — so the
	// stale-shot detector can stop flagging visually-correct panels
	// every time a non-visual field gets a tweak. Empty string for
	// pre-T4 rows; the detector falls back to AnchorsVersion in
	// that case to preserve legacy behaviour.
	AppearanceHash string     `json:"appearanceHash" gorm:"column:appearance_hash;type:char(16);not null;default:'';comment:T4 视觉特征摘要"`
	CalibratedAt   *time.Time `json:"calibratedAt" gorm:"column:calibrated_at;type:datetime;default:null;comment:上次AI校准时间"`
	// VoicePreset is the TTS voice preset ID (e.g. "neutral", "female-warm").
	// Resolved to a per-provider voice at render time via
	// service/tts/voices.go#LookupProviderVoice. Nullable — unassigned
	// characters fall back to the project / caller default.
	VoicePreset string `json:"voicePreset" gorm:"column:voice_preset;type:varchar(64);default:null;comment:TTS声音预设ID"`
	// PreviousAvatarImageURL is the one-level undo slot — on a
	// successful AI regenerate the handler copies the old avatar URL
	// here before overwriting AvatarImageURL. A single "undo" button
	// swaps them back. Deliberately ONE level, not a version log, so
	// the schema cost stays at a single column.
	PreviousAvatarImageURL string  `json:"previousAvatarImageUrl" gorm:"column:previous_avatar_image_url;type:varchar(2048);not null;default:'';comment:上一次AI生成前的头像URL"`
	LoraModelID            *uint64 `json:"loraModelId" gorm:"column:lora_model_id;type:bigint unsigned;default:null;index:idx_lora_model;comment:关联LoRA模型"`
	SourceKind             string  `json:"sourceKind" gorm:"column:source_kind;type:varchar(32);not null;default:'manual';comment:来源 manual/comic_extracted/template/extracted/uploaded/imported"`
	Status                 int8    `json:"status" gorm:"column:status;type:tinyint;not null;default:1;index:idx_uid_status,priority:2;comment:状态"`
	// Confirmed + ConfirmedAt (Sprint-E platform-wide lifecycle).
	// Workagent's M4 character protocol writes Confirmed=false until
	// the user vocalizes the extraction; canvas-created characters
	// default Confirmed=true (no review step). Consumers that want
	// only fully-vetted characters filter on Confirmed=true.
	// Confirmed — see model.Brand.Confirmed for the GORM tag rationale.
	// Default DDL is 1 so canvas-created characters land as confirmed
	// without an explicit set; workagent extractions Select("confirmed")
	// to force-write false.
	Confirmed   bool       `json:"confirmed" gorm:"column:confirmed;type:tinyint(1);comment:用户已确认 (workagent M4 vocalize 后置位)"`
	ConfirmedAt *time.Time `json:"confirmedAt" gorm:"column:confirmed_at;type:datetime;default:null;comment:确认时间"`
	// SourceThreadID — when this character was extracted by a
	// workagent thread, this points back at it for traceability.
	// NULL for canvas / manual creations.
	SourceThreadID *uint64 `json:"sourceThreadId" gorm:"column:source_thread_id;type:bigint unsigned;default:null;comment:来源线程ID (workagent extraction)"`
}

func (Character) TableName() string {
	return "w_global_character"
}

// CharacterReference stores reference images used to maintain visual
// consistency across renders for the parent Character.
type CharacterReference struct {
	globals.GraMODEL
	DeletedAt     *time.Time `json:"-" gorm:"column:deleted_at;index"`
	CharacterID   uint64     `json:"characterId" gorm:"column:character_id;type:bigint unsigned;not null;index:idx_character;comment:角色ID"`
	UID           int        `json:"uid" gorm:"column:uid;not null;default:0;comment:所属用户"`
	ImageURL      string     `json:"imageUrl" gorm:"column:image_url;type:varchar(2048);not null;default:'';comment:图片URL"`
	ReferenceType string     `json:"referenceType" gorm:"column:reference_type;type:varchar(32);not null;default:'face';comment:参考图类型 face/body/outfit/expression/pose"`
	Label         string     `json:"label" gorm:"column:label;type:varchar(80);not null;default:'';comment:短标签"`
	SortOrder     int        `json:"sortOrder" gorm:"column:sort_order;type:int;not null;default:0;comment:排序"`
	Metadata      JSONMap    `json:"metadata" gorm:"column:metadata;type:json;comment:附加元数据,如boundingBox/confidence"`
}

func (CharacterReference) TableName() string {
	return "w_global_character_reference"
}

const (
	CharacterRoleProtagonist = "protagonist"
	CharacterRoleSupporting  = "supporting"
	CharacterRoleExtra       = "extra"

	CharacterSourceManual         = "manual"
	CharacterSourceComicExtracted = "comic_extracted"
	CharacterSourceTemplate       = "template"

	CharacterReferenceTypeFace    = "face"
	CharacterReferenceTypeBody    = "body"
	CharacterReferenceTypeOutfit  = "outfit"
	CharacterReferenceTypeExpress = "expression"
	CharacterReferenceTypePose    = "pose"

	CharacterStatusActive int8 = 1
)
