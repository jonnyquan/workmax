package workagent

import (
	"time"

	"server/globals"
	workagentService "server/service/tools/workagent"
)

// ChatMode constants for request routing and execution
const (
	// ChatModeAgent is the unified chat mode with structured message system
	ChatModeAgent string = "agent"
)

// AIChatApiNew represents the AIChat API controller
type AIChatApiNew struct {
	threadLifecycleService *workagentService.ThreadLifecycleService
	fileService            *workagentService.FileService
}

// NewAIChatApiNew creates a new AIChatApiNew instance
func NewAIChatApiNew() *AIChatApiNew {
	return &AIChatApiNew{
		threadLifecycleService: workagentService.NewThreadLifecycleService(globals.GraDBs["system"]),
		fileService:            workagentService.GetFileService(),
	}
}

// ChatStreamRequest represents the streaming chat request structure
type ChatStreamRequest struct {
	Messages       []ChatMessage       `json:"messages"`
	ChatMode       string              `json:"chatMode,omitempty"` // Chat mode: "normal" or "agent" (replaces old "mode" field)
	ConversationID string              `json:"conversationId"`
	Files          []FileInfo          `json:"files,omitempty"`
	Model          string              `json:"model,omitempty"`
	MaxTokens      int                 `json:"max_tokens,omitempty"`
	Stream         bool                `json:"stream,omitempty"`
	Lang           string              `json:"lang,omitempty"`
	Metadata       ChatRequestMetadata `json:"metadata,omitempty"`
}

// FileReference represents a file reference from user input
type FileReference struct {
	Type         string            `json:"type"`                   // "name", "index", "all", "recent"
	Value        string            `json:"value"`                  // filename or index
	Raw          string            `json:"raw"`                    // original text (e.g., "@sales.xlsx")
	StartIndex   int               `json:"startIndex"`             // position in content
	EndIndex     int               `json:"endIndex"`               // position in content
	ResolvedFile *ResolvedFileInfo `json:"resolvedFile,omitempty"` // resolved file info
}

// ResolvedFileInfo represents resolved file information from frontend
type ResolvedFileInfo struct {
	Id           string `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"originalName"`
	Size         int64  `json:"size"`
	Type         string `json:"type"`
	Path         string `json:"path"`
	UploadedAt   string `json:"uploadedAt"`
	Source       string `json:"source"` // "upload", "message", etc.
}

// ChatRequestMetadata represents metadata for chat requests
type ChatRequestMetadata struct {
	ThreadID string `json:"threadId,omitempty"`

	// Agent Mode field - controls which mode-specific prompt to use.
	// Must be one of allowedAgentModes (conversation_api.go); unknown
	// values are normalized to "" by determineAgentMode and fall back
	// to DefaultAgentMode ("ppt"). This Server allowlist is authoritative;
	// Desktop consumes it through the Agent catalog contract, pinned by
	// agent_mode_contract_test.go.
	AgentMode string   `json:"agentMode,omitempty"` // e.g. "ppt", "flashCard", "socialAd" (15 values; see allowedAgentModes)
	ModelTier string   `json:"modelTier,omitempty"` // "work-pro", "work-plus"
	PPTSpec   *PPTSpec `json:"pptSpec,omitempty"`

	// Optional source preference for multi-input PPT generation.
	SourcePriority []string `json:"sourcePriority,omitempty"` // e.g. ["text","doc","data","asset"]

	// File reference system fields
	FileReferences  []FileReference    `json:"fileReferences,omitempty"`
	ReferencedFiles []ResolvedFileInfo `json:"referencedFiles,omitempty"`
	Timestamp       string             `json:"timestamp,omitempty"`
	FileCount       int                `json:"fileCount,omitempty"`

	// PlanMode (A3, 2026-05-16) — when true the system-prompt
	// composer appends the propose-plan-then-execute protocol so
	// the agent emits a plan card and waits for the user to
	// approve before doing work. Matches the canvas surface's
	// per-request planMode flag (server/service/tools/workagent/
	// surfaces/canvas/types.go::CanvasAgentContext.PlanMode) so
	// the FE can reuse the same wire shape.
	//
	// Per-request rather than persisted: matches canvas, and avoids
	// the "I forgot Plan mode was on three weeks ago" failure mode.
	// FE keeps the toggle in MessageInput local state so a refresh
	// resets to off.
	PlanMode bool `json:"planMode,omitempty"`
}

// PPTSpec captures structured PPT generation requirements from frontend.
type PPTSpec struct {
	Topic        string   `json:"topic,omitempty"`
	Audience     string   `json:"audience,omitempty"`
	Goal         string   `json:"goal,omitempty"`
	SlideCount   int      `json:"slideCount,omitempty"`
	Language     string   `json:"language,omitempty"`
	Tone         string   `json:"tone,omitempty"`
	Style        string   `json:"style,omitempty"`
	BrandColors  []string `json:"brandColors,omitempty"`
	TemplateRef  string   `json:"templateRef,omitempty"`
	OutputFormat string   `json:"outputFormat,omitempty"` // Expected: pptx
}

// ChatMessage represents a single chat message
type ChatMessage struct {
	Id        string     `json:"id"`
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Timestamp time.Time  `json:"timestamp"`
	Files     []FileInfo `json:"files,omitempty"`
}

// FileInfo represents file information
type FileInfo struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Type        string `json:"type"`
	FilePath    string `json:"filePath"`
	Path        string `json:"path"`       // Alternative field name from frontend
	Converted   bool   `json:"converted"`  // 是否发生了格式转换
	FileSource  string `json:"fileSource"` // File source: "upload", "output", "system"
	FileRole    string `json:"fileRole,omitempty"`
	DownloadURL string `json:"downloadUrl,omitempty"`
	PreviewURL  string `json:"previewUrl,omitempty"`
}

// ConversationCreateRequest represents conversation creation request
type ConversationCreateRequest struct {
	Title     string `json:"title"`
	AgentMode string `json:"agentMode,omitempty"`
	// ProjectID optionally binds the new thread to a platform Project
	// (Plan-A Phase A3). When set, every agent turn on this thread
	// auto-scopes project-aware tool calls (lookup_asset, future
	// project-scoped helpers) to that project unless the per-call
	// input overrides. 0 / omitted leaves the thread project-
	// unbound (legacy uid-only scope).
	ProjectID uint `json:"projectId,omitempty"`
}

// ConversationResponse represents conversation response
type ConversationResponse struct {
	Id        string `json:"id"`
	ThreadID  int    `json:"thread_id"`
	UUID      string `json:"uuid,omitempty"`
	ProjectID uint   `json:"projectId,omitempty"`
}

// updateThreadStatistics updates thread statistics (message count, preview, file count).
//
// Three counts come from three repos (Message / File / Thread) so this
// helper stays a thin orchestrator — no inline GORM. SetStatistics
// invalidates the ThreadCache so the next turn re-reads the fresh row;
// without the invalidate, per-turn stats stay frozen for up to 5 min.
func (api *AIChatApiNew) updateThreadStatistics(threadID uint) error {
	msgRepo := workagentService.DefaultMessageRepository()

	messageCount, err := msgRepo.CountByThread(threadID)
	if err != nil {
		return err
	}

	preview, err := msgRepo.LoadLatestAIPreviewByThread(threadID)
	if err != nil {
		return err
	}
	// Truncate to 200 chars for storage. Repo returns the raw column
	// (it's a generic helper, not chat-list-specific) and trimming is
	// the caller's responsibility.
	if len([]rune(preview)) > 200 {
		preview = string([]rune(preview)[:200])
	}

	fileCount, err := workagentService.GetFileService().CountFilesByThread(threadID)
	if err != nil {
		return err
	}

	return workagentService.DefaultThreadRepository().SetStatistics(threadID, workagentService.ThreadStatistics{
		MessageCount: messageCount,
		Preview:      preview,
		FileCount:    fileCount,
		UpdatedAt:    time.Now(),
	})
}

// capitalizeASCII upper-cases the first byte of a string. Used in place
// of strings.Title for inputs that are guaranteed ASCII (validated enums
// like "user"/"assistant"), so we don't pull in golang.org/x/text/cases
// for a single-letter transform. strings.Title is deprecated and has
// documented Unicode word-boundary bugs.
func capitalizeASCII(s string) string {
	if s == "" {
		return s
	}
	if c := s[0]; c >= 'a' && c <= 'z' {
		return string(c-'a'+'A') + s[1:]
	}
	return s
}
