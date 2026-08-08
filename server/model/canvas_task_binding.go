package model

import (
	"server/globals"
)

// CanvasTaskBinding ties a generation task back to the canvas project
// (and optionally a specific canvas element) that spawned it.
//
// It exists because GenerationTask lives in the shared generator layer
// and doesn't know about canvas concepts. Writing a tiny binding row
// on submit lets the recovery endpoint (§canvas-tool-design M2-W4-01)
// ask "what's still running for this project?" without polluting the
// generator schema.
//
// Lifecycle: inserted in the same transaction that creates the task,
// read by GetCanvasTasks, garbage-collected alongside the task (future
// retention job) — we leave rows around after the task terminates so
// the client can still cross-reference history.
type CanvasTaskBinding struct {
	globals.GraMODEL
	UID                uint   `json:"uid" gorm:"column:uid;not null;index:idx_canvas_binding_uid_project,priority:1;comment:用户ID"`
	ProjectID          uint   `json:"projectId" gorm:"column:project_id;not null;index:idx_canvas_binding_uid_project,priority:2;comment:关联 Canvas 项目ID"`
	TaskID             string `json:"taskId" gorm:"column:task_id;type:varchar(64);not null;uniqueIndex:uk_canvas_binding_task_id;comment:GenerationTask.TaskID"`
	ElementID          string `json:"elementId" gorm:"column:element_id;type:varchar(64);default:'';comment:前端画布元素ID，用于刷新后恢复占位"`
	GenerationRunID    string `json:"generationRunId,omitempty" gorm:"column:generation_run_id;type:varchar(64);default:'';comment:Canvas chat direct generation run id"`
	GenerationThreadID string `json:"generationThreadId,omitempty" gorm:"column:generation_thread_id;type:varchar(64);default:'';comment:Canvas chat/workagent thread uuid for direct generation"`
}

func (CanvasTaskBinding) TableName() string {
	return "w_canvas_task_binding"
}
