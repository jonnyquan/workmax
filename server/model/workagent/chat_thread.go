package workagent

import (
	"server/globals"
	"time"

	"github.com/google/uuid"
)

// DefaultAgentMode is the canonical fallback when no mode is supplied
// at thread creation, request metadata, or SDK client construction.
//
// The same string ALSO appears in three places that can't import this
// constant — keep them in lockstep manually:
//
//  1. The GORM tag on AgentMode below (default:'ppt') — DDL value.
//  2. The MySQL migration scripts under server/migrations.
//  3. The SQLite test schema in server/utils/testutil/testdb.go.
//
// Bumping this default to a different mode is a coordinated change
// across (a) this constant, (b) the GORM tag below, (c) the SQL
// migration that ALTERs the column default, (d) the test-DB schema.
// Without all four, "thread created with default X, SDK call uses Y"
// silently appears and is hard to chase. Sites that read this
// constant: see grep DefaultAgentMode across server/.
const DefaultAgentMode = "ppt"

type ChatThread struct {
	globals.GraMODEL
	UID                   int        `json:"uid" gorm:"column:uid;not null;comment:用户id"`
	UUID                  string     `json:"uuid" gorm:"column:uuid;type:varchar(36);unique;not null;comment:用于分享的UUID"`
	ProjectID             uint       `json:"projectId,omitempty" gorm:"column:project_id;default:0;index:idx_workagent_thread_uid_project,priority:2;comment:Canvas project id for canvas-surface threads"`
	AgentSessionID        string     `json:"agentSessionId" gorm:"column:agent_session_id;type:varchar(255);comment:Agent CLI session ID"`
	AgentSessionCreatedAt *time.Time `json:"agentSessionCreatedAt" gorm:"column:agent_session_created_at;comment:Agent session 创建时间"`
	Name                  string     `json:"name" gorm:"column:name;type:varchar(255);comment:会话名称"`
	AgentMode             string     `json:"agentMode" gorm:"column:agent_mode;type:varchar(50);default:'ppt';comment:Agent模式(ppt/flashCard/pictureBook/character/logo/productShot/modelTryOn/marketingPoster/lifestyle/packaging/socialAd/webBanner/mobileStory/oohBillboard)"`
	AgentType             string     `json:"agentType" gorm:"column:agent_type;type:varchar(50);default:'general_agent';comment:Agent类型(general_agent)"`
	WorkspacePath         string     `json:"workspacePath" gorm:"column:workspace_path;type:varchar(255);default:'';comment:Workspace相对路径"`
	Model                 string     `json:"model" gorm:"column:model;type:varchar(255);comment:模型"`
	MaxTokens             int        `json:"maxTokens" gorm:"column:max_tokens;comment:最大token数"`
	ContextCount          int        `json:"contextCount" gorm:"column:context_count;comment:上下文数量"`
	PresencePenalty       float32    `json:"presencePenalty" gorm:"column:presence_penalty;comment:存在惩罚"`
	FrequencyPenalty      float32    `json:"frequencyPenalty" gorm:"column:frequency_penalty;comment:频率惩罚"`
	Temperature           float32    `json:"temperature" gorm:"column:temperature;comment:温度"`
	Prompt                string     `json:"prompt" gorm:"column:prompt;comment:提示词"`
	// Removed in B-M1: TopSort / Plugins / LocalPlugins / Icon. These
	// columns existed from a generic LLM-chat schema imported at
	// project start; the agent flow never read or wrote them and no
	// frontend referenced the JSON keys (verified via grep). DB
	// columns are LEFT IN PLACE for now — dropping live columns
	// requires a coordinated migration + read replica check, which
	// is out of scope for the in-flight cleanup. Without struct
	// fields, GORM stops SELECTing them (slightly lighter rows) and
	// stops INSERTing into them (DB defaults take over). Safe.
	MessageCount int    `json:"messageCount" gorm:"column:message_count;default:0;comment:消息数量"`
	MsgPreview   string `json:"msgPreview" gorm:"column:msg_preview;type:text;comment:最新消息预览"`
	FileCount    int    `json:"fileCount" gorm:"column:file_count;default:0;comment:文件数量"`
	// IsPublic gates the public share endpoints. New threads are private
	// by default; SetConversationVisibility is the explicit publish action.
	IsPublic bool `json:"isPublic" gorm:"column:is_public;default:false;not null;comment:是否允许公开分享"`
	// LatestPlan is the most-recent TodoWrite snapshot for this thread,
	// stored as the raw JSON the SDK emits (array of
	// {id, content, status, priority}). Populated by
	// saveAgentConversation when a turn fires TodoWrite; the Progress
	// section in the ContextPanel reads it on conversation load so
	// the plan rehydrates even when the source message has been
	// paginated out. Older threads stay NULL until their next
	// TodoWrite — the frontend falls back to scanning loaded messages
	// in that case, so missing values are not a regression.
	LatestPlan string `json:"latestPlan,omitempty" gorm:"column:latest_plan;type:json;comment:最新一次 TodoWrite 快照 (JSON)，用于跨分页恢复 Progress 视图"`
	// PlanHistory is a JSON array of the last N=5 TodoWrite snapshots
	// (oldest first), each {todos, captured_at}. Maintained by
	// saveAgentConversation as push-and-truncate so super-long
	// conversations (>50 messages) can rehydrate the FULL timeline UI
	// without paging back to find earlier TodoWrites. The cap of 5 is
	// centralized in the writer; LatestPlan is still the
	// single-snapshot fallback when this is NULL.
	PlanHistory string `json:"planHistory,omitempty" gorm:"column:plan_history;type:json;comment:最近 N=5 条 TodoWrite 快照数组，供超长会话首屏 rehydrate 完整时间轴"`
}

// SEO 状态常量
const (
	SeoStatusPrivate  = "private"  // 私有（默认）
	SeoStatusPending  = "pending"  // 等待审核
	SeoStatusApproved = "approved" // 已通过
	SeoStatusFeatured = "featured" // 精选
	SeoStatusRejected = "rejected" // 已拒绝
)

func (ChatThread) TableName() string {
	return "w_workagent_thread"
}

// ChatThreadResponse represents the response structure for API
type ChatThreadResponse struct {
	Id               uint      `json:"id"`
	UID              int       `json:"uid"`
	UUID             string    `json:"uuid"`
	ProjectID        uint      `json:"projectId,omitempty"`
	Name             string    `json:"name"`
	Model            string    `json:"model"`
	MaxTokens        int       `json:"maxTokens"`
	ContextCount     int       `json:"contextCount"`
	PresencePenalty  float32   `json:"presencePenalty"`
	FrequencyPenalty float32   `json:"frequencyPenalty"`
	Temperature      float32   `json:"temperature"`
	AgentMode        string    `json:"agentMode"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// ToResponse converts ChatThread to response format
func (ct *ChatThread) ToResponse() ChatThreadResponse {
	return ChatThreadResponse{
		Id:               ct.Id,
		UID:              ct.UID,
		UUID:             ct.UUID,
		ProjectID:        ct.ProjectID,
		Name:             ct.Name,
		Model:            ct.Model,
		MaxTokens:        ct.MaxTokens,
		ContextCount:     ct.ContextCount,
		PresencePenalty:  ct.PresencePenalty,
		FrequencyPenalty: ct.FrequencyPenalty,
		Temperature:      ct.Temperature,
		AgentMode:        ct.AgentMode,
		CreatedAt:        ct.CreatedAt,
		UpdatedAt:        ct.UpdatedAt,
	}
}

// CreateAIChatThread creates a new chat thread for AI Agent
func CreateAIChatThread(userID int, title string, agentMode string) *ChatThread {
	if agentMode == "" {
		agentMode = DefaultAgentMode
	}
	return &ChatThread{
		UID:       userID,
		UUID:      uuid.New().String(), // Generate unique UUID
		Name:      title,
		AgentMode: agentMode,
		AgentType: "general_agent",
		Model:     "work-pro",
		IsPublic:  false,
	}
}
