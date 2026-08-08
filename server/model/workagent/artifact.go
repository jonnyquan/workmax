package workagent

import (
	"time"

	"server/globals"
)

const (
	ArtifactStatusDraft            = "draft"
	ArtifactStatusReference        = "reference"
	ArtifactStatusSystem           = "system"
	ArtifactStatusNeedsReview      = "needs-review"
	ArtifactStatusApproved         = "approved"
	ArtifactStatusFinal            = "final"
	ArtifactStatusChangesRequested = "changes-requested"
	ArtifactStatusExported         = "exported"

	ArtifactReviewNone             = "none"
	ArtifactReviewNeedsReview      = "needs-review"
	ArtifactReviewApproved         = "approved"
	ArtifactReviewChangesRequested = "changes-requested"

	ArtifactRelationRevision         = "revision"
	ArtifactRelationComparisonReport = "comparison_report"

	ArtifactAssetKindBrand         = "brand"
	ArtifactAssetKindCharacter     = "character"
	ArtifactAssetKindProduct       = "product"
	ArtifactAssetKindDirectorStyle = "director_style"
	ArtifactAssetKindPromptAsset   = "prompt_asset"
	ArtifactAssetKindDesignSystem  = "design_system"
	ArtifactAssetKindReference     = "reference"

	ArtifactAssetCandidateStatusDraft     = "draft"
	ArtifactAssetCandidateStatusConfirmed = "confirmed"
	ArtifactAssetCandidateStatusRejected  = "rejected"
	ProjectDesignSystemStatusArchived     = "archived"
	ProjectDesignSystemStatusRejected     = "rejected"

	ArtifactAssetCandidateTargetGlobalAsset  = "global_asset"
	ArtifactAssetCandidateTargetDesignSystem = "design_system"
	ArtifactAssetCandidateTargetPromptAsset  = "prompt_asset"

	ArtifactExportJobStatusQueued    = "queued"
	ArtifactExportJobStatusRunning   = "running"
	ArtifactExportJobStatusSucceeded = "succeeded"
	ArtifactExportJobStatusFailed    = "failed"
	ArtifactExportJobStatusBlocked   = "blocked"
)

// ArtifactRegistry is the explicit P1 artifact registry. It dual-writes
// from w_workagent_thread_file so newer outputs have stable lifecycle
// metadata while older rows still render through the read-only adapter.
type ArtifactRegistry struct {
	globals.GraMODEL
	UID                    int        `json:"uid" gorm:"column:uid;not null;index;comment:用户ID"`
	ThreadID               uint       `json:"threadId" gorm:"column:thread_id;not null;index;comment:线程ID"`
	ThreadFileID           uint       `json:"threadFileId" gorm:"column:thread_file_id;not null;uniqueIndex;comment:w_workagent_thread_file.id"`
	ArtifactKey            string     `json:"artifactKey" gorm:"column:artifact_key;type:varchar(320);not null;index;comment:版本分组键"`
	Name                   string     `json:"name" gorm:"column:name;type:varchar(500);not null;comment:原始文件名"`
	DisplayName            string     `json:"displayName" gorm:"column:display_name;type:varchar(500);not null;default:'';comment:显示名称"`
	ArtifactType           string     `json:"artifactType" gorm:"column:artifact_type;type:varchar(64);not null;index;comment:artifact 类型"`
	OutputType             string     `json:"outputType" gorm:"column:output_type;type:varchar(64);not null;comment:输出格式"`
	PreviewType            string     `json:"previewType" gorm:"column:preview_type;type:varchar(64);not null;comment:预览类型"`
	ExportTargets          string     `json:"exportTargets" gorm:"column:export_targets;type:text;comment:JSON array"`
	Version                int        `json:"version" gorm:"column:version;not null;default:1;comment:同 artifact_key 内版本号"`
	Status                 string     `json:"status" gorm:"column:status;type:varchar(32);not null;default:'draft';index;comment:artifact lifecycle status"`
	ReviewState            string     `json:"reviewState" gorm:"column:review_state;type:varchar(32);not null;default:'none';comment:review state"`
	ReviewSource           string     `json:"reviewSource" gorm:"column:review_source;type:varchar(64);not null;default:'';comment:latest review source"`
	ReviewSummary          string     `json:"reviewSummary" gorm:"column:review_summary;type:text;comment:latest review summary and fix hints"`
	ReviewDetailsJSON      string     `json:"reviewDetailsJson" gorm:"column:review_details_json;type:text;comment:structured latest review details JSON"`
	ComparisonSource       string     `json:"comparisonSource" gorm:"column:comparison_source;type:varchar(64);not null;default:'';comment:latest comparison source"`
	ComparisonSummary      string     `json:"comparisonSummary" gorm:"column:comparison_summary;type:text;comment:latest comparison summary"`
	ComparisonDecision     string     `json:"comparisonDecision" gorm:"column:comparison_decision;type:varchar(32);not null;default:'';comment:keep/revise/rollback recommendation"`
	HTMLPreviewDiagnostics string     `json:"htmlPreviewDiagnostics" gorm:"column:html_preview_diagnostics;type:text;comment:JSON array of browser preview runtime diagnostics"`
	DesignSystemBasename   string     `json:"designSystemBasename" gorm:"column:design_system_basename;type:varchar(320);not null;default:'';index;comment:artifact-level design system snapshot basename"`
	DesignSystemTitle      string     `json:"designSystemTitle" gorm:"column:design_system_title;type:varchar(255);not null;default:'';comment:artifact-level design system snapshot title"`
	DesignSystemDerived    string     `json:"designSystemDerivedFrom" gorm:"column:design_system_derived_from;type:varchar(255);not null;default:'';comment:artifact-level design system snapshot source"`
	Source                 FileSource `json:"source" gorm:"column:source;type:varchar(20);not null;default:'upload';index;comment:upload/output/system"`
	ParentArtifactID       uint       `json:"parentArtifactId" gorm:"column:parent_artifact_id;not null;default:0;index;comment:上一版本 artifact id"`
	ArtifactRelation       string     `json:"artifactRelation" gorm:"column:artifact_relation;type:varchar(32);not null;default:'';index;comment:relation to parent artifact"`
	FileHash               string     `json:"fileHash" gorm:"column:file_hash;type:varchar(64);not null;default:'';comment:源文件 hash"`
}

func (ArtifactRegistry) TableName() string {
	return "w_workagent_artifact"
}

// ArtifactAssetCandidate is the staging area between final/exported
// artifacts and typed reusable assets. It records the user's/agent's
// proposed kind and extracted reusable profile before confirmation writes
// to the corresponding platform asset table or project-local design system.
type ArtifactAssetCandidate struct {
	globals.GraMODEL
	UID          int    `json:"uid" gorm:"column:uid;not null;index;comment:用户ID"`
	ThreadID     uint   `json:"threadId" gorm:"column:thread_id;not null;index;comment:线程ID"`
	ArtifactID   uint   `json:"artifactId" gorm:"column:artifact_id;not null;index;comment:w_workagent_artifact.id"`
	ThreadFileID uint   `json:"threadFileId" gorm:"column:thread_file_id;not null;index;comment:w_workagent_thread_file.id"`
	AssetKind    string `json:"assetKind" gorm:"column:asset_kind;type:varchar(32);not null;index;comment:brand/character/product/director_style/prompt_asset/design_system/reference"`
	Name         string `json:"name" gorm:"column:name;type:varchar(255);not null;default:'';comment:candidate display name"`
	Slug         string `json:"slug" gorm:"column:slug;type:varchar(255);not null;default:'';comment:candidate slug"`
	ProfileJSON  string `json:"profileJson" gorm:"column:profile_json;type:text;comment:extracted reusable asset profile JSON"`
	Status       string `json:"status" gorm:"column:status;type:varchar(32);not null;default:'draft';index;comment:draft/confirmed/rejected"`
	TargetKind   string `json:"targetKind" gorm:"column:target_kind;type:varchar(32);not null;default:'';index;comment:materialized target kind"`
	TargetID     uint   `json:"targetId" gorm:"column:target_id;not null;default:0;index;comment:materialized target id"`
}

func (ArtifactAssetCandidate) TableName() string {
	return "w_workagent_artifact_asset_candidate"
}

// ProjectDesignSystem is the project-local design-system library materialized
// from confirmed design_system artifact candidates. It keeps custom systems in
// a first-class table while preserving candidate/artifact traceability.
type ProjectDesignSystem struct {
	globals.GraMODEL
	UID         int        `json:"uid" gorm:"column:uid;not null;index;comment:用户ID"`
	ProjectID   uint       `json:"projectId" gorm:"column:project_id;not null;default:0;index;comment:项目ID"`
	ThreadID    uint       `json:"threadId" gorm:"column:thread_id;not null;default:0;index;comment:来源线程ID"`
	ArtifactID  uint       `json:"artifactId" gorm:"column:artifact_id;not null;default:0;index;comment:来源artifact id"`
	CandidateID uint       `json:"candidateId" gorm:"column:candidate_id;not null;default:0;uniqueIndex;comment:来源asset candidate id"`
	Name        string     `json:"name" gorm:"column:name;type:varchar(255);not null;default:'';comment:显示名称"`
	Slug        string     `json:"slug" gorm:"column:slug;type:varchar(255);not null;default:'';comment:slug"`
	Basename    string     `json:"basename" gorm:"column:basename;type:varchar(320);not null;default:'';index;comment:catalog basename"`
	Title       string     `json:"title" gorm:"column:title;type:varchar(255);not null;default:'';comment:标题"`
	DerivedFrom string     `json:"derivedFrom" gorm:"column:derived_from;type:varchar(255);not null;default:'';comment:来源说明"`
	Version     int        `json:"version" gorm:"column:version;not null;default:1;comment:project-local design system version"`
	Body        string     `json:"body" gorm:"column:body;type:mediumtext;comment:9-section design system markdown"`
	Status      string     `json:"status" gorm:"column:status;type:varchar(32);not null;default:'confirmed';index;comment:confirmed/archived"`
	ReviewedBy  int        `json:"reviewedBy" gorm:"column:reviewed_by;not null;default:0;index;comment:latest reviewer uid"`
	ReviewedAt  *time.Time `json:"reviewedAt,omitempty" gorm:"column:reviewed_at;comment:latest review timestamp"`
	ReviewNote  string     `json:"reviewNote" gorm:"column:review_note;type:text;comment:latest review note"`
}

func (ProjectDesignSystem) TableName() string {
	return "w_workagent_project_design_system"
}

// PromptAsset is a reusable prompt snippet materialized from confirmed
// prompt_asset artifact candidates. It gives prompt assets a durable target
// table instead of leaving them as candidate-backed pseudo-assets.
type PromptAsset struct {
	globals.GraMODEL
	UID            int    `json:"uid" gorm:"column:uid;not null;index;comment:用户ID"`
	ProjectID      uint   `json:"projectId" gorm:"column:project_id;not null;default:0;index;comment:项目ID"`
	ThreadID       uint   `json:"threadId" gorm:"column:thread_id;not null;default:0;index;comment:来源线程ID"`
	ArtifactID     uint   `json:"artifactId" gorm:"column:artifact_id;not null;default:0;index;comment:来源artifact id"`
	CandidateID    uint   `json:"candidateId" gorm:"column:candidate_id;not null;default:0;uniqueIndex;comment:来源asset candidate id"`
	Name           string `json:"name" gorm:"column:name;type:varchar(255);not null;default:'';comment:显示名称"`
	Slug           string `json:"slug" gorm:"column:slug;type:varchar(255);not null;default:'';comment:slug"`
	Prompt         string `json:"prompt" gorm:"column:prompt;type:mediumtext;comment:positive prompt content"`
	NegativePrompt string `json:"negativePrompt" gorm:"column:negative_prompt;type:text;comment:negative prompt content"`
	ProfileJSON    string `json:"profileJson" gorm:"column:profile_json;type:text;comment:source profile JSON"`
	Status         string `json:"status" gorm:"column:status;type:varchar(32);not null;default:'confirmed';index;comment:confirmed/archived"`
}

func (PromptAsset) TableName() string {
	return "w_workagent_prompt_asset"
}

// ArtifactExportJob is the asynchronous export handoff for non-source
// artifact targets. ZIP still has a synchronous API path; PNG/PDF/MP4/GIF
// use this row as the stable contract a browser renderer worker can claim.
type ArtifactExportJob struct {
	globals.GraMODEL
	UID               int    `json:"uid" gorm:"column:uid;not null;index;comment:用户ID"`
	ThreadID          uint   `json:"threadId" gorm:"column:thread_id;not null;index;comment:线程ID"`
	ArtifactID        uint   `json:"artifactId" gorm:"column:artifact_id;not null;index;comment:w_workagent_artifact.id"`
	ThreadFileID      uint   `json:"threadFileId" gorm:"column:thread_file_id;not null;index;comment:w_workagent_thread_file.id"`
	Target            string `json:"target" gorm:"column:target;type:varchar(16);not null;index;comment:html/png/pdf/mp4/gif/zip"`
	Kind              string `json:"kind" gorm:"column:kind;type:varchar(32);not null;default:'';comment:source/bundle/render_static/render_motion"`
	Worker            string `json:"worker" gorm:"column:worker;type:varchar(64);not null;default:'';index;comment:export worker name"`
	Status            string `json:"status" gorm:"column:status;type:varchar(32);not null;default:'queued';index;comment:queued/running/succeeded/failed/blocked"`
	Reason            string `json:"reason" gorm:"column:reason;type:varchar(255);not null;default:'';comment:block/failure reason"`
	OutputExtension   string `json:"outputExtension" gorm:"column:output_extension;type:varchar(16);not null;default:'';comment:expected output extension"`
	PrerequisitesJSON string `json:"prerequisitesJson" gorm:"column:prerequisites_json;type:text;comment:JSON array"`
	PlanJSON          string `json:"planJson" gorm:"column:plan_json;type:text;comment:job plan snapshot JSON"`
	OutputFileID      uint   `json:"outputFileId" gorm:"column:output_file_id;not null;default:0;index;comment:rendered output thread file id"`
	OutputPath        string `json:"outputPath" gorm:"column:output_path;type:varchar(1024);not null;default:'';comment:rendered output path"`
	ErrorMessage      string `json:"errorMessage" gorm:"column:error_message;type:text;comment:worker error message"`
}

func (ArtifactExportJob) TableName() string {
	return "w_workagent_artifact_export_job"
}
