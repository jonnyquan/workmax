package workagent

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"server/globals"
	"server/model/common/response"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

const workAgentUploadMultipartOverhead = 2 * 1024 * 1024
const workAgentHTMLValidationMaxBytes = 512 * 1024

// ContentDispositionAttachment formats the Content-Disposition header
// for a download response, encoding non-ASCII characters per RFC 5987
// so CJK / emoji / accented filenames render correctly across all
// major browsers (Chrome / Firefox / Safari / Edge).
//
// The legacy `filename="..."` value carries an ASCII-safe fallback
// (any non-ASCII rune dropped) so old browsers still get something;
// modern ones prefer `filename*=UTF-8”<percent-encoded>` and that
// wins in their parsing order. We also strip `"` / CR / LF from the
// fallback to defend against header injection if a stored filename
// ever contained them — past sanitizers have shipped subtle holes
// (zero-width joiners, double-decoded sequences) and a future
// regression here would otherwise let a crafted filename set
// arbitrary headers.
//
// Exported so the router-layer workspace-file serving (which lives in
// a different package) can apply the same encoding when forcing
// attachment downloads on active-content file types.
func ContentDispositionAttachment(filename string) string {
	// ASCII fallback: drop any byte > 0x7E and the control set, plus
	// the few bytes that can break the quoted-string syntax.
	asciiFallback := strings.Map(func(r rune) rune {
		if r > 0x7E || r < 0x20 || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, filename)
	if asciiFallback == "" {
		asciiFallback = "download"
	}
	// RFC 5987 percent-encoding for the UTF-8 native name.
	encoded := url.PathEscape(filename)
	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=UTF-8''%s", asciiFallback, encoded)
}

// UploadFile handles file upload for conversations
func (api *AIChatApiNew) UploadFile(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	// Add file size limits (50MB for regular files, 100MB for premium users)
	const maxFileSize = 50 * 1024 * 1024         // 50MB default
	const maxPremiumFileSize = 100 * 1024 * 1024 // 100MB for premium

	// Check user premium status. The previous shape did a direct
	// `globals.GraDBs["system"].First(&user, uid)` from the api
	// package — same cross-table coupling we fenced for the share
	// endpoints with LoadOwnerNickname. IsUserPremium puts the
	// w_user schema reference behind one named function.
	isPremium := workagentService.IsUserPremium(uid)

	sizeLimit := maxFileSize
	if isPremium {
		sizeLimit = maxPremiumFileSize
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(sizeLimit+workAgentUploadMultipartOverhead))

	conversationId := c.Request.FormValue("conversationId")
	source := c.Request.FormValue("source") // "message", "reference-selector", or legacy "floating"
	if source == "" {
		source = "message"
	}
	if strings.TrimSpace(conversationId) == "" {
		response.FailWithMessage("Please select or create a conversation before uploading files", c)
		return
	}

	// Get uploaded file
	file, err := c.FormFile("file")
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to get uploaded file: %v", err))
		response.FailWithMessage("Failed to get uploaded file", c)
		return
	}

	if file.Size > int64(sizeLimit) {
		globals.Warn(fmt.Sprintf("File size %d exceeds limit %d for user %d (premium: %v)",
			file.Size, sizeLimit, uid, isPremium))
		response.FailWithMessage(fmt.Sprintf("File size exceeds limit (%dMB for %s users)",
			sizeLimit/(1024*1024), map[bool]string{true: "premium", false: "regular"}[isPremium]), c)
		return
	}

	globals.Info(fmt.Sprintf("Processing file upload: %s (%d bytes) for user %d", file.Filename, file.Size, uid))

	// Save file with conversion support
	fileInfo, err := api.saveUploadedFileWithConversion(c, file, conversationId, uid)
	if err != nil {
		response.FailWithMessage("Failed to save file", c)
		return
	}

	// If conversationId provided, add files to ChatThread and vectorize
	if conversationId != "" {
		threadIdUint, err := strconv.ParseUint(conversationId, 10, 32)
		if err != nil {
			globals.Warn(fmt.Sprintf("Invalid conversation Id: %v", err))
		} else {
			// Create ThreadFileRequest using new design
			threadFileRequest := workagentModel.ThreadFileRequest{
				UID:         int(uid),
				ThreadID:    uint(threadIdUint),
				FileName:    fileInfo.Name,
				DisplayName: fileInfo.Name, // Set display name for user-friendly display
				FileSize:    uint64(fileInfo.Size),
				FileType:    fileInfo.Type,
				FilePath:    fileInfo.FilePath,
			}

			// Use FileService to add file
			threadFileResponse, err := api.fileService.AddFile(threadFileRequest)
			if err != nil {
				globals.Error(fmt.Sprintf("Failed to add file to thread with vectorization: %v", err))
				// Fallback to basic add method
				chatThread, fallbackErr := api.getChatThread(conversationId, uid)
				if fallbackErr != nil {
					globals.Error(fmt.Sprintf("Failed to get chat thread for fallback: %v", fallbackErr))
				} else {
					fileInfos := []FileInfo{{
						Id:        fileInfo.Id,
						Name:      fileInfo.Name,
						Size:      fileInfo.Size,
						Type:      fileInfo.Type,
						FilePath:  fileInfo.FilePath,
						Converted: false,
					}}
					fallbackErr = api.updateThreadFiles(chatThread, fileInfos, source)
					if fallbackErr != nil {
						globals.Error(fmt.Sprintf("Fallback method also failed to update thread files: %v", fallbackErr))
					} else {
						globals.Info(fmt.Sprintf("Successfully added file %s to thread %d using fallback method", fileInfo.Name, threadIdUint))
					}
				}
			} else {
				globals.Info(fmt.Sprintf("Successfully added file %s to thread %d with ID %d",
					fileInfo.Name, threadIdUint, threadFileResponse.Id))
				// 🔥 REMOVED: Smart Suggestions auto-generation (handled by frontend now)
				// api.smartSuggestionsTrigger.OnFileUpload(c.Request.Context(), conversationId, uid, threadFileResponse.Id)

				// Update thread statistics after file upload
				if err := api.updateThreadStatistics(uint(threadIdUint)); err != nil {
					globals.Error(fmt.Sprintf("Failed to update thread statistics after file upload: %v", err))
				}
			}
		}
	}

	safePath := api.sanitizeFilePath(fileInfo.FilePath)

	response.OkWithData(map[string]interface{}{
		"id":        fileInfo.Id,
		"url":       safePath,
		"filePath":  safePath,
		"name":      fileInfo.Name,
		"size":      fileInfo.Size,
		"converted": fileInfo.Converted,
	}, c)
}

// GetConversationFiles gets files associated with a conversation
func (api *AIChatApiNew) GetConversationFiles(c *gin.Context) {
	// Support two parameter names for compatibility with different paths
	conversationId := c.Param("id")
	if conversationId == "" {
		conversationId = c.Param("threadId")
	}

	threadIdUint, err := strconv.ParseUint(conversationId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}

	// Get user ID
	uid := utils.GetUserID(c)

	// Use FileService to get file list
	threadFiles, err := api.fileService.GetAllFiles(int(uid), uint(threadIdUint))
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to get thread files: %v", err))
		response.FailWithMessage("Failed to get file list", c)
		return
	}

	// Convert ThreadFileResponse to frontend FileInfo format and check file existence
	files := make([]FileInfo, 0, len(threadFiles))
	for _, tf := range threadFiles {
		fullPath := api.resolveWorkspaceFilePath(tf.FilePath)

		// The resolver returns "" when the stored path escapes the workspace
		// (absolute path or ".." traversal). Don't treat that as "file not
		// found" — os.Stat("") returns ENOENT, which would have deleted the
		// DB record below. Skip the record from the listing instead so a
		// path-validation bug can't silently purge legitimate rows.
		if fullPath == "" {
			globals.Warn(fmt.Sprintf("Skipping file with unresolvable path (ID: %d, Thread: %d, stored: %q)",
				tf.Id, threadIdUint, tf.FilePath))
			continue
		}

		// Check if file exists on disk
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			globals.Warn(fmt.Sprintf("File not found on disk, deleting record: %s (ID: %d, Thread: %d)", fullPath, tf.Id, threadIdUint))

			// Use FileService.RemoveFile so the orphan cleanup wraps
			// the ThreadFile delete AND the matching user_asset_ledger
			// delete in a single transaction. The previous shape
			// deleted only the ThreadFile row, leaving an orphan
			// ledger entry pointing at a file_id that no longer
			// existed — visible on the asset panel as a "ghost" row
			// whose download/preview both 404'd. (Note: this on-read
			// cleanup is the only orphan-reaper actually wired up; the
			// dedicated FileService.DeleteOrphanedRecords helper has
			// zero api-layer callers.)
			if removeErr := api.fileService.RemoveFile(int(uid), uint(threadIdUint), tf.Id); removeErr != nil {
				globals.Error(fmt.Sprintf("Failed to remove orphaned file record (id=%d): %v", tf.Id, removeErr))
			} else {
				globals.Info(fmt.Sprintf("Successfully removed orphaned file record: ID %d", tf.Id))
			}
			continue // Skip files that don't exist on disk
		}

		fileInfo := FileInfo{
			Id:         strconv.FormatUint(uint64(tf.Id), 10),
			Name:       tf.DisplayName, // Use display name for user-friendly display
			Size:       int64(tf.FileSize),
			Type:       tf.FileType,
			FilePath:   fullPath,              // Absolute path for downloads/preview
			Path:       tf.FilePath,           // Original relative/absolute path from database for Agent
			Converted:  tf.IsConverted,        // Map IsConverted to Converted field
			FileSource: string(tf.FileSource), // File source: upload/output/system
		}

		downloadURL := api.sanitizeFilePath(fullPath)
		previewURL := downloadURL
		if isPresentationPath(fullPath) {
			if previewPDFPath, convErr := ensurePresentationPreviewPDF(fullPath); convErr == nil && previewPDFPath != "" {
				previewURL = api.sanitizeFilePath(previewPDFPath)
			} else if shouldLogPreviewConversionError(convErr) {
				globals.Warn(fmt.Sprintf("[GetConversationFiles] PPT preview conversion skipped for %s: %v", fullPath, convErr))
			}
		}

		fileInfo.DownloadURL = downloadURL
		fileInfo.PreviewURL = previewURL
		files = append(files, api.sanitizeFileInfo(fileInfo))
	}

	response.OkWithData(files, c)
}

// GetConversationArtifacts returns a P1 artifact-first view over the
// same thread files that GetConversationFiles serves. It is additive:
// no schema migration, no replacement of the legacy files endpoint.
func (api *AIChatApiNew) GetConversationArtifacts(c *gin.Context) {
	conversationId := c.Param("id")
	if conversationId == "" {
		conversationId = c.Param("threadId")
	}

	threadIdUint, err := strconv.ParseUint(conversationId, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}

	uid := utils.GetUserID(c)
	artifacts, err := workagentService.ListArtifactViewsByThread(nil, int(uid), uint(threadIdUint))
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to get thread artifacts: %v", err))
		response.FailWithMessage("Failed to get artifact list", c)
		return
	}
	for i := range artifacts {
		api.enrichArtifactURLs(&artifacts[i])
	}

	response.OkWithData(gin.H{
		"items": artifacts,
		"count": len(artifacts),
	}, c)
}

func (api *AIChatApiNew) enrichArtifactURLs(artifact *workagentService.ArtifactView) {
	if artifact == nil || strings.TrimSpace(artifact.FilePath) == "" {
		return
	}
	fullPath := api.resolveWorkspaceFilePath(artifact.FilePath)
	if fullPath == "" {
		return
	}
	downloadURL := api.sanitizeFilePath(fullPath)
	previewURL := downloadURL
	if isPresentationPath(fullPath) {
		if previewPDFPath, convErr := ensurePresentationPreviewPDF(fullPath); convErr == nil && previewPDFPath != "" {
			previewURL = api.sanitizeFilePath(previewPDFPath)
		} else if shouldLogPreviewConversionError(convErr) {
			globals.Warn(fmt.Sprintf("[GetConversationArtifacts] PPT preview conversion skipped for %s: %v", fullPath, convErr))
		}
	}
	artifact.DownloadURL = downloadURL
	artifact.PreviewURL = previewURL
	api.enrichArtifactHTMLValidation(artifact, fullPath)
}

func (api *AIChatApiNew) enrichArtifactHTMLValidation(artifact *workagentService.ArtifactView, fullPath string) {
	if artifact == nil || artifact.PreviewType != "html" || strings.TrimSpace(fullPath) == "" {
		return
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, workAgentHTMLValidationMaxBytes+1))
	if err != nil {
		return
	}
	result := workagentService.ValidateHTMLArtifactContent(string(raw))
	if artifact.HTMLValidation != nil && len(artifact.HTMLValidation.PreviewDiagnostics) > 0 {
		result.PreviewDiagnostics = artifact.HTMLValidation.PreviewDiagnostics
		result.Issues = append(result.Issues, artifact.HTMLValidation.PreviewDiagnostics...)
		result.IssueCount = len(result.Issues)
		for _, issue := range artifact.HTMLValidation.PreviewDiagnostics {
			if issue.Severity == "error" && result.Status != workagentService.ArtifactHTMLValidationBlock {
				result.Status = workagentService.ArtifactHTMLValidationWarn
			}
		}
	}
	if len(raw) > workAgentHTMLValidationMaxBytes {
		result.Issues = append(result.Issues, workagentService.ArtifactHTMLValidationIssue{
			Code:     "validation_truncated",
			Severity: "warn",
			Message:  "HTML artifact exceeded static validation size budget",
		})
		result.IssueCount = len(result.Issues)
		if result.Status == workagentService.ArtifactHTMLValidationPass {
			result.Status = workagentService.ArtifactHTMLValidationWarn
		}
	}
	artifact.HTMLValidation = &result
	artifact.HTMLExportPlan = workagentService.BuildHTMLExportPlan(*artifact)
	artifact.HTMLBrowserValidationPlan = workagentService.BuildHTMLBrowserValidationPlan(*artifact)
}

// DownloadFile handles file download
func (api *AIChatApiNew) DownloadFile(c *gin.Context) {
	fileIDStr := c.Param("id")
	if fileIDStr == "" {
		response.FailWithMessage("File ID is required", c)
		return
	}

	// Parse file ID as uint
	fileID, err := strconv.ParseUint(fileIDStr, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid file ID", c)
		return
	}

	uid := utils.GetUserID(c)

	// LoadFileForOwner bakes the uid predicate into the WHERE — same
	// CWE-639 defence as the thread-side LoadByIDForOwner. Drops 12
	// lines of inline GORM that DownloadFile / PreviewFile / DeleteFile
	// each repeated.
	threadFilePtr, err := workagentService.GetFileService().LoadFileForOwner(uint(fileID), uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("File not found or access denied", c)
		} else {
			response.FailWithMessage("Database error", c)
		}
		return
	}
	threadFile := *threadFilePtr

	fullDownloadPath := api.resolveWorkspaceFilePath(threadFile.FilePath)
	if fullDownloadPath == "" {
		// Empty from the resolver means the stored path escaped workspaceRoot
		// (absolute path or ".." traversal). Treat as a missing file so the
		// reason isn't leaked to the caller.
		globals.Warn(fmt.Sprintf("Refusing to serve out-of-workspace path for file %d: %q", threadFile.Id, threadFile.FilePath))
		response.FailWithMessage("File not found on disk", c)
		return
	}
	if _, err := os.Stat(fullDownloadPath); os.IsNotExist(err) {
		response.FailWithMessage("File not found on disk", c)
		return
	}

	c.Header("Content-Disposition", ContentDispositionAttachment(threadFile.FileName))
	c.Header("Content-Type", "application/octet-stream")
	c.File(fullDownloadPath)
}

// PreviewFile handles file preview by returning file metadata and URL for client-side rendering
func (api *AIChatApiNew) PreviewFile(c *gin.Context) {
	fileIDStr := c.Param("id")
	if fileIDStr == "" {
		response.FailWithMessage("File ID is required", c)
		return
	}

	// Parse file ID as uint
	fileID, err := strconv.ParseUint(fileIDStr, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid file ID", c)
		return
	}

	uid := utils.GetUserID(c)

	// Same uid-scoped owner load as DownloadFile — see that handler's
	// note on LoadFileForOwner.
	threadFilePtr, err := workagentService.GetFileService().LoadFileForOwner(uint(fileID), uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("File not found or access denied", c)
		} else {
			response.FailWithMessage("Database error", c)
		}
		return
	}
	threadFile := *threadFilePtr

	previewPath := api.resolveWorkspaceFilePath(threadFile.FilePath)
	if previewPath == "" {
		globals.Warn(fmt.Sprintf("Refusing to preview out-of-workspace path for file %d: %q", threadFile.Id, threadFile.FilePath))
		response.FailWithMessage("File not found on disk", c)
		return
	}
	if _, err := os.Stat(previewPath); os.IsNotExist(err) {
		response.FailWithMessage("File not found on disk", c)
		return
	}

	// Return file metadata for client-side processing
	// Frontend will handle Excel/CSV parsing using xlsx library
	safePath := api.sanitizeFilePath(previewPath)

	previewData := map[string]interface{}{
		"fileId":   threadFile.Id,
		"fileName": threadFile.FileName,
		"fileSize": threadFile.FileSize,
		"fileType": threadFile.FileType,
		"filePath": safePath,
		"fileUrl":  safePath, // Frontend will use this to download and parse
	}

	response.OkWithDetailed(previewData, "File metadata loaded successfully", c)
}

// DeleteFile handles file deletion
func (api *AIChatApiNew) DeleteFile(c *gin.Context) {
	fileIDStr := c.Param("id")
	if fileIDStr == "" {
		response.FailWithMessage("File ID is required", c)
		return
	}

	// Parse file ID as uint
	fileID, err := strconv.ParseUint(fileIDStr, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid file ID", c)
		return
	}

	// Get user ID from JWT token
	uid := utils.GetUserID(c)

	// Same uid-scoped owner load — see DownloadFile's note. errors.Is
	// instead of `==` because the repo wraps the underlying gorm
	// error in some paths (uid==0 short-circuit).
	threadFilePtr, lookupErr := workagentService.GetFileService().LoadFileForOwner(uint(fileID), uid)
	if lookupErr != nil {
		if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			response.FailWithMessage("File not found", c)
		} else {
			// Log the actual driver error server-side; don't leak
			// schema details / connection info to the API caller.
			globals.Error(fmt.Sprintf("[DeleteFile] DB lookup failed for fileID=%d uid=%d: %v", fileID, uid, lookupErr))
			response.FailWithMessage("Failed to query file", c)
		}
		return
	}
	threadFile := *threadFilePtr

	// Get file path and thread ID for physical deletion and statistics update
	filePath := api.resolveWorkspaceFilePath(threadFile.FilePath)
	threadID := threadFile.ThreadID

	// Remove the physical file before the DB row. If the order is
	// reversed and os.Remove fails with EACCES / EBUSY / ENOENT-on-NFS
	// etc., the file is orphaned on disk with no DB record pointing at
	// it — silent storage leak, undiscoverable. Doing it this way means
	// a hard filesystem failure leaves the DB row intact, so the user
	// can retry the delete (which will now hit os.IsNotExist and pass).
	if filePath != "" {
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			// os.Remove errors include the absolute server path —
			// leak that string to the API caller and we hand them
			// the deployment's filesystem layout. Keep it server-side
			// only. The user-facing message stays generic.
			globals.Warn(fmt.Sprintf("Failed to delete physical file %s: %v", filePath, err))
			response.FailWithMessage("Failed to delete file", c)
			return
		}
	}

	// Delete the file record after the physical file is gone. Route
	// through FileService.RemoveFile so the ThreadFile delete and the
	// matching user_asset_ledger delete run in one transaction —
	// otherwise the ledger row orphans pointing at a file_id that no
	// longer exists, surfacing as a "ghost" entry on the asset panel
	// whose download / preview both 404. Same fix posture as 8bcfbb27
	// (the on-read auto-cleanup path); the explicit user-triggered
	// delete had the identical mismatch.
	if err := api.fileService.RemoveFile(int(uid), threadID, threadFile.Id); err != nil {
		globals.Error(fmt.Sprintf("[DeleteFile] DB delete failed for fileID=%d uid=%d: %v", fileID, uid, err))
		response.FailWithMessage("Failed to delete file", c)
		return
	}

	// Update thread statistics after file deletion
	if err := api.updateThreadStatistics(threadID); err != nil {
		globals.Error(fmt.Sprintf("Failed to update thread statistics after file delete: %v", err))
	}

	response.OkWithData(map[string]interface{}{
		"message": "File deleted successfully",
		"fileId":  fileID,
	}, c)
}

// SetDefaultFile API removed - Default functionality no longer needed

// saveUploadedFileWithConversion saves uploaded file to disk with XLS conversion support (BC47FD29 style)
func (api *AIChatApiNew) saveUploadedFileWithConversion(c *gin.Context, fileHeader *multipart.FileHeader, conversationId string, uid uint) (FileInfo, error) {
	// Validate conversationId parses as a numeric thread id (the
	// chat-side wire format). getChatThread accepts the string form
	// directly — gorm coerces in the WHERE — so we don't need the
	// int round-trip the previous shape did.
	if _, err := strconv.Atoi(conversationId); err != nil {
		return FileInfo{}, fmt.Errorf("invalid conversation Id: %s", conversationId)
	}

	// Cross-tenant guard + UUID lookup, both via the cached helper.
	// The previous shape did a raw First + manual `UID != int(uid)`
	// check — bypassing the ThreadCache and duplicating the IDOR
	// defence already embedded in getChatThread. Now uniform: first
	// upload pays a DB hit, subsequent uploads to the same thread
	// hit the 5-min TTL cache.
	chatThread, err := api.getChatThread(conversationId, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// Same shape the prior code returned for both miss and
			// uid-mismatch — caller surfaces as "permission denied"
			// in its own envelope. Don't differentiate here; the
			// sentinel is the IDOR-safe choice.
			return FileInfo{}, fmt.Errorf("permission denied: thread does not belong to caller")
		}
		return FileInfo{}, fmt.Errorf("failed to get thread info: %w", err)
	}

	// 检测原始文件类型和扩展名，确保文件名有合适的扩展名
	originalFileName := fileHeader.Filename
	detectedExt := strings.ToLower(filepath.Ext(originalFileName))

	// 如果文件名没有扩展名，记录警告
	if detectedExt == "" {
		contentType := fileHeader.Header.Get("Content-Type")
		globals.Warn(fmt.Sprintf("Cannot detect file type for: %s, Content-Type: %s", originalFileName, contentType))
	}

	// 使用原始文件名（清理后），不添加前缀
	// 因为文件已经按用户+线程隔离在 agent_workspace/uid/{uid}/{date}/thread_{uuid}/uploads/ 目录下
	cleanFileName := api.sanitizeFileName(originalFileName)
	newFileName := cleanFileName

	// 使用FileService处理文件上传和转换
	file, err := fileHeader.Open()
	if err != nil {
		return FileInfo{}, fmt.Errorf("failed to open uploaded file: %w", err)
	}
	defer file.Close()

	// 创建一个新的FileHeader副本，用新文件名
	newHeader := &multipart.FileHeader{
		Filename: newFileName,
		Header:   fileHeader.Header,
		Size:     fileHeader.Size,
	}

	// 创建工作目录并构建绝对文件路径
	agentManager := workagentService.GetAgentClientManager()
	if agentManager == nil {
		return FileInfo{}, fmt.Errorf("agent workspace manager not initialized")
	}

	threadWorkspace, err := agentManager.EnsureThreadWorkspace(int(uid), chatThread.UUID)
	if err != nil {
		return FileInfo{}, fmt.Errorf("failed to prepare agent workspace: %w", err)
	}

	uploadsDir := filepath.Join(threadWorkspace, "uploads")
	customPath := filepath.Join(uploadsDir, newFileName)

	// Defense in depth: even after sanitizeFileName, confirm the joined path
	// stays inside uploadsDir. Past sanitizers have shipped with subtle holes
	// (zero-width joiners, double-decoded sequences); this check catches any
	// future regression before it lets us write outside the user's workspace.
	absUploads, errAbsU := filepath.Abs(uploadsDir)
	absCustom, errAbsC := filepath.Abs(customPath)
	if errAbsU != nil || errAbsC != nil {
		return FileInfo{}, fmt.Errorf("failed to resolve upload path")
	}
	sep := string(filepath.Separator)
	if !strings.HasPrefix(absCustom+sep, absUploads+sep) {
		return FileInfo{}, fmt.Errorf("invalid file name")
	}

	// 使用FileService处理文件（包括自动转换）
	uploadedFile, err := api.fileService.ProcessUploadedFileWithCustomPath(file, newHeader, customPath, uid)
	if err != nil {
		return FileInfo{}, fmt.Errorf("failed to process uploaded file: %w", err)
	}

	// 使用原始文件名作为显示名称
	userDisplayName := fileHeader.Filename

	// Convert absolute path to relative path for database storage
	// Database should store: agent_workspace/uid/{uid}/{date}/thread_{uuid}/uploads/filename.xlsx
	workspaceRoot := agentManager.WorkspaceRoot()
	relPath, err := filepath.Rel(workspaceRoot, uploadedFile.Path)
	if err != nil {
		globals.Warn(fmt.Sprintf("Failed to get relative path, using absolute: %v", err))
		relPath = uploadedFile.Path
	}
	// Ensure forward slashes for cross-platform compatibility
	relPath = filepath.ToSlash(relPath)

	// Compute the file hash now that bytes are on disk. The hash flows
	// through ThreadFile.FileHash and is part of the agent_files_context
	// cache fingerprint, so an in-place re-upload busts the prompt cache
	// instead of letting the agent silently see the previous version.
	// Failures here are non-fatal (legacy rows lived without it for
	// years; the cache falls back to ModTime when hash is empty).
	fileHash, hashErr := workagentService.ComputeFileHash(uploadedFile.Path)
	if hashErr != nil {
		globals.Warn(fmt.Sprintf("Failed to hash uploaded file %s: %v", uploadedFile.Path, hashErr))
	}

	// 使用FileService保存文件记录
	threadFileRequest := workagentModel.ThreadFileRequest{
		UID:         int(uid),
		ThreadID:    chatThread.Id,
		FileName:    originalFileName, // 保存修正后的文件名（带扩展名，用于技术处理和判断是否转换）
		DisplayName: userDisplayName,  // 用户友好的显示名称
		FileSize:    uint64(uploadedFile.Size),
		FileType:    uploadedFile.Type,
		FilePath:    relPath, // 存储相对路径而不是绝对路径
		FileHash:    fileHash,
	}

	threadFileResponse, err := api.fileService.AddFile(threadFileRequest)
	if err != nil {
		// 删除物理文件
		os.Remove(uploadedFile.Path)
		return FileInfo{}, fmt.Errorf("failed to save file record: %w", err)
	}

	// 向量化功能已移除

	return FileInfo{
		Id:        strconv.FormatUint(uint64(threadFileResponse.Id), 10),
		Name:      threadFileResponse.DisplayName, // 返回数据库中存储的用户友好显示名称
		Size:      uploadedFile.Size,
		Type:      uploadedFile.Type,
		FilePath:  relPath, // 返回相对路径而不是绝对路径
		Converted: false,   // 不再进行转换
	}, nil
}

// getFileType determines file type based on filename
func getFileType(fileName string) string {
	fileName = strings.ToLower(fileName)
	if strings.HasSuffix(fileName, ".json") {
		return "json"
	} else if strings.HasSuffix(fileName, ".txt") {
		return "text"
	}
	return "unknown"
}

// sanitizeFileName 清理文件名，移除不安全字符并确保符合系统要求.
//
// Unicode normalization: NFC composition runs FIRST so two visually-
// identical inputs that differ only by NFC vs NFD encoding (common
// for CJK characters and Latin accents — e.g. "café" with U+00E9 vs
// "café") collapse to the same byte sequence before the regex
// + dedup downstream. Without this, a user re-uploading the same
// file from macOS (which often hands back NFD) and Linux (NFC)
// produces TWO distinct DB rows that look identical to the user but
// have different file_name columns — the upload-dedup branch in
// AddFile (which keys off uid + thread + file_name) misses the dup
// and we end up with an orphan duplicate row + double-charged disk.
func (api *AIChatApiNew) sanitizeFileName(fileName string) string {
	// 0. Normalize to NFC before anything else.
	fileName = norm.NFC.String(fileName)

	// 1. 分离文件名和扩展名
	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)

	// 2. 移除或替换不安全字符
	// 保留字母、数字、下划线、连字符、点和空格，其他字符替换为下划线
	reg := regexp.MustCompile(`[^a-zA-Z0-9\x{4e00}-\x{9fa5}._\- ]`)
	cleanBase := reg.ReplaceAllString(base, "_")

	// 3. 替换多个连续空格为单个下划线
	spaceReg := regexp.MustCompile(`\s+`)
	cleanBase = spaceReg.ReplaceAllString(cleanBase, "_")

	// 4. 移除开头和结尾的特殊字符
	cleanBase = strings.Trim(cleanBase, "._- ")

	// 5. 如果文件名为空，使用默认名称
	if cleanBase == "" {
		cleanBase = "file"
	}

	// 6. 确保扩展名是小写的
	ext = strings.ToLower(ext)

	return cleanBase + ext
}

// resolveWorkspaceFilePath returns the absolute on-disk path for a
// workspace-scoped file, or "" if the input would escape workspaceRoot.
// Callers must treat "" as "file not found" and avoid leaking why.
func (api *AIChatApiNew) resolveWorkspaceFilePath(path string) string {
	manager := workagentService.GetAgentClientManager()
	root := ""
	if manager != nil {
		root = manager.WorkspaceRoot()
	}
	if strings.TrimSpace(root) == "" {
		root = workspaceRoot()
	}
	return workagentService.ResolveInsideWorkspace(root, path)
}

func (api *AIChatApiNew) sanitizeFilePath(path string) string {
	return workagentService.SanitizePathForClient(path)
}

func (api *AIChatApiNew) sanitizeFileInfo(info FileInfo) FileInfo {
	info.FilePath = api.sanitizeFilePath(info.FilePath)
	info.DownloadURL = api.sanitizeFilePath(info.DownloadURL)
	info.PreviewURL = api.sanitizeFilePath(info.PreviewURL)
	return info
}

// SyncFiles syncs output files from filesystem with database
func (api *AIChatApiNew) SyncFiles(c *gin.Context) {
	var request struct {
		ThreadID string `json:"threadId" binding:"required"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&request); err != nil {
		response.FailWithMessage("threadId is required", c)
		return
	}

	uid := utils.GetUserID(c)

	// Parse thread ID
	threadIdUint, err := strconv.ParseUint(request.ThreadID, 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}

	// Get thread (with ownership check + cache benefit) via the
	// shared helper. The previous shape used a raw `Where("id=? AND
	// uid=?")` First — equivalent ownership semantics, but bypassed
	// the ThreadCache and the gorm.ErrRecordNotFound sentinel that
	// the rest of the package now consistently surfaces.
	thread, err := api.getChatThread(strconv.FormatUint(threadIdUint, 10), uid)
	if err != nil {
		response.FailWithMessage("Thread not found", c)
		return
	}

	// Get thread workspace
	manager := workagentService.GetAgentClientManager()
	threadWorkspace, err := manager.EnsureThreadWorkspace(thread.UID, thread.UUID)
	if err != nil {
		response.FailWithMessage("Failed to access workspace", c)
		return
	}

	// Sync output files
	result, err := api.fileService.SyncOutputFiles(int(uid), uint(threadIdUint), threadWorkspace, thread.UUID)
	if err != nil {
		globals.Error(fmt.Sprintf("[SyncFiles] Failed to sync files: %v", err))
		response.FailWithMessage("Failed to sync files", c)
		return
	}

	response.OkWithData(map[string]interface{}{
		"added":    result.Added,
		"updated":  result.Updated,
		"removed":  result.Removed,
		"duration": result.Duration,
		"errors":   result.Errors,
	}, c)
}
