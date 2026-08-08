package canvas

import "server/model"

// These structs define the Server-owned Canvas v2 wire shape. They exist so the document
// migrator (M3-W6-02), schema validator (ValidateCanvasDocumentV2 in
// document_v2_validate.go), and future asset/injection code can
// reference strongly-typed field names instead of map lookups.
//
// CanvasVersion.Document stays as model.JSONMap in the database; these
// types are used at the edges (HTTP handler / validator / migrator).

// SchemaVersionV2 is the document-level schemaVersion tag for v2.
const SchemaVersionV2 = 2

// WorkMaxTypeName is the discriminated-union tag consumed by Desktop.
type WorkMaxTypeName string

const (
	WorkMaxTypeElement WorkMaxTypeName = "element"
	WorkMaxTypeShot    WorkMaxTypeName = "shot"
	WorkMaxTypeAsset   WorkMaxTypeName = "asset"
	WorkMaxTypeSession WorkMaxTypeName = "session"
)

// WorkMaxBaseRecord is the metadata every persisted record carries in v2.
type WorkMaxBaseRecord struct {
	ID           string          `json:"id"`
	TypeName     WorkMaxTypeName `json:"typeName"`
	Version      int             `json:"version"`
	VersionNonce int64           `json:"versionNonce"`
}

// AssetBindingScope and AssetBinding are re-exported from model so the
// task-queue layer can stash bindings in model.TaskRequestData without
// importing canvas (which would cycle). Canvas-side call-sites keep the
// short `canvas.AssetBinding` name they already use.
type AssetBindingScope = model.AssetBindingScope
type AssetBinding = model.AssetBinding

const (
	AssetScopeElement = model.AssetScopeElement
	AssetScopeShot    = model.AssetScopeShot
	AssetScopeProject = model.AssetScopeProject
)

// ShotLockState captures a per-Shot editing lock (§8.3).
type ShotLockState struct {
	UserID     int    `json:"userId"`
	JobID      string `json:"jobId"`
	AcquiredAt int64  `json:"acquiredAt"`
}

// SessionAttempt is one recorded attempt inside a Session.
type SessionAttempt struct {
	JobID   string `json:"jobId"`
	Status  string `json:"status"`
	AssetID string `json:"assetId,omitempty"`
}

// Session is Runway-style grouping for repeated attempts at one Shot.
type Session struct {
	ID            string           `json:"id"`
	CreatedAt     int64            `json:"createdAt"`
	ResultAssetID string           `json:"resultAssetId,omitempty"`
	Attempts      []SessionAttempt `json:"attempts"`
}

// ShotLink bridges a canvas card to `w_canvas_shot`.
type ShotLink struct {
	WorkMaxBaseRecord
	ShotID           int            `json:"shotId"`
	LocalCardID      string         `json:"localCardId"`
	OrderIndex       int            `json:"orderIndex"`
	TimelineStart    *int64         `json:"timelineStart,omitempty"`
	TimelineDuration *int64         `json:"timelineDuration,omitempty"`
	Sessions         []Session      `json:"sessions,omitempty"`
	Lock             *ShotLockState `json:"lock,omitempty"`
}

// TimelineTrackKind enumerates track payload types.
type TimelineTrackKind string

const (
	TimelineTrackVideo    TimelineTrackKind = "video"
	TimelineTrackAudio    TimelineTrackKind = "audio"
	TimelineTrackSubtitle TimelineTrackKind = "subtitle"
)

// TimelineTrack groups ShotLinks on a single horizontal lane.
type TimelineTrack struct {
	ID    string            `json:"id"`
	Kind  TimelineTrackKind `json:"kind"`
	Clips []ShotLink        `json:"clips"`
}

// Transition is a timed effect between two clips.
type Transition struct {
	ID         string `json:"id"`
	FromClipID string `json:"fromClipId"`
	ToClipID   string `json:"toClipId"`
	DurationMs int64  `json:"durationMs"`
	Effect     string `json:"effect"`
}

// AudioTrack is a standalone audio lane (music bed, VO, SFX).
type AudioTrack struct {
	ID         string `json:"id"`
	Src        string `json:"src"`
	StartMs    int64  `json:"startMs"`
	DurationMs int64  `json:"durationMs"`
}

// Subtitle is one timed caption line.
type Subtitle struct {
	ID      string `json:"id"`
	StartMs int64  `json:"startMs"`
	EndMs   int64  `json:"endMs"`
	Text    string `json:"text"`
}

// TimelineConfig is the document-level timeline view payload.
type TimelineConfig struct {
	FPS           int             `json:"fps"`
	TotalDuration int64           `json:"totalDuration"`
	Tracks        []TimelineTrack `json:"tracks"`
	Transitions   []Transition    `json:"transitions,omitempty"`
	AudioTracks   []AudioTrack    `json:"audioTracks,omitempty"`
	Subtitles     []Subtitle      `json:"subtitles,omitempty"`
}

// CanvasViewMode enumerates the persisted canvas views.
type CanvasViewMode string

const (
	ViewModeFreeform  CanvasViewMode = "freeform"
	ViewModeShotBoard CanvasViewMode = "shot-board"
	ViewModeTimeline  CanvasViewMode = "timeline"
	ViewModeWorkflow  CanvasViewMode = "workflow"
)

// TemplateRef records which template a project was instantiated from.
type TemplateRef struct {
	TemplateID      int                    `json:"templateId"`
	TemplateVersion int                    `json:"templateVersion"`
	Slots           map[string]interface{} `json:"slots"`
}

type CanvasWorkflowRunStatus string

const (
	WorkflowRunQueued    CanvasWorkflowRunStatus = "queued"
	WorkflowRunRunning   CanvasWorkflowRunStatus = "running"
	WorkflowRunCompleted CanvasWorkflowRunStatus = "completed"
	WorkflowRunFailed    CanvasWorkflowRunStatus = "failed"
	WorkflowRunCancelled CanvasWorkflowRunStatus = "cancelled"
)

type CanvasWorkflowNodeRunStatus string

const (
	WorkflowNodeRunIdle      CanvasWorkflowNodeRunStatus = "idle"
	WorkflowNodeRunQueued    CanvasWorkflowNodeRunStatus = "queued"
	WorkflowNodeRunRunning   CanvasWorkflowNodeRunStatus = "running"
	WorkflowNodeRunBlocked   CanvasWorkflowNodeRunStatus = "blocked"
	WorkflowNodeRunCompleted CanvasWorkflowNodeRunStatus = "completed"
	WorkflowNodeRunFailed    CanvasWorkflowNodeRunStatus = "failed"
	WorkflowNodeRunSkipped   CanvasWorkflowNodeRunStatus = "skipped"
)

type CanvasWorkflowNodeResult struct {
	NodeID          string                      `json:"nodeId"`
	Status          CanvasWorkflowNodeRunStatus `json:"status"`
	InputElementIDs []string                    `json:"inputElementIds,omitempty"`
	OutputElementID string                      `json:"outputElementId,omitempty"`
	TaskID          string                      `json:"taskId,omitempty"`
	WorkflowRunID   string                      `json:"workflowRunId,omitempty"`
	WorkflowNodeID  string                      `json:"workflowNodeId,omitempty"`
	ErrorCode       string                      `json:"errorCode,omitempty"`
	ErrorMessage    string                      `json:"errorMessage,omitempty"`
	StartedAt       *int64                      `json:"startedAt,omitempty"`
	CompletedAt     *int64                      `json:"completedAt,omitempty"`
}

type CanvasWorkflowRunSummary struct {
	GeneratedImages  int `json:"generatedImages"`
	ImageOps         int `json:"imageOps"`
	VideoGenerations int `json:"videoGenerations"`
	Skipped          int `json:"skipped"`
	Failed           int `json:"failed"`
}

type CanvasWorkflowRun struct {
	ID                string                     `json:"id"`
	WorkflowID        string                     `json:"workflowId,omitempty"`
	Status            CanvasWorkflowRunStatus    `json:"status"`
	StartedAt         int64                      `json:"startedAt"`
	CompletedAt       *int64                     `json:"completedAt,omitempty"`
	ResumeCount       int                        `json:"resumeCount,omitempty"`
	LastResumedNodeID string                     `json:"lastResumedNodeId,omitempty"`
	LastResumedAt     *int64                     `json:"lastResumedAt,omitempty"`
	NodeResults       []CanvasWorkflowNodeResult `json:"nodeResults"`
	Summary           CanvasWorkflowRunSummary   `json:"summary"`
}

type CanvasWorkflow struct {
	Nodes        []map[string]interface{} `json:"nodes"`
	Edges        []map[string]interface{} `json:"edges"`
	GraphVersion *int                     `json:"graphVersion,omitempty"`
	LastEditedAt *int64                   `json:"lastEditedAt,omitempty"`
	LastEditedBy string                   `json:"lastEditedBy,omitempty"`
}

// CanvasElementV2 is the v2 element shape: v1 fields plus optional
// record metadata, shot/asset/template bindings. We keep v1 fields as
// a free-form map because §8.3 explicitly preserves the v1 schema —
// the migrator (M3-W6-02) is responsible for mapping those in place.
type CanvasElementV2 struct {
	// V1 fields preserved as-is. Kept as raw map so the migrator can
	// pass v1 documents through without shape loss.
	V1Fields map[string]interface{} `json:"-"`

	// Optional v2 record metadata.
	ID             string          `json:"id,omitempty"`
	TypeName       WorkMaxTypeName `json:"typeName,omitempty"`
	Version        int             `json:"version,omitempty"`
	VersionNonce   int64           `json:"versionNonce,omitempty"`
	Kind           string          `json:"kind,omitempty"`
	ShotLinkID     string          `json:"shotLinkId,omitempty"`
	AssetBindings  *AssetBinding   `json:"assetBindings,omitempty"`
	TemplateSlotID string          `json:"templateSlotId,omitempty"`
}

// Viewport mirrors the frontend Viewport type.
type Viewport struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Scale float64 `json:"scale"`
}

// CanvasDocumentV2 is the top-level v2 document shape. The JSONMap
// representation persisted in `w_canvas_snapshot.document` (the
// version-history surface that subsumed the legacy version-row
// table at M5-B-06 Stage 5; that legacy table was later dropped by
// migration 20260534) is expected to match this structure once the
// migrator upgrades it.
type CanvasDocumentV2 struct {
	SchemaVersion        int                    `json:"schemaVersion"`
	Elements             []CanvasElementV2      `json:"elements"`
	Viewport             Viewport               `json:"viewport"`
	ViewMode             CanvasViewMode         `json:"viewMode"`
	SelectedIDs          []string               `json:"selectedIds,omitempty"`
	Settings             map[string]interface{} `json:"settings,omitempty"`
	Shots                []ShotLink             `json:"shots,omitempty"`
	Timeline             *TimelineConfig        `json:"timeline,omitempty"`
	Workflow             *CanvasWorkflow        `json:"workflow,omitempty"`
	WorkflowRuns         []CanvasWorkflowRun    `json:"workflowRuns,omitempty"`
	ProjectAssetBindings *AssetBinding          `json:"projectAssetBindings,omitempty"`
	TemplateRef          *TemplateRef           `json:"templateRef,omitempty"`
}
