package workagent

import (
	"path/filepath"
	"server/globals"
	"strings"
	"time"
)

// FileSource represents the source of a file
type FileSource string

const (
	FileSourceUpload FileSource = "upload" // User uploaded file
	FileSourceOutput FileSource = "output" // AI Agent generated output
	FileSourceSystem FileSource = "system" // System generated file
)

// FileTypeValue represents semantic file type for agent outputs
type FileTypeValue string

const (
	// Essay Writer Agent file types
	FileTypeEssayContent FileTypeValue = "essay_content" // Generated essay HTML content
	FileTypeEssayForm    FileTypeValue = "essay_form"    // Essay form data JSON
	FileTypeEssayOutline FileTypeValue = "essay_outline" // Essay outline JSON

	// Scholar Agent file types
	FileTypeScholarResearch FileTypeValue = "scholar_research" // Research report HTML
	FileTypeScholarOverview FileTypeValue = "scholar_overview" // Literature overview markdown
	FileTypeScholarForm     FileTypeValue = "scholar_form"     // Scholar form data JSON

	// AI Humanizer Agent file types
	FileTypeHumanizerContent FileTypeValue = "humanizer_content" // Humanized content HTML
	FileTypeHumanizerForm    FileTypeValue = "humanizer_form"    // Humanizer form data JSON

	// Write Agent file types
	FileTypeWriteagentOutput FileTypeValue = "workagent_output" // Write agent generated output

	// General file types
	FileTypeUpload  FileTypeValue = "upload"  // User uploaded file
	FileTypeGeneral FileTypeValue = "general" // General output file
)

// ThreadFile 线程文件关联模型
type ThreadFile struct {
	globals.GraMODEL
	UID           int        `json:"uid" gorm:"column:uid;not null;index;comment:用户ID"`
	ThreadID      uint       `json:"threadId" gorm:"column:thread_id;not null;index;comment:线程ID"`
	MessageID     uint       `json:"messageId" gorm:"column:message_id;default:0;comment:关联的消息ID"`
	FileName      string     `json:"fileName" gorm:"column:file_name;type:varchar(500);not null;comment:原始文件名(显示用)"`
	DisplayName   string     `json:"displayName" gorm:"column:display_name;type:varchar(500);default:'';comment:用户友好显示名称"`
	FileSize      uint64     `json:"fileSize" gorm:"column:file_size;default:0;comment:文件大小(bytes)"`
	FileType      string     `json:"fileType" gorm:"column:file_type;type:varchar(100);default:'';index;comment:文件类型"`
	MimeType      string     `json:"mimeType" gorm:"column:mime_type;type:varchar(200);default:'';comment:MIME类型"`
	FilePath      string     `json:"filePath" gorm:"column:file_path;type:varchar(1000);default:'';comment:文件存储路径(相对路径)"`
	FileSource    FileSource `json:"fileSource" gorm:"column:file_source;type:varchar(20);default:'upload';index;comment:文件来源(upload/output/system)"`
	Description   string     `json:"description" gorm:"column:description;type:text;comment:文件描述"`
	FileHash      string     `json:"fileHash" gorm:"column:file_hash;type:varchar(64);default:'';index;comment:文件哈希(用于去重)"`
	GlobalAssetID uint       `json:"globalAssetId,omitempty" gorm:"column:global_asset_id;type:bigint unsigned;not null;default:0;index:idx_workagent_thread_file_global_asset;comment:可选的 w_global_asset.id 桥接"`
	LastSyncedAt  *time.Time `json:"lastSyncedAt" gorm:"column:last_synced_at;comment:最后同步时间"`
	ExistsOnDisk  bool       `json:"existsOnDisk" gorm:"column:exists_on_disk;default:true;comment:文件是否存在于磁盘"`
}

// TableName 设置表名
func (ThreadFile) TableName() string {
	return "w_workagent_thread_file"
}

// ThreadFileRequest 添加文件到线程的请求.
//
// FileHash is the optional MD5 of the file's bytes. When the upload
// path can compute it cheaply (file is already on disk after
// ProcessUploadedFileWithCustomPath), passing it lets AddFile detect
// in-place replacements in the fast-path: same (file_name, file_path)
// tuple but new bytes ⇒ refresh the row instead of returning the stale
// existing one. Without this, the agent_files_context cache fingerprint
// (which keys off ThreadFile.FileHash) keeps serving the previous
// system prompt to Claude even though the user uploaded new bytes.
type ThreadFileRequest struct {
	UID         int        `json:"uid" binding:"required"`
	ThreadID    uint       `json:"threadId" binding:"required"`
	MessageID   uint       `json:"messageId"`
	FileName    string     `json:"fileName" binding:"required"`
	DisplayName string     `json:"displayName"`
	FileSize    uint64     `json:"fileSize"`
	FileType    string     `json:"fileType"`
	MimeType    string     `json:"mimeType"`
	FilePath    string     `json:"filePath"`
	FileHash    string     `json:"fileHash"`
	FileSource  FileSource `json:"fileSource"`
	Description string     `json:"description"`
}

// ThreadFileResponse 线程文件响应
type ThreadFileResponse struct {
	Id           uint       `json:"id"`
	UID          int        `json:"uid"`
	ThreadID     uint       `json:"threadId"`
	FileName     string     `json:"fileName"`
	DisplayName  string     `json:"displayName"`
	FileSize     uint64     `json:"fileSize"`
	FileType     string     `json:"fileType"`
	MimeType     string     `json:"mimeType"`
	FilePath     string     `json:"filePath"`
	FileSource   FileSource `json:"fileSource"`
	Description  string     `json:"description"`
	IsConverted  bool       `json:"isConverted"`
	ExistsOnDisk bool       `json:"existsOnDisk"`
	DownloadURL  string     `json:"downloadUrl,omitempty"`
	PreviewURL   string     `json:"previewUrl,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// IsConverted reports whether the stored file was silently converted
// from .xls (or an Excel-typed file with no extension) to .xlsx during
// upload. The signal: file_path ends in .xlsx but the original
// file_name does not. The Excel-type fallback handles the
// "no extension on the original name" case where the only hint we
// have that we're looking at an Excel file is FileType.
func (tf *ThreadFile) IsConverted() bool {
	pathIsXlsx := strings.HasSuffix(strings.ToLower(tf.FilePath), ".xlsx")
	if !pathIsXlsx {
		return false
	}
	nameLower := strings.ToLower(tf.FileName)
	if strings.HasSuffix(nameLower, ".xlsx") {
		return false
	}
	if strings.HasSuffix(nameLower, ".xls") {
		return true
	}
	// No extension on original name — fall back to FileType.
	if filepath.Ext(nameLower) == "" {
		ft := strings.ToLower(tf.FileType)
		return strings.Contains(ft, "excel") || ft == "application/vnd.ms-excel"
	}
	return false
}

// ToResponse 转换为响应格式
func (tf *ThreadFile) ToResponse() ThreadFileResponse {
	displayName := tf.DisplayName
	if displayName == "" {
		// 如果没有设置显示名称，使用原始文件名
		displayName = tf.FileName
	}

	// Default FileSource to "upload" if not set
	fileSource := tf.FileSource
	if fileSource == "" {
		fileSource = FileSourceUpload
	}

	return ThreadFileResponse{
		Id:           tf.Id,
		UID:          tf.UID,
		ThreadID:     tf.ThreadID,
		FileName:     tf.FileName,
		DisplayName:  displayName,
		FileSize:     tf.FileSize,
		FileType:     tf.FileType,
		MimeType:     tf.MimeType,
		FilePath:     tf.FilePath,
		FileSource:   fileSource,
		Description:  tf.Description,
		IsConverted:  tf.IsConverted(),
		ExistsOnDisk: tf.ExistsOnDisk,
		CreatedAt:    tf.CreatedAt,
		UpdatedAt:    tf.UpdatedAt,
	}
}

// RemoveFileRequest 从线程移除文件的请求
type RemoveFileRequest struct {
	UID      int  `json:"uid" binding:"required"`
	ThreadID uint `json:"threadId" binding:"required"`
	FileID   uint `json:"fileId" binding:"required"` // 改为数据库ID
}

// RemoveFileResponse 移除文件响应
type RemoveFileResponse struct {
	Success   bool      `json:"success"`
	ThreadID  uint      `json:"threadId"`
	FileID    uint      `json:"fileId"` // 改为数据库ID
	RemovedAt time.Time `json:"removedAt"`
	Message   string    `json:"message"`
}

// SetDefaultFile requests and responses removed - Default functionality no longer needed
