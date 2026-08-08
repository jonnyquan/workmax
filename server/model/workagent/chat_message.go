package workagent

import (
	"encoding/json"
	"server/globals"
	"server/service/llm"
	"time"
)

type ChatMessage struct {
	globals.GraMODEL
	UID                  int    `json:"uid" gorm:"column:uid;not null;comment:用户id"`
	UUID                 string `json:"uuid" gorm:"column:uuid;type:varchar(36);unique;not null;comment:用于分享的UUID"`
	ThreadID             int    `json:"threadId" gorm:"column:thread_id;not null;comment:thread表id"`
	UserText             string `json:"userText" gorm:"column:user_text;type:longtext;comment:用户输入"`
	AIText               string `json:"aiText" gorm:"column:ai_text;type:longtext;comment:AI回答"`
	TotalPrompt          string `json:"totalPrompt" gorm:"column:total_prompt;type:longtext;comment:完整的对话prompt"`
	IP                   string `json:"ip" gorm:"column:ip;type:varchar(20);comment:ip"`
	TaskID               string `json:"taskId" gorm:"column:task_id;type:varchar(20);comment:task_id"`
	Model                string `json:"model" gorm:"column:model;type:varchar(20);comment:模型"`
	DeductIntegral       int    `json:"deductIntegral" gorm:"column:deduct_integral;type:int;comment:扣除积分"`
	RefundIntegral       int    `json:"refundIntegral" gorm:"column:refund_integral;type:int;comment:退回积分"`
	UseTokens            int    `json:"useTokens" gorm:"column:use_tokens;type:int;comment:使用token"`
	PromptTokens         int    `json:"promptTokens" gorm:"column:prompt_tokens;type:int;comment:prompt_tokens"`
	CompletionTokens     int    `json:"completionTokens" gorm:"column:completion_tokens;type:int;comment:completion_tokens"`
	ContextTokens        int    `json:"contextTokens" gorm:"column:context_tokens;type:int;comment:context_tokens"`
	UseImages            string `json:"useImages" gorm:"column:use_images;type:text;comment:使用图片"`
	AIAudio              string `json:"aiAudio" gorm:"column:ai_audio;type:text;comment:AI音频"`
	UserAudio            string `json:"userAudio" gorm:"column:user_audio;type:text;comment:用户音频"`
	AppendDeductIntegral int    `json:"appendDeductIntegral" gorm:"column:append_deduct_integral;type:int;comment:追加扣除积分"`
	UseFiles             string `json:"useFiles" gorm:"column:use_files;type:text;comment:使用文件"`
	ChatMode             string `json:"chatMode" gorm:"column:chat_mode;type:varchar(20);comment:聊天模式"`
	// Agent Message System fields
	ContentType           string  `json:"contentType,omitempty" gorm:"column:content_type;type:varchar(50);comment:消息内容类型"`
	StructuredContent     string  `json:"structuredContent,omitempty" gorm:"column:structured_content;type:longtext;comment:结构化内容JSON"`
	Actions               string  `json:"actions,omitempty" gorm:"column:actions;type:text;comment:交互操作JSON"`
	Metadata              string  `json:"metadata,omitempty" gorm:"column:metadata;type:text;comment:元数据JSON"`
	MessageIdempotencyKey *string `json:"messageIdempotencyKey,omitempty" gorm:"column:message_idempotency_key;type:varchar(180);comment:Canvas direct-message idempotency key"`

	// Critique-loop fields (P0 #3, migration 20260616). UserRating
	// is set by the 👍/👎 buttons in the message bubble; UserFeedback
	// is the optional text the user enters when they thumbs-down.
	// The next-turn preflight composer reads the most-recent
	// thumbs-down + feedback on this thread and injects it as a
	// <previous-critique> block, so the agent reads the correction
	// directly instead of guessing from the next user turn.
	//
	// Valid UserRating values: -1 (thumbs-down), 0 (unrated), 1
	// (thumbs-up). Enforced at the repo write boundary.
	UserRating   int8   `json:"userRating" gorm:"column:user_rating;type:tinyint;default:0;not null;comment:用户评分: -1=差评 0=未评 1=好评"`
	UserFeedback string `json:"userFeedback,omitempty" gorm:"column:user_feedback;type:text;comment:用户的修改反馈，配合差评一起写入"`
}

// MarshalJSON customizes JSON output to include alias fields for admin panel
func (cm *ChatMessage) MarshalJSON() ([]byte, error) {
	type Alias ChatMessage
	return json.Marshal(&struct {
		*Alias
		UserTextFull string `json:"userTextFull"`
		AITextFull   string `json:"aiTextFull"`
	}{
		Alias:        (*Alias)(cm),
		UserTextFull: cm.UserText,
		AITextFull:   cm.AIText,
	})
}

func (ChatMessage) TableName() string {
	return "w_workagent_message"
}

// ChatModeType represents chat mode constants
type ChatModeType string

const (
	// Chat modes for AIChat
	ChatModeAgent ChatModeType = "agent" // Unified agent mode with structured message system

	// Deprecated modes (kept for backward compatibility)
	ChatModeAssistant ChatModeType = "assistant" // Deprecated: Use ChatModeAgent instead
)

// ChatMessageResponse represents the response structure for API
type ChatMessageResponse struct {
	Id                uint      `json:"id"`
	UID               int       `json:"uid"`
	UUID              string    `json:"uuid"`
	ThreadID          int       `json:"threadId"`
	UserText          string    `json:"userText"`
	AIText            string    `json:"aiText"`
	TotalPrompt       string    `json:"totalPrompt,omitempty"` // omitempty to avoid sending large prompts in API responses unless needed
	Model             string    `json:"model"`
	UseTokens         int       `json:"useTokens"`
	PromptTokens      int       `json:"promptTokens"`
	CompletionTokens  int       `json:"completionTokens"`
	ContextTokens     int       `json:"contextTokens"`
	UseFiles          string    `json:"useFiles"`
	ChatMode          string    `json:"chatMode"`
	ContentType       string    `json:"contentType,omitempty"`
	StructuredContent string    `json:"structuredContent,omitempty"`
	Metadata          string    `json:"metadata,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// ToResponse converts ChatMessage to response format
func (cm *ChatMessage) ToResponse() ChatMessageResponse {
	return ChatMessageResponse{
		Id:                cm.Id,
		UID:               cm.UID,
		UUID:              cm.UUID,
		ThreadID:          cm.ThreadID,
		UserText:          cm.UserText,
		AIText:            cm.AIText,
		TotalPrompt:       cm.TotalPrompt,
		Model:             cm.Model,
		UseTokens:         cm.UseTokens,
		PromptTokens:      cm.PromptTokens,
		CompletionTokens:  cm.CompletionTokens,
		ContextTokens:     cm.ContextTokens,
		UseFiles:          cm.UseFiles,
		ChatMode:          cm.ChatMode,
		ContentType:       cm.ContentType,
		StructuredContent: cm.StructuredContent,
		Metadata:          cm.Metadata,
		CreatedAt:         cm.CreatedAt,
		UpdatedAt:         cm.UpdatedAt,
	}
}

// IsAgent checks if this message is in Agent mode
func (cm *ChatMessage) IsAgent() bool {
	return cm.ChatMode == string(ChatModeAgent)
}

// IsAssistant checks if this message is in Assistant mode
// Deprecated: Use IsAgent() instead as assistant mode has been unified into agent mode
func (cm *ChatMessage) IsAssistant() bool {
	return cm.ChatMode == string(ChatModeAssistant)
}

// getDefaultChatModel returns the default chat model based on configuration
func getDefaultChatModel() string {
	return llm.GetModel()
}

// SetTokenUsage sets token usage information
func (cm *ChatMessage) SetTokenUsage(promptTokens, completionTokens int) {
	cm.PromptTokens = promptTokens
	cm.CompletionTokens = completionTokens
	cm.UseTokens = promptTokens + completionTokens
}

// SetTotalPrompt sets the complete conversation prompt used for this chat request
func (cm *ChatMessage) SetTotalPrompt(totalPrompt string) {
	cm.TotalPrompt = totalPrompt
}
