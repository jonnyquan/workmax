package model

import (
	"server/globals"
	"time"
)

// Brand is the cross-cutting brand asset — the platform-level analog
// of model.Character. Sprint-E (2026-05-11) promoted this from the
// workagent-internal w_workagent_brand_asset table to a first-class
// platform asset, so canvas tools, video-ad surfaces, and any future
// generator can read brand identity without going through workagent.
//
// Schema parity with Character (intentional — drives the
// asset_library.Descriptor abstraction):
//   - uid + project_id + lang scoping (i18n fan-out for system rows)
//   - soft-delete via DeletedAt
//   - Status int8 + Confirmed/ConfirmedAt lifecycle
//   - 6-layer IdentityAnchors + NegativeAnchors for staleness machinery
//   - AnchorsVersion + AppearanceHash facet-aware staleness signal
//   - SourceKind / SourceURL / SourceThreadID for traceability
//   - PromptSuffix / NegativePrompt for downstream prompt assembly
//
// Brand-specific fields are the seven M4 protocol JSON sections
// (colors / typography / spacing / layout / components / motion /
// voice). The composer's <brand-spec> XML drains these.
//
// i18n model: system rows (uid=0 + project_id IS NULL) fan out by
// (slug, lang); user /
// project rows carry lang as metadata only.
type Brand struct {
	globals.GraMODEL

	DeletedAt *time.Time `json:"-" gorm:"column:deleted_at;index"`

	UID       int     `json:"uid" gorm:"column:uid;not null;default:0;index:idx_brand_uid_status,priority:1;comment:所属用户"`
	ProjectID *uint64 `json:"projectId" gorm:"column:project_id;type:bigint unsigned;default:null;index:idx_brand_project;comment:项目归属,NULL=全局"`
	TeamID    *uint64 `json:"teamId" gorm:"column:team_id;type:bigint unsigned;default:null;index:idx_brand_team;comment:团队ID(预留)"`
	Lang      string  `json:"lang" gorm:"column:lang;type:varchar(16);not null;default:'en';index:idx_brand_lang_slug,priority:1;comment:语言代码"`

	// Identity.
	Name string `json:"name" gorm:"column:name;type:varchar(120);not null;default:'';comment:品牌名"`
	Slug string `json:"slug" gorm:"column:slug;type:varchar(160);not null;default:'';index:idx_brand_lang_slug,priority:2;comment:slug"`

	// M4 brand-spec sections — schema-flexible JSON. The composer's
	// <brand-spec> XML walks each populated section into a YAML-ish
	// line. Empty / null / "{}" sections drop cleanly so partial
	// extractions don't pollute the prompt.
	Colors     JSONMap `json:"colors" gorm:"column:colors;type:json;comment:M4颜色tokens"`
	Typography JSONMap `json:"typography" gorm:"column:typography;type:json;comment:M4排版tokens"`
	Spacing    JSONMap `json:"spacing" gorm:"column:spacing;type:json;comment:M4间距tokens"`
	Layout     JSONMap `json:"layout" gorm:"column:layout;type:json;comment:M4布局tokens"`
	Components JSONMap `json:"components" gorm:"column:components;type:json;comment:M4组件tokens"`
	Motion     JSONMap `json:"motion" gorm:"column:motion;type:json;comment:M4动效tokens"`
	Voice      JSONMap `json:"voice" gorm:"column:voice;type:json;comment:M4语调tokens"`

	// Staleness machinery (parity with Character).
	//
	// IdentityAnchors is the structured 6-layer brand consistency
	// anchor (palette / type / spacing / layout / motion / voice).
	// Populated by the AI calibrator from extraction or manual
	// edit. Consumed by image / video prompt assembly to hold
	// cross-render brand consistency.
	IdentityAnchors JSONMap `json:"identityAnchors" gorm:"column:identity_anchors;type:json;comment:6层身份锚点"`
	NegativeAnchors JSONMap `json:"negativeAnchors" gorm:"column:negative_anchors;type:json;comment:结构化负面锚点 {avoid[],styleExclusions[]}"`
	AnchorsVersion  int     `json:"anchorsVersion" gorm:"column:anchors_version;type:int;not null;default:1;comment:锚点版本号 (staleness signal)"`
	// AppearanceHash — 16-hex-char digest over visually-affecting
	// fields (colors + typography + identity_anchors + negative_anchors).
	// Identity-only edits (e.g. name / slug) leave the hash unchanged
	// so the stale-render detector doesn't flag visually-correct
	// renders on every metadata tweak.
	AppearanceHash string     `json:"appearanceHash" gorm:"column:appearance_hash;type:char(16);not null;default:'';comment:T4 视觉特征摘要"`
	CalibratedAt   *time.Time `json:"calibratedAt" gorm:"column:calibrated_at;type:datetime;default:null;comment:上次AI校准时间"`

	// Prompt scaffolding.
	PromptSuffix   string `json:"promptSuffix" gorm:"column:prompt_suffix;type:text;comment:生成时自动附加的prompt"`
	NegativePrompt string `json:"negativePrompt" gorm:"column:negative_prompt;type:text;comment:默认负向prompt"`

	// Source traceability.
	SourceKind     string  `json:"sourceKind" gorm:"column:source_kind;type:varchar(32);not null;default:'manual';comment:来源 manual/extracted/uploaded/imported/template"`
	SourceURL      string  `json:"sourceUrl" gorm:"column:source_url;type:varchar(2048);not null;default:'';comment:来源URL (品牌站点等)"`
	SourceThreadID *uint64 `json:"sourceThreadId" gorm:"column:source_thread_id;type:bigint unsigned;default:null;index:idx_brand_source_thread;comment:来源线程ID"`

	// RawSpecMD preserves the M4-protocol extractor's verbatim output
	// so a future schema migration can re-extract without re-running
	// the LLM extraction.
	RawSpecMD string `json:"rawSpecMd" gorm:"column:raw_spec_md;type:mediumtext;comment:M4原始品牌规范"`

	// Lifecycle.
	Status int8 `json:"status" gorm:"column:status;type:tinyint;not null;default:1;index:idx_brand_uid_status,priority:2;comment:状态 (1=active)"`
	// Confirmed — no `default:1` GORM tag because GORM skips bool
	// zero-values during INSERT (which would let the DB default
	// override an explicit false). The DDL still carries DEFAULT 1
	// so canvas-created brands that don't explicitly set this
	// column still come up as confirmed=1 (no Vocalize step
	// required for canvas flows). Workagent flows explicitly set
	// Confirmed before INSERT via the descriptor's Create path.
	Confirmed   bool       `json:"confirmed" gorm:"column:confirmed;type:tinyint(1);comment:用户已确认"`
	ConfirmedAt *time.Time `json:"confirmedAt" gorm:"column:confirmed_at;type:datetime;default:null;comment:确认时间"`
}

func (Brand) TableName() string {
	return "w_global_brand"
}

// IsActive — fully-vetted predicate matching workagent semantics:
// confirmed + active status + not soft-deleted. Used by preflight
// to inject only "ready" brands into the system prompt.
func (b *Brand) IsActive() bool {
	return b != nil && b.Confirmed && b.Status == BrandStatusActive && b.DeletedAt == nil
}

// BrandReference stores reference imagery for a Brand — logos,
// mood boards, screenshots, pattern swatches. Sibling of
// CharacterReference; same column shape, different ReferenceType
// taxonomy (logo / mood_board / screenshot / pattern).
type BrandReference struct {
	globals.GraMODEL

	DeletedAt     *time.Time `json:"-" gorm:"column:deleted_at;index"`
	BrandID       uint64     `json:"brandId" gorm:"column:brand_id;type:bigint unsigned;not null;index:idx_brand_ref;comment:品牌ID"`
	UID           int        `json:"uid" gorm:"column:uid;not null;default:0;comment:所属用户"`
	ImageURL      string     `json:"imageUrl" gorm:"column:image_url;type:varchar(2048);not null;default:'';comment:图片URL"`
	ReferenceType string     `json:"referenceType" gorm:"column:reference_type;type:varchar(32);not null;default:'mood_board';comment:类型 logo/mood_board/screenshot/pattern"`
	Label         string     `json:"label" gorm:"column:label;type:varchar(80);not null;default:'';comment:短标签"`
	SortOrder     int        `json:"sortOrder" gorm:"column:sort_order;type:int;not null;default:0;comment:排序"`
	Metadata      JSONMap    `json:"metadata" gorm:"column:metadata;type:json;comment:附加元数据"`
}

func (BrandReference) TableName() string {
	return "w_global_brand_reference"
}

const (
	// BrandSourceKind values — superset across canvas / workagent flows.
	BrandSourceManual    = "manual"
	BrandSourceExtracted = "extracted"
	BrandSourceUploaded  = "uploaded"
	BrandSourceImported  = "imported"
	BrandSourceTemplate  = "template"

	// BrandReferenceType values.
	BrandReferenceTypeLogo       = "logo"
	BrandReferenceTypeMoodBoard  = "mood_board"
	BrandReferenceTypeScreenshot = "screenshot"
	BrandReferenceTypePattern    = "pattern"

	// Status — int8 active flag matching Character.
	BrandStatusActive int8 = 1
)
