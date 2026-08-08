package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ThreadFullRow is the full thread metadata returned by
// GET /api/desktop/sync/threads/:id (P1.A.3). Includes the heavy
// columns the delta endpoint (P1.A.2) deliberately excludes:
// prompt, latest_plan, plan_history. The desktop renderer fetches
// this when the user opens a thread that needs the full context
// (e.g. resuming a long-running plan after a fresh sync).
//
// Field selection vs ThreadDeltaRow:
//   - Includes prompt / latest_plan / plan_history (the heavy
//     blobs the delta endpoint skips for payload size)
//   - Includes workspace_path / max_tokens / temperature etc.
//     (control-plane knobs the renderer surfaces in settings UI)
//   - Same uuid / cloud_thread_id / name / agent_mode / etc. as
//     the delta row so callers can substitute a Full for a Delta
//     in any place that needs both (rare, but defensible).
type ThreadFullRow struct {
	CloudThreadID    string    `json:"cloud_thread_id"`
	UUID             string    `json:"uuid"`
	Name             string    `json:"name"`
	AgentMode        string    `json:"agent_mode"`
	AgentType        string    `json:"agent_type"`
	Model            string    `json:"model"`
	WorkspacePath    string    `json:"workspace_path,omitempty"`
	MaxTokens        int       `json:"max_tokens,omitempty"`
	ContextCount     int       `json:"context_count,omitempty"`
	PresencePenalty  float32   `json:"presence_penalty,omitempty"`
	FrequencyPenalty float32   `json:"frequency_penalty,omitempty"`
	Temperature      float32   `json:"temperature,omitempty"`
	Prompt           string    `json:"prompt,omitempty"`
	MessageCount     int       `json:"message_count"`
	MsgPreview       string    `json:"msg_preview,omitempty"`
	FileCount        int       `json:"file_count"`
	IsPublic         bool      `json:"is_public"`
	LatestPlan       string    `json:"latest_plan,omitempty"`
	PlanHistory      string    `json:"plan_history,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
	CreatedAt        time.Time `json:"created_at"`
}

// ErrThreadNotFound is returned by GetThreadByCloudID when the
// thread doesn't exist OR isn't owned by the requesting uid.
// Same "don't leak existence" posture as ErrThreadNotOwned in
// the messages repo.
var ErrThreadNotFound = errors.New("sync threads: thread not found or not owned by uid")

// GetThreadByCloudID returns the full metadata for one thread by
// its cloud PK + uid. Strict uid-scoping for IDOR safety; mismatch
// AND missing both surface as ErrThreadNotFound so the wire shape
// can't be used to enumerate existence.
func GetThreadByCloudID(ctx context.Context, db *gorm.DB, uid int, cloudThreadID uint64) (ThreadFullRow, error) {
	if db == nil {
		return ThreadFullRow{}, fmt.Errorf("get thread: db is nil")
	}
	if uid <= 0 {
		return ThreadFullRow{}, fmt.Errorf("get thread: uid must be positive (got %d)", uid)
	}
	if cloudThreadID == 0 {
		return ThreadFullRow{}, fmt.Errorf("get thread: cloud_thread_id required")
	}

	type scanRow struct {
		ID               uint
		UUID             string
		Name             string
		AgentMode        string
		AgentType        string
		Model            string
		WorkspacePath    string
		MaxTokens        int
		ContextCount     int
		PresencePenalty  float32
		FrequencyPenalty float32
		Temperature      float32
		Prompt           string
		MessageCount     int
		MsgPreview       string
		FileCount        int
		IsPublic         bool
		LatestPlan       string
		PlanHistory      string
		UpdatedAt        string
		CreatedAt        string
	}
	var r scanRow
	err := db.WithContext(ctx).
		Table("w_workagent_thread").
		Select(`id, uuid, name, agent_mode, agent_type, model,
		        COALESCE(workspace_path,'')     AS workspace_path,
		        max_tokens, context_count,
		        presence_penalty, frequency_penalty, temperature,
		        COALESCE(prompt,'')             AS prompt,
		        message_count, msg_preview, file_count, is_public,
		        COALESCE(latest_plan,'')        AS latest_plan,
		        COALESCE(plan_history,'')       AS plan_history,
		        updated_at, created_at`).
		Where("id = ? AND uid = ?", cloudThreadID, uid).
		Limit(1).
		Scan(&r).Error
	if err != nil {
		return ThreadFullRow{}, fmt.Errorf("get thread: query: %w", err)
	}
	if r.ID == 0 {
		// Either no row OR uid mismatch — both collapse to
		// "not found" so we don't leak existence.
		return ThreadFullRow{}, ErrThreadNotFound
	}
	return ThreadFullRow{
		CloudThreadID:    fmt.Sprintf("%d", r.ID),
		UUID:             r.UUID,
		Name:             r.Name,
		AgentMode:        r.AgentMode,
		AgentType:        r.AgentType,
		Model:            r.Model,
		WorkspacePath:    r.WorkspacePath,
		MaxTokens:        r.MaxTokens,
		ContextCount:     r.ContextCount,
		PresencePenalty:  r.PresencePenalty,
		FrequencyPenalty: r.FrequencyPenalty,
		Temperature:      r.Temperature,
		Prompt:           r.Prompt,
		MessageCount:     r.MessageCount,
		MsgPreview:       r.MsgPreview,
		FileCount:        r.FileCount,
		IsPublic:         r.IsPublic,
		LatestPlan:       r.LatestPlan,
		PlanHistory:      r.PlanHistory,
		UpdatedAt:        parseRowTime(r.UpdatedAt),
		CreatedAt:        parseRowTime(r.CreatedAt),
	}, nil
}
