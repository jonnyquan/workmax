package model

import (
	"server/globals"
	"time"
)

// DirectorStyle captures a directorial / cinematic fingerprint a
// user wants to reuse across renders — the platform-level analog
// of Character + Brand. Sprint-E (2026-05-11) promoted this from
// the workagent-internal w_workagent_director_style_asset table
// to a first-class platform asset.
//
// Schema parity with Character + Brand (drives the
// asset_library.Descriptor abstraction): uid + project_id + lang
// scoping, soft-delete, Status int8, Confirmed/ConfirmedAt
// lifecycle, full anchors machinery, source traceability, prompt
// scaffolding.
//
// Director-style-specific: 5 cinematic axes (composition / color /
// lighting / motion / texture) — each in its own JSON column so
// preflight injectors can slice (a still-image generator might
// pull only color + lighting; a motion-graphics tool pulls all 5).
//
// Distinct from the visual_directions.yaml catalog (M2 fallback
// presets):
//   - yaml is global, 5 fallback options
//   - this table is per-uid (or system row), unbounded library
//   - the preflight read is choose-this-one, not pick-from-five
type DirectorStyle struct {
	globals.GraMODEL

	DeletedAt *time.Time `json:"-" gorm:"column:deleted_at;index"`

	UID       int     `json:"uid" gorm:"column:uid;not null;default:0;index:idx_director_uid_status,priority:1;comment:所属用户"`
	ProjectID *uint64 `json:"projectId" gorm:"column:project_id;type:bigint unsigned;default:null;index:idx_director_project;comment:项目归属,NULL=全局"`
	TeamID    *uint64 `json:"teamId" gorm:"column:team_id;type:bigint unsigned;default:null;index:idx_director_team;comment:团队ID(预留)"`
	Lang      string  `json:"lang" gorm:"column:lang;type:varchar(16);not null;default:'en';index:idx_director_lang_slug,priority:1;comment:语言代码"`

	// Identity. era / genre help library UI categorise without re-
	// parsing description (e.g. era="60s", genre="noir").
	Name  string `json:"name" gorm:"column:name;type:varchar(120);not null;default:'';comment:导演名/风格名"`
	Slug  string `json:"slug" gorm:"column:slug;type:varchar(160);not null;default:'';index:idx_director_lang_slug,priority:2;comment:slug"`
	Era   string `json:"era" gorm:"column:era;type:varchar(64);not null;default:'';comment:时代 (e.g. 60s/modern)"`
	Genre string `json:"genre" gorm:"column:genre;type:varchar(64);not null;default:'';index:idx_director_genre;comment:类型 (e.g. noir/comedy-drama)"`

	// 5 cinematic axes — each independent enough that preflight may
	// pull just one slice. Empty (nil/null/{}) when the source didn't
	// surface that axis; the composer drops empty fields cleanly.
	Composition JSONMap `json:"composition" gorm:"column:composition;type:json;comment:构图/几何/对称"`
	Color       JSONMap `json:"color" gorm:"column:color;type:json;comment:调色/grading"`
	Lighting    JSONMap `json:"lighting" gorm:"column:lighting;type:json;comment:光照"`
	Motion      JSONMap `json:"motion" gorm:"column:motion;type:json;comment:运镜/节奏"`
	Texture     JSONMap `json:"texture" gorm:"column:texture;type:json;comment:质感/grain/post"`

	// Staleness machinery (parity with Character + Brand).
	IdentityAnchors JSONMap    `json:"identityAnchors" gorm:"column:identity_anchors;type:json;comment:6层身份锚点"`
	NegativeAnchors JSONMap    `json:"negativeAnchors" gorm:"column:negative_anchors;type:json;comment:结构化负面锚点"`
	AnchorsVersion  int        `json:"anchorsVersion" gorm:"column:anchors_version;type:int;not null;default:1;comment:锚点版本号"`
	AppearanceHash  string     `json:"appearanceHash" gorm:"column:appearance_hash;type:char(16);not null;default:'';comment:T4 视觉特征摘要"`
	CalibratedAt    *time.Time `json:"calibratedAt" gorm:"column:calibrated_at;type:datetime;default:null;comment:上次AI校准时间"`

	// Prompt scaffolding.
	PromptSuffix   string `json:"promptSuffix" gorm:"column:prompt_suffix;type:text;comment:生成时自动附加的prompt"`
	NegativePrompt string `json:"negativePrompt" gorm:"column:negative_prompt;type:text;comment:默认负向prompt"`

	// Source traceability.
	SourceKind     string  `json:"sourceKind" gorm:"column:source_kind;type:varchar(32);not null;default:'manual';comment:来源 manual/extracted/uploaded/imported/template"`
	SourceURL      string  `json:"sourceUrl" gorm:"column:source_url;type:varchar(2048);not null;default:'';comment:来源URL (导演reel等)"`
	SourceThreadID *uint64 `json:"sourceThreadId" gorm:"column:source_thread_id;type:bigint unsigned;default:null;index:idx_director_source_thread;comment:来源线程ID"`

	RawSpecMD string `json:"rawSpecMd" gorm:"column:raw_spec_md;type:mediumtext;comment:M4等价 原始风格规范"`

	// Lifecycle.
	Status int8 `json:"status" gorm:"column:status;type:tinyint;not null;default:1;index:idx_director_uid_status,priority:2;comment:状态 (1=active)"`
	// Confirmed — see model.Brand.Confirmed for the GORM tag rationale.
	Confirmed   bool       `json:"confirmed" gorm:"column:confirmed;type:tinyint(1);comment:用户已确认"`
	ConfirmedAt *time.Time `json:"confirmedAt" gorm:"column:confirmed_at;type:datetime;default:null;comment:确认时间"`

	// HasReferencesCached — transient flag populated by the
	// director_style descriptor's List path via a bulk
	// HasReferencesByID lookup so descriptor.Summarise can emit
	// hasReferences=true without an N+1 per-row query. NOT
	// persisted (gorm:"-") and NOT serialised in JSON responses
	// (json:"-") — the wire-shape hasReferences flag rides via
	// the Summary struct's Extras map instead.
	HasReferencesCached bool `json:"-" gorm:"-"`
}

func (DirectorStyle) TableName() string {
	return "w_global_director_style"
}

func (d *DirectorStyle) IsActive() bool {
	return d != nil && d.Confirmed && d.Status == DirectorStyleStatusActive && d.DeletedAt == nil
}

// DirectorStyleReference — reference imagery for a DirectorStyle:
// reel clips, stills, lookbooks, lighting references. Sibling of
// CharacterReference + BrandReference; same column shape with a
// kind-specific ReferenceType taxonomy.
type DirectorStyleReference struct {
	globals.GraMODEL

	DeletedAt       *time.Time `json:"-" gorm:"column:deleted_at;index"`
	DirectorStyleID uint64     `json:"directorStyleId" gorm:"column:director_style_id;type:bigint unsigned;not null;index:idx_director_ref;comment:风格ID"`
	UID             int        `json:"uid" gorm:"column:uid;not null;default:0;comment:所属用户"`
	ImageURL        string     `json:"imageUrl" gorm:"column:image_url;type:varchar(2048);not null;default:'';comment:图片/视频URL"`
	ReferenceType   string     `json:"referenceType" gorm:"column:reference_type;type:varchar(32);not null;default:'still';comment:类型 reel_clip/still/lookbook/lighting_ref"`
	Label           string     `json:"label" gorm:"column:label;type:varchar(80);not null;default:'';comment:短标签"`
	SortOrder       int        `json:"sortOrder" gorm:"column:sort_order;type:int;not null;default:0;comment:排序"`
	Metadata        JSONMap    `json:"metadata" gorm:"column:metadata;type:json;comment:附加元数据 (e.g. timecode for reel_clip)"`
}

func (DirectorStyleReference) TableName() string {
	return "w_global_director_style_reference"
}

const (
	// DirectorStyleSourceKind values.
	DirectorStyleSourceManual    = "manual"
	DirectorStyleSourceExtracted = "extracted"
	DirectorStyleSourceUploaded  = "uploaded"
	DirectorStyleSourceImported  = "imported"
	DirectorStyleSourceTemplate  = "template"

	// DirectorStyleReferenceType values.
	DirectorStyleReferenceTypeReelClip    = "reel_clip"
	DirectorStyleReferenceTypeStill       = "still"
	DirectorStyleReferenceTypeLookbook    = "lookbook"
	DirectorStyleReferenceTypeLightingRef = "lighting_ref"

	// Status.
	DirectorStyleStatusActive int8 = 1
)
