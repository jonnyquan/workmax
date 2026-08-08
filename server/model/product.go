package model

import (
	"server/globals"
	"time"
)

// Product is the cross-cutting product / SKU asset — the
// platform-level analog of model.Brand and model.Character.
// P1 #5 (2026-05-12) — fourth registered asset_library kind,
// promoted from "deferred" status the asset-library-platform
// design doc carried (§ "Adding a 4th asset kind").
//
// Schema parity with Brand / Character (drives the
// asset_library.Descriptor abstraction):
//   - uid + project_id + lang scoping (i18n fan-out for system
//     rows)
//   - soft-delete via DeletedAt
//   - Status int8 + Confirmed/ConfirmedAt lifecycle
//   - 6-layer IdentityAnchors + NegativeAnchors machinery
//   - AnchorsVersion + AppearanceHash staleness signal
//   - SourceKind / SourceURL / SourceThreadID traceability
//   - PromptSuffix / NegativePrompt prompt scaffolding
//
// Product-specific fields (the JSON sections that differ from
// Brand's M4 protocol):
//   - SKU + Category as flat identity fields (frequently queried)
//   - Description text (selling points, value props)
//   - Specs JSONMap (variants, dimensions, materials, weight)
//   - VisualGuidance JSONMap (photography style, angle hints,
//     lighting posture, surface placement)
//   - TargetAudience JSONMap (per-product, distinct from any
//     brand-wide audience descriptor)
//
// i18n model — same db-side-i18n-pattern.md contract as Brand:
// system rows (uid=0 + project_id IS NULL) fan out by (slug, lang);
// user / project rows carry lang as authoring-locale metadata.
type Product struct {
	globals.GraMODEL

	DeletedAt *time.Time `json:"-" gorm:"column:deleted_at;index"`

	UID       int     `json:"uid" gorm:"column:uid;not null;default:0;index:idx_product_uid_status,priority:1;comment:所属用户"`
	ProjectID *uint64 `json:"projectId" gorm:"column:project_id;type:bigint unsigned;default:null;index:idx_product_project;comment:项目归属,NULL=全局"`
	TeamID    *uint64 `json:"teamId" gorm:"column:team_id;type:bigint unsigned;default:null;index:idx_product_team;comment:团队ID(预留)"`
	Lang      string  `json:"lang" gorm:"column:lang;type:varchar(16);not null;default:'en';index:idx_product_lang_slug,priority:1;comment:语言代码"`

	// Identity.
	Name string `json:"name" gorm:"column:name;type:varchar(160);not null;default:'';comment:产品名"`
	Slug string `json:"slug" gorm:"column:slug;type:varchar(200);not null;default:'';index:idx_product_lang_slug,priority:2;comment:slug"`

	// Frequently-queried identity (vs the JSON blobs below).
	// SKU is the merchant-side identifier; Category groups
	// products in the picker UI. Both stay as flat columns so
	// the picker can filter without JSON-extract gymnastics.
	SKU      string `json:"sku" gorm:"column:sku;type:varchar(120);not null;default:'';index:idx_product_sku;comment:商品 SKU"`
	Category string `json:"category" gorm:"column:category;type:varchar(80);not null;default:'';index:idx_product_category;comment:产品分类"`

	// Description — free-form selling points / value props body.
	// Used by the composer's <product-context> XML block.
	Description string `json:"description" gorm:"column:description;type:text;comment:卖点 / 文案 body"`

	// Product-specific JSON sections (schema-flexible).
	//
	// Specs: variants / dimensions / materials / weight /
	// finish / etc. The composer renders into a YAML-ish line
	// per top-level key.
	//
	// VisualGuidance: photography direction (angle / lighting /
	// background / surface placement). Distinct from Brand's
	// visual identity because per-product guidance is much
	// tighter than per-brand: a brand might say "minimalist";
	// a product says "3/4 angle, soft-box top-left, gradient
	// backdrop matching SKU color".
	//
	// TargetAudience: per-product audience descriptor. The
	// brand may target gen-z broadly, but THIS product
	// (premium SKU) targets a narrower slice — keep both
	// signals available to the agent.
	Specs           JSONMap `json:"specs" gorm:"column:specs;type:json;comment:产品规格 (variants / dimensions / materials / weight)"`
	VisualGuidance  JSONMap `json:"visualGuidance" gorm:"column:visual_guidance;type:json;comment:摄影方向 (angle / lighting / backdrop)"`
	TargetAudience  JSONMap `json:"targetAudience" gorm:"column:target_audience;type:json;comment:目标受众 (per-product, 非品牌级)"`

	// Staleness machinery — parity with Brand / Character.
	IdentityAnchors JSONMap    `json:"identityAnchors" gorm:"column:identity_anchors;type:json;comment:身份锚点 (跨渲染一致性)"`
	NegativeAnchors JSONMap    `json:"negativeAnchors" gorm:"column:negative_anchors;type:json;comment:结构化负面锚点"`
	AnchorsVersion  int        `json:"anchorsVersion" gorm:"column:anchors_version;type:int;not null;default:1;comment:锚点版本 (staleness signal)"`
	AppearanceHash  string     `json:"appearanceHash" gorm:"column:appearance_hash;type:char(16);not null;default:'';comment:视觉特征摘要"`
	CalibratedAt    *time.Time `json:"calibratedAt" gorm:"column:calibrated_at;type:datetime;default:null;comment:上次AI校准时间"`

	// Prompt scaffolding.
	PromptSuffix   string `json:"promptSuffix" gorm:"column:prompt_suffix;type:text;comment:生成时自动附加的prompt"`
	NegativePrompt string `json:"negativePrompt" gorm:"column:negative_prompt;type:text;comment:默认负向prompt"`

	// Source traceability.
	SourceKind     string  `json:"sourceKind" gorm:"column:source_kind;type:varchar(32);not null;default:'manual';comment:来源 manual/extracted/uploaded/imported/template"`
	SourceURL      string  `json:"sourceUrl" gorm:"column:source_url;type:varchar(2048);not null;default:'';comment:来源URL (产品页等)"`
	SourceThreadID *uint64 `json:"sourceThreadId" gorm:"column:source_thread_id;type:bigint unsigned;default:null;index:idx_product_source_thread;comment:来源线程ID"`

	// RawSpecMD — preserves the verbatim extractor output (e.g.
	// scraped product page JSON-LD or M4 product-spec markdown)
	// so a future schema migration can re-extract without
	// re-running the LLM.
	RawSpecMD string `json:"rawSpecMd" gorm:"column:raw_spec_md;type:mediumtext;comment:原始产品规范"`

	// Lifecycle — same flat (Status int8 + Confirmed bool +
	// DeletedAt) tuple as Brand/Character. Canvas creates
	// default Confirmed=1; workagent extractor writes
	// Confirmed=0 via Select("*").Create() to survive GORM's
	// zero-value-skip.
	Status      int8       `json:"status" gorm:"column:status;type:tinyint;not null;default:1;index:idx_product_uid_status,priority:2;comment:状态 (1=active)"`
	Confirmed   bool       `json:"confirmed" gorm:"column:confirmed;type:tinyint(1);comment:用户已确认"`
	ConfirmedAt *time.Time `json:"confirmedAt" gorm:"column:confirmed_at;type:datetime;default:null;comment:确认时间"`
}

func (Product) TableName() string {
	return "w_global_product"
}

// IsActive — fully-vetted predicate matching the asset-library
// IsActive() shape: confirmed + active status + not soft-deleted.
// Used by preflight to inject only "ready" products into the
// system prompt.
func (p *Product) IsActive() bool {
	return p != nil && p.Confirmed && p.Status == ProductStatusActive && p.DeletedAt == nil
}

// ProductReference — sibling of BrandReference/CharacterReference.
// Stores reference imagery for a Product. Per-kind
// reference_type taxonomy lets the asset library UI render
// kind-specific affordances.
//
// Product reference types:
//   - product_shot  : main hero / catalogue shot
//   - lifestyle     : in-use scene (model holding, ambient context)
//   - detail        : close-up of texture / craft / finish
//   - packaging     : box / wrap / unboxing context
//   - swatch        : color / variant grid
type ProductReference struct {
	globals.GraMODEL

	DeletedAt     *time.Time `json:"-" gorm:"column:deleted_at;index"`
	ProductID     uint64     `json:"productId" gorm:"column:product_id;type:bigint unsigned;not null;index:idx_product_ref;comment:产品ID"`
	UID           int        `json:"uid" gorm:"column:uid;not null;default:0;comment:所属用户"`
	ImageURL      string     `json:"imageUrl" gorm:"column:image_url;type:varchar(2048);not null;default:'';comment:图片URL"`
	ReferenceType string     `json:"referenceType" gorm:"column:reference_type;type:varchar(32);not null;default:'product_shot';comment:类型 product_shot/lifestyle/detail/packaging/swatch"`
	Label         string     `json:"label" gorm:"column:label;type:varchar(80);not null;default:'';comment:短标签"`
	SortOrder     int        `json:"sortOrder" gorm:"column:sort_order;type:int;not null;default:0;comment:排序"`
	Metadata      JSONMap    `json:"metadata" gorm:"column:metadata;type:json;comment:附加元数据"`
}

func (ProductReference) TableName() string {
	return "w_global_product_reference"
}

const (
	// ProductSourceKind values — superset across canvas /
	// workagent flows. Same shape as BrandSourceKind so the
	// asset_library descriptor abstraction can project both
	// through the same SourceKind enum.
	ProductSourceManual    = "manual"
	ProductSourceExtracted = "extracted"
	ProductSourceUploaded  = "uploaded"
	ProductSourceImported  = "imported"
	ProductSourceTemplate  = "template"

	// ProductReferenceType values.
	ProductReferenceTypeProductShot = "product_shot"
	ProductReferenceTypeLifestyle   = "lifestyle"
	ProductReferenceTypeDetail      = "detail"
	ProductReferenceTypePackaging   = "packaging"
	ProductReferenceTypeSwatch      = "swatch"

	// Status — int8 active flag matching Brand/Character.
	ProductStatusActive int8 = 1
)
