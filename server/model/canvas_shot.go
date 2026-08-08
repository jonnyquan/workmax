package model

import (
	"server/globals"
	"time"
)

// Shot is the persisted row that backs a canvas §8.3 ShotLink. One row
// per card the user has promoted to a "shot" (named scene with its own
// identity inside the project). Canvas documents carry ShotLinks; this
// table gives each link a stable DB id so downstream tooling — shot
// boards, storyboard syncs, queue jobs — can reference it without
// embedding the whole canvas document.
//
// LocalCardID is the idempotency key for canvas → DB sync. It mirrors
// the canvas element's string id, so running SyncShots twice with the
// same document yields no net changes.
type Shot struct {
	globals.GraMODEL
	DeletedAt          *time.Time `json:"-" gorm:"column:deleted_at;index"`
	UID                int        `json:"uid" gorm:"column:uid;not null;index:idx_uid_project,priority:1"`
	CanvasProjectID    uint       `json:"canvasProjectId" gorm:"column:canvas_project_id;not null;index:idx_uid_project,priority:2;uniqueIndex:uk_project_card,priority:1"`
	LocalCardID        string     `json:"localCardId" gorm:"column:local_card_id;type:varchar(64);not null;uniqueIndex:uk_project_card,priority:2"`
	OrderIndex         int        `json:"orderIndex" gorm:"column:order_index;not null;default:0"`
	Title              string     `json:"title" gorm:"column:title;type:varchar(200);not null;default:''"`
	TimelineStartMs    *int64     `json:"timelineStartMs,omitempty" gorm:"column:timeline_start_ms;default:null"`
	TimelineDurationMs *int64     `json:"timelineDurationMs,omitempty" gorm:"column:timeline_duration_ms;default:null"`
	Status             int8       `json:"status" gorm:"column:status;type:tinyint;not null;default:0"`

	// Per-Shot collaboration lock — see canvas-tool-design.md §8.3
	// (ShotLink.lock) and §13 M4-W12-01. LockUserID = 0 means unlocked.
	// Acquire/heartbeat/release atomicity lives in the shot_lock service;
	// these columns are the persisted state, nothing more.
	LockUserID      int        `json:"lockUserId,omitempty" gorm:"column:lock_user_id;not null;default:0"`
	LockJobID       string     `json:"lockJobId,omitempty" gorm:"column:lock_job_id;type:varchar(64);not null;default:''"`
	LockAcquiredAt  *time.Time `json:"lockAcquiredAt,omitempty" gorm:"column:lock_acquired_at;default:null"`
}

// TableName sets the persisted name. The "w_" prefix is the project
// convention for user-scoped workspace tables.
func (Shot) TableName() string {
	return "w_canvas_shot"
}

// Shot statuses. Kept as int8 to match the rest of the model package.
const (
	ShotStatusDraft    int8 = 0
	ShotStatusReady    int8 = 1
	ShotStatusArchived int8 = 2
)
