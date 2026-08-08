package model

import (
	"server/globals"
	"time"
)

type CanvasProject struct {
	globals.GraMODEL
	DeletedAt     *time.Time `json:"-" gorm:"column:deleted_at;index"`
	UID           int        `json:"uid" gorm:"column:uid;not null;index:idx_uid_updated_at,priority:1"`
	// Stage 1.5 (migration 20260630): unambiguous marker for system-
	// managed projects. 0 = user-created (default), 1 = workagent
	// Personal Workspace. Reads / writes via the GlobalProjectKind*
	// constants below so a typo at a call site can't drift the
	// sentinel.
	SystemKind    int8       `json:"systemKind,omitempty" gorm:"column:system_kind;type:tinyint;not null;default:0;index:idx_global_project_uid_system_kind,priority:2"`
	UUID          string     `json:"uuid" gorm:"column:uuid;type:varchar(36);uniqueIndex:uk_canvas_project_uuid;not null"`
	// C-11 share-link token. NULL = legacy state where the share URL
	// is just /share/{uuid} (no token required). When set, public
	// reads must pass ?t=<share_token> or get ProjectNotFound. Pointer
	// so JSON omitempty + NULL distinction round-trip cleanly.
	ShareToken    *string    `json:"shareToken,omitempty" gorm:"column:share_token;type:varchar(64);default:null"`
	Title         string     `json:"title" gorm:"column:title;type:varchar(200);not null;default:''"`
	Visibility    int8       `json:"visibility" gorm:"column:visibility;type:tinyint;not null;default:0;index:idx_visibility_updated_at,priority:1"`
	ThumbnailURL  string     `json:"thumbnailUrl" gorm:"column:thumbnail_url;type:longtext;not null"`
	LatestVersion int        `json:"latestVersion" gorm:"column:latest_version;not null;default:0"`
	// M5-B-06 Stage 1+2 dual-write target: live document on the project
	// row. Stage 2 populates these on every write path; Stage 4 swaps
	// reads to use them. Nullable for back-compat with pre-Stage-1 rows
	// — Stage 5's contract step tightens to NOT NULL after backfill.
	Document      JSONMap `json:"document,omitempty" gorm:"column:document;type:json;default:null"`
	SchemaVersion int     `json:"schemaVersion" gorm:"column:schema_version;default:null"`
	ElementCount  int     `json:"elementCount" gorm:"column:element_count;default:null"`

	// Share snapshot metadata is response-only. The live draft remains on
	// CanvasProject; these fields let owners see whether public links are stale
	// without coupling autosave to publishing.
	PublishedAt           *time.Time `json:"publishedAt,omitempty" gorm:"-"`
	PublishedVersion      int        `json:"publishedVersion,omitempty" gorm:"-"`
	HasUnpublishedChanges bool       `json:"hasUnpublishedChanges" gorm:"-"`

	// P1 #6 (migration 20260618) — project-level credit budget.
	// The Team / Enterprise pricing tier's "this campaign can
	// spend at most N credits" promise.
	//
	// BudgetCreditsCap is *int because NULL means "no cap"
	// (the default for every existing row); a literal 0 means
	// "paused — reject all new spend". The pointer keeps the
	// no-cap / 0-cap distinction round-trippable through JSON.
	//
	// BudgetCreditsUsed is the running tally. Reservation settle
	// hook (slice 2) increments it; refund decrements. Concurrent
	// settles use a conditional UPDATE so two parallel writers
	// can't both pass the cap check then both increment.
	BudgetCreditsCap  *int `json:"budgetCreditsCap,omitempty" gorm:"column:budget_credits_cap;default:null;comment:可选的每项目 credits 上限"`
	BudgetCreditsUsed int  `json:"budgetCreditsUsed" gorm:"column:budget_credits_used;not null;default:0;comment:已消耗的 credits 累计"`
}

func (CanvasProject) TableName() string {
	// Plan-A Phase A4: table renamed from w_canvas_project →
	// w_global_project. The Go type keeps its CanvasProject name
	// (still used by the canvas surface's snapshot / share /
	// patch path) but the storage is now platform-level: brand /
	// character / director_style / thread.project_id all
	// reference this same row set.
	return "w_global_project"
}

// Stage 1.5 SystemKind constants. Use these instead of literal int8
// values at every read / write site so the sentinel mapping is
// audit-traceable. New kinds get added here (and only here) — the
// workagent / canvas service code never compares to bare integers.
const (
	GlobalProjectKindUser              int8 = 0 // default — user-created project
	GlobalProjectKindPersonalWorkspace int8 = 1 // workagent default workspace, undeletable except via account close
	// 2-127 reserved for future system kinds (team-default, template
	// root, etc.). When adding, also update DeleteProjectWithOptions
	// guard semantics in canvas_project_service.go — by default any
	// non-zero kind is treated as system-managed and refuses delete.
)

// CanvasVersion is the response DTO for /api/tools/canvas/projects/:id/versions/*
// endpoints. After M5-B-06 Stage 5 (Retire-T5), the legacy version-row
// table (dropped by migration 20260534) no longer exists — values of
// this struct are composed by GetCanvasVersion / ListCanvasVersions
// from w_global_project (live document for the latest version) and
// w_canvas_snapshot (frozen history). The field shape stays
// unchanged so the HTTP envelope and frontend consumers don't
// break — only the storage backing changed.
//
// Document mirrors the snapshot's frozen state for historical
// versions, or project.document (live state) for the latest.
type CanvasVersion struct {
	Id           uint      `json:"id"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	ProjectID    uint      `json:"projectId"`
	Version      int       `json:"version"`
	CreatedBy    int       `json:"createdBy"`
	Source       string    `json:"source"`
	Message      string    `json:"message"`
	ElementCount int       `json:"elementCount"`
	ThumbnailURL string    `json:"thumbnailUrl"`
	Document     JSONMap   `json:"document"`
}

// CanvasSnapshot is M5-B-06's immutable snapshot table — replaces
// the legacy version-row table's snapshot rows after Stages 2-5
// ship (that legacy table was dropped by migration 20260534). Stage 2
// dual-writes here on CreateCanvasVersion ("Save as new version");
// PatchElements / UpdateCanvasVersion don't touch this table because
// patches and autosave are sub-snapshot operations. snapshot_no is a
// monotonic per-project counter allocated via SELECT MAX(...) FOR UPDATE
// inside the create-version transaction.
//
// No GraMODEL embed: snapshots are immutable, so there's no UpdatedAt
// column. No DeletedAt either — the table is hard-delete only (Stage 5's
// retention cap will trim oldest by created_at).
type CanvasSnapshot struct {
	Id            uint      `json:"id" gorm:"primarykey"`
	ProjectID     uint      `json:"projectId" gorm:"column:project_id;not null;uniqueIndex:uk_project_snapshot,priority:1;index:idx_project_created,priority:1"`
	SnapshotNo    int       `json:"snapshotNo" gorm:"column:snapshot_no;not null;uniqueIndex:uk_project_snapshot,priority:2"`
	CreatedBy     int       `json:"createdBy" gorm:"column:created_by;not null"`
	Source        string    `json:"source" gorm:"column:source;type:varchar(20);not null;default:'manual'"`
	Message       string    `json:"message" gorm:"column:message;type:varchar(255);not null;default:''"`
	ElementCount  int       `json:"elementCount" gorm:"column:element_count;not null;default:0"`
	ThumbnailURL  string    `json:"thumbnailUrl" gorm:"column:thumbnail_url;type:longtext;not null"`
	Document      JSONMap   `json:"document" gorm:"column:document;type:json;not null"`
	SchemaVersion int       `json:"schemaVersion" gorm:"column:schema_version;not null"`
	CreatedAt     time.Time `json:"createdAt" gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP;autoCreateTime"`
}

func (CanvasSnapshot) TableName() string {
	return "w_canvas_snapshot"
}

// CanvasShareSnapshot is the explicit publish surface for Canvas share
// links. Project.document remains the live draft; this row is only
// updated when the owner publishes by switching visibility to unlisted
// or public.
type CanvasShareSnapshot struct {
	globals.GraMODEL
	ProjectID        uint    `json:"projectId" gorm:"column:project_id;not null;uniqueIndex:uk_canvas_share_project"`
	PublishedVersion int     `json:"publishedVersion" gorm:"column:published_version;not null;default:0"`
	ThumbnailURL     string  `json:"thumbnailUrl" gorm:"column:thumbnail_url;type:longtext;not null"`
	Document         JSONMap `json:"document" gorm:"column:document;type:json;not null"`
	SchemaVersion    int     `json:"schemaVersion" gorm:"column:schema_version;not null"`
}

func (CanvasShareSnapshot) TableName() string {
	return "w_canvas_share_snapshot"
}

// CanvasElementDTO is a typed representation of canvas elements used in the
// Chat API context. It provides compile-time safety for element data that was
// previously passed as map[string]interface{}.
type CanvasElementDTO struct {
	ID          string  `json:"id"`
	Type        string  `json:"type"` // text | image | shape | drawing | widget
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Content     string  `json:"content,omitempty"`
	Width       float64 `json:"width,omitempty"`
	Height      float64 `json:"height,omitempty"`
	Color       string  `json:"color,omitempty"`
	Src         string  `json:"src,omitempty"`
	Path        string  `json:"path,omitempty"`
	Visible     *bool   `json:"visible,omitempty"`
	Locked      *bool   `json:"locked,omitempty"`
	Rotation    float64 `json:"rotation,omitempty"`
	StrokeWidth float64 `json:"strokeWidth,omitempty"`
	FontSize    float64 `json:"fontSize,omitempty"`
	GroupId     string  `json:"groupId,omitempty"`
	GroupName   string  `json:"groupName,omitempty"`

	// Frame support
	FrameId     string `json:"frameId,omitempty"`
	ClipContent *bool  `json:"clipContent,omitempty"`

	// AI Generation State & Metadata (synced with frontend CanvasElement type)
	IsGenerating   *bool  `json:"isGenerating,omitempty"`
	TaskId         string `json:"taskId,omitempty"`
	TaskStatus     string `json:"taskStatus,omitempty"` // pending | processing | failed | completed
	Prompt         string `json:"prompt,omitempty"`
	NegativePrompt string `json:"negativePrompt,omitempty"`
	Seed           string `json:"seed,omitempty"`
	Model          string `json:"model,omitempty"`
	StylePreset    string `json:"stylePreset,omitempty"`
	AspectRatio    string `json:"aspectRatio,omitempty"`
}

// ViewportDTO is a typed representation of the canvas viewport state.
// Replaces map[string]interface{} for compile-time safety.
type ViewportDTO struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Scale float64 `json:"scale"`
}

// PaginatedResult is a generic paginated response wrapper.
type PaginatedResult[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Limit int   `json:"limit"`
}
